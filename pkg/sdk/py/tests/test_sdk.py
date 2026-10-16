"""Real-daemon tests: a local HTTP fixture and a real ``sweef serve`` (no mocks).

SWEEF_BIN must point at a sweef binary. Run: python -m unittest discover -s tests
"""

import json
import os
import shutil
import subprocess
import sys
import tempfile
import threading
import time
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))
from sweef import RpcError, Sweef  # noqa: E402

PAGES = {
    "/": """<!doctype html><title>Acme</title><nav><a href="/login">Sign in</a></nav><h1>Ship faster</h1>
<button id="cta" onclick="document.getElementById('out').textContent='clicked'">Start</button><p id="out"></p>
<button id="ask" onclick="document.title = confirm('sure?') ? 'yes' : 'no'">Ask</button>
<script>console.log('ready')</script>""",
    "/login": """<!doctype html><title>Sign in</title><form action="/welcome" method="get">
<label for="email">Email</label><input id="email" name="email"><input id="keys">
<label><input type="checkbox" id="remember"> Remember me</label>
<select id="plan"><option value="free">Free</option><option value="pro">Pro</option></select>
<button type="submit">Sign in</button></form>""",
    "/regress": """<!doctype html><title>
    Tester   regressions
</title><body style="margin:0"><form id="checkboxes"><input type="checkbox" id="c1"> one<br><input type="checkbox" id="c2" checked> two</form>
<input id="user" style="position:absolute;left:10px;top:100px;width:200px;height:30px"><input id="pass" style="position:absolute;left:10px;top:150px;width:200px;height:30px">
<select id="dropdown" style="position:absolute;left:10px;top:200px"><option value="" disabled selected>Please select an option</option><option value="1">Option 1</option><option value="2">Option 2</option></select>
<script>
var dd = document.getElementById('dropdown');
dd.onchange = function (e) {
  var o = dd.getElementsByTagName('option');
  for (var i = 0; i < o.length; i++) o[i].removeAttribute('selected');
  document.querySelector("#dropdown option[value='" + e.target.value + "']").setAttribute('selected', 'selected');
};
</script>""",
}


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def do_GET(self):
        u = urlparse(self.path)
        if u.path == "/api":
            body, ctype = b'{"real":true}', "application/json"
        elif u.path == "/cookie":
            self.send_response(200)
            self.send_header("Set-Cookie", "sid=abc; Path=/")
            self.send_header("Content-Type", "text/html")
            self.end_headers()
            self.wfile.write(b"<title>c</title>ok")
            return
        elif u.path == "/welcome":
            body, ctype = ("<title>Welcome</title><h1>Welcome %s</h1>" % parse_qs(u.query).get("email", [""])[0]).encode(), "text/html"
        elif u.path in PAGES:
            body, ctype = PAGES[u.path].encode(), "text/html; charset=utf-8"
        else:
            self.send_response(404)
            self.end_headers()
            return
        self.send_response(200)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


class SDKTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.httpd = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        threading.Thread(target=cls.httpd.serve_forever, daemon=True).start()
        cls.base = "http://127.0.0.1:%d" % cls.httpd.server_address[1]
        cls.sw = Sweef.launch()

    @classmethod
    def tearDownClass(cls):
        cls.sw.close()
        cls.httpd.shutdown()

    def test_navigate_snapshot_click(self):
        page = self.sw.new_page()
        self.assertEqual(page.goto(self.base + "/")["title"], "Acme")
        self.assertEqual(page.title(), "Acme")
        self.assertEqual(page.url(), self.base + "/")
        snap = page.snapshot()
        import re
        m = re.search(r'button "Start" \[ref=(e\d+)\]', snap)
        self.assertIsNotNone(m, snap)
        page.click(m.group(1))
        self.assertEqual(page.locator("#out").text_content(), "clicked")
        self.assertIn("clicked", page.snapshot(delta=True))
        self.assertTrue(any(e["data"]["text"] == "ready" for e in page.console_messages()))
        page.close()

    def test_tester_regressions(self):
        # failures an external tester hit on real sites, reproduced offline
        page = self.sw.new_page(self.base + "/regress")
        self.assertEqual(page.title(), "Tester regressions")  # Page.title strips and collapses like document.title
        boxes = page.locator("input[type=checkbox]")
        self.assertEqual(boxes.count(), 2)
        self.assertEqual(boxes.first.count(), 1)  # first/last/nth build "sel >> nth=N"
        boxes.first.check()
        self.assertTrue(page.locator("#c1").is_checked())
        self.assertTrue(boxes.last.is_checked())
        self.assertEqual(boxes.nth(1).get_attribute("id"), "c2")
        self.assertEqual(page.locator("#checkboxes").locator("input").nth(-1).get_attribute("id"), "c2")
        page.click("#user")  # click, then type with no selector
        page.keyboard.type("tomsmith")
        page.keyboard.press("Tab")
        page.keyboard.type("secret")
        page.mouse.click(50, 115)
        page.keyboard.type("!")
        self.assertEqual(page.evaluate("document.querySelector('#user').value + '|' + document.querySelector('#pass').value"), "tomsmith!|secret")
        page.select_option("#dropdown", "2")  # the change handler rewrites option[selected]
        self.assertEqual(page.evaluate("(() => { const s = document.querySelector('#dropdown'); return s.value + '|' + s.selectedIndex })()"), "2|2")
        page.select_option("#dropdown", "Option 1")
        self.assertEqual(page.evaluate("document.querySelector('#dropdown').value"), "1")
        page.close()

    def test_forms_keyboard_locators(self):
        page = self.sw.new_page(self.base + "/login")
        page.get_by_label("Email").fill("ada@example.com")
        self.assertEqual(page.locator("#email").input_value(), "ada@example.com")
        page.check("#remember")
        self.assertTrue(page.locator("#remember").is_checked())
        page.select_option("#plan", "Pro")
        page.focus("#keys")
        page.keyboard.type("hi")
        self.assertEqual(page.evaluate("document.querySelector('#keys').value"), "hi")
        self.assertEqual(page.evaluate("s => document.querySelector(s).value", "#plan"), "pro")
        self.assertEqual(page.get_by_role("button", name="Sign in").count(), 1)
        page.press("#email", "Enter")
        page.wait_for_url("**/welcome**", timeout=5000)
        self.assertEqual(page.text_content("h1"), "Welcome ada@example.com")
        page.go_back()
        self.assertEqual(page.title(), "Sign in")
        page.close()

    def test_screenshot_png(self):
        page = self.sw.new_page(self.base + "/")
        page.set_viewport_size(800, 600)
        png = page.screenshot()
        self.assertEqual(png[1:4], b"PNG")
        self.assertEqual(int.from_bytes(png[16:20], "big"), 800)
        self.assertEqual(int.from_bytes(png[20:24], "big"), 600)
        with tempfile.TemporaryDirectory() as d:
            p = os.path.join(d, "full.png")
            page.screenshot(path=p, full_page=True)
            self.assertGreater(os.path.getsize(p), 100)
        self.assertIn("Ship faster", page.screenshot_text())
        page.close()

    def test_batch_routes_dialogs_waits(self):
        page = self.sw.new_page()
        r = page.batch(["open %s/login" % self.base, "fill #email bo@x.io", {"cmd": "click", "args": ["button[type=submit]"]},
                        "expect url /welcome", {"method": "Page.title"}], observe="interactive")
        self.assertTrue(r["ok"], r["steps"])
        self.assertEqual(r["steps"][4]["result"]["title"], "Welcome")
        page.route("**/api", json={"mocked": 1})
        self.assertEqual(page.evaluate("fetch('%s/api').then(r => r.json())" % self.base), {"mocked": 1})
        self.assertEqual(len(page.routes()), 1)
        page.unroute_all()
        self.assertEqual(page.evaluate("fetch('%s/api').then(r => r.json())" % self.base), {"real": True})
        page.goto(self.base + "/")
        page.arm_dialog("accept")
        page.click("#ask")
        self.assertEqual(page.title(), "yes")
        t0 = time.time()
        page.wait_for_timeout(50)
        self.assertLess(time.time() - t0, 1.0)
        with self.assertRaises(RpcError):
            page.wait_for_selector("#never", timeout=300)
        with self.assertRaises(RpcError) as cm:
            page.click("#missing")
        self.assertIn("missing", str(cm.exception))
        page.close()

    def test_contexts_isolate_cookies(self):
        ctx = self.sw.new_context("iso")
        p = ctx.new_page(self.base + "/cookie")
        self.assertTrue(any(c["name"] == "sid" for c in ctx.cookies()))
        other = self.sw.new_context("other")
        self.assertEqual(other.cookies(), [])
        p.close()
        ctx.close()
        other.close()
        self.assertIn("Sweef", self.sw.version()["product"])

    def test_session_shares_pages_with_cli(self):
        binp = os.environ.get("SWEEF_BIN") or __import__("sweef").ensure_binary()
        home = tempfile.mkdtemp(prefix="sweef-home-")
        env = dict(os.environ, SWEEF_HOME=home)
        saved = os.environ.get("SWEEF_HOME")
        os.environ["SWEEF_HOME"] = home
        try:
            subprocess.run([binp, "open", self.base + "/login"], env=env, check=True, capture_output=True)
            s = Sweef.session()
            page = s.pages()[0]
            self.assertEqual(page.title(), "Sign in")
            page.fill("#email", "cli@x.io")
            out = subprocess.run([binp, "get", "value", "#email"], env=env, check=True, capture_output=True, text=True).stdout.strip()
            self.assertEqual(out, "cli@x.io")
            s.close()
        finally:
            subprocess.run([binp, "close"], env=env, capture_output=True)
            if saved is None:
                os.environ.pop("SWEEF_HOME", None)
            else:
                os.environ["SWEEF_HOME"] = saved
            shutil.rmtree(home, ignore_errors=True)


if __name__ == "__main__":
    unittest.main()
