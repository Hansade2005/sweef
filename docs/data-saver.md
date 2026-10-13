# Data Saver: build contract

Goal: a person on a 100 MB plan can research and explore for a long time. Everything happens on the device. There is **no
proxy and no server-side transcoding** (that is how Chrome Lite Mode and Opera Turbo worked, and it sent every page through
the vendor). Savings come from fetching less, fetching later, caching harder, and telling the person where their data went.

## Honesty rules

* "Used" is **measured**: bytes actually read from and written to sockets, counted in a `net.Conn` wrapper (TLS overhead
  included, TCP/IP headers not). Per-tab and per-site numbers are attributed from response header+body transfer sizes and are
  labelled approximate when they differ from the socket total.
* "Saved" is only claimed from things we can measure: responses aborted early because `Content-Length` exceeded the cap
  (bytes not read), cache hits (stored transfer size), and requests blocked whose size was learned from the cache or a
  `Content-Length`. Everything else is shown as a count ("23 requests skipped"), or as an **estimate** that is flagged as one in
  the UI (median size per resource type, documented in code). Never present an estimate as a measurement.
* The headline claim ("N% less data") may only come from `bench/datasaver` (same pages, same machine, same network, three
  modes plus real Chromium measured through CDP). The README quotes that table and nothing else.

## Levels

| Level | What it does |
|---|---|
| `off` | Counting and the budget still work; nothing is blocked. |
| `standard` | Sends `Save-Data: on`; blocks ads/trackers (existing shield); no autoplay and no media preload (video/audio download only when the person presses play); images load lazily (only near the viewport) and any image whose `Content-Length` exceeds `ImageMaxBytes` (default 200 KB) is replaced by a tap-to-load placeholder; web fonts load only when the page's text uses them, only as WOFF2/WOFF, up to 300 KB per font and 600 KB per page (the rest show their fallback); third-party iframes are click-to-load; `prefetch`/`preload`/`prerender`/`dns-prefetch` hints and speculative loads are dropped; background tabs get no timers, polling or new network requests; the HTTP cache is extended for static assets (ignore max-age < 1 day for images/fonts/css/js with validators; always revalidate with ETag/If-Modified-Since instead of refetching); `Accept-Encoding: br, gzip` on everything. |
| `strict` (text first) | Everything in standard plus: no images unless tapped (placeholders keep their layout size when `width`/`height` are known), no web fonts (icon fonts excepted, or the page would show words such as `arrow_back`), no media, no third-party scripts, first-party scripts only up to `ScriptMaxBytes` (default 300 KB total per page), CSS kept. "Load full page" button always available. |

Per-site exceptions (`always full`, `always lite`) are stored by origin. A one-tap "Load this page fully, once" override exists.

## Engine API (owner: engine/net team) in package `engine`

```go
type SaverLevel int // SaverOff, SaverStandard, SaverStrict

type DataPolicy struct {
    Level            SaverLevel
    ImageMaxBytes    int   // standard: tap-to-load above this (default 200<<10); 0 = no cap
    ScriptMaxBytes   int   // strict: total first-party script budget per page (default 300<<10)
    LazyImages       bool  // fetch only near the viewport
    BlockFonts       bool
    FontMaxBytes, FontTotalBytes int // additive: per-font and per-page web font caps (standard)
    FontWOFFOnly     bool           // additive: only WOFF2/WOFF sources (standard)
    BlockMediaLoad   bool  // no preload/autoplay; fetch on play only
    ClickToLoad3PFrames bool
    DropHints        bool  // prefetch/preload/prerender/dns-prefetch
    SuspendBackground bool // background tabs: no timers, no requests
    ExtendCache      bool
    SaveDataHeader   bool
}
func PolicyFor(level SaverLevel) DataPolicy            // the table above

func (c *BrowserContext) SetDataPolicy(DataPolicy)
func (t *Tab) SetDataPolicy(*DataPolicy)               // per-tab override (nil = inherit), used for "load fully once"

type DataKind string // "document","script","style","image","font","media","xhr","frame","other"

type DataStats struct {
    WireIn, WireOut   int64               // exact socket totals since the context was created
    ByKind            map[DataKind]int64  // transfer bytes by kind (approx attribution)
    ByOrigin          map[string]int64    // approx
    CacheHitBytes     int64               // bytes served from cache (measured)
    AbortedBytes      int64               // bytes not read because of a size cap (measured)
    Skipped           map[DataKind]int    // request counts skipped by policy
    SkippedEstimate   int64               // ESTIMATED bytes avoided (flagged as estimate everywhere)
}
func (c *BrowserContext) DataStats() DataStats
func (t *Tab) DataStats() DataStats                    // this tab since its last navigation
func (e *Engine) Bus()  // emits Event{Type:"data", Tab:id} throttled to 4/s while loading
```

Placeholders: skipped images become an in-DOM element `<img data-sweef-lite="skipped" ...>` that the layout engine draws as a
labelled box with the declared size (or 300x150); `Tab.LoadSkipped(nodeID)` fetches and swaps one resource (used by tap).
`Tab.LoadFully()` reloads without policy once.

Agent loads: `engine` exposes `agent` loading mode = `SaverStrict` minus script limits (agents read the accessibility snapshot,
they never need images, fonts or media). The agent bridge opens agent-driven loads with it unless the user turned it off.

## App and UI API (owner: app/ui team)

Settings (`settings.set`): `data.level` ("off"|"standard"|"strict", default "standard" when a budget is set, else "off"),
`data.budget` `{enabled, limitMB, period: "day"|"week"|"month", resetDay, warnAt:[70,90], hardStop}`, `data.sites`
`{origin: "full"|"lite"}`, `data.agentLite` (bool, default true), `data.showMeter` (bool).

State to the chrome page: `state.data = {level, used, usedToday, budget:{limitMB,period,usedMB,pct,resetsInDays,exhausted},
tab:{used, bySrc, skipped, saved:{measured, estimated}}, top:[{origin,bytes}], kinds:{image,script,...}}`.
Messages UI -> Go: `data.setLevel {level}`, `data.setBudget {...}`, `data.loadFully {}`, `data.loadSkipped {nodeId}`,
`data.siteRule {origin, rule}`, `data.reset {}`. Go -> UI: `state.patch` on `data`, `toast` at the warn thresholds, and
`data.exhausted` when the hard stop trips.

Persistence: daily usage totals (per origin top-N) in the profile via `internal/app/store`, reset per `period`; incognito usage
is counted for the budget but never stored with origins.

UX requirements: a calm meter chip in the toolbar (used / limit, colour shifts at warn thresholds, never alarming at
50%), a popover with a ring, "This page: 640 KB (saved about 1.9 MB, estimated)" with the measured/estimated split visible,
top five sites, a "Load images on this page" button and a "Load fully, once" button; Settings -> Data section; onboarding
asks "Do you have a data plan?" with 100 MB / 500 MB / 1 GB / Unlimited; at the hard stop an interstitial offers
"Load once", "Raise limit", "Wait until reset"; the new-tab page can show a one-line "You have 61 MB left this month".
Copy must be plain, never guilt-trippy.

## Benchmark (owner: bench team): `bench/datasaver`

`go run ./bench/datasaver -sites bench/datasaver/sites.txt -runs 3` loads each URL fresh (cold cache) in: Sweef off, Sweef
standard, Sweef strict, and headless Chromium (CDP `Network.loadingFinished.encodedDataLength` + request headers, which is
transfer size), then repeats warm for the cache effect. Reports per site and total: wire bytes, requests, page ready time,
"pages per 100 MB", with the exact method written next to the table (`docs/data-saver-bench.md`). The site list is real public
pages (news, docs, reference, shopping category, forum, blog) chosen before running, not tuned afterwards. Run results are
committed as dated files; the README quotes only those.
