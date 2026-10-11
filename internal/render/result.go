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
	rc := bx.Border
	if bx.HasTransform {
		rc = transformRect(rc, bx.Transform)
	}
	for p := n.Parent; p != nil; p = p.Parent {
		pb := r.Boxes[p]
		if pb != nil && pb.HasTransform {
			rc = transformRect(rc, pb.Transform)
		}
	}
	return rc, true
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
