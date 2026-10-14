# Frames, popups and windows

How `<iframe>` documents, `window.open` and linked windows work in the engine and in the desktop browser, what is
tested, and what does not work. Code: `internal/engine/frames*.go`, `windows.go`, `sandbox.go`, `frameguard.go`,
`frames_prelude.js`; the window side in `internal/app` (`input.go`, `nav.go`, `tabs.go`).

Before this work the engine fetched no iframe documents at all (a frame was an empty box, `contentWindow` was `null`),
`window.open` opened a tab from the chrome with no link to the page that asked, and there was no `window.opener`,
`postMessage` between windows, or popup blocker beyond a 2-second click check.

## What works

| | |
|---|---|
| Frame documents | Every `<iframe>` is a child `Tab` with its own JavaScript VM, document, styles and layout. It starts loading when the element enters the DOM (also `display:none`, also script-inserted), follows `src` / `srcdoc` changes, fires the element's `load` event, and is closed with its parent. Nested frames work (depth 6, 64 frames per document). |
| Painting | Layout emits a `render.Bitmap` op at the iframe's content box (`render.FrameEnv`); at raster time the frame is painted at the box size and the page's device scale on a transparent canvas and drawn like a canvas. So the box clip, border-radius, `overflow:hidden` ancestors, transforms, opacity and filters of ancestors, z-order, `visibility:hidden` and `display:none` behave as for any replaced element. Borders and padding of the iframe are the parent's own. A frame that changes (timers, images, input) bumps the parent's `Generation()` and calls its update hook, so a window repaints. |
| Scrolling inside a frame | A frame scrolls inside its box (wheel over it, `Tab.WheelAt`); at either end the wheel goes back to the page. |
| `loading="lazy"` | The frame starts when its box is within 1250 CSS px of the viewport (checked after layout and on every scroll). A headless snapshot starts lazy frames it needs. |
| `srcdoc`, `about:blank` | Supported. The document inherits the origin of the embedder. |
| `sandbox` | `allow-scripts` (without it page scripts never run in the frame; the browser's own helper script still does), `allow-same-origin` (without it the origin is opaque: `location.origin` and message origins are `"null"`, Web Storage is a throw-away area), `allow-popups`, `allow-popups-to-escape-sandbox`, `allow-forms`, `allow-top-navigation`, `allow-top-navigation-by-user-activation`. Restrictions accumulate through nested frames and are inherited by popups unless they escape. |
| Framing policy | `X-Frame-Options: DENY / SAMEORIGIN` and CSP `frame-ancestors` (`'none'`, `'self'`, `*`, scheme, host and port sources) are enforced against every ancestor: the frame shows a "cannot be shown in a frame" page and a console error. `sweef://`, `file://` and `view-source:` cannot be loaded into a frame or window by a web page. |
| Pointer input | The parent's own hit test (`render.Result.HitAt`: paint order, clipping, ancestor transforms, `pointer-events`) finds the frame; the point is translated into the frame's document (through nested frames). `visibility:hidden` and `pointer-events:none` frames take nothing. Mouse move/down/up/click/wheel, hover leave, cursor, context menu (link and image under the pointer, selection) reach the frame. |
| Keyboard, text, clipboard | A click gives the frame keyboard focus (`Tab.FocusFrame`); key presses, typed text, copy/cut/paste/select-all go to the focused frame's focused element, and clicking back into the page moves focus (the old element is blurred). |
| `window.open(url, name, features)` | Returns a `WindowProxy` for a new tab (`opener`, `name`, `closed`, `focus()`, `close()`, `postMessage`, `location` (readable for same-origin windows, assignable for any), `parent` / `top` / `frames`), or an existing window or frame of that name. `noopener` / `noreferrer` features return `null` and cut the opener. |
| Links and forms | `<a target>` / `<form target>`: `_self`, `_parent`, `_top`, `_blank`, a frame name, a window name (an existing window is reused). `target=_blank` has no opener unless `rel=opener` (the current browser default); `rel=noopener` / `noreferrer` always cut it. Form POST into a frame or a new window works. |
| Popup blocker | A window is created only during transient user activation: a click, tap or key press in the document (or in a frame of it) within the last 5 s in the engine, 2 s in the desktop app, consumed by the popup (one popup per gesture). Agent actions that stand in for a person (`Click`, `Press`, `Type`, `Keyboard`) count; `element.click()` from script does not. Otherwise `window.open` returns `null`, a `PopupEvent{Blocked:true}` is recorded (`Tab.BlockedPopups`, `Page.popups`, bus event `Page.popup`) and the app shows a toast. Sandboxed documents without `allow-popups` are blocked even with a gesture. |
| `window.close()` | Closes windows that script created (`window.open`, or a link or form `target` that opened a window); a console warning otherwise (a page cannot close the person's own tab, nor a tab opened by a plain link click in the app); never a frame. The opener sees `closed === true`. In the desktop app the tab closes and the opener comes back to the front. |
| `BroadcastChannel`, `storage` events | Delivered to the other same-origin windows (and frames) of the browser context; opaque origins hear nothing. |
| `postMessage` | Between a page and its frames (both ways), the opener and its popups, and between popups, with `targetOrigin` (`*`, `/`, an origin) enforced, `event.origin`, `event.source` (the same proxy object as `window.open` / `contentWindow` returned), replies through `event.source`. A document can address only windows it was handed. Messages are cloned through a JSON envelope that keeps `Date`, `Map`, `Set`, `ArrayBuffer` and typed arrays, `undefined` and non-finite numbers; other class instances arrive as plain objects. |
| COOP | `Cross-Origin-Opener-Policy` `same-origin`, `same-origin-allow-popups` and `unsafe-none` (and `-plus-coep` read as `same-origin`) decide at every top-level navigation whether the popup and its opener may keep their references (`coopMatch` in `windows.go`, with a table test). A cut link makes `window.opener` `null` in the popup, `closed` `true` and messages go nowhere on the other side; the popup loses its `name`. An OAuth popup of a `same-origin-allow-popups` page keeps its opener. |
| Desktop browser | A popup is a tab right after its opener (and its earlier popups), activated, marked with an accent bar in the tab strip (`popup` in the tab state), not offered by "reopen closed tab", and `window.close()` returns to the opener. Only http, https and `about:blank` open as popups. Background tabs pause their frames. |
| Agent, CLI, MCP | `Page.ariaSnapshot` includes the frames' accessibility trees under their `iframe` line (`[frame=f1]`); refs inside a frame are prefixed (`f1e5`, nested `f1f2e5`) and `click`, `fill`, `type`, `press`, `hover`, `check`, `select` and the other ref actions route to the frame, cross-origin or not. `Page.frames` lists the frames, `Page.popups` the popup decisions. The Sidekick reads a frame only when the site of the frame itself is one the person allowed (`Engine.SetFrameReadFilter`), not just the page that embeds it. |
| Data Saver | A third-party frame under `ClickToLoad3PFrames` is still a placeholder and loads nothing until tapped; tapping now loads the real frame. |

## How it fits together

* **Window ids.** Every `Tab` is a browsing context (top-level tab, popup or frame) with an id. JavaScript holds ids only;
  `__sw.win*` host functions check that the calling document was handed the id before doing anything, and
  one VM never touches another: messages and navigation requests are queued on the target `Tab` (`postMessageTo`,
  `queueTask`) and run by the goroutine that owns it (the window's worker, or the frame's own loop).
* **Frame loop.** One goroutine per frame loads the document (progressively), runs its event loop in short slices, and
  sleeps when it has nothing to do (woken by messages, images, input, scroll). `Settle` and the window's pump are not
  needed for frames. Headless `Navigate` waits (up to 4 s) for the frames it started so a snapshot taken right after
  contains them.
* **Window hooks.** `Engine.SetPopupHandler`, `SetWindowCloseHandler`, `SetWindowFocusHandler`. Without a popup handler
  the engine navigates the popup itself in the background (CLI, tests).

## What does not work (known gaps)

* **No cross-window DOM access.** `iframe.contentDocument`, `win.document`, `frameElement` and `document.write` into a
  popup are not available (`contentDocument` is `null`; `win.document` throws). Same-origin frames cannot script each
  other; `postMessage` is the only channel (plus reading a same-origin window's `location`). Libraries that reach into a
  same-origin frame's DOM, or call a function the opener defined (`window.opener.onAuth(token)`), will not work.
* **No storage partitioning.** Frames use the same Web Storage and cookie jar entries as the same origin at top level.
  Third-party cookies follow the existing setting (the app's cookie sweep now sees frame traffic because frames share
  the page's network log). There is no Storage Access API and `SameSite` is not applied to frame requests.
* **No process isolation.** A frame is a goroutine and a VM in the same process; "cross-origin" means the origin checks
  above, not site isolation.
* **Messaging.** `MessagePort` transfer and `MessageChannel` across windows do not work (`BroadcastChannel` and
  `localStorage` `storage` events do reach other windows of the same origin and browser context, not `sessionStorage`
  events). Structured clone is the JSON envelope above (no class instances,
  `Blob`, `File`, `ImageBitmap`, cycles, `BigInt`).
* **Frame scrolling and size.** No scrollbars are drawn for frame content; no keyboard, touch or drag scrolling inside a
  frame (wheel only); `position:fixed` content inside a scrolled frame scrolls with it. A frame's size is its box (CSS
  or 300x150), never its content.
* **Compositing.** Frames are rasterised separately and drawn as bitmaps: sharp at whole-pixel positions and scales, resampled
  (slightly soft) otherwise, and a page that contains a frame is repainted in full (no incremental repaint) whenever
  the frame changes. Every frame costs a VM and a layout; a page with dozens of live frames is slow and memory-heavy.
  Background tabs pause their frames, and a hidden-by-Data-Saver frame loads nothing.
* **Input details.** No IME composition events (typed text arrives as insertions, as for the page), no drag and drop
  across frames, the hover state of a frame is cleared by one synthetic `mousemove` outside it, `focus()` on a frame
  or its `contentWindow` does not move keyboard focus.
* **Popups.** `window.open` features other than `noopener`/`noreferrer` (size, position, `popup`) are ignored: a popup
  is a tab, not a small window. A popup of `about:blank` cannot be written into from the opener (no `document.write`).
  `window.name` is not cleared on cross-site navigation. Dialogs (`alert`, `confirm`, `prompt`) in frames and popups are
  answered automatically as for any tab.
* **Sandbox.** `allow-modals`, `allow-downloads`, `allow-pointer-lock`, `allow-orientation-lock`, `allow-presentation`
  are not enforced; an opaque origin does not block `document.cookie`; `allow-top-navigation-by-user-activation` is
  approximated by the 5 s / 2 s activation rule.
* **Policies not implemented.** `Permissions-Policy` / the `allow` attribute, `Cross-Origin-Embedder-Policy` /
  `Cross-Origin-Resource-Policy` for frames, `referrerpolicy` on iframes, `csp` attribute, `credentialless`.
* **Not verified.** Real third-party embeds (YouTube, Stripe Elements, reCAPTCHA, Google/Facebook sign-in) were not run
  against the live services in this change. The tests use local servers that behave like the pieces those flows depend
  on (cross-origin frames, a cross-origin popup that redirects back and posts to its opener, `window.close()`).
  Players that need MSE/EME or WebRTC inside a frame work exactly as far as they do at top level. The Win32 and Cocoa
  shells are build-tested only.

## Tests

`internal/engine`: `frames_test.go` (pixels of a cross-origin frame in the parent's raster), `frames_input_test.go`
(click into a cross-origin frame, keyboard into a frame input, nested frames), `frames_more_test.go` (sandbox scripts,
popups, forms, top navigation, parent/frame `postMessage`, `load` event and script-inserted frames, snapshot refs through
nested frames, srcdoc, hidden / pointer-events:none frames, ancestor opacity and transform, lazy loading, repaint on
frame change, scrolling inside a frame, closing, request headers), `frames_layout_test.go` (border, padding, 2x scale),
`frames_guard_test.go` (X-Frame-Options, frame-ancestors), `frames_policy_test.go` (schemes, agent read filter),
`windows_test.go` (popup blocker, activation expiry and consumption, `target` links and named windows, `noopener`,
OAuth round trip with `postMessage` and `window.close()`, targetOrigin, structured clone, COOP, same-origin `location` reads), `windows_sync_test.go` (`storage` events and `BroadcastChannel`). `internal/app`:
`frames_popups_test.go` (a frame in the window's pixels, click and typing into it with real shell events, the OAuth
popup flow as tabs, blocked popup).
