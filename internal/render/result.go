package render

import (
	"math"

	"golang.org/x/net/html"
)

// ClientRect returns the bounding rectangle of n as getBoundingClientRect
// reports it at scroll offset 0: the union of its fragments with the
// transforms of n and all its ancestors applied. ok is false for nodes
// without a box (display:none and descendants).
func (r *Result) ClientRect(n *html.Node) (Rect, bool) {
	bx := r.Boxes[n]
	if bx == nil || bx.Display == "none" {
		return Rect{}, false
	}
	if pts, ok := r.projectChain(n, rectCorners(bx.Border)); ok {
		return boundsOf(pts), true
	}
	return r.rectOfFrag(n, bx.Border)
}

// rectOfFrag maps one rectangle of n through the 2D transform chain.
func (r *Result) rectOfFrag(n *html.Node, rc Rect) (Rect, bool) {
	bx := r.Boxes[n]
	// the four corners go through every transform of the ancestor chain; the
	// bounding box is taken once at the end (rotations of rotations compose)
	pts := [4][2]float64{{rc.X, rc.Y}, {rc.Right(), rc.Y}, {rc.X, rc.Bottom()}, {rc.Right(), rc.Bottom()}}
	moved := false
	apply := func(m [6]float64) {
		for i := range pts {
			x, y := pts[i][0], pts[i][1]
			pts[i] = [2]float64{m[0]*x + m[2]*y + m[4], m[1]*x + m[3]*y + m[5]}
		}
		moved = true
	}
	if bx.HasTransform {
		apply(bx.Transform)
	}
	for p := n.Parent; p != nil; p = p.Parent {
		pb := r.Boxes[p]
		if pb != nil && pb.HasTransform {
			apply(pb.Transform)
		}
	}
	if !moved {
		return rc, true
	}
	x0, y0, x1, y1 := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, q := range pts {
		x0, x1 = math.Min(x0, q[0]), math.Max(x1, q[0])
		y0, y1 = math.Min(y0, q[1]), math.Max(y1, q[1])
	}
	return Rect{x0, y0, x1 - x0, y1 - y0}, true
}

func transformRect(rc Rect, m [6]float64) Rect {
	xs := [4]float64{rc.X, rc.Right(), rc.X, rc.Right()}
	ys := [4]float64{rc.Y, rc.Y, rc.Bottom(), rc.Bottom()}
	x0, y0, x1, y1 := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for i := 0; i < 4; i++ {
		x := m[0]*xs[i] + m[2]*ys[i] + m[4]
		y := m[1]*xs[i] + m[3]*ys[i] + m[5]
		x0, x1 = math.Min(x0, x), math.Max(x1, x)
		y0, y1 = math.Min(y0, y), math.Max(y1, y)
	}
	return Rect{x0, y0, x1 - x0, y1 - y0}
}
