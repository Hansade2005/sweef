package engine

// frames.go: nested browsing contexts (<iframe>).
//
// Every <iframe> of a connected document gets a child Tab of its own: a separate document, origin, script VM and
// window, hidden from the engine's tab list. The child is a complete Tab (it fetches through the same cookie jar,
// service worker control and HTTP cache as any navigation, with the Sec-Fetch-* headers of a frame), it runs on
// its own goroutine like a worker, and its storage is keyed by (origin, top-level site) when it is embedded under
// another site (partition.go).
//
// What a frame is not: it is not painted into the parent's page (the embedding box stays empty, as before), and
// pointer and keyboard input does not reach it. Its DOM can be read (Tab.FrameTabs, Snapshot, Text), it runs
// scripts, and pages talk to it through window.postMessage and, for same-origin frames, through contentWindow /
// contentDocument (frame_prelude.js: calls and property access are forwarded to the frame's VM, objects stay
// there and are referenced through proxies; they are not copies).
//
// Implemented: src, srcdoc, about:blank (created synchronously, so a script can write into it right after
// appending the element), data: URLs (opaque origin), sandbox (scripts, same-origin, forms, top-navigation,
// modals, popups), X-Frame-Options and CSP frame-ancestors, window.parent / top / frames / frameElement /
// length, postMessage with origin checks and transferred ports, load events, the allow attribute is kept.

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/html"

	fnet "github.com/sweefbrowser/sweef/internal/net"
)

const (
	maxFrameDepth   = 6  // nesting limit
	maxFramesPerDoc = 24 // frames one document may have alive
	maxFramesPerTop = 64 // frames in one page tree
)

// frameSem bounds the frame documents being fetched and run at once.
var frameSem = make(chan struct{}, 4)

// sandbox flags
const (
	sbSandboxed uint32 = 1 << iota
	sbScripts
	sbSameOrigin
	sbForms
	sbPopups
	sbTopNav
	sbTopNavUser
	sbModals
	sbDownloads
	sbStorageAccess
	sbPopupsEscape
)

func parseSandbox(v string) uint32 {
	f := sbSandboxed
	for _, tok := range strings.Fields(strings.ToLower(v)) {
		switch tok {
		case "allow-scripts":
			f |= sbScripts
		case "allow-same-origin":
			f |= sbSameOrigin
		case "allow-forms":
			f |= sbForms
		case "allow-popups":
			f |= sbPopups
		case "allow-top-navigation":
			f |= sbTopNav
		case "allow-top-navigation-by-user-activation":
			f |= sbTopNavUser
		case "allow-modals":
			f |= sbModals
		case "allow-downloads":
			f |= sbDownloads
		case "allow-storage-access-by-user-activation":
			f |= sbStorageAccess
		case "allow-popups-to-escape-sandbox":
			f |= sbPopupsEscape
		}
	}
	return f
}

type frameSet struct {
	mu      sync.Mutex
	kids    map[int64]*Tab // by <iframe> backend node id
	pending atomic.Int32   // child documents still loading
	total   atomic.Int32   // (top-level tab) frames created in this page tree
	rt      *bgRuntime     // (a frame) its pump
	blocked atomic.Bool    // (a frame) the document was refused by X-Frame-Options / frame-ancestors
	opaque  atomic.Bool    // (a frame) the current document has an opaque origin (data: URL, blocked frame)
}

var winReg sync.Map // window id -> *Tab

func (t *Tab) isFrame() bool { return t.bc != nil && t.bc.isFrame }

// topTab is the top-level tab of the page tree t belongs to (t itself for a top-level tab, nil for a worker).
func (t *Tab) topTab() *Tab {
	if t.bc == nil {
		return t
	}
	if t.bc.isFrame {
		return t.bc.top
	}
	return nil
}

func (t *Tab) framesPending() int {
	n := int(t.fr.pending.Load())
	if n == 0 {
		return 0
	}
	return n
}

// FrameInfo describes one nested browsing context.
type FrameInfo struct {
	Name    string
	URL     string
	Origin  string // "" = opaque
	Blocked bool   // refused by X-Frame-Options or CSP frame-ancestors
	Sandbox bool
}

// FrameTabs returns the child frames of the tab's document in document order. Each is a Tab that can be
// evaluated in, read and snapshotted like any other; it is not in the engine's tab list.
func (t *Tab) FrameTabs() []*Tab {
	t.fr.mu.Lock()
	ids := make([]int64, 0, len(t.fr.kids))
	for id := range t.fr.kids {
		ids = append(ids, id)
	}
	kids := make(map[int64]*Tab, len(ids))
	for id, k := range t.fr.kids {
		kids[id] = k
	}
	t.fr.mu.Unlock()
	order := t.frameOrder()
	var out []*Tab
	for _, id := range order {
		if k := kids[id]; k != nil {
			out = append(out, k)
			delete(kids, id)
		}
	}
	return out
}

// Frames describes the child frames in document order.
func (t *Tab) Frames() []FrameInfo {
	var out []FrameInfo
	for _, k := range t.FrameTabs() {
		out = append(out, FrameInfo{Name: k.bc.name, URL: k.URL(), Origin: k.docOrigin(), Blocked: k.fr.blocked.Load(), Sandbox: k.bc.sandbox != 0})
	}
	return out
}

// WaitFrames waits until the frames of this document (and theirs) have finished loading or the timeout passes.
func (t *Tab) WaitFrames(d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		if !t.framesBusy() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func (t *Tab) framesBusy() bool {
	if t.fr.pending.Load() > 0 {
		return true
	}
	t.fr.mu.Lock()
	kids := make([]*Tab, 0, len(t.fr.kids))
	for _, k := range t.fr.kids {
		kids = append(kids, k)
	}
	t.fr.mu.Unlock()
	for _, k := range kids {
		if k.framesBusy() {
			return true
		}
	}
	return false
}

// frameOrder lists the iframe element ids of the document in tree order.
func (t *Tab) frameOrder() []int64 {
	var ids []int64
	_ = t.withDoc(func(d *fnet.Doc) error {
		ids = d.ElementsBy(0, "tag", "iframe")
		return nil
	})
	return ids
}

// ---- creation -------------------------------------------------------------------------------------

type frameAttrs struct {
	src, srcdoc, name, allow string
	hasSrcdoc                bool
	sandbox                  uint32
	lite                     bool
}

func (t *Tab) readFrameAttrs(d *fnet.Doc, id int64) (frameAttrs, bool) {
	n := d.NodeByID(id)
	if n == nil || n.Type != html.ElementNode || n.Data != "iframe" || !d.IsConnected(id) {
		return frameAttrs{}, false
	}
	var a frameAttrs
	get := func(k string) (string, bool) { return d.GetAttr(id, k) }
	a.src, _ = get("src")
	a.src = strings.TrimSpace(a.src)
	a.srcdoc, a.hasSrcdoc = get("srcdoc")
	a.name, _ = get("name")
	a.allow, _ = get("allow")
	if sb, ok := get("sandbox"); ok {
		a.sandbox = parseSandbox(sb)
	}
	if v, _ := get(liteMark); v != "" {
		a.lite = true
	}
	return a, true
}

// ensureFrame returns the child tab of the iframe element, creating it (and starting its load) when the element
// is connected and has none. It returns nil when the element is not a connected iframe, the Data Saver withheld
// the frame, or a limit was reached.
func (t *Tab) ensureFrame(elem int64) *Tab {
	if t.closed.Load() || t.eng.opts.DisableFrames {
		return nil
	}
	t.fr.mu.Lock()
	if k := t.fr.kids[elem]; k != nil {
		t.fr.mu.Unlock()
		var live bool
		_ = t.withDoc(func(d *fnet.Doc) error { live = d.IsConnected(elem); return nil })
		if !live { // the element was removed: its frame is going away
			go t.pruneFrames()
			return nil
		}
		return k
	}
	t.fr.mu.Unlock()
	var attrs frameAttrs
	var ok bool
	_ = t.withDoc(func(d *fnet.Doc) error {
		attrs, ok = t.readFrameAttrs(d, elem)
		return nil
	})
	if !ok || attrs.lite {
		return nil
	}
	top := t.topTab()
	if top == nil || t.depth() >= maxFrameDepth || int(top.fr.total.Load()) >= maxFramesPerTop {
		return nil
	}
	t.fr.mu.Lock()
	if k := t.fr.kids[elem]; k != nil {
		t.fr.mu.Unlock()
		return k
	}
	if len(t.fr.kids) >= maxFramesPerDoc {
		t.fr.mu.Unlock()
		return nil
	}
	c := t.newFrameTab(elem, attrs, top)
	if t.fr.kids == nil {
		t.fr.kids = map[int64]*Tab{}
	}
	t.fr.kids[elem] = c
	top.fr.total.Add(1)
	t.fr.mu.Unlock()
	c.beginFrameLoad(attrs)
	return c
}

func (t *Tab) depth() int {
	n := 0
	for p := t; p != nil && p.isFrame(); p = p.bc.parent {
		n++
	}
	return n
}

func (t *Tab) newFrameTab(elem int64, a frameAttrs, top *Tab) *Tab {
	c := newTab(t.eng, fmt.Sprintf("%s.f%d", t.ID, elem))
	c.ctx = t.Context()
	c.consoleRing, c.netRing = t.consoleRing, t.netRing
	// emulation settings follow the page the frame is in
	t.mu.RLock()
	c.userAgent, c.offline, c.colorScheme, c.extraHdrs = t.userAgent, t.offline, t.colorScheme, t.extraHdrs
	c.geo, c.permissions, c.mobile, c.dsf = t.geo, t.permissions, t.mobile, t.dsf
	c.vpW, c.vpH = t.vpW, t.vpH
	t.mu.RUnlock()
	c.bc = &browsingCtx{
		parent: t, top: top, isFrame: true, topSite: top.topLevelSite(),
		inherit: t.docOrigin(), opaque: a.sandbox != 0 && a.sandbox&sbSameOrigin == 0,
		elem: elem, name: a.name, sandbox: a.sandbox, allow: a.allow,
	}
	if t.bc != nil && t.bc.opaque {
		c.bc.opaque = true
	}
	winReg.Store(c.winID, c)
	return c
}

// pickFrameSize applies the iframe's width and height attributes (pixels; the default frame is 300x150) to the
// child's viewport.
func (t *Tab) applyFrameSize(d *fnet.Doc, id int64) {
	px := func(k string, def int) int {
		v, ok := d.GetAttr(id, k)
		if !ok {
			return def
		}
		v = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(v), "px"))
		n := 0
		for _, ch := range v {
			if ch < '0' || ch > '9' {
				return def
			}
			n = n*10 + int(ch-'0')
		}
		if n <= 0 {
			return def
		}
		return n
	}
	t.mu.Lock()
	t.vpW, t.vpH = px("width", 300), px("height", 150)
	t.mu.Unlock()
}

func (c *Tab) beginFrameLoad(a frameAttrs) {
	p := c.bc.parent
	_ = p.withDoc(func(d *fnet.Doc) error { c.applyFrameSize(d, c.bc.elem); return nil })
	blank := !a.hasSrcdoc && (a.src == "" || strings.EqualFold(a.src, "about:blank"))
	if blank {
		// created synchronously: the parent's script can use contentWindow / contentDocument at once
		c.setFrameDocument("<!doctype html><html><head></head><body></body></html>", "about:blank")
		c.startFramePump()
		p.frameEvent(c.bc.elem, "load")
		return
	}
	// until the real document is in, the frame has the initial about:blank document of its creator's origin
	c.setFrameDocument("<!doctype html><html><head></head><body></body></html>", "about:blank")
	p.fr.pending.Add(1)
	go func() {
		defer p.fr.pending.Add(-1)
		frameSem <- struct{}{}
		func() {
			defer func() { <-frameSem }()
			if c.closed.Load() {
				return
			}
			switch {
			case a.hasSrcdoc:
				c.setFrameDocument(a.srcdoc, "about:srcdoc")
			default:
				c.loadFrameURL(a.src)
			}
		}()
		if c.closed.Load() {
			return
		}
		c.startFramePump()
		p.frameEvent(c.bc.elem, "load")
	}()
}

func (c *Tab) setFrameDocument(src, finalURL string) {
	if c.closed.Load() {
		return
	}
	c.setDocument(src, finalURL, false)
}

// loadFrameURL navigates the frame to src (resolved against the parent's document).
func (c *Tab) loadFrameURL(src string) {
	p := c.bc.parent
	base := p.URL()
	if d := p.docBase(); d != "" {
		base = d
	}
	abs, err := resolveURL(base, src)
	lower := strings.ToLower(strings.TrimSpace(abs))
	switch {
	case err != nil, strings.HasPrefix(lower, "javascript:"), strings.HasPrefix(lower, "blob:"):
		c.setFrameDocument("<!doctype html><html><head></head><body></body></html>", "about:blank")
		return
	case strings.HasPrefix(lower, "data:"):
		// a data: document has an opaque origin
		c.fr.opaque.Store(true)
	case !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") && !strings.HasPrefix(lower, "about:"):
		c.setFrameDocument("<!doctype html><html><head></head><body></body></html>", "about:blank")
		return
	}
	if err := c.navigate(abs, nil, false); err != nil {
		c.LogConsole("error", fmt.Sprintf("frame %s failed to load: %v", abs, err))
		c.setFrameDocument("<!doctype html><html><head></head><body></body></html>", "about:blank")
		c.fr.opaque.Store(true)
	}
}

func (t *Tab) docBase() string {
	t.mu.RLock()
	d := t.dom
	t.mu.RUnlock()
	if d == nil {
		return ""
	}
	return d.BaseURL()
}

// startFramePump gives the frame's VM its own goroutine (once the first document is in).
func (c *Tab) startFramePump() {
	c.fr.mu.Lock()
	if c.fr.rt != nil || c.closed.Load() {
		c.fr.mu.Unlock()
		return
	}
	rt := &bgRuntime{tab: c, wakeCh: make(chan struct{}, 1), quit: make(chan struct{}), done: make(chan struct{})}
	rt.cur = func() *jsVM {
		c.mu.RLock()
		defer c.mu.RUnlock()
		return c.js
	}
	c.fr.rt = rt
	c.fr.mu.Unlock()
	wake := func() { rt.nudge() }
	c.onWake.Store(&wake)
	rt.start()
}

// ---- teardown ---------------------------------------------------------------------------------------

// closeFrames closes every child frame (the document is going away).
func (t *Tab) closeFrames() {
	t.fr.mu.Lock()
	kids := t.fr.kids
	t.fr.kids = nil
	t.fr.mu.Unlock()
	if len(kids) == 0 {
		return
	}
	if top := t.topTab(); top != nil {
		top.fr.total.Add(-int32(len(kids)))
	}
	for _, k := range kids {
		k.closeFrameTab()
	}
}

func (c *Tab) closeFrameTab() {
	c.fr.mu.Lock()
	rt := c.fr.rt
	c.fr.rt = nil
	c.fr.mu.Unlock()
	if rt != nil {
		rt.stopped.Store(true)
		close(rt.quit)
		rt.nudge()
		if rt.started.Load() {
			<-rt.done
		}
	}
	c.close()
	winReg.Delete(c.winID)
}

// pruneFrames closes the children whose element left the document.
func (t *Tab) pruneFrames() {
	t.fr.mu.Lock()
	if len(t.fr.kids) == 0 {
		t.fr.mu.Unlock()
		return
	}
	ids := make([]int64, 0, len(t.fr.kids))
	for id := range t.fr.kids {
		ids = append(ids, id)
	}
	t.fr.mu.Unlock()
	var gone []int64
	_ = t.withDoc(func(d *fnet.Doc) error {
		for _, id := range ids {
			if !d.IsConnected(id) {
				gone = append(gone, id)
			}
		}
		return nil
	})
	for _, id := range gone {
		t.fr.mu.Lock()
		k := t.fr.kids[id]
		delete(t.fr.kids, id)
		t.fr.mu.Unlock()
		if k != nil {
			if top := t.topTab(); top != nil {
				top.fr.total.Add(-1)
			}
			go k.closeFrameTab()
		}
	}
}

// scanFrames creates the frames of every connected iframe element (called when a document has been parsed and
// after DOM changes that may have inserted some).
func (t *Tab) scanFrames() {
	if t.eng.opts.DisableFrames || t.closed.Load() {
		return
	}
	for _, id := range t.frameOrder() {
		t.ensureFrame(id)
	}
}

// frameInserted is called when a subtree was inserted: iframe elements in it get their frame.
func (t *Tab) frameInserted(d *fnet.Doc, id int64) {
	if t.eng.opts.DisableFrames {
		return
	}
	var ids []int64
	if d.TagName(id) == "iframe" {
		ids = append(ids, id)
	} else {
		ids = append(ids, d.ElementsBy(id, "tag", "iframe")...)
	}
	for _, f := range ids {
		if d.IsConnected(f) {
			// the load is started outside the DOM call that is inserting the element
			t.ensureFrameDeferred(f)
		}
	}
}

// ensureFrameDeferred creates the frame for an element that was just inserted. A blank frame is created at once
// (the caller may be about to write into it); one with a source is started in the background.
func (t *Tab) ensureFrameDeferred(elem int64) {
	var ok bool
	_ = t.withDoc(func(d *fnet.Doc) error { _, ok = t.readFrameAttrs(d, elem); return nil })
	if !ok {
		return
	}
	go t.ensureFrame(elem)
}

// frameMutation reacts to attribute changes of iframe elements: a new src or srcdoc navigates the frame.
func (t *Tab) frameMutation(m fnet.Mutation) {
	if m.Op != "set-attr" && m.Op != "remove-attr" {
		return
	}
	key := strings.ToLower(m.Key)
	if key != "src" && key != "srcdoc" {
		return
	}
	t.fr.mu.Lock()
	k := t.fr.kids[m.BackendID]
	t.fr.mu.Unlock()
	if k == nil {
		return
	}
	go func() {
		var a frameAttrs
		var ok bool
		_ = t.withDoc(func(d *fnet.Doc) error { a, ok = t.readFrameAttrs(d, m.BackendID); return nil })
		if !ok || k.closed.Load() {
			return
		}
		t.fr.pending.Add(1)
		defer t.fr.pending.Add(-1)
		frameSem <- struct{}{}
		func() {
			defer func() { <-frameSem }()
			if a.hasSrcdoc {
				k.setFrameDocument(a.srcdoc, "about:srcdoc")
			} else if a.src == "" || strings.EqualFold(a.src, "about:blank") {
				k.setFrameDocument("<!doctype html><html><head></head><body></body></html>", "about:blank")
			} else {
				k.fr.blocked.Store(false)
				k.loadFrameURL(a.src)
			}
		}()
		t.frameEvent(m.BackendID, "load")
	}()
}

// ---- events to the embedding document -----------------------------------------------------------------

func (t *Tab) frameEvent(elem int64, typ string) {
	t.mu.RLock()
	j := t.js
	t.mu.RUnlock()
	if j == nil {
		return
	}
	pushFrame(j, map[string]any{"t": typ, "elem": elem})
}

// ---- response checks ------------------------------------------------------------------------------------

// ancestorOrigins lists the origins of the frame's ancestors, nearest first.
func (c *Tab) ancestorOrigins() []string {
	var out []string
	for p := c.bc.parent; p != nil; {
		o := p.docOrigin()
		if o == "" {
			o = "null"
		}
		out = append(out, o)
		if p.isFrame() {
			p = p.bc.parent
		} else {
			break
		}
	}
	return out
}

// frameAncestorsBlocked applies X-Frame-Options and the frame-ancestors directive of Content-Security-Policy
// to a frame document. docURL is the URL of the response.
func (c *Tab) frameAncestorsBlocked(h http.Header, docURL string) (bool, string) {
	anc := c.ancestorOrigins()
	self := siteOriginOf(docURL)
	// Content-Security-Policy: frame-ancestors wins over X-Frame-Options when present
	csp := false
	for _, v := range h.Values("Content-Security-Policy") {
		for _, dir := range strings.Split(v, ",") { // several policies may be folded into one header
			for _, part := range strings.Split(dir, ";") {
				f := strings.Fields(strings.TrimSpace(part))
				if len(f) == 0 || !strings.EqualFold(f[0], "frame-ancestors") {
					continue
				}
				csp = true
				if !frameAncestorsAllow(f[1:], anc, self) {
					return true, "Content-Security-Policy: frame-ancestors " + strings.Join(f[1:], " ")
				}
			}
		}
	}
	if csp {
		return false, ""
	}
	if xfo := strings.ToLower(strings.TrimSpace(strings.Split(h.Get("X-Frame-Options"), ",")[0])); xfo != "" {
		switch xfo {
		case "deny":
			return true, "X-Frame-Options: deny"
		case "sameorigin":
			for _, o := range anc {
				if o != self {
					return true, "X-Frame-Options: sameorigin"
				}
			}
		}
	}
	return false, ""
}

func frameAncestorsAllow(sources []string, ancestors []string, self string) bool {
	if len(sources) == 0 {
		return true
	}
	for _, s := range sources {
		if strings.EqualFold(s, "'none'") {
			return false
		}
	}
	// every ancestor must match some source
	for _, a := range ancestors {
		ok := false
		for _, s := range sources {
			if cspSourceMatches(s, a, self) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

func cspSourceMatches(src, origin, self string) bool {
	src = strings.TrimSpace(src)
	switch {
	case src == "*":
		return strings.HasPrefix(origin, "http://") || strings.HasPrefix(origin, "https://")
	case strings.EqualFold(src, "'self'"):
		return origin == self
	case strings.HasSuffix(src, ":") && !strings.Contains(src, "/"):
		return strings.HasPrefix(origin, strings.ToLower(src)) // a scheme source: https:
	}
	ou, err := url.Parse(origin)
	if err != nil || ou.Host == "" {
		return false
	}
	spec := src
	scheme := ""
	if i := strings.Index(spec, "://"); i >= 0 {
		scheme, spec = strings.ToLower(spec[:i]), spec[i+3:]
	}
	spec = strings.TrimSuffix(spec, "/")
	if i := strings.Index(spec, "/"); i >= 0 {
		spec = spec[:i]
	}
	host, port := spec, ""
	if i := strings.LastIndex(spec, ":"); i >= 0 && !strings.Contains(spec[i:], "]") {
		host, port = spec[:i], spec[i+1:]
	}
	if scheme != "" && scheme != ou.Scheme {
		return false
	}
	if port != "" && port != "*" && port != ou.Port() {
		return false
	}
	if scheme == "" && ou.Scheme != "http" && ou.Scheme != "https" {
		return false
	}
	h := strings.ToLower(ou.Hostname())
	host = strings.ToLower(host)
	if strings.HasPrefix(host, "*.") {
		return strings.HasSuffix(h, host[1:])
	}
	return h == host
}
