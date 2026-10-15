"""Sweef Browser Python SDK.

Playwright-shaped, synchronous, standard library only. Speaks the SWF1 framed
JSON-RPC protocol to a ``sweef serve`` process (or the session CLI's daemon).

    from sweef import Sweef

    with Sweef.launch() as sw:
        page = sw.new_page("https://example.com")
        print(page.title())
        print(page.snapshot(interactive_only=True))
        page.click("text=Learn more")
"""

from __future__ import annotations

import base64
import json
import os
import shlex
import shutil
import socket
import struct
import subprocess
import sys
import tempfile
import time
from typing import Any, Callable, Dict, List, Optional, Union

from ._install import SweefInstallError, ensure_binary

__version__ = "0.2.1"
MAGIC = b"SWF1"
__all__ = ["ensure_binary", "SweefInstallError", "Sweef", "Page", "Locator", "BrowserContext", "Keyboard", "Mouse", "Connection", "RpcError", "__version__"]

Step = Union[str, Dict[str, Any]]


class RpcError(Exception):
    """An error returned by the engine (bad selector, timeout, navigation failure...)."""

    def __init__(self, code: int, message: str, data: Any = None):
        super().__init__(message)
        self.code, self.message, self.data = code, message, data


def _parse_addr(addr: Union[str, int]) -> Any:
    if isinstance(addr, int):
        return ("127.0.0.1", addr)
    if addr.startswith("unix:"):
        return addr[5:]
    if addr.startswith("/"):
        return addr
    host, _, port = addr.rpartition(":")
    return (host or "127.0.0.1", int(port))


class Connection:
    """One SWF1 connection. Calls are synchronous; one call at a time per connection."""

    def __init__(self, addr: Union[str, int] = "127.0.0.1:9222", timeout: float = 60.0):
        target = _parse_addr(addr)
        if isinstance(target, str):
            self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            self.sock.settimeout(timeout)
            self.sock.connect(target)
        else:
            self.sock = socket.create_connection(target, timeout=timeout)
            self.sock.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)
        self.sock.settimeout(timeout)
        self.timeout = timeout
        self._rf = self.sock.makefile("rb")
        self._id = 0

    def _read_exact(self, n: int) -> bytes:
        b = self._rf.read(n)
        if b is None or len(b) < n:
            raise ConnectionError("sweef connection closed")
        return b

    def call(self, method: str, params: Optional[Dict[str, Any]] = None, timeout: Optional[float] = None) -> Any:
        self._id += 1
        payload = json.dumps({"jsonrpc": "2.0", "id": self._id, "method": method, "params": params or {}},
                             separators=(",", ":")).encode()
        if timeout is not None:
            self.sock.settimeout(timeout)
        try:
            self.sock.sendall(MAGIC + struct.pack("<I", len(payload)) + payload)
            while True:
                hdr = self._read_exact(8)
                if hdr[:4] != MAGIC:
                    raise ConnectionError("bad frame magic from sweef")
                (ln,) = struct.unpack("<I", hdr[4:])
                msg = json.loads(self._read_exact(ln))
                if msg.get("id") != self._id:
                    continue  # a notification
                err = msg.get("error")
                if err:
                    raise RpcError(err.get("code", -1), err.get("message", ""), err.get("data"))
                return msg.get("result")
        finally:
            if timeout is not None:
                self.sock.settimeout(self.timeout)

    def close(self) -> None:
        for c in (self._rf, self.sock):
            try:
                c.close()
            except OSError:
                pass


def _wait_timeout(ms: Optional[float]) -> float:
    return max(60.0, (ms or 30000) / 1000.0 + 5.0)


class Locator:
    def __init__(self, page: "Page", selector: str):
        self.page, self.selector = page, selector

    def _p(self, **extra: Any) -> Dict[str, Any]:
        return {"selector": self.selector, **extra}

    def _r(self, method: str, timeout: Optional[float] = None, **extra: Any) -> Any:
        return self.page._rpc(method, self._p(**extra), timeout=timeout)

    def locator(self, selector: str) -> "Locator":
        return Locator(self.page, f"{self.selector} >> {selector}")

    def nth(self, i: int) -> "Locator":
        return Locator(self.page, f"{self.selector} >> nth={i}")

    @property
    def first(self) -> "Locator":
        return self.nth(0)

    @property
    def last(self) -> "Locator":
        return self.nth(-1)

    def click(self) -> None: self._r("Input.click")
    def fill(self, value: str) -> None: self._r("Input.fill", value=value)
    def type(self, text: str, delay: float = 0) -> None: self._r("Input.type", text=text, delayMs=delay)
    def press_sequentially(self, text: str, delay: float = 0) -> None: self.type(text, delay)
    def press(self, key: str = "Enter") -> None: self._r("Input.press", key=key)
    def check(self) -> None: self._r("Input.check")
    def uncheck(self) -> None: self._r("Input.uncheck")
    def set_checked(self, checked: bool) -> None: self.check() if checked else self.uncheck()
    def select_option(self, value: str) -> None: self._r("Input.selectOption", value=value)
    def hover(self) -> None: self._r("Input.hover")
    def focus(self) -> None: self._r("Input.focus")
    def tap(self) -> None: self._r("Input.tap")
    def scroll_into_view_if_needed(self) -> None: self._r("Input.scroll")

    def drag_to(self, target: Union["Locator", str]) -> None:
        t = target.selector if isinstance(target, Locator) else target
        self.page._rpc("Input.drag", {"source": self.selector, "target": t})

    def text_content(self) -> str: return self._r("Locator.textContent")["text"]
    def inner_text(self) -> str: return self.text_content()
    def inner_html(self) -> str: return self._r("Locator.innerHTML")["innerHTML"]
    def outer_html(self) -> str: return self._r("DOM.getOuterHTML")["outerHTML"]

    def get_attribute(self, name: str) -> Optional[str]:
        r = self._r("Locator.getAttribute", name=name)
        return None if r.get("present") is False else r.get("value")

    def attributes(self) -> Dict[str, str]: return self._r("DOM.getAttributes")
    def input_value(self) -> str: return self.page._step_result({"cmd": "get", "args": ["value", self.selector]})
    def count(self) -> int: return int(self._r("Locator.count")["count"])
    def is_visible(self) -> bool: return bool(self._r("Locator.isVisible").get("value"))
    def is_hidden(self) -> bool: return not self.is_visible()
    def is_enabled(self) -> bool: return bool(self._r("Locator.isEnabled").get("value"))
    def is_disabled(self) -> bool: return not self.is_enabled()
    def is_editable(self) -> bool: return bool(self._r("Locator.isEditable").get("value"))
    def is_checked(self) -> bool: return bool(self._r("Locator.isChecked").get("value"))
    def bounding_box(self) -> Dict[str, float]: return self._r("Locator.boundingBox")

    def wait_for(self, state: str = "visible", timeout: float = 30000) -> None:
        self._r("Page.waitForSelector", timeout=_wait_timeout(timeout), state=state, timeoutMs=timeout)

    def snapshot(self, **opts: Any) -> str: return self.page.snapshot(selector=self.selector, **opts)


class Keyboard:
    def __init__(self, page: "Page"):
        self.page = page

    def type(self, text: str, delay: float = 0) -> None:
        self.page._rpc("Input.keyboard", {"text": text, "delayMs": delay})

    def insert_text(self, text: str) -> None: self.type(text)

    def press(self, key: str) -> None:
        """A key or combination: "Enter", "Control+a", "Shift+Tab"."""
        self.page._rpc("Input.keyboard", {"key": key})


class Mouse:
    def __init__(self, page: "Page"):
        self.page = page

    def _m(self, action: str, x: float, y: float) -> None:
        self.page._rpc("Input.mouse", {"action": action, "x": x, "y": y})

    def click(self, x: float, y: float) -> None: self._m("click", x, y)
    def move(self, x: float, y: float) -> None: self._m("move", x, y)
    def down(self, x: float = 0, y: float = 0) -> None: self._m("down", x, y)
    def up(self, x: float = 0, y: float = 0) -> None: self._m("up", x, y)
    def wheel(self, delta_x: float, delta_y: float) -> None: self.page._rpc("Input.scroll", {"x": delta_x, "y": delta_y})


def _norm_step(s: Step) -> Dict[str, Any]:
    if isinstance(s, str):
        toks = shlex.split(s)
        return {"cmd": toks[0], "args": toks[1:]}
    if "args" in s:
        return {**s, "args": [str(a) for a in s["args"]]}
    return s


def _js(fn_or_expr: str, arg: Any = None, has_arg: bool = False) -> str:
    return fn_or_expr if not has_arg else f"({fn_or_expr})({json.dumps(arg)})"


class Page:
    def __init__(self, conn: Connection, tab: str, context: Optional["BrowserContext"] = None):
        self.conn, self.tab, self.context = conn, tab, context
        self.keyboard = Keyboard(self)
        self.mouse = Mouse(self)

    def _rpc(self, method: str, params: Optional[Dict[str, Any]] = None, timeout: Optional[float] = None) -> Any:
        return self.conn.call(method, {"tab": self.tab, **(params or {})}, timeout=timeout)

    # navigation
    def goto(self, url: str, headers: Optional[Dict[str, str]] = None) -> Dict[str, Any]:
        return self._rpc("Page.navigate", {"url": url, **({"headers": headers} if headers else {})})

    def go_back(self) -> None: self._rpc("Page.goBack")
    def go_forward(self) -> None: self._rpc("Page.goForward")
    def reload(self) -> None: self._rpc("Page.reload")
    def url(self) -> str: return self._rpc("Page.url")["url"]
    def title(self) -> str: return self._rpc("Page.title")["title"]
    def frames(self) -> List[Any]: return self._rpc("Page.frames")["frames"]
    def popups(self) -> List[Any]: return self._rpc("Page.popups")["popups"]

    # reading
    def content(self) -> str: return self._rpc("Page.content")["html"]
    def text_content(self, selector: str = "body") -> str: return self._rpc("Page.textContent", {"selector": selector})["text"]
    def inner_text(self, selector: str = "body") -> str: return self.text_content(selector)

    def snapshot(self, selector: Optional[str] = None, interactive_only: bool = False, delta: bool = False,
                 max_chars: int = 0, max_text_len: int = 0, max_depth: int = 0) -> str:
        """The YAML accessibility tree; controls carry [ref=eN], usable as selectors."""
        p: Dict[str, Any] = {}
        if selector:
            p["selector"] = selector
        if interactive_only:
            p["interactiveOnly"] = True
        if delta:
            p["delta"] = True
        if max_chars:
            p["maxChars"] = max_chars
        if max_text_len:
            p["maxTextLen"] = max_text_len
        if max_depth:
            p["maxDepth"] = max_depth
        return self._rpc("Page.ariaSnapshot", p)["yaml"]

    aria_snapshot = snapshot

    def accessibility_tree(self) -> List[Dict[str, Any]]: return self._rpc("Page.snapshot")

    def screenshot(self, path: Optional[str] = None, full_page: bool = False, width: int = 0, height: int = 0,
                   device_scale_factor: float = 0) -> bytes:
        """PNG bytes (also written to path when given)."""
        r = self._rpc("Page.screenshot", {"fullPage": full_page, "width": width, "height": height,
                                          "deviceScaleFactor": device_scale_factor, "noText": True})
        b = base64.b64decode(r.get("pngBase64", ""))
        if path:
            with open(path, "wb") as f:
                f.write(b)
        return b

    def screenshot_text(self, width: int = 0) -> str:
        return self._rpc("Page.screenshot", {"width": width, "type": "text"})["text"]

    def pdf(self, path: Optional[str] = None, format: str = "", landscape: bool = False, margin: float = 0,
            scale: float = 0, display_header_footer: bool = False) -> bytes:
        r = self._rpc("Page.pdf", {"path": path, "format": format, "landscape": landscape, "margin": margin,
                                   "scale": scale, "displayHeaderFooter": display_header_footer})
        if r.get("pdfBase64"):
            return base64.b64decode(r["pdfBase64"])
        if path:
            with open(path, "rb") as f:
                return f.read()
        return b""

    # acting
    def locator(self, selector: str) -> Locator: return Locator(self, selector)

    def get_by_role(self, role: str, name: Optional[str] = None) -> Locator:
        return self.locator(f"role={role}" if name is None else f"role={role}[name={json.dumps(name)}]")

    def get_by_text(self, text: str) -> Locator: return self.locator(f"text={text}")
    def get_by_label(self, text: str) -> Locator: return self.locator(f"label={text}")
    def get_by_placeholder(self, text: str) -> Locator: return self.locator(f"placeholder={text}")
    def get_by_alt_text(self, text: str) -> Locator: return self.locator(f"alt={text}")
    def get_by_test_id(self, test_id: str) -> Locator: return self.locator(f"testid={test_id}")
    def click(self, selector: str) -> None: self.locator(selector).click()
    def fill(self, selector: str, value: str) -> None: self.locator(selector).fill(value)
    def type(self, selector: str, text: str, delay: float = 0) -> None: self.locator(selector).type(text, delay)
    def press(self, selector: str, key: str = "Enter") -> None: self.locator(selector).press(key)
    def check(self, selector: str) -> None: self.locator(selector).check()
    def uncheck(self, selector: str) -> None: self.locator(selector).uncheck()
    def select_option(self, selector: str, value: str) -> None: self.locator(selector).select_option(value)
    def hover(self, selector: str) -> None: self.locator(selector).hover()
    def focus(self, selector: str) -> None: self.locator(selector).focus()
    def tap(self, selector: str) -> None: self.locator(selector).tap()
    def drag_and_drop(self, source: str, target: str) -> None: self._rpc("Input.drag", {"source": source, "target": target})
    def scroll(self, x: float = 0, y: float = 0) -> None: self._rpc("Input.scroll", {"x": x, "y": y})

    def batch(self, steps: List[Step], observe: str = "none", stop_on_error: bool = True) -> Dict[str, Any]:
        """Many steps, one round trip: "fill #q hi", {"cmd": "click", "args": [sel]} or {"method": ..., "params": ...}."""
        return self._rpc("Batch.run", {"steps": [_norm_step(s) for s in steps], "observe": observe,
                                       "stopOnError": stop_on_error}, timeout=300)

    def _step_result(self, step: Step) -> Any:
        r = self.batch([step])
        st = (r.get("steps") or [None])[0]
        if not st or not st.get("ok"):
            raise RpcError(-32000, (st or {}).get("error", "step failed"))
        v = st.get("result")
        return v["value"] if isinstance(v, dict) and "value" in v else v

    # JavaScript
    def evaluate(self, expression: str, arg: Any = None) -> Any:
        """Evaluate an expression, or call a JS function source with arg: evaluate("s => s.length", "abc")."""
        has_arg = arg is not None
        return self._rpc("Runtime.evaluate", {"expression": _js(expression, arg, has_arg)}).get("result")

    def add_init_script(self, source: str) -> str: return self._rpc("Page.addInitScript", {"source": source})["id"]
    def remove_init_script(self, script_id: str) -> None: self._rpc("Page.removeInitScript", {"id": script_id})

    def expose_binding(self, name: str, reply: Any = None) -> None:
        self._rpc("Runtime.addBinding", {"name": name, **({"reply": reply} if reply is not None else {})})

    def set_binding_reply(self, name: str, reply: Any) -> None: self._rpc("Runtime.setBindingReply", {"name": name, "reply": reply})
    def binding_calls(self, since: int = 0) -> List[Any]: return self._rpc("Runtime.bindingCalls", {"since": since}).get("calls", [])

    # waiting (the page keeps running meanwhile)
    def wait_for_selector(self, selector: str, state: str = "visible", timeout: float = 30000) -> Dict[str, Any]:
        return self._rpc("Page.waitForSelector", {"selector": selector, "state": state, "timeoutMs": timeout}, timeout=_wait_timeout(timeout))

    def wait_for_function(self, expression: str, timeout: float = 30000, polling: float = 100) -> None:
        self._rpc("Page.waitForFunction", {"expression": expression, "timeoutMs": timeout, "pollMs": polling}, timeout=_wait_timeout(timeout))

    def wait_for_load_state(self, state: str = "load", timeout: float = 30000) -> None:
        self._rpc("Page.waitForLoadState", {"state": state, "timeoutMs": timeout}, timeout=_wait_timeout(timeout))

    def wait_for_url(self, pattern: str, timeout: float = 30000) -> None:
        """Glob (** any, * one segment) or substring."""
        self._rpc("Page.waitForURL", {"pattern": pattern, "timeoutMs": timeout}, timeout=_wait_timeout(timeout))

    def wait_for_timeout(self, ms: float) -> None: self._rpc("Page.waitForTimeout", {"ms": ms}, timeout=_wait_timeout(ms))

    # logs and network
    def console_messages(self, since: int = 0) -> List[Any]: return self._rpc("Log.getConsole", {"since": since}).get("messages", [])
    def network_log(self, since: int = 0) -> List[Any]: return self._rpc("Network.getLogs", {"since": since}).get("entries", [])
    def events(self, since: int = 0) -> List[Any]: return self._rpc("Events.poll", {"since": since})
    def state_log(self, since: int = 0) -> Any: return self._rpc("Page.getStateLog", {"since": since})
    def har(self) -> Any: return self._rpc("Network.har")

    def route(self, pattern: str, action: str = "fulfill", **opts: Any) -> None:
        """opts: status, body, json, content_type, headers, url, new_method, new_body, method, body_contains, times."""
        keys = {"content_type": "contentType", "new_method": "newMethod", "new_body": "newBody", "body_contains": "bodyContains"}
        self._rpc("Network.route", {"pattern": pattern, "action": action, **{keys.get(k, k): v for k, v in opts.items()}})

    def unroute(self, pattern: str = "") -> None: self._rpc("Network.unroute", {"pattern": pattern})
    def unroute_all(self) -> None: self._rpc("Network.unrouteAll")
    def routes(self) -> List[Any]: return self._rpc("Network.routes").get("routes", [])
    def set_extra_http_headers(self, headers: Dict[str, str]) -> None: self._rpc("Network.setExtraHTTPHeaders", {"headers": headers})
    def downloads(self) -> List[Any]: return self._rpc("Page.downloads").get("downloads", [])
    def download(self, url: str, filename: str = "") -> Any: return self._rpc("Page.download", {"url": url, "filename": filename})
    def scripts(self) -> Any: return self._rpc("Storage.getScripts")

    # dialogs
    def arm_dialog(self, action: str = "accept", prompt_text: str = "") -> None:
        self._rpc("Page.armDialog", {"action": action, "promptText": prompt_text})

    def dialogs(self, since: int = 0) -> List[Any]:
        r = self._rpc("Page.getDialogs", {"since": since})
        return r if isinstance(r, list) else r.get("dialogs", [])

    # emulation
    def set_viewport_size(self, width: int, height: int, device_scale_factor: float = 0) -> None:
        self._rpc("Emulation.setDeviceMetrics", {"width": width, "height": height, "deviceScaleFactor": device_scale_factor})

    def set_offline(self, offline: bool) -> None: self._rpc("Emulation.setOffline", {"offline": offline})
    def set_user_agent(self, ua: str) -> None: self._rpc("Emulation.setUserAgentOverride", {"userAgent": ua})
    def set_geolocation(self, latitude: float, longitude: float) -> None: self._rpc("Emulation.setGeolocation", {"latitude": latitude, "longitude": longitude})
    def grant_permissions(self, permissions: List[str]) -> List[str]: return self._rpc("Emulation.grantPermissions", {"permissions": permissions}).get("granted", [])
    def emulate_media(self, color_scheme: str) -> None: self._rpc("Emulation.setColorScheme", {"colorScheme": color_scheme})

    def close(self) -> None: self.conn.call("Browser.closeTab", {"tab": self.tab})


class BrowserContext:
    def __init__(self, conn: Connection, name: Optional[str] = None):
        self.conn, self.name = conn, name

    def _c(self, **extra: Any) -> Dict[str, Any]:
        return {**({"context": self.name} if self.name else {}), **extra}

    def new_page(self, url: str = "about:blank") -> Page:
        if self.name:
            r = self.conn.call("Browser.newTabInContext", {"context": self.name, "url": url})
        else:
            r = self.conn.call("Browser.newTab", {"url": url})
        return Page(self.conn, r["tab"], self)

    def storage_state(self, path: Optional[str] = None) -> Dict[str, Any]:
        if path:
            self.conn.call("BrowserContext.saveStorageState", self._c(path=path))
        return self.conn.call("BrowserContext.storageState", self._c())["state"]

    def set_storage_state(self, state: Union[str, Dict[str, Any], None] = None, path: Optional[str] = None) -> None:
        if isinstance(state, dict):
            state = json.dumps(state)
        self.conn.call("BrowserContext.loadStorageState", self._c(state=state, path=path))

    def add_cookies(self, cookies: List[Dict[str, Any]]) -> None: self.conn.call("BrowserContext.addCookies", self._c(cookies=cookies))
    def clear_cookies(self) -> None: self.conn.call("BrowserContext.clearCookies", self._c())
    def cookies(self) -> List[Dict[str, Any]]: return self.storage_state().get("cookies") or []

    def request(self, method: str, url: str, headers: Optional[Dict[str, str]] = None, body: str = "") -> Dict[str, Any]:
        return self.conn.call("Fetch.request", self._c(method=method, url=url, headers=headers or {}, body=body))

    def close(self) -> None:
        if self.name:
            self.conn.call("Browser.closeContext", {"context": self.name})


def _session_dir() -> str:
    if os.environ.get("SWEEF_HOME"):
        return os.environ["SWEEF_HOME"]
    if os.environ.get("XDG_RUNTIME_DIR"):
        return os.path.join(os.environ["XDG_RUNTIME_DIR"], "sweef")
    uid = os.getuid() if hasattr(os, "getuid") else 0
    return os.path.join(tempfile.gettempdir(), f"sweef-{uid}")


def _find_binary(binary: Optional[str]) -> str:
    # explicit binary, SWEEF_BIN, sweef on PATH, per-user cache, then an automatic download into the cache
    # (SWEEF_SKIP_DOWNLOAD=1 disables it). See sweef._install.
    return ensure_binary(binary)


def _dial(addr: Union[str, int], deadline: float, proc: Optional[subprocess.Popen] = None) -> Connection:
    last: Optional[Exception] = None
    while time.time() < deadline:
        if proc is not None and proc.poll() is not None:
            raise ConnectionError(f"sweef serve exited with code {proc.returncode}")
        try:
            return Connection(addr)
        except OSError as e:  # not listening yet
            last = e
            time.sleep(0.01)
    raise ConnectionError(f"sweef did not start on {addr}: {last}")


class Sweef:
    def __init__(self, conn: Connection, proc: Optional[subprocess.Popen] = None, cleanup: Optional[Callable[[], None]] = None):
        self.conn, self._proc, self._cleanup = conn, proc, cleanup

    @classmethod
    def connect(cls, addr: Union[str, int] = "127.0.0.1:9222", timeout: float = 60.0) -> "Sweef":
        """Attach to a running ``sweef serve`` ("unix:/path", "host:port" or a port)."""
        return cls(Connection(addr, timeout))

    @classmethod
    def launch(cls, addr: Optional[str] = None, binary: Optional[str] = None, args: Optional[List[str]] = None,
               startup_timeout: float = 10.0) -> "Sweef":
        """Start a private ``sweef serve`` (a unix socket; a loopback port on Windows) and connect.
        ``close()`` stops it."""
        exe = _find_binary(binary)
        cleanup = None
        if addr is None:
            if sys.platform == "win32":
                s = socket.socket()
                s.bind(("127.0.0.1", 0))
                addr = f"127.0.0.1:{s.getsockname()[1]}"
                s.close()
            else:
                d = tempfile.mkdtemp(prefix="sweef-sdk-")
                addr = "unix:" + os.path.join(d, "s.sock")
                cleanup = lambda: shutil.rmtree(d, ignore_errors=True)  # noqa: E731
        proc = subprocess.Popen([exe, "serve", "--addr", addr, *(args or [])], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        try:
            return cls(_dial(addr, time.time() + startup_timeout, proc), proc, cleanup)
        except Exception:
            proc.kill()
            if cleanup:
                cleanup()
            raise

    @classmethod
    def session(cls, name: Optional[str] = None, binary: Optional[str] = None, startup_timeout: float = 10.0) -> "Sweef":
        """Attach to the session CLI's daemon (``sweef open ...``), starting it when absent,
        so a script and the CLI drive the same pages."""
        name = name or os.environ.get("SWEEF_SESSION") or "default"
        sock = os.path.join(_session_dir(), name + ".sock")
        try:
            return cls(Connection("unix:" + sock))
        except OSError:
            pass
        os.makedirs(os.path.dirname(sock), mode=0o700, exist_ok=True)
        try:
            os.remove(sock)
        except OSError:
            pass
        subprocess.Popen([_find_binary(binary), "serve", "--addr", "unix:" + sock, "--idle-timeout",
                          os.environ.get("SWEEF_IDLE_TIMEOUT", "1h")], stdout=subprocess.DEVNULL,
                         stderr=subprocess.DEVNULL, start_new_session=True)
        return cls(_dial("unix:" + sock, time.time() + startup_timeout))

    def new_page(self, url: str = "about:blank") -> Page: return BrowserContext(self.conn).new_page(url)

    def new_context(self, name: str = "", viewport: Optional[Dict[str, int]] = None, **opts: Any) -> BrowserContext:
        """opts: user_agent, device_scale_factor, mobile, offline, color_scheme, proxy, extra_http_headers, permissions."""
        keys = {"user_agent": "userAgent", "device_scale_factor": "deviceScaleFactor", "color_scheme": "colorScheme",
                "extra_http_headers": "extraHTTPHeaders", "storage_path": "storagePath"}
        p: Dict[str, Any] = {"name": name, **{keys.get(k, k): v for k, v in opts.items()}}
        if viewport:
            p["viewportWidth"], p["viewportHeight"] = viewport.get("width"), viewport.get("height")
        r = self.conn.call("Browser.newContext", p)
        return BrowserContext(self.conn, r.get("context") or r.get("id"))

    def pages(self) -> List[Page]: return [Page(self.conn, t["id"]) for t in self.conn.call("Browser.listTabs")]
    def page(self, tab_id: str) -> Page: return Page(self.conn, tab_id)
    def contexts(self) -> Any: return self.conn.call("Browser.listContexts")
    def version(self) -> Dict[str, Any]: return self.conn.call("Browser.getVersion")
    def stats(self) -> Dict[str, Any]: return self.conn.call("Browser.stats")

    def call(self, method: str, params: Optional[Dict[str, Any]] = None, timeout: Optional[float] = None) -> Any:
        """Any RPC method (see the method list in the main README)."""
        return self.conn.call(method, params, timeout=timeout)

    def close(self) -> None:
        if self._proc is not None:
            try:
                self.conn.call("Browser.shutdown", timeout=2)
            except (OSError, RpcError, ConnectionError):
                pass
        self.conn.close()
        if self._proc is not None:
            try:
                self._proc.wait(timeout=3)
            except subprocess.TimeoutExpired:
                self._proc.kill()
                self._proc.wait()
        if self._cleanup:
            self._cleanup()

    def __enter__(self) -> "Sweef": return self
    def __exit__(self, *exc: Any) -> None: self.close()
