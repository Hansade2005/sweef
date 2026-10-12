package app

import (
	"github.com/sweefbrowser/sweef/internal/app/store"
)

// scheduleSession asks for a debounced, crash-safe session write. Incognito
// tabs are never part of it, and incognito apps write nothing.
func (a *App) scheduleSession() {
	if a.cfg.Incognito || a.prof.Incognito || a.closing.Load() {
		return
	}
	a.prof.Session.Schedule(a.sessionSnapshot)
}

func (a *App) sessionSnapshot() store.Session {
	var w store.SessionWindow
	act := a.model.Active(a.winID)
	for _, id := range a.model.Order(a.winID) {
		t, ok := a.model.Tab(id)
		if !ok || t.Incognito || t.URL == "" || t.URL == "about:blank" {
			continue
		}
		if len(t.URL) > 12 && t.URL[:12] == "sweef://erro" {
			continue
		}
		if id == act {
			w.Active = len(w.Tabs)
		}
		w.Tabs = append(w.Tabs, store.SessionTab{URL: t.URL, Title: t.Title, Scroll: t.Scroll, Pinned: t.Pinned,
			History: trimHistory(t.History, t.HIdx), HIdx: min(t.HIdx, 49)})
	}
	return store.Session{Windows: []store.SessionWindow{w}}
}

func trimHistory(h []string, idx int) []string {
	if len(h) > 50 {
		start := idx - 25
		if start < 0 {
			start = 0
		}
		if start+50 < len(h) {
			h = h[start : start+50]
		} else {
			h = h[len(h)-50:]
		}
	}
	return append([]string(nil), h...)
}

// saveSession writes now; clean marks a normal exit.
func (a *App) saveSession(clean bool) {
	if a.cfg.Incognito || a.prof.Incognito {
		return
	}
	ss := a.sessionSnapshot()
	ss.CleanExit = clean
	_ = a.prof.Session.Save(ss)
}

// restoreOrOpen opens the initial tabs. It reports whether it opened any.
func (a *App) restoreOrOpen() bool {
	defer func() {
		if a.model.TabCount() == 0 {
			a.openTab(openOpts{URL: newTabURL, Activate: true})
		}
	}()
	if len(a.cfg.InitialURLs) > 0 {
		for i, u := range a.cfg.InitialURLs {
			a.openTab(openOpts{URL: u, Activate: i == 0})
		}
		return true
	}
	if a.cfg.NoRestore || a.cfg.Incognito || a.prof.Incognito {
		return false
	}
	mode := a.prof.Settings.Str("restoreSession")
	if mode == "never" {
		return false
	}
	ss, ok := a.prof.Session.Load()
	if !ok || len(ss.Windows) == 0 || len(ss.Windows[0].Tabs) == 0 {
		return false
	}
	if mode == "ask" && ss.CleanExit {
		return false // a normal exit starts fresh; a crash restores
	}
	w := ss.Windows[0]
	if w.Active < 0 || w.Active >= len(w.Tabs) {
		w.Active = 0
	}
	// mark as unclean until we exit normally
	_ = a.prof.Session.Save(store.Session{Windows: ss.Windows})
	for i, t := range w.Tabs {
		a.openTab(openOpts{URL: t.URL, Title: t.Title, Scroll: t.Scroll, Pinned: t.Pinned, History: t.History, HIdx: t.HIdx,
			Activate: i == w.Active, Lazy: i != w.Active})
	}
	return true
}
