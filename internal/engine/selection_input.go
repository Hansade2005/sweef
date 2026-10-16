package engine

import (
	"math"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"

	fnet "github.com/sweefbrowser/sweef/internal/net"
	"github.com/sweefbrowser/sweef/internal/render"
)

// selection_input.go: the selection gestures of the mouse and the movement of the caret, both of which need to
// know where the text is (the layout's text records).

const (
	granChar  = 0
	granWord  = 1
	granBlock = 2
)

func (t *Tab) docAndEnv() (*fnet.Doc, *tabEnv) {
	t.mu.RLock()
	d := t.dom
	t.mu.RUnlock()
	return d, t.selEnv()
}

// controlAt returns the text control under the page point, if the point is on one.
func controlAt(res *render.Result, x, y float64) *html.Node {
	if res.Hit == nil {
		return nil
	}
	n := res.Hit(x, y)
	if n != nil && isTextControl(n) {
		return n
	}
	return nil
}

// ctlOffsetAt is the offset (runes of the value) in control c nearest the page point.
func ctlOffsetAt(ix *render.TextIndex, c *html.Node, x, y float64) (int, bool) {
	boxes := ix.Boxes(c)
	if len(boxes) == 0 {
		return 0, false
	}
	best := boxes[0]
	bd := math.MaxFloat64
	for _, b := range boxes {
		d := 0.0
		if y < b.Rect.Y {
			d = b.Rect.Y - y
		} else if y >= b.Rect.Y+b.Rect.H {
			d = y - (b.Rect.Y + b.Rect.H)
		}
		if d < bd {
			best, bd = b, d
		}
	}
	lx, _ := best.LocalPoint(x, y)
	if len(best.Xs) > 0 && lx < best.X(0) {
		return best.Off(0), true
	}
	return best.Off(best.CaretFor(lx)), true
}

// SelectDown starts a selection gesture at the page point (x, y): a click puts the caret there, a double click
// selects the word, a triple click the paragraph, and with shift the selection is extended from where it was.
// It reports whether the gesture is the selection's (false: the point is not on selectable text, for example on
// an element with user-select: none, and the selection was cleared).
func (t *Tab) SelectDown(x, y float64, clicks int, shift bool) bool {
	res, ix, d := t.textIndex()
	env := t.selEnv()
	if res == nil || env == nil {
		return false
	}
	s := &env.sel
	// a text control: the selection lives in the control
	if c := controlAt(res, x, y); c != nil {
		off, ok := ctlOffsetAt(ix, c, x, y)
		if !ok {
			off = len([]rune(ctlValue(d, c)))
		}
		v := ctlValue(d, c)
		rs := []rune(v)
		a, f := off, off
		gran := granChar
		switch {
		case clicks == 2:
			a, f = wordAt(rs, off)
			gran = granWord
		case clicks >= 3:
			a, f = lineBounds(ix, c, off, len(rs))
			gran = granBlock
		case shift:
			if st, en, _, ok := t.CtlSelGet(d.IDOf(c)); ok {
				// extend from the end that stays
				anchor := runeFromU16(v, st)
				if u16 := runeFromU16(v, en); off < anchor {
					_ = u16
				}
				a = anchor
			}
		}
		s.mu.Lock()
		s.down, s.gran, s.moved, s.gctl = true, gran, false, c
		s.origA, s.origF = selPoint{c, a}, selPoint{c, f} // rune offsets for a control
		s.mu.Unlock()
		dir := 1
		if f < a {
			dir = -1
		}
		t.ctlSelSetNode(env, c, runeToU16(v, a), runeToU16(v, f), dir)
		t.setDocSelectionEmpty(env)
		return true
	}
	if res.Hit != nil {
		if n := res.Hit(x, y); n != nil {
			if b := res.Boxes[n]; b != nil && b.Style != nil && b.Style.UserSelect == "none" {
				t.SelClear()
				return false
			}
		}
	}
	pos, box, stop, ok := ix.PosAt(x, y, false)
	if !ok {
		return false
	}
	p := selPoint{pos.Node, runeToU16(pos.Node.Data, pos.Off)}
	_ = box
	_ = stop
	s.mu.Lock()
	s.down, s.moved, s.gctl = true, false, nil
	switch {
	case clicks == 2:
		rs := []rune(pos.Node.Data)
		a, f := wordAt(rs, pos.Off)
		s.a, s.f = selPoint{pos.Node, runeToU16(pos.Node.Data, a)}, selPoint{pos.Node, runeToU16(pos.Node.Data, f)}
		s.origA, s.origF = s.a, s.f
		s.gran, s.has = granWord, true
	case clicks >= 3:
		a, f := blockBounds(ix, box, pos.Node)
		s.a, s.f = a, f
		s.origA, s.origF = a, f
		s.gran, s.has = granBlock, true
	case shift && s.has:
		s.f = p
		s.gran = granChar
	default:
		s.a, s.f, s.has = p, p, true
		s.gran = granChar
		s.origA, s.origF = p, p
	}
	s.last, s.hasLst = p, true
	s.gen++
	s.mu.Unlock()
	t.clearCtlSelections(env)
	t.selChanged()
	return true
}

func runeFromU16(v string, u int) int { return u16ToRune(v, u) }

// lineBounds is the extent of the visual line of control c that holds rune offset off.
func lineBounds(ix *render.TextIndex, c *html.Node, off, n int) (int, int) {
	for _, b := range ix.Boxes(c) {
		if off >= b.FirstOff() && off <= b.LastOff() {
			return b.FirstOff(), b.LastOff()
		}
	}
	return 0, n
}

// blockBounds is the paragraph that the run box belongs to: from the start of its first text to the end of its last.
func blockBounds(ix *render.TextIndex, box *render.TextBox, fallback *html.Node) (selPoint, selPoint) {
	if box == nil || box.Block == nil {
		return selPoint{fallback, 0}, selPoint{fallback, u16Len(fallback.Data)}
	}
	var first, last *html.Node
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode && len(ix.Boxes(n)) > 0 {
			if first == nil {
				first = n
			}
			last = n
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(box.Block)
	if first == nil {
		return selPoint{fallback, 0}, selPoint{fallback, u16Len(fallback.Data)}
	}
	return selPoint{first, 0}, selPoint{last, u16Len(last.Data)}
}

// SelectMove extends the selection gesture in progress to the page point (x, y).
func (t *Tab) SelectMove(x, y float64) bool {
	res, ix, d := t.textIndex()
	env := t.selEnv()
	if res == nil || env == nil {
		return false
	}
	s := &env.sel
	s.mu.Lock()
	if !s.down {
		s.mu.Unlock()
		return false
	}
	gran, oa, of, gc := s.gran, s.origA, s.origF, s.gctl
	s.moved = true
	s.mu.Unlock()
	// the gesture started in a control: it stays in it
	if gc != nil {
		c := gc
		off, ok := ctlOffsetAt(ix, c, x, y)
		if !ok {
			return false
		}
		v := ctlValue(d, c)
		rs := []rune(v)
		a, f := oa.off, off
		switch gran {
		case granWord:
			ws, we := wordAt(rs, off)
			if off < oa.off {
				a, f = of.off, ws
			} else {
				a, f = oa.off, we
			}
		case granBlock:
			ls, le := lineBounds(ix, c, off, len(rs))
			if off < oa.off {
				a, f = of.off, ls
			} else {
				a, f = oa.off, le
			}
		}
		dir := 1
		if f < a {
			dir = -1
		}
		t.ctlSelSetNode(env, c, runeToU16(v, a), runeToU16(v, f), dir)
		return true
	}
	pos, box, _, ok := ix.PosAt(x, y, false)
	if !ok {
		return false
	}
	p := selPoint{pos.Node, runeToU16(pos.Node.Data, pos.Off)}
	s.mu.Lock()
	switch gran {
	case granWord:
		rs := []rune(pos.Node.Data)
		ws, we := wordAt(rs, pos.Off)
		wsP, weP := selPoint{pos.Node, runeToU16(pos.Node.Data, ws)}, selPoint{pos.Node, runeToU16(pos.Node.Data, we)}
		if comparePoints(p, oa) < 0 {
			s.a, s.f = of, wsP
		} else {
			s.a, s.f = oa, weP
		}
	case granBlock:
		bs, be := blockBounds(ix, box, pos.Node)
		if comparePoints(p, oa) < 0 {
			s.a, s.f = of, bs
		} else {
			s.a, s.f = oa, be
		}
	default:
		changed := s.f != p
		s.f = p
		if !changed {
			s.mu.Unlock()
			return true
		}
	}
	s.has = true
	s.gen++
	s.mu.Unlock()
	t.selChanged()
	return true
}

// SelectUp ends the selection gesture.
func (t *Tab) SelectUp() {
	env := t.selEnv()
	if env == nil {
		return
	}
	s := &env.sel
	s.mu.Lock()
	s.down = false
	s.mu.Unlock()
}

// SelectAll selects the contents of the focused text control, or of the editing host that holds the caret, or
// of the whole page.
func (t *Tab) SelectAll() {
	d, env := t.docAndEnv()
	if d == nil || env == nil {
		return
	}
	s := &env.sel
	s.mu.Lock()
	foc := s.focus
	s.mu.Unlock()
	if foc != nil && isTextControl(foc) {
		v := ctlValue(d, foc)
		t.ctlSelSetNode(env, foc, 0, u16Len(v), 1)
		return
	}
	root := d.Root
	if foc != nil && isEditableHost(foc) {
		root = editingHost(foc)
	} else if b := findElement(d.Root, "body"); b != nil {
		root = b
	}
	if root == nil {
		return
	}
	s.mu.Lock()
	s.a, s.f, s.has = selPoint{root, 0}, selPoint{root, childCount(root)}, true
	s.gen++
	s.mu.Unlock()
	t.clearCtlSelections(env)
	t.selChanged()
}

func findElement(n *html.Node, tag string) *html.Node {
	if n.Type == html.ElementNode && n.Data == tag {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if r := findElement(c, tag); r != nil {
			return r
		}
	}
	return nil
}

// editingHost is the outermost element of the editable region n is in.
func editingHost(n *html.Node) *html.Node {
	var host *html.Node
	for ; n != nil; n = n.Parent {
		if n.Type != html.ElementNode {
			continue
		}
		if v, ok := attrValOK(n, "contenteditable"); ok {
			switch strings.ToLower(v) {
			case "", "true", "plaintext-only":
				host = n
			case "false":
				return host
			}
		}
	}
	return host
}

func attrValOK(n *html.Node, k string) (string, bool) {
	for _, a := range n.Attr {
		if a.Key == k {
			return a.Val, true
		}
	}
	return "", false
}

// clearCtlSelections empties the selections inside text controls: the selection of the document moved on.
func (t *Tab) clearCtlSelections(env *tabEnv) {
	s := &env.sel
	s.mu.Lock()
	for n, c := range s.ctl {
		if n != s.focus {
			c.start, c.end, c.dir = 0, 0, 0
		}
	}
	s.mu.Unlock()
}

func (t *Tab) setDocSelectionEmpty(env *tabEnv) {
	s := &env.sel
	s.mu.Lock()
	changed := s.has
	s.has = false
	s.mu.Unlock()
	if changed {
		t.selChanged()
	}
}

// SelClear removes the selection of the document.
func (t *Tab) SelClear() { t.SelSet(0, 0, 0, 0) }

// ---- moving the caret ---------------------------------------------------------------------------------

// CaretMove computes where the caret or the selection's focus goes from (node id, offset in UTF-16 units; for a
// text control the offset is into its value) when moved by dir: "left", "right", "wordleft", "wordright", "up",
// "down", "linestart", "lineend", "docstart", "docend", "pageup", "pagedown". It returns the new node id and
// offset, and ok false when the caret cannot move that way.
func (t *Tab) CaretMove(id int64, off int, dir string) (nid int64, noff int, ok bool) {
	res, ix, d := t.freshTextIndex()
	if res == nil {
		return 0, 0, false
	}
	n := d.NodeByID(id)
	if n == nil {
		return 0, 0, false
	}
	var p selPoint
	if n.Type == html.TextNode || isTextControl(n) {
		str := n.Data
		if isTextControl(n) {
			str = ctlValue(d, n)
		}
		p = selPoint{n, u16ToRune(str, off)}
	} else {
		// an element boundary: use the text node next to it
		if tn, o, found := textNear(ix, n, off); found {
			p = selPoint{tn, o}
		} else {
			return 0, 0, false
		}
	}
	q, ok := moveCaret(ix, d, p, dir)
	if !ok {
		return 0, 0, false
	}
	str := q.n.Data
	if isTextControl(q.n) {
		str = ctlValue(d, q.n)
	}
	return d.IDOf(q.n), runeToU16(str, q.off), true
}

// textNear finds a text node with text next to the element boundary (n, off): the first text at or after it
// (rune offset 0), else the last text before it.
func textNear(ix *render.TextIndex, n *html.Node, off int) (*html.Node, int, bool) {
	var after, before *html.Node
	cnt := 0
	var walk func(x *html.Node)
	walk = func(x *html.Node) {
		if x.Type == html.TextNode && len(ix.Boxes(x)) > 0 {
			if cnt >= off && after == nil {
				after = x
			}
			if cnt < off {
				before = x
			}
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walk(c)
		cnt++
	}
	if after != nil {
		return after, 0, true
	}
	if before != nil {
		return before, utf8.RuneCountInString(before.Data), true
	}
	return nil, 0, false
}

// textNodesWithBoxes lists the text nodes of the document (in tree order) that have a run on the page, or the
// single control.
func textNodesWithBoxes(ix *render.TextIndex, root *html.Node) []*html.Node {
	var out []*html.Node
	var walk func(x *html.Node)
	walk = func(x *html.Node) {
		if x.Type == html.TextNode && len(ix.Boxes(x)) > 0 {
			out = append(out, x)
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	return out
}

// visualPos finds the run and caret stop that stand for the position p.
func visualPos(ix *render.TextIndex, p selPoint, preferStart bool) (*render.TextBox, int) {
	boxes := ix.Boxes(p.n)
	var hit *render.TextBox
	for _, b := range boxes {
		if p.off >= b.FirstOff() && p.off <= b.LastOff() {
			if hit == nil || (preferStart && p.off < b.LastOff() && hit.LastOff() == p.off) {
				hit = b
			}
			if !preferStart && p.off < b.LastOff() {
				hit = b
				break
			}
		}
	}
	if hit == nil && len(boxes) > 0 {
		// between runs (collapsed blanks): the nearer one
		hit = boxes[0]
		for _, b := range boxes {
			if p.off >= b.FirstOff() {
				hit = b
			}
		}
		if p.off > hit.LastOff() {
			return hit, hit.Stops() - 1
		}
		return hit, 0
	}
	if hit == nil {
		return nil, 0
	}
	return hit, hit.StopAt(p.off)
}

func moveCaret(ix *render.TextIndex, d *fnet.Doc, p selPoint, dir string) (selPoint, bool) {
	isCtl := isTextControl(p.n)
	switch dir {
	case "left", "right":
		return stepChar(ix, d, p, dir == "right", isCtl)
	case "wordleft", "wordright":
		return stepWord(ix, d, p, dir == "wordright", isCtl)
	case "up", "down", "pageup", "pagedown":
		n := 1
		if dir == "pageup" || dir == "pagedown" {
			n = 12
		}
		var q selPoint
		ok := false
		for i := 0; i < n; i++ {
			nq, k := stepLine(ix, p, dir == "down" || dir == "pagedown", isCtl)
			if !k {
				break
			}
			q, ok, p = nq, true, nq
		}
		return q, ok
	case "linestart", "lineend":
		return lineEdge(ix, p, dir == "lineend")
	case "docstart", "docend":
		if isCtl {
			if dir == "docstart" {
				return selPoint{p.n, 0}, true
			}
			return selPoint{p.n, utf8.RuneCountInString(ctlValue(d, p.n))}, true
		}
		root := editingHost(p.n)
		if root == nil {
			root = d.Root
		}
		nodes := textNodesWithBoxes(ix, root)
		if len(nodes) == 0 {
			return p, false
		}
		if dir == "docstart" {
			return selPoint{nodes[0], 0}, true
		}
		l := nodes[len(nodes)-1]
		return selPoint{l, utf8.RuneCountInString(l.Data)}, true
	}
	return p, false
}

func stepChar(ix *render.TextIndex, d *fnet.Doc, p selPoint, right, isCtl bool) (selPoint, bool) {
	if isCtl {
		n := utf8.RuneCountInString(ctlValue(d, p.n))
		if right && p.off < n {
			return selPoint{p.n, p.off + 1}, true
		}
		if !right && p.off > 0 {
			return selPoint{p.n, p.off - 1}, true
		}
		return p, false
	}
	box, stop := visualPos(ix, p, right)
	if box == nil {
		return p, false
	}
	boxes := ix.Boxes(p.n)
	// the position at the end of a run is the start of the next run of the node: step from there
	idx := -1
	for i, b := range boxes {
		if b == box {
			idx = i
		}
	}
	if right {
		if stop >= box.Stops()-1 {
			if idx+1 < len(boxes) {
				box, stop = boxes[idx+1], 0
			} else if q, ok := adjacentNode(ix, p.n, true); ok {
				return q, true
			} else {
				return p, false
			}
		}
		return selPoint{p.n, box.Off(stop + 1)}, true
	}
	if stop <= 0 {
		if idx > 0 {
			box, stop = boxes[idx-1], boxes[idx-1].Stops()-1
		} else if q, ok := adjacentNode(ix, p.n, false); ok {
			return q, true
		} else {
			return p, false
		}
	}
	return selPoint{p.n, box.Off(stop - 1)}, true
}

// adjacentNode returns the position at the end (for next=false) or the start (next=true) of the text node that
// comes after or before n in the document, one character in (the same position visually as the end of n when
// they are neighbours in one paragraph).
func adjacentNode(ix *render.TextIndex, n *html.Node, next bool) (selPoint, bool) {
	root := n
	for root.Parent != nil {
		root = root.Parent
	}
	nodes := textNodesWithBoxes(ix, root)
	for i, x := range nodes {
		if x != n {
			continue
		}
		if next && i+1 < len(nodes) {
			m := nodes[i+1]
			bm := ix.Boxes(m)
			bn := ix.Boxes(n)
			if bm[0].Block == bn[len(bn)-1].Block && len(bm[0].Xs) > 1 {
				return selPoint{m, bm[0].Off(1)}, true
			}
			return selPoint{m, bm[0].Off(0)}, true
		}
		if !next && i > 0 {
			m := nodes[i-1]
			bm := ix.Boxes(m)
			bn := ix.Boxes(n)
			last := bm[len(bm)-1]
			if last.Block == bn[0].Block && last.Stops() > 1 {
				return selPoint{m, last.Off(last.Stops() - 2)}, true
			}
			return selPoint{m, last.Off(last.Stops() - 1)}, true
		}
	}
	return selPoint{}, false
}

func stepWord(ix *render.TextIndex, d *fnet.Doc, p selPoint, right, isCtl bool) (selPoint, bool) {
	var rs []rune
	if isCtl {
		rs = []rune(ctlValue(d, p.n))
	} else {
		rs = []rune(p.n.Data)
	}
	off := p.off
	cls := func(r rune) int {
		switch {
		case isWordRune(r):
			return 1
		case r == ' ' || r == '\t' || r == '\n' || r == 0xa0:
			return 0
		}
		return 2
	}
	if right {
		for off < len(rs) && cls(rs[off]) == 0 {
			off++
		}
		if off < len(rs) {
			c := cls(rs[off])
			for off < len(rs) && cls(rs[off]) == c {
				off++
			}
			return selPoint{p.n, off}, true
		}
		if isCtl {
			return selPoint{p.n, off}, off != p.off
		}
		if q, ok := adjacentNode(ix, p.n, true); ok && off == p.off || ok && off >= len(rs) {
			return q, true
		}
		return selPoint{p.n, off}, off != p.off
	}
	for off > 0 && cls(rs[off-1]) == 0 {
		off--
	}
	if off > 0 {
		c := cls(rs[off-1])
		for off > 0 && cls(rs[off-1]) == c {
			off--
		}
		return selPoint{p.n, off}, true
	}
	if isCtl {
		return selPoint{p.n, off}, off != p.off
	}
	if q, ok := adjacentNode(ix, p.n, false); ok {
		return q, true
	}
	return selPoint{p.n, off}, off != p.off
}

// stepLine moves the caret one visual line down or up, keeping its x.
func stepLine(ix *render.TextIndex, p selPoint, down, isCtl bool) (selPoint, bool) {
	box, stop := visualPos(ix, p, true)
	if box == nil {
		return p, false
	}
	x := box.X(stop)
	curTop := box.Rect.Y
	var cands []*render.TextBox
	best := math.MaxFloat64
	res := ix.Result()
	for i := range res.Text {
		t := &res.Text[i]
		if t.NoSelect || t.Ctl != isCtl || (isCtl && t.Node != p.n) {
			continue
		}
		if !isCtl && editingHost(p.n) != editingHost(t.Node) {
			continue
		}
		dy := t.Rect.Y - curTop
		if down && dy <= 0.5 || !down && dy >= -0.5 {
			continue
		}
		ad := math.Abs(dy)
		if ad < best-0.5 {
			best, cands = ad, cands[:0]
			cands = append(cands, t)
		} else if ad < best+0.5 {
			cands = append(cands, t)
		}
	}
	if len(cands) == 0 {
		return p, false
	}
	// on the target line, the run that contains x (or the nearest)
	target := cands[0]
	td := math.MaxFloat64
	for _, c := range cands {
		l, r := c.X(0), c.X(c.Stops()-1)
		if l > r {
			l, r = r, l
		}
		dd := 0.0
		if x < l {
			dd = l - x
		} else if x > r {
			dd = x - r
		}
		if dd < td {
			target, td = c, dd
		}
	}
	return selPoint{target.Node, target.Off(target.CaretFor(x))}, true
}

// lineEdge is the start or end of the visual line of the caret.
func lineEdge(ix *render.TextIndex, p selPoint, end bool) (selPoint, bool) {
	box, _ := visualPos(ix, p, true)
	if box == nil {
		return p, false
	}
	res := ix.Result()
	var best *render.TextBox
	bestX := 0.0
	for i := range res.Text {
		t := &res.Text[i]
		if t.NoSelect || t.Ctl != box.Ctl || t.Rect.Y != box.Rect.Y || t.Rect.H != box.Rect.H || t.Block != box.Block {
			continue
		}
		if box.Ctl && t.Node != box.Node {
			continue
		}
		var x float64
		var st int
		if end {
			l, r := t.X(0), t.X(t.Stops()-1)
			st = t.Stops() - 1
			x = r
			if l > r {
				st, x = 0, l
			}
			if best == nil || x > bestX {
				best, bestX = t, x
			}
		} else {
			l, r := t.X(0), t.X(t.Stops()-1)
			st = 0
			x = l
			if r < l {
				st, x = t.Stops()-1, r
			}
			if best == nil || x < bestX {
				best, bestX = t, x
			}
		}
		_ = st
	}
	if best == nil {
		return p, false
	}
	if end {
		l, r := best.X(0), best.X(best.Stops()-1)
		if l > r {
			return selPoint{best.Node, best.Off(0)}, true
		}
		_ = l
		return selPoint{best.Node, best.Off(best.Stops() - 1)}, true
	}
	l, r := best.X(0), best.X(best.Stops()-1)
	if r < l {
		return selPoint{best.Node, best.Off(best.Stops() - 1)}, true
	}
	return selPoint{best.Node, best.Off(0)}, true
}
