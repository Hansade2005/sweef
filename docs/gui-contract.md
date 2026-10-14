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
* `sweef.sync` is `true` in the GUI host: the host settles the page and paints once after every message it delivers
  or a page event, and it answers `omni.input` inside the same event turn (`omni.suggestions`, or `{id, stale:true}`
  when it could not in time, the real answer then follows). A page that sees the flag builds its dropdown from the
  answer instead of painting its old rows first, and coalesces its renders with a microtask instead of a 16 ms timer.
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

## Tab audio

Each tab owns an `audioout` group (see [audio-output.md](audio-output.md)). The engine emits `EvAudio` with
`engine.AudioState{Audible, Muted}` when the tab starts or stops making sound (audible = the mixed peak of its
media elements and Web Audio contexts passed a threshold within the last 1.5 s) and when its mute changes.
`internal/app` turns the event into the `audible`/`muted` fields of the tab entry in the chrome state;
`tab.mute {id}` calls `Tab.SetAudioMuted`, and the mute is re-applied when a tab is recreated. The chrome shows
a speaker icon on the tab strip entry; clicking it sends `tab.mute`.

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

**Data Saver (see `docs/data-saver.md`).** Everything below is additive and optional on the UI side.

*Settings keys* (`settings.set {key,value}`): `data.level` (`auto|off|standard|strict`; `auto` is standard when a budget is set, else off; the resolved level is `state.data.level`), `data.budget` (object, see below), `data.sites` (`{origin: "full"|"lite"}`), `data.agentLite` (bool, default true), `data.showMeter` (bool, default true). `data.budget` is `{enabled, limitMB, period: "day"|"week"|"month", resetDay, warnAt:[70,90], hardStop}`; `resetDay` is 0..6 (Sunday=0) for `week`, 1..28 for `month`, ignored for `day`. `enabled:false` means "No limit": usage is still counted.

*state.data* (a merge patch on `state`, at most four updates per second while a page loads; byte values are bytes, `*MB` values are MB as floats):
`{level, levelSetting, available, used, usedToday, budget{enabled, limitMB, period, resetDay, warnAt, hardStop, usedMB, leftMB, pct, resetsInDays, resetsOn "YYYY-MM-DD", exhausted, usedBytes, limitBytes}, saved{measured, estimated}, top[{origin, bytes}] (five heaviest sites of the budget period, empty in a private window), tab{used, bySrc[{origin,bytes}] (top five for this page), skipped (request count), skippedBy{kind:count}, saved{measured, estimated}, approx:true, origin, rule "full"|"lite"|"", once, canLoad}, kinds{kind:bytes} (this page), agentLite, showMeter, sites{origin:rule}, incognito}`.
Honesty: `used`, `usedToday`, `budget.used*` are **measured** (socket bytes, private windows included). `saved.measured` is cache hits plus responses aborted at a size cap; `saved.estimated` is the engine's estimate for what policy skipped. The UI shows them as separate rows with a "Measured" or "Estimate" pill and never adds them. `tab.*` numbers are attributed per response and flagged `approx`. `available:false` means the engine does not count (the chip hides, no zeros are shown).

*UI -> Go*: `data.setLevel {level}`, `data.setBudget {...partial budget}` (merged into the stored budget), `data.loadFully {}` (the active tab, once), `data.loadSkipped {nodeId?}` (one placeholder by backend node id, or every withheld image on the page when `nodeId` is absent), `data.siteRule {origin, rule: "full"|"lite"|""}` (empty forgets), `data.reset {}` (restart the usage counter), `data.loadOnce {url?}` and `data.wait {}` (the hard-stop page). A click on an engine-drawn placeholder inside a page is resolved by the app itself (no message).
*Go -> UI*: `state.patch` on `data`; `toast {text, kind, action}` at each warn threshold and at the limit (once per period each; the 70% toast offers `data.setLevel {level:"strict"}`; the limit toast offers `tab.new {url:"sweef://settings#data"}`); `data.exhausted {limitMB, usedMB, period, resetsInDays, hardStop}` once per period when the budget is used up; saving a budget never toasts.
*agent.event done* gains `data {bytes, savedMeasured, savedEstimated, skipped, lite, available, text}` (the real counters of the tabs the run used); the Sidekick shows "Research used 412 KB" and, when present, the estimate labelled as one. It is omitted when `available` is false.

*Pages*: `sweef://datastop?url=<encoded>` (new page host `datastop`, in `ui.Pages`) is what a tab shows instead of `url` when the budget is used up and `hardStop` is on; the app injects `__sweefData {url, host, limitMB, usedMB, period, resetsInDays, resetsOn}` and keeps the requested address in the tab. Its three choices post `data.loadOnce`, `data.setBudget` followed by `nav.reload`, and `data.wait`. `sweef://settings#data` is the Settings section; `sweef://onboarding` has a fourth step ("Do you have a data plan?": 100 MB / 500 MB / 1 GB / No limit) that posts `data.setBudget` and `data.setLevel` (100 MB starts in Text first, 500 MB and 1 GB in Standard, No limit leaves the saver off) and posts nothing when skipped.

*Go API (internal/app)*: `Config.DataEngine` (an interface over the engine's Data Saver API, `dataengine_real.go` is the adapter, tests inject a fake with deterministic byte counts) and `Config.Now` (clock for the budget). `App.AgentDataBegin()` / `App.AgentDataEnd() AgentData` bracket an agent run; `agentbridge` calls them. Usage is kept per local day in `datausage.json` of the profile (`store.DataUsage`: top origins per day, private-window bytes as a bare total, thresholds announced once per period and remembered across restarts).

**Site storage (see `docs/storage.md`).** `state.data.storage` is `{sites: [{origin, bytes, persisted}], used, budget}`: what each site keeps on this device (IndexedDB, Cache Storage, the origin private file system, service worker scripts), largest first, at most 50, and the browser's storage budget; it is empty in a private window. UI -> Go: `data.storageClear {origin}` deletes everything that one site stored (open pages of the site get their database connections closed). `Go API`: `App.SetStoragePersistPrompt(func(origin string) bool)` installs the prompt behind `navigator.storage.persist()`; bookmarked sites are granted without asking, anything else is refused when no prompt is set.

**Notifications and permissions (see `docs/notifications.md`).** *state.perm*: `{available, dnd, push, default, site{origin, state: ask|allowed|blocked} (http(s) page, not private), prompt{id, origin, host, kind}, sites[{origin,state,embargo,periodic}] (only while sweef://settings is active), badges{origin: n|-1}}`. *UI -> Go*: `perm.answer {id, answer: allow|block|dismiss}`, `perm.set {origin, name?, state: granted|denied|""}`, `notify.click {id}`, `notify.action {id, action}`, `notify.closed {id, user}`; settings keys `notifications.dnd`, `notifications.default` (`ask|blocked`), `push.service`. *Go -> UI*: `toast {kind:"notify", id, title, text, origin, sticky, actions[{id,title}]}` (the in-window fallback for a notification); `ui.command {name:"perm.focus"}` (Alt+Shift+P while a question is waiting).
