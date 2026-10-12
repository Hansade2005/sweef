package app

import (
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/sweefbrowser/sweef/internal/app/omnibox"
	"github.com/sweefbrowser/sweef/internal/app/state"
	"github.com/sweefbrowser/sweef/internal/engine"
)

const newTabURL = "sweef://newtab"

type openOpts struct {
	URL       string
	Incognito bool
	Activate  bool
	After     string // insert right after this tab
	Index     int    // explicit index when HasIndex
	HasIndex  bool
	History   []string
	HIdx      int
	Scroll    int
	Pinned    bool
	Title     string
	Lazy      bool // restore as a discarded tab: no load until activated
}

func (a *App) newTab(url string, incognito, activate bool) string {
	return a.openTab(openOpts{URL: url, Incognito: incognito, Activate: activate})
}

func (a *App) incogContext() *engine.BrowserContext {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.incogCtx == nil {
		a.incogCtx = a.eng.NewContext("incognito", engine.ContextOptions{})
	}
	return a.incogCtx
}

// openTab creates the engine tab, the model entry and starts loading.
func (a *App) openTab(o openOpts) string {
	incog := o.Incognito || a.cfg.Incognito
	id := a.newTabID()
	var e *etab
	if !o.Lazy {
		if e = a.makeEtab(id, incog); e == nil {
			return ""
		}
	}
	url := o.URL
	title := o.Title
	if title == "" {
		title = "New Tab"
	}
	idx := -1
	if o.HasIndex {
		idx = o.Index
	} else if o.After != "" {
		for i, x := range a.model.Order(a.winID) {
			if x == o.After {
				idx = i + 1
			}
		}
	}
	st := state.Tab{ID: id, URL: url, Title: title, Pinned: o.Pinned, Incognito: incog,
		History: o.History, HIdx: o.HIdx, Scroll: o.Scroll, Discarded: o.Lazy}
	if len(o.History) > 0 {
		st.URL = o.History[min(o.HIdx, len(o.History)-1)]
		st.CanBack = o.HIdx > 0
		st.CanForward = o.HIdx < len(o.History)-1
		url = st.URL
	}
	st.Security = securityFor(st.URL, false)
	a.model.AddTab(a.winID, st, idx, o.Activate)
	if url == "" {
		url = newTabURL
	}
	if o.Lazy {
		if o.Activate {
			a.activateTab(id)
		}
		a.markState()
		return id
	}
	if o.Activate {
		a.activateTab(id)
	}
	mode := modePush
	if len(o.History) > 0 {
		mode = modeReplace
	}
	e.scrollCSS.Store(int32(o.Scroll))
	a.loadURL(id, url, mode, false)
	if o.Scroll > 0 {
		e.restoreScroll.Store(int32(o.Scroll))
	}
	a.markState()
	return id
}

func (a *App) newTabID() string {
	a.tabSeq++
	return "t" + itoa(a.tabSeq)
}

func itoa(n int) string { return strconv.Itoa(n) }

// makeEtab creates the engine tab + worker for state tab id.
func (a *App) makeEtab(id string, incog bool) *etab {
	var (
		t   *engine.Tab
		err error
	)
	if incog {
		t, err = a.eng.NewTabInContext(a.incogContext(), "about:blank")
	} else {
		t, err = a.eng.NewTab("about:blank")
	}
	if err != nil || t == nil {
		a.logf("new tab: %v", err)
		return nil
	}
	e := a.newEtab("page", id, t, incog)
	e.vpW.Store(int32(a.vpLogW()))
	e.vpH.Store(int32(a.vpLogH()))
	t.SetViewport(a.pageCSS(a.vpLogW()), a.pageCSS(a.vpLogH()), a.pageDSF(), false, false)
	e.lastActive.Store(time.Now().UnixNano())
	a.mu.Lock()
	a.pages[id] = e
	a.mu.Unlock()
	return e
}

func (a *App) vpLogW() int {
	if a.vpLog.empty() {
		return a.wLog
	}
	return int(a.vpLog.W + 0.5)
}

func (a *App) vpLogH() int {
	if a.vpLog.empty() {
		return a.hLog
	}
	return int(a.vpLog.H + 0.5)
}

func (a *App) closeTab(id string) {
	st, ok := a.model.Tab(id)
	if !ok {
		return
	}
	next, _ := a.model.CloseTab(id)
	a.mu.Lock()
	e := a.pages[id]
	delete(a.pages, id)
	a.mu.Unlock()
	if e != nil {
		e.close()
	}
	if st.Incognito {
		a.maybeWipeIncognito()
	}
	if next == "" {
		if a.model.TabCount() == 0 {
			a.Close()
			return
		}
	}
	if a.model.Active(a.winID) != "" && a.activeShown() != a.model.Active(a.winID) {
		a.activateTab(a.model.Active(a.winID))
	}
	a.markState()
}

// maybeWipeIncognito drops the incognito profile and context once its last
// tab is gone, so nothing outlives the incognito session.
func (a *App) maybeWipeIncognito() {
	for _, id := range a.model.Order(a.winID) {
		if t, ok := a.model.Tab(id); ok && t.Incognito {
			return
		}
	}
	if a.cfg.Incognito {
		return
	}
	if a.incog != nil {
		_ = a.incog.Close()
		a.incog = nil
	}
	a.mu.Lock()
	c := a.incogCtx
	a.incogCtx = nil
	a.mu.Unlock()
	if c != nil {
		go func() { _ = c.Close() }()
	}
}

// activateTab makes id the visible tab.
func (a *App) activateTab(id string) {
	e := a.page(id)
	if e == nil {
		st, ok := a.model.Tab(id)
		if !ok || !st.Discarded {
			return
		}
		if e = a.makeEtab(id, st.Incognito); e == nil {
			return
		}
		a.model.Update(id, func(t *state.Tab) { t.Discarded = false })
		target := st.URL
		if target == "" {
			target = newTabURL
		}
		defer a.loadURL(id, target, modeReplace, false)
	}
	prev := a.activeShown()
	a.model.Activate(id)
	a.setActiveShown(id)
	e.lastActive.Store(time.Now().UnixNano())
	if prev != id {
		a.comp.SetPage(nil)
		a.resetScroll()
		a.paintedDocH = 0
		a.model.Find = state.Find{}
	}
	e.vpW.Store(int32(a.vpLogW()))
	e.vpH.Store(int32(a.vpLogH()))
	e.w.do(func() {
		e.t.SetViewport(a.pageCSS(int(e.vpW.Load())), a.pageCSS(int(e.vpH.Load())), a.pageDSF(), false, false)
		e.paint(true)
		e.armPump()
	})
	if st, ok := a.model.Tab(id); ok && (st.URL == newTabURL || st.URL == "") {
		a.setFocus("chrome")
		a.sendChrome("omnibox.focus", map[string]any{"select": true})
		a.focusOmnibox()
	} else {
		a.setFocus("page")
	}
	a.markState()
}

func (a *App) activeShown() string {
	v, _ := a.shownTab.Load().(string)
	return v
}
func (a *App) setActiveShown(id string) { a.shownTab.Store(id) }

func (a *App) togglePin(id string) {
	a.model.Update(id, func(t *state.Tab) { t.Pinned = !t.Pinned })
	a.markState()
}

func (a *App) toggleMute(id string) {
	a.model.Update(id, func(t *state.Tab) { t.Muted = !t.Muted })
	a.markState()
}

func (a *App) reopenClosed() {
	c, ok := a.model.PopClosed()
	if !ok {
		return
	}
	a.openTab(openOpts{URL: c.URL, Activate: true, History: c.History, HIdx: c.HIdx, Scroll: c.Scroll,
		Pinned: c.Pinned, Index: c.Index, HasIndex: true, Title: c.Title})
}

// ---- memory saver: discard idle background tabs ------------------------------------------

// discardIdle drops the engine tabs of background tabs unused for the
// configured time. The model keeps them (greyed out); activating reloads.
func (a *App) discardIdle() {
	mins := a.prof.Settings.Int("tabDiscardMinutes")
	if mins <= 0 {
		return
	}
	cutoff := time.Now().Add(-time.Duration(mins) * time.Minute).UnixNano()
	active := a.model.Active(a.winID)
	for _, id := range a.model.Order(a.winID) {
		st, _ := a.model.Tab(id)
		if id == active || st.Discarded || st.Loading || st.Audio || st.Pinned {
			continue
		}
		e := a.page(id)
		if e == nil || e.lastActive.Load() > cutoff {
			continue
		}
		a.discard(id)
	}
}

func (a *App) discard(id string) {
	a.mu.Lock()
	e := a.pages[id]
	delete(a.pages, id)
	a.mu.Unlock()
	if e == nil {
		return
	}
	e.close()
	a.model.Update(id, func(t *state.Tab) { t.Discarded, t.Loading = true, false })
	a.markState()
}

// ---- helpers shared by menus / shortcuts -------------------------------------------------------

func (a *App) toggleBookmark(id string) {
	st, ok := a.model.Tab(id)
	if !ok || st.URL == "" || strings.HasPrefix(st.URL, "sweef://error") {
		return
	}
	on, err := a.prof.Bookmarks.Toggle(st.Title, st.URL)
	if err != nil {
		return
	}
	msg := "Bookmark removed"
	if on {
		msg = "Bookmark added"
	}
	a.sendChrome(mToast, map[string]any{"text": msg, "kind": "info"})
	a.markState()
}

func (a *App) setSidebar(open bool) {
	if a.model.Sidebar.Open == open {
		return
	}
	a.model.Sidebar.Open = open
	a.sendChrome("sidebar", map[string]any{"open": open, "width": a.model.Sidebar.Width})
	a.markState()
	a.layoutFallback()
}

// omniInput answers omni.input with ranked suggestions.
func (a *App) omniInput(p struct {
	ID   any    `json:"id"`
	Text string `json:"text"`
}) {
	t0 := time.Now()
	resp := a.suggest(p.Text)
	if p.ID != nil {
		resp.ID = p.ID
	} else {
		a.omniSeq++
		resp.ID = a.omniSeq
	}
	a.stats.noteSuggest(time.Since(t0))
	a.sendChrome(mOmniSuggest, resp)
}

func (a *App) suggest(text string) omnibox.Response {
	src := omnibox.Sources{
		Engine: a.engineSearch,
		History: func(q string, n int) []omnibox.HistoryHit {
			var out []omnibox.HistoryHit
			for _, h := range a.prof.History.Search(q, n) {
				out = append(out, omnibox.HistoryHit{URL: h.URL, Title: h.Title, Frecency: h.Frec, Typed: h.Typed})
			}
			return out
		},
		Bookmarks: func(q string, n int) []omnibox.BookmarkHit {
			var out []omnibox.BookmarkHit
			for _, b := range a.prof.Bookmarks.Search(q, n) {
				out = append(out, omnibox.BookmarkHit{URL: b.URL, Title: b.Title})
			}
			return out
		},
		Tabs: func() []omnibox.TabHit {
			var out []omnibox.TabHit
			for _, id := range a.model.Order(a.winID) {
				if t, ok := a.model.Tab(id); ok && t.URL != "" && !strings.HasPrefix(t.URL, "sweef://") {
					out = append(out, omnibox.TabHit{ID: t.ID, URL: t.URL, Title: t.Title})
				}
			}
			return out
		},
	}
	return omnibox.Suggest(src, text, 8)
}

// openMenu answers menu.open with the data the menu shows.
func (a *App) openMenu(p struct {
	Name string  `json:"name"`
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
}) {
	out := map[string]any{"name": p.Name, "x": p.X, "y": p.Y}
	switch p.Name {
	case "downloads":
		out["items"] = a.prof.Downloads.List()
	case "bookmarks":
		out["items"] = a.prof.Bookmarks.Children("bar")
	case "history":
		out["items"] = a.prof.History.Recent(30)
	case "main":
		out["usage"] = a.prof.Stats.Snapshot(a.model.TabCount())
	}
	a.sendChrome("menu.open", out)
}

var _ atomic.Int32
