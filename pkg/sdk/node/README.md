# @sweef/browser

Node.js SDK for [Sweef](https://github.com/sweefbrowser/sweef), a browser engine for AI agents written in Go. The API follows Playwright where it can (`page.goto`, `page.locator(...).click()`, `getByRole`, `page.keyboard`, `route`, `waitForURL`...), and pages are read as a YAML accessibility tree in which every control carries a `[ref=eN]` handle you can pass back as a selector.

Zero dependencies, types included, Node 16+. The SDK talks to the `sweef` engine over a local socket; it installs the engine for you on first use, so this is all you need:

```bash
npm install @sweef/browser
```

## The sweef engine: installed for you

You do not download Sweef or edit PATH. The SDK finds the engine in this order:

1. the `binary` option (`Sweef.launch({ binary })`)
2. `$SWEEF_BIN`
3. `sweef` on PATH
4. the per-user cache: `$XDG_CACHE_HOME/sweef/<engine-version>/sweef` (default `~/.cache/...`), `~/Library/Caches/sweef/...` on macOS, `%LOCALAPPDATA%\sweef\cache\<engine-version>\sweef.exe` on Windows
5. otherwise it downloads the pinned engine (`0.1.0-alpha.4`) from the [GitHub release](https://github.com/sweefbrowser/sweef/releases) into that cache, once, and prints one line to stderr. The archive's SHA-256 is checked before anything is executed; the SDK pins the expected hashes for its default version and falls back to the release's `SHA256SUMS` for other versions.

`npm install -g @sweef/browser` also puts a `sweef` command on PATH (it runs the same engine, downloading it first if needed). `npx -p @sweef/browser sweef-install` downloads the engine ahead of time and prints where it is (`--print-path` only looks, never downloads). A best-effort `postinstall` step fetches the engine at `npm install`; it never fails the install and is skipped with `SWEEF_SKIP_DOWNLOAD=1` or `npm install --ignore-scripts` (the engine is then fetched on first use). In code, `await ensureBinary()` returns the engine path, downloading it if necessary.

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

```js
const { Sweef } = require('@sweef/browser');

(async () => {
  const sw = await Sweef.launch();                       // starts `sweef serve` on a private socket
  const page = await sw.newPage('https://example.com');
  console.log(await page.title());                       // "Example Domain"
  console.log(await page.snapshot({ interactiveOnly: true }));
  // - link "Learn more" [ref=e1]:
  //   - /url: https://iana.org/help/example-domains
  const png = await page.screenshot();                   // PNG bytes (Buffer)
  console.log(png.length, 'bytes of PNG');
  await sw.close();                                      // stops the server it started
})();
```

## Connecting

| | |
|---|---|
| `Sweef.launch({ binary, args, addr, timeout })` | start a private `sweef serve` and connect; `close()` stops it |
| `Sweef.connect({ addr })` | attach to a running `sweef serve --addr ...` (`'unix:/path'`, `'host:port'`, or `{ host, port }`) |
| `Sweef.session(name)` | attach to the session CLI's daemon, so a script and `sweef open/click/...` drive the same pages |

## Pages

```js
const page = await sw.newPage();
await page.goto('https://news.ycombinator.com/');
const tree = await page.snapshot();                      // YAML; options: interactiveOnly, delta, selector, maxChars
const ref = tree.match(/link "new" \[ref=(e\d+)\]/)[1];
await page.click(ref);                                    // refs, CSS, text=, role=, label=, placeholder=, testid=
await page.getByRole('link', { name: 'past' }).click();
console.log(await page.snapshot({ delta: true }));       // only the lines that changed
```

- Reading: `title()`, `url()`, `content()`, `textContent(sel)`, `snapshot(opts)`, `accessibilityTree()`, `screenshot({ fullPage, width, height, path })`, `screenshotText()`, `pdf({ path, format })`, `frames()`, `popups()`.
- Acting: `click`, `dblclick`, `fill`, `type`, `press` (keys and combinations such as `Control+a`), `check`, `uncheck`, `selectOption`, `hover`, `focus`, `tap`, `dragAndDrop`, `scroll`, `keyboard.type/press`, `mouse.click/move/down/up/wheel`.
- Locators: `page.locator(sel)` with `click`, `fill`, `textContent`, `innerHTML`, `getAttribute`, `inputValue`, `count`, `isVisible`, `isChecked`, `boundingBox`, `waitFor`, `nth`, `first`, `last`.
- JavaScript: `evaluate(expr | fn, arg)`, `addInitScript`, `exposeBinding` + `bindingCalls`.
- Waiting (the page keeps running meanwhile): `waitForSelector`, `waitForFunction`, `waitForLoadState`, `waitForURL`, `waitForTimeout`.
- Network: `route(glob, { json | body, status, action })`, `unroute`, `unrouteAll`, `routes`, `networkLog`, `har`, `setExtraHTTPHeaders`, `downloads`, `download`.
- Logs and dialogs: `consoleMessages`, `events`, `armDialog('accept')`, `dialogs`.
- Emulation: `setViewportSize`, `setUserAgent`, `setGeolocation`, `grantPermissions`, `setOffline`, `emulateMedia({ colorScheme })`.

## Batches: many steps, one round trip

```js
const r = await page.batch([
  'open https://the-internet.herokuapp.com/login',
  'fill #username tomsmith',
  'fill #password SuperSecretPassword!',
  'click button[type=submit]',
  'expect text You logged into a secure area',
  { method: 'Page.title' },                      // any RPC method works as a step
], { observe: 'interactive' });
console.log(r.ok, r.totalMs, r.observation);
```

## Contexts

```js
const ctx = await sw.newContext({ userAgent: 'my-agent/1.0', viewport: { width: 390, height: 844 } });
const p = await ctx.newPage('https://example.com');
console.log(await ctx.cookies());
await ctx.storageState({ path: 'state.json' });
await ctx.close();
```

## Anything else

Every RPC method of the engine is reachable with `sw.call(method, params)`; see the method list in the main README.

## Tests

`SWEEF_BIN=/path/to/sweef npm test` runs the suite against a real `sweef serve` and a local HTTP fixture (no network).

License: Apache-2.0.
