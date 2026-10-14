package app

// ctxmenu.go: right-click menus.
//
// The menu is drawn by the chrome page (chrome/ctx.js), in the layer above the viewport, so it looks and behaves the
// same on every OS and may extend outside the page area. This file decides what is in it for web pages and internal
// pages and runs the chosen action:
//
//	Go -> chrome   ctx.menu  {token, x, y, kind, items:[{id,label,kbd,sep,disabled,danger,checked,sub}]}
//	               ctx.close {}                       (outside click, scroll, blur, resize, tab change)
//	chrome -> Go   ctx.pick  {token, id}              (an item was chosen)
//	               ctx.state {open, token}            (a menu opened or closed; token 0 = a menu the chrome built)
//	page -> Go     ctx.show  {items:[{id,label,...}]} (an internal page's own menu; the choice goes back to it as ctx.pick {id})
//
// Menus for chrome elements (tab strip, omnibox, bookmarks bar, Sidekick) are built in the chrome page itself.
//
// Order of events for a click on a web page: the contextmenu event is dispatched to the page first, with the right
// clientX/clientY and target; the page may preventDefault() it, in which case no menu is shown.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/sweefbrowser/sweef/internal/app/store"
	"github.com/sweefbrowser/sweef/internal/engine"
	"github.com/sweefbrowser/sweef/internal/shell"
)

func shellRight(x, y float64) shell.Event {
	return shell.Event{Kind: shell.EvMouseDown, X: x, Y: y, Button: 2}
}

// ctxItem is one row of a context menu as the chrome page receives it.
type ctxItem struct {
	ID       string    `json:"id,omitempty"`
	Label    string    `json:"label,omitempty"`
	Kbd      string    `json:"kbd,omitempty"`
	Sep      bool      `json:"sep,omitempty"`
	Disabled bool      `json:"disabled,omitempty"`
	Danger   bool      `json:"danger,omitempty"`
	Checked  bool      `json:"checked,omitempty"`
	Sub      []ctxItem `json:"sub,omitempty"`
}

// ctxState is the menu the app has open (UI loop only).
type ctxState struct {
	token      int
	open       bool // a menu the app built is showing
	chromeOpen bool // any menu is showing, including the ones the chrome page builds
	kind       string
	items      []ctxItem
	run        map[string]func()
	x, y       float64
	prevFocus  string
	rightAt    struct {
		x, y float64
		t    time.Time
	}
}

// menuB builds a menu: items with an action, separators, submenus.
type menuB struct {
	items []ctxItem
	run   map[string]func()
	n     *int
}

func newMenuB() *menuB { n := 0; return &menuB{run: map[string]func(){}, n: &n} }

func (m *menuB) id() string { *m.n++; return "m" + strconv.Itoa(*m.n) }

// add appends an item; a nil action makes it a disabled row.
func (m *menuB) add(label, kbd string, fn func()) *ctxItem {
	it := ctxItem{ID: m.id(), Label: label, Kbd: kbd, Disabled: fn == nil}
	if fn != nil {
		m.run[it.ID] = fn
	}
	m.items = append(m.items, it)
	return &m.items[len(m.items)-1]
}

func (m *menuB) addIf(ok bool, label, kbd string, fn func()) {
	if ok {
		m.add(label, kbd, fn)
	} else {
		m.add(label, kbd, nil)
	}
}

func (m *menuB) sep() { m.items = append(m.items, ctxItem{Sep: true}) }

func (m *menuB) sub(label string, build func(s *menuB)) {
	s := &menuB{run: m.run, n: m.n}
	build(s)
	m.items = append(m.items, ctxItem{ID: m.id(), Label: label, Sub: tidy(s.items)})
}

// tidy drops leading, trailing and doubled separators.
func tidy(in []ctxItem) []ctxItem {
	var out []ctxItem
	for _, it := range in {
		if it.Sep && (len(out) == 0 || out[len(out)-1].Sep) {
			continue
		}
		out = append(out, it)
	}
	for len(out) > 0 && out[len(out)-1].Sep {
		out = out[:len(out)-1]
	}
	return out
}

// ctxInfo is what the page reports about the spot that was clicked (jshelpers __sweefPtr('context')).
type ctxInfo struct {
	Prevented bool   `json:"prevented"`
	Link      string `json:"link"`
	LinkText  string `json:"linkText"`
	Image     string `json:"image"`
	Selection string `json:"selection"`
	Editable  bool   `json:"editable"`
	Edit      *struct {
		ReadOnly bool `json:"readOnly"`
		Password bool `json:"password"`
		HasSel   bool `json:"hasSel"`
		Empty    bool `json:"empty"`
		CanUndo  bool `json:"canUndo"`
		CanRedo  bool `json:"canRedo"`
	} `json:"edit"`
	Media *struct {
		Kind     string  `json:"kind"`
		Src      string  `json:"src"`
		Paused   bool    `json:"paused"`
		Muted    bool    `json:"muted"`
		Loop     bool    `json:"loop"`
		Rate     float64 `json:"rate"`
		Controls bool    `json:"controls"`
		PIP      bool    `json:"pip"`
	} `json:"media"`
	Frame string `json:"frame"`
}

// contextMenu runs on the tab's worker after a right button press. The page (or chrome) sees the contextmenu event
// first; the app builds a menu only for page tabs whose script did not cancel it.
func (a *App) contextMenu(e *etab, tgt string, px, py, mods int, wx, wy float64) {
	a.post(func() { a.ctx.rightAt.x, a.ctx.rightAt.y, a.ctx.rightAt.t = wx, wy, time.Now() })
	if tgt == "overlay" {
		return
	}
	tt, lx, ly := e.pointerTarget(tgt, px, py)
	var info ctxInfo
	_ = e.evalOn(tt, "__sweefPtr('context',"+itoa(lx)+","+itoa(ly)+",2,"+itoa(mods)+")", &info)
	e.afterOp()
	if info.Prevented || tgt != "page" || e.kind != "page" {
		return // the chrome page builds the menus of its own elements
	}
	a.post(func() { a.pageContextMenu(e, tt, lx, ly, info, wx, wy) })
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func clip(s string, n int) string {
	r := []rune(strings.Join(strings.Fields(s), " "))
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return string(r)
}

// pageContextMenu builds the menu for a click on a page (UI loop).
func (a *App) pageContextMenu(e *etab, tt *engine.Tab, lx, ly int, info ctxInfo, wx, wy float64) {
	if e.closed.Load() {
		return
	}
	st, _ := a.model.Tab(e.id)
	internal := strings.HasPrefix(st.URL, "sweef:")
	m := newMenuB()
	kind := "page"
	id := e.id
	openBG := func(u string) func() {
		return func() { a.openTab(openOpts{URL: u, Incognito: e.incog, Activate: false, After: id}) }
	}
	copyText := func(s string) func() {
		return func() { _ = a.clip.writeNow([]engine.ClipboardEntry{{Type: "text/plain", Data: []byte(s)}}) }
	}
	hasLink := info.Link != "" && !strings.HasPrefix(strings.ToLower(info.Link), "javascript:")
	if hasLink {
		kind = "link"
		m.add("Open link in new tab", "", openBG(info.Link))
		m.add("Copy link address", "", copyText(info.Link))
		m.addIf(canSaveURL(info.Link), "Save link as…", "", func() { a.saveURLAs(e, info.Link) })
		m.sep()
	}
	if info.Image != "" {
		if kind == "page" {
			kind = "image"
		}
		m.add("Open image in new tab", "", openBG(info.Image))
		m.add("Copy image", "", func() { a.copyImage(e, info.Image) })
		m.add("Copy image address", "", copyText(info.Image))
		m.addIf(canSaveURL(info.Image), "Save image as…", "", func() { a.saveURLAs(e, info.Image) })
		m.sep()
	}
	if md := info.Media; md != nil {
		kind = "media"
		act := func(op, arg string) func() {
			return func() {
				e.w.do(func() {
					_, _ = tt.Evaluate("__sweefCtxAct(" + jsString(op) + "," + itoa(lx) + "," + itoa(ly) + "," + jsString(arg) + ")")
					e.afterOp()
				})
			}
		}
		noun := md.Kind
		if noun != "audio" {
			noun = "video"
		}
		m.add(map[bool]string{true: "Play", false: "Pause"}[md.Paused], "", act("toggle", ""))
		mute := m.add(map[bool]string{true: "Unmute", false: "Mute"}[md.Muted], "", act("mute", ""))
		_ = mute
		loop := m.add("Loop", "", act("loop", ""))
		loop.Checked = md.Loop
		m.sub("Playback speed", func(s *menuB) {
			for _, r := range []float64{0.5, 0.75, 1, 1.25, 1.5, 2} {
				label := strconv.FormatFloat(r, 'f', -1, 64) + "x"
				if r == 1 {
					label = "Normal"
				}
				it := s.add(label, "", act("rate", strconv.FormatFloat(r, 'f', -1, 64)))
				it.Checked = md.Rate == r
			}
		})
		if md.PIP {
			m.add("Picture in picture", "", act("pip", ""))
		}
		m.sep()
		if md.Src != "" {
			m.add("Copy "+noun+" address", "", copyText(md.Src))
			m.addIf(canSaveURL(md.Src), "Save "+noun+" as…", "", func() { a.saveURLAs(e, md.Src) })
			m.sep()
		}
	}
	if info.Editable && info.Edit != nil {
		kind = "editable"
		ed := info.Edit
		canPaste := !ed.ReadOnly && len(a.clip.readNow()) > 0
		hasSel := ed.HasSel && !ed.Password
		do := func(op string) func() {
			return func() { a.editOp(e, op) }
		}
		m.addIf(!ed.ReadOnly && ed.CanUndo, "Undo", a.mod("Z"), do("undo"))
		m.addIf(!ed.ReadOnly && ed.CanRedo, "Redo", a.mod("Shift+Z"), do("redo"))
		m.sep()
		m.addIf(!ed.ReadOnly && hasSel, "Cut", a.mod("X"), func() { a.cutSelection(e) })
		m.addIf(hasSel, "Copy", a.mod("C"), func() { a.copySelection(e) })
		m.addIf(canPaste, "Paste", a.mod("V"), func() { a.pasteClipboard(e) })
		m.addIf(!ed.ReadOnly && ed.HasSel, "Delete", "", do("delete"))
		m.sep()
		m.addIf(!ed.Empty, "Select all", a.mod("A"), func() { a.selectAll(e) })
		m.sep()
		if sel := strings.TrimSpace(info.Selection); sel != "" && !ed.Password {
			a.addSearchItem(m, e, sel)
		}
	} else if sel := strings.TrimSpace(info.Selection); sel != "" {
		if kind == "page" {
			kind = "selection"
		}
		m.add("Copy", a.mod("C"), func() { a.copySelection(e) })
		a.addSearchItem(m, e, sel)
		m.sep()
	}
	plain := kind == "page"
	if plain {
		m.addIf(st.CanBack, "Back", "Alt+Left", func() { a.traverse(id, -1) })
		m.addIf(st.CanForward, "Forward", "Alt+Right", func() { a.traverse(id, +1) })
		m.add("Reload", a.mod("R"), func() { a.reload(id, false) })
		m.sep()
		if !internal {
			m.add("Save page as…", a.mod("S"), func() { a.savePage(e) })
			m.add("Print…", a.mod("P"), func() { a.printToPDF() })
			m.sep()
			m.add("View page source", a.mod("U"), func() { a.newTab("view-source:"+st.URL, st.Incognito, true) })
		}
	}
	m.add("Inspect", "Ctrl+Shift+I", func() { a.openDevTools(id) })
	a.showCtx(kind, wx, wy, m)
}

// mod spells an accelerator for this OS.
func (a *App) mod(k string) string {
	if runtime.GOOS == "darwin" {
		return "Cmd+" + k
	}
	return "Ctrl+" + k
}

func (a *App) addSearchItem(m *menuB, e *etab, sel string) {
	eng := a.engineSearch()
	q := sel
	m.add("Search "+eng.Name+" for “"+clip(sel, 26)+"”", "", func() {
		a.openTab(openOpts{URL: eng.SearchURL(q), Incognito: e.incog, Activate: true, After: e.id})
	})
}

func canSaveURL(raw string) bool {
	l := strings.ToLower(raw)
	return strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://") || strings.HasPrefix(l, "data:")
}

// ---- showing, closing, choosing ---------------------------------------------------------------------------

// showCtx sends a built menu to the chrome page. Keys go to the menu while it is open.
func (a *App) showCtx(kind string, x, y float64, m *menuB) {
	items := tidy(m.items)
	if len(items) == 0 {
		return
	}
	if !a.ctx.open {
		a.ctx.prevFocus = a.focus
	}
	a.ctx.token++
	a.ctx.open, a.ctx.chromeOpen = true, true
	a.ctx.kind, a.ctx.items, a.ctx.run, a.ctx.x, a.ctx.y = kind, items, m.run, x, y
	a.setFocus("chrome")
	a.sendChrome("ctx.menu", map[string]any{"token": a.ctx.token, "x": x, "y": y, "kind": kind, "items": items})
}

// ctxDismiss closes the menu when something outside it happens (scroll, resize, window blur, tab change).
func (a *App) ctxDismiss() {
	if !a.ctx.open && !a.ctx.chromeOpen {
		return
	}
	a.sendChrome("ctx.close", map[string]any{})
	a.ctx.chromeOpen = false
	a.ctxClosed()
}

// ctxClosed forgets the app's menu and gives the keyboard back to where it was.
func (a *App) ctxClosed() {
	if !a.ctx.open {
		return
	}
	a.ctx.open = false
	if a.ctx.prevFocus != "" && a.focus == "chrome" {
		a.setFocus(a.ctx.prevFocus)
	}
	a.ctx.prevFocus = ""
}

// onCtxMessage handles the ctx.* and edit.* messages. It reports whether typ was one of them.
func (a *App) onCtxMessage(src *etab, typ string, raw json.RawMessage) bool {
	switch typ {
	case "ctx.state":
		p := decode[struct {
			Open  bool `json:"open"`
			Token int  `json:"token"`
		}](raw)
		a.ctx.chromeOpen = p.Open
		if !p.Open && (p.Token == a.ctx.token || p.Token == 0) {
			a.ctxClosed()
		}
	case "ctx.pick":
		p := decode[struct {
			Token int    `json:"token"`
			ID    string `json:"id"`
		}](raw)
		if p.Token != a.ctx.token || a.ctx.run == nil {
			return true
		}
		fn := a.ctx.run[p.ID]
		a.ctx.run = nil
		a.ctxClosed()
		if fn != nil {
			fn()
		}
	case "ctx.rightclick":
		// a right click on the scrim that closed the menu: open the menu of whatever is under it, as other browsers do
		p := decode[struct {
			X float64 `json:"x"`
			Y float64 `json:"y"`
		}](raw)
		a.holeBusy.Store(false)
		a.onMouseButton(shellRight(p.X, p.Y), true)
		a.onMouseButton(shellRight(p.X, p.Y), false)
	case "ctx.show":
		a.onPageMenu(src, raw)
	case "edit.op":
		a.editOp(a.focusTab(), decode[struct {
			Op string `json:"op"`
		}](raw).Op)
	case "omni.pasteAndGo":
		if c := a.clip.readNow(); len(c) > 0 {
			for _, f := range c {
				if f.Type == "text/plain" {
					a.navigateInput(a.model.Active(a.winID), strings.TrimSpace(string(f.Data)))
					break
				}
			}
		}
	default:
		return false
	}
	return true
}

// onPageMenu shows a menu an internal page built (ctx.show). The page keeps its own actions: a choice is sent back
// to it as ctx.pick {id}.
func (a *App) onPageMenu(src *etab, raw json.RawMessage) {
	if src == nil || src.kind != "page" {
		return
	}
	var p struct {
		Items []ctxItem `json:"items"`
	}
	if json.Unmarshal(raw, &p) != nil || len(p.Items) == 0 || len(p.Items) > 40 {
		return
	}
	m := newMenuB()
	var walk func(in []ctxItem, out *menuB, depth int)
	walk = func(in []ctxItem, out *menuB, depth int) {
		for _, it := range in {
			if it.Sep {
				out.sep()
				continue
			}
			it := it
			if len(it.Sub) > 0 && depth < 2 {
				out.sub(clip(it.Label, 60), func(s *menuB) { walk(it.Sub, s, depth+1) })
				continue
			}
			var fn func()
			if !it.Disabled {
				pid := it.ID
				fn = func() {
					if !src.closed.Load() {
						a.sendTo(src, "ctx.pick", map[string]any{"id": pid})
					}
				}
			}
			ni := out.add(clip(it.Label, 80), clip(it.Kbd, 20), fn)
			ni.Danger, ni.Checked = it.Danger, it.Checked
		}
	}
	walk(p.Items, m, 0)
	x, y := a.ctx.rightAt.x, a.ctx.rightAt.y
	if time.Since(a.ctx.rightAt.t) > 3*time.Second {
		x, y = a.vpLog.X+a.vpLog.W/2, a.vpLog.Y+a.vpLog.H/3
	}
	a.showCtx("page-ui", x, y, m)
}

// ---- actions ---------------------------------------------------------------------------------------------

// The edit actions below go through clipboardOp (input.go) for the focused document. They are thin on purpose: the
// selection work lives in the engine and clipboardOp, and these only make sure the keyboard focus is on the tab the
// menu was opened for.

func (a *App) focusFor(e *etab) {
	if e == nil {
		return
	}
	if e.kind == "page" {
		a.focus = "page"
	}
}

func (a *App) copySelection(e *etab) { a.focusFor(e); a.clipboardOp("copy") }
func (a *App) cutSelection(e *etab)  { a.focusFor(e); a.clipboardOp("cut") }
func (a *App) pasteClipboard(e *etab) {
	a.focusFor(e)
	a.clipboardOp("paste")
}
func (a *App) selectAll(e *etab) { a.focusFor(e); a.clipboardOp("selectall") }

// editOp runs undo, redo, delete (and the clipboard ops) in the document that has the keyboard.
func (a *App) editOp(e *etab, op string) {
	if e == nil {
		return
	}
	switch op {
	case "copy":
		a.copySelection(e)
	case "cut":
		a.cutSelection(e)
	case "paste":
		a.pasteClipboard(e)
	case "selectall":
		a.selectAll(e)
	case "undo", "redo", "delete":
		a.focusFor(e)
		fe := a.focusTab()
		if fe == nil {
			return
		}
		fe.w.do(func() {
			_, _ = fe.inputTab().Evaluate("__sweefEdit(" + jsString(op) + ")")
			fe.afterOp()
			if fe.kind == "page" {
				a.post(func() { a.afterPageInput(fe) })
			}
		})
	}
}

func fileNameOfURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "download"
	}
	if b := path.Base(u.Path); b != "" && b != "/" && b != "." {
		return b
	}
	if h := u.Hostname(); h != "" {
		return h
	}
	return "download"
}

// saveURLAs saves a link, image or media address into the downloads folder (the downloads list shows progress).
func (a *App) saveURLAs(e *etab, raw string) {
	if strings.HasPrefix(strings.ToLower(raw), "data:") {
		mime, data, err := decodeDataURL(raw)
		if err != nil {
			a.sendChrome(mToast, map[string]any{"text": "Could not save: " + err.Error(), "kind": "error"})
			return
		}
		name := "download" + extForMime(mime)
		a.saveBytes(e, raw, name, mime, data)
		return
	}
	if !canSaveURL(raw) {
		a.sendChrome(mToast, map[string]any{"text": "This address cannot be saved", "kind": "warn"})
		return
	}
	a.startDownload(raw, fileNameOfURL(raw))
}

func (a *App) saveBytes(e *etab, srcURL, name, mime string, data []byte) {
	dir := a.downloadsDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		a.sendChrome(mToast, map[string]any{"text": "Could not save: " + err.Error(), "kind": "error"})
		return
	}
	name = store.SafeFilename(name)
	p := uniqueFile(filepath.Join(dir, name))
	if err := os.WriteFile(p, data, 0o600); err != nil {
		a.sendChrome(mToast, map[string]any{"text": "Could not save: " + err.Error(), "kind": "error"})
		return
	}
	it := a.profileFor(e != nil && e.incog).Downloads.Record(srcURL, filepath.Base(p), p, int64(len(data)), mime)
	a.sendChrome(mToast, map[string]any{"text": "Saved " + it.Name, "kind": "info"})
}

func uniqueFile(p string) string {
	if _, err := os.Stat(p); os.IsNotExist(err) {
		return p
	}
	ext := filepath.Ext(p)
	base := strings.TrimSuffix(p, ext)
	for i := 1; ; i++ {
		q := base + " (" + strconv.Itoa(i) + ")" + ext
		if _, err := os.Stat(q); os.IsNotExist(err) {
			return q
		}
	}
}

func extForMime(m string) string {
	switch strings.ToLower(strings.SplitN(m, ";", 2)[0]) {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/svg+xml":
		return ".svg"
	case "text/html":
		return ".html"
	case "text/plain":
		return ".txt"
	}
	return ""
}

// decodeDataURL splits a data: URL into its media type and bytes.
func decodeDataURL(raw string) (string, []byte, error) {
	rest := strings.TrimPrefix(raw, raw[:5])
	i := strings.IndexByte(rest, ',')
	if i < 0 {
		return "", nil, io.ErrUnexpectedEOF
	}
	meta, body := rest[:i], rest[i+1:]
	mime := strings.SplitN(meta, ";", 2)[0]
	if mime == "" {
		mime = "text/plain"
	}
	if strings.HasSuffix(meta, ";base64") {
		b, err := base64.StdEncoding.DecodeString(body)
		if err != nil {
			b, err = base64.RawStdEncoding.DecodeString(body)
		}
		return mime, b, err
	}
	s, err := url.PathUnescape(body)
	return mime, []byte(s), err
}

// savePage writes the page's current HTML into the downloads folder.
func (a *App) savePage(e *etab) {
	st, _ := a.model.Tab(e.id)
	e.w.do(func() {
		html := e.t.HTML(false)
		a.post(func() {
			name := store.SafeFilename(orURL(st.Title, "page")) + ".html"
			a.saveBytes(e, st.URL, name, "text/html", []byte(html))
		})
	})
}

// copyImage fetches an image and puts it on the clipboard as PNG (and its address as text, which is what other
// applications see: the window clipboard carries text only).
func (a *App) copyImage(e *etab, src string) {
	fail := func(msg string) {
		a.post(func() { a.sendChrome(mToast, map[string]any{"text": msg, "kind": "error"}) })
	}
	finish := func(mime string, data []byte) {
		if mime != "image/png" {
			if img, _, err := image.Decode(bytes.NewReader(data)); err == nil {
				var b bytes.Buffer
				if png.Encode(&b, img) == nil {
					mime, data = "image/png", b.Bytes()
				}
			}
		}
		a.post(func() {
			_ = a.clip.writeNow([]engine.ClipboardEntry{{Type: "text/plain", Data: []byte(src)}, {Type: mime, Data: data}})
		})
	}
	if strings.HasPrefix(strings.ToLower(src), "data:") {
		mime, data, err := decodeDataURL(src)
		if err != nil {
			fail("Could not copy the image")
			return
		}
		go finish(mime, data)
		return
	}
	if !canSaveURL(src) {
		fail("Could not copy the image")
		return
	}
	cookie, ua := e.t.Context().JarRef().ForURL(src), e.t.UserAgent()
	page := a.model.Active(a.winID)
	go func() {
		req, err := http.NewRequest("GET", src, nil)
		if err != nil {
			fail("Could not copy the image")
			return
		}
		req.Header.Set("User-Agent", ua)
		if cookie != "" {
			req.Header.Set("Cookie", cookie)
		}
		if st, ok := a.model.Tab(page); ok && strings.HasPrefix(st.URL, "http") {
			req.Header.Set("Referer", st.URL)
		}
		resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
		if err != nil {
			fail("Could not copy the image")
			return
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		if err != nil || resp.StatusCode >= 400 {
			fail("Could not copy the image")
			return
		}
		finish(strings.SplitN(resp.Header.Get("Content-Type"), ";", 2)[0], data)
	}()
}
