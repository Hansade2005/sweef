package app

import (
	"net/url"
	"runtime"
	"strings"
	"time"

	"github.com/sweefbrowser/sweef/internal/app/state"
	"github.com/sweefbrowser/sweef/internal/app/store"
)

// buildState is the snapshot every UI page receives: the model's core state
// (tabs, active tab, settings, sidebar, window) plus what the pages read from
// the stores. All extras are optional on the UI side.
func (a *App) buildState() map[string]any {
	a.syncSettingsToModel()
	snap := a.model.Snapshot(a.winID)
	snap["platform"] = runtime.GOOS
	snap["version"] = a.version()
	snap["systemDark"] = false // the shell reports no OS theme; "system" therefore means light
	act, hasAct := a.activeTab()
	snap["bookmarksBar"] = a.bookmarksBar()
	snap["topSites"] = a.topSites(8)
	snap["downloads"] = a.downloadList(50)
	if hasAct && (act.URL == newTabURL || strings.HasPrefix(act.URL, newTabURL+"?")) && !act.Incognito {
		// real numbers only: process RSS from the OS, tabs from the model
		u := a.prof.Stats.Snapshot(a.model.TabCount())
		snap["stats"] = map[string]any{"memMB": int(u.RSSBytes >> 20), "tabs": u.Tabs, "blocked": u.Blocked, "rssExact": u.RSSExact}
	}
	if hasAct {
		if c := a.compatFor(act); c != nil {
			snap["compat"] = c
		}
		snap["page"] = map[string]any{"type": a.pageTypes.get(act.ID)}
	}
	snap["agent"] = a.agentState(act, hasAct)
	snap["data"] = a.dataState()
	snap["media"] = a.mediaState(act, hasAct)
	snap["perm"] = a.permState(act, hasAct)
	if dt := a.devtoolsSnapshot(); dt != nil {
		snap["devtools"] = dt
	}
	return snap
}

func (a *App) version() string {
	if a.cfg.Version != "" {
		return a.cfg.Version
	}
	return "dev"
}

func (a *App) bookmarksBar() []map[string]any {
	out := []map[string]any{}
	if !a.prof.Settings.Bool("bookmarks.bar") {
		return out
	}
	for _, b := range a.prof.Bookmarks.Children(store.FolderBar) {
		if b.Folder {
			continue
		}
		out = append(out, map[string]any{"id": b.ID, "title": b.Title, "url": b.URL, "favicon": a.iconFor(b.URL)})
	}
	return out
}

func (a *App) topSites(n int) []map[string]any {
	out := []map[string]any{}
	over, _ := a.prof.Settings.Get("newtab.sites").(map[string]any)
	for _, h := range a.prof.History.TopSites(n + len(over)) {
		t, u := h.Title, h.URL
		if t == "" {
			t = displayTitle(h.URL)
		}
		if o, ok := over[h.URL].(map[string]any); ok {
			if hide, _ := o["hide"].(bool); hide {
				continue
			}
			if s, _ := o["title"].(string); s != "" {
				t = s
			}
			if s, _ := o["url"].(string); s != "" {
				u = s
			}
		}
		if len(out) >= n {
			break
		}
		out = append(out, map[string]any{"title": t, "url": u, "key": h.URL, "favicon": a.iconFor(u)})
	}
	return out
}

// setTopSite hides or edits a tile of the new tab page (key is the address history knows it by).
func (a *App) setTopSite(key string, hide bool, title, rawurl string) {
	if key == "" {
		return
	}
	cur, _ := a.prof.Settings.Get("newtab.sites").(map[string]any)
	next := map[string]any{}
	for k, v := range cur {
		next[k] = v
	}
	next[key] = map[string]any{"hide": hide, "title": strings.TrimSpace(title), "url": strings.TrimSpace(rawurl)}
	if err := a.prof.Settings.Set("newtab.sites", next); err == nil {
		a.markState()
	}
}

// iconFor returns the cached favicon (a data: URL) for a page's host, or "".
func (a *App) iconFor(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return a.favicons.byHost(u.Hostname())
}

func (a *App) downloadList(limit int) []map[string]any {
	out := []map[string]any{}
	for i, it := range a.prof.Downloads.List() {
		if i >= limit {
			break
		}
		out = append(out, dlJSON(it))
	}
	return out
}

// dlJSON maps a download to the shape the UI reads. States the UI knows:
// progress | done | failed | paused.
func dlJSON(it store.DownloadItem) map[string]any {
	st := it.State
	if st == store.DLError || st == store.DLCanceled {
		st = "failed"
	}
	m := map[string]any{"id": it.ID, "name": it.Name, "url": it.URL, "state": st, "received": it.Received,
		"total": it.Total, "speed": it.Speed, "path": it.Path}
	if it.Error != "" {
		m["error"] = it.Error
	}
	return m
}

// ---- compat banner -------------------------------------------------------------------

// compatFor returns the "this page may not work here" banner for the active
// tab, or nil. It appears when a page raised several script errors while
// loading (a hint that it needs something the engine lacks), unless the user
// dismissed it for that origin. It is a heuristic and is labelled as one.
func (a *App) compatFor(t state.Tab) map[string]any {
	if t.URL == "" || !strings.HasPrefix(t.URL, "http") {
		return nil
	}
	origin := store.OriginOf(t.URL)
	if a.compatDismissed[origin] {
		return nil
	}
	n, ok := a.compatErrs[t.ID]
	if !ok || n < compatThreshold {
		return nil
	}
	return map[string]any{"url": t.URL, "message": "This page raised script errors while loading and may not work correctly in Sweef."}
}

const compatThreshold = 3

// ---- page type chips ---------------------------------------------------------------------

type pageTypeCache struct{ m map[string]string }

func (c *pageTypeCache) get(id string) string {
	if v := c.m[id]; v != "" {
		return v
	}
	return "any"
}

func (c *pageTypeCache) set(id, t string) {
	if c.m == nil {
		c.m = map[string]string{}
	}
	c.m[id] = t
}

// ---- delivery -----------------------------------------------------------------------------

// uiTargets are the engine tabs that speak the sweef bridge: the chrome, the
// agent overlay and every page tab currently showing a sweef:// page that said
// ui.ready.
func (a *App) uiTargets() []*etab {
	a.mu.Lock()
	out := []*etab{a.chrome, a.overlay}
	a.mu.Unlock()
	for e := range a.uiPages {
		out = append(out, e)
	}
	return out
}

// broadcast delivers a message to every UI page (any goroutine for chrome/overlay
// only; UI-loop callers get the page tabs too).
func (a *App) broadcast(typ string, payload any) {
	for _, e := range a.uiTargets() {
		a.sendTo(e, typ, payload)
	}
}

// sendTo delivers a message to one UI page.
func (a *App) sendTo(e *etab, typ string, payload any) {
	if e == nil || a.closing.Load() || e.closed.Load() {
		return
	}
	// a page that left its sweef:// document (an error page, a redirect) has no bridge: skip it quietly
	js := "(typeof sweef !== 'undefined' && sweef.__recv) ? sweef.__recv(" + jsString(typ) + "," + jsJSON(payload) + ") : undefined"
	e.w.do(func() {
		// A tab that showed a Sweef page and has since navigated to a web page is still on the list of UI pages.
		// What the browser sends to its UI (tabs, settings, Developer Tools state) must never be evaluated in a
		// web page's JavaScript: the page could have defined window.sweef.
		if e.kind == "page" && !strings.HasPrefix(e.t.URL(), "sweef://") {
			return
		}
		if _, err := e.t.Evaluate(js); err != nil {
			a.logf("%s <- %s: %v", e.kind, typ, err)
		}
		e.afterOp()
	})
}

func (a *App) recordCompatErrors(e *etab) {
	n := 0
	for _, ev := range e.t.ConsoleLog(0) {
		if m, ok := ev.Data.(map[string]string); ok && m["level"] == "error" {
			n++
		}
	}
	id := e.id
	a.post(func() {
		if a.compatErrs[id] != n {
			a.compatErrs[id] = n
			a.markState()
		}
	})
}

var _ = time.Second
