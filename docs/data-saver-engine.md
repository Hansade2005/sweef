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
  manager and update check). `BrowserContext.CountedTransport()` returns the engine's
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
| `Accept-Encoding` | `br, gzip` when the level is not off, `gzip` otherwise (what `net/http` sends by itself, so off is unchanged on the wire). The engine decodes both itself, in the transport wrapper, so compressed byte counts are exact. Brotli is decoded by `github.com/dsnet/compress/brotli` (BSD-3, pure Go, decoder only, +0.1 ms init, +0.35 MB binary). Range requests are left alone. | Savings depend on servers having brotli; CDNs mostly do. |
| `LazyImages` | Images are fetched during layout. Under this policy an image is fetched only when paint says it is within one viewport above or two below the visible window (`render.ImageHinter`); the rest are deferred and re-checked after every layout pass, on `SetScrollY`, on the wheel and when a background tab comes to the front. Page layout is unchanged when images have `width`/`height` (or CSS sizes); the tests compare every element box with the policy off. | Images without a declared size have no box until they load, so they stack at one spot: at most 3 per layout round are admitted, then layout runs again (up to 12 rounds), which can cost extra layout time and moves content below a late image. Layout is done for the whole document, not lazily. |
| `ImageMaxBytes` | The request is made with a size cap. A `Content-Length` over the cap closes the response before any body byte is read (`AbortedBytes`, measured); a chunked response is read up to the cap and abandoned (those bytes are used; the rest is not measured). The `<img>` gets `data-sweef-lite="skipped"` and a labelled box of its declared size (300x150 if none; one dimension declared gives a 2:1 box). Tapping loads it at full size, ignoring the cap. | We cannot transcode or downscale: an image is loaded whole or not at all. The cap needs the server to send `Content-Length` to save anything on the first byte. CSS `background-image`s over the cap are simply omitted (no node to mark). |
| `NoImages` (strict, agent) | No image requests at all (`<img>`, CSS backgrounds, SVG `<image>`, video posters). `<img>` elements become placeholders with their alt text; `Tab.LoadSkipped` or `LoadSkippedImages` loads them. Sizes already in the cache are credited as measured; others are estimated. | Placeholder boxes use the declared size, so a page that sizes images with CSS only (`width:100%`) gets a 2:1 box and reflows. |
| `BlockFonts` | **A no-op on the wire today**: this engine loads no web fonts in any mode (`registerFonts` is a stub; the text is always laid out with its fallback). The switch is enforced at the single place a font read would go through (`fetchRes` with `KindFont`) so a future font loader obeys it. Tests check that no font is requested in any mode and that layout is identical. | Do not count it as a saving. |
| `BlockMediaLoad` | `<video>`/`<audio>`/`new Audio()` neither preload nor autoplay: no request is made until `play()` is called by the page in response to the person (the existing user-activation rules apply; muted autoplay is also held back). Without it, `preload=metadata` (the default) already costs a 1 MB range read per element, which is where the large saving is. Video posters follow the image rules. | Players that fetch through `fetch`/XHR/MSE (YouTube-style) decide for themselves; the engine cannot tell their requests from any others. |
| `ClickToLoad3PFrames` | Third-party `<iframe src=http(s)…>` get `data-sweef-lite="frame"` and a labelled box showing the host. **The engine renders no iframe documents in any mode** (a frame is an empty box), so this saves nothing; it tells the person what is embedded. Tapping uncovers the (empty) frame. `Tab.Skipped()` lists them with their URL so the UI can offer to open one. | Not counted in the stats. |
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

## Cache rules (`rescache.go`, `fetchres.go`, `tab.go`)

Applies to sub-resources the engine fetches itself (scripts, styles, images, workers) and, under
`ExtendCache`, to documents. Page `fetch`/XHR and media are never served from this cache.

* `no-store`: never stored (and the prefetch/loader handover keeps it for one use only).
  `Vary: *`: never stored. Entries are partitioned by browsing context, so a private window never
  reads what another stored. A stored response is reused only if the request headers named by its
  `Vary` (Cookie and User-Agent included) match the ones it was stored under.
* Stale entries with `ETag`/`Last-Modified` are revalidated with `If-None-Match`/`If-Modified-Since`;
  `304` reuses the stored body.
* Extended freshness, only for images, fonts, CSS and JS that have a validator, and never over
  `no-store`/`no-cache`: an `immutable` or versioned URL (hash segment, `?v=`) is fresh for at least a
  day (or the server's longer max-age, at most 30 days); with no stated lifetime the RFC 9111 heuristic
  applies (10% of the time since `Last-Modified`, at most a day); otherwise the server's lifetime
  stands and expiry means a conditional request, not a download. The contract table says to ignore
  `max-age < 1 day` for all such assets; this implementation applies the one-day floor to versioned
  URLs only, because a stable URL with `max-age=300` is how a site ships a CSS fix.
* Documents: with `ExtendCache`, a stored document with a validator is requested with
  `If-None-Match`/`If-Modified-Since` and shown from the cache on `304`. It is **never** shown without
  asking the server, whatever its `max-age`. `Reload` and `LoadFully` skip the stored copy; a document
  without a validator or with `no-store` is not stored.
* `off` keeps the engine's previous rules exactly (including its 5 s memory window and its habit of
  reading a no-store script twice per navigation); under the saver levels the window only serves the
  navigation that filled it.

## Measured on the test page

`TestDataSaverBytesDecreaseByMode` loads one synthetic heavy page (a stylesheet with an `@font-face`,
a 192 KB hero image, eight 58 KB photos below the fold, a muted autoplay video, a third-party iframe,
four hint links, a 20 KB first-party script and a 60 KB ad script on another host), once per level in
its own context, cold cache, localhost. Socket bytes:

| Level | Bytes used | Requests | Of which |
|---|---|---|---|
| off | 1,791,369 | 14 | media 1,048,888; images 656,858; scripts 82,468 |
| standard | 277,992 | 5 | images 192,621 (hero only); scripts 82,506 |
| strict | 23,555 | 3 | scripts 20,773 (first party only) |

That is -84% and -99% on a page built to be heavy. It is a test fixture, not a benchmark: the only
numbers the README may quote are `bench/datasaver`'s.
`TestDataSaverContentLengthAbortAndTapToLoad` shows the early abort on its own: two images of 1.4 MB
and 1.0 MB with a cap of 200 KB cost 279,921 bytes on the wire together with a 58 KB image and one
chunked image read up to the cap; `AbortedBytes` is exactly 2,492,901, the sum of the two
`Content-Length`s.

## What the Data Saver cannot do here

* Transcode, downscale or recompress images or video: that needs a proxy, which this design rules out.
* Make a JavaScript-rendered site cheap in `standard`: its scripts and the data they fetch are loaded in
  full. Strict mode trades the page for the bytes.
* Save anything on fonts, iframes or resource hints, because the engine does not fetch them (above).
* Count other HTTP clients' traffic (see `CountedTransport`), or the kernel-buffered bytes of an aborted
  response.
* Know the size of a skipped request unless the cache or a `Content-Length` said so: those are counts
  plus an estimate.

## Cost

Policy off adds no work to the hot paths beyond reading a few atomics. `engine.New` takes the same ~30 µs
and 20 tabs with a document idle at the same heap (about 4–5.5 MB) as before. The brotli package adds 0.07 ms
of `init` (27 KB of allocations) to process start-up and 0.35 MB to the binary; a `sweef version` start
measures the same as before within noise (about 7 ms on the test machine, both).
