package render

import (
	"math"
	"sort"

	"golang.org/x/net/html"
)

// textrec.go: where the text of a page is on the page.
//
// While painting, layout records every run of text it draws as a TextBox: which DOM text node (or form control)
// it came from, which characters of it, and the horizontal position of every caret stop. That is all that
// selection needs: finding the character under a point, the rectangles a range of characters covers, and the
// places to put highlights and a caret in the display list. The records are kept in the Result, in paint order.

// TextBox is one run of text on one line.
type TextBox struct {
	Node *html.Node // the text node, or the form control whose value this is
	Ctl  bool       // text of a form control (offsets are into its value, in runes)
	// Rect is the line: X is where the run starts (its left edge), Y the top of the line box, H its height, W the
	// run's advance.
	Rect Rect
	Base float64 // baseline y
	// Start is the offset (in runes of the node's text) of the first caret stop. Src, when not nil, holds the
	// offset of every caret stop (the text was collapsed or transformed: several source characters can map to one
	// shown one); otherwise stop i is at Start+i.
	Start int
	Src   []int32
	// Xs is the x of every caret stop, relative to Rect.X, in logical order: len(Xs) is the number of characters
	// plus one. In a right-to-left run Xs falls from the right edge of the run to its left edge.
	Xs []float32
	// Op is the index in Result.List where the run's text op is (or would be, for an empty control): highlights
	// and carets are inserted before it.
	Op       int
	NoSelect bool // user-select: none
	RTL      bool
	LineEnd  bool // last run of its line
	// Clip is the intersection of the clip rectangles in force (document coordinates, before transforms), when
	// HasClip; M the transform in force, when HasM.
	Clip    Rect
	HasClip bool
	M       [6]float64
	HasM    bool
	// Order is the position in paint order; boxes of one node are ordered by Start.
	Order int
	// Block is the element whose inline content this run is part of (the paragraph); runs of different blocks are
	// separated by line breaks when text is copied. Break says how the line ends: softly (wrapped), or forced.
	Block *html.Node
	Break int
	// CollapseWS: runs of white space in the source were collapsed (white-space: normal); KeepNL: but newlines
	// stay (pre-line).
	CollapseWS, KeepNL bool
}

// How a line of text ends.
const (
	BreakNone   = 0
	BreakSoft   = 1 // wrapped to the next line
	BreakForced = 2 // <br> or a newline in preserved white space
)

// Stops is the number of caret stops of the run.
func (t *TextBox) Stops() int { return len(t.Xs) }

// Off returns the source offset (runes) of caret stop i.
func (t *TextBox) Off(i int) int {
	if i < 0 {
		i = 0
	}
	if i >= len(t.Xs) {
		i = len(t.Xs) - 1
	}
	if t.Src != nil && i < len(t.Src) {
		return int(t.Src[i])
	}
	return t.Start + i
}

// FirstOff and LastOff are the source offsets of the first and last caret stop.
func (t *TextBox) FirstOff() int { return t.Off(0) }
func (t *TextBox) LastOff() int  { return t.Off(len(t.Xs) - 1) }

// StopAt returns the caret stop whose source offset is nearest to off: exact when off belongs to the run, the
// nearer end when it does not.
func (t *TextBox) StopAt(off int) int {
	n := len(t.Xs)
	if n == 0 {
		return 0
	}
	if t.Src == nil {
		i := off - t.Start
		if i < 0 {
			return 0
		}
		if i >= n {
			return n - 1
		}
		return i
	}
	// Src is non-decreasing
	i := sort.Search(n, func(k int) bool { return int(t.Src[k]) >= off })
	if i >= n {
		return n - 1
	}
	return i
}

// Contains reports whether the source offset lies within the run (its first stop up to its last).
func (t *TextBox) Contains(off int) bool { return off >= t.FirstOff() && off <= t.LastOff() }

// X is the document x of caret stop i.
func (t *TextBox) X(i int) float64 {
	if i < 0 {
		i = 0
	}
	if i >= len(t.Xs) {
		i = len(t.Xs) - 1
	}
	if i < 0 {
		return t.Rect.X
	}
	return t.Rect.X + float64(t.Xs[i])
}

// SpanRect is the rectangle of the characters between caret stops i and j (any order), as a line-high box.
func (t *TextBox) SpanRect(i, j int) Rect {
	x0, x1 := t.X(i), t.X(j)
	if x0 > x1 {
		x0, x1 = x1, x0
	}
	return Rect{x0, t.Rect.Y, x1 - x0, t.Rect.H}
}

// CaretFor returns the caret stop nearest to document x on this run.
func (t *TextBox) CaretFor(x float64) int {
	best, bd := 0, math.MaxFloat64
	for i := range t.Xs {
		d := math.Abs(t.X(i) - x)
		if d <= bd { // ties go to the later stop: a click past the end of a line lands after its hanging blanks
			best, bd = i, d
		}
	}
	return best
}

// visibleAt reports whether (x, y) is inside the clip of the run (the run may be scrolled out of its container).
func (t *TextBox) visibleAt(x, y float64) bool {
	if t.HasM {
		inv, ok := invert(t.M)
		if !ok {
			return false
		}
		x, y = inv[0]*x+inv[2]*y+inv[4], inv[1]*x+inv[3]*y+inv[5]
	}
	if t.HasClip {
		c := t.Clip
		if x < c.X || x >= c.X+c.W || y < c.Y || y >= c.Y+c.H {
			return false
		}
	}
	return true
}

// LocalPoint maps a document point into the space the run was recorded in (a no-op without transforms).
func (t *TextBox) LocalPoint(x, y float64) (float64, float64) { return t.local(x, y) }

// Result is the layout result the index was built from.
func (ix *TextIndex) Result() *Result { return ix.res }

// local maps a document point into the space the run was recorded in.
func (t *TextBox) local(x, y float64) (float64, float64) {
	if t.HasM {
		if inv, ok := invert(t.M); ok {
			return inv[0]*x + inv[2]*y + inv[4], inv[1]*x + inv[3]*y + inv[5]
		}
	}
	return x, y
}

// ---- queries ------------------------------------------------------------------------------------------

// TextIndex orders the runes of a Result for lookups. Build it once per Result.
type TextIndex struct {
	res    *Result
	byY    []int // indexes into res.Text ordered by line top
	byNode map[*html.Node][]int
}

// NewTextIndex indexes res.Text.
func NewTextIndex(res *Result) *TextIndex {
	ix := &TextIndex{res: res, byNode: map[*html.Node][]int{}}
	ix.byY = make([]int, len(res.Text))
	for i := range res.Text {
		ix.byY[i] = i
		n := res.Text[i].Node
		ix.byNode[n] = append(ix.byNode[n], i)
	}
	sort.SliceStable(ix.byY, func(a, b int) bool { return res.Text[ix.byY[a]].Rect.Y < res.Text[ix.byY[b]].Rect.Y })
	for _, l := range ix.byNode {
		sort.SliceStable(l, func(a, b int) bool { return res.Text[l[a]].FirstOff() < res.Text[l[b]].FirstOff() })
	}
	return ix
}

// Boxes returns the runs of node n in order of their position in the node's text.
func (ix *TextIndex) Boxes(n *html.Node) []*TextBox {
	l := ix.byNode[n]
	out := make([]*TextBox, len(l))
	for i, k := range l {
		out[i] = &ix.res.Text[k]
	}
	return out
}

// Pos is a place in text: a node and a source offset in runes.
type Pos struct {
	Node *html.Node
	Off  int
}

// PosAt finds the caret position nearest to the document point (x, y): the line whose box contains y (or the
// nearest one above or below), and on it the caret stop nearest x. runs only counts runs that may be selected
// (user-select) unless all is set. ok is false when the page has no text.
func (ix *TextIndex) PosAt(x, y float64, all bool) (p Pos, box *TextBox, stop int, ok bool) {
	res := ix.res
	if len(res.Text) == 0 {
		return
	}
	// the first run whose line bottom is below y, by binary search on the top (lines are at most a few hundred px
	// tall, so scan back a little)
	i := sort.Search(len(ix.byY), func(k int) bool { return res.Text[ix.byY[k]].Rect.Y > y })
	lo := i - 1
	var cand []*TextBox
	// runs whose vertical extent contains y
	for k := lo; k >= 0 && k > lo-64; k-- {
		t := &res.Text[ix.byY[k]]
		if t.NoSelect && !all {
			continue
		}
		ly := t.local1(x, y)
		if ly >= t.Rect.Y && ly < t.Rect.Y+t.Rect.H && t.visibleAt(x, y) {
			cand = append(cand, t)
		}
	}
	if len(cand) == 0 {
		// between lines or outside the text: the nearest line above or below
		var best *TextBox
		bd := math.MaxFloat64
		consider := func(k int) {
			if k < 0 || k >= len(ix.byY) {
				return
			}
			t := &res.Text[ix.byY[k]]
			if t.NoSelect && !all {
				return
			}
			if t.HasClip && (y < t.Clip.Y-2 && false) {
				return
			}
			d := 0.0
			if y < t.Rect.Y {
				d = t.Rect.Y - y
			} else if y >= t.Rect.Y+t.Rect.H {
				d = y - (t.Rect.Y + t.Rect.H)
			}
			if d < bd {
				best, bd = t, d
			}
		}
		for k := i - 1; k >= 0 && k > i-32; k-- {
			consider(k)
		}
		for k := i; k < len(ix.byY) && k < i+32; k++ {
			consider(k)
		}
		if best == nil {
			return
		}
		cand = append(cand, best)
		// all runs on the same line as the best one
		for k := range res.Text {
			t := &res.Text[k]
			if t != best && (!t.NoSelect || all) && t.Rect.Y == best.Rect.Y && t.Rect.H == best.Rect.H {
				cand = append(cand, t)
			}
		}
	}
	// among the runs of the line, the one containing x, else the nearest horizontally
	best := cand[0]
	bd := math.MaxFloat64
	for _, t := range cand {
		lx, _ := t.local(x, y)
		l, r := t.X(0), t.X(len(t.Xs)-1)
		if l > r {
			l, r = r, l
		}
		d := 0.0
		if lx < l {
			d = l - lx
		} else if lx > r {
			d = lx - r
		}
		if d < bd || d == bd && t.Order > best.Order {
			best, bd = t, d
		}
	}
	lx, _ := best.local(x, y)
	stop = best.CaretFor(lx)
	return Pos{best.Node, best.Off(stop)}, best, stop, true
}

func (t *TextBox) local1(x, y float64) float64 {
	_, ly := t.local(x, y)
	return ly
}

// RectsFor returns the line rectangles (document coordinates, before transforms) covered by the characters
// [from, to) of node n.
func (ix *TextIndex) RectsFor(n *html.Node, from, to int) []Rect {
	var out []Rect
	for _, t := range ix.Boxes(n) {
		a, b := t.FirstOff(), t.LastOff()
		lo, hi := max(from, a), min(to, b)
		if hi < lo || (hi == lo && !(from == to && from >= a && from <= b)) {
			continue
		}
		if lo == hi && from != to {
			continue
		}
		r := t.SpanRect(t.StopAt(lo), t.StopAt(hi))
		out = append(out, r)
	}
	return out
}

// Highlight is one part of the page to mark in the display list.
type Highlight struct {
	Node        *html.Node
	From, To    int // source offsets, runes; From == To marks nothing (see Caret)
	Fill        Color
	Text        Color // glyphs inside the highlight are drawn in this colour when HasText
	HasText     bool
	Newline     bool // the highlight goes on after this node's last run: mark the end of line as well
	ControlOnly bool
}

// Caret is a text cursor to draw.
type Caret struct {
	Node  *html.Node
	Off   int
	Color Color
}

// ApplyHighlights returns list with the highlights painted into it: a filled rectangle before the run's text op
// for every span, and the glyphs inside the span in the highlight's text colour. Carets are inserted the same
// way. The list is not modified.
func ApplyHighlights(list []Op, ix *TextIndex, hl []Highlight, carets []Caret) []Op {
	if len(hl) == 0 && len(carets) == 0 {
		return list
	}
	type edit struct {
		before []Op
		recol  []recolor
	}
	edits := map[int]*edit{}
	add := func(op int) *edit {
		e := edits[op]
		if e == nil {
			e = &edit{}
			edits[op] = e
		}
		return e
	}
	for _, h := range hl {
		for _, t := range ix.Boxes(h.Node) {
			if t.NoSelect && !t.Ctl {
				continue
			}
			if h.ControlOnly != t.Ctl && h.ControlOnly {
				continue
			}
			a, b := t.FirstOff(), t.LastOff()
			lo, hi := max(h.From, a), min(h.To, b)
			if hi < lo {
				continue
			}
			var r Rect
			switch {
			case hi > lo:
				r = t.SpanRect(t.StopAt(lo), t.StopAt(hi))
			case h.Newline && t.LineEnd && h.To > b && lo == hi && lo == b:
				r = Rect{t.X(len(t.Xs) - 1), t.Rect.Y, 0, t.Rect.H}
			default:
				continue
			}
			if h.Newline && t.LineEnd && h.To > b && hi == b {
				r.W += t.Rect.H * 0.3 // the line break: about the width of a space
				if t.RTL {
					r.X -= t.Rect.H * 0.3
				}
			}
			if r.W <= 0 || r.H <= 0 {
				continue
			}
			e := add(t.Op)
			e.before = append(e.before, FillRect{Rect: r, Color: h.Fill})
			if h.HasText && hi > lo {
				e.recol = append(e.recol, recolor{x0: r.X, x1: r.X + r.W, col: h.Text})
			}
		}
	}
	for _, c := range carets {
		bx := ix.Boxes(c.Node)
		var t *TextBox
		for _, k := range bx {
			if k.Contains(c.Off) {
				if t == nil || k.LastOff() != c.Off || k.FirstOff() == c.Off {
					t = k
				}
				if k.FirstOff() <= c.Off && c.Off < k.LastOff() {
					t = k
					break
				}
			}
		}
		if t == nil {
			continue
		}
		x := t.X(t.StopAt(c.Off))
		e := add(t.Op)
		e.before = append(e.before, FillRect{Rect: Rect{x - 0.5, t.Rect.Y, 1, t.Rect.H}, Color: c.Color})
	}
	if len(edits) == 0 {
		return list
	}
	out := make([]Op, 0, len(list)+len(edits)*2)
	for i, op := range list {
		e := edits[i]
		if e == nil {
			out = append(out, op)
			continue
		}
		out = append(out, e.before...)
		if tr, ok := op.(TextRun); ok && len(e.recol) > 0 {
			out = append(out, splitRun(tr, e.recol)...)
		} else {
			out = append(out, op)
		}
	}
	// ops recorded just past the end of the list (an empty control last on the page)
	if e := edits[len(list)]; e != nil {
		out = append(out, e.before...)
	}
	return out
}

type recolor struct {
	x0, x1 float64
	col    Color
}

// splitRun cuts a text run into pieces at the edges of the recoloured spans, so the glyphs inside a span can be
// drawn in another colour. Glyph positions are relative to the run's x, so the pieces keep them; a glyph belongs
// to a span when its centre does.
func splitRun(tr TextRun, rc []recolor) []Op {
	colorOf := func(g Glyph) (Color, int) {
		cx := tr.X + g.X + 0.25*tr.Size // a point well inside the glyph's cell, whatever its width
		for i, r := range rc {
			if cx >= r.x0 && cx < r.x1 {
				return r.col, i + 1
			}
		}
		return tr.Color, 0
	}
	var out []Op
	start := 0
	cur := -1
	flush := func(end int) {
		if end <= start {
			return
		}
		p := tr
		p.Glyphs = tr.Glyphs[start:end]
		if cur > 0 {
			p.Color = rc[cur-1].col
			p.Shadows = nil
		}
		// decorations ride on the first piece only, over the whole width
		if start > 0 {
			p.Underline, p.Overline, p.LineThrough = false, false, false
		}
		out = append(out, p)
	}
	for i, g := range tr.Glyphs {
		_, k := colorOf(g)
		if cur == -1 {
			cur = k
		}
		if k != cur {
			flush(i)
			start, cur = i, k
		}
	}
	flush(len(tr.Glyphs))
	if len(out) == 0 {
		return []Op{tr}
	}
	return out
}
