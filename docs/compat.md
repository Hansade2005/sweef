# Site compatibility notes

What is known not to work, why, and what was checked. Measured in the benchmark environment of
`docs/data-saver-bench.md` (a cloud sandbox: all traffic leaves through one egress gateway that terminates TLS), so the
network-level findings are about that path; nothing here is a claim about every network.

Sweef sends the headers an ordinary browser sends (`Accept` with only the image formats it decodes,
`Accept-Language`, `Sec-Fetch-Dest/Mode/Site/User`, `Upgrade-Insecure-Requests`, `Referer` under the default
`strict-origin-when-cross-origin` policy) and a truthful `User-Agent` (the embedder's; the desktop app's is
`Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Safari/537.36 Sweef/0.1`). It does not send
Client Hints, does not copy Chromium's TLS or HTTP/2 fingerprint, and does not try to pass bot-detection challenges.
A site that decides to refuse such a client is listed below as a limitation instead of being worked around.

## Fixed

| Site | Symptom | Cause | Fix |
|---|---|---|---|
| github.com | `403` on the first request | GitHub rejects navigations that carry no `Sec-Fetch-*` headers. Same request with curl and the Sweef user agent: no extra headers 403, `Accept-Language` alone 403, `Upgrade-Insecure-Requests` alone 403, `Sec-Fetch-Dest/Mode/Site/User` alone 200. | The engine now sends the ordinary navigation headers (`browserheaders.go`). |
| bbc.com/news | page never finished loading (60 s timeout in `off` and `standard`) | A lock-order inversion in the worker support: a starting `Worker` holds its VM lock while its script runs and then pushes the result to the page's event queue; the page loop asked "do workers have work?" while holding the queue lock and waiting for the same VM lock. The goroutine dump of the hung process shows `hasLiveWork` and `workerCreate` waiting on each other. `strict` finished because it runs no third-party scripts. | `hasLiveWork` no longer takes a worker's VM lock (the inbox has its own lock). Regression tests in `worker_deadlock_test.go`. |

## Known limitations

### stackoverflow.com, allrecipes.com (Cloudflare managed challenge)

The first response is `403` with `Cf-Mitigated: challenge` and `Server: cloudflare`; the body is Cloudflare's
interactive JavaScript challenge (`/cdn-cgi/challenge-platform/.../orchestrate/chl_page`). It is decided by Cloudflare
from the client's network, TLS and behaviour, not from a header we omit: the response is identical with the headers
above, with Chromium's user agent, and with curl's.

Evidence from the same benchmark run (`bench/datasaver/results/2026-10-13-full.json`, status of the main document):

* stackoverflow.com: `403` in Sweef (off, standard, strict) and in Chromium and Chromium with `Save-Data`.
* allrecipes.com: `403` in Sweef and in plain Chromium, `200` in Chromium with `Save-Data` on the same run: the
  decision is not stable even for one client.

Both are therefore listed as excluded in the benchmark for every browser. Passing the challenge would mean emulating a
Chromium client, which Sweef does not do.

### ebay.com (Akamai Bot Manager)

`403` with `Server: AkamaiGHost`, `X-Cdn: Akamai`, a `bm_s` bot-manager cookie and a 1.8 KB error page, with or without
the headers above. Chromium also gets `403` in the same environment (benchmark results, main document status for
chromium and chromium-savedata). Not worked around.

### Dynamic imports in bundled modules

On some Vite/Rolldown sites (lonelyplanet.com seen) a `import("./chunk.js")` inside a module is requested relative to
the page URL instead of the module, which answers `404` (a handful of small requests per page). Not fixed here: it is module-resolution behaviour in `jsvm.go`, outside the network layer.

### WebAssembly: supported, with limits

The `WebAssembly` global exists (`Module`, `Instance`, `Memory`, `Table`, `Global`, `validate`, `compile`, `instantiate`,
the streaming variants and the three error classes) in pages and Web Workers, running modules as native code through
wazero. Sites that only need the core API work; sites that need `WebAssembly.Tag` or
`externref` at the JS boundary do not, and a module that crosses into JS tens of thousands of times per frame is slow
because each crossing costs microseconds. Threads and `SharedArrayBuffer` work on cross-origin isolated pages (COOP + COEP), see [web-platform.md](web-platform.md). Full list, design and measured costs: [webassembly.md](webassembly.md).
No real site was run for this entry; the evidence is the hand-assembled modules in `internal/engine/wasm_test.go`.
