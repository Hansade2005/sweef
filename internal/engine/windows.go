package engine

// windows.go: browsing contexts that can reference each other. Every Tab is a
// browsing context: a top-level tab, a popup opened by script (window.open or a
// link with a target), or a frame document embedded by an <iframe> (frames.go).
// This file holds what they have in common:
//
//   - the registry that turns a window id (the Tab id) into a Tab, so a
//     WindowProxy in one JavaScript VM can address another VM safely;
//   - transient user activation and the popup blocker;
//   - window.open, link/form targets, window.close, window.name, opener;
//   - cross-window postMessage (queued, delivered by the receiver's event loop);
//   - Cross-Origin-Opener-Policy handling (severing the opener link).
//
// What is shared between windows is only what the platform shares: messages and
// navigation requests. A VM never touches another VM; requests are queued on the
// target Tab and run by the goroutine that owns it.

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/html"

	fnet "github.com/sweefbrowser/sweef/internal/net"
)

// DefaultActivationWindow is how long a click or key press keeps allowing popups.
const DefaultActivationWindow = 5 * time.Second

// winState is the per-Tab part of the window model.
type winState struct {
	parent *Tab   // embedding document for a frame, nil for a top-level context
	frameN int    // index of the frame among its parent's frames (ref prefix f<N>)
	fr     *frame // the parent's record of this frame

	opener       atomic.Pointer[Tab]
	severed      atomic.Bool // COOP cut the opener link: the former opener sees this window as closed
	scriptOpened bool        // created by window.open / a target link (window.close() is then allowed)
	winClosed    atomic.Bool // script asked to close it; the tab is going away
	name         string      // window.name (guarded by Tab.mu)
	sbx          *sandbox    // nil = not sandboxed

	coopMu     sync.Mutex
	coop       string // COOP value of the current document: unsafe-none | same-origin-allow-popups | same-origin
	coopOrigin string
	openees    []*Tab // windows this one opened (for COOP severing)

	activation atomic.Int64 // unix nanos of the last user activation
	actWindow  atomic.Int64 // activation lifetime in nanos (0 = DefaultActivationWindow)

	inMu  sync.Mutex
	inbox []winMsg

	taskMu sync.Mutex
	tasks  []func()

	known sync.Map // window ids this document has been handed (it may address only those)

	popups *Ring // PopupEvent log (allowed and blocked)

	// frames this document embeds (frames.go)
	fmu          sync.Mutex
	frames       map[*html.Node]*frame
	frameSeq     int
	frameDoc     *fnet.Doc // the document the frames belong to
	scanGen      uint64
	scanPending  atomic.Bool
	mayFrames    atomic.Bool // the document has, or script may have inserted, an <iframe> (otherwise scans are rare)
	lastScan     time.Time   // guarded by fmu
	frameGen     atomic.Uint64
	framesPaused atomic.Bool
	focusFrame   atomic.Pointer[frame] // frame holding keyboard focus inside this document
	paintDSF     atomic.Uint64         // math.Float64bits of the scale of the last paint
}

type winMsg struct{ src, origin, data string }

// PopupEvent is one window.open / target=_blank decision, allowed or not.
type PopupEvent struct {
	Opener  string    `json:"opener"`
	Popup   string    `json:"popup,omitempty"`
	URL     string    `json:"url"`
	Name    string    `json:"name,omitempty"`
	Blocked bool      `json:"blocked"`
	Reason  string    `json:"reason,omitempty"` // no-user-activation | sandbox | ...
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

// winRegistry maps window ids to tabs and holds the embedder's window hooks.
type winRegistry struct {
	mu       sync.RWMutex
	m        map[string]*Tab
	popup    func(PopupRequest)
	closeWin func(*Tab)
	focusWin func(*Tab)
}

func (e *Engine) regWin(t *Tab) {
	e.wm.mu.Lock()
	if e.wm.m == nil {
		e.wm.m = map[string]*Tab{}
	}
	e.wm.m[t.ID] = t
	e.wm.mu.Unlock()
}

func (e *Engine) unregWin(t *Tab) {
	e.wm.mu.Lock()
	delete(e.wm.m, t.ID)
	e.wm.mu.Unlock()
}

func (e *Engine) lookupWin(id string) *Tab {
	e.wm.mu.RLock()
	defer e.wm.mu.RUnlock()
	return e.wm.m[id]
}

// SetPopupHandler registers the embedder's handler for windows created by page
// script. The Popup tab already exists (about:blank, opener linked); the handler
// must show it and start its navigation to req.URL (or call Popup.Navigate). Without
// a handler the engine navigates the popup itself in the background (headless use).
func (e *Engine) SetPopupHandler(f func(PopupRequest)) {
	e.wm.mu.Lock()
	e.wm.popup = f
	e.wm.mu.Unlock()
}

// SetWindowCloseHandler registers what happens when script calls window.close() on a
// tab it may close: the embedder should close the tab. Without a handler the engine
// closes it.
func (e *Engine) SetWindowCloseHandler(f func(*Tab)) {
	e.wm.mu.Lock()
	e.wm.closeWin = f
	e.wm.mu.Unlock()
}

// SetFrameReadFilter sets a predicate that decides, per frame document URL, whether the
// snapshot may include the frame and refs may act inside it. The desktop app uses it so
// that an embedded page the person did not allow the agent to read stays unread even
// when the page that embeds it is allowed. nil allows every frame.
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

// navSchemeAllowed: a web page cannot make a frame or window show the browser's own pages
// or local files (a document may open its own kind: a sweef:// page another sweef:// page).
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

// SetWindowFocusHandler registers what happens when script calls focus() on a popup.
func (e *Engine) SetWindowFocusHandler(f func(*Tab)) {
	e.wm.mu.Lock()
	e.wm.focusWin = f
	e.wm.mu.Unlock()
}

// ---- relations ------------------------------------------------------------------

// Parent is the embedding tab for a frame document, nil otherwise.
func (t *Tab) Parent() *Tab { return t.win.parent }

// Opener is the window that opened this one with script (nil after noopener, a
// COOP severing, or when the opener is gone).
func (t *Tab) Opener() *Tab {
	if o := t.win.opener.Load(); o != nil && !o.closed.Load() && !t.win.severed.Load() {
		return o
	}
	return nil
}

// Top is the top-level browsing context of a frame tree (the tab itself when it is top-level).
func (t *Tab) Top() *Tab {
	c := t
	for c.win.parent != nil {
		c = c.win.parent
	}
	return c
}

// IsClosed reports whether the tab has been closed.
func (t *Tab) IsClosed() bool { return t.closed.Load() }

// WindowName is window.name.
func (t *Tab) WindowName() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.win.name
}

func (t *Tab) setWindowName(n string) {
	t.mu.Lock()
	t.win.name = n
	t.mu.Unlock()
}

// SetWindowName sets window.name (used by embedders that open tabs for a target).
func (t *Tab) SetWindowName(n string) { t.setWindowName(n) }

// WindowClosed reports whether the window is gone as far as other windows can tell.
func (t *Tab) WindowClosed() bool {
	return t.closed.Load() || t.win.winClosed.Load() || t.win.severed.Load()
}

// origin is the document's origin for messaging and storage ("null" when opaque).
func (t *Tab) origin() string {
	if t.opaqueOrigin() {
		return "null"
	}
	u, err := url.Parse(t.URL())
	if err == nil && u.Host != "" {
		return u.Scheme + "://" + u.Host
	}
	if err == nil && u.Scheme == "about" {
		// about:blank and about:srcdoc inherit the origin of whoever created them
		if p := t.win.parent; p != nil {
			return p.origin()
		}
		if o := t.win.opener.Load(); o != nil {
			return o.origin()
		}
	}
	return t.URL()
}

// storageKey keys the document's Web Storage: an opaque origin gets a private, throw-away area.
func (t *Tab) storageKey() string {
	if t.opaqueOrigin() {
		return "null#" + t.ID
	}
	return t.origin()
}

// originSerialization normalises an origin string for comparison.
func originSerialization(s string) string {
	if s == "" || s == "null" {
		return s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return s
	}
	host := strings.ToLower(u.Host)
	switch {
	case u.Scheme == "http" && strings.HasSuffix(host, ":80"):
		host = strings.TrimSuffix(host, ":80")
	case u.Scheme == "https" && strings.HasSuffix(host, ":443"):
		host = strings.TrimSuffix(host, ":443")
	}
	return strings.ToLower(u.Scheme) + "://" + host
}

func sameOriginStr(a, b string) bool {
	return a != "null" && originSerialization(a) == originSerialization(b)
}

// canReach: a document may address only windows it has been handed (its frames, its
// parent and top, its opener and popups, senders of messages it received) in its own
// browser context.
func (t *Tab) canReach(id string) bool {
	_, ok := t.win.known.Load(id)
	return ok
}

func (t *Tab) knows(o *Tab) {
	if o != nil {
		t.win.known.Store(o.ID, true)
	}
}

// ---- user activation and the popup blocker ---------------------------------------

// SetActivationWindow sets how long a user gesture keeps allowing popups (default 5 s).
func (t *Tab) SetActivationWindow(d time.Duration) { t.win.actWindow.Store(int64(d)) }

// NoteUserActivation records a trusted user gesture (click, key press, tap) in this
// document and in the documents that embed it. Embedders call it from real input; the
// engine calls it for agent actions that stand in for a person (Click, Press, Type).
func (t *Tab) NoteUserActivation() {
	now := time.Now().UnixNano()
	for c := t; c != nil; c = c.win.parent {
		c.win.activation.Store(now)
	}
}

// HasTransientActivation reports whether a user gesture happened recently enough to
// allow a popup.
func (t *Tab) HasTransientActivation() bool {
	at := t.win.activation.Load()
	if at == 0 {
		return false
	}
	w := time.Duration(t.win.actWindow.Load())
	if w <= 0 {
		w = DefaultActivationWindow
	}
	return time.Since(time.Unix(0, at)) <= w
}

func (t *Tab) consumeActivation() {
	for c := t; c != nil; c = c.win.parent {
		c.win.activation.Store(0)
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

// ---- tasks run by the tab's own event loop ----------------------------------------

// queueTask hands f to the goroutine that owns the tab: it runs at the start of the
// tab's next event-loop slice (Settle, a window pump). Used for navigations that
// another window asks for.
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

func (t *Tab) hasTasks() bool {
	t.win.taskMu.Lock()
	defer t.win.taskMu.Unlock()
	return len(t.win.tasks) > 0
}

// ---- postMessage -------------------------------------------------------------------

// postMessageTo queues a message from t to window id. Messages to a window the sender
// cannot address, a closed window, or with a targetOrigin that does not match are
// dropped without an error, exactly as in a browser.
func (t *Tab) postMessageTo(id, data, targetOrigin string) {
	dst := t.eng.lookupWin(id)
	if dst == nil || dst.closed.Load() {
		return
	}
	if id != t.ID && (!t.canReach(id) || dst.WindowClosed() || dst.Context() != t.Context()) {
		return
	}
	switch targetOrigin {
	case "*":
	case "/":
		if !sameOriginStr(t.origin(), dst.origin()) {
			return
		}
	default:
		if originSerialization(targetOrigin) != originSerialization(dst.origin()) || dst.origin() == "null" {
			return
		}
	}
	dst.knows(t)
	dst.win.inMu.Lock()
	if len(dst.win.inbox) < 4096 {
		dst.win.inbox = append(dst.win.inbox, winMsg{src: t.ID, origin: t.origin(), data: data})
	}
	dst.win.inMu.Unlock()
	dst.wake()
}

func (t *Tab) takeInbox() []winMsg {
	t.win.inMu.Lock()
	m := t.win.inbox
	t.win.inbox = nil
	t.win.inMu.Unlock()
	return m
}

func (t *Tab) inboxLen() int {
	t.win.inMu.Lock()
	defer t.win.inMu.Unlock()
	return len(t.win.inbox)
}

// drainInbox hands queued window messages to page JavaScript.
func (j *jsVM) drainInbox() bool {
	if j.tab == nil {
		return false
	}
	msgs := j.tab.takeInbox()
	for _, m := range msgs {
		if _, err := j.call("__swDeliverMessage", m.src, m.origin, m.data); err != nil {
			j.tab.LogConsole("error", jsErrorText(err, "message"))
		}
	}
	return len(msgs) > 0
}

// PostMessage delivers data (JSON) to the tab's window as if a script from the given
// origin had called postMessage (used by embedders and tests).
func (t *Tab) PostMessage(data any, origin string) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	t.win.inMu.Lock()
	t.win.inbox = append(t.win.inbox, winMsg{src: "", origin: origin, data: string(b)})
	t.win.inMu.Unlock()
	t.wake()
	return nil
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
	method     string
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

func isSelfTarget(k string) bool { return k == "_self" }

// openFromScript is window.open(url, target, features). It returns the window the
// call refers to (a new popup, an existing named window or frame), or nil when the
// call is blocked or the popup is opened with noopener.
func (t *Tab) openFromScript(rawURL, target, features string) *Tab {
	noop, noref := parseFeatures(features)
	return t.navigateTarget(navSpec{rawURL: rawURL, target: target, features: features, fromScript: true, noOpener: noop, noReferrer: noref})
}

// navigateTarget resolves a navigation target (_self, _top, _parent, _blank, a frame
// or window name) and starts the navigation there.
func (t *Tab) navigateTarget(s navSpec) *Tab {
	base := t.URL()
	abs := strings.TrimSpace(s.rawURL)
	if abs == "" || strings.EqualFold(abs, "about:blank") {
		abs = "about:blank"
	} else if strings.HasPrefix(strings.ToLower(abs), "javascript:") {
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
	nav := func(dest *Tab) {
		dest.queueNavigation(abs, s, t)
	}
	switch key {
	case "_self":
		if t.win.sbx != nil && s.post != nil && !t.win.sbx.forms {
			return nil
		}
		nav(t)
		return t
	case "_parent", "_top":
		dest := t.Top()
		if key == "_parent" && t.win.parent != nil {
			dest = t.win.parent
		}
		if dest != t && !t.mayNavigateAncestor() {
			t.recordPopup(PopupEvent{URL: abs, Blocked: true, Reason: "sandbox: top-level navigation not allowed"})
			return nil
		}
		nav(dest)
		return dest
	case "_blank":
		return t.openNewWindow(abs, "", s)
	}
	// a named target: a frame of this page, then an existing window, then a new one
	if f := t.Top().findFrameByName(s.target); f != nil {
		f.navigateTo(abs, s, t)
		return f.child
	}
	if w := t.eng.findWindowByName(t, s.target); w != nil {
		if !t.HasTransientActivation() && s.fromScript && w != t {
			t.recordPopup(PopupEvent{URL: abs, Name: s.target, Blocked: true, Reason: "no user activation"})
			return nil
		}
		nav(w)
		t.knows(w)
		return w
	}
	return t.openNewWindow(abs, s.target, s)
}

func (t *Tab) mayNavigateAncestor() bool {
	for c := t; c != nil; c = c.win.parent {
		if c.win.sbx != nil && !(c.win.sbx.topNav || (c.win.sbx.topNavUser && t.HasTransientActivation())) {
			return false
		}
	}
	return true
}

// queueNavigation runs the navigation on the destination's own goroutine.
func (dest *Tab) queueNavigation(abs string, s navSpec, from *Tab) {
	hdrs := map[string]string(nil)
	if !s.noReferrer && from != nil && from != dest {
		if ref := refererFor(mustParse(from.URL()), mustParse(abs)); ref != "" {
			hdrs = map[string]string{"Referer": ref}
		}
	}
	if dest.win.parent != nil && dest.win.fr != nil {
		dest.win.fr.navigateTo(abs, s, from)
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

// findWindowByName finds an open top-level window of the same browser context with the given name.
func (e *Engine) findWindowByName(from *Tab, name string) *Tab {
	if name == "" {
		return nil
	}
	e.wm.mu.RLock()
	defer e.wm.mu.RUnlock()
	for _, w := range e.wm.m {
		if w.win.parent != nil || w.closed.Load() || w.win.winClosed.Load() || w.win.severed.Load() || w.Context() != from.Context() {
			continue
		}
		if w.WindowName() == name {
			return w
		}
	}
	return nil
}

// openNewWindow creates a popup (subject to the popup blocker) and returns it, or nil
// when blocked or opened without an opener reference.
func (t *Tab) openNewWindow(abs, name string, s navSpec) *Tab {
	if t.win.sbx != nil && !t.win.sbx.popups {
		t.recordPopup(PopupEvent{URL: abs, Name: name, Blocked: true, Reason: "sandbox: allow-popups is not set"})
		return nil
	}
	if !t.HasTransientActivation() {
		t.recordPopup(PopupEvent{URL: abs, Name: name, Blocked: true, Reason: "no user activation"})
		return nil
	}
	t.consumeActivation()
	keep := !s.noOpener
	p := t.eng.newPopupTab(t, name, keep)
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
	if os := opener.win.sbx; os != nil && !os.popupsEscape {
		t.win.sbx = os
	}
	if keepOpener {
		t.win.opener.Store(opener)
		opener.win.coopMu.Lock()
		opener.win.openees = append(opener.win.openees, t)
		opener.win.coopMu.Unlock()
		opener.knows(t)
		t.knows(opener)
	}
	t.SetDocument("<html><head><title></title></head><body></body></html>", "about:blank")
	return t
}

// copyEnvironmentTo gives a new tab the settings of its creator: emulation, init
// scripts and request interception (so a tracker blocker also covers popups and frames).
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
	c.routes = append([]*fnet.Route(nil), t.routes...)
	t.interceptMu.RUnlock()
	t.routes2.mu.Lock()
	rules := append([]*RouteRule(nil), t.routes2.rules...)
	t.routes2.mu.Unlock()
	c.routes2.mu.Lock()
	c.routes2.rules = rules
	c.routes2.mu.Unlock()
}

// CloseWindow is window.close() for a script of this tab: allowed for windows that
// script opened (and a window with a single history entry), ignored with a console
// warning otherwise, and never for a frame.
func (t *Tab) closeFromScript() bool {
	if t.win.parent != nil {
		return false
	}
	t.mu.RLock()
	single := len(t.history) <= 1
	t.mu.RUnlock()
	if !t.win.scriptOpened && !single {
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
	if t.win.parent != nil {
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

func parseCOOP(v string) string {
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

// coopMatch reports whether two documents may stay in one browsing context group
// (and so keep window.opener references to each other). opener is the document that
// opened the other. It follows the HTML Standard's rules for the three policy values;
// the "same-origin-allow-popups opener, unsafe-none popup" pair is the one that lets
// an OAuth popup keep working.
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

// commitCOOP records the policy of the document that is about to replace the current
// one and severs opener links that the policy forbids, in both directions.
func (t *Tab) commitCOOP(header string, finalURL string) {
	pol := parseCOOP(header)
	var org string
	if u, err := url.Parse(finalURL); err == nil && u.Host != "" {
		org = u.Scheme + "://" + u.Host
	} else {
		org = "null"
	}
	if t.win.parent != nil {
		return // COOP applies to top-level documents only
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

// ---- header helper ------------------------------------------------------------------

func coopHeader(h http.Header) string { return h.Get("Cross-Origin-Opener-Policy") }
