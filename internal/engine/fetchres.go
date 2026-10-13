package engine

// fetchres.go: sub-resource fetching with the Data Saver gates for scripts and
// fonts. The HTTP cache itself (freshness, validators, Vary, 304, redirects,
// disk persistence) is a transport layer, see httpcache_rt.go: everything
// fetched through httpDo gets it. What stays here is the per-navigation memo
// (a resource is read from the network once per navigation).

import (
	"net/http"
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

// knownSize is the transfer size of a cached response, or -1.
func (t *Tab) knownSize(key string) int64 { return t.eng.res.peekXfer(key) }

func (t *Tab) knownSizeURL(u string) int64 {
	key, _ := t.eng.res.key(t.Context(), u, "")
	return t.knownSize(key)
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
	key, _ := t.eng.res.key(t.Context(), abs, "")

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

	// Inside one navigation a resource is read from the network once: the loader finds what the parallel
	// prefetch fetched, and every relayout finds the stylesheet. This is the browser's memory cache for the
	// document; it ignores freshness and costs no event.
	if !t.IsOfflineMode() {
		if e := t.eng.res.navEntry(key, t.ID, t.ds.epoch.Load()); e != nil && !e.Resp.IsZero() {
			spend(e.Xfer)
			return string(e.Body), e.Status, nil
		}
	}
	if pol.ExtendCache && !o.prefetch {
		// what the navigation's prefetch read but may not be stored (no-store, Vary: *)
		if body, ok := t.takeStash(key); ok {
			return body, http.StatusOK, nil
		}
	}
	r, err := t.httpDo(httpOpts{method: "GET", url: abs, kind: kind, maxBody: o.maxBody})
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
	if r.fromCache {
		spend(r.cachedXfer) // the page behaves the same warm or cold
		return string(r.body), r.status, nil
	}
	if r.status == http.StatusOK {
		spend(r.xfer)
		t.Context().data().observe(kind, r.xfer)
		if noStoreResponse(r.header) && pol.ExtendCache && o.prefetch {
			t.stash(key, string(r.body))
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
