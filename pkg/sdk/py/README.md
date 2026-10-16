# sweef-browser

Python SDK for [Sweef](https://github.com/sweefbrowser/sweef), a browser engine for AI agents written in Go. Synchronous, standard library only, typed, Python 3.8+. The API follows Playwright's sync API where it can, and pages are read as a YAML accessibility tree in which every control carries a `[ref=eN]` handle you can pass back as a selector.

The SDK talks to the `sweef` engine over a local socket; it installs the engine for you on first use, so this is all you need:

```bash
pip install sweef-browser
```

## The sweef engine: installed for you

You do not download Sweef or edit PATH. The SDK finds the engine in this order:

1. the `binary` option (`Sweef.launch(binary=...)`)
2. `$SWEEF_BIN`
3. `sweef` on PATH
4. the per-user cache: `$XDG_CACHE_HOME/sweef/<engine-version>/sweef` (default `~/.cache/...`), `~/Library/Caches/sweef/...` on macOS, `%LOCALAPPDATA%\sweef\cache\<engine-version>\sweef.exe` on Windows
5. otherwise it downloads the pinned engine (`0.1.0-alpha.6`) from the [GitHub release](https://github.com/sweefbrowser/sweef/releases) into that cache, once, and prints one line to stderr. The archive's SHA-256 is checked before anything is executed; the SDK pins the expected hashes for its default version and falls back to the release's `SHA256SUMS` for other versions.

`pip install sweef-browser` (or `pipx install sweef-browser`) also puts a `sweef` command on PATH (it runs the same engine, downloading it first if needed). `sweef-install` or `python -m sweef` downloads the engine ahead of time and prints where it is (`--print-path` only looks, never downloads). `import sweef` has no side effects; the download happens on the first `Sweef.launch()` / `Sweef.session()`. In code, `sweef.ensure_binary()` returns the engine path, downloading it if necessary.

| Variable | Effect |
|---|---|
| `SWEEF_BIN` | use this binary, skip everything else |
| `SWEEF_VERSION` | engine version to use/download instead of the pinned one |
| `SWEEF_DOWNLOAD_BASE` | mirror root, layout `<base>/v<version>/<asset>` and `SHA256SUMS` (air-gapped or corporate mirrors; `http://` allowed) |
| `SWEEF_ENGINE_CACHE` | cache root instead of the per-user default |
| `SWEEF_SKIP_DOWNLOAD=1` | never download: behave as before and fail with the manual install instructions |
| `HTTPS_PROXY`, `NO_PROXY` | standard proxy variables are honored |

Air-gapped: mirror the release files under `$SWEEF_DOWNLOAD_BASE/v<version>/`, or put the engine on PATH / in `SWEEF_BIN` and set `SWEEF_SKIP_DOWNLOAD=1`. Uninstall: delete the cache directory (`<cache>/sweef/<engine-version>`); nothing else is written outside the package, and PATH is never modified.

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
