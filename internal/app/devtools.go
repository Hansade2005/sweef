package app

// devtools.go: Developer Tools, the docked panel at the bottom of the window (docs/devtools.md).
//
// The panel is part of the chrome page (chrome/devtools.js, loaded when it is first opened); the chrome
// owns the layout, so opening it shrinks the viewport hole like any other chrome and the page is resized
// by the normal layout path. This file is the Go side:
//
//   - which tab the panel is bound to (the active one; it follows tab switches),
//   - the `devtools.req` message and the methods it can call, run on the target tab's worker,
//   - the entry points other code uses: openDevTools(tabID), openDevToolsAt(tabID, x, y) for an
//     "Inspect" item of a page context menu, toggleDevTools for the shortcuts and the menu.
//
// Isolation. Only the chrome page may send devtools.* messages (onUIMessage checks the sender), and a
// message can only address the tab the panel is bound to, which the app chose from this window's tabs; a
// page cannot ask for either. Pages never receive anything from here: results go to the chrome only.
// Tabs showing a sweef:// page can be looked at but not changed or evaluated in. Nothing in this file
// opens a connection: the data is what the engine recorded for the page's own requests.

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/sweefbrowser/sweef/internal/engine"
)

// devtoolsState is the panel's binding (UI loop only).
type devtoolsState struct {
	open  bool
	tab   string // engine tab the panel inspects
	timer *time.Timer
	seen  engine.DevSignals // what the chrome was last told about
	// ready: the panel's script is loaded and listens (it is loaded the first time the panel opens, a moment after
	// the app asks for it). pendSel is an "Inspect" selection that waited for that.
	ready   bool
	pendSel map[string]any
}

// devtoolsPulse is how often the app looks at the inspected tab's counters while the panel is open. The panel itself
// runs no timers (a page that keeps a timer armed is stepped every few milliseconds); it is told when to fetch.
const devtoolsPulse = 300 * time.Millisecond

// devtoolsPanels are the panel names the chrome page knows.
var devtoolsPanels = map[string]bool{"console": true, "network": true, "elements": true, "sources": true, "application": true}

// openDevTools opens Developer Tools for a tab: it becomes the active tab and the panel docks below it.
// Context menus call this for "Inspect" (UI loop only).
func (a *App) openDevTools(tabID string) { a.openDevToolsPanel(tabID, "") }

// openDevToolsPanel is openDevTools with a panel to show ("" keeps the last one).
func (a *App) openDevToolsPanel(tabID, panel string) {
	if tabID == "" {
		tabID = a.model.Active(a.winID)
	}
	e := a.page(tabID)
	if e == nil {
		return
	}
	if a.model.Active(a.winID) != tabID {
		a.activateTab(tabID)
	}
	wasOpen := a.dt.open
	a.bindDevTools(tabID)
	if !wasOpen {
		a.setFocus("chrome") // the panel's prompt and filters are in the chrome page
	}
	if devtoolsPanels[panel] {
		_ = a.prof.Settings.Set("devtools.panel", panel)
	}
	a.markState()
	a.sendChrome("devtools.focus", map[string]any{"panel": panel})
}

// openDevToolsAt opens Developer Tools with the element at a point of the page selected: x and y are CSS pixels
// in the page's viewport, as the page's context menu has them (UI loop only).
func (a *App) openDevToolsAt(tabID string, x, y int) {
	a.openDevToolsPanel(tabID, "elements")
	e := a.page(tabID)
	if e == nil {
		return
	}
	e.w.do(func() {
		id, err := e.t.DevNodeAt(x, y)
		if err != nil {
			return
		}
		path, _ := e.t.DevDOMPath(id)
		sel := map[string]any{"id": id, "path": path}
		a.post(func() {
			if a.dt.ready {
				a.sendChrome("devtools.select", sel)
			} else {
				a.dt.pendSel = sel // the panel has not finished loading: it asks for this with devtools.ready
			}
		})
	})
}

// toggleDevTools opens the panel for the active tab, or closes it.
func (a *App) toggleDevTools() {
	if a.dt.open {
		a.closeDevTools()
		return
	}
	a.openDevTools("")
}

func (a *App) closeDevTools() {
	if !a.dt.open {
		return
	}
	a.unbindDevTools()
	a.dt.open = false
	if a.focus == "chrome" {
		a.setFocus("page") // the panel's inputs are gone with it
	}
	a.markState()
}

// bindDevTools points the panel at a tab.
func (a *App) bindDevTools(tabID string) {
	if a.dt.open && a.dt.tab == tabID {
		return
	}
	a.unbindDevTools()
	a.dt.open, a.dt.tab = true, tabID
	a.dt.seen = engine.DevSignals{}
	if e := a.page(tabID); e != nil {
		e.t.DevAttach(true)
	}
	a.armDevtoolsPulse()
}

func (a *App) armDevtoolsPulse() {
	if a.dt.timer != nil {
		a.dt.timer.Stop()
	}
	a.dt.timer = time.AfterFunc(devtoolsPulse, func() {
		if !a.closing.Load() {
			a.post(a.devtoolsPulse)
		}
	})
}

// devtoolsPulse tells the chrome what changed in the inspected tab since the last pulse (UI loop).
func (a *App) devtoolsPulse() {
	if !a.dt.open {
		return
	}
	defer a.armDevtoolsPulse()
	e := a.page(a.dt.tab)
	if e == nil {
		return
	}
	s := e.t.DevSignals()
	if s == a.dt.seen {
		return
	}
	d := map[string]any{"con": s.Console != a.dt.seen.Console, "net": s.Net != a.dt.seen.Net, "dom": s.Doc != a.dt.seen.Doc, "nav": s.Nav != a.dt.seen.Nav}
	a.dt.seen = s
	a.sendChrome("devtools.dirty", d)
}

// unbindDevTools lets go of the tab the panel was bound to: no highlight stays on its page and it stops keeping
// response bodies for the Network panel.
func (a *App) unbindDevTools() {
	if a.dt.timer != nil {
		a.dt.timer.Stop()
		a.dt.timer = nil
	}
	if a.dt.tab == "" {
		return
	}
	if e := a.page(a.dt.tab); e != nil {
		e.t.DevAttach(false)
		if e.t.DevHighlight(0) {
			e.requestPaint(false)
		}
	}
	a.dt.tab = ""
}

// devtoolsFollow re-binds the open panel to the tab that just became active.
func (a *App) devtoolsFollow(tabID string) {
	if !a.dt.open || a.dt.tab == tabID {
		return
	}
	a.unbindDevTools()
	a.bindDevTools(tabID)
	a.markState()
}

// devtoolsState is what the chrome page reads in state.devtools (nil when closed).
func (a *App) devtoolsSnapshot() map[string]any {
	if !a.dt.open {
		return nil
	}
	ro := false
	if st, ok := a.model.Tab(a.dt.tab); ok {
		ro = strings.HasPrefix(st.URL, "sweef://")
	}
	return map[string]any{"open": true, "tab": a.dt.tab, "readOnly": ro}
}

// ---- messages --------------------------------------------------------------------------------------------

type devtoolsReq struct {
	ID int64           `json:"id"`
	M  string          `json:"m"`
	P  json.RawMessage `json:"p"`
}

// onDevtoolsMessage handles devtools.* messages from the chrome page (UI loop).
func (a *App) onDevtoolsMessage(src *etab, typ string, raw json.RawMessage) {
	if src == nil || src.kind != "chrome" {
		from := "no tab"
		if src != nil {
			from = "a " + src.kind + " tab"
		}
		a.logf("devtools: %q refused from %s", typ, from)
		return
	}
	switch typ {
	case "devtools.open":
		p := decode[struct {
			Panel string `json:"panel"`
		}](raw)
		a.openDevToolsPanel(a.model.Active(a.winID), p.Panel)
	case "devtools.close":
		a.closeDevTools()
	case "devtools.ready":
		a.dt.ready = true
		if a.dt.pendSel != nil && a.dt.open {
			a.sendChrome("devtools.select", a.dt.pendSel)
		}
		a.dt.pendSel = nil
	case "devtools.req":
		a.devtoolsRequest(decode[devtoolsReq](raw))
	default:
		a.logf("devtools: unknown message %q", typ)
	}
}

// devtoolsRequest runs one call on the bound tab's worker and answers the chrome.
func (a *App) devtoolsRequest(r devtoolsReq) {
	reply := func(res any, err error) {
		m := map[string]any{"id": r.ID}
		if err != nil {
			m["error"] = err.Error()
		} else {
			m["result"] = res
		}
		a.sendChrome("devtools.res", m)
	}
	if !a.dt.open {
		reply(nil, errors.New("Developer Tools is not open"))
		return
	}
	id := a.dt.tab
	e := a.page(id)
	if e == nil {
		reply(nil, errors.New("the tab is gone"))
		return
	}
	ro := false
	if st, ok := a.model.Tab(id); ok {
		ro = strings.HasPrefix(st.URL, "sweef://")
	}
	if !e.w.do(func() {
		res, err := a.devtoolsCall(e, r.M, r.P, ro)
		reply(res, err)
	}) {
		reply(nil, errors.New("the tab is closed"))
	}
}

var errReadOnly = errors.New("this is a Sweef page: it can be looked at but not changed or run code in")

// devtoolsCall is the method table (tab worker only).
func (a *App) devtoolsCall(e *etab, m string, raw json.RawMessage, readOnly bool) (any, error) {
	t := e.t
	switch m {
	case "info":
		return map[string]any{"url": t.URL(), "title": t.Title(), "gen": t.DevSignals().Doc, "readOnly": readOnly,
			"loading": t.LoadPending(), "nav": t.DevNav()}, nil
	case "console.get":
		p := decode[struct {
			Since int64 `json:"since"`
		}](raw)
		out := []map[string]any{}
		for _, ev := range t.ConsoleLog(p.Since) {
			d, _ := ev.Data.(map[string]string)
			if d == nil {
				continue
			}
			ent := map[string]any{"sid": ev.SID, "level": d["level"], "text": d["text"], "at": ev.At.UnixMilli()}
			if d["loc"] != "" {
				ent["loc"] = d["loc"]
			}
			out = append(out, ent)
		}
		return out, nil
	case "console.eval":
		if readOnly {
			return nil, errReadOnly
		}
		p := decode[struct {
			Expr string `json:"expr"`
		}](raw)
		r, err := t.DevEval(p.Expr)
		e.afterOp()
		return r, err
	case "net.get":
		p := decode[struct {
			Since   int64   `json:"since"`
			Pending []int64 `json:"pending"`
		}](raw)
		return map[string]any{"entries": t.DevNetUpdates(p.Since, p.Pending), "now": time.Now().UnixMilli()}, nil
	case "net.clear":
		t.DevClearNetwork()
		return true, nil
	case "net.body":
		p := decode[struct {
			ID int64 `json:"id"`
		}](raw)
		return devtoolsBody(t, p.ID)
	case "dom.root":
		return t.DevDOMRoot()
	case "dom.children":
		p := decode[struct {
			ID    int64 `json:"id"`
			Depth int   `json:"depth"`
		}](raw)
		return t.DevDOMChildren(p.ID, min(max(p.Depth, 0), 3))
	case "dom.node":
		return t.DevDOMNode(decode[idInt](raw).ID)
	case "dom.path":
		return t.DevDOMPath(decode[idInt](raw).ID)
	case "dom.style":
		return t.DevStyle(decode[idInt](raw).ID)
	case "dom.at":
		p := decode[struct{ X, Y int }](raw)
		id, err := t.DevNodeAt(p.X, p.Y)
		if err != nil {
			return nil, err
		}
		path, _ := t.DevDOMPath(id)
		return map[string]any{"id": id, "path": path}, nil
	case "dom.highlight":
		if t.DevHighlight(decode[idInt](raw).ID) {
			e.requestPaint(false)
		}
		return true, nil
	case "dom.setAttr", "dom.removeAttr", "dom.setText", "dom.remove":
		if readOnly {
			return nil, errReadOnly
		}
		p := decode[struct {
			ID    int64  `json:"id"`
			Name  string `json:"name"`
			Value string `json:"value"`
		}](raw)
		var err error
		switch m {
		case "dom.setAttr":
			err = t.DevSetAttr(p.ID, p.Name, p.Value)
		case "dom.removeAttr":
			err = t.DevRemoveAttr(p.ID, p.Name)
		case "dom.setText":
			err = t.DevSetText(p.ID, p.Value)
		case "dom.remove":
			err = t.DevRemoveNode(p.ID)
		}
		if err == nil {
			e.requestPaint(false)
		}
		return true, err
	case "src.list":
		return t.DevSources(), nil
	case "src.get":
		p := decode[struct {
			Key string `json:"key"`
		}](raw)
		txt, err := t.DevSource(p.Key)
		if errors.Is(err, engine.ErrNotFound) {
			return nil, errors.New("the text of this file was not kept; reload the page with Developer Tools open")
		}
		if len(txt) > 3<<20 {
			txt = txt[:3<<20]
		}
		return map[string]any{"text": txt}, err
	case "app.get":
		local, session := t.DevStorage()
		if readOnly {
			local, session = nil, nil
		}
		return map[string]any{"cookies": t.DevCookies(), "local": local, "session": session, "origin": t.URL()}, nil
	}
	return nil, errors.New("unknown method " + m)
}

type idInt struct {
	ID int64 `json:"id"`
}

// devtoolsBody describes a stored response body for the preview: text and JSON as text, images as a data URL.
func devtoolsBody(t *engine.Tab, id int64) (any, error) {
	body, en, ok := t.DevNetBody(id)
	if !ok {
		return map[string]any{"kind": "none", "size": en.BodySize}, nil
	}
	mime := strings.ToLower(en.MIME)
	switch {
	case strings.HasPrefix(mime, "image/") && !strings.Contains(mime, "svg"):
		return map[string]any{"kind": "image", "mime": mime, "size": en.BodySize, "truncated": en.Truncated,
			"data": "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(body)}, nil
	case isTextMIME(mime) || isProbablyText(body):
		txt := string(body)
		if len(txt) > 512<<10 {
			txt, en.Truncated = txt[:512<<10], true
		}
		kind := "text"
		if strings.Contains(mime, "json") {
			kind = "json"
		}
		return map[string]any{"kind": kind, "mime": mime, "size": en.BodySize, "truncated": en.Truncated, "text": strings.ToValidUTF8(txt, "�")}, nil
	}
	return map[string]any{"kind": "binary", "mime": mime, "size": en.BodySize}, nil
}

func isTextMIME(m string) bool {
	return m == "" || strings.HasPrefix(m, "text/") || strings.Contains(m, "json") || strings.Contains(m, "javascript") ||
		strings.Contains(m, "xml") || strings.Contains(m, "svg") || strings.Contains(m, "x-www-form-urlencoded")
}

func isProbablyText(b []byte) bool {
	n := min(len(b), 512)
	for _, c := range b[:n] {
		if c == 0 {
			return false
		}
	}
	return true
}
