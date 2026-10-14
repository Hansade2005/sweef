package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"image"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/sweefbrowser/sweef/internal/engine"
	fnet "github.com/sweefbrowser/sweef/internal/net"
)

// etab wraps one engine tab with its worker and per-tab app state.
type etab struct {
	a      *App
	id     string // state tab id (page) or "chrome" / "overlay"
	t      *engine.Tab
	w      *worker
	kind   string // "chrome" | "overlay" | "page"
	incog  bool
	closed atomic.Bool

	// worker-only
	bindSID   int64
	paintedAt struct {
		gen    uint64
		w, h   int
		scroll int
		scale  float64
	}
	band         bandState                  // page tabs: the band of the document last rasterised (worker only)
	spare        atomic.Pointer[image.RGBA] // a band texture the compositor has let go of, for the next paint to reuse
	scrollPosDev atomic.Int32               // last scroll offset the compositor was moved to (device px)
	wheelState   atomic.Int32               // wheelUnknown | wheelPassive | wheelCancelled (wheel.go), reset by each navigation
	elemPos      atomic.Int64               // scroll offsets an inner scroller is to be moved to (x<<32 | y)
	elemBusy     atomic.Bool                // a job moving an inner scroller is queued or running on the worker
	scrollVel    atomic.Int32               // scroll velocity, CSS px/s (negative: up), 0 when the view rests
	scrollAt     atomic.Int64               // unix nanos of the last scroll movement
	scrollDir    atomic.Int32               // sign of the last scroll movement: -1 up, 0 unknown, 1 down
	dlSeen       int
	netSID       int64 // last network-log entry examined by the cookie sweep
	lastTitle    string
	certError    atomic.Bool
	vpApplied    struct {
		w, h int
		dsf  float64
	} // viewport last given to the engine (worker only): setting it again would drop the layout cache
	lastURL    string
	dataScript string
	hoverLeaf  *engine.Tab // frame the pointer is in (worker only)

	// UI loop only: the navigation whose URL and title were committed at its first frame
	commitSeq int64
	commitURL string

	// cross-goroutine
	paintQueued   atomic.Bool
	vpW, vpH      atomic.Int32 // CSS px
	scrollCSS     atomic.Int32
	topURL        atomic.Value // string: URL of the top-level page, for the shield
	navTarget     atomic.Value // string: document URL currently being fetched
	navSeq        atomic.Int64
	driveCb       func()       // worker only: the load-finished callback a later driveLoad of the same navigation handed over
	loadMode      atomic.Int32 // 0 none, 1 push, 2 replace, 3 traverse
	blocked       atomic.Int32
	reqN, respN   atomic.Int32
	lastInput     atomic.Int64 // unix nanos of the last user gesture, for the popup gate
	popupAt       atomic.Int64
	docCSS        atomic.Int32 // last known document height, CSS px
	pumpArmed     atomic.Bool
	scrollNotify  atomic.Bool
	paintW        atomic.Int32 // overlay: painted extent (CSS px)
	paintH        atomic.Int32 // overlay: painted height (CSS px)
	lastActive    atomic.Int64
	wakeQueued    atomic.Bool  // an event-loop slice for async results (detached images) is queued
	driveSeq      atomic.Int64 // navigation whose progressive load a worker op chain is stepping (driveLoad), 0 = none
	loadErr       atomic.Value // string: error of the last load started by loadURL ("" = ok); read by the agent host
	restoreScroll atomic.Int32
	navHdr        atomic.Value // map[string]string: headers for the next load (consumed once)

	// Data Saver
	agentLoad   atomic.Bool  // the agent drives this tab: loads use the agent mode
	fullOnce    atomic.Bool  // request: the next load runs without saving (consumed by loadURL)
	fullActive  atomic.Bool  // the current page was loaded fully, once
	loadOnce    atomic.Value // string: URL allowed through the hard stop once
	liteSkipped atomic.Int32 // tap-to-load placeholders on the page (from the engine's counters)
}

func (a *App) newEtab(kind, id string, t *engine.Tab, incog bool) *etab {
	e := &etab{a: a, id: id, t: t, kind: kind, incog: incog, w: newWorker(&a.pending)}
	e.topURL.Store("")
	e.navTarget.Store("")
	e.loadOnce.Store("")
	e.installHooks()
	// the selection changed (mouse, keys or script): page script hears selectionchange, and the window repaints
	t.SetSelectionHook(func() {
		if e.closed.Load() {
			return
		}
		e.w.tryDo(func() {
			_, _ = e.t.Evaluate("__swSelChanged()")
			e.afterOp()
		})
	})
	if kind == "page" {
		// pages are shown while they load: images and the rest of the load arrive after the first frame
		// and repaint it; the agent switches this off for the tabs it drives (see loadURL)
		e.t.SetAsyncImages(true)
		e.t.SetProgressiveNav(true)
		e.t.SetUpdateHook(func() { e.requestPaint(false) })
		e.t.SetWakeHook(e.wakePump)
		e.t.SetActivationWindow(2 * time.Second) // a click or key press keeps allowing popups for 2 s (the engine default is 5 s)
		e.t.SetMediaBroker(a.media)
		e.t.SetScrollHook(func(r engine.ScrollRequest) { a.post(func() { a.scriptScroll(e, r) }) })
	}
	return e
}

// installHooks wires the helper script, bindings and the routing handler.
func (e *etab) installHooks() {
	e.t.AddInitScript(helperJS)
	e.t.AddInitScript(helperJS2)
	_ = e.t.ExposeBinding("__sweefPost")
	e.t.Route("**", e.route)
	if e.kind == "page" {
		e.t.SetExtraHeaders(e.a.priv.Headers())
	}
}

// route is the single interception point: it serves sweef:// pages from the
// embedded UI and applies the tracker blocker to everything else.
func (e *etab) route(req *http.Request) (int, string, bool) {
	u := req.URL
	if u.Scheme == "sweef" {
		return e.a.serveInternal(e, u)
	}
	if e.kind != "page" {
		return 0, "", false
	}
	raw := u.String()
	if nt, _ := e.navTarget.Load().(string); nt == raw {
		return 0, "", false // never block the document itself
	}
	top, _ := e.topURL.Load().(string)
	if d := e.a.priv.Check(top, raw); d.Block {
		e.t.DevNoteBlocked(raw, "blocked by the tracker blocker ("+u.Hostname()+")")
		e.blocked.Add(1)
		e.a.prof.Stats.AddBlocked(1)
		e.a.noteBlocked(e, u.Hostname())
		return 0, "", true
	}
	return 0, "", false
}

// setViewport gives the engine tab a viewport, but only when it changed
// (worker only): the call invalidates layout, which is expensive on big pages.
func (e *etab) setViewport(w, h int, dsf float64) {
	if e.vpApplied.w == w && e.vpApplied.h == h && e.vpApplied.dsf == dsf {
		return
	}
	e.vpApplied.w, e.vpApplied.h, e.vpApplied.dsf = w, h, dsf
	e.t.SetViewport(w, h, dsf, false, false)
}

// ---- worker helpers ------------------------------------------------------------

// call runs f on the tab worker and waits for it (used by the Backend).
func (e *etab) call(f func() error) error {
	ch := make(chan error, 1)
	if !e.w.do(func() {
		err := f()
		if e.kind == "page" {
			e.drainBindings()
		} else {
			e.afterOp() // chrome/overlay: also repaint and re-probe what covers the viewport
		}
		ch <- err
	}) {
		return errors.New("tab closed")
	}
	select {
	case err := <-ch:
		return err
	case <-time.After(60 * time.Second):
		return errors.New("timeout")
	}
}

func (e *etab) text() (string, error) {
	var s string
	err := e.call(func() error {
		var err error
		s, err = e.t.Text("body")
		return err
	})
	if len(s) > 200_000 {
		s = s[:200_000]
	}
	return s, err
}

// eval evaluates js (worker only) and decodes the JSON result into out.
func (e *etab) eval(js string, out any) error {
	raw, err := e.t.Evaluate(js)
	if err != nil {
		return err
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// setNavHeaders sets request headers for the next document load of the tab (a popup's Referer).
func (e *etab) setNavHeaders(h map[string]string) { e.navHdr.Store(h) }

func (e *etab) takeNavHeaders() map[string]string {
	h, _ := e.navHdr.Swap(map[string]string(nil)).(map[string]string)
	return h
}

// evalOn is eval for another document of the page: a frame, or this tab itself.
func (e *etab) evalOn(t *engine.Tab, js string, out any) error {
	raw, err := t.Evaluate(js)
	if err != nil {
		return err
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// pointerTarget is the document that receives a pointer event at (px, py): for the page, the
// frame under the point (with the point translated into it) or the page itself.
func (e *etab) pointerTarget(tgt string, px, py int) (*engine.Tab, int, int) {
	if tgt != "page" || e.kind != "page" {
		return e.t, px, py
	}
	return e.t.FrameHit(px, py)
}

// inputTab is the document that receives keyboard input: the frame the person last clicked into, or the page.
func (e *etab) inputTab() *engine.Tab {
	if e.kind != "page" {
		return e.t
	}
	return e.t.FocusedTab()
}

// leaveHover tells the frame the pointer just left that it did, when the pointer is now in another document.
func (e *etab) leaveHover(now *engine.Tab) {
	old := e.hoverLeaf
	if now == e.t {
		e.hoverLeaf = nil
	} else {
		e.hoverLeaf = now
	}
	if old != nil && old != now && !old.IsClosed() {
		_, _ = old.Evaluate("__sweefPtr('move',-1,-1,0,0)")
	}
}

// drainBindings forwards new binding calls to the UI loop. Worker only.
func (e *etab) drainBindings() {
	calls := e.t.BindingCalls(e.bindSID)
	for _, c := range calls {
		if c.SID > e.bindSID {
			e.bindSID = c.SID
		}
		switch c.Name {
		case "__sweefPost":
			// privilege check: only sweef:// documents may talk to the browser
			if !strings.HasPrefix(c.URL, "sweef://") {
				continue
			}
			var args []json.RawMessage
			if json.Unmarshal(c.Args, &args) != nil || len(args) < 1 {
				continue
			}
			var typ string
			if json.Unmarshal(args[0], &typ) != nil {
				continue
			}
			var payload json.RawMessage
			if len(args) > 1 {
				payload = args[1]
			}
			if typ == "omni.input" {
				e.omniSync(payload)
				continue
			}
			e.a.post(func() { e.a.onUIMessage(e, typ, payload) })
		}
	}
}

// pump gives page timers a short slice and forwards what they produced.
func (e *etab) pump(d time.Duration) {
	e.t.Settle(d)
	e.drainBindings()
	if e.kind == "page" && e.t.LoadPending() {
		// page code navigated and the new document is loading progressively: nobody else steps it
		e.driveLoad(e.navSeq.Load(), nil)
	}
}

// wakePump gives the page's event loop a slice because an asynchronous result is waiting for it (the load
// event of a detached image). Any goroutine.
func (e *etab) wakePump() {
	if e.closed.Load() || !e.wakeQueued.CompareAndSwap(false, true) {
		return
	}
	if !e.w.tryDo(func() {
		e.wakeQueued.Store(false)
		e.pump(4 * time.Millisecond)
		e.paint(false)
		e.armPump()
	}) {
		e.wakeQueued.Store(false)
	}
}

// hasTimers reports whether page JS still has timers pending (worker only).
func (e *etab) hasTimers() bool {
	var n int
	if e.eval(`__swTimerCount()`, &n) != nil {
		return false
	}
	return n > 0
}

// requestPaint schedules a (coalesced) repaint on the worker.
func (e *etab) requestPaint(force bool) {
	if e.closed.Load() {
		return
	}
	if !force && !e.paintQueued.CompareAndSwap(false, true) {
		return
	}
	if force {
		e.paintQueued.Store(true)
	}
	run := func() {
		e.paintQueued.Store(false)
		e.paint(force)
	}
	if force {
		e.w.do(run)
	} else if !e.w.tryDo(run) {
		e.paintQueued.Store(false)
	}
}

// paint rasterises the tab and posts the pixels to the UI loop. Worker only.
func (e *etab) paint(force bool) {
	if e.closed.Load() || e.a.closing.Load() {
		return
	}
	a := e.a
	gen := e.t.Generation()
	scale := a.scaleAtomic()
	vw, vh := int(e.vpW.Load()), int(e.vpH.Load())
	if vw <= 0 || vh <= 0 {
		return
	}
	switch e.kind {
	case "chrome", "overlay":
		// the chrome and the overlay are painted on a transparent canvas: where they
		// draw nothing the page (or the chrome below the overlay) shows through
		opts := engine.PaintOptions{DSF: scale, Transparent: true}
		// the chrome repaints only what its mutation touched; a forced paint (a new
		// size or scale, a recreated compositor) always repaints everything
		opts.Incremental = e.kind == "chrome" && !force
		if e.kind == "overlay" {
			vw, vh = int(e.paintW.Load()), int(e.paintH.Load())
			if vw <= 0 || vh <= 0 {
				return
			}
		}
		if !force && gen == e.paintedAt.gen && vw == e.paintedAt.w && vh == e.paintedAt.h && scale == e.paintedAt.scale {
			return
		}
		t0 := time.Now()
		opts.Width, opts.Height = vw, vh
		img, ok := e.t.PaintRGBAOpts(opts)
		if !ok || img == nil {
			return
		}
		dt := time.Since(t0)
		if pi := e.t.LastPaintInfo(); pi != nil && e.kind == "chrome" {
			// where a chrome repaint goes: style+layout, then the raster of the damage
			e.a.comp.RecordPaint("chrome: style + layout", pi.Layout)
			e.a.comp.RecordPaint("chrome: raster", pi.Raster)
		}
		e.paintedAt.gen, e.paintedAt.w, e.paintedAt.h, e.paintedAt.scale = gen, vw, vh, scale
		kind := e.kind
		e.a.post(func() { e.a.onChromePainted(kind, img, dt) })
	default:
		e.paintPage(force, vw, vh)
	}
}

// pageBandAlign is the granularity (CSS px) of band edges for a device scale: the smallest step that is a whole
// number of device pixels, so every band is rasterised on the same pixel grid and bands can be stitched.
func pageBandAlign(scale float64) int {
	for _, q := range []int{8, 40, 200, 1000, 10000} {
		d := float64(q) * scale
		if math.Abs(d-math.Round(d)) < 1e-6 {
			return q
		}
	}
	return 1
}

// pageBand chooses the rows of the document (CSS px) to keep rasterised around scroll offset sc for a viewport
// of height vh: the visible rows plus a screen behind and two ahead in the direction of travel (dir < 0 is
// upwards). Edges are multiples of q (see pageBandAlign) and the band never extends past the document.
func pageBand(sc, vh, doc, vel, q int) (y0, y1 int) {
	// vel is the scroll velocity in CSS px per second (negative: upwards). The faster the view moves, the more
	// rows ahead of it are worth having: half a second of travel, between two and six screens.
	reach := min(max(2*vh, absInt(vel)/2), 6*vh)
	behind, ahead := vh, reach
	if vel < 0 {
		behind, ahead = reach, vh
	}
	y0 = sc - behind
	if y0 < 0 {
		y0 = 0
	}
	y0 = y0 / q * q
	y1 = sc + vh + ahead
	y1 = (y1 + q - 1) / q * q
	if y1 > doc {
		y1 = doc
	}
	if y1 <= y0 {
		y1 = y0 + vh
	}
	return
}

// pageNeed is the part of the document that has to be rasterised for the view at sc to look right while it
// moves a little: half a screen of margin on either side.
func pageNeed(sc, vh, doc, vel int) (y0, y1 int) {
	ahead := min(max(vh/2, absInt(vel)/4), 3*vh)
	back := vh / 2
	if vel < 0 {
		back, ahead = ahead, back
	}
	y0 = sc - back
	if y0 < 0 {
		y0 = 0
	}
	y1 = sc + vh + ahead
	if y1 > doc {
		y1 = doc
	}
	return
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// paintPage rasterises the band of the page around the scroll position and posts it to the compositor. A band
// is repainted only when the document changed or the view is about to leave the band; when only the view moved,
// rows the previous band already holds are copied and only the new rows are rasterised, so the cost of a
// scroll step does not grow with the length of the page or with how far down the view is. Worker only.
func (e *etab) paintPage(force bool, vw, vh int) {
	a := e.a
	e.checkMeta()
	e.t.NormalizeScroll() // inner scrollers whose content shrank or whose box got smaller come back into range
	gen := e.t.Generation()
	scale := a.pageDSF()
	vw, vh = a.pageCSS(vw), a.pageCSS(vh)
	var doc int
	_ = e.eval(`__sweefDocH()`, &doc)
	if doc <= 0 {
		doc = vh
	}
	e.docCSS.Store(int32(doc))
	sc := int(e.scrollCSS.Load())
	if sc > doc {
		sc = doc
	}
	b := &e.band
	same := !force && b.img != nil && b.gen == gen && b.w == vw && b.scale == scale
	vel := int(e.scrollVel.Load())
	n0, n1 := pageNeed(sc, vh, doc, vel)
	if same && b.y0 <= n0 && b.y1 >= n1 {
		// same content and the band covers the view: only the document height may differ
		e.a.post(func() { e.a.onPageMetrics(e.id, doc) })
		return
	}
	q := pageBandAlign(scale)
	y0, y1 := pageBand(sc, vh, doc, vel, q)
	targets := [][2]int{{y0, y1}}
	if same {
		ov := max(0, min(y1, b.y1)-max(y0, b.y0))
		if (y1-y0)-ov > vh*3/2 {
			// The view has moved a long way from the old band (a fling, End, a jump): paint the rows it needs
			// first and send them at once, then fill in the rest of the band around them.
			qy0, qy1 := n0/q*q, (n1+q-1)/q*q
			qy0, qy1 = max(qy0, y0), min(qy1, y1)
			if qy1 > qy0 && (qy1-qy0)*4 < (y1-y0)*3 {
				targets = [][2]int{{qy0, qy1}, {y0, y1}}
			}
		}
	} else if (y1-y0) > vh*3/2 && force {
		// a new document or size: the first frame needs only the view
		qy0, qy1 := n0/q*q, (n1+q-1)/q*q
		qy0, qy1 = max(qy0, y0), min(qy1, y1)
		if qy1 > qy0 && (qy1-qy0)*4 < (y1-y0)*3 {
			targets = [][2]int{{qy0, qy1}, {y0, y1}}
		}
	}
	for _, t := range targets {
		if !e.buildBand(t[0], t[1], vw, scale, gen, doc, same) {
			return
		}
		if len(targets) > 1 && e.closed.Load() {
			return
		}
	}
}

// buildBand rasterises the document rows [y0, y1) (CSS px) into the band texture and posts it to the compositor,
// copying the rows the previous band already holds when reuse is set and the content has not changed (a forced
// paint never reuses: a new document can have the generation of the one before). Worker only.
func (e *etab) buildBand(y0, y1, vw int, scale float64, gen uint64, doc int, reuse bool) bool {
	b := &e.band
	same := reuse && b.img != nil && b.gen == gen && b.w == vw && b.scale == scale
	dev := func(y int) int { return int(math.Round(float64(y) * scale)) }
	W := int(math.Round(float64(vw) * scale))
	H := dev(y1) - dev(y0)
	if W < 1 || H < 1 {
		return false
	}
	t0 := time.Now()
	paintRows := func(r0, r1 int) *image.RGBA {
		img, ok := e.t.PaintRGBAOpts(engine.PaintOptions{Width: vw, Height: r1 - r0, DSF: scale, ScrollY: r0, Cull: true})
		if !ok {
			return nil
		}
		return img
	}
	var out *image.RGBA
	ov0, ov1 := y0, y0
	if same {
		ov0, ov1 = max(y0, b.y0), min(y1, b.y1)
	}
	if same && ov1 > ov0 && b.img.Bounds().Dx() == W {
		// reuse the overlap with the previous band
		out = e.spare.Swap(nil)
		if out == nil || out.Bounds().Dx() != W || len(out.Pix) < H*4*W {
			out = image.NewRGBA(image.Rect(0, 0, W, H))
		} else {
			out.Rect = image.Rect(0, 0, W, H)
			out.Stride = 4 * W
		}
		rows := dev(ov1) - dev(ov0)
		src := b.img.PixOffset(0, dev(ov0)-dev(b.y0))
		dst := out.PixOffset(0, dev(ov0)-dev(y0))
		copy(out.Pix[dst:dst+rows*out.Stride], b.img.Pix[src:src+rows*b.img.Stride])
		// the rows next to the view first would be better still, but the two parts are painted back to back
		for _, part := range [][2]int{{y0, ov0}, {ov1, y1}} {
			if part[1] <= part[0] {
				continue
			}
			img := paintRows(part[0], part[1])
			if img == nil {
				return false
			}
			blitRows(out, img, dev(part[0])-dev(y0))
		}
	} else {
		img := paintRows(y0, y1)
		if img == nil {
			return false
		}
		out = img
	}
	dt := time.Since(t0)
	*b = bandState{img: out, y0: y0, y1: y1, w: vw, scale: scale, gen: gen}
	e.a.post(func() { e.a.onPagePainted(e.id, out, dev(y0), doc, dt) })
	return true
}

type bandState struct {
	img    *image.RGBA
	y0, y1 int // CSS px
	w      int // CSS px
	scale  float64
	gen    uint64
}

// blitRows copies src into dst starting at row y of dst.
func blitRows(dst, src *image.RGBA, y int) {
	n := src.Bounds().Dy()
	if y+n > dst.Bounds().Dy() {
		n = dst.Bounds().Dy() - y
	}
	w := 4 * min(src.Bounds().Dx(), dst.Bounds().Dx())
	for r := 0; r < n; r++ {
		copy(dst.Pix[(y+r)*dst.Stride:][:w], src.Pix[r*src.Stride:][:w])
	}
}

// close stops the worker asynchronously and removes the engine tab.
func (e *etab) close() {
	if !e.closed.CompareAndSwap(false, true) {
		return
	}
	e.a.wg.Add(1)
	go func() {
		defer e.a.wg.Done()
		e.w.stop()
		if e.kind == "page" {
			_ = e.a.eng.CloseTab(e.t.ID)
		}
	}()
}

// checkMeta notices title changes made by page scripts and URL changes made by
// history.pushState (worker only; called whenever the tab is repainted).
func (e *etab) checkMeta() {
	title, cur := e.t.Title(), e.t.URL()
	var loc string
	if e.eval(`location.href`, &loc) == nil && loc != "" {
		cur = loc // history.pushState changes location, not the engine's tab URL
	}
	if title != e.lastTitle {
		e.lastTitle = title
		if title != "" {
			id := e.id
			e.a.post(func() { e.a.onTitle(id, title) })
		}
	}
	if cur != e.lastURL {
		if e.lastURL != "" && e.loadMode.Load() == modeNone && cur != "about:blank" {
			e.a.post(func() { e.a.onInPageURL(e, cur) })
		}
		e.lastURL = cur
	}
}

// sweepThirdPartyCookies deletes cookies that third-party responses set while
// this tab was on its current site (worker only). The engine has no per-request
// cookie hook, so this runs after loads and inputs: such a cookie never
// persists and is never sent on a later request.
func (e *etab) sweepThirdPartyCookies() {
	log := e.t.NetworkLog(e.netSID)
	if len(log) == 0 {
		return
	}
	e.netSID = log[len(log)-1].SID
	if e.kind != "page" || !e.a.priv.Settings().BlockThirdPartyCookies {
		return
	}
	top, _ := e.topURL.Load().(string)
	tu, err := url.Parse(top)
	if err != nil || tu.Hostname() == "" {
		return
	}
	bad := map[string]bool{}
	for _, ev := range log {
		rec, ok := ev.Data.(fnet.RequestRecord)
		if !ok || ev.Kind != engine.EvResponse || rec.ResponseHeaders["Set-Cookie"] == "" {
			continue
		}
		ru, err := url.Parse(rec.URL)
		if err != nil || ru.Hostname() == "" || !e.a.priv.Active(tu.Hostname(), ru.Hostname()) {
			continue
		}
		bad[strings.ToLower(ru.Hostname())] = true
	}
	if len(bad) == 0 {
		return
	}
	jar := e.t.Context().JarRef()
	var keep []*engine.Cookie
	dropped := 0
	for _, c := range jar.All("") {
		dom := strings.ToLower(strings.TrimPrefix(c.Domain, "."))
		hit := false
		for h := range bad {
			if h == dom || strings.HasSuffix(h, "."+dom) {
				hit = true
			}
		}
		if hit {
			dropped++
		} else {
			keep = append(keep, c)
		}
	}
	if dropped > 0 {
		jar.Replace(keep)
	}
}

// ---- engine event bus ---------------------------------------------------------------

// busLoop turns engine events into progress / commit / download updates.
func (a *App) busLoop() {
	defer a.wg.Done()
	id, ch := a.eng.Bus().Subscribe(8192)
	defer a.eng.Bus().Unsubscribe(id)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return
			}
			a.onEngineEvent(ev)
		case <-a.quit:
			return
		}
	}
}

func (a *App) etabByEngineID(id string) *etab {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, e := range a.pages {
		if e.t.ID == id {
			return e
		}
	}
	return nil
}

func (a *App) onEngineEvent(ev engine.Event) {
	if a.dat.eng != nil && a.dat.eng.IsDataEvent(ev) {
		a.dataKick() // throttled "data" events: the counters moved
		return
	}
	if ev.Tab == "" {
		return
	}
	e := a.etabByEngineID(ev.Tab)
	if e == nil {
		return
	}
	switch ev.Kind {
	case engine.EvRequest:
		e.reqN.Add(1)
		a.post(func() { a.bumpProgress(e) })
	case engine.EvResponse:
		e.respN.Add(1)
		a.post(func() { a.bumpProgress(e) })
	case engine.EvNavigated:
		if m, ok := ev.Data.(map[string]any); ok {
			if u, _ := m["url"].(string); u != "" {
				a.post(func() { a.onEngineNavigated(e, u) })
			}
		}
	case engine.EvDownload:
		if d, ok := ev.Data.(engine.Download); ok {
			a.post(func() { a.onEngineDownload(e, d) })
		}
	case engine.EvPopup:
		if pe, ok := ev.Data.(engine.PopupEvent); ok && pe.Blocked {
			now := time.Now().UnixNano()
			if now-e.popupAt.Swap(now) > int64(500*time.Millisecond) {
				a.post(func() { a.sendChrome(mToast, map[string]any{"text": "A pop-up was blocked", "kind": "info"}) })
			}
		}
	case engine.EvAudio:
		if st, ok := ev.Data.(engine.AudioState); ok {
			a.post(func() { a.onTabAudio(e, st) })
		}
	case engine.EvMediaSession:
		a.post(func() { a.markState() })
	case engine.EvFullscreen:
		if st, ok := ev.Data.(engine.FullscreenState); ok {
			a.post(func() { a.onTabFullscreen(e, st.Active) })
		}
	case engine.EvDialog:
		a.post(func() {
			a.sendChrome(mToast, map[string]any{"text": "This page showed a dialog that was handled automatically", "kind": "info"})
		})
	}
}

// ---- small helpers --------------------------------------------------------------------

func jsString(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSpace(b.String())
}

func jsJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	// U+2028/2029 are valid JSON but terminate JS string literals in old parsers
	s := strings.ReplaceAll(string(b), " ", ` `)
	return strings.ReplaceAll(s, " ", ` `)
}

func parseHTTP(raw string) (*url.URL, bool) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, false
	}
	return u, true
}

var _ = image.Rect
