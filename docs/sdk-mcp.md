# Agent interfaces: MCP server, Node and Python SDKs, CLI

Sweef offers four ways for an agent or a script to drive pages. All of them run the same engine and the same RPC methods, so anything one can do the others can too.

| Interface | Start it with | Best for |
|---|---|---|
| MCP server | `sweef mcp` (stdio) | LLM hosts: Claude Desktop, Claude Code, Cursor, any MCP client |
| Node SDK `@sweef/browser` | `Sweef.launch()` | scripts and agent frameworks in JavaScript/TypeScript |
| Python SDK `sweef-browser` | `Sweef.launch()` | scripts and agent frameworks in Python |
| Session CLI | `sweef open <url>` | agents that run shell commands; humans at a terminal |

Measured against `@playwright/mcp`, `playwright-core` and Vercel's `agent-browser` in [bench/surfaces](../bench/surfaces/README.md).

The SDK packages are publish-ready but not yet on npm or PyPI; until they are, install them from a checkout: `npm install ./pkg/sdk/node`, `pip install ./pkg/sdk/py`.

## MCP server

`sweef mcp` speaks MCP (protocol 2025-06-18, also 2025-03-26 and 2024-11-05) over stdin/stdout. The engine runs in the same process: there is no browser to download or launch.

### Host configuration

Claude Code:

```bash
claude mcp add sweef -- /usr/local/bin/sweef mcp
```

Claude Desktop (`claude_desktop_config.json`), Cursor (`~/.cursor/mcp.json` or `.cursor/mcp.json`) and most other hosts use the same shape:

```json
{
  "mcpServers": {
    "sweef": { "command": "/usr/local/bin/sweef", "args": ["mcp"] }
  }
}
```

Flags go after `mcp`, for example `["mcp", "--settle-ms", "800", "--user-agent", "my-agent/1.0"]`.

### Tools

| Group | Tools |
|---|---|
| Read | `browser_snapshot` (YAML accessibility tree with `[ref=eN]`; `interactiveOnly`, `delta`, `selector`, `maxChars`), `browser_get_text`, `browser_get_html`, `browser_get_attribute`, `browser_locator_count`, `browser_screenshot` |
| Navigate | `browser_navigate` (absolute URL or a path on the current site), `browser_back`, `browser_forward`, `browser_reload`, tabs (`browser_tab_new/list/close`) |
| Act | `browser_click`, `browser_fill`, `browser_type`, `browser_press`, `browser_select_option`, `browser_check`, `browser_uncheck`, `browser_hover`, `browser_drag`, `browser_scroll`, `browser_mouse`, `browser_keyboard` |
| Many steps at once | `browser_batch` (verbs or raw RPC steps, one observation at the end) |
| Wait | `browser_wait_for_selector`, `browser_wait_for_function`, `browser_wait_for_load_state`, `browser_wait_for_url`, `browser_wait` |
| JavaScript | `browser_evaluate`, `browser_add_init_script`, `browser_expose_binding`, `browser_binding_calls` |
| Network and logs | `browser_network_log`, `browser_console_log`, `browser_events_poll`, `browser_route_mock`, `browser_unroute`, `browser_har_export`, `browser_request` |
| State | `browser_storage_state`, `browser_set_storage_state`, `browser_new_context`, `browser_list_contexts`, `browser_close_context`, `browser_emulate`, `browser_dialog_respond` |
| Files | `browser_pdf`, `browser_download`, `browser_downloads` |
| Anything else | `browser_call` (any RPC method below), `browser_stats` |

Selectors everywhere accept a snapshot ref (`e12`), CSS, and the locator engines `text=`, `role=button[name="Sign in"]`, `label=`, `placeholder=`, `alt=`, `testid=`.

### What the tools return

The replies are written for a context window:

- `browser_navigate` returns two lines, title and URL; text, HTML and attribute tools return the value itself, not JSON around it; `browser_evaluate` returns the JSON value.
- After an action (click, fill, type, press, select, check, hover, drag, scroll, keyboard, mouse, back, forward, reload) the reply carries the snapshot lines that changed since your last `browser_snapshot` of the tab, with the same options. A navigation is reported in one line (`[navigated: <title> <url>; call browser_snapshot to read it]`), and so is a change larger than 2,500 characters. Before your first snapshot nothing is added. `SWEEF_MCP_AUTO_DELTA=0` turns this off.
- Console and network logs are one line per entry (`GET 200 https://… (text/html)`) with a `since` cursor for the next call; `verbose=true` gives the raw records.
- Text outputs are capped at 20,000 characters (`maxChars`) with a note giving the `offset` to continue from; snapshots have their own cap (`maxChars`, default 60,000).
- `browser_screenshot` returns a PNG image part only; `text=true` adds a text render of the page.
- Errors are a short message in an `isError` result.

## Node SDK

```bash
npm install @sweef/browser      # plus the sweef binary on PATH or in SWEEF_BIN
```

```js
const { Sweef } = require('@sweef/browser');
const sw = await Sweef.launch();
const page = await sw.newPage('https://example.com');
console.log(await page.snapshot({ interactiveOnly: true }));
await page.getByRole('link', { name: 'Learn more' }).click();
await sw.close();
```

`Sweef.launch()` starts a private `sweef serve` on a unix socket; `Sweef.connect({ addr })` attaches to a running one; `Sweef.session(name)` attaches to the session CLI's daemon so a script and `sweef click ...` share pages. The page API follows Playwright (`goto`, `locator`, `getByRole`, `keyboard`, `mouse`, `route`, `waitForURL`, `screenshot`, `pdf`, ...) plus `snapshot()`, `batch()` and `call()` for any RPC method. Full reference: [pkg/sdk/node/README.md](../pkg/sdk/node/README.md); types in `index.d.ts`.

## Python SDK

```bash
pip install sweef-browser       # plus the sweef binary on PATH or in SWEEF_BIN
```

```python
from sweef import Sweef

with Sweef.launch() as sw:
    page = sw.new_page("https://example.com")
    print(page.snapshot(interactive_only=True))
    page.get_by_role("link", name="Learn more").click()
```

Synchronous, standard library only, typed (`py.typed`). The same methods as the Node SDK in snake_case. Full reference: [pkg/sdk/py/README.md](../pkg/sdk/py/README.md).

## Session CLI

```bash
sweef open https://example.com      # starts the session daemon on first use
sweef snapshot -i                   # controls and headings with [ref=eN]
sweef click @e1
sweef batch "click @e1" "wait 500" "get title" --observe interactive
sweef rpc Page.frames               # any RPC method against the session
sweef --json get url                # machine-readable output
sweef close
```

`--session <name>` keeps several sessions apart; the daemon exits after an hour idle (`SWEEF_IDLE_TIMEOUT`). `sweef help` lists every verb.

## Environment

| Variable | Effect |
|---|---|
| `SWEEF_BIN` | the binary the SDKs start |
| `SWEEF_CACHE_DIR` | bytecode, HTTP and font-index caches (`off` disables) |
| `SWEEF_MCP_AUTO_DELTA=0` | no snapshot delta after MCP actions |
| `SWEEF_AUTOPLAY=1` | let pages autoplay media (off for agents) |
| `SWEEF_ALLOW_TRACKERS=1` | load known third-party trackers (blocked for agents) |
| `SWEEF_SESSION`, `SWEEF_HOME`, `SWEEF_IDLE_TIMEOUT` | session CLI name, socket directory, idle exit |

## RPC methods

Every interface reaches these (MCP through its tools or `browser_call`, the SDKs through their methods or `call()`, the CLI through its verbs or `sweef rpc`):

| Domain | Methods |
|---|---|
| Batch | run |
| Browser | newTab, closeTab, listTabs, newContext, newTabInContext, closeContext, listContexts, getVersion, stats, shutdown |
| BrowserContext | storageState, saveStorageState, loadStorageState, addCookies, clearCookies |
| Page | navigate, goBack, goForward, reload, title, url, content, textContent, ariaSnapshot, snapshot, screenshot, pdf, frames, popups, armDialog, getDialogs, addInitScript, removeInitScript, download, downloads, getStateLog, waitForSelector, waitForFunction, waitForLoadState, waitForURL, waitForTimeout |
| Input | click, fill, type, press, check, uncheck, selectOption, hover, focus, tap, drag, scroll, mouse, keyboard |
| Locator | count, textContent, innerHTML, getAttribute, boundingBox, isVisible, isEnabled, isEditable, isChecked |
| DOM | querySelector, querySelectorAll, getAttributes, getOuterHTML, resolveLocator |
| Runtime | evaluate, addBinding, bindingCalls, setBindingReply |
| Network | getLogs, har, route, routes, unroute, unrouteAll, setExtraHTTPHeaders |
| Emulation | setDeviceMetrics, setUserAgentOverride, setGeolocation, grantPermissions, setOffline, setColorScheme |
| Log, Events, Fetch, Storage | getConsole; poll; request; getScripts |

The wire format (`SWF1` framing) is described in `internal/protocol/wire.go`.
