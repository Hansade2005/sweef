package engine

// frames_input.go: input into frames. A frame is a viewport inside another page, so a pointer position in the
// parent is routed by the parent's own hit test (render.Result.HitAt, which already honours paint order, clipping,
// ancestor transforms and pointer-events) and translated into the frame's document space. Keyboard input goes to
// the document that has focus: the leaf of the focus chain that the last click set up.

import (
	"math"
	"strings"

	fnet "github.com/sweefbrowser/sweef/internal/net"
)

// frameAt reports the frame under the document point (x, y) of this tab and the point translated to that frame's
// document space, or nil.
func (t *Tab) frameAt(x, y float64) (*Tab, float64, float64) {
	t.fr.mu.Lock()
	n := len(t.fr.kids)
	t.fr.mu.Unlock()
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
	k := t.frameKid(doc.IDOf(hit))
	if k == nil || k.closed.Load() {
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
	return k, lx, ly + float64(k.frameScrollY(int(math.Round(c.H))))
}

// FrameHit finds the document that receives a pointer event at the document point (x, y): the tab itself, or the
// deepest frame under the point with the point translated into its document space.
func (t *Tab) FrameHit(x, y int) (leaf *Tab, lx, ly int) {
	cur, fx, fy := t, float64(x), float64(y)
	for depth := 0; depth < maxFrameDepth+1; depth++ {
		k, ax, ay := cur.frameAt(fx, fy)
		if k == nil {
			break
		}
		cur, fx, fy = k, ax, ay
	}
	return cur, int(math.Floor(fx)), int(math.Floor(fy))
}

// FocusFrame makes leaf (this tab or one of its nested frames) the document that receives keyboard input, blurring
// the element of the document that had it.
func (t *Tab) FocusFrame(leaf *Tab) {
	prev := t.FocusedTab()
	var chain []*Tab
	for c := leaf; c != nil && c != t; {
		chain = append([]*Tab{c}, chain...)
		if !c.isFrame() {
			break
		}
		c = c.bc.parent
	}
	cur := t
	for _, c := range chain {
		cur.win.focusFrame.Store(c)
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
		k := cur.win.focusFrame.Load()
		if k == nil || k.closed.Load() || !k.isFrame() || k.bc.parent != cur {
			return cur
		}
		cur = k
	}
	return cur
}

// blurDocument blurs the focused element of a document that lost focus.
func (t *Tab) blurDocument() {
	if t.closed.Load() {
		return
	}
	t.mu.RLock()
	has := t.js != nil
	t.mu.RUnlock()
	if !has {
		return
	}
	_, _ = t.Evaluate(`(function(){var e=document.activeElement;if(e&&e!==document.body&&e.blur)e.blur();})()`)
}

// ScrollFrameBy scrolls a frame document by dy CSS px inside its box and reports whether it moved (false at either
// end, so the caller can scroll the page instead).
func (t *Tab) ScrollFrameBy(dy int) bool {
	if !t.isFrame() || dy == 0 {
		return false
	}
	t.win.fp.mu.Lock()
	h := t.win.fp.h
	t.win.fp.mu.Unlock()
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
	t.mu.RLock()
	has := t.js != nil
	t.mu.RUnlock()
	if has {
		_, _ = t.Evaluate(`typeof __sweefScrollTo==='function'&&__sweefScrollTo(` + itoa(y) + `)`)
	}
	t.contentChanged()
}

// WheelAt routes a wheel gesture at the document point (x, y) to the innermost frame under it that can still
// scroll; false means no frame took it and the page scrolls.
func (t *Tab) WheelAt(x, y, dx, dy int) bool {
	leaf, _, _ := t.FrameHit(x, y)
	for c := leaf; c != nil && c != t && c.isFrame(); c = c.bc.parent {
		if c.ScrollFrameBy(dy) {
			return true
		}
	}
	return false
}

func relHas(rel, tok string) bool {
	for _, f := range strings.Fields(strings.ToLower(rel)) {
		if f == tok {
			return true
		}
	}
	return false
}

// anchorNoOpener: links to a new window get no opener reference unless they ask for it with rel=opener (the default
// of target=_blank in current browsers); rel=noopener and rel=noreferrer always cut it.
func anchorNoOpener(target, rel string) bool {
	if relHas(rel, "noopener") || relHas(rel, "noreferrer") {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(target), "_blank") && !relHas(rel, "opener")
}

// submitFormTargeted submits a form honouring its target attribute and the frame's sandbox.
func (t *Tab) submitFormTargeted(d *fnet.Doc, bid int64, method, action string, values map[string]string) error {
	if f := t.sandboxFlags(); f&sbSandboxed != 0 && f&sbForms == 0 {
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
