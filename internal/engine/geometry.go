package engine

import (
	"math"

	fnet "github.com/sweefbrowser/sweef/internal/net"
	"github.com/sweefbrowser/sweef/internal/render/layout"
	"github.com/sweefbrowser/sweef/internal/render/style"
	"golang.org/x/net/html"
)

// geometry.go answers CSSOM View queries (getBoundingClientRect,
// getClientRects, client*/offset*/scroll* metrics, window scrolling) from
// the layout engine's fragment tree. With the schematic flow layout it
// falls back to its integer boxes.

// geomOf returns the page-space geometry of element id.
func (t *Tab) geomOf(d *fnet.Doc, id int64) map[string]any {
	n := d.NodeByID(id)
	empty := map[string]any{"x": 0.0, "y": 0.0, "w": 0.0, "h": 0.0, "rects": []any{}, "rendered": false}
	if n == nil {
		return empty
	}
	l := d.Layout(d.LayoutWidth())
	r, _ := l.Engine.(*layout.Result)
	if r == nil {
		b, ok := l.Boxes[n]
		if !ok {
			return empty
		}
		x, y, w, h := float64(b[0]), float64(b[1]), float64(b[2]), float64(b[3])
		return map[string]any{"x": x, "y": y, "w": w, "h": h, "rects": []any{[]any{x, y, w, h}},
			"cl": 0.0, "ct": 0.0, "cw": w, "ch": h, "sw": w, "sh": h, "ox": x, "oy": y, "op": int64(0), "rendered": true, "inline": false}
	}
	frags := r.Fragments[n]
	if len(frags) == 0 {
		return empty
	}
	x, y, w, h, _ := r.BorderBox(n)
	// Scrolled ancestors move the element; fixed ones pin it to the
	// viewport (the caller then skips the window scroll).
	dx, dy, fixed := 0.0, 0.0, false
	for _, f := range frags {
		fixed = fixed || f.Fixed
	}
	for p := n.Parent; p != nil && p.Type == html.ElementNode; p = p.Parent {
		sx, sy := d.ScrollOffset(p)
		dx, dy = dx+sx, dy+sy
		if st := styleOf(r, p); st != nil && st.Position == style.PosFixed {
			fixed = true
		}
	}
	x, y = x-dx, y-dy
	rects := make([]any, 0, len(frags))
	for _, f := range frags {
		rects = append(rects, []any{f.X - dx, f.Y - dy, f.W, f.H})
	}
	f := frags[0]
	inline := f.Kind == layout.KindInline
	out := map[string]any{"x": x, "y": y, "w": w, "h": h, "rects": rects, "rendered": true, "inline": inline, "fixed": fixed}
	if inline {
		out["cl"], out["ct"], out["cw"], out["ch"] = 0.0, 0.0, 0.0, 0.0
	} else {
		_, _, pw, ph := f.PaddingBox()
		out["cl"], out["ct"], out["cw"], out["ch"] = f.Border[3], f.Border[0], math.Floor(pw), math.Floor(ph)
	}
	sw, sh := 0.0, 0.0
	if !inline {
		_, _, pw, ph := f.PaddingBox()
		sw, sh = math.Max(pw, f.ScrollW), math.Max(ph, f.ScrollH)
	}
	if n.Data == "html" || n.Data == "body" && isRootScroller(d, n) {
		sw, sh = math.Max(sw, r.DocW), math.Max(sh, r.DocH)
	}
	out["sw"], out["sh"] = sw, sh
	op, ox, oy := offsetParent(d, r, n, x, y)
	out["op"], out["ox"], out["oy"] = op, ox, oy
	return out
}

func isRootScroller(d *fnet.Doc, n *html.Node) bool {
	return n.Parent != nil && n.Parent.Data == "html"
}

// offsetParent implements CSSOM View's offsetParent and offsetTop/Left.
func offsetParent(d *fnet.Doc, r *layout.Result, n *html.Node, x, y float64) (int64, float64, float64) {
	st := styleOf(r, n)
	if n.Data == "body" || n.Data == "html" || st == nil || st.Position == style.PosFixed {
		return 0, x, y
	}
	for p := n.Parent; p != nil && p.Type == html.ElementNode; p = p.Parent {
		ps := styleOf(r, p)
		if ps == nil {
			continue
		}
		if ps.Position != style.PosStatic || p.Data == "body" ||
			(st.Position == style.PosStatic && (p.Data == "td" || p.Data == "th" || p.Data == "table")) ||
			len(ps.Transform) > 0 {
			if p.Data == "body" {
				return d.IDOf(p), x, y
			}
			px, py, _, _, ok := r.BorderBox(p)
			if !ok {
				return d.IDOf(p), x, y
			}
			pf := r.Fragments[p][0]
			return d.IDOf(p), x - px - pf.Border[3], y - py - pf.Border[0]
		}
	}
	return 0, x, y
}

func styleOf(r *layout.Result, n *html.Node) *style.ComputedStyle {
	if fs := r.Fragments[n]; len(fs) > 0 {
		return fs[0].Style
	}
	return nil
}

// scrollWindow sets the window scroll position, clamped to the document,
// and returns the position actually used.
func (t *Tab) scrollWindow(x, y float64) (float64, float64) {
	t.mu.RLock()
	d := t.dom
	vw, vh := float64(t.vpW), float64(t.vpH)
	t.mu.RUnlock()
	maxX, maxY := 0.0, 0.0
	if d != nil {
		l := d.Layout(d.LayoutWidth())
		dw, dh := float64(l.Width), float64(l.Height)
		if r, ok := l.Engine.(*layout.Result); ok {
			dw, dh = r.DocW, r.DocH
		}
		maxX, maxY = math.Max(0, dw-vw), math.Max(0, dh-vh)
	}
	x = math.Max(0, math.Min(maxX, x))
	y = math.Max(0, math.Min(maxY, y))
	t.mu.Lock()
	t.scrollX, t.scrollY = x, y
	t.mu.Unlock()
	return x, y
}

// WindowScroll returns the window scroll position.
func (t *Tab) WindowScroll() (float64, float64) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.scrollX, t.scrollY
}

// scrollElement sets a scroll container's offsets, clamped to its
// scrollable overflow; returns the offsets used.
func (t *Tab) scrollElement(d *fnet.Doc, id int64, x, y float64) (float64, float64) {
	g := t.geomOf(d, id)
	sw, _ := g["sw"].(float64)
	sh, _ := g["sh"].(float64)
	cw, _ := g["cw"].(float64)
	ch, _ := g["ch"].(float64)
	x = math.Max(0, math.Min(math.Max(0, sw-cw), x))
	y = math.Max(0, math.Min(math.Max(0, sh-ch), y))
	d.SetScrollOffset(id, x, y)
	t.renderGen.Add(1)
	return x, y
}
