package engine

// fetchres.go: sub-resource fetching through the engine's HTTP cache, with the
// Data Saver gates for scripts and fonts and the cache rules of the saver
// levels. The cache rules in short (see rescache.go for freshness):
//
//   - no-store responses are never stored; Vary: * responses are never stored;
//   - a stored response is only reused when the request headers named by its
//     Vary match the ones it was stored under (Cookie and User-Agent included);
//   - stale entries with a validator are revalidated with a conditional
//     request, a 304 reuses the stored body and costs only the headers;
//   - cache entries are partitioned by browsing context (incognito never reads
//     what another context stored).

import (
	"net/http"
	"strings"
	"time"

	fnet "github.com/sweefbrowser/sweef/internal/net"
)

type fetchOpts struct {
	kind     DataKind
	forced   bool   // the person asked for this (tap to load): no policy gates, no caps
	maxBody  int64  // transfer-size cap, see httpOpts
	prefetch bool   // the parallel read of a navigation's scripts and styles ahead of the loader
	page     string // URL of the page the resource belongs to (default: the tab's; differs while a document is being installed)
}

// fetchResource GETs a sub-resource (script, module) as text through the
// engine's HTTP cache: fresh hits cost nothing, stale ones are revalidated.
func (t *Tab) fetchResource(rawurl string) (string, int, error) {
	return t.fetchRes(rawurl, fetchOpts{kind: KindOther})
}

func (t *Tab) fetchResourceKind(rawurl string, kind DataKind) (string, int, error) {
	return t.fetchRes(rawurl, fetchOpts{kind: kind})
}

// varyValue snapshots the request headers named by names for url.
func (t *Tab) varyValue(names []string, abs string, extra map[string]string) string {
	if len(names) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, n := range names {
		v := ""
		switch n {
		case "cookie":
			v = t.Context().JarRef().ForURL(abs)
		case "user-agent":
			v = t.UserAgent()
		case "accept":
			v = "*/*"
		default:
			for k, x := range t.ExtraHeaders() {
				if strings.EqualFold(k, n) {
					v = x
				}
			}
			for k, x := range extra {
				if strings.EqualFold(k, n) {
					v = x
				}
			}
		}
		sb.WriteString(n)
		sb.WriteByte('=')
		sb.WriteString(v)
		sb.WriteByte('\n')
	}
	return sb.String()
}

// entryFresh decides whether a stored response can be served without asking.
func (t *Tab) entryFresh(e *resEntry, pol DataPolicy, now time.Time) bool {
	if e.expires.IsZero() || !now.Before(e.expires) {
		return false
	}
	if e.windowOnly && pol.ExtendCache {
		// the short window exists so the loader finds what the parallel prefetch
		// of the same navigation fetched; under the saver levels it does not
		// outlive that navigation (a no-cache response is revalidated every time)
		return e.tab == t.ID && e.epoch == t.ds.epoch.Load()
	}
	return true
}

// creditHit books a cache hit as bytes saved, but only when the entry comes from an
// earlier navigation: reuse inside one navigation (the stylesheet the parallel
// prefetch read and the layout reads again) is bookkeeping, not a saving.
func (t *Tab) creditHit(e *resEntry) {
	if e.tab != t.ID || e.epoch != t.ds.epoch.Load() {
		t.noteCacheHit(e.xfer)
	}
}

// knownSize is the transfer size of a cached response, or -1.
func (t *Tab) knownSize(key string) int64 {
	if e := t.eng.res.get(key); e != nil && e.xfer > 0 {
		return e.xfer
	}
	return -1
}

func (t *Tab) fetchRes(rawurl string, o fetchOpts) (string, int, error) {
	abs, err := resolveURL(t.URL(), rawurl)
	if err != nil {
		return "", 0, err
	}
	kind := o.kind
	if kind == "" {
		kind = KindOther
	}
	pol := t.policy()
	now := time.Now()
	key := t.eng.res.key(t.Context(), abs)
	entry := t.eng.res.get(key)

	budget := int64(-1) // remaining first-party script budget, -1 = none
	if !o.forced {
		switch kind {
		case KindFont:
			if pol.BlockFonts {
				t.noteSkipped(KindFont, abs, t.knownSize(key), t.knownSize(key), "font", 0)
				return "", 0, errSaverBlocked
			}
		case KindScript:
			page := o.page
			if page == "" {
				page = t.URL()
			}
			if pol.BlockThirdPartyScripts && thirdParty(page, abs) {
				k := t.knownSize(key)
				t.noteSkipped(KindScript, abs, k, k, "third-party", 0)
				return "", 0, errSaverBlocked
			}
			if pol.ScriptMaxBytes > 0 {
				budget = int64(pol.ScriptMaxBytes) - t.ds.scriptSpent.Load()
				k := t.knownSize(key)
				if budget <= 0 || (k > budget) {
					t.noteSkipped(KindScript, abs, k, k, "script-budget", 0)
					return "", 0, errSaverBlocked
				}
				o.maxBody = budget
			}
		}
	}
	spend := func(n int64) {
		if kind == KindScript && budget >= 0 && n > 0 {
			t.ds.scriptSpent.Add(n)
		}
	}

	if entry == nil && pol.ExtendCache && !o.prefetch {
		// what the navigation's prefetch read but may not be stored (no-store, Vary: *)
		if body, ok := t.takeStash(key); ok {
			return body, http.StatusOK, nil
		}
	}
	var hdr map[string]string
	if entry != nil && len(entry.vary) > 0 && entry.varyVal != t.varyValue(entry.vary, abs, nil) {
		entry = nil // stored for a request that differed in a header the server varies on
	}
	if entry != nil {
		if t.entryFresh(entry, pol, now) && !t.IsOfflineMode() {
			t.netRing.Push(Event{Tab: t.ID, Kind: EvResponse, At: now, Data: fnet.RequestRecord{Method: "GET", URL: abs, FinalURL: abs, Status: entry.status, Headers: map[string]string{"x-sweef-cache": "hit"}}})
			t.creditHit(entry)
			spend(entry.xfer)
			return entry.body, entry.status, nil
		}
		hdr = map[string]string{}
		if entry.etag != "" {
			hdr["If-None-Match"] = entry.etag
		}
		if entry.lastMod != "" {
			hdr["If-Modified-Since"] = entry.lastMod
		}
	}
	r, err := t.httpDo(httpOpts{method: "GET", url: abs, headers: hdr, kind: kind, maxBody: o.maxBody})
	if err != nil {
		return "", 0, err
	}
	if r.aborted {
		if kind == KindScript {
			t.noteSkipped(KindScript, abs, -2, r.declared, "script-budget", 0)
			return "", 0, errSaverBlocked
		}
		return "", 0, errTooLarge{declared: r.declared}
	}
	h := r.header
	if r.status == http.StatusNotModified && entry != nil {
		hh := http.Header{}
		hh.Set("Cache-Control", h.Get("Cache-Control"))
		hh.Set("Expires", h.Get("Expires"))
		for _, k := range []string{"ETag", "Last-Modified", "Date"} {
			if v := h.Get(k); v != "" {
				hh.Set(k, v)
			} else if k == "ETag" && entry.etag != "" {
				hh.Set(k, entry.etag)
			} else if k == "Last-Modified" && entry.lastMod != "" {
				hh.Set(k, entry.lastMod)
			}
		}
		var exp time.Time
		if pol.ExtendCache {
			exp = extendedFreshness(hh, now, kind, abs)
		} else {
			exp = freshness(hh, now)
		}
		window := false
		if exp.IsZero() && pol.ExtendCache {
			// revalidated just now: the rest of this navigation may reuse it
			exp, window = now.Add(5*time.Second), true
		}
		t.creditHit(entry) // before refresh moves the entry into this navigation
		t.eng.res.refresh(key, exp, now, window, t.ID, t.ds.epoch.Load())
		spend(entry.xfer)
		return entry.body, entry.status, nil
	}
	if r.status == http.StatusOK {
		spend(r.xfer)
		t.Context().data().observe(kind, r.xfer)
		if !cacheable(h) && pol.ExtendCache && o.prefetch {
			t.stash(key, string(r.body))
		}
		if cacheable(h) {
			var exp time.Time
			if pol.ExtendCache {
				exp = extendedFreshness(h, now, kind, abs)
			} else {
				exp = freshness(h, now)
			}
			window := false
			if exp.IsZero() {
				// Memory-cache window: a resource fetched for this navigation
				// (e.g. by the parallel prefetch) is reused by the loader.
				exp = now.Add(5 * time.Second)
				window = true
			}
			names := varyNames(h)
			t.eng.res.put(key, &resEntry{
				body: string(r.body), status: r.status, etag: h.Get("Etag"), lastMod: h.Get("Last-Modified"),
				expires: exp, storedAt: now, xfer: r.xfer, vary: names, varyVal: t.varyValue(names, abs, nil),
				windowOnly: window, tab: t.ID, epoch: t.ds.epoch.Load(),
			})
		}
	} else if r.status >= 200 && r.status < 300 {
		spend(r.xfer)
	}
	return string(r.body), r.status, nil
}

// stash keeps a response that must not be stored for the one use the loader of the
// same navigation makes of it (the parallel prefetch would otherwise be thrown
// away and the resource downloaded twice). It never outlives the navigation.
func (t *Tab) stash(key, body string) {
	t.ds.mu.Lock()
	if t.ds.stashed == nil {
		t.ds.stashed = map[string]string{}
	}
	t.ds.stashed[key] = body
	t.ds.mu.Unlock()
}

func (t *Tab) takeStash(key string) (string, bool) {
	t.ds.mu.Lock()
	defer t.ds.mu.Unlock()
	b, ok := t.ds.stashed[key]
	delete(t.ds.stashed, key)
	return b, ok
}
