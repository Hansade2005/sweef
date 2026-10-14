# Frames, popups and windows

How `<iframe>` documents are painted and driven, how `window.open` and linked windows work, what is tested, and what does
not work. Frames as nested browsing contexts (separate document, origin, VM, partitioned storage, `postMessage`,
same-origin `contentDocument`, `sandbox`, `X-Frame-Options`, `frame-ancestors`) are described in
[storage-partitioning.md](storage-partitioning.md) and [web-platform.md](web-platform.md); this page covers what sits on
top: pixels, input, popups and the agent view. Code: `internal/engine/frames.go`, `frames_host.go`, `frame_prelude.js`
(the frames), `frames_paint.go`, `frames_input.go`, `frames_snapshot.go`, `windows.go`, `storage_events.go` (this layer),
and `internal/app` (`input.go`, `nav.go`, `tabs.go`) for the window.

## What works

| | |
|---|---|
| Painting | Layout emits a `render.Bitmap` op at the iframe's content box (`render.FrameEnv`); at raster time the frame is painted at the box size and the page's device scale on a transparent canvas and drawn like a canvas. So the box clip, border-radius, `overflow:hidden` ancestors, transforms, opacity and filters of ancestors, z-order, `visibility:hidden` and `display:none` behave as for any replaced element. A frame with no box size of its own is 300x150; the frame document is laid out at the size of its box. A frame that changes (timers, images, input) bumps the parent's `Generation()` and calls its update hook, so a window repaints. |
| Scrolling inside a frame | A frame scrolls inside its box (wheel over it, `Tab.WheelAt`); at either end the wheel goes back to the page. |
| `loading="lazy"` | The frame is created when its box is within 1250 CSS px of the viewport (checked after layout and on every scroll). |
| Pointer input | The parent's own hit test (`render.Result.HitAt`: paint order, clipping, ancestor transforms, `pointer-events`) finds the frame; the point is translated into the frame's document, through nested frames. `visibility:hidden` and `pointer-events:none` frames take nothing. Mouse move/down/up/click/wheel, hover leave, cursor and context menu (link and image under the pointer, selection) reach the frame. A frame whose sandbox lacks `allow-scripts` is clicked natively (links work without script). |
| Keyboard, text, clipboard | A click gives the frame keyboard focus (`Tab.FocusFrame`); key presses, typed text and copy/cut/paste/select-all go to the focused frame's focused element, and clicking back into the page moves focus (the old element is blurred). |
| `window.open(url, name, features)` | Returns a window proxy for a new tab (`opener`, `name`, `closed`, `focus()`, `close()`, `postMessage`, `location`), or for an existing window or frame of that name; `noopener` / `noreferrer` features return `null` and cut the opener. A popup of the same opener group is reached exactly like a frame: `postMessage` with origin checks, and for a same-origin window property access, `win.document`, `win.location.search` and calls such as `window.opener.onAuth(token)` (forwarded to the other VM). Another origin gets only `postMessage`, `closed`, `location` assignment, `parent`/`top`/`frames`. |
| Links and forms | `<a target>` / `<form target>`: `_self`, `_parent`, `_top`, `_blank`, a frame name, a window name (an existing window is reused). `target=_blank` has no opener unless `rel=opener` (the current browser default); `rel=noopener` / `noreferrer` always cut it. A form POST into a frame or a new window works. |
| Popup blocker | A window is created only during transient user activation: a click, tap or key press in the document (or in a frame of it) within the last 5 s in the engine, 2 s in the desktop app, consumed by the popup (one popup per gesture). Agent actions that stand in for a person (`Click`, `Press`, `Type`, `Keyboard`) count; `element.click()` from script does not. Otherwise `window.open` returns `null`, a `PopupEvent{Blocked:true}` is recorded (`Tab.BlockedPopups`, `Page.popups`, bus event `Page.popup`) and the app shows a toast. A sandboxed frame without `allow-popups` is blocked even with a gesture. |
| `window.close()` | Closes windows that script created (`window.open`, or a link or form `target` that opened a window); a console warning otherwise (a page cannot close the person's own tab, nor a tab opened by a plain link click in the app); never a frame. The opener sees `closed === true`. In the desktop app the tab closes and the opener comes back to the front. |
| COOP | `Cross-Origin-Opener-Policy` `same-origin`, `same-origin-allow-popups` and `unsafe-none` (`-plus-coep` read as `same-origin`) decide at every top-level navigation whether a popup and its opener may keep their references (`coopMatch`, with a table test). A cut link makes `window.opener` `null`, `closed` `true` for the other side, messages go nowhere, and the popup loses its name. An OAuth popup of a `same-origin-allow-popups` page keeps its opener. Not done: a separate agent cluster for the popup. |
| `storage` events | A `localStorage` change fires `storage` in the other documents that share the storage area (same partitioned key): other tabs, popups, frames. (`BroadcastChannel` across windows is the partitioned implementation of `partition.go`.) |
| Desktop browser | A popup is a tab right after its opener (and its earlier popups), activated, marked with an accent bar in the tab strip (`popup` in the tab state), not offered by "reopen closed tab", and `window.close()` returns to the opener. Only http, https and `about:blank` open as popups; `sweef://`, `file://` and `view-source:` cannot be opened by a web page into a window or frame. Background tabs pause their frames. |
| Agent, CLI, MCP | `Page.ariaSnapshot` includes the frames' accessibility trees under their `iframe` line (`[frame=f1]`); refs inside a frame are prefixed (`f1e5`, nested `f1f2e5`) and `click`, `fill`, `type`, `press`, `hover`, `check`, `select` and the other ref actions route to the frame, cross-origin or not. `Page.frames` lists the frames, `Page.popups` the popup decisions. The Sidekick reads a frame only when the site of the frame itself is one the person allowed (`Engine.SetFrameReadFilter`), not just the page that embeds it. Headless `Navigate` waits (up to 4 s) for the frames it started. |

## How it fits together

* **One window model.** Every `Tab` has a `winID`; frames are in `winReg`, popups and tabs too. A script holds ids; the Proxy in
  `frame_prelude.js` reaches another window through `frames_host.go`, which checks that the two windows are related: the
  same page tree, or the same opener group (`windowsRelated`: windows connected by non-severed opener links). Messages and
  navigation requests are queued on the target and run by the goroutine that owns it (a frame's loop, or a top-level
  window's worker through `Tab.queueTask`).
* **Frame loop.** Each frame has a goroutine (`bgRuntime`) that runs its event loop; after every slice it tells the parent if
  the frame changed (`frameTick`). Frames of a tab in the background are paused (`SetFramesPaused`).
* **Window hooks.** `Engine.SetPopupHandler`, `SetWindowCloseHandler`, `SetWindowFocusHandler`. Without a popup handler the
  engine navigates the popup itself in the background (CLI, tests).

## What does not work (known gaps)

* **Calls across windows are synchronous only into the other VM** (the frame machinery's rule): callbacks passed across run
  later and return nothing, DOM nodes cannot be passed between windows. `iframe.contentDocument` and `win.document` need the
  same origin.
* **No process isolation.** A frame is a goroutine and a VM in the same process; "cross-origin" means the origin checks, not
  site isolation. A popup does not get its own agent cluster.
* **Sandbox:** a popup opened by a sandboxed frame (with `allow-popups`) is not itself sandboxed; the flags that only gate UI
  (`allow-modals`, `allow-pointer-lock`, ...) are not enforced for popups.
* **Frame scrolling and size.** No scrollbars are drawn for frame content; no keyboard, touch or drag scrolling inside a frame
  (wheel only); `position:fixed` content inside a scrolled frame scrolls with it. A frame's size is its box (CSS or 300x150),
  never its content.
* **Compositing.** Frames are rasterised separately and drawn as bitmaps: sharp at whole-pixel positions and scales, resampled
  (slightly soft) otherwise, and a page that contains a frame is repainted in full (no incremental repaint) whenever the frame
  changes. Every frame costs a VM and a layout; a page with dozens of live frames is slow and memory-heavy.
* **Input details.** No IME composition events (typed text arrives as insertions, as for the page), no drag and drop across
  frames, the hover state of a frame is cleared by one synthetic `mousemove` outside it, `focus()` on a frame or its
  `contentWindow` does not move keyboard focus.
* **Popups.** `window.open` features other than `noopener`/`noreferrer` (size, position, `popup`) are ignored: a popup is a
  tab, not a small window. `window.name` is not cleared on cross-site navigation. Dialogs in frames and popups are answered
  automatically as for any tab.
* **Not verified.** Real third-party embeds (YouTube, Stripe Elements, reCAPTCHA, Google/Facebook sign-in) were not run against
  the live services in this change. The tests use local servers that behave like the pieces those flows depend on
  (cross-origin frames, a cross-origin popup that redirects back and posts to its opener, `window.close()`). Players that need
  MSE/EME or WebRTC inside a frame work exactly as far as they do at top level. The Win32 and Cocoa shells are build-tested only.

## Tests

`internal/engine`: `frames_test.go` and `partition*_test.go` (the frames and their storage, unchanged), `frames_helpers_test.go`
(pixels of a cross-origin frame in the parent's raster, shared test servers), `frames_input_test.go` (click into a
cross-origin frame, keyboard into a frame input, nested frames), `frames_more_test.go` (sandbox scripts, popups, forms, top
navigation, parent/frame `postMessage`, `load` event and script-inserted frames, snapshot refs through nested frames, srcdoc,
hidden / pointer-events:none frames, ancestor opacity and transform, lazy loading, repaint on frame change, scrolling inside a
frame, closing, request headers), `frames_layout_test.go` (border, padding, 2x scale), `frames_policy_test.go` (schemes, agent
read filter), `windows_test.go` (popup blocker, activation expiry and consumption, `target` links and named windows,
`noopener`, OAuth round trip with `postMessage` and `window.close()`, targetOrigin, structured clone, COOP, same-origin
`location` reads), `windows_sync_test.go` (`storage` events and `BroadcastChannel`), `windows_remote_test.go` (same-origin access between opener and popup). `internal/app`: `frames_popups_test.go`
(a frame in the window's pixels, click and typing into it with real shell events, the OAuth popup flow as tabs, blocked popup).
