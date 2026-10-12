// Package state is the browser's in-memory model: windows, tabs, per-window
// active tab, ordering, the closed-tab stack and per-tab navigation history.
// It is pure data plus a mutex, with no I/O and no engine dependency, and it
// renders to the JSON snapshot / RFC 7386 merge patches that the chrome page
// consumes as the `state` and `state.patch` messages.
//
// Snapshot schema (what the chrome page receives):
//
//	{
//	  "window":   {"id":1,"w":1280,"h":800,"scale":1,"incognito":false,"focused":true},
//	  "tabs":     [{"id","url","title","favicon","loading","progress","canBack","canForward",
//	                "security":{"level","https","certValid","issuer"},"audio","pinned","muted",
//	                "incognito","discarded","blocked"}, ...],   // visual order, pinned first
//	  "active":   "tab-id",
//	  "url","title","loading","canBack","canForward","security","bookmarked","incognito",
//	                                       // mirrors of the active tab for the toolbar
//	  "theme":    "light|dark|system",
//	  "sidebar":  {"open":false,"width":360},
//	  "settings": {...},
//	  "find":     {"open":false,"query":"","matches":0,"index":0},
//	  "closedTabs": 0
//	}
package state

import (
	"encoding/json"
	"sort"
	"sync"
	"time"
)

// Security describes the connection security shown by the lock icon.
type Security struct {
	Level     string `json:"level"` // "secure" | "insecure" | "internal" | "error" | "none"
	HTTPS     bool   `json:"https"`
	CertValid bool   `json:"certValid"`
	Issuer    string `json:"issuer,omitempty"`
}

// Tab is one browser tab as the chrome page sees it.
type Tab struct {
	ID         string   `json:"id"`
	URL        string   `json:"url"`
	Title      string   `json:"title"`
	Favicon    string   `json:"favicon,omitempty"`
	Loading    bool     `json:"loading"`
	Progress   float64  `json:"progress"`
	CanBack    bool     `json:"canBack"`
	CanForward bool     `json:"canForward"`
	Security   Security `json:"security"`
	Audio      bool     `json:"audio"`
	Pinned     bool     `json:"pinned"`
	Muted      bool     `json:"muted"`
	Incognito  bool     `json:"incognito"`
	Discarded  bool     `json:"discarded"`
	Blocked    int      `json:"blocked"` // trackers blocked on this tab

	// not sent to the chrome page
	Window  int       `json:"-"`
	History []string  `json:"-"` // navigation entries (urls)
	HIdx    int       `json:"-"`
	Scroll  int       `json:"-"` // device-independent scroll offset, for session restore
	Opened  time.Time `json:"-"`
}

// Window is a browser window.
type Window struct {
	ID        int
	Order     []string // tab ids, visual order
	Active    string
	Incognito bool
	W, H      int
	Scale     float64
	Focused   bool
}

// ClosedTab is an entry of the reopen stack.
type ClosedTab struct {
	URL       string    `json:"url"`
	Title     string    `json:"title"`
	History   []string  `json:"history,omitempty"`
	HIdx      int       `json:"hidx"`
	Scroll    int       `json:"scroll"`
	Pinned    bool      `json:"pinned"`
	Index     int       `json:"index"`
	Window    int       `json:"window"`
	Incognito bool      `json:"-"`
	At        time.Time `json:"at"`
}

// Sidebar is the agent sidebar geometry.
type Sidebar struct {
	Open  bool `json:"open"`
	Width int  `json:"width"`
}

// Find is the find-in-page bar state.
type Find struct {
	Open    bool   `json:"open"`
	Query   string `json:"query"`
	Matches int    `json:"matches"`
	Index   int    `json:"index"`
}

const maxClosed = 25

// Model is the whole browser state. All methods are safe for concurrent use.
type Model struct {
	mu       sync.RWMutex
	windows  map[int]*Window
	tabs     map[string]*Tab
	closed   []ClosedTab
	nextWin  int
	Theme    string
	Sidebar  Sidebar
	Find     Find
	Settings map[string]any
	// Bookmarked answers "is this URL bookmarked" for the toolbar star.
	Bookmarked func(url string) bool
	rev        uint64
}

// New returns an empty model.
func New() *Model {
	return &Model{
		windows:  map[int]*Window{},
		tabs:     map[string]*Tab{},
		Theme:    "system",
		Sidebar:  Sidebar{Width: 360},
		Settings: map[string]any{},
	}
}

// Rev increments on every mutation; consumers use it to skip diffing.
func (m *Model) Rev() uint64 { m.mu.RLock(); defer m.mu.RUnlock(); return m.rev }

func (m *Model) bump() { m.rev++ }

// AddWindow creates a window and returns its id.
func (m *Model) AddWindow(w, h int, scale float64, incognito bool) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextWin++
	m.windows[m.nextWin] = &Window{ID: m.nextWin, W: w, H: h, Scale: scale, Incognito: incognito, Focused: true}
	m.bump()
	return m.nextWin
}

// SetWindowSize records the logical size and device scale.
func (m *Model) SetWindowSize(win, w, h int, scale float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if x := m.windows[win]; x != nil {
		x.W, x.H, x.Scale = w, h, scale
		m.bump()
	}
}

// WindowInfo returns a copy of a window.
func (m *Model) WindowInfo(win int) (Window, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	x := m.windows[win]
	if x == nil {
		return Window{}, false
	}
	c := *x
	c.Order = append([]string(nil), x.Order...)
	return c, true
}

// SetFocused records window focus.
func (m *Model) SetFocused(win int, f bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if w := m.windows[win]; w != nil && w.Focused != f {
		w.Focused = f
		m.bump()
	}
}

// AddTab inserts a tab. index < 0 appends (after pinned tabs for unpinned).
// The tab becomes active when activate is true.
func (m *Model) AddTab(win int, t Tab, index int, activate bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	w := m.windows[win]
	if w == nil {
		return
	}
	t.Window = win
	if t.Opened.IsZero() {
		t.Opened = time.Now()
	}
	if t.Incognito == false {
		t.Incognito = w.Incognito
	}
	if len(t.History) == 0 && t.URL != "" {
		t.History = []string{t.URL}
		t.HIdx = 0
	}
	cp := t
	m.tabs[t.ID] = &cp
	if index < 0 || index > len(w.Order) {
		index = len(w.Order)
	}
	w.Order = append(w.Order, "")
	copy(w.Order[index+1:], w.Order[index:])
	w.Order[index] = t.ID
	m.normalizeOrder(w)
	if activate || w.Active == "" {
		w.Active = t.ID
	}
	m.bump()
}

// normalizeOrder keeps pinned tabs first, preserving relative order.
func (m *Model) normalizeOrder(w *Window) {
	sort.SliceStable(w.Order, func(i, j int) bool {
		return m.tabs[w.Order[i]].Pinned && !m.tabs[w.Order[j]].Pinned
	})
}

// Tab returns a copy of the tab.
func (m *Model) Tab(id string) (Tab, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t := m.tabs[id]
	if t == nil {
		return Tab{}, false
	}
	return *t, true
}

// Update applies fn to the tab under the lock.
func (m *Model) Update(id string, fn func(t *Tab)) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.tabs[id]
	if t == nil {
		return false
	}
	fn(t)
	if w := m.windows[t.Window]; w != nil {
		m.normalizeOrder(w)
	}
	m.bump()
	return true
}

// Activate makes id the active tab of its window.
func (m *Model) Activate(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.tabs[id]
	if t == nil {
		return false
	}
	t.Discarded = false
	m.windows[t.Window].Active = id
	m.bump()
	return true
}

// Active returns the active tab id of a window ("" if none).
func (m *Model) Active(win int) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if w := m.windows[win]; w != nil {
		return w.Active
	}
	return ""
}

// Order returns the tab ids of a window in visual order.
func (m *Model) Order(win int) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if w := m.windows[win]; w != nil {
		return append([]string(nil), w.Order...)
	}
	return nil
}

// CloseTab removes a tab, pushes it on the reopen stack (not for incognito
// tabs, which must leave no trace) and returns the tab that became active
// ("" if the window is now empty).
func (m *Model) CloseTab(id string) (next string, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.tabs[id]
	if t == nil {
		return "", false
	}
	w := m.windows[t.Window]
	idx := 0
	for i, o := range w.Order {
		if o == id {
			idx = i
			w.Order = append(w.Order[:i], w.Order[i+1:]...)
			break
		}
	}
	if !t.Incognito && t.URL != "" && t.URL != "about:blank" {
		m.closed = append(m.closed, ClosedTab{URL: t.URL, Title: t.Title, History: append([]string(nil), t.History...),
			HIdx: t.HIdx, Scroll: t.Scroll, Pinned: t.Pinned, Index: idx, Window: t.Window, At: time.Now()})
		if len(m.closed) > maxClosed {
			m.closed = m.closed[len(m.closed)-maxClosed:]
		}
	}
	delete(m.tabs, id)
	if w.Active == id {
		w.Active = ""
		if len(w.Order) > 0 {
			n := idx
			if n >= len(w.Order) {
				n = len(w.Order) - 1
			}
			w.Active = w.Order[n]
		}
	}
	m.bump()
	return w.Active, true
}

// PopClosed returns the most recently closed tab.
func (m *Model) PopClosed() (ClosedTab, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.closed) == 0 {
		return ClosedTab{}, false
	}
	c := m.closed[len(m.closed)-1]
	m.closed = m.closed[:len(m.closed)-1]
	m.bump()
	return c, true
}

// ClosedCount is the size of the reopen stack.
func (m *Model) ClosedCount() int { m.mu.RLock(); defer m.mu.RUnlock(); return len(m.closed) }

// Move reorders a tab to index `to` (clamped; pinned tabs stay first).
func (m *Model) Move(id string, to int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.tabs[id]
	if t == nil {
		return false
	}
	w := m.windows[t.Window]
	for i, o := range w.Order {
		if o == id {
			w.Order = append(w.Order[:i], w.Order[i+1:]...)
			break
		}
	}
	if to < 0 {
		to = 0
	}
	if to > len(w.Order) {
		to = len(w.Order)
	}
	w.Order = append(w.Order, "")
	copy(w.Order[to+1:], w.Order[to:])
	w.Order[to] = id
	m.normalizeOrder(w)
	m.bump()
	return true
}

// Navigated records a committed navigation in the tab's history. replace
// overwrites the current entry (redirects, reload); traverse moves the
// index (back/forward) without adding.
func (m *Model) Navigated(id, url string, replace bool) {
	m.Update(id, func(t *Tab) {
		t.URL = url
		switch {
		case len(t.History) == 0:
			t.History = []string{url}
			t.HIdx = 0
		case replace:
			t.History[t.HIdx] = url
		case t.History[t.HIdx] == url:
		default:
			t.History = append(t.History[:t.HIdx+1], url)
			t.HIdx = len(t.History) - 1
		}
		t.CanBack = t.HIdx > 0
		t.CanForward = t.HIdx < len(t.History)-1
	})
}

// Traverse moves delta entries through history and returns the target url.
func (m *Model) Traverse(id string, delta int) (string, bool) {
	var out string
	ok := m.Update(id, func(t *Tab) {
		n := t.HIdx + delta
		if n < 0 || n >= len(t.History) {
			return
		}
		t.HIdx = n
		t.URL = t.History[n]
		t.CanBack = n > 0
		t.CanForward = n < len(t.History)-1
		out = t.URL
	})
	return out, ok && out != ""
}

// TabCount counts tabs (optionally all windows).
func (m *Model) TabCount() int { m.mu.RLock(); defer m.mu.RUnlock(); return len(m.tabs) }

// Snapshot renders the full state for one window as a JSON-ready map.
func (m *Model) Snapshot(win int) map[string]any {
	m.mu.RLock()
	defer m.mu.RUnlock()
	w := m.windows[win]
	if w == nil {
		return map[string]any{}
	}
	tabs := make([]any, 0, len(w.Order))
	var act *Tab
	for _, id := range w.Order {
		t := m.tabs[id]
		tabs = append(tabs, tabMap(t))
		if id == w.Active {
			act = t
		}
	}
	out := map[string]any{
		"window":     map[string]any{"id": w.ID, "w": w.W, "h": w.H, "scale": w.Scale, "incognito": w.Incognito, "focused": w.Focused},
		"tabs":       tabs,
		"active":     w.Active,
		"theme":      m.Theme,
		"sidebar":    map[string]any{"open": m.Sidebar.Open, "width": m.Sidebar.Width},
		"find":       map[string]any{"open": m.Find.Open, "query": m.Find.Query, "matches": m.Find.Matches, "index": m.Find.Index},
		"settings":   cloneJSON(m.Settings),
		"closedTabs": len(m.closed),
		"incognito":  w.Incognito,
	}
	if act != nil {
		out["url"] = act.URL
		out["title"] = act.Title
		out["loading"] = act.Loading
		out["canBack"] = act.CanBack
		out["canForward"] = act.CanForward
		out["security"] = secMap(act.Security)
		bm := false
		if m.Bookmarked != nil {
			bm = m.Bookmarked(act.URL)
		}
		out["bookmarked"] = bm
	} else {
		out["url"], out["title"] = "", ""
		out["loading"], out["canBack"], out["canForward"], out["bookmarked"] = false, false, false, false
		out["security"] = secMap(Security{Level: "none"})
	}
	return out
}

func secMap(s Security) map[string]any {
	m := map[string]any{"level": s.Level, "https": s.HTTPS, "certValid": s.CertValid}
	if s.Issuer != "" {
		m["issuer"] = s.Issuer
	}
	return m
}

func tabMap(t *Tab) map[string]any {
	m := map[string]any{
		"id": t.ID, "url": t.URL, "title": t.Title, "loading": t.Loading, "progress": t.Progress,
		"canBack": t.CanBack, "canForward": t.CanForward, "security": secMap(t.Security),
		"audio": t.Audio, "pinned": t.Pinned, "muted": t.Muted, "incognito": t.Incognito,
		"discarded": t.Discarded, "blocked": t.Blocked,
	}
	if t.Favicon != "" {
		m["favicon"] = t.Favicon
	}
	return m
}

// cloneJSON deep-copies through plain maps so later diffs never alias.
func cloneJSON(in map[string]any) map[string]any {
	b, _ := json.Marshal(in)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	if out == nil {
		out = map[string]any{}
	}
	return out
}

// Normalize round-trips v through JSON so numbers/bools/maps have canonical
// types; Diff expects normalised inputs.
func Normalize(v map[string]any) map[string]any { return cloneJSON(v) }
