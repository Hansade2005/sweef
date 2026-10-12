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
   The chrome repaints incrementally (`engine.PaintOptions.Incremental`): the engine diffs the new
   display list against the previous one and repaints only the rows the difference can reach, with
   the same pixels as a full paint (`internal/engine/paint_golden_test.go`). A forced paint (resize,
   scale change) is always a full one. `SWEEF_INCR_CHECK=1` compares every incremental paint with a
   full paint and repairs a difference (development aid).

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

## UI additions (internal/ui)

Everything below is additive. The UI tolerates missing fields, nulls and partial snapshots.

**Serving.** `ui.FS` root holds `chrome/ newtab/ settings/ history/ downloads/ bookmarks/ onboarding/ error/ shared/`.
`ui.Route(*url.URL)` maps `sweef://<page>[/file]` to a path in `ui.FS` (`sweef://newtab` -> `newtab/index.html`;
`sweef://<any page>/shared/x.css` -> `shared/x.css`; `sweef://ui/<path>` -> `<path>`). Pages use root-relative
`/shared/...` and page-relative assets. The chrome page is `chrome/index.html`; the agent overlay is a second transparent
surface, `chrome/overlay.html`, same size as the window. All pages get the same `window.sweef` and post `ui.ready {page}` once loaded
(Go answers with `state`). Pages accept `?theme=dark|light|incognito`, `?platform=darwin|windows|linux`.

**Engine facts the UI relies on.** The engine has no native element scrolling: the UI scrolls with transforms and needs DOM
`wheel` events (deltaY in px) delivered to the chrome tab. Hit-testing: a point belongs to the page tab iff
`elementFromPoint` there is `#viewport` or one of its ancestors; anything else (popovers, dropdowns, find bar, toasts, scrims) is chrome.
For the overlay surface, mouse events go to the overlay only inside the rects from `overlay.hit`.
Elements with `-webkit-app-region: drag` (class `.drag`) are window-drag handles; the UI also posts `window.drag {x,y}` on mousedown there.

**state fields read** (all optional): `platform, version, theme ("light"|"dark"|"system"), systemDark, incognito, window{w,h,scale,maximized}, tabs[{id,title,url,favicon,loading,audible,muted,pinned,crashed}], active, url, title, loading, progress (0..1), canBack, canForward, security{level: secure|insecure|internal|warning|none, certValid}, bookmarked, bookmarksBar[{title,url,favicon}], sidebar{open,width}, settings{...}, downloads[{id,name,url,state: progress|done|failed|paused,received,total,speed}], topSites[{title,url,favicon}], stats{memMB,tabs} (new tab card renders only if present), compat{url,message}, defaultBrowser, page{type: article|shop|form|video|any}, agent{state: idle|working|waiting|paused|error|off, hasKey, model, pageVisibility: allowed|blocked|ask (for the active origin), chats[{id,title,when}], chat{title}, sites[{origin,allowed}]}`.

**settings keys** (`settings.set {key,value}`): `theme, density, sidebar.open, sidebar.width (320..560), agent.mode (ask|agent), agent.latencyStrip, agent.sources.page, agent.sources.web, agent.confirmations, agent.visibilityDefault (ask|allowed|blocked), privacy.trackers (standard|strict|off), privacy.gpc, privacy.httpsUpgrade, privacy.clearOnExit, search.engine, search.suggest, omni.askRow, startup.mode, startup.pages, downloads.dir, downloads.ask, bookmarks.bar`.

**UI -> Go (new types):** `ui.ready {page}`; `omni.input` gains `{id, scope: bar|newtab|palette, limit}`; `nav.go` from the new tab has `tab:""` (= sender's tab);
`agent.send` gains `{chat, page{url,title,tab}, attachments[{kind,tab}], sources{page,web}, source}`; `agent.confirm {runId,id,approved}`; `agent.resume`; `agent.setKey {key}`; `agent.clearKey`;
`agent.chat.new`, `agent.chat.open {id}`; `agent.pageVisibility {origin, allowed: true|false|null(forget)}`; `tab.mute {id}`; `find.step {dir:1|-1}`, `find.close`;
`window.drag {x,y}`, `window.fullscreen`; `downloads.cancel|retry|remove|show {id}`, `downloads.clear`, `downloads.chooseFolder`; `clipboard.write {text}`; `compat.dismiss {origin}`;
`openExternal {url}`; `menu.open {name}` with name in `print, zoom.in, zoom.out, zoom.reset, certificate, cookies, reportSite`; `layout {viewport}` (as before); `overlay.hit {rects[{x,y,w,h}]}`;
`data.query {kind: history|bookmarks, q, offset, limit, id}`, `data.remove {kind,id}`, `data.clear`; `import.start {source: chrome|edge|firefox|safari}`; `defaultBrowser.set`; `onboarding.done {trackers,visibility}`, `onboarding.skip`; `cert.proceed {url}`.

**Go -> UI (new types):** `omni.suggestions {id, inline?, items[{type: history|bookmark|tab|search|nav, title, url, text, favicon, tab, inline}]}`;
`agent.event` kinds: `text {text, full?}`, `thought {text, done?}`, `action {id,label,detail,status: running|ok|error|pending,ms}`, `status {state,text}`, `confirm {id,title,risk,action{...}}`, `sources {items[{title,url}]}`, `done {ttft,tps,ms,sources}`, `error {code: auth|rate_limit|network|..., message, retryAfter}`; `runId` ties events to one reply (events may arrive before UI knows it: the first one is adopted);
`agent.chat.load {id,title,messages[{role,text}]}`; `overlay {state: off|acting|control|done|stopped, viewport{x,y,w,h}, task, step, cursor{x,y,label,pressed}}` (to the overlay surface);
`find.result {count,index}`; `ui.command {name}` (names: `palette, find.open, focusOmnibox, sidebar.toggle, sidebar.focus, tab.new, tab.closeActive, nav.reload, bookmark.toggle, agent.chat.new, incognito.new`) so the app can forward accelerators that arrive while the page tab has focus (the chrome also handles Ctrl/Cmd+K, L, F, T, W, D and Shift+A itself when it has focus: do not also act on those);
`data.result {kind,id,offset,total,items[{id,title,url,ts,favicon}]}`; `downloads.update` (item, array or `{items}`; merged by id); `import.progress {source,pct}`, `import.done {source,bookmarks|summary}`, `import.unavailable {source}`; `defaultBrowser.result {ok}`; `agent.keyResult {ok}`; `onboarding.step {n}`; `toast {text,kind,action{label,type,payload}}`.
