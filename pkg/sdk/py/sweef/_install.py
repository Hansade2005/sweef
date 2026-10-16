"""Engine resolution and installation (standard library only).

Order: explicit ``binary``, ``$SWEEF_BIN``, ``sweef`` on PATH, the per-user cache, then an automatic
download from GitHub Releases into the cache (sha256-verified before anything is executed).
Nothing here modifies PATH, shell profiles or system locations. Importing this module has no side effects.
"""

import contextlib
import hashlib
import os
import platform as _platform
import re
import shutil
import sys
import sysconfig
import tarfile
import tempfile
import time
import zlib
import urllib.error
import urllib.parse
import urllib.request
import zipfile
from typing import Callable, Dict, Mapping, Optional

DEFAULT_VERSION = "0.1.0-alpha.4"
# sha256 of the release archives for DEFAULT_VERSION (pinned in this SDK release; other versions use SHA256SUMS).
PINNED_SHA256: Dict[str, str] = {
    "sweef_0.1.0-alpha.4_darwin_universal.zip": "78c1ffefe9a0145640fa332ae5f0ef1308be584848dc44f6ab65a097826f4b29",
    "sweef_0.1.0-alpha.4_linux_amd64.tar.gz": "c21d1ccbe04946136c559f2d23c6e7c6bcaea6e9444c36b449ca35d44d24b1da",
    "sweef_0.1.0-alpha.4_linux_arm64.tar.gz": "59687026ed6d74f9e1687dce9ed7e99d9b3915105dcb003b97ccde21fe5f1d4f",
    "sweef_0.1.0-alpha.4_windows_amd64.zip": "7e51c828a2e64813cbb144d0736010f4a4e9c1eabf860215db1b100fdc9a7ef8",
    "sweef_0.1.0-alpha.4_windows_arm64.zip": "33b7cc71e2429d91fbb525df0cc9ab3da31540104f60fba43846f48609b8de61",
}
DEFAULT_BASE = "https://github.com/sweefbrowser/sweef/releases/download"
LAUNCHER_MARKER = "sweef.cli"
MAX_ARCHIVE = 512 * 1024 * 1024
MANUAL = ("Install it manually from https://github.com/sweefbrowser/sweef/releases "
          "(or build it from a clone: `go build -o sweef ./cmd/sweef`), then put it on PATH, "
          "set SWEEF_BIN, or pass binary=.")


class SweefInstallError(RuntimeError):
    """The engine could not be found or downloaded; the message has manual install instructions."""


# ---- platform ------------------------------------------------------------------------------

class Target:
    def __init__(self, os_: str, arch: str, ext: str, exe: str, member: "re.Pattern[str]", asset: str):
        self.os, self.arch, self.ext, self.exe, self.member, self.asset = os_, arch, ext, exe, member, asset


def target(system: Optional[str] = None, machine: Optional[str] = None, version: Optional[str] = None) -> Target:
    """Map (platform.system(), platform.machine()) to the release asset and the member to extract."""
    system = (system or _platform.system()).lower()
    machine = (machine or _platform.machine()).lower()
    version = version or engine_version()
    arch = {"x86_64": "amd64", "amd64": "amd64", "arm64": "arm64", "aarch64": "arm64"}.get(machine)
    if system == "linux" and arch:
        os_, cpu, ext, exe, member = "linux", arch, "tar.gz", "sweef", r"^[^/]+/bin/sweef$"
    elif system == "darwin" and arch:
        os_, cpu, ext, exe, member = "darwin", "universal", "zip", "sweef", r"^Sweef\.app/Contents/MacOS/sweef$"
    elif system in ("windows", "win32") and arch:
        os_, cpu, ext, exe, member = "windows", arch, "zip", "sweef.exe", r"^[^/]+/sweef\.exe$"
    else:
        raise SweefInstallError(f"no prebuilt sweef engine for {system}/{machine}. {MANUAL}")
    return Target(os_, cpu, ext, exe, re.compile(member), f"sweef_{version}_{os_}_{cpu}.{ext}")


def engine_version() -> str:
    v = (os.environ.get("SWEEF_VERSION") or DEFAULT_VERSION)
    v = v[1:] if v.startswith("v") else v
    if not re.match(r"^[0-9A-Za-z][0-9A-Za-z.+_-]*$", v) or ".." in v:
        raise SweefInstallError(f"invalid SWEEF_VERSION: {v!r}")
    return v


def cache_root(env: Optional[Mapping[str, str]] = None, system: Optional[str] = None, home: Optional[str] = None) -> str:
    env = os.environ if env is None else env
    system = (system or _platform.system()).lower()
    home = home or os.path.expanduser("~")
    if env.get("SWEEF_ENGINE_CACHE"):
        return env["SWEEF_ENGINE_CACHE"]
    if system in ("windows", "win32"):
        return "\\".join([env.get("LOCALAPPDATA") or home + "\\AppData\\Local", "sweef", "cache"])
    if system == "darwin":
        return home + "/Library/Caches/sweef"
    return (env.get("XDG_CACHE_HOME") or home + "/.cache") + "/sweef"


def cached_binary_path(version: Optional[str] = None) -> str:
    version = version or engine_version()
    return os.path.join(cache_root(), version, target(version=version).exe)


# ---- lookup (no network) --------------------------------------------------------------------

def _is_exec(p: str) -> bool:
    return os.path.isfile(p) and os.access(p, os.X_OK)


def is_own_launcher(p: str) -> bool:
    """True for the ``sweef`` console script this package installs (it would otherwise shadow the engine)."""
    try:
        real = os.path.realpath(p)
        if sys.platform == "win32":
            scripts = sysconfig.get_path("scripts")
            return bool(scripts) and os.path.normcase(os.path.dirname(real)) == os.path.normcase(scripts)
        if os.path.getsize(real) > 16384:
            return False
        with open(real, "rb") as f:
            return LAUNCHER_MARKER.encode() in f.read()
    except OSError:
        return False


def skip_download() -> bool:
    v = os.environ.get("SWEEF_SKIP_DOWNLOAD", "")
    return bool(v) and v != "0" and v.lower() != "false"


def lookup_binary(binary: Optional[str] = None) -> Optional[str]:
    """Find an existing engine without downloading; None when absent."""
    exe = binary or os.environ.get("SWEEF_BIN")
    if exe:
        return exe
    name = "sweef.exe" if sys.platform == "win32" else "sweef"
    for d in os.environ.get("PATH", "").split(os.pathsep):
        if d:
            p = os.path.join(d, name)
            if _is_exec(p) and not is_own_launcher(p):
                return p
    try:
        c = cached_binary_path()
    except SweefInstallError:
        return None
    return c if _is_exec(c) else None


# ---- archives ------------------------------------------------------------------------------

def _safe(name: str) -> str:
    n = name.replace("\\", "/")
    if n.startswith("/") or re.match(r"^[A-Za-z]:", n) or ".." in n.split("/") or "\0" in n:
        raise SweefInstallError(f"unsafe path in archive: {name!r}")
    return n


def extract_member(archive: str, ext: str, wanted: "re.Pattern[str]") -> bytes:
    """Bytes of the single archive member matching ``wanted``. Every member name is validated (no absolute
    paths, no ``..``) and nothing is ever written under an archive-chosen name."""
    found: Optional[bytes] = None
    try:
        if ext == "zip":
            with zipfile.ZipFile(archive) as z:
                for info in z.infolist():
                    name = _safe(info.filename)
                    if name.endswith("/") or not wanted.search(name):
                        continue
                    if found is not None:
                        raise SweefInstallError(f"archive has more than one {wanted.pattern}")
                    if info.file_size > 1 << 30:
                        raise SweefInstallError("archive member too large")
                    found = z.read(info)
        else:
            with tarfile.open(archive, "r:gz") as t:
                for m in t:
                    name = _safe(m.name)
                    if not m.isreg() or not wanted.search(name):
                        continue
                    if found is not None:
                        raise SweefInstallError(f"archive has more than one {wanted.pattern}")
                    if m.size > 1 << 30:
                        raise SweefInstallError("archive member too large")
                    f = t.extractfile(m)
                    found = f.read() if f else None
    except (tarfile.TarError, zipfile.BadZipFile, EOFError, OSError, zlib.error) as e:
        raise SweefInstallError(f"corrupt archive: {e}")
    if not found:
        raise SweefInstallError(f"archive does not contain the sweef engine ({wanted.pattern})")
    return found


# ---- http ----------------------------------------------------------------------------------

class _NoDowngrade(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):  # type: ignore[no-untyped-def]
        if urllib.parse.urlsplit(req.full_url).scheme == "https" and urllib.parse.urlsplit(newurl).scheme != "https":
            raise SweefInstallError("refusing https to http redirect")
        return super().redirect_request(req, fp, code, msg, headers, newurl)


def _opener(url: str) -> urllib.request.OpenerDirector:
    host = urllib.parse.urlsplit(url).hostname or ""
    handlers = [_NoDowngrade()]
    if host in ("localhost", "127.0.0.1", "::1"):
        handlers.append(urllib.request.ProxyHandler({}))  # never proxy loopback mirrors
    return urllib.request.build_opener(*handlers)  # default ProxyHandler honours HTTPS_PROXY/HTTP_PROXY/NO_PROXY


def _open(url: str, timeout: float = 60.0):  # type: ignore[no-untyped-def]
    if urllib.parse.urlsplit(url).scheme not in ("http", "https"):
        raise SweefInstallError(f"unsupported URL scheme: {url}")
    req = urllib.request.Request(url, headers={"User-Agent": "sweef-sdk-python"})
    try:
        return _opener(url).open(req, timeout=timeout)
    except urllib.error.HTTPError as e:
        raise SweefInstallError(f"GET {url}: HTTP {e.code}")


def _fetch_text(url: str) -> str:
    with contextlib.closing(_open(url)) as r:
        data = r.read((1 << 20) + 1)
    if len(data) > 1 << 20:
        raise SweefInstallError("response too large")
    return data.decode("utf-8", "replace")


def _fetch_file(url: str, dest: str) -> str:
    h = hashlib.sha256()
    n = 0
    with contextlib.closing(_open(url)) as r, open(dest, "wb") as out:
        while True:
            chunk = r.read(1 << 16)
            if not chunk:
                break
            n += len(chunk)
            if n > MAX_ARCHIVE:
                raise SweefInstallError("download too large")
            h.update(chunk)
            out.write(chunk)
    return h.hexdigest()


# ---- lock ----------------------------------------------------------------------------------

def _pid_alive(pid: int) -> bool:
    if pid <= 0:
        return False
    if sys.platform == "win32":
        return True  # rely on the age limit
    try:
        os.kill(pid, 0)
    except PermissionError:
        return True
    except OSError:
        return False
    return True


def _with_lock(directory: str, ready: Callable[[], Optional[str]], fn: Callable[[], str],
               stale: float = 600.0, wait: float = 720.0) -> str:
    lock = os.path.join(directory, ".lock")
    deadline = time.time() + wait
    while True:
        try:
            fd = os.open(lock, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
            with os.fdopen(fd, "w") as f:
                f.write(f"{os.getpid()} {time.time()}\n")
            break
        except FileExistsError:
            pass
        r = ready()
        if r:
            return r
        try:
            with open(lock) as f:
                parts = f.read().split()
            pid, ts = int(parts[0]), float(parts[1])
            if time.time() - ts > stale or not _pid_alive(pid):
                os.remove(lock)
                continue
        except (OSError, ValueError, IndexError):
            pass
        if time.time() > deadline:
            raise SweefInstallError(f"timed out waiting for another sweef install ({lock})")
        time.sleep(0.1)
    try:
        return ready() or fn()
    finally:
        with contextlib.suppress(OSError):
            os.remove(lock)


# ---- install -------------------------------------------------------------------------------

def parse_sums(text: str) -> Dict[str, str]:
    out: Dict[str, str] = {}
    for line in text.splitlines():
        m = re.match(r"^([0-9a-fA-F]{64}) [ *]?(\S.*)$", line.strip())
        if m:
            out[m.group(2)] = m.group(1).lower()
    return out


def download_engine(version: Optional[str] = None, base: Optional[str] = None, cache_dir: Optional[str] = None,
                    pins: Optional[Mapping[str, str]] = None, log: Optional[Callable[[str], None]] = None) -> str:
    """Fetch, verify and install the engine into the cache; returns its path. Arguments override the
    environment (for tests and mirrors)."""
    version = version or engine_version()
    t = target(version=version)
    directory = os.path.join(cache_dir or cache_root(), version)
    dest = os.path.join(directory, t.exe)

    def ready() -> Optional[str]:
        return dest if _is_exec(dest) else None

    if ready():
        return dest
    os.makedirs(directory, mode=0o755, exist_ok=True)

    def work() -> str:
        rel = (base or os.environ.get("SWEEF_DOWNLOAD_BASE") or DEFAULT_BASE).rstrip("/") + f"/v{version}"
        (log or (lambda m: print(m, file=sys.stderr)))(f"sweef: downloading engine {version} ({t.os}/{t.arch}) to {directory}")
        pinned = PINNED_SHA256 if pins is None else pins
        want = pinned.get(t.asset)
        fd, tmp = tempfile.mkstemp(prefix=".dl-", dir=directory)
        os.close(fd)
        out = tmp + ".bin"
        try:
            if not want:
                want = parse_sums(_fetch_text(rel + "/SHA256SUMS")).get(t.asset)
                if not want:
                    raise SweefInstallError(f"{t.asset} is not listed in {rel}/SHA256SUMS")
            got = _fetch_file(f"{rel}/{t.asset}", tmp)
            if got != want.lower():
                raise SweefInstallError(f"checksum mismatch for {t.asset}: expected {want}, got {got}; refusing to install")
            data = extract_member(tmp, t.ext, t.member)
            fd = os.open(out, os.O_CREAT | os.O_WRONLY | os.O_TRUNC, 0o755)
            with os.fdopen(fd, "wb") as f:
                f.write(data)
            os.chmod(out, 0o755)
            os.replace(out, dest)
            return dest
        finally:
            for p in (tmp, out):
                with contextlib.suppress(OSError):
                    os.remove(p)

    return _with_lock(directory, ready, work)


def ensure_binary(binary: Optional[str] = None, **kw: object) -> str:
    """Resolve the engine path: ``binary``, ``$SWEEF_BIN``, ``sweef`` on PATH, the per-user cache, else download
    it (sha256-verified) into the cache. ``SWEEF_SKIP_DOWNLOAD=1`` disables the download."""
    found = lookup_binary(binary)
    if found:
        return found
    if skip_download():
        raise FileNotFoundError("sweef binary not found: set SWEEF_BIN, put sweef on PATH, or pass binary= "
                                "(automatic download disabled by SWEEF_SKIP_DOWNLOAD). " + MANUAL)
    try:
        return download_engine(**kw)  # type: ignore[arg-type]
    except SweefInstallError as e:
        if "no prebuilt" in str(e):
            raise
        raise _download_error(e)
    except OSError as e:
        raise _download_error(e)


def _download_error(e: BaseException) -> SweefInstallError:
    err = SweefInstallError(f"could not download the sweef engine automatically: {e}. {MANUAL} "
                            "(Behind a proxy? set HTTPS_PROXY. Mirror? set SWEEF_DOWNLOAD_BASE. "
                            "Disable auto-download: SWEEF_SKIP_DOWNLOAD=1.)")
    err.__cause__ = e
    return err
