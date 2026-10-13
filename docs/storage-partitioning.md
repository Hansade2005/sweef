# State partitioning by top-level site

What a page, a frame or a worker can remember, and who else can see it. Code: `internal/engine/partition.go` (keys and cookie
policy), `cookie_rt.go` (cookies per request), `frames.go`, `frames_host.go`, `frame_prelude.js` (nested browsing contexts),
`workers.go`, `webcoord.go`, `platform_opfs.go`. Tests: `partition_test.go`, `partition_sw_cache_test.go`, `frames_test.go`,
`workers_*_test.go`.

## The rule

A **site** is the scheme plus the registrable domain (public suffix list + 1): `https://news.example`, `http://a.localhost`.
Every store below is keyed by a **storage key**:

* in a first-party context the key is the plain origin (`https://embed.example`), exactly as before partitioning;
* in a third-party context (the document, or the worker it started, is embedded under a different site) the key is the pair
  `origin^top=<top-level site>` (`https://embed.example^top=https://news.example`);
* if an ancestor frame is on another site than the document itself while the document is on the top-level site (A embeds B
  embeds A) the key gets `^xa`: the inner A shares neither A's first-party state nor B's.

A frame on the same site as the top-level page and all its ancestors (`x.a.localhost` inside `a.localhost`) is first-party.
A document with an opaque origin (sandbox without `allow-same-origin`, `data:`) has no web storage at all.

## Policy table

| API | Partition key | Threat it addresses |
|---|---|---|
| Cookies (requests, `document.cookie`) | unpartitioned jar in a first-party context; partition = top-level site in a third-party one (`Cookie.Partition`), with the `Partitioned` attribute always partition = top-level site | an embed recognising a person across the sites that embed it; a first-party session cookie riding on embedded requests (CSRF/clickjacking) |
| `localStorage` | storage key | same: a cross-site identifier in storage |
| `sessionStorage` | one per document (VM), unavailable to opaque origins | same |
| IndexedDB | storage key (database files live under the key's directory) | same; also storage-quota probing |
| Cache Storage | storage key | same, and cache probing |
| Origin Private File System | storage key, per BrowserContext (a private window shares nothing with the normal one) | same |
| Service worker registrations and scope, the worker's script fetch | storage key (a registration made by an embed is visible to documents embedded the same way) | a worker is persistent storage that also runs code |
| BroadcastChannel, SharedWorker, Web Locks | storage key (+ BrowserContext) | a covert channel between two embeds of one third party on different sites |
| Dedicated Worker | inherits the key of the document that started it | a worker is part of its creator's context |
| HTTP cache | top-level site + URL (`httpCache.keyTop`) | cache probing: a site learning that a resource was loaded by another site; a shared copy as a cross-site identifier |
| Web font / image / preload fetches | go through the HTTP cache, so top-level site + URL | same |
| Process-wide compiled-code caches (QuickJS bytecode, wasm compilation) | content hash only | not partitioned; keyed by bytes the page already has, observable only as a timing difference |

## Cookies

Per request hop (the redirect hops included) `cacheRT.RoundTrip` asks `Tab.cookieScopeFor`:

1. A top-level navigation, and a page that has no site of its own (`about:blank`, `data:`), is first-party: the unpartitioned jar.
2. A request whose site is the top-level site, with no cross-site ancestor frame, is first-party.
3. Anything else is third-party: the request neither sends nor stores unpartitioned cookies. It uses the partition of the
   top-level site, and only cookies with `SameSite=None` are sent or accepted (Lax, Strict and unset cookies are refused from a
   cross-site context). `Secure` is satisfied on https and on loopback hosts.
4. `credentials: omit` requests (`httpOpts.anonymous`) carry and keep nothing.

Policy (`BrowserContext.SetPartitionPolicy`):

| Mode | Third-party context |
|---|---|
| `CookiesPartitioned` (default) | partitioned jar |
| `CookiesBlocked` | only cookies with `Partitioned` are kept (in their partition) |
| `CookiesAllowed` | the old behaviour, no partitioning and no SameSite filter (the app maps "block third-party cookies" off to it) |

`PartitionPolicy.Exception(top, embedded)` returns true to give a pair of sites the unpartitioned jar; `GrantThirdPartyCookies`
does the same for one pair and is what a granted `document.requestStorageAccess()` records. A grant is per (top-level site,
embedded site). `SameSite=None` is still required with a grant, as in browsers.

`document.hasStorageAccess()` is true in a first-party context or with a grant. `document.requestStorageAccess()` is refused
for opaque origins and for sandboxed frames without `allow-storage-access-by-user-activation`; otherwise it asks
`PartitionPolicy.StorageAccess(top, embedded, interacted)`; with no hook it grants when the person has interacted (click, key
press) with the embedded site as a top-level page in this context. There is no user-activation check in the frame itself: frames
receive no input in this engine. There is no First-Party-Set style automatic grant.

`document.cookie` no longer lists `HttpOnly` cookies and cannot create them.

## Clearing

`ClearSiteData(origin)` clears IndexedDB, Cache Storage, service worker registrations, the Origin Private File System,
`localStorage` and partitioned cookies of the storage keys that mention the origin: its own, the partitions it holds as an
embed under other sites, and the partitions keyed under it as a top-level site. `ClearSiteData("")` clears everything.
`ClearCookies("")` now really clears every cookie, partitions included.

## Nested browsing contexts

Before this work an `<iframe>` was an empty box: nothing was fetched and `contentWindow` was `null`, so no third party could
run in a page. Frames are now loaded (see `frames.go`): separate document, origin, VM and goroutine, `postMessage` with
`targetOrigin` checks, `window.parent/top/frames/frameElement`, same-origin `contentWindow`/`contentDocument` access through
proxies, `sandbox`, `srcdoc`, `about:blank`, `data:`, `X-Frame-Options` and CSP `frame-ancestors`.

Not done: frames are not painted into the parent's box and get no pointer or keyboard input (they are readable by
`Tab.FrameTabs()`); `window.open`, popups and `window.opener`; `document.domain`; `allow` is stored but permissions policy is not
enforced; `Sec-Fetch-Dest` for frames is sent but `Referrer-Policy` of the embedder is not consulted; navigations started by a frame to
`javascript:` URLs; cross-window calls are synchronous only into the other VM (callbacks passed across run later and return
nothing) and DOM nodes cannot be passed between windows; a frame of a data-saver "click to load" placeholder is not loaded until
uncovered. `Options.DisableFrames` restores the old behaviour.

## Cost

* Resources that several top-level sites share from a third party (a CDN library) are downloaded once per site instead of once:
  `TestHTTPCacheIsPartitionedByTopLevelSite` shows the second site's request reaching the server. The Data Saver benchmark
  (`docs/data-saver-bench.md`) browses 18 live sites cold and over the network; it could not be re-run in the offline
  environment this change was made in, so **no hit-rate figure for it is claimed**. Within one site the cache hits as before.
* Existing on-disk HTTP cache entries are not found under the new keys and age out.
* A frame costs a VM (memory limit as for a page) and its network; frames beyond 24 per document, 64 per page tree or depth 6 are not created.
