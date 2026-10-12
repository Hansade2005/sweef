# Sweef GUI: build contract

Sweef's own engine draws the browser chrome. The chrome is an HTML/CSS/JS app
("chrome page") loaded into a dedicated privileged engine Tab; page tabs are
other engine Tabs. The compositor stitches chrome pixels and the active page
pixels into one RGBA frame and hands it to a native `shell.Window`.

## Packages and owners (disjoint)

| Path | Owns |
|---|---|
| `internal/shell/{x11,win32,cocoa}` + `internal/shell/shell.go` | native windows (pure Go, no CGO) |
| `internal/agent` | Go agent loop, Anthropic client, context mgmt, tool backend |
| `internal/ui` (embed assets) | chrome HTML/CSS/JS, new tab page, sidebar UI, menus, incognito theme, design tokens, UI previews |
| `internal/app` | browser state (tabs, history, bookmarks, downloads, settings, sessions, privacy), compositor, input routing, `sweef gui` command wiring, headless frame mode |
| `packaging/` + `.github/workflows/release.yml` | installers for Linux, macOS, Windows |

Packages talk only through the interfaces and message formats below.

## Chrome page <-> Go bridge

In the chrome page, `window.sweef` exists:

* `sweef.post(type, payload)`: UI -> Go. Types: `nav.go {tab, input}`, `nav.back`, `nav.forward`,
  `nav.reload`, `nav.stop`, `tab.new {url?, incognito?}`, `tab.close {id}`, `tab.activate {id}`,
  `tab.move {id, to}`, `tab.pin {id}`, `omni.input {text}` (Go answers with `omni.suggestions`),
  `bookmark.toggle`, `menu.open {name}`, `agent.send {text, mode}`, `agent.stop`, `agent.takeControl`,
  `agent.pageVisibility {origin, allowed}`, `settings.set {key, value}`, `window.{minimize,maximize,close}`,
  `find.query {text}`, `downloads.open {id}`, `incognito.new`.
* `sweef.onMessage = (type, payload) => {}`: Go -> UI. Types: `state` (full snapshot: tabs, active tab, url,
  title, loading, canBack/Forward, security {level, certValid}, bookmarked, incognito, theme,
  sidebar {open, width}, settings), `state.patch` (RFC 7386 merge patch), `omni.suggestions {id, items}`,
  `agent.event {runId, kind: "text"|"thought"|"action"|"status"|"done"|"error"|"confirm", ...}`,
  `downloads.update`, `toast {text, kind}`.
* The chrome page owns layout of: tab strip, toolbar, omnibox, sidebar, popovers, find bar. Go tells it the
  window size via `state.window {w,h,scale}`. The page viewport area is a transparent hole
  (`<div id="viewport">`); Go reads its rect through `sweef.post("layout", {viewport:{x,y,w,h}})`
  whenever it changes, and composites the active page tab there.
* Chrome pages are served from `sweef://chrome/...` (embedded FS). `sweef://newtab`, `sweef://settings`,
  `sweef://history`, `sweef://downloads`, `sweef://bookmarks` are internal pages from the same FS.

## Compositor (internal/app)

1. Paint chrome tab -> RGBA (device pixels).
2. Paint active page tab for the viewport rect (existing `paintPage`/`PNGScreenshot` machinery, but
   to `*image.RGBA` with no PNG encode) -> blit into the hole.
3. Damage tracking: only repaint when a tab reports invalidation (`Tab` mutation counter) or input
   happened; `shell.Present(frame, damage)`.
4. Input: shell events -> hit test (chrome vs viewport) -> `Tab.Mouse*`, `Tab.Keyboard`, wheel, text.
   Cursor from CSS `cursor` of the hit element.
5. Frame budget: target <8 ms for a chrome-only repaint at 1x; report in `sweef gui --stats`.

## Agent <-> app

`internal/agent` defines `type Backend interface` (see that package). `internal/app` implements it on
top of `engine`/`rpc.Batch`. The agent never imports `internal/app` or `internal/ui`.
Agent events are plain structs the app forwards to the chrome page as `agent.event`.

## Honesty rules (product)

* Every public claim ("fastest", "lightest") must have a reproducible script in `bench/` and a measured
  number in the README. If we have not measured it, we do not say it.
* Known gaps are surfaced in-product: sites that need features we lack show a one-click
  "Open in system browser" banner. First installers are labelled **alpha**.
* Secrets: the Anthropic key is never embedded in the repo or installers. Users bring their own key
  (stored in the OS keychain where available, else a 0600 file); a hosted proxy URL can be configured.
