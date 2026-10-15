# sweef-browser

Python SDK for [Sweef](https://github.com/sweefbrowser/sweef), a browser engine for AI agents written in Go. Synchronous, standard library only, typed, Python 3.8+. The API follows Playwright's sync API where it can, and pages are read as a YAML accessibility tree in which every control carries a `[ref=eN]` handle you can pass back as a selector.

The SDK talks to a `sweef` binary over a local socket, so you need the binary too:

```bash
go install github.com/sweefbrowser/sweef/cmd/sweef@latest   # or download a release; put it on PATH or set SWEEF_BIN
pip install sweef-browser
```

## Quick start

```python
from sweef import Sweef

with Sweef.launch() as sw:                       # starts `sweef serve` on a private socket, stops it on exit
    page = sw.new_page("https://example.com")
    print(page.title())                          # Example Domain
    print(page.snapshot(interactive_only=True))  # - link "Learn more" [ref=e1]: ...
    png = page.screenshot()                      # PNG bytes
    print(len(png), "bytes of PNG")
```

## Connecting

| | |
|---|---|
| `Sweef.launch(addr=None, binary=None, args=None)` | start a private `sweef serve` and connect; `close()` (or `with`) stops it |
| `Sweef.connect("unix:/path" \| "host:port")` | attach to a running `sweef serve --addr ...` |
| `Sweef.session(name=None)` | attach to the session CLI's daemon, so a script and `sweef open/click/...` drive the same pages |

## Pages

```python
import re
from sweef import Sweef

with Sweef.launch() as sw:
    page = sw.new_page("https://news.ycombinator.com/")
    tree = page.snapshot()                                    # options: interactive_only, delta, selector, max_chars
    ref = re.search(r'link "new" \[ref=(e\d+)\]', tree).group(1)
    page.click(ref)                                           # refs, CSS, text=, role=, label=, placeholder=, testid=
    page.get_by_role("link", name="past").click()
    print(page.snapshot(delta=True))                          # only the lines that changed
```

- Reading: `title()`, `url()`, `content()`, `text_content(sel)`, `snapshot(...)`, `accessibility_tree()`, `screenshot(path=, full_page=, width=, height=)`, `screenshot_text()`, `pdf(path=, format=)`, `frames()`, `popups()`.
- Acting: `click`, `fill`, `type`, `press` (keys and combinations such as `Control+a`), `check`, `uncheck`, `select_option`, `hover`, `focus`, `tap`, `drag_and_drop`, `scroll`, `keyboard.type/press`, `mouse.click/move/down/up/wheel`.
- Locators: `page.locator(sel)` with `click`, `fill`, `text_content`, `inner_html`, `get_attribute`, `input_value`, `count`, `is_visible`, `is_checked`, `bounding_box`, `wait_for`, `nth`, `first`, `last`.
- JavaScript: `evaluate(expr)` or `evaluate("s => s.length", arg)`, `add_init_script`, `expose_binding` + `binding_calls`.
- Waiting (the page keeps running meanwhile): `wait_for_selector`, `wait_for_function`, `wait_for_load_state`, `wait_for_url`, `wait_for_timeout`.
- Network: `route(glob, json=... | body=..., status=, action=)`, `unroute`, `unroute_all`, `routes`, `network_log`, `har`, `set_extra_http_headers`, `downloads`, `download`.
- Logs and dialogs: `console_messages`, `events`, `arm_dialog("accept")`, `dialogs`.
- Emulation: `set_viewport_size`, `set_user_agent`, `set_geolocation`, `grant_permissions`, `set_offline`, `emulate_media(color_scheme)`.

## Batches: many steps, one round trip

```python
from sweef import Sweef

with Sweef.launch() as sw:
    page = sw.new_page()
    r = page.batch([
        "open https://the-internet.herokuapp.com/login",
        "fill #username tomsmith",
        "fill #password SuperSecretPassword!",
        "click button[type=submit]",
        "expect text 'You logged into a secure area'",
        {"method": "Page.title"},                     # any RPC method works as a step
    ], observe="interactive")
    print(r["ok"], r["totalMs"], r["observation"])
```

## Contexts

```python
ctx = sw.new_context(user_agent="my-agent/1.0", viewport={"width": 390, "height": 844})
p = ctx.new_page("https://example.com")
print(ctx.cookies())
ctx.storage_state(path="state.json")
ctx.close()
```

Every RPC method of the engine is reachable with `sw.call(method, params)`; see the method list in the main README.

## Tests

`SWEEF_BIN=/path/to/sweef python -m unittest discover -s tests` runs the suite against a real `sweef serve` and a local HTTP fixture (no network).

License: Apache-2.0.
