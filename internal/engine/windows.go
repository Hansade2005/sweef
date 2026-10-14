package engine

// windows.go: top-level windows that can reference each other. Frames (frames.go) and these windows share one
// window model: every Tab has a winID, a script holds only ids, and a Proxy in frame_prelude.js reaches another
// window through the host functions of frames_host.go. This file adds what frames do not have:
//
//   - the opener relation and popups: window.open, link and form targets, window.close, window.name;
//   - transient user activation and the popup blocker;
//   - Cross-Origin-Opener-Policy severing of the opener link;
//   - navigation requests that another window makes of a top-level tab.
//
// postMessage, same-origin property access (win.document, opener.fn()), BroadcastChannel and storage events across
// windows are the frame machinery's: a popup of the same page group is reached exactly like a frame.

import (
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultActivationWindow is how long a click or key press keeps allowing popups.
const DefaultActivationWindow = 5 * time.Second

// frameWaitBudget bounds how long a headless navigation waits for the frames it started.
const frameWaitBudget = 4 * time.Second

// winState is the per-Tab part of the window model that frames.go does not cover.
type winState struct {
	opener       atomic.Pointer[Tab]
	severed      atomic.Bool // COOP cut the opener link: the former opener sees this window as closed
	scriptOpened bool        // created by window.open / a target link (window.close() is then allowed)
	winClosed    atomic.Bool // script asked to close it; the tab is going away
	name         string      // window.name of a top-level window (guarded by Tab.mu)

	coopMu     sync.Mutex
	coop       string // COOP value of the current document: unsafe-none | same-origin-allow-popups | same-origin
	coopOrigin string
	openees    []*Tab // windows this one opened (for COOP severing and window groups)

	activation atomic.Int64 // unix nanos of the last user activation
	actWindow  atomic.Int64 // activation lifetime in nanos (0 = DefaultActivationWindow)

	taskMu sync.Mutex
	tasks  []func()

	popups *Ring // PopupEvent log (allowed and blocked)

	// painting and input of frames (frames_paint.go, frames_input.go)
	frameGen     atomic.Uint64
	framesPaused atomic.Bool
	focusFrame   atomic.Pointer[Tab] // frame holding keyboard focus inside this document
	paintDSF     atomic.Uint64       // math.Float64bits of the scale of the last paint
	fp           framePaint          // (a frame) its cached pixels and box
	lazyMu       sync.Mutex
	lazyOK       map[int64]bool       // lazy frames that were released
	lazyBox      map[int64][2]float64 // lazy frames waiting: y and height of the box
}

// PopupEvent is one window.open / target=_blank decision, allowed or not.
type PopupEvent struct {
	Opener  string    `json:"opener"`
	Popup   string    `json:"popup,omitempty"`
	URL     string    `json:"url"`
	Name    string    `json:"name,omitempty"`
	Blocked bool      `json:"blocked"`
	Reason  string    `json:"reason,omitempty"` // no user activation | sandbox | scheme
	At      time.Time `json:"at"`
}

// PopupRequest is handed to the embedder's popup handler once the Popup tab exists.
type PopupRequest struct {
	Opener, Popup *Tab
	URL           string // absolute, "about:blank" for window.open("")
	Name          string
	Features      string
	Referer       string
	NoOpener      bool
	Post          func(dest *Tab) error // non-nil for a form submitted with method=post to a new window
}

// winRegistry holds the embedder's window hooks.
type winRegistry struct {
	mu       sync.RWMutex
	popup    func(PopupRequest)
	closeWin func(*Tab)
	focusWin func(*Tab)
}

// SetPopupHandler registers the embedder's handler for windows created by page script. The Popup tab already
// exists (about:blank, opener linked); the handler must show it and start its navigation to req.URL (or call
// Popup.Navigate). Without a handler the engine navigates the popup itself in the background (CLI, tests).
func (e *Engine) SetPopupHandler(f func(PopupRequest)) {
	e.wm.mu.Lock()
	e.wm.popup = f
	e.wm.mu.Unlock()
}

// SetWindowCloseHandler registers what happens when script calls window.close() on a tab it may close: the
// embedder should close the tab. Without a handler the engine closes it.
func (e *Engine) SetWindowCloseHandler(f func(*Tab)) {
	e.wm.mu.Lock()
	e.wm.closeWin = f
	e.wm.mu.Unlock()
}

// SetWindowFocusHandler registers what happens when script calls focus() on a popup.
func (e *Engine) SetWindowFocusHandler(f func(*Tab)) {
	e.wm.mu.Lock()
	e.wm.focusWin = f
	e.wm.mu.Unlock()
}

// SetFrameReadFilter sets a predicate that decides, per frame document URL, whether the snapshot may include the
// frame and refs may act inside it. The desktop app uses it so that an embedded page the person did not allow the
// agent to read stays unread even when the page that embeds it is allowed. nil allows every frame.
func (e *Engine) SetFrameReadFilter(f func(frameURL string) bool) {
	if f == nil {
		e.frameRead.Store(nil)
		return
	}
	e.frameRead.Store(&f)
}

func (e *Engine) frameReadable(frameURL string) bool {
	if f := e.frameRead.Load(); f != nil {
		return (*f)(frameURL)
	}
	return true
}

// navSchemeAllowed: a web page cannot make a window show the browser's own pages or local files (a document may
// open its own kind: a sweef:// page another sweef:// page).
func navSchemeAllowed(fromURL, target string) bool {
	l := strings.ToLower(strings.TrimSpace(target))
	from := strings.ToLower(fromURL)
	switch {
	case strings.HasPrefix(l, "sweef:"):
		return strings.HasPrefix(from, "sweef:")
	case strings.HasPrefix(l, "file:"):
		return strings.HasPrefix(from, "file:")
	case strings.HasPrefix(l, "view-source:"), strings.HasPrefix(l, "chrome:"), strings.HasPrefix(l, "chrome-extension:"):
		return false
	}
	return true
}

// ---- relations ------------------------------------------------------------------

// Parent is the embedding tab for a frame document, nil otherwise.
func (t *Tab) Parent() *Tab {
	if t.isFrame() {
		return t.bc.parent
	}
	return nil
}

// Top is the top-level tab of a frame tree (the tab itself when it is top-level).
func (t *Tab) Top() *Tab {
	if top := t.topTab(); top != nil {
		return top
	}
	return t
}

// Opener is the window that opened this one with script (nil after noopener, a COOP severing, or when the opener is
// gone).
func (t *Tab) Opener() *Tab {
	t = t.Top()
	if o := t.win.opener.Load(); o != nil && !o.closed.Load() && !t.win.severed.Load() {
		return o
	}
	return nil
}

// groupRoot is the first window of the opener chain this window belongs to: windows with the same root can reach
// each other (a popup, its opener and the popups of the same opener).
func (t *Tab) groupRoot() *Tab {
	c := t.Top()
	for i := 0; i < 32; i++ {
		o := c.Opener()
		if o == nil {
			return c
		}
		c = o.Top()
	}
	return c
}

// windowsRelated reports whether script in a may hold a reference to b: same page tree, or the same opener group.
func windowsRelated(a, b *Tab) bool {
	ta, tb := a.Top(), b.Top()
	return ta == tb || ta.groupRoot() == tb.groupRoot()
}

// WindowName is window.name.
func (t *Tab) WindowName() string { return t.windowName() }

func (t *Tab) setWindowName(n string) {
	if t.bc != nil && t.bc.isFrame {
		return // a frame's name is its iframe's name
	}
	t.mu.Lock()
	t.win.name = n
	t.mu.Unlock()
}

// SetWindowName sets window.name (used by embedders that open tabs for a target).
func (t *Tab) SetWindowName(n string) { t.setWindowName(n) }

// IsClosed reports whether the tab has been closed.
func (t *Tab) IsClosed() bool { return t.closed.Load() }

// WindowClosed reports whether the window is gone as far as other windows can tell.
func (t *Tab) WindowClosed() bool {
	return t.closed.Load() || t.win.winClosed.Load() || t.win.severed.Load()
}

// Sandboxed reports whether the document is inside a sandboxed frame.
func (t *Tab) Sandboxed() bool { return t.bc != nil && t.bc.isFrame && t.bc.sandbox&sbSandboxed != 0 }

// ScriptsAllowed reports whether script (including the embedder's input helper) can run in the document.
func (t *Tab) ScriptsAllowed() bool { return t.scriptsAllowed() }

// sandboxFlags are the sandbox flags of a frame (0 for a window that is not in a sandbox).
func (t *Tab) sandboxFlags() uint32 {
	if t.bc != nil && t.bc.isFrame {
		return t.bc.sandbox
	}
	return 0
}

// ---- user activation and the popup blocker ---------------------------------------

// SetActivationWindow sets how long a user gesture keeps allowing popups (default 5 s).
func (t *Tab) SetActivationWindow(d time.Duration) { t.Top().win.actWindow.Store(int64(d)) }

// NoteUserActivation records a trusted user gesture (click, tap, key press) in this document and in the documents
// that embed it. Embedders call it from real input; the engine calls it for agent actions that stand in for a person.
func (t *Tab) NoteUserActivation() {
	now := time.Now().UnixNano()
	for c := t; c != nil; {
		c.win.activation.Store(now)
		if !c.isFrame() {
			break
		}
		c = c.bc.parent
	}
	t.Top().pw.activate()
}

// HasTransientActivation reports whether a user gesture happened recently enough to allow a popup.
func (t *Tab) HasTransientActivation() bool {
	at := t.win.activation.Load()
	if at == 0 {
		return false
	}
	w := time.Duration(t.Top().win.actWindow.Load())
	if w <= 0 {
		w = DefaultActivationWindow
	}
	return time.Since(time.Unix(0, at)) <= w
}

func (t *Tab) consumeActivation() {
	for c := t; c != nil; {
		c.win.activation.Store(0)
		if !c.isFrame() {
			break
		}
		c = c.bc.parent
	}
}

// PopupLog returns the allowed and blocked window.open decisions of this tab.
func (t *Tab) PopupLog(sinceSID int64) []Event { return t.win.popups.Since(sinceSID) }

// BlockedPopups lists the popups the blocker refused in this tab.
func (t *Tab) BlockedPopups() []PopupEvent {
	var out []PopupEvent
	for _, e := range t.win.popups.All() {
		if pe, ok := e.Data.(PopupEvent); ok && pe.Blocked {
			out = append(out, pe)
		}
	}
	return out
}

func (t *Tab) recordPopup(pe PopupEvent) {
	pe.Opener, pe.At = t.ID, time.Now()
	ev := t.win.popups.Push(Event{Tab: t.ID, Kind: EvPopup, At: pe.At, Data: pe})
	t.Top().emit(EvPopup, ev.Data) // the window that owns the frame tree is the one the embedder knows
	if pe.Blocked {
		t.LogConsole("warning", "Blocked a popup to "+pe.URL+": "+pe.Reason)
	}
}

// ---- tasks run by a top-level tab's own event loop ---------------------------------

// queueTask hands f to the goroutine that owns the tab: it runs at the start of the tab's next event-loop slice
// (Settle, a window pump). Used for navigations that another window asks of a top-level tab.
func (t *Tab) queueTask(f func()) {
	t.win.taskMu.Lock()
	t.win.tasks = append(t.win.tasks, f)
	t.win.taskMu.Unlock()
	t.wake()
}

func (t *Tab) runTasks() bool {
	t.win.taskMu.Lock()
	ts := t.win.tasks
	t.win.tasks = nil
	t.win.taskMu.Unlock()
	for _, f := range ts {
		f()
	}
	return len(ts) > 0
}

// ---- window.open, targets ----------------------------------------------------------

// navSpec describes one navigation request that names a target.
type navSpec struct {
	rawURL     string
	target     string
	features   string
	fromScript bool // window.open (opener kept unless noopener) as opposed to a link or form
	noOpener   bool
	noReferrer bool
	post       func(dest *Tab) error // form POST: how to submit into the destination
}

func parseFeatures(f string) (noopener, noreferrer bool) {
	for _, tok := range strings.FieldsFunc(strings.ToLower(f), func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
		k := tok
		if i := strings.IndexByte(tok, '='); i >= 0 {
			k = tok[:i]
		}
		switch k {
		case "noopener":
			noopener = true
		case "noreferrer":
			noopener, noreferrer = true, true
		}
	}
	return
}

// openFromScript is window.open(url, target, features). It returns the window the call refers to (a new popup, an
// existing named window or frame), or nil when the call is blocked or the popup is opened with noopener.
func (t *Tab) openFromScript(rawURL, target, features string) *Tab {
	noop, noref := parseFeatures(features)
	return t.navigateTarget(navSpec{rawURL: rawURL, target: target, features: features, fromScript: true, noOpener: noop, noReferrer: noref})
}

// navigateTarget resolves a navigation target (_self, _top, _parent, _blank, a frame or window name) and starts the
// navigation there.
func (t *Tab) navigateTarget(s navSpec) *Tab {
	base := t.URL()
	abs := strings.TrimSpace(s.rawURL)
	if abs == "" || strings.EqualFold(abs, "about:blank") {
		abs = "about:blank"
	} else if isScriptURL(abs) { // javascript: and vbscript:; data: is judged by navSchemeAllowed below
		abs = "about:blank"
	} else if u, err := resolveURL(base, abs); err == nil {
		abs = u
	}
	if !navSchemeAllowed(base, abs) {
		t.recordPopup(PopupEvent{URL: abs, Name: s.target, Blocked: true, Reason: "scheme: not allowed from this page"})
		return nil
	}
	key := strings.ToLower(strings.TrimSpace(s.target))
	if key == "" {
		if s.fromScript {
			key = "_blank"
		} else {
			key = "_self"
		}
	}
	switch key {
	case "_self":
		if f := t.sandboxFlags(); s.post != nil && f&sbSandboxed != 0 && f&sbForms == 0 {
			return nil
		}
		t.queueNavigation(abs, s, t)
		return t
	case "_parent", "_top":
		dest := t.Top()
		if key == "_parent" && t.isFrame() {
			dest = t.bc.parent
		}
		if dest != t && !t.mayNavigateAncestor() {
			t.recordPopup(PopupEvent{URL: abs, Blocked: true, Reason: "sandbox: top-level navigation not allowed"})
			return nil
		}
		t.queueNavigation(abs, s, dest)
		return dest
	case "_blank":
		return t.openNewWindow(abs, "", s)
	}
	// a named target: a frame of this page, then an existing window, then a new one
	if f := t.Top().findFrameByName(s.target); f != nil {
		t.queueNavigation(abs, s, f)
		return f
	}
	if w := t.eng.findWindowByName(t, s.target); w != nil {
		if !t.HasTransientActivation() && s.fromScript && w != t {
			t.recordPopup(PopupEvent{URL: abs, Name: s.target, Blocked: true, Reason: "no user activation"})
			return nil
		}
		t.queueNavigation(abs, s, w)
		return w
	}
	return t.openNewWindow(abs, s.target, s)
}

// findFrameByName finds a frame (at any depth) of t's tree whose window name matches.
func (t *Tab) findFrameByName(name string) *Tab {
	for _, k := range t.FrameTabs() {
		if k.windowName() == name {
			return k
		}
		if g := k.findFrameByName(name); g != nil {
			return g
		}
	}
	return nil
}

// mayNavigateAncestor: the sandboxes between this frame and the top allow navigating the top-level page.
func (t *Tab) mayNavigateAncestor() bool {
	for c := t; c != nil && c.isFrame(); c = c.bc.parent {
		f := c.bc.sandbox
		if f&sbSandboxed != 0 && !(f&sbTopNav != 0 || (f&sbTopNavUser != 0 && t.HasTransientActivation())) {
			return false
		}
	}
	return true
}

// queueNavigation starts the navigation in dest: a frame navigates on its own goroutine, a top-level tab on the
// goroutine that owns it (its window's worker), by a task.
func (t *Tab) queueNavigation(abs string, s navSpec, dest *Tab) {
	hdrs := map[string]string(nil)
	if !s.noReferrer && dest != t {
		if ref := refererFor(mustParse(t.URL()), mustParse(abs)); ref != "" {
			hdrs = map[string]string{"Referer": ref}
		}
	}
	if dest.isFrame() {
		go func() {
			if dest.closed.Load() {
				return
			}
			if s.post != nil {
				_ = s.post(dest)
			} else {
				_ = dest.Navigate(abs, hdrs)
			}
			dest.bc.parent.contentChanged()
		}()
		return
	}
	dest.queueTask(func() {
		if s.post != nil {
			_ = s.post(dest)
			return
		}
		if dest.progMode.Load() {
			_ = dest.NavigateProgressive(abs, hdrs)
		} else {
			_ = dest.Navigate(abs, hdrs)
		}
	})
}

func mustParse(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		return nil
	}
	return u
}

// findWindowByName finds an open top-level window of the same browser context and opener group with the given name.
func (e *Engine) findWindowByName(from *Tab, name string) *Tab {
	if name == "" {
		return nil
	}
	root := from.groupRoot()
	for _, w := range e.ListTabs() {
		if w.closed.Load() || w.win.winClosed.Load() || w.win.severed.Load() || w.Context() != from.Context() {
			continue
		}
		if w.WindowName() == name && (w == from.Top() || w.groupRoot() == root) {
			return w
		}
	}
	return nil
}

// openNewWindow creates a popup (subject to the popup blocker) and returns it, or nil when blocked or opened
// without an opener reference.
func (t *Tab) openNewWindow(abs, name string, s navSpec) *Tab {
	if f := t.sandboxFlags(); f&sbSandboxed != 0 && f&sbPopups == 0 {
		t.recordPopup(PopupEvent{URL: abs, Name: name, Blocked: true, Reason: "sandbox: allow-popups is not set"})
		return nil
	}
	if !t.HasTransientActivation() {
		t.recordPopup(PopupEvent{URL: abs, Name: name, Blocked: true, Reason: "no user activation"})
		return nil
	}
	t.consumeActivation()
	keep := !s.noOpener
	top := t.Top()
	p := t.eng.newPopupTab(top, name, keep)
	if p == nil {
		return nil
	}
	t.recordPopup(PopupEvent{URL: abs, Name: name, Popup: p.ID})
	req := PopupRequest{Opener: t, Popup: p, URL: abs, Name: name, Features: s.features, NoOpener: !keep, Post: s.post}
	if !s.noReferrer {
		req.Referer = refererFor(mustParse(t.URL()), mustParse(abs))
	}
	t.eng.wm.mu.RLock()
	h := t.eng.wm.popup
	t.eng.wm.mu.RUnlock()
	if h != nil {
		h(req)
	} else {
		hdrs := map[string]string(nil)
		if req.Referer != "" {
			hdrs = map[string]string{"Referer": req.Referer}
		}
		go func() {
			if p.closed.Load() {
				return
			}
			switch {
			case req.Post != nil:
				_ = req.Post(p)
			case abs != "about:blank":
				_ = p.Navigate(abs, hdrs)
			}
		}()
	}
	if !keep {
		return nil
	}
	return p
}

// newPopupTab creates the tab for a script-opened window, linked to its opener.
func (e *Engine) newPopupTab(opener *Tab, name string, keepOpener bool) *Tab {
	if e.closed.Load() {
		return nil
	}
	e.mu.Lock()
	t := newTab(e, e.newTabID())
	t.ctx = opener.ctx
	e.tabs[t.ID] = t
	e.order = append(e.order, t.ID)
	e.mu.Unlock()
	opener.copyEnvironmentTo(t)
	t.win.scriptOpened = true
	t.win.name = name
	t.win.actWindow.Store(opener.win.actWindow.Load())
	if keepOpener {
		t.win.opener.Store(opener)
		opener.win.coopMu.Lock()
		opener.win.openees = append(opener.win.openees, t)
		opener.win.coopMu.Unlock()
	}
	t.SetDocument("<html><head><title></title></head><body></body></html>", "about:blank")
	return t
}

// copyEnvironmentTo gives a new tab the settings of its creator: emulation, init scripts and request interception
// (so a tracker blocker also covers popups).
func (t *Tab) copyEnvironmentTo(c *Tab) {
	t.mu.RLock()
	c.vpW, c.vpH, c.dsf = t.vpW, t.vpH, t.dsf
	c.mobile, c.landscape = t.mobile, t.landscape
	c.userAgent = t.userAgent
	c.colorScheme = t.colorScheme
	c.offline = t.offline
	c.geo = t.geo
	if len(t.permissions) > 0 {
		c.permissions = map[string]bool{}
		for k, v := range t.permissions {
			c.permissions[k] = v
		}
	}
	if len(t.extraHdrs) > 0 {
		c.extraHdrs = map[string]string{}
		for k, v := range t.extraHdrs {
			c.extraHdrs[k] = v
		}
	}
	c.initScripts = append([]*CompiledScript(nil), t.initScripts...)
	c.bindings = append([]string(nil), t.bindings...)
	t.mu.RUnlock()
	t.interceptMu.RLock()
	c.routes = append(c.routes[:0:0], t.routes...)
	t.interceptMu.RUnlock()
	t.routes2.mu.Lock()
	rules := append([]*RouteRule(nil), t.routes2.rules...)
	t.routes2.mu.Unlock()
	c.routes2.mu.Lock()
	c.routes2.rules = rules
	c.routes2.mu.Unlock()
}

// closeFromScript is window.close() for a script of this tab: allowed for windows that script opened (window.open,
// or a link target that created the window), ignored with a console warning otherwise (a page does not get to close
// the person's own tab), and never for a frame.
func (t *Tab) closeFromScript() bool {
	if t.isFrame() {
		return false
	}
	if !t.win.scriptOpened {
		t.LogConsole("warning", "Scripts may close only the windows that were opened by them.")
		return false
	}
	if !t.win.winClosed.CompareAndSwap(false, true) {
		return true
	}
	t.eng.wm.mu.RLock()
	h := t.eng.wm.closeWin
	t.eng.wm.mu.RUnlock()
	if h != nil {
		h(t)
		return true
	}
	// the call may come from inside this tab's own script: let it return first
	go func() { _ = t.eng.CloseTab(t.ID) }()
	return true
}

func (t *Tab) focusFromScript() {
	if t.isFrame() {
		return
	}
	t.eng.wm.mu.RLock()
	h := t.eng.wm.focusWin
	t.eng.wm.mu.RUnlock()
	if h != nil {
		h(t)
	}
}

// ---- Cross-Origin-Opener-Policy -----------------------------------------------------

func coopPolicy(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if i := strings.IndexByte(v, ';'); i >= 0 {
		v = strings.TrimSpace(v[:i])
	}
	switch v {
	case "same-origin", "same-origin-plus-coep":
		return "same-origin"
	case "same-origin-allow-popups":
		return "same-origin-allow-popups"
	}
	return "unsafe-none"
}

func sameOriginStr(a, b string) bool {
	return a != "" && a != "null" && canonOrigin(a) == canonOrigin(b)
}

// coopMatch reports whether two documents may stay in one browsing context group (and so keep window.opener
// references to each other). opener is the document that opened the other. It follows the HTML Standard's rules for
// the three policy values; the "same-origin-allow-popups opener, unsafe-none popup" pair is the one that lets an
// OAuth popup keep working.
func coopMatch(openerPol, openerOrigin, popPol, popOrigin string) bool {
	switch {
	case openerPol == "unsafe-none" && popPol == "unsafe-none":
		return true
	case openerPol == "same-origin-allow-popups" && popPol == "unsafe-none":
		return true
	case openerPol == "same-origin" && popPol == "same-origin", openerPol == "same-origin-allow-popups" && popPol == "same-origin-allow-popups":
		return sameOriginStr(openerOrigin, popOrigin)
	}
	return false
}

func coopHeader(h map[string][]string) string {
	for k, v := range h {
		if strings.EqualFold(k, "Cross-Origin-Opener-Policy") && len(v) > 0 {
			return v[0]
		}
	}
	return ""
}

// commitCOOP records the policy of the document that is about to replace the current one and severs opener links
// that the policy forbids, in both directions.
func (t *Tab) commitCOOP(header string, finalURL string) {
	if t.isFrame() || t.bc != nil {
		return // COOP applies to top-level documents only
	}
	pol := coopPolicy(header)
	org := "null"
	if u, err := url.Parse(finalURL); err == nil && u.Host != "" {
		org = u.Scheme + "://" + u.Host
	}
	if op := t.win.opener.Load(); op != nil && !t.win.severed.Load() {
		op.win.coopMu.Lock()
		opPol, opOrg := op.win.coop, op.win.coopOrigin
		op.win.coopMu.Unlock()
		if opPol == "" {
			opPol = "unsafe-none"
		}
		if !coopMatch(opPol, opOrg, pol, org) {
			t.severOpener()
		}
	}
	t.win.coopMu.Lock()
	t.win.coop, t.win.coopOrigin = pol, org
	openees := append([]*Tab(nil), t.win.openees...)
	t.win.coopMu.Unlock()
	for _, p := range openees {
		if p.win.opener.Load() != t || p.win.severed.Load() || p.closed.Load() {
			continue
		}
		p.win.coopMu.Lock()
		pp, po := p.win.coop, p.win.coopOrigin
		p.win.coopMu.Unlock()
		if pp == "" {
			pp = "unsafe-none"
		}
		if !coopMatch(pol, org, pp, po) {
			p.severOpener()
		}
	}
}

func (t *Tab) severOpener() {
	if t.win.severed.Swap(true) {
		return
	}
	t.win.opener.Store(nil)
	t.mu.Lock()
	t.win.name = ""
	t.mu.Unlock()
	t.LogConsole("info", "The opener link was severed by Cross-Origin-Opener-Policy")
}

// origin is scheme://host of the tab's URL (the URL itself for a document without a host).
func (t *Tab) origin() string {
	u, err := url.Parse(t.URL())
	if err != nil || u.Host == "" {
		return t.URL()
	}
	return u.Scheme + "://" + u.Host
}
