package app

// siteusage.go: the application's side of site storage (docs/storage.md, "Quota,
// eviction and persistence"). The engine's storage manager enforces the quota and
// evicts; this file decides which origins are never evicted (persist()), shows
// the person what each site stores and lets them clear one site.

import (
	"sort"
	"sync"

	"github.com/sweefbrowser/sweef/internal/app/store"
)

// storagePrompt is the application's way of asking the person whether a site may keep its data
// ("navigator.storage.persist()"). It is set by whoever embeds the App and may block until the person has
// answered; with none set, only the automatic grants below apply.
type storagePrompt struct {
	mu sync.Mutex
	f  func(origin string) bool
}

// SetStoragePersistPrompt installs the prompt shown when a page asks for persistent storage and no automatic
// rule has decided. It runs on the page's script thread, so it should answer promptly; it must not call back
// into the page. Passing nil removes it (the default: such requests are denied).
func (a *App) SetStoragePersistPrompt(f func(origin string) bool) {
	a.sprompt.mu.Lock()
	a.sprompt.f = f
	a.sprompt.mu.Unlock()
}

// storagePersistPolicy answers navigator.storage.persist() for an origin: granted when the person bookmarked a
// page of the site, otherwise when the person agrees in the prompt, otherwise refused. A persistent origin is
// never evicted under storage pressure; it stays until the person clears it.
func (a *App) storagePersistPolicy(origin string) bool {
	for _, b := range a.prof.Bookmarks.All() {
		if store.OriginOf(b.URL) == origin {
			return true
		}
	}
	a.sprompt.mu.Lock()
	f := a.sprompt.f
	a.sprompt.mu.Unlock()
	return f != nil && f(origin)
}

// storageState is state.data.storage: what each site keeps on this device, largest first, and the browser's
// budget. A private window shows nothing (it has no stored sites of its own and must not reveal the others).
func (a *App) storageState(private bool) map[string]any {
	out := map[string]any{"sites": []any{}}
	if private || a.eng == nil {
		return out
	}
	m := a.eng.DefaultContext().SiteStorage()
	sites := m.Sites()
	sort.SliceStable(sites, func(i, j int) bool { return sites[i].Usage.Total > sites[j].Usage.Total })
	list := make([]any, 0, len(sites))
	for i, s := range sites {
		if i == 50 {
			break
		}
		list = append(list, map[string]any{"origin": s.Origin, "bytes": s.Usage.Total, "persisted": s.Persisted})
	}
	b := m.Budget()
	out["sites"] = list
	out["used"] = b.Used
	out["budget"] = b.Global
	return out
}

// clearSiteStorage removes everything one site stored: databases, caches, files and service workers. Open
// pages of the site are told their connections were closed.
func (a *App) clearSiteStorage(origin string) {
	o := store.OriginOf(origin)
	if o == "" || a.eng == nil {
		return
	}
	a.eng.DefaultContext().ClearSiteData(o)
	a.sendChrome(mToast, map[string]any{"text": "Site data cleared", "kind": "info"})
	a.markState()
}
