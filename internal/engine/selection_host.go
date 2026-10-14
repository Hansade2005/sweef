package engine

import (
	"golang.org/x/net/html"

	"github.com/sweefbrowser/sweef/internal/render"
)

// selection_host.go: what page script's Selection and Range need from the layout.

// RangeRects returns the rectangles (document coordinates, CSS px) of the text between two boundary points, one
// per line box, for Range.getClientRects.
func (t *Tab) RangeRects(sid int64, so int, eid int64, eo int) [][4]float64 {
	_, ix, d := t.freshTextIndex()
	if ix == nil {
		return nil
	}
	sn, en := d.NodeByID(sid), d.NodeByID(eid)
	if sn == nil || en == nil {
		return nil
	}
	a, b := selPoint{sn, so}, selPoint{en, eo}
	if comparePoints(a, b) > 0 {
		a, b = b, a
	}
	var out [][4]float64
	textsInRange(a, b, func(n *html.Node, from, to int) {
		for _, r := range ix.RectsFor(n, from, to) {
			out = append(out, [4]float64{r.X, r.Y, r.W, r.H})
		}
	})
	return out
}

// PosAtPoint is the boundary point nearest to the document point (x, y) on selectable text, for
// caretPositionFromPoint; ok is false on a page without text.
func (t *Tab) PosAtPoint(x, y float64) (id int64, off int, ok bool) {
	_, ix, d := t.freshTextIndex()
	if ix == nil {
		return 0, 0, false
	}
	p, _, _, found := ix.PosAt(x, y, true)
	if !found {
		return 0, 0, false
	}
	return d.IDOf(p.Node), runeToU16(p.Node.Data, p.Off), true
}

// CaretRect is the rectangle of the caret at a text boundary (document coordinates).
func (t *Tab) CaretRect(id int64, off int) (r [4]float64, ok bool) {
	_, ix, d := t.freshTextIndex()
	if ix == nil {
		return
	}
	n := d.NodeByID(id)
	if n == nil {
		return
	}
	str := n.Data
	if isTextControl(n) {
		str = ctlValue(d, n)
	}
	roff := u16ToRune(str, off)
	box, stop := visualPos(ix, selPoint{n, roff}, true)
	if box == nil {
		return
	}
	x := box.X(stop)
	return [4]float64{x, box.Rect.Y, 1, box.Rect.H}, true
}

// SelRowsOf is the rows (document y, CSS px) of the text of the selection, for scrolling it into view; ok is
// false when nothing is selected or the selection is not on the page.
func (t *Tab) SelFocusRect() (r [4]float64, ok bool) {
	info := t.SelGet()
	if !info.Has {
		return
	}
	return t.CaretRect(info.FocusID, info.FocusOff)
}

var _ = render.BreakNone
