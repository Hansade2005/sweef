"""Sweef Browser Python SDK.

Playwright-shaped, synchronous, stdlib only. Speaks the SWF1 framed JSON-RPC
protocol to a running ``sweef serve`` (or launches one).

    from sweef import Sweef

    with Sweef.launch() as sw:
        page = sw.new_page("https://example.com")
        print(page.title())
        print(page.aria_snapshot(interactive_only=True))
        page.click("text=Learn more")
"""

from __future__ import annotations

import json
import os
import shutil
import socket
import struct
import subprocess
import time
from typing import Any, Dict, List, Optional

MAGIC = b"SWF1"
__all__ = ["Sweef", "Page", "Locator", "BrowserContext", "RpcError"]


class RpcError(Exception):
    def __init__(self, code: int, message: str, data: Any = None):
        super().__init__(f"rpc {code}: {message}")
        self.code, self.data = code, data


class Connection:
    def __init__(self, addr: str = "127.0.0.1:9222", timeout: float = 60.0):
        if addr.startswith("unix:"):
            self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            self.sock.connect(addr[5:])
        else:
            host, _, port = addr.rpartition(":")
            self.sock = socket.create_connection((host or "127.0.0.1", int(port)), timeout=timeout)
        self.sock.settimeout(timeout)
        self._id = 0

    def _read_exact(self, n: int) -> bytes:
        buf = b""
        while len(buf) < n:
            chunk = self.sock.recv(n - len(buf))
            if not chunk:
                raise ConnectionError("sweef connection closed")
            buf += chunk
        return buf

    def call(self, method: str, params: Optional[Dict[str, Any]] = None) -> Any:
        self._id += 1
        payload = json.dumps({"jsonrpc": "2.0", "id": self._id, "method": method, "params": params or {}}).encode()
        self.sock.sendall(MAGIC + struct.pack("<I", len(payload)) + payload)
        while True:
            hdr = self._read_exact(8)
            if hdr[:4] != MAGIC:
                raise ConnectionError("bad magic from sweef")
            (ln,) = struct.unpack("<I", hdr[4:])
            msg = json.loads(self._read_exact(ln))
            if msg.get("id") != self._id:
                continue  # event/notification; ignore
            if "error" in msg and msg["error"]:
                e = msg["error"]
                raise RpcError(e.get("code", -1), e.get("message", ""), e.get("data"))
            return msg.get("result")

    def close(self) -> None:
        try:
            self.sock.close()
        except OSError:
            pass


class Locator:
    def __init__(self, page: "Page", selector: str):
        self.page, self.selector = page, selector

    def _p(self, **extra: Any) -> Dict[str, Any]:
        return {"selector": self.selector, **extra}

    def nth(self, i: int) -> "Locator":
        return Locator(self.page, f"{self.selector} >> nth={i}")

    @property
    def first(self) -> "Locator":
        return self.nth(0)

    @property
    def last(self) -> "Locator":
        return self.nth(-1)

    def click(self) -> Any: return self.page._rpc("Input.click", self._p())
    def fill(self, value: str) -> Any: return self.page._rpc("Input.fill", self._p(value=value))
    def type(self, text: str, delay_ms: int = 0) -> Any: return self.page._rpc("Input.type", self._p(text=text, delayMs=delay_ms))
    def press(self, key: str = "Enter") -> Any: return self.page._rpc("Input.press", self._p(key=key))
    def check(self) -> Any: return self.page._rpc("Input.check", self._p())
    def uncheck(self) -> Any: return self.page._rpc("Input.uncheck", self._p())
    def select_option(self, value: str) -> Any: return self.page._rpc("Input.selectOption", self._p(value=value))
    def hover(self) -> Any: return self.page._rpc("Input.hover", self._p())
    def focus(self) -> Any: return self.page._rpc("Input.focus", self._p())
    def text_content(self) -> Optional[str]:
        r = self.page._rpc("Locator.textContent", self._p())
        return r.get("text") if isinstance(r, dict) else r
    def inner_html(self) -> Optional[str]:
        r = self.page._rpc("Locator.innerHTML", self._p())
        return r.get("html") if isinstance(r, dict) else r
    def get_attribute(self, name: str) -> Optional[str]:
        r = self.page._rpc("Locator.getAttribute", self._p(name=name))
        return r.get("value") if isinstance(r, dict) else r
    def count(self) -> int:
        r = self.page._rpc("Locator.count", self._p())
        return int(r.get("count", 0)) if isinstance(r, dict) else 0
    def is_visible(self) -> bool: return bool((self.page._rpc("Locator.isVisible", self._p()) or {}).get("visible"))
    def is_enabled(self) -> bool: return bool((self.page._rpc("Locator.isEnabled", self._p()) or {}).get("enabled"))
    def is_checked(self) -> bool: return bool((self.page._rpc("Locator.isChecked", self._p()) or {}).get("checked"))
    def wait_for(self, state: str = "visible", timeout_ms: int = 30000) -> Any:
        return self.page._rpc("Page.waitForSelector", self._p(state=state, timeoutMs=timeout_ms))


class Page:
    def __init__(self, conn: Connection, tab: str):
        self.conn, self.tab = conn, tab

    def _rpc(self, method: str, params: Optional[Dict[str, Any]] = None) -> Any:
        return self.conn.call(method, {"tab": self.tab, **(params or {})})

    def goto(self, url: str) -> Any: return self._rpc("Page.navigate", {"url": url})
    def url(self) -> str: return self._rpc("Page.url")["url"]
    def title(self) -> str: return self._rpc("Page.title")["title"]
    def content(self) -> str: return self._rpc("Page.content")["html"]
    def text_content(self, selector: str = "body") -> str: return self._rpc("Page.textContent", {"selector": selector})["text"]
    def aria_snapshot(self, selector: Optional[str] = None, interactive_only: bool = False,
                      max_chars: int = 0, max_text_len: int = 0, max_depth: int = 0) -> str:
        p: Dict[str, Any] = {"interactiveOnly": interactive_only}
        if selector: p["selector"] = selector
        if max_chars: p["maxChars"] = max_chars
        if max_text_len: p["maxTextLen"] = max_text_len
        if max_depth: p["maxDepth"] = max_depth
        return self._rpc("Page.ariaSnapshot", p)["yaml"]
    def snapshot(self) -> List[Dict[str, Any]]: return self._rpc("Page.snapshot")
    def screenshot(self, width: int = 0, height: int = 0, device_scale_factor: float = 1.0) -> bytes:
        import base64
        r = self._rpc("Page.screenshot", {"width": width, "height": height, "deviceScaleFactor": device_scale_factor})
        return base64.b64decode(r.get("pngBase64", ""))
    def evaluate(self, expression: str) -> Any: return self._rpc("Runtime.evaluate", {"expression": expression}).get("result")
    def go_back(self) -> Any: return self._rpc("Page.goBack")
    def go_forward(self) -> Any: return self._rpc("Page.goForward")
    def reload(self) -> Any: return self._rpc("Page.reload")
    def locator(self, selector: str) -> Locator: return Locator(self, selector)
    def click(self, selector: str) -> Any: return self.locator(selector).click()
    def fill(self, selector: str, value: str) -> Any: return self.locator(selector).fill(value)
    def type(self, selector: str, text: str, delay_ms: int = 0) -> Any: return self.locator(selector).type(text, delay_ms)
    def press(self, selector: str, key: str = "Enter") -> Any: return self.locator(selector).press(key)
    def check(self, selector: str) -> Any: return self.locator(selector).check()
    def uncheck(self, selector: str) -> Any: return self.locator(selector).uncheck()
    def select_option(self, selector: str, value: str) -> Any: return self.locator(selector).select_option(value)
    def hover(self, selector: str) -> Any: return self.locator(selector).hover()
    def wait_for_selector(self, selector: str, state: str = "visible", timeout_ms: int = 30000) -> Any:
        return self._rpc("Page.waitForSelector", {"selector": selector, "state": state, "timeoutMs": timeout_ms})
    def wait_for_function(self, expression: str, timeout_ms: int = 30000, poll_ms: int = 100) -> Any:
        return self._rpc("Page.waitForFunction", {"expression": expression, "timeoutMs": timeout_ms, "pollMs": poll_ms})
    def wait_for_load_state(self, state: str = "load", timeout_ms: int = 30000) -> Any:
        return self._rpc("Page.waitForLoadState", {"state": state, "timeoutMs": timeout_ms})
    def add_init_script(self, source: str) -> Any: return self._rpc("Page.addInitScript", {"source": source})
    def console_messages(self, since: int = 0) -> List[Any]: return self._rpc("Log.getConsole", {"since": since}).get("messages", [])
    def network_log(self, since: int = 0) -> List[Any]: return self._rpc("Network.getLogs", {"since": since}).get("entries", [])
    def har(self) -> Any: return self._rpc("Network.har")
    def route(self, pattern: str, status: int = 200, body: str = "", action: str = "fulfill") -> Any:
        return self._rpc("Network.route", {"pattern": pattern, "status": status, "body": body, "action": action})
    def unroute(self, pattern: str) -> Any: return self._rpc("Network.unroute", {"pattern": pattern})
    def set_offline(self, offline: bool) -> Any: return self._rpc("Emulation.setOffline", {"offline": offline})
    def set_user_agent(self, ua: str) -> Any: return self._rpc("Emulation.setUserAgentOverride", {"userAgent": ua})
    def set_viewport_size(self, width: int, height: int) -> Any: return self._rpc("Emulation.setDeviceMetrics", {"width": width, "height": height})
    def set_extra_http_headers(self, headers: Dict[str, str]) -> Any: return self._rpc("Network.setExtraHTTPHeaders", {"headers": headers})
    def arm_dialog(self, action: str = "accept", prompt_text: str = "") -> Any:
        return self._rpc("Page.armDialog", {"action": action, "promptText": prompt_text})
    def close(self) -> Any: return self._rpc("Browser.closeTab")


class BrowserContext:
    def __init__(self, conn: Connection, name: Optional[str] = None):
        self.conn, self.name = conn, name

    def new_page(self, url: str = "about:blank") -> Page:
        if self.name:
            r = self.conn.call("Browser.newTabInContext", {"context": self.name, "url": url})
        else:
            r = self.conn.call("Browser.newTab", {"url": url})
        return Page(self.conn, r["tab"])

    def storage_state(self, path: Optional[str] = None) -> Any:
        return self.conn.call("BrowserContext.storageState", {"context": self.name, "path": path})

    def set_storage_state(self, state: Optional[str] = None, path: Optional[str] = None) -> Any:
        return self.conn.call("BrowserContext.loadStorageState", {"context": self.name, "state": state, "path": path})

    def add_cookies(self, cookies: List[Dict[str, Any]]) -> Any:
        return self.conn.call("BrowserContext.addCookies", {"context": self.name, "cookies": cookies})

    def clear_cookies(self) -> Any: return self.conn.call("BrowserContext.clearCookies", {"context": self.name})

    def request(self, method: str, url: str, headers: Optional[Dict[str, str]] = None, body: str = "") -> Any:
        return self.conn.call("Fetch.request", {"context": self.name, "method": method, "url": url, "headers": headers or {}, "body": body})

    def close(self) -> Any:
        return self.conn.call("Browser.closeContext", {"context": self.name}) if self.name else None


class Sweef:
    def __init__(self, conn: Connection, proc: Optional[subprocess.Popen] = None):
        self.conn, self._proc = conn, proc

    @classmethod
    def connect(cls, addr: str = "127.0.0.1:9222") -> "Sweef":
        return cls(Connection(addr))

    @classmethod
    def launch(cls, addr: str = "127.0.0.1:9222", binary: Optional[str] = None, args: Optional[List[str]] = None,
               startup_timeout: float = 10.0) -> "Sweef":
        """Start ``sweef serve`` and connect to it. ``binary`` defaults to
        ``$SWEEF_BIN`` or ``sweef`` on PATH."""
        exe = binary or os.environ.get("SWEEF_BIN") or shutil.which("sweef")
        if not exe:
            raise FileNotFoundError("sweef binary not found; set SWEEF_BIN or pass binary=")
        proc = subprocess.Popen([exe, "serve", "--addr", addr, *(args or [])], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        deadline = time.time() + startup_timeout
        last: Optional[Exception] = None
        while time.time() < deadline:
            try:
                return cls(Connection(addr), proc)
            except OSError as e:  # not listening yet
                last = e
                time.sleep(0.05)
        proc.terminate()
        raise ConnectionError(f"sweef did not start on {addr}: {last}")

    def new_page(self, url: str = "about:blank") -> Page: return BrowserContext(self.conn).new_page(url)

    def new_context(self, name: str = "", **opts: Any) -> BrowserContext:
        r = self.conn.call("Browser.newContext", {"name": name, **opts})
        return BrowserContext(self.conn, r.get("context") or r.get("id"))

    def contexts(self) -> Any: return self.conn.call("Browser.listContexts")
    def version(self) -> Any: return self.conn.call("Browser.getVersion")
    def stats(self) -> Any: return self.conn.call("Browser.stats")

    def close(self) -> None:
        self.conn.close()
        if self._proc:
            self._proc.terminate()
            try:
                self._proc.wait(timeout=3)
            except subprocess.TimeoutExpired:
                self._proc.kill()

    def __enter__(self) -> "Sweef": return self
    def __exit__(self, *exc: Any) -> None: self.close()
