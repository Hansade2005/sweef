package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/sweefbrowser/sweef/internal/app/omnibox"
	"github.com/sweefbrowser/sweef/internal/app/state"
	"github.com/sweefbrowser/sweef/internal/app/store"
	"github.com/sweefbrowser/sweef/internal/pdf"
)

// ---- omnibox ------------------------------------------------------------------------

type omniReq struct {
	ID    any    `json:"id"`
	Text  string `json:"text"`
	Scope string `json:"scope"` // bar | newtab | palette
	Limit int    `json:"limit"`
}

// omniInput answers omni.input. The UI builds its own "go to / search / ask"
// rows, so only knowledge from the browser goes back: open tabs, bookmarks and
// history (type tab|bookmark|history), plus an inline completion. No favicons
// travel with the rows (the dropdown draws a type icon): the answer is compiled as
// script text on every key stroke and must stay a few hundred bytes.
func (a *App) omniInput(src *etab, p omniReq) {
	a.omniAnswer(src, p, nil)
}

// omniWait is a worker waiting inside the event turn for the answer to an
// omni.input it just forwarded. Whoever moves state from 0 wins: the UI loop
// (1) hands over the script to run, the worker (2) gives up and the answer is
// sent as an ordinary message when it is ready.
type omniWait struct {
	state atomic.Int32
	js    chan string
}

// omniSyncWait bounds how long a worker holds a frame back for the omnibox
// answer. The answer takes tens of microseconds; this only matters when the
// UI loop is busy with something else.
const omniSyncWait = 4 * time.Millisecond

// omniSync forwards omni.input to the UI loop and waits (worker only, called
// from drainBindings) for the answer so the key stroke paints once: the page
// builds its rows from the suggestions instead of painting its old ones first
// and the new ones in a second frame. On a timeout the page is told to show
// what it has ({stale:true}) and the answer arrives later as a normal message.
func (e *etab) omniSync(raw json.RawMessage) {
	a := e.a
	w := &omniWait{js: make(chan string, 1)}
	p := decode[omniReq](raw)
	a.post(func() { a.omniAnswer(e, p, w) })
	t := time.NewTimer(omniSyncWait)
	var js string
	select {
	case js = <-w.js:
		t.Stop()
	case <-t.C:
		if w.state.CompareAndSwap(0, 2) {
			js = "sweef.__recv(\"omni.suggestions\",{\"stale\":true,\"id\":" + jsJSON(p.ID) + "})"
		} else {
			js = <-w.js
		}
	case <-a.quit:
		return
	}
	if _, err := e.t.Evaluate(js); err != nil {
		a.logf("%s <- omni.suggestions: %v", e.kind, err)
	}
}

func (a *App) omniAnswer(src *etab, p omniReq, w *omniWait) {
	t0 := time.Now()
	limit := p.Limit
	if limit <= 0 || limit > 24 {
		limit = 8
	}
	resp := a.suggest(p.Text, limit)
	items := make([]omnibox.Item, 0, len(resp.Items))
	for _, it := range resp.Items {
		switch it.Type {
		case omnibox.TypeTab, omnibox.TypeBookmark, omnibox.TypeHistory:
			items = append(items, it)
		}
	}
	if strings.TrimSpace(p.Text) == "" { // primed with nothing typed: what the user probably wants next
		items = a.defaultSuggestions(p.Scope, limit)
	}
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		m := map[string]any{"type": it.Type, "title": it.Title, "url": it.URL, "text": it.Text}
		if it.Tab != "" {
			m["tab"] = it.Tab
		}
		out = append(out, m)
	}
	msg := map[string]any{"items": out}
	if p.ID != nil {
		msg["id"] = p.ID
	}
	if resp.Inline != "" {
		msg["inline"] = resp.Inline
	}
	a.stats.noteSuggest(time.Since(t0))
	if w != nil && w.state.CompareAndSwap(0, 1) {
		w.js <- "sweef.__recv(" + jsString(mOmniSuggest) + "," + jsJSON(msg) + ")"
		return
	}
	a.sendTo(src, mOmniSuggest, msg)
}

func (a *App) defaultSuggestions(scope string, limit int) []omnibox.Item {
	var out []omnibox.Item
	if scope != "newtab" {
		for _, id := range a.model.Order(a.winID) {
			if t, ok := a.model.Tab(id); ok && strings.HasPrefix(t.URL, "http") && id != a.model.Active(a.winID) {
				out = append(out, omnibox.Item{Type: omnibox.TypeTab, Title: orURL(t.Title, t.URL), URL: t.URL, Text: t.URL, Tab: id})
			}
		}
	}
	for _, b := range a.prof.Bookmarks.All() {
		out = append(out, omnibox.Item{Type: omnibox.TypeBookmark, Title: orURL(b.Title, b.URL), URL: b.URL, Text: b.URL})
	}
	for _, h := range a.prof.History.Top(limit) {
		out = append(out, omnibox.Item{Type: omnibox.TypeHistory, Title: orURL(h.Title, h.URL), URL: h.URL, Text: h.URL})
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func orURL(title, u string) string {
	if title != "" {
		return title
	}
	return u
}

// ---- history / bookmarks pages ----------------------------------------------------------

type dataReq struct {
	Kind   string `json:"kind"` // history | bookmarks
	Q      string `json:"q"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
	ID     int    `json:"id"`
}

type dataItem struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	TS      int64  `json:"ts,omitempty"` // milliseconds, as JavaScript dates want
	Favicon string `json:"favicon,omitempty"`
}

// dataQuery answers data.query for the history and bookmarks pages with a
// window of the (searched) list.
func (a *App) dataQuery(src *etab, p dataReq) {
	if p.Limit <= 0 || p.Limit > 500 {
		p.Limit = 300
	}
	var all []dataItem
	switch p.Kind {
	case "history":
		prof := a.profileFor(src != nil && src.incog)
		var rows []store.HistoryEntry
		if strings.TrimSpace(p.Q) == "" {
			rows = prof.History.Recent(p.Offset + p.Limit)
		} else {
			rows = prof.History.Search(p.Q, p.Offset+p.Limit)
		}
		for _, h := range rows {
			all = append(all, dataItem{ID: h.URL, Title: h.Title, URL: h.URL, TS: h.Last.UnixMilli(), Favicon: a.iconFor(h.URL)})
		}
	case "bookmarks":
		for _, b := range a.prof.Bookmarks.Search(p.Q, 0) {
			all = append(all, dataItem{ID: b.ID, Title: b.Title, URL: b.URL, TS: b.Added.UnixMilli(), Favicon: a.iconFor(b.URL)})
		}
	default:
		return
	}
	total := len(all)
	if p.Kind == "history" {
		total = a.profileFor(src != nil && src.incog).History.Len()
		if strings.TrimSpace(p.Q) != "" {
			total = len(all) // search results are capped at offset+limit; the list grows as the page asks for more
			if len(all) == p.Offset+p.Limit {
				total = len(all) + 1
			}
		}
	}
	lo := min(p.Offset, len(all))
	hi := min(lo+p.Limit, len(all))
	a.sendTo(src, "data.result", map[string]any{"kind": p.Kind, "id": p.ID, "offset": p.Offset, "total": total, "items": all[lo:hi]})
}

func (a *App) dataRemove(kind, id string) {
	switch kind {
	case "history":
		a.prof.History.Delete(id)
		if a.incog != nil {
			a.incog.History.Delete(id)
		}
	case "bookmarks":
		a.prof.Bookmarks.Remove(id)
	}
	a.markState()
}

// clearBrowsingData wipes history, cookies, site data and the icon cache
// ("Clear browsing data now"). Bookmarks, settings and downloads stay.
func (a *App) clearBrowsingData() {
	a.prof.History.Clear()
	if a.incog != nil {
		a.incog.History.Clear()
	}
	ctx := a.eng.DefaultContext()
	ctx.ClearCookies("")
	for _, e := range a.pagesSnapshot() {
		ctx.ClearStorage(urlOrigin(e.topURL.Load()))
	}
	ctx.ClearSiteData("") // IndexedDB, Cache Storage and service worker registrations of every site
	a.favicons.clear()
	a.sendChrome(mToast, map[string]any{"text": "Browsing data cleared", "kind": "info"})
	a.markState()
}

func urlOrigin(v any) string {
	s, _ := v.(string)
	return store.OriginOf(s)
}

func (a *App) pagesSnapshot() []*etab {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]*etab, 0, len(a.pages))
	for _, e := range a.pages {
		out = append(out, e)
	}
	return out
}

// ---- onboarding -----------------------------------------------------------------------------

// finishOnboarding records that onboarding is over and replaces the onboarding
// tab with the new tab page.
func (a *App) finishOnboarding(tabID string) {
	_ = a.prof.Settings.Set("onboarding.done", true)
	if tabID != "" {
		if st, ok := a.model.Tab(tabID); ok && strings.HasPrefix(st.URL, "sweef://onboarding") {
			a.loadURL(tabID, newTabURL, modeReplace, false)
		}
	}
}

// ---- menus ----------------------------------------------------------------------------------

// openMenu handles menu.open names from the chrome's menus.
func (a *App) openMenu(name string) {
	switch name {
	case "zoom.in":
		a.zoom(10)
	case "zoom.out":
		a.zoom(-10)
	case "zoom.reset":
		a.zoom(0)
	case "print":
		a.printToPDF()
	case "cookies", "certificate", "reportSite":
		a.sendChrome(mToast, map[string]any{"text": "That panel is not available yet", "kind": "info"})
	}
}

// printToPDF renders the active page to a PDF in the downloads folder.
func (a *App) printToPDF() {
	e := a.activePage()
	if e == nil {
		return
	}
	st, _ := a.model.Tab(e.id)
	dir := a.downloadsDir()
	e.w.do(func() {
		data, err := e.t.PDF(pdf.Options{})
		if err != nil {
			a.post(func() {
				a.sendChrome(mToast, map[string]any{"text": "Could not create the PDF: " + err.Error(), "kind": "error"})
			})
			return
		}
		name := store.SafeFilename(orURL(st.Title, "page")) + ".pdf"
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return
		}
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			return
		}
		a.post(func() {
			it := a.profileFor(e.incog).Downloads.Record(st.URL, name, p, int64(len(data)), "application/pdf")
			a.sendChrome(mToast, map[string]any{"text": "Saved " + it.Name, "kind": "info"})
		})
	})
}

// downloadsDir resolves the configured downloads folder ("" = ~/Downloads).
func (a *App) downloadsDir() string {
	if a.cfg.DownloadsDir != "" {
		return a.cfg.DownloadsDir
	}
	d := a.prof.Settings.Str("downloads.dir")
	if d == "" || d == "~/Downloads" {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, "Downloads")
		}
		return filepath.Join(os.TempDir(), "sweef-downloads")
	}
	if strings.HasPrefix(d, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, d[2:])
		}
	}
	return d
}

func (a *App) retryDownload(id string) {
	it, ok := a.prof.Downloads.Get(id)
	if !ok {
		return
	}
	a.prof.Downloads.Remove(id)
	a.startDownload(it.URL, it.Name)
}

var _ = state.Tab{}
