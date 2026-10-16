"""Offline tests for engine resolution/download.

The HTTP server below is a LOCAL FAKE RELEASE (a stub ``sweef`` script in a tarball/zip plus SHA256SUMS), used
only so CI needs no network. The real-release check is a separate manual step.
Run: python -m unittest discover -s tests
"""

import hashlib
import io
import os
import shutil
import subprocess
import sys
import tarfile
import tempfile
import threading
import unittest
import zipfile
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))
from sweef import _install as inst  # noqa: E402

STUB = b'#!/bin/sh\necho "sweef stub 9.9.9"\n'
VER = "9.9.9"
ENV_KEYS = ["PATH", "SWEEF_BIN", "SWEEF_VERSION", "SWEEF_DOWNLOAD_BASE", "SWEEF_SKIP_DOWNLOAD", "SWEEF_ENGINE_CACHE",
            "HTTP_PROXY", "http_proxy"]
POSIX = unittest.skipIf(sys.platform == "win32", "stub is a shell script")


def tar_of(entries):
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w:gz") as t:
        for name, data, *typ in entries:
            ti = tarfile.TarInfo(name)
            ti.size = len(data)
            ti.mode = 0o755
            if typ:
                ti.type = typ[0]
            t.addfile(ti, io.BytesIO(data))
    return buf.getvalue()


def zip_of(entries, compression=zipfile.ZIP_DEFLATED):
    buf = io.BytesIO()
    with zipfile.ZipFile(buf, "w", compression) as z:
        for name, data in entries:
            z.writestr(name, data)
    return buf.getvalue()


def release(version, tamper=False):
    t = inst.target(version=version)
    top = f"sweef-{version}-{t.os}-{t.arch}"
    if t.ext == "zip":
        arc = zip_of([("Sweef.app/Contents/MacOS/sweef" if t.os == "darwin" else f"{top}/sweef.exe", STUB)])
    else:
        arc = tar_of([(f"{top}/NOTICE", b"n"), (f"{top}/bin/sweef", STUB)])
    h = hashlib.sha256(arc).hexdigest()
    sums = f"{h}  {t.asset}\n{'0' * 64}  other.zip\n"
    return t, (arc + b"\0" if tamper else arc), sums, h


class Fake:
    def __init__(self, t, archive, sums):
        self.hits = {"archive": 0, "sums": 0}
        hits = self.hits

        class H(BaseHTTPRequestHandler):
            def log_message(self, *a):
                pass

            def do_GET(self):
                if self.path == f"/v{VER}/SHA256SUMS":
                    hits["sums"] += 1
                    body = sums.encode()
                elif self.path == f"/v{VER}/{t.asset}":
                    hits["archive"] += 1
                    body = archive
                else:
                    self.send_response(404)
                    self.end_headers()
                    return
                self.send_response(200)
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

        self.srv = ThreadingHTTPServer(("127.0.0.1", 0), H)
        self.base = f"http://127.0.0.1:{self.srv.server_address[1]}"
        threading.Thread(target=self.srv.serve_forever, daemon=True).start()

    def close(self):
        self.srv.shutdown()
        self.srv.server_close()


class Hermetic(unittest.TestCase):
    def setUp(self):
        self.saved = {k: os.environ.get(k) for k in ENV_KEYS}
        self.tmp = tempfile.mkdtemp(prefix="sweef-inst-")
        self.empty = os.path.join(self.tmp, "emptybin")
        os.mkdir(self.empty)
        for k in ENV_KEYS:
            os.environ.pop(k, None)
        os.environ.update(PATH=self.empty, SWEEF_ENGINE_CACHE=os.path.join(self.tmp, "cache"), SWEEF_VERSION=VER)
        self.fakes = []

    def tearDown(self):
        for f in self.fakes:
            f.close()
        for k, v in self.saved.items():
            if v is None:
                os.environ.pop(k, None)
            else:
                os.environ[k] = v
        shutil.rmtree(self.tmp, ignore_errors=True)

    def serve(self, **kw):
        t, arc, sums, h = release(VER, **kw)
        f = Fake(t, arc, sums)
        self.fakes.append(f)
        return t, f, h

    quiet = staticmethod(lambda m: None)


class TestDownload(Hermetic):
    @POSIX
    def test_success_verifies_installs_and_runs(self):
        t, f, _ = self.serve()
        logs = []
        p = inst.ensure_binary(base=f.base, log=logs.append)
        self.assertEqual(p, os.path.join(os.environ["SWEEF_ENGINE_CACHE"], VER, t.exe))
        self.assertEqual(subprocess.check_output([p, "--version"]).decode().strip(), "sweef stub 9.9.9")
        self.assertTrue(os.access(p, os.X_OK))
        self.assertEqual(len(logs), 1)
        self.assertIn("downloading engine 9.9.9", logs[0])
        self.assertEqual(os.listdir(os.path.dirname(p)), [t.exe], "no temp or lock files left")

    @POSIX
    def test_env_base_and_cache_hit(self):
        _, f, _ = self.serve()
        os.environ["SWEEF_DOWNLOAD_BASE"] = f.base + "/"
        a = inst.ensure_binary(log=self.quiet)
        b = inst.ensure_binary(log=self.quiet)
        self.assertEqual(a, b)
        self.assertEqual(f.hits, {"archive": 1, "sums": 1})

    @POSIX
    def test_checksum_mismatch_rejected(self):
        _, f, _ = self.serve(tamper=True)
        with self.assertRaisesRegex(inst.SweefInstallError, "checksum mismatch"):
            inst.ensure_binary(base=f.base, log=self.quiet)
        self.assertEqual(os.listdir(os.path.join(os.environ["SWEEF_ENGINE_CACHE"], VER)), [])

    @POSIX
    def test_pin_wins_over_sums(self):
        t, f, h = self.serve()
        with self.assertRaisesRegex(inst.SweefInstallError, "checksum mismatch"):
            inst.ensure_binary(base=f.base, pins={t.asset: "f" * 64}, log=self.quiet)
        self.assertEqual(f.hits["sums"], 0)
        self.assertTrue(os.path.exists(inst.ensure_binary(base=f.base, pins={t.asset: h}, log=self.quiet)))

    @POSIX
    def test_missing_asset_and_http_error(self):
        t, f, _ = self.serve()
        f.close()
        t2, arc, _, _ = release(VER)
        f2 = Fake(t2, arc, f"{'0' * 64}  other.zip\n")
        self.fakes.append(f2)
        with self.assertRaisesRegex(inst.SweefInstallError, "not listed in .*SHA256SUMS"):
            inst.ensure_binary(base=f2.base, log=self.quiet)
        os.environ["SWEEF_VERSION"] = "1.0.0"
        with self.assertRaisesRegex(inst.SweefInstallError, "could not download.*HTTP 404.*SWEEF_BIN"):
            inst.ensure_binary(base=f2.base, log=self.quiet)

    def test_offline_error_is_actionable(self):
        with self.assertRaisesRegex(inst.SweefInstallError, "could not download.*HTTPS_PROXY.*SWEEF_SKIP_DOWNLOAD"):
            inst.ensure_binary(base="http://127.0.0.1:9", log=self.quiet)

    def test_skip_download(self):
        _, f, _ = self.serve()
        os.environ["SWEEF_SKIP_DOWNLOAD"] = "1"
        with self.assertRaisesRegex(FileNotFoundError, "sweef binary not found: set SWEEF_BIN, put sweef on PATH"):
            inst.ensure_binary(base=f.base, log=self.quiet)
        self.assertEqual(f.hits, {"archive": 0, "sums": 0})
        self.assertFalse(os.path.exists(os.environ["SWEEF_ENGINE_CACHE"]))
        os.environ["SWEEF_SKIP_DOWNLOAD"] = "0"
        self.assertIsNone(inst.lookup_binary())

    @POSIX
    def test_sdk_launch_path_uses_ensure(self):
        import sweef
        _, f, _ = self.serve()
        os.environ["SWEEF_DOWNLOAD_BASE"] = f.base
        p = sweef._find_binary(None)  # same entry point Sweef.launch/session use
        self.assertTrue(p.endswith("sweef"))
        self.assertEqual(sweef._find_binary("/x/y"), "/x/y")

    @POSIX
    def test_concurrent_first_runs_download_once(self):
        _, f, _ = self.serve()
        code = ("import sys; sys.path.insert(0, %r); from sweef import _install as i; "
                "print(i.ensure_binary(log=lambda m: None))" % os.path.join(os.path.dirname(__file__), ".."))
        env = dict(os.environ, SWEEF_DOWNLOAD_BASE=f.base)
        procs = [subprocess.Popen([sys.executable, "-I", "-c", code], env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
                 for _ in range(5)]
        outs = []
        for p in procs:
            o, e = p.communicate(timeout=60)
            self.assertEqual(p.returncode, 0, e.decode())
            outs.append(o.decode().strip())
        self.assertEqual(len(set(outs)), 1)
        self.assertEqual(f.hits["archive"], 1)

    @POSIX
    def test_stale_lock_is_broken(self):
        _, f, _ = self.serve()
        d = os.path.join(os.environ["SWEEF_ENGINE_CACHE"], VER)
        os.makedirs(d)
        with open(os.path.join(d, ".lock"), "w") as fh:
            fh.write("2147483646 0\n")
        self.assertTrue(os.path.exists(inst.ensure_binary(base=f.base, log=self.quiet)))


class TestResolution(Hermetic):
    @POSIX
    def test_precedence(self):
        def mk(d, name="sweef"):
            os.makedirs(d, exist_ok=True)
            p = os.path.join(d, name)
            with open(p, "wb") as fh:
                fh.write(STUB)
            os.chmod(p, 0o755)
            return p

        cached = mk(os.path.join(os.environ["SWEEF_ENGINE_CACHE"], VER), inst.target(version=VER).exe)
        self.assertEqual(inst.lookup_binary(), cached)
        on_path = mk(os.path.join(self.tmp, "pathbin"))
        os.environ["PATH"] = os.pathsep.join([self.empty, os.path.join(self.tmp, "pathbin")])
        self.assertEqual(inst.lookup_binary(), on_path)
        os.environ["SWEEF_BIN"] = "/env/sweef"
        self.assertEqual(inst.lookup_binary(), "/env/sweef")
        self.assertEqual(inst.ensure_binary("/explicit/sweef"), "/explicit/sweef")
        self.assertEqual(inst.ensure_binary(), "/env/sweef")

    @POSIX
    def test_own_console_script_is_skipped(self):
        d = os.path.join(self.tmp, "scripts")
        os.mkdir(d)
        p = os.path.join(d, "sweef")
        with open(p, "w") as fh:
            fh.write("#!/usr/bin/python\nfrom sweef.cli import main\nmain()\n")
        os.chmod(p, 0o755)
        os.environ["PATH"] = d
        self.assertIsNone(inst.lookup_binary())

    def test_platform_mapping(self):
        rows = [("Linux", "x86_64", "sweef_1.2.3_linux_amd64.tar.gz", "sweef"),
                ("Linux", "aarch64", "sweef_1.2.3_linux_arm64.tar.gz", "sweef"),
                ("Darwin", "x86_64", "sweef_1.2.3_darwin_universal.zip", "sweef"),
                ("Darwin", "arm64", "sweef_1.2.3_darwin_universal.zip", "sweef"),
                ("Windows", "AMD64", "sweef_1.2.3_windows_amd64.zip", "sweef.exe"),
                ("Windows", "ARM64", "sweef_1.2.3_windows_arm64.zip", "sweef.exe")]
        for s, m, asset, exe in rows:
            t = inst.target(s, m, "1.2.3")
            self.assertEqual((t.asset, t.exe), (asset, exe))
        self.assertTrue(inst.target("Linux", "x86_64", "1.2.3").member.search("sweef-1.2.3-linux-amd64/bin/sweef"))
        self.assertFalse(inst.target("Linux", "x86_64", "1.2.3").member.search("sweef-1.2.3-linux-amd64/bin/sweef-browser"))
        self.assertTrue(inst.target("Darwin", "arm64", "1.2.3").member.search("Sweef.app/Contents/MacOS/sweef"))
        self.assertTrue(inst.target("Windows", "AMD64", "1.2.3").member.search("sweef-1.2.3-windows-amd64/sweef.exe"))
        for s, m in [("Linux", "i686"), ("FreeBSD", "x86_64"), ("Linux", "armv7l"), ("Windows", "x86")]:
            with self.assertRaisesRegex(inst.SweefInstallError, "no prebuilt sweef engine"):
                inst.target(s, m, "1.2.3")

    def test_cache_locations(self):
        self.assertEqual(inst.cache_root({"XDG_CACHE_HOME": "/x"}, "Linux", "/h"), "/x/sweef")
        self.assertEqual(inst.cache_root({}, "Linux", "/h"), "/h/.cache/sweef")
        self.assertEqual(inst.cache_root({}, "Darwin", "/Users/u"), "/Users/u/Library/Caches/sweef")
        self.assertEqual(inst.cache_root({"LOCALAPPDATA": "C:\\L"}, "Windows", "C:\\U"), "C:\\L\\sweef\\cache")
        self.assertEqual(inst.cache_root({"SWEEF_ENGINE_CACHE": "/c"}, "Linux", "/h"), "/c")

    def test_version_validation(self):
        os.environ["SWEEF_VERSION"] = "v1.2.3-rc.1"
        self.assertEqual(inst.engine_version(), "1.2.3-rc.1")
        for bad in ["../x", "a/b", "1 2", ".."]:
            os.environ["SWEEF_VERSION"] = bad
            with self.assertRaises(inst.SweefInstallError):
                inst.engine_version()

    def test_import_is_side_effect_free(self):
        r = subprocess.run([sys.executable, "-I", "-c",
                            "import sys; sys.path.insert(0, %r); import sweef, os; print(os.path.exists(%r))"
                            % (os.path.join(os.path.dirname(__file__), ".."), os.environ["SWEEF_ENGINE_CACHE"])],
                           capture_output=True, text=True, env=dict(os.environ))
        self.assertEqual((r.returncode, r.stdout.strip(), r.stderr), (0, "False", ""))


class TestArchives(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp(prefix="sweef-x-")

    def tearDown(self):
        shutil.rmtree(self.tmp, ignore_errors=True)

    def w(self, name, data):
        p = os.path.join(self.tmp, name)
        with open(p, "wb") as fh:
            fh.write(data)
        return p

    def test_happy_paths(self):
        import re
        d = b"payload"
        self.assertEqual(inst.extract_member(self.w("a", tar_of([("top/bin/sweef", d)])), "tar.gz", re.compile(r"^[^/]+/bin/sweef$")), d)
        self.assertEqual(inst.extract_member(self.w("b", zip_of([("top/sweef.exe", d)])), "zip", re.compile(r"^[^/]+/sweef\.exe$")), d)
        self.assertEqual(inst.extract_member(self.w("c", zip_of([("top/sweef.exe", d)], zipfile.ZIP_STORED)), "zip", re.compile(r"sweef\.exe$")), d)
        with self.assertRaisesRegex(inst.SweefInstallError, "does not contain"):
            inst.extract_member(self.w("a", tar_of([("top/bin/sweef", d)])), "tar.gz", re.compile("nothing"))
        with self.assertRaisesRegex(inst.SweefInstallError, "corrupt"):
            inst.extract_member(self.w("bad", b"not an archive"), "tar.gz", re.compile("x"))

    def test_traversal_and_duplicates_rejected(self):
        import re
        d = b"x"
        pat = re.compile(r"^[^/]+/(bin/sweef|sweef\.exe)$")
        cases = [("t1", "tar.gz", tar_of([("top/bin/sweef", d), ("../evil", d)])),
                 ("t2", "tar.gz", tar_of([("/abs/evil", d), ("top/bin/sweef", d)])),
                 ("t3", "tar.gz", tar_of([("a/../../evil", d)])),
                 ("z1", "zip", zip_of([("top/sweef.exe", d), ("../../evil", d)])),
                 ("z2", "zip", zip_of([("C:\\evil.exe", d)])),
                 ("dup", "tar.gz", tar_of([("a/bin/sweef", d), ("b/bin/sweef", d)]))]
        for n, ext, buf in cases:
            with self.assertRaisesRegex(inst.SweefInstallError, "unsafe path|more than one", msg=n):
                inst.extract_member(self.w(n, buf), ext, pat)
        self.assertFalse(os.path.exists(os.path.join(self.tmp, "..", "evil")))


if __name__ == "__main__":
    unittest.main()
