package engine

// frames_input.go: input into frames. A frame is a viewport inside another page, so
// a pointer position in the parent is routed by the parent's own hit test
// (render.Result.HitAt, which already honours paint order, clipping, ancestor
// transforms and pointer-events) and translated into the frame's document space.
// Keyboard input goes to the document that has focus: the leaf of the focus chain
// that the last click set up.

import (
	"math"
	"strings"

	fnet "github.com/sweefbrowser/sweef/internal/net"
)

// frameAt reports the frame under the document point (x, y) of this tab and the
// point translated to that frame's document space, or nil.
func (t *Tab) frameAt(x, y float64) (*frame, float64, float64) {
	t.win.fmu.Lock()
	n := len(t.win.frames)
	t.win.fmu.Unlock()
	if n == 0 {
		return nil, 0, 0
	}
	t.mu.RLock()
	doc := t.dom
	t.mu.RUnlock()
	if doc == nil || !renderEnabled() {
		return nil, 0, 0
	}
	w := doc.LayoutWidth()
	if w <= 0 {
		w = t.vpW
	}
	l := doc.Layout(w)
	ext, _ := l.Ext.(*renderExt)
	if ext == nil || ext.res.HitAt == nil {
		return nil, 0, 0
	}
	hit, px, py := ext.res.HitAt(x, y)
	if hit == nil || hit.Data != "iframe" {
		return nil, 0, 0
	}
	f := t.frameOf(hit)
	if f == nil || !f.isStarted() {
		return nil, 0, 0
	}
	b := ext.res.Boxes[hit]
	if b == nil || b.Display == "none" || !b.Visible {
		return nil, 0, 0 // visibility:hidden frames take no input
	}
	c := b.Content
	lx, ly := px-c.X, py-c.Y
	if lx < 0 || ly < 0 || lx >= c.W || ly >= c.H {
		return nil, 0, 0 // on the border
	}
	return f, lx, ly + float64(f.child.frameScrollY(int(math.Round(c.H))))
}

// FrameHit finds the document that receives a pointer event at the document point
// (x, y): the tab itself, or the deepest frame under the point with the point
// translated into its document space.
func (t *Tab) FrameHit(x, y int) (leaf *Tab, lx, ly int) {
	cur, fx, fy := t, float64(x), float64(y)
	for depth := 0; depth < maxFrameDepth+1; depth++ {
		f, ax, ay := cur.frameAt(fx, fy)
		if f == nil {
			break
		}
		cur, fx, fy = f.child, ax, ay
	}
	return cur, int(math.Floor(fx)), int(math.Floor(fy))
}

// FocusFrame makes leaf (this tab or one of its nested frames) the document that
// receives keyboard input, blurring the element of the document that had it.
func (t *Tab) FocusFrame(leaf *Tab) {
	prev := t.FocusedTab()
	var chain []*Tab
	for c := leaf; c != nil && c != t; c = c.win.parent {
		chain = append([]*Tab{c}, chain...)
	}
	cur := t
	for _, c := range chain {
		cur.win.focusFrame.Store(c.win.fr)
		cur = c
	}
	cur.win.focusFrame.Store(nil)
	if prev != nil && prev != leaf {
		prev.blurDocument()
	}
}

// FocusedTab is the document that has keyboard focus: t, or a nested frame.
func (t *Tab) FocusedTab() *Tab {
	cur := t
	for i := 0; i < maxFrameDepth+1; i++ {
		f := cur.win.focusFrame.Load()
		if f == nil || f.child.closed.Load() || f.parent != cur {
			return cur
		}
		cur = f.child
	}
	return cur
}

// blurDocument blurs the focused element of a document that lost focus.
func (t *Tab) blurDocument() {
	if t.jsOrNil() == nil || t.closed.Load() {
		return
	}
	_, _ = t.Evaluate(`(function(){var e=document.activeElement;if(e&&e!==document.body&&e.blur)e.blur();})()`)
}

// ScrollFrameBy scrolls a frame document by dy CSS px inside its box and reports whether
// it moved (false at either end, so the caller can scroll the page instead).
func (t *Tab) ScrollFrameBy(dy int) bool {
	f := t.win.fr
	if t.win.parent == nil || f == nil || dy == 0 {
		return false
	}
	f.mu.Lock()
	h := f.h
	f.mu.Unlock()
	max := t.docHeight() - h
	if max <= 0 {
		return false
	}
	cur := t.ScrollY()
	nxt := cur + dy
	if nxt < 0 {
		nxt = 0
	}
	if nxt > max {
		nxt = max
	}
	if nxt == cur {
		return false
	}
	t.setFrameScroll(nxt)
	return true
}

func (t *Tab) setFrameScroll(y int) {
	t.SetScrollY(y)
	if t.jsOrNil() != nil {
		_, _ = t.Evaluate(`typeof __sweefScrollTo==='function'&&__sweefScrollTo(` + itoa(y) + `)`)
	}
	t.contentChanged()
}

// WheelAt routes a wheel gesture at the document point (x, y) to the innermost frame
// under it that can still scroll; false means no frame took it and the page scrolls.
func (t *Tab) WheelAt(x, y, dx, dy int) bool {
	leaf, _, _ := t.FrameHit(x, y)
	for c := leaf; c != nil && c != t; c = c.win.parent {
		if c.ScrollFrameBy(dy) {
			return true
		}
	}
	return false
}

// startLazyFramesNear starts loading="lazy" frames that the scroll position brought near the viewport.
func (t *Tab) startLazyFramesNear() {
	_, vh, _, _, _ := t.Viewport()
	sy := float64(t.ScrollY())
	for _, f := range t.frameList() {
		if !f.lazy || f.isStarted() {
			continue
		}
		f.mu.Lock()
		by, bh := f.by, f.bh
		f.mu.Unlock()
		if by < sy+float64(vh)+lazyMargin && by+bh > sy-lazyMargin {
			f.start()
		}
	}
}

// needsOriginOverride: documents whose origin the page cannot derive from its URL.
func (t *Tab) needsOriginOverride() bool {
	return t.win.parent != nil || t.win.opener.Load() != nil || t.opaqueOrigin()
}

func relHas(rel, tok string) bool {
	for _, f := range strings.Fields(strings.ToLower(rel)) {
		if f == tok {
			return true
		}
	}
	return false
}

// anchorNoOpener: links to a new window get no opener reference unless they ask for it
// with rel=opener (the default of target=_blank in current browsers); rel=noopener and
// rel=noreferrer always cut it.
func anchorNoOpener(target, rel string) bool {
	if relHas(rel, "noopener") || relHas(rel, "noreferrer") {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(target), "_blank") && !relHas(rel, "opener")
}

// submitFormTargeted submits a form honouring its target attribute and the frame's sandbox.
func (t *Tab) submitFormTargeted(d *fnet.Doc, bid int64, method, action string, values map[string]string) error {
	if sb := t.win.sbx; sb != nil && !sb.forms {
		t.LogConsole("warning", "Blocked form submission in a sandboxed frame (allow-forms is not set)")
		return nil
	}
	fid := d.FormOf(bid)
	if fid == 0 {
		fid = bid
	}
	target, _ := d.GetAttr(fid, "target")
	target = strings.TrimSpace(target)
	if target == "" || strings.EqualFold(target, "_self") {
		return t.submitForm(method, action, values)
	}
	abs := t.URL()
	if action != "" {
		if u, err := resolveURL(abs, action); err == nil {
			abs = u
		}
	}
	rel, _ := d.GetAttr(fid, "rel")
	s := navSpec{target: target, noOpener: anchorNoOpener(target, rel), noReferrer: relHas(rel, "noreferrer")}
	if strings.EqualFold(method, "post") {
		s.rawURL = abs
		vals := values
		s.post = func(dest *Tab) error { return dest.postForm(abs, vals) }
	} else {
		u := mustParse(abs)
		if u == nil {
			return nil
		}
		q := u.Query()
		for k, v := range values {
			q.Set(k, v)
		}
		u.RawQuery = q.Encode()
		s.rawURL = u.String()
	}
	t.navigateTarget(s)
	return nil
}
