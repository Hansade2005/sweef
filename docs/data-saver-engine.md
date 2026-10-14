# Data Saver: engine side

This is what the engine really does for each switch of the Data Saver contract
(`docs/data-saver.md`), where it lives, and where its limits are. Everything here is
measured by tests in `internal/engine` (`datasaver*_test.go`, run them with
`go test ./internal/engine -run 'DataSaver|ExtendedCache|WireCounters|Brotli'`).

The engine is not Chromium. Several things the contract table lists are already free
here, and the document says so instead of counting them as savings.

## Used vs saved

* **Used** is exact for a `BrowserContext`: every connection the HTTP transport dials is
  wrapped, and each `Read`/`Write` adds to atomic counters (`wire.go`). The wrapped
  connection is the one under TLS, so TLS handshakes and record framing are in the numbers;
  TCP/IP headers are not. The tests compare `WireIn/WireOut` with what the test servers
  saw at their own sockets: they are equal to the byte for plain HTTP, and for HTTPS with
  HTTP/2. Each non-default context has its own transport, so its numbers are its own; the
  engine total is `Engine.WireTotals()`.
* **By kind and by origin** are attributed per HTTP exchange: request header and body bytes
  written, response header bytes (computed from the parsed headers) plus the raw, still
  compressed, body bytes read. They are approximate (HTTP/2 compresses headers) and in the
  tests land within 3% of the exact total. A tab's `WireIn/WireOut` are these attributed
  sums, not socket counts; only a context has socket counts.
* **Saved** is only reported from measurements:
  * `AbortedBytes`: the `Content-Length` of responses aborted by a size cap, minus the body
    bytes that were read (none: the response is closed before any body byte is read).
    Caveat: bytes the kernel had already received into its buffers when the connection was
    closed were still sent by the network and are invisible to the counters. With HTTP/1.1
    the abort also closes the connection (the next request pays a handshake); with HTTP/2 it
    resets the stream and the connection lives on.
  * `CacheHitBytes`: the transfer size (headers + raw body) that a stored response cost when
    it was downloaded, credited when it is reused from an *earlier navigation*, either fresh
    or after a `304`. Reuse inside one navigation (the stylesheet that the parallel prefetch
    read and layout reads again) is not credited, so `off` shows no savings. The `304`
    exchange itself is in used/by-kind (a few hundred bytes).
  * `SkippedKnownBytes` (additive): requests that were blocked and whose size was known from
    the cache.
  * `SkippedEstimate` is an **estimate**, kept apart: for each skipped request of unknown
    size, the median transfer size of that kind seen in this context (needs five samples), else
    a rounded default (`defaultSizeEstimate` in `datasaver.go`: image 40 KB, script 30 KB,
    style 15 KB, font 25 KB, media 1 MB, frame 30 KB, xhr 5 KB). The defaults are guesses
    in the range of public HTTP Archive medians. UIs must label the number as estimated.
* Not counted: traffic made through other HTTP clients (the app's favicon fetcher, downloads
  manager and update check). `BrowserContext.CountedTransport()` returns the engine's (cache, accounting and proxy included)
  transport for code that wants its traffic in the numbers.

## API as implemented

Package `engine`. Everything of the contract is there; additions are marked (+).

```go
PolicyFor(level) DataPolicy              AgentPolicy() DataPolicy (+)
ParseSaverLevel(string) SaverLevel (+)

(*BrowserContext).SetDataPolicy(DataPolicy)   .DataPolicy() (+)   .DataStats()
(*BrowserContext).SetAgentLoading(bool) (+)   .ResetDataStats() (+)   .CountedTransport() (+)
(*Engine).WireTotals() (in, out int64) (+)

(*Tab).SetDataPolicy(*DataPolicy)         // per-tab override, nil = inherit
(*Tab).DataStats()                        // since the last navigation
(*Tab).SetAgentLoading(bool) (+)          // strict minus script limits, for agent-driven loads
(*Tab).SetBackground(bool)                // background tabs: no timers, no page requests
(*Tab).SetScrollY(int) (+)                // scroll position in CSS px: lazy images follow it
(*Tab).LoadSkipped(nodeID int64) error    // tap to load one placeholder
(*Tab).LoadSkippedImages() (int, error) (+)   // "Load images on this page"
(*Tab).LoadFully() error                  // reload once without policy
(*Tab).FullyLoaded() bool (+)   .Skipped() []SkippedItem (+)   .ScrollY() (+)
```

Differences from the contract text:

* Bus events have `Kind: "data"` (`engine.EvData`), not `Type`: that is the field name of
  `engine.Event`. `Event.Data` is a `DataEvent{Context, Tab DataStats, WireIn, WireOut}`.
  Throttled to 4 per second per tab, with a trailing event so the last state is always
  published; context-level traffic (no tab) is throttled the same way with `Tab == ""`. Nothing
  is built when nobody has subscribed.
* `DataPolicy` has two extra switches, `NoImages` and `BlockThirdPartyScripts`, so that the
  agent preset can be "strict minus script limits". `ScriptMaxBytes == 0` means no budget.
  `Level == SaverOff` ignores every other field.
* `DataStats` has `SkippedKnownBytes` and `Requests` in addition.
* The app does not have to call `SetScrollY` for the usual scroll path: `Tab.Evaluate` recognises the
  `__sweefScrollTo(y)` notification the app already sends after each scroll and calls it.
  `MouseWheel` and `ScrollIntoView` call it too.
* Extended-cache freshness is narrower than the contract table (below), on purpose.
* `SetBackground` is a flag the app must set: nothing in the engine knows which tab is
  visible. Until the app calls it, no tab is ever suspended.

## What each level does

`standard` (`PolicyFor(SaverStandard)`), `strict` (`PolicyFor(SaverStrict)`), `agent`
(`AgentPolicy()`), and `off` (nothing; counting still works).

| Switch | What it does in this engine | Limits |
|---|---|---|
| `SaveDataHeader` | `Save-Data: on` on every request of the tab or context (documents, sub-resources, page `fetch`/XHR, media, redirect hops). | Servers decide what to do with it. |
| `Accept-Encoding` | `br, gzip` at **every** level, `off` included (it was `gzip` at `off` before: that is what `net/http` sends by itself; checked on the wire by `TestDataSaverHeadersAndEncodings`). The engine decodes both itself, in the transport wrapper, so compressed byte counts are exact. Brotli is decoded by `github.com/dsnet/compress/brotli` (BSD-3, pure Go, decoder only, +0.1 ms init, +0.35 MB binary). Range requests are left alone. | Savings depend on servers having brotli; CDNs mostly do. |
| `LazyImages` | Images are fetched during layout. Under this policy an image is fetched only when paint says it is within one viewport above or two below the visible window (`render.ImageHinter`); the rest are deferred and re-checked after every layout pass, on `SetScrollY`, on the wheel and when a background tab comes to the front. Page layout is unchanged when images have `width`/`height` (or CSS sizes); the tests compare every element box with the policy off. | Images without a declared size have no box until they load, so they stack at one spot: at most 3 per layout round are admitted, then layout runs again (up to 12 rounds), which can cost extra layout time and moves content below a late image. Layout is done for the whole document, not lazily. |
| `ImageMaxBytes` | The request is made with a size cap. A `Content-Length` over the cap closes the response before any body byte is read (`AbortedBytes`, measured). Requests with a cap use connections with a 64 KiB HTTP/2 receive window (`cappedStreamWindow`): with the default 4 MiB window the server had already been allowed to send the whole body when the stream was reset, so a refused 1.4 MB image still cost 1.44 MB on the wire over HTTP/2 while `AbortedBytes` claimed it as saved (now about 70 KB, `TestSizeCapOverHTTP2CostsAtMostOneSmallWindow`). A refusal is remembered with the response's freshness (a tombstone in the cache): the next visit does not send the request while it is fresh; a chunked response is read up to the cap and abandoned (those bytes are used; the rest is not measured). The `<img>` gets `data-sweef-lite="skipped"` and a labelled box of its declared size (300x150 if none; one dimension declared gives a 2:1 box). Tapping loads it at full size, ignoring the cap. | We cannot transcode or downscale: an image is loaded whole or not at all. The cap needs the server to send `Content-Length` to save anything on the first byte. CSS `background-image`s over the cap are simply omitted (no node to mark). |
| `NoImages` (strict, agent) | No image requests at all (`<img>`, CSS backgrounds, SVG `<image>`, video posters). `<img>` elements become placeholders with their alt text; `Tab.LoadSkipped` or `LoadSkippedImages` loads them. Sizes already in the cache are credited as measured; others are estimated. | Placeholder boxes use the declared size, so a page that sizes images with CSS only (`width:100%`) gets a 2:1 box and reflows. |
| `BlockFonts` + `FontMaxBytes`, `FontTotalBytes`, `FontWOFFOnly` | Web fonts load (`docs/webfonts.md`): a face is fetched only when the page's text needs a character in its `unicode-range`, so unused faces and subsets cost nothing. **standard** (`BlockFonts` false): only WOFF2/WOFF sources are used (`FontWOFFOnly`), a font over `FontMaxBytes` (300 KB, cut off at the headers like an oversized image: `AbortedBytes`) is skipped, and the fonts of a page share `FontTotalBytes` (600 KB; they are fetched one at a time in the order the text asks for them so the budget is exact). **strict** (`BlockFonts` true, also the agent preset): no web font is fetched, except icon fonts (below). A skipped face shows its fallback; `Skipped[font]` counts the faces, `Tab.Skipped()` lists them with the reason (`font`, `font-size`, `font-budget`, `font-format`) and the size from the cache when known. `local()` sources and `data:` URLs cost nothing and are always used. "Load full page" ignores all of it. The page-facing API (`document.fonts`, `FontFace`) obeys the same gates. | The caps are `DataPolicy` fields: change them per context or tab. A skipped font changes the text's metrics, so the layout differs from the page's real one (the same trade as for images). |
| icon fonts (exception to the font gates) | Without them a page shows words such as `arrow_back` (ligature icon fonts) or empty boxes. A face is an icon font when its family name is one of the usual libraries (Material Icons/Symbols, Font Awesome, Glyphicons, Ionicons, Bootstrap Icons, Remix, Boxicons, Themify, Dashicons, Octicons, Codicons, Feather, Lucide, Tabler, Phosphor, IcoMoon and Fontello exports, ...: `isIconFamilyName`), or its `unicode-range` lies in the private use areas, or the first character that asked for it is a private use code point (U+E000-F8FF, planes 15-16). Icon fonts are loaded in strict, may be TrueType/OpenType in standard, and do not count against the 600 KB page budget (the 300 KB per-font cap still applies). | Measured on the sites in `bench/datasaver`: go.dev (Material Icons, 129 KB WOFF2) keeps its icons in strict at +129 KB (+70% of the strict page), joelonsoftware.com keeps a 85 KB icon font. A family named without an icon word and used only through ligatures is not recognised in strict (it costs nothing, and the word shows). |
| `BlockMediaLoad` | `<video>`/`<audio>`/`new Audio()` neither preload nor autoplay: no request is made until `play()` is called by the page in response to the person (the existing user-activation rules apply; muted autoplay is also held back). Without it, `preload=metadata` (the default) already costs a 1 MB range read per element, which is where the large saving is. Video posters follow the image rules. | Players that fetch through `fetch`/XHR/MSE (YouTube-style) decide for themselves; the engine cannot tell their requests from any others. |
| `ClickToLoad3PFrames` | Third-party `<iframe src=http(s)…>` get `data-sweef-lite="frame"` and a labelled box showing the host. The engine loads iframe documents now (`docs/frames-popups.md`): a marked frame loads nothing, and no frame request is made for it, until the person taps the placeholder, which then loads the real frame. A frame's bytes count toward the budget (the context's socket counters) but not toward the per-page "used on this page" figure. `Tab.Skipped()` lists them with their URL so the UI can offer to open one. | Not counted in the stats. |
| `DropHints` | **Also free today**: the engine never fetches `prefetch`, `preload`, `prerender`, `modulepreload` or `dns-prefetch`/`preconnect` links (tests assert zero requests). The only speculative read is the parallel prefetch of the page's scripts and stylesheets before the loader runs; with a script budget (strict) or third-party blocking it only reads what the loader will use, in document order. | |
| `SuspendBackground` | With `SetBackground(true)`: no page timers (`setTimeout`, `setInterval`, rAF, workers) run, and page-initiated requests (`fetch`, XHR, media, fonts) fail immediately with a network error; promise jobs and results already delivered still run, so an explicit `Evaluate` does not hang. Documents, their scripts and styles still load when the app navigates the tab. Not applied to agent loading. Tabs come back with `SetBackground(false)`. | The engine does not know what is visible; the app must call it. A page's own network error handling will see "Failed to fetch" while it is in the background. |
| `ExtendCache` | See below. Also: a linked stylesheet is read once per document (relayouts do not ask the server again), and a navigation's prefetch is handed to its loader even when the response may not be stored. | |
| `BlockThirdPartyScripts` (strict) | A script from another site (registrable domain; IPs compare as themselves) is not requested, and its `<script>` gets an `error` event. | The shield (ad/tracker blocker, `internal/app/privacy`) is separate and keeps working in all levels. |
| `ScriptMaxBytes` (strict) | First-party script transfer bytes per page, in document order. A script whose `Content-Length` exceeds what is left is aborted unread (measured); one that does not say is cut once the budget is spent; later scripts are skipped. Cache hits count toward the budget too, so the page behaves the same warm or cold. | Pages that are rendered by JavaScript break when the framework bundle does not fit. That is what strict means; "Load fully" is one tap. Inline scripts are not counted. |

Agent loading (`SetAgentLoading`) is strict minus `BlockThirdPartyScripts`, `ScriptMaxBytes` and
background suspension. A per-tab policy override (`Tab.SetDataPolicy`) wins over it, and
`LoadFully` over both.

`LoadFully` reloads the page unconditionally with no policy, and the exemption lasts until the
tab navigates somewhere else (including lazy loading while you scroll).

## Cache rules (`httpcache_policy.go`, `httpcache_rt.go`, `httpcache_store.go`)

The HTTP cache is a transport layer (`cacheRT`, between the `http.Client` and the accounting/decoding wrapper), so
every request the engine makes for a page goes through it: documents, scripts, styles, images, workers, page
`fetch`/XHR (GET), redirect hops, media ranges. A hit costs no bytes and is not charged; a revalidation costs the
304's few hundred. The request log (`Network.response` records) shows what it did under the request header
`x-sweef-cache`: `hit`, `revalidated`, `stale-if-error`, `tombstone`, `bypass`, or `miss: stored lifetime=Ns` /
`miss: no-store` / `miss: no-lifetime` / `miss: status-302` ...

This is the same at every Data Saver level except for the extensions marked below. `BrowserContext.Fetch`
(Playwright's APIRequestContext) never uses it.

* **Freshness** (RFC 9111): `max-age`; else `Expires` minus `Date` (an invalid `Expires` is already expired);
  `s-maxage` and `proxy-revalidate` are ignored (this is a private cache, so `private` responses are stored);
  `no-cache` and `max-age=0` store the response but revalidate before every reuse; `immutable` without a lifetime is
  fresh for a day. Age is the larger of `Age` and the apparent age (`response time - Date`), plus the time stored.
  Without a stated lifetime, non-document responses get the heuristic 10% of the time since `Last-Modified`, at most
  7 days; `301`/`308` are reused for a day. **A document is only served without asking when the server stated a
  lifetime**; there is no heuristic for documents.
* **Not stored**: `no-store` (an older copy is dropped too), `Vary: *`, request `Cache-Control: no-store`, `302`/`307`
  and error statuses unless the server stated a lifetime, `5xx`, responses with neither a lifetime nor a validator
  (kept for the rest of the navigation only, in memory, so the loader finds what the parallel prefetch read),
  documents/XHR/media/frames of that kind not at all. `Set-Cookie` is never stored: cookies are applied from the
  network response only.
* **Revalidation**: a stale entry with `ETag`/`Last-Modified` is requested with `If-None-Match`/`If-Modified-Since`.
  A `304` keeps the stored body, merges the new headers (not `Content-*`), restarts freshness. A `5xx` or a network error
  serves the stale entry when `stale-if-error` allows and `must-revalidate` is absent. A request that already carries
  validators (page code) goes to the network untouched.
* **Request directives**: `Cache-Control: no-cache` / `Pragma: no-cache` (what `Reload` and `LoadFully` send) skip the
  stored copy and send no validators; the answer replaces it. `max-age=N` and `min-fresh` on the request are honoured.
* **Vary**: an entry is used only if the request headers named by its `Vary` (Cookie, User-Agent, Accept-Language, Save-Data ...)
  match those it was stored under; `Accept-Encoding` is ignored because the engine decodes everything it asks for.
* **Redirects**: each hop is cached on its own, so a `301`/`308` (or a `302` with a lifetime) is not asked for again.
* **Ranges**: a `206` is stored under URL plus `Range`; the same range is answered from the cache, another is not.
* **Unsafe methods**: a successful `POST`/`PUT`/`DELETE`/`PATCH` drops the stored GET of the same URL.
* **Key**: normalised URL (no fragment, lower-case scheme and host, default port dropped); the query is never reordered.
  The default context shares entries; every other context (private windows, isolated contexts) has its own namespace in
  memory, so it never reads what another stored and leaves nothing on disk.
* **Per navigation**: a resource is read from the network once per navigation whatever its headers (the stylesheet that
  layout reads again, the script the prefetch read): `fetchRes` keeps this memo and it costs no event.
* **Tiers**: memory (`Options.MaxCacheMiB`, default 32, LRU) and the profile-level disk cache under
  `<CacheDir>/http` (`Options.DiskCacheMiB`, default 100, negative = memory only), default context only, never for
  loopback hosts, entries up to a eighth of the cap. Files are written under a temporary name and renamed into place
  (a reader sees a whole entry or none, also from another process on the same profile), a corrupt file is a miss and is
  removed, the least recently used files go when the directory passes the cap, and the writer is one goroutine started on
  the first write, so engine start-up touches no disk. `Engine.DropMemoryCache()` keeps only the disk tier (the state
  after a restart, used by the benchmark), `Engine.ClearHTTPCache()` / `BrowserContext.DeleteCache()` forget entries.
* **Data Saver extension** (`ExtendCache`, standard and strict): for images, fonts, CSS and JS that have a validator, an
  `immutable` or versioned URL (hash segment, `?v=`) is fresh for at least a day (or the server's longer lifetime, at most 30
  days), never over `no-store`/`no-cache`. The contract table says to ignore `max-age < 1 day` for all such assets; this
  implementation applies the one-day floor to versioned URLs only, because a stable URL with `max-age=300` is how a site
  ships a CSS fix. Also: a navigation's prefetch of a `no-store` response is handed to its loader once (stash).
* **Documents** used to be revalidated always under `ExtendCache` and never cached under `off`. They now follow the rules
  above at every level: fresh by the server's word means no request, otherwise a conditional request when there is a
  validator. Reload and LoadFully bypass.

### Why a warm visit used to cost 71% (off) / 47% (standard) of a cold one

Measured with `datasaver probe` (per-request log of two visits): (1) the memory cache held 8 MiB with oldest-first
eviction and refused any entry over 2 MiB, so a page that fetched more than 8 MiB (theverge.com 18 MB) lost its
stylesheets and scripts to its own images before the second visit; (2) images were reused only while their entry
survived, and nothing persisted past the process; (3) refused over-cap images (lonelyplanet.com in standard: three images,
1.6 MB) were requested again on every visit and, over HTTP/2, sent in full each time (above); (4) documents were not
cached at `off`, scripts/styles/images were the only cached kinds, and redirects and `fetch`/XHR never. See
`docs/data-saver-bench.md` for the numbers of the fixed engine.

### Proxy, headers and WebSockets

* `Options.ProxyURL` / `ContextOptions.ProxyURL` (http, https or socks5 URL; `direct` for none); with neither set,
  `HTTPS_PROXY`, `HTTP_PROXY` and `NO_PROXY` apply (`http.ProxyFromEnvironment`, loopback never proxied). An explicit
  URL that does not parse fails every request instead of going direct. The dial to the proxy is the counted dial, so
  `WireIn/WireOut` stay exact.
* WebSockets are dialled by the same counted dialer through the same proxy (CONNECT for `ws` and `wss`), so their
  frames are in the context's counters (`TestWebSocketBytesAreCounted`, `TestWebSocketGoesThroughTheProxy`).
* Requests carry the headers an ordinary browser sends (`browserheaders.go`): `Accept` (only formats the engine decodes),
  `Accept-Language` (`Options.AcceptLanguage`, `ContextOptions.AcceptLanguage`, `Engine.SetAcceptLanguage`; default
  `en-US,en;q=0.9`, as `navigator.language`), `Sec-Fetch-Dest/Mode/Site/User` (to https and loopback only),
  `Upgrade-Insecure-Requests`, and `Referer` under `strict-origin-when-cross-origin`. See `docs/compat.md`.

## Measured on the test page

`TestDataSaverBytesDecreaseByMode` loads one synthetic heavy page (a stylesheet with an `@font-face`,
a 192 KB hero image, eight 58 KB photos below the fold, a muted autoplay video, a third-party iframe,
four hint links, a 20 KB first-party script and a 60 KB ad script on another host), once per level in
its own context, cold cache, localhost. Socket bytes:

| Level | Bytes used | Requests | Of which |
|---|---|---|---|
| off | 1,835,420 | 15 | media 1,049,032; images 658,874; scripts 82,684; font 41,324 |
| standard | 320,102 | 6 | images 192,841 (hero only); scripts 82,714; font 41,339 |
| strict | 24,002 | 3 | scripts 20,918 (first party only); 1 font, 9 images and 1 script skipped |

The page's font file is 40 KB of filler (not a valid font): it is fetched in off and standard, rejected by
the decoder, and the text stays in its fallback; in strict it is skipped. That is -83% and -99% on a page built to be heavy. It is a test fixture, not a benchmark: the only
numbers the README may quote are `bench/datasaver`'s.
`TestDataSaverContentLengthAbortAndTapToLoad` shows the early abort on its own: two images of 1.4 MB
and 1.0 MB with a cap of 200 KB cost 279,921 bytes on the wire together with a 58 KB image and one
chunked image read up to the cap; `AbortedBytes` is exactly 2,492,901, the sum of the two
`Content-Length`s.

## What the Data Saver cannot do here

* Transcode, downscale or recompress images or video: that needs a proxy, which this design rules out.
* Make a JavaScript-rendered site cheap in `standard`: its scripts and the data they fetch are loaded in
  full. Strict mode trades the page for the bytes.
* Save anything on resource hints, because the engine does not fetch them (above). Iframes and fonts used to be in this
  list: both are fetched now, so they cost bytes (the fonts row above; frames are click-to-load when third-party) and
  strict saves most fonts again. Only `ClickToLoad3PFrames` withholds frames, and only third-party ones.
* Count other HTTP clients' traffic (see `CountedTransport`), or the kernel-buffered bytes of an aborted
  response.
* Know the size of a skipped request unless the cache or a `Content-Length` said so: those are counts
  plus an estimate.

## Cost

Policy off adds no work to the hot paths beyond reading a few atomics. `engine.New` takes the same ~30 µs
and 20 tabs with a document idle at the same heap (about 4–5.5 MB) as before. The brotli package adds 0.07 ms
of `init` (27 KB of allocations) to process start-up and 0.35 MB to the binary; a `sweef version` start
measures the same as before within noise (about 7 ms on the test machine, both).
