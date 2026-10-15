# @sweef/browser

Node.js SDK for [Sweef](https://github.com/sweefbrowser/sweef), a browser engine for AI agents written in Go. The API follows Playwright where it can (`page.goto`, `page.locator(...).click()`, `getByRole`, `page.keyboard`, `route`, `waitForURL`...), and pages are read as a YAML accessibility tree in which every control carries a `[ref=eN]` handle you can pass back as a selector.

Zero dependencies, types included, Node 16+. The SDK talks to a `sweef` binary over a local socket, so you need the binary too:

```bash
go install github.com/sweefbrowser/sweef/cmd/sweef@latest   # or download a release; then put it on PATH or set SWEEF_BIN
npm install @sweef/browser
```

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
