package engine

// frames.go: <iframe> documents.
//
// Every <iframe> of a document becomes a frame: a child Tab with its own JavaScript
// VM, document, styles and layout, created by the parent as soon as the element is
// seen in the DOM (so a display:none frame still loads, as in a browser). The child
// is driven by a goroutine of its own (load, event loop, input-driven work), and
// whenever it has new content it invalidates the parent, which repaints.
//
// Painting: layout emits an ordinary render.Bitmap op at the iframe's content box
// (render.FrameEnv), so everything the display list already does to replaced
// content applies to frames for free: clipping to the box, border radius,
// transforms, opacity and filters of ancestors, visibility, z-order. At raster time
// Env.Bitmap(iframe) asks the frame for its pixels: the child painted at the box
// size and the parent's device scale, scrolled by the frame's own scroll position,
// on a transparent canvas (a frame document with no background shows what is behind it).
//
// Input is routed by hit testing through the parent's layout (see frames_input.go).

import (
	"image"
	"math"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/html"

	fnet "github.com/sweefbrowser/sweef/internal/net"
	"github.com/sweefbrowser/sweef/internal/render"
)

const (
	defaultFrameW = 300
	defaultFrameH = 150
	// lazyMargin: a loading="lazy" frame starts to load when it is within this many CSS px of the viewport.
	lazyMargin = 1250
	// frameWaitBudget bounds how long a headless navigation waits for the frames it started.
	frameWaitBudget = 4 * time.Second
	maxFrameDepth   = 6
	maxFramesPerDoc = 64
)

// frame is the parent's record of one <iframe>.
type frame struct {
	parent *Tab
	node   *html.Node
	nodeID int64
	idx    int
	child  *Tab
	lazy   bool

	mu      sync.Mutex
	key     string // what the frame was last asked to show (src / srcdoc), to notice attribute changes
	w, h    int
	started bool
	by, bh  float64       // the box of the iframe in the parent's document (set by layout)
	ready   chan struct{} // closed when the current navigation has loaded (or failed)
	nav     *frameNav
	lastGen uint64
	loadSeq int

	// cached pixels (guarded by mu)
	img      *image.RGBA
	imgGen   uint64
	imgW     int
	imgH     int
	imgDSF   float64
	imgSY    int
	imgValid bool

	startCh  chan struct{}
	stopCh   chan struct{}
	stopOnce sync.Once
	wakeCh   chan struct{}
	exited   chan struct{}
	stopped  atomic.Bool
}

type frameNav struct {
	url  string
	spec navSpec
	from *Tab
}

func (f *frame) poke() {
	select {
	case f.wakeCh <- struct{}{}:
	default:
	}
}

func (f *frame) stop() {
	f.stopOnce.Do(func() {
		f.stopped.Store(true)
		close(f.stopCh)
	})
}

// sleep waits for d, a poke, or stop; true means "keep running".
func (f *frame) sleep(d time.Duration) bool {
	if d <= 0 {
		d = time.Millisecond
	}
	tm := time.NewTimer(d)
	defer tm.Stop()
	select {
	case <-f.stopCh:
		return false
	case <-f.wakeCh:
	case <-tm.C:
	}
	return !f.stopped.Load()
}

// start lets the frame's goroutine begin loading (lazy frames wait for this).
func (f *frame) start() {
	f.mu.Lock()
	if f.started {
		f.mu.Unlock()
		return
	}
	f.started = true
	f.mu.Unlock()
	close(f.startCh)
}

func (f *frame) isStarted() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.started
}

// contentChanged is called when something painted by this document changed (a frame
// of it repainted, an image arrived): the window must paint again, and so must the
// document that embeds this one.
func (t *Tab) contentChanged() {
	t.win.frameGen.Add(1)
	if f := t.onUpdate.Load(); f != nil {
		(*f)()
	}
}

// frameOf returns the frame record of an iframe element of the current document.
func (t *Tab) frameOf(n *html.Node) *frame {
	t.win.fmu.Lock()
	defer t.win.fmu.Unlock()
	return t.win.frames[n]
}

// HasFrame implements render.FrameEnv: the iframe element has a live frame document.
func (e *tabEnv) HasFrame(n *html.Node) bool {
	f := e.t.frameOf(n)
	return f != nil && f.isStarted()
}

func (t *Tab) depth() int {
	d := 0
	for c := t; c.win.parent != nil; c = c.win.parent {
		d++
	}
	return d
}

// ---- discovery ------------------------------------------------------------------------

// SyncFrames makes the set of frame children match the <iframe> elements of the
// current document: new elements get a child tab that starts loading, removed ones
// are closed, a changed src/srcdoc navigates. It is cheap when the document has not
// changed since the last call.
func (t *Tab) SyncFrames() { t.syncFrames(false) }

func (t *Tab) syncFrames(force bool) {
	if t.closed.Load() {
		return
	}
	t.mu.RLock()
	d := t.dom
	t.mu.RUnlock()
	if d == nil {
		return
	}
	t.win.fmu.Lock()
	if t.win.frameDoc != d {
		old := t.win.frames
		t.win.frames, t.win.frameDoc, t.win.scanGen = nil, d, 0
		for _, f := range old {
			t.closeFrameLocked(f)
		}
	}
	gen := d.MutationGen() + 1
	if !force && gen == t.win.scanGen {
		t.win.fmu.Unlock()
		return
	}
	t.win.scanGen = gen
	var nodes []*html.Node
	if d.Root != nil {
		walkNodes(d.Root, func(n *html.Node) {
			if n.Type == html.ElementNode && n.Data == "iframe" {
				nodes = append(nodes, n)
			}
		})
	}
	seen := make(map[*html.Node]bool, len(nodes))
	created := false
	for _, n := range nodes {
		if len(seen) >= maxFramesPerDoc {
			break
		}
		seen[n] = true
		if attrVal(n, liteMark) != "" { // a Data Saver placeholder: nothing loads until the person asks
			if f := t.win.frames[n]; f != nil {
				delete(t.win.frames, n)
				t.closeFrameLocked(f)
			}
			continue
		}
		f := t.win.frames[n]
		key := t.frameKey(d, n)
		if f != nil {
			f.mu.Lock()
			changed := f.key != key
			f.key = key
			f.mu.Unlock()
			if changed {
				f.navigateTo("", navSpec{}, nil) // reload from the element's current attributes
			}
			continue
		}
		if t.depth() >= maxFrameDepth || t.closed.Load() {
			continue
		}
		if t.win.frames == nil {
			t.win.frames = map[*html.Node]*frame{}
		}
		t.win.frames[n] = t.newFrameLocked(d, n, key)
		created = true
	}
	for n, f := range t.win.frames {
		if !seen[n] {
			delete(t.win.frames, n)
			t.closeFrameLocked(f)
		}
	}
	t.win.fmu.Unlock()
	if created {
		// layout decides whether an iframe paints as a live frame: run it again
		d.Touch()
		t.contentChanged()
	}
}

// frameKey is what an iframe element asks to show: srcdoc wins over src.
func (t *Tab) frameKey(d *fnet.Doc, n *html.Node) string {
	for _, a := range n.Attr {
		if a.Key == "srcdoc" {
			return "doc:" + a.Val
		}
	}
	src := strings.TrimSpace(attrVal(n, "src"))
	if src == "" || strings.HasPrefix(strings.ToLower(src), "javascript:") {
		return "src:about:blank"
	}
	if abs, err := resolveURL(d.BaseURL(), src); err == nil {
		return "src:" + abs
	}
	return "src:" + src
}

func (t *Tab) newFrameLocked(d *fnet.Doc, n *html.Node, key string) *frame {
	e := t.eng
	e.mu.Lock()
	c := newTab(e, e.newTabID())
	e.mu.Unlock()
	c.ctx = t.ctx
	t.copyEnvironmentTo(c)
	c.win.parent = t
	t.win.frameSeq++
	idx := t.win.frameSeq
	c.win.frameN = idx
	c.win.sbx = parseSandbox(attrVal(n, "sandbox"), hasAttrNode(n, "sandbox"), t.win.sbx)
	c.win.actWindow.Store(t.win.actWindow.Load())
	c.setWindowName(attrVal(n, "name"))
	c.vpW, c.vpH = defaultFrameW, defaultFrameH
	if w, ok := cssPx(attrVal(n, "width")); ok {
		c.vpW = w
	}
	if h, ok := cssPx(attrVal(n, "height")); ok {
		c.vpH = h
	}
	c.SetAsyncImages(true)
	c.SetProgressiveNav(true)
	f := &frame{
		parent: t, node: n, nodeID: d.IDOf(n), idx: idx, child: c, key: key,
		w: c.vpW, h: c.vpH, lazy: strings.EqualFold(attrVal(n, "loading"), "lazy"),
		ready:   make(chan struct{}),
		startCh: make(chan struct{}), stopCh: make(chan struct{}),
		wakeCh: make(chan struct{}, 1), exited: make(chan struct{}),
	}
	c.win.fr = f
	c.SetUpdateHook(t.contentChanged)
	c.SetWakeHook(f.poke)
	t.knows(c)
	c.knows(t)
	c.knows(t.Top())
	if !f.lazy {
		f.started = true
		close(f.startCh)
	}
	go f.run()
	return f
}

func hasAttrNode(n *html.Node, k string) bool {
	for _, a := range n.Attr {
		if a.Key == k {
			return true
		}
	}
	return false
}

func cssPx(s string) (int, bool) {
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "px"))
	if s == "" || strings.ContainsAny(s, "%.") {
		return 0, false
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
		if n > 20000 {
			return 0, false
		}
	}
	return n, n > 0
}

func (t *Tab) closeFrameLocked(f *frame) {
	f.stop()
	c := f.child
	go func() {
		<-f.exited
		c.close()
	}()
}

// closeFrames stops and closes every frame of the tab (navigation away, tab close).
func (t *Tab) closeFrames() {
	t.win.fmu.Lock()
	old := t.win.frames
	t.win.frames, t.win.frameDoc, t.win.scanGen = nil, nil, 0
	t.win.fmu.Unlock()
	for _, f := range old {
		t.closeFrameLocked(f)
	}
}

func (t *Tab) frameList() []*frame {
	t.win.fmu.Lock()
	defer t.win.fmu.Unlock()
	out := make([]*frame, 0, len(t.win.frames))
	for _, f := range t.win.frames {
		out = append(out, f)
	}
	// stable order: by creation index
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].idx < out[j-1].idx; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// findFrameByName finds a frame (at any depth) whose name attribute / window.name matches.
func (t *Tab) findFrameByName(name string) *frame {
	for _, f := range t.frameList() {
		if f.child.WindowName() == name {
			return f
		}
		if g := f.child.findFrameByName(name); g != nil {
			return g
		}
	}
	return nil
}

// ---- layout feedback --------------------------------------------------------------------

// updateFrames runs at the end of every layout pass of the parent: it gives each frame
// the size of its box and starts lazy frames that came near the viewport. It only
// reads the layout result; frames are created by SyncFrames, never here (the layout
// lock is held).
func (e *tabEnv) updateFrames(res *render.Result, vh float64) {
	t := e.t
	t.win.fmu.Lock()
	n := len(t.win.frames)
	frames := make([]*frame, 0, n)
	for _, f := range t.win.frames {
		frames = append(frames, f)
	}
	t.win.fmu.Unlock()
	if n == 0 {
		return
	}
	sy := float64(t.ScrollY())
	for _, f := range frames {
		b := res.Boxes[f.node]
		if b == nil || b.Display == "none" {
			continue
		}
		w, h := int(math.Round(b.Content.W)), int(math.Round(b.Content.H))
		if w > 0 && h > 0 {
			f.resize(w, h)
		}
		f.mu.Lock()
		f.by, f.bh = b.Border.Y, b.Border.H
		f.mu.Unlock()
		if f.lazy && !f.isStarted() && b.Border.Y < sy+vh+lazyMargin && b.Border.Y+b.Border.H > sy-lazyMargin {
			f.start()
		}
	}
}

// resize gives the frame document a new viewport.
func (f *frame) resize(w, h int) {
	f.mu.Lock()
	same := f.w == w && f.h == h
	f.w, f.h = w, h
	f.mu.Unlock()
	if same {
		return
	}
	_, _, dsf, mobile, landscape := f.child.Viewport()
	f.child.SetViewport(w, h, dsf, mobile, landscape)
	f.child.mu.RLock()
	d := f.child.dom
	f.child.mu.RUnlock()
	if d != nil {
		d.Touch() // a new viewport size is a new layout
	}
	f.poke()
	f.parent.contentChanged()
}

// ---- painting --------------------------------------------------------------------------

func (t *Tab) setPaintDSF(d float64) { t.win.paintDSF.Store(math.Float64bits(d)) }

func (t *Tab) lastPaintDSF() float64 {
	d := math.Float64frombits(t.win.paintDSF.Load())
	if d <= 0 {
		return 1
	}
	return d
}

// snapshot returns the frame's current pixels at the parent's scale, repainting the
// child only when its content, size, scale or scroll position changed.
func (f *frame) snapshot() image.Image {
	c := f.child
	if c.closed.Load() {
		return nil
	}
	dsf := f.parent.lastPaintDSF()
	f.mu.Lock()
	w, h := f.w, f.h
	f.mu.Unlock()
	gen := c.Generation()
	sy := c.frameScrollY(h)
	f.mu.Lock()
	if f.imgValid && f.imgGen == gen && f.imgW == w && f.imgH == h && f.imgDSF == dsf && f.imgSY == sy {
		img := f.img
		f.mu.Unlock()
		return img
	}
	f.mu.Unlock()
	img, ok := c.PaintRGBAOpts(PaintOptions{Width: w, Height: h, DSF: dsf, Transparent: true, ScrollY: sy})
	if !ok || img == nil {
		return nil
	}
	f.mu.Lock()
	f.img, f.imgGen, f.imgW, f.imgH, f.imgDSF, f.imgSY, f.imgValid = img, gen, w, h, dsf, sy, true
	f.mu.Unlock()
	return img
}

// frameScrollY is the frame's scroll offset clamped to its content.
func (t *Tab) frameScrollY(vh int) int {
	sy := t.ScrollY()
	if sy <= 0 {
		return 0
	}
	if max := t.docHeight() - vh; sy > max {
		sy = max
	}
	if sy < 0 {
		sy = 0
	}
	return sy
}

// docHeight is the height of the laid out document in CSS px.
func (t *Tab) docHeight() int {
	t.mu.RLock()
	d := t.dom
	t.mu.RUnlock()
	if d == nil {
		return 0
	}
	w := d.LayoutWidth()
	if w <= 0 {
		w = t.vpW
	}
	l := d.Layout(w)
	if ext, _ := l.Ext.(*renderExt); ext != nil {
		return int(math.Ceil(ext.res.Height))
	}
	return l.Height
}

// ---- the frame's goroutine -------------------------------------------------------------

func (f *frame) resolveSrc() (src, srcdoc string, hasDoc bool) {
	f.mu.Lock()
	key := f.key
	f.mu.Unlock()
	switch {
	case strings.HasPrefix(key, "doc:"):
		return "", key[4:], true
	case strings.HasPrefix(key, "src:"):
		return key[4:], "", false
	}
	return "about:blank", "", false
}

func (f *frame) markReady() {
	f.mu.Lock()
	select {
	case <-f.ready:
		f.mu.Unlock()
		return
	default:
	}
	close(f.ready)
	f.mu.Unlock()
	// the iframe element's load event
	if j := f.parent.jsOrNil(); j != nil && !f.stopped.Load() && f.nodeID != 0 {
		j.dispatch(f.nodeID, "load", map[string]any{"bubbles": false, "cancelable": false})
	}
	f.parent.contentChanged()
}

// navigateTo asks the frame to load url (empty = what the element's attributes say).
func (f *frame) navigateTo(abs string, s navSpec, from *Tab) {
	f.mu.Lock()
	f.nav = &frameNav{url: abs, spec: s, from: from}
	f.loadSeq++
	select {
	case <-f.ready:
		f.ready = make(chan struct{})
	default:
	}
	f.mu.Unlock()
	f.poke()
}

// loadInitial / loadNav perform a navigation of the child.
func (f *frame) doLoad(nv *frameNav) {
	c := f.child
	var url string
	var srcdoc string
	hasDoc := false
	if nv != nil && nv.url != "" {
		url = nv.url
	} else {
		url, srcdoc, hasDoc = f.resolveSrc()
	}
	hdrs := map[string]string(nil)
	if nv != nil && nv.spec.noReferrer {
		hdrs = map[string]string{"Referer": ""}
	}
	switch {
	case nv != nil && nv.spec.post != nil:
		_ = nv.spec.post(c)
	case hasDoc:
		c.SetDocument(srcdoc, "about:srcdoc")
	case url == "" || url == "about:blank":
		c.SetDocument("<!doctype html><html><head></head><body></body></html>", "about:blank")
	default:
		_ = c.NavigateProgressive(url, hdrs)
	}
}

func (f *frame) run() {
	defer close(f.exited)
	select {
	case <-f.startCh:
	case <-f.stopCh:
		return
	}
	c := f.child
	loading := true
	var nv *frameNav
	first := true
	for !f.stopped.Load() {
		if f.parent.win.framesPaused.Load() && !first {
			if !f.sleep(100 * time.Millisecond) {
				return
			}
			continue
		}
		f.mu.Lock()
		if f.nav != nil {
			nv, f.nav = f.nav, nil
			loading = true
		}
		f.mu.Unlock()
		if first || nv != nil {
			first = false
			f.doLoad(nv)
			nv = nil
			loading = true
		}
		c.runTasks()
		wait := time.Duration(0)
		busy := false
		if loading {
			more, w := c.LoadStep(6 * time.Millisecond)
			wait, busy = w, more
			if !more || c.LoadLoaded() {
				if !more {
					loading = false
				}
				f.markReady()
			}
		}
		if !busy {
			if j := c.jsOrNil(); j != nil {
				now := time.Now()
				_, fin := j.pumpUntil(now.Add(40*time.Millisecond), now.Add(4*time.Millisecond))
				if c.LoadPending() {
					loading = true // page code navigated: the new document is loading
					f.mu.Lock()
					select {
					case <-f.ready:
						f.ready = make(chan struct{})
					default:
					}
					f.mu.Unlock()
					wait = 0
				} else if fin && !j.hasLiveWork() && c.inboxLen() == 0 && !c.hasTasks() {
					wait = time.Second // idle: only a poke (message, image, scroll, input) wakes it earlier
				} else {
					wait = time.Duration(j.wakeMs * float64(time.Millisecond))
					if wait < 4*time.Millisecond {
						wait = 4 * time.Millisecond
					}
				}
			} else if !loading {
				f.markReady()
				wait = time.Second
			}
		}
		if g := c.Generation(); g != f.lastGen {
			f.lastGen = g
			f.parent.contentChanged()
		}
		if wait > 0 || !busy {
			if wait <= 0 {
				wait = 4 * time.Millisecond
			}
			if !f.sleep(wait) {
				return
			}
		}
	}
}

func (t *Tab) jsOrNil() *jsVM {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.js
}

// waitFrames blocks until the frames this tab has started finished their first load
// (or the budget is spent). Headless navigations use it so a snapshot taken right
// after Navigate contains the frames' content.
func (t *Tab) waitFrames(budget time.Duration) {
	deadline := time.Now().Add(budget)
	for _, f := range t.frameList() {
		if !f.isStarted() {
			continue
		}
		f.mu.Lock()
		ch := f.ready
		f.mu.Unlock()
		select {
		case <-ch:
		case <-time.After(time.Until(deadline)):
			return
		}
		f.child.waitFrames(time.Until(deadline))
	}
}

// ---- public frame API ------------------------------------------------------------------

// FrameInfo describes one frame of a document.
type FrameInfo struct {
	Ref     string `json:"ref"`  // prefix of the refs inside it in a snapshot: f1, f2, ...
	Name    string `json:"name"` // the iframe's name
	URL     string `json:"url"`
	Origin  string `json:"origin"`
	Sandbox bool   `json:"sandbox,omitempty"`
	Tab     *Tab   `json:"-"`
}

// Frames lists the frames of the current document (direct children only).
func (t *Tab) Frames() []FrameInfo {
	var out []FrameInfo
	for _, f := range t.frameList() {
		out = append(out, FrameInfo{Ref: "f" + itoa(f.idx), Name: f.child.WindowName(), URL: f.child.URL(),
			Origin: f.child.origin(), Sandbox: f.child.Sandboxed(), Tab: f.child})
	}
	return out
}

// FrameByRef resolves "f1" or "f1f2" to the nested frame tab.
func (t *Tab) FrameByRef(ref string) (*Tab, bool) {
	cur := t
	for ref != "" {
		if ref[0] != 'f' {
			return nil, false
		}
		i := 1
		n := 0
		for i < len(ref) && ref[i] >= '0' && ref[i] <= '9' {
			n = n*10 + int(ref[i]-'0')
			i++
		}
		if i == 1 {
			return nil, false
		}
		var next *Tab
		for _, f := range cur.frameList() {
			if f.idx == n {
				next = f.child
			}
		}
		if next == nil {
			return nil, false
		}
		cur, ref = next, ref[i:]
	}
	return cur, cur != t
}

// splitFrameRef splits a frame-qualified selector "f1e5" / "f1f2e5" / "ref=f1e5" into
// the frame path and the selector inside it ("e5").
func splitFrameRef(sel string) (path, inner string, ok bool) {
	s := strings.TrimSpace(sel)
	s = strings.TrimPrefix(s, "ref=")
	i := 0
	for i < len(s) && s[i] == 'f' {
		j := i + 1
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		if j == i+1 {
			return "", "", false
		}
		i = j
	}
	if i == 0 || i >= len(s) || s[i] != 'e' || i+1 >= len(s) || s[i+1] < '0' || s[i+1] > '9' {
		return "", "", false
	}
	for k := i + 1; k < len(s); k++ {
		if s[k] < '0' || s[k] > '9' {
			return "", "", false
		}
	}
	return s[:i], s[i:], true
}

// routeFrameSel resolves a frame-qualified ref to the tab that owns it.
func (t *Tab) routeFrameSel(sel string) (*Tab, string, bool) {
	path, inner, ok := splitFrameRef(sel)
	if !ok {
		return nil, "", false
	}
	c, ok := t.FrameByRef(path)
	if !ok {
		return nil, "", false
	}
	return c, inner, true
}

// SetFramesPaused pauses (true) or resumes the frames' event loops: a background tab
// should not run its embedded players.
func (t *Tab) SetFramesPaused(p bool) {
	t.win.framesPaused.Store(p)
	for _, f := range t.frameList() {
		f.child.SetFramesPaused(p)
		if !p {
			f.poke()
		}
	}
}

// snapshotFrames returns, for the iframe element n, the child's accessibility tree
// with every ref qualified by the frame's prefix.
func (t *Tab) snapshotFrame(n *html.Node, o fnet.SnapshotOptions) (string, []string) {
	f := t.frameOf(n)
	if f == nil {
		return "", nil
	}
	if f.lazy && !f.isStarted() {
		f.start()
	}
	f.mu.Lock()
	ch := f.ready
	f.mu.Unlock()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
	}
	o.Selector = ""
	c := f.child
	c.syncFrames(false)
	o.Frame = func(m *html.Node) (string, []string) { return c.snapshotFrame(m, o) }
	c.mu.RLock()
	d := c.dom
	c.mu.RUnlock()
	if d == nil {
		return "", nil
	}
	y, err := d.AriaSnapshotV2(o)
	if err != nil {
		return "", nil
	}
	prefix := "f" + itoa(f.idx)
	lines := strings.Split(strings.TrimRight(y, "\n"), "\n")
	for i, ln := range lines {
		lines[i] = qualifyRefs(ln, prefix)
	}
	return prefix, lines
}

var (
	refAttrRE   = regexp.MustCompile(`ref=((?:f\d+)*e\d+)\]`)
	frameAttrRE = regexp.MustCompile(`frame=((?:f\d+)+)\]`)
)

// qualifyRefs rewrites "[ref=e5]" to "[ref=f1e5]" (and "f2e5" to "f1f2e5", "[frame=f2]" to
// "[frame=f1f2]") in a snapshot line.
func qualifyRefs(line, prefix string) string {
	line = refAttrRE.ReplaceAllString(line, "ref="+prefix+"$1]")
	return frameAttrRE.ReplaceAllString(line, "frame="+prefix+"$1]")
}
