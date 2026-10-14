package engine

import (
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"

	fnet "github.com/sweefbrowser/sweef/internal/net"
	"github.com/sweefbrowser/sweef/internal/render"
)

// selection.go: the selection of a document.
//
// A document has one selection (anchor and focus, as DOM boundary points) and, for each text control, the
// selection inside its value. The state lives here, not in page script, so that the mouse and keyboard of the
// window can change it without a round trip into JavaScript, and so that painting can show it without a layout
// pass: the display list the layout produced is left alone, and the highlights and the caret are put into a copy
// of it when it is painted (render.ApplyHighlights). Script sees the same state through window.getSelection(),
// Range and the selection properties of inputs.
//
// Offsets of boundary points follow the DOM: UTF-16 code units into the data of a text node, child index for an
// element. Layout works in runes; the conversions are here.

// selPoint is a DOM boundary point.
type selPoint struct {
	n   *html.Node
	off int
}

type ctlSel struct {
	start, end int // UTF-16 units into the value
	dir        int // 1 forward, -1 backward, 0 none
}

// selState is the selection state of one document.
type selState struct {
	mu     sync.Mutex
	has    bool
	a, f   selPoint // anchor and focus
	ctl    map[*html.Node]*ctlSel
	focus  *html.Node // the focused element (set by script's focus handling)
	gran   int        // granularity of the gesture in progress: 0 character, 1 word, 2 block
	origA  selPoint   // the unit a double or triple click selected, which a drag extends from
	origF  selPoint
	down   bool
	moved  bool
	gctl   *html.Node // the text control the gesture in progress started in
	last   selPoint   // where the last gesture started, for shift+click
	hasLst bool
	find   *findState
	gen    uint64
	rows   [2]float64 // the rows the highlights occupied when selRows last looked
	rowsOK bool
}

// ---- strings ----------------------------------------------------------------------------------------

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// u16Len is the length of s in UTF-16 code units.
func u16Len(s string) int {
	if isASCII(s) {
		return len(s)
	}
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// u16ToRune converts an offset in UTF-16 units into an offset in runes (an offset inside a surrogate pair rounds
// down).
func u16ToRune(s string, u int) int {
	if u <= 0 {
		return 0
	}
	if isASCII(s) {
		return min(u, len(s))
	}
	n, units := 0, 0
	for _, r := range s {
		w := 1
		if r >= 0x10000 {
			w = 2
		}
		if units+w > u {
			return n
		}
		units += w
		n++
	}
	return n
}

// runeToU16 converts an offset in runes into UTF-16 units.
func runeToU16(s string, r int) int {
	if r <= 0 {
		return 0
	}
	if isASCII(s) {
		return min(r, len(s))
	}
	n, units := 0, 0
	for _, c := range s {
		if n >= r {
			break
		}
		if c >= 0x10000 {
			units += 2
		} else {
			units++
		}
		n++
	}
	return units
}

// ---- tree order -------------------------------------------------------------------------------------

func childIndex(n *html.Node) int {
	i := 0
	for s := n.PrevSibling; s != nil; s = s.PrevSibling {
		i++
	}
	return i
}

func childCount(n *html.Node) int {
	i := 0
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		i++
	}
	return i
}

func childAt(n *html.Node, i int) *html.Node {
	c := n.FirstChild
	for ; c != nil && i > 0; i-- {
		c = c.NextSibling
	}
	return c
}

// nodeLen is the length of a node in the unit its boundary points use.
func nodeLen(n *html.Node) int {
	switch n.Type {
	case html.TextNode, html.CommentNode:
		return u16Len(n.Data)
	}
	return childCount(n)
}

// nextInTree is the node after n in tree order.
func nextInTree(n *html.Node) *html.Node {
	if n.FirstChild != nil {
		return n.FirstChild
	}
	return nextAfterSubtree(n)
}

func nextAfterSubtree(n *html.Node) *html.Node {
	for ; n != nil; n = n.Parent {
		if n.NextSibling != nil {
			return n.NextSibling
		}
	}
	return nil
}

// pathKey is a sortable key for a boundary point: the child indexes down from the root, then the offset. A
// child c counts as 2c+1 and an offset o as 2o (the point before child o), so a boundary compares correctly
// against the nodes around it.
func pathKey(p selPoint) []int {
	var rev []int
	for n := p.n; n != nil && n.Parent != nil; n = n.Parent {
		rev = append(rev, 2*childIndex(n)+1)
	}
	key := make([]int, 0, len(rev)+1)
	for i := len(rev) - 1; i >= 0; i-- {
		key = append(key, rev[i])
	}
	return append(key, 2*p.off)
}

// comparePoints orders two boundary points of the same tree: -1 a before b, 0 equal, 1 after.
func comparePoints(a, b selPoint) int {
	if a.n == b.n {
		switch {
		case a.off < b.off:
			return -1
		case a.off > b.off:
			return 1
		}
		return 0
	}
	ka, kb := pathKey(a), pathKey(b)
	for i := 0; i < len(ka) && i < len(kb); i++ {
		if ka[i] != kb[i] {
			if ka[i] < kb[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(ka) < len(kb):
		return -1
	case len(ka) > len(kb):
		return 1
	}
	return 0
}

// orderedRange returns the selection's boundary points in document order.
func (s *selState) orderedLocked() (start, end selPoint, ok bool) {
	if !s.has || s.a.n == nil || s.f.n == nil {
		return
	}
	if comparePoints(s.a, s.f) <= 0 {
		return s.a, s.f, true
	}
	return s.f, s.a, true
}

// textsInRange calls fn for every text node that lies wholly or partly inside [start, end] in tree order, with
// the part inside as rune offsets.
func textsInRange(start, end selPoint, fn func(n *html.Node, from, to int)) {
	var first, stop *html.Node
	if start.n.Type == html.TextNode {
		first = start.n
	} else if c := childAt(start.n, start.off); c != nil {
		first = c
	} else {
		first = nextAfterSubtree(start.n)
	}
	if end.n.Type == html.TextNode {
		stop = nextInTree(end.n)
	} else if c := childAt(end.n, end.off); c != nil {
		stop = c
	} else {
		stop = nextAfterSubtree(end.n)
	}
	for n := first; n != nil && n != stop; n = nextInTree(n) {
		if n.Type != html.TextNode {
			continue
		}
		from, to := 0, utf8.RuneCountInString(n.Data)
		if n == start.n {
			from = u16ToRune(n.Data, start.off)
		}
		if n == end.n {
			to = u16ToRune(n.Data, end.off)
		}
		if to > from {
			fn(n, from, to)
		}
	}
}

// ---- access to the current layout -----------------------------------------------------------------------

type textIdx struct {
	res *render.Result
	ix  *render.TextIndex
}

// textIndex returns the text index of the layout computed last. It never starts a layout.
func (t *Tab) textIndex() (*render.Result, *render.TextIndex, *fnet.Doc) {
	res, d := t.cachedResult()
	if res == nil {
		return nil, nil, nil
	}
	env := t.ds.env.Load()
	if env == nil {
		return nil, nil, nil
	}
	return res, env.indexFor(res), d
}

// indexFor returns the text index of a layout result, built once.
func (e *tabEnv) indexFor(res *render.Result) *render.TextIndex {
	e.tix.Lock()
	defer e.tix.Unlock()
	if e.tix.res != res {
		e.tix.res, e.tix.ix = res, render.NewTextIndex(res)
	}
	return e.tix.ix
}

// freshTextIndex is textIndex for a thread that may wait for layout (the tab's worker).
func (t *Tab) freshTextIndex() (*render.Result, *render.TextIndex, *fnet.Doc) {
	t.mu.RLock()
	d := t.dom
	t.mu.RUnlock()
	if d == nil {
		return nil, nil, nil
	}
	d.Layout(d.LayoutWidth())
	return t.textIndex()
}

// ---- reading and writing the selection -------------------------------------------------------------------

// SelInfo is the document selection as script sees it.
type SelInfo struct {
	Has                 bool
	AnchorID, FocusID   int64
	AnchorOff, FocusOff int
}

func (t *Tab) selEnv() *tabEnv { return t.ds.env.Load() }

// SelGet returns the selection of the document.
func (t *Tab) SelGet() SelInfo {
	env := t.selEnv()
	t.mu.RLock()
	d := t.dom
	t.mu.RUnlock()
	if env == nil || d == nil {
		return SelInfo{}
	}
	s := &env.sel
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.has || s.a.n == nil || s.f.n == nil || !attachedTo(d, s.a.n) || !attachedTo(d, s.f.n) {
		return SelInfo{}
	}
	return SelInfo{Has: true, AnchorID: d.IDOf(s.a.n), AnchorOff: clampOff(s.a), FocusID: d.IDOf(s.f.n), FocusOff: clampOff(s.f)}
}

func clampOff(p selPoint) int {
	if l := nodeLen(p.n); p.off > l {
		return l
	}
	if p.off < 0 {
		return 0
	}
	return p.off
}

func attachedTo(d *fnet.Doc, n *html.Node) bool {
	for ; n != nil; n = n.Parent {
		if n == d.Root {
			return true
		}
	}
	return false
}

// SelSet sets the selection from boundary points given by backend node ids; an id of 0 clears it.
func (t *Tab) SelSet(aid int64, ao int, fid int64, fo int) {
	t.mu.RLock()
	d := t.dom
	t.mu.RUnlock()
	env := t.selEnv()
	if d == nil || env == nil {
		return
	}
	an, fn := d.NodeByID(aid), d.NodeByID(fid)
	s := &env.sel
	s.mu.Lock()
	changed := false
	if an == nil || fn == nil {
		changed = s.has
		s.has = false
		s.a, s.f = selPoint{}, selPoint{}
	} else {
		na, nf := selPoint{an, ao}, selPoint{fn, fo}
		changed = !s.has || s.a != na || s.f != nf
		s.has, s.a, s.f = true, na, nf
	}
	if changed {
		s.gen++
	}
	s.mu.Unlock()
	if changed {
		t.selChanged()
	}
}

// selChanged tells the window the selection moved: the rows of the page it touched (before and after) have to be
// repainted, and script gets selectionchange.
func (t *Tab) selChanged() {
	t.mu.RLock()
	d := t.dom
	t.mu.RUnlock()
	if d != nil {
		d.TouchPaint()
	}
	if top, bot, ok := t.selRows(); ok {
		t.quiet.addRows(top, bot)
	}
	if h := t.selHook.Load(); h != nil {
		(*h)()
	}
}

// selRows is the rows of the page the highlights and carets occupy now or did when this was last asked.
func (t *Tab) selRows() (top, bot float64, ok bool) {
	env := t.selEnv()
	res, ix, d := t.textIndex()
	if env == nil || res == nil {
		return 0, 0, false
	}
	hl, carets := t.highlights(res, ix, d)
	cur, any, bail := rowsOfHighlights(ix, hl, carets)
	s := &env.sel
	s.mu.Lock()
	prev, prevOK := s.rows, s.rowsOK
	s.rows, s.rowsOK = cur, any
	s.mu.Unlock()
	if bail {
		return 0, 0, false
	}
	switch {
	case any && prevOK:
		return min(cur[0], prev[0]), max(cur[1], prev[1]), true
	case any:
		return cur[0], cur[1], true
	case prevOK:
		return prev[0], prev[1], true
	}
	return 0, 0, true
}

// rowsOfHighlights is the rows covered by the lines the highlights and carets are on. bail is true when one of
// them is on a transformed run (the rows are not the page rows then).
func rowsOfHighlights(ix *render.TextIndex, hl []render.Highlight, carets []render.Caret) (rows [2]float64, any, bail bool) {
	add := func(t *render.TextBox) {
		if t.HasM {
			bail = true
			return
		}
		top, bot := t.Rect.Y-2, t.Rect.Y+t.Rect.H+2
		if !any {
			rows, any = [2]float64{top, bot}, true
			return
		}
		rows[0], rows[1] = min(rows[0], top), max(rows[1], bot)
	}
	for _, h := range hl {
		for _, t := range ix.Boxes(h.Node) {
			if t.LastOff() >= h.From && t.FirstOff() <= h.To {
				add(t)
			}
		}
	}
	for _, c := range carets {
		for _, t := range ix.Boxes(c.Node) {
			if t.Contains(c.Off) {
				add(t)
			}
		}
	}
	return
}

// SetSelectionHook registers a function called (on whatever goroutine changed it) when the selection or a
// control's selection changed.
func (t *Tab) SetSelectionHook(f func()) {
	if f == nil {
		t.selHook.Store(nil)
		return
	}
	t.selHook.Store(&f)
}

// ---- control selections -------------------------------------------------------------------------------

// CtlSelGet returns the selection of the text control with backend id, in UTF-16 units of its value, and its
// direction (1 forward, -1 backward, 0 none). ok is false while the control has never had one.
func (t *Tab) CtlSelGet(id int64) (start, end, dir int, ok bool) {
	env := t.selEnv()
	t.mu.RLock()
	d := t.dom
	t.mu.RUnlock()
	if env == nil || d == nil {
		return
	}
	n := d.NodeByID(id)
	s := &env.sel
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.ctl[n]; c != nil {
		return c.start, c.end, c.dir, true
	}
	return
}

// CtlSelSet sets the selection of a text control.
func (t *Tab) CtlSelSet(id int64, start, end, dir int) {
	env := t.selEnv()
	t.mu.RLock()
	d := t.dom
	t.mu.RUnlock()
	if env == nil || d == nil {
		return
	}
	n := d.NodeByID(id)
	if n == nil {
		return
	}
	t.ctlSelSetNode(env, n, start, end, dir)
}

func (t *Tab) ctlSelSetNode(env *tabEnv, n *html.Node, start, end, dir int) {
	s := &env.sel
	s.mu.Lock()
	if s.ctl == nil {
		s.ctl = map[*html.Node]*ctlSel{}
	}
	c := s.ctl[n]
	changed := c == nil || c.start != start || c.end != end || c.dir != dir
	if c == nil {
		c = &ctlSel{}
		s.ctl[n] = c
	}
	c.start, c.end, c.dir = start, end, dir
	if changed {
		s.gen++
	}
	s.mu.Unlock()
	if changed {
		t.selChanged()
	}
}

// SetFocusNode records which element has keyboard focus (script's focus handling reports it); it decides where
// the caret is drawn.
func (t *Tab) SetFocusNode(id int64) {
	env := t.selEnv()
	t.mu.RLock()
	d := t.dom
	t.mu.RUnlock()
	if env == nil || d == nil {
		return
	}
	n := d.NodeByID(id)
	s := &env.sel
	s.mu.Lock()
	changed := s.focus != n
	s.focus = n
	s.mu.Unlock()
	if changed {
		t.selChanged()
	}
}

// ---- text of the selection ---------------------------------------------------------------------------

func isBlockBreakTag(n *html.Node) int {
	if n == nil {
		return 1
	}
	if n.Data == "p" {
		return 2
	}
	return 1
}

// rangeTextLocked is the text of [start, end] as the page shows it: collapsed white space, a line break where a
// line is forced to end (<br>, a newline in pre text) and between blocks, nothing for text the user cannot
// select (user-select: none) or that is not rendered.
func (t *Tab) rangeText(start, end selPoint) string {
	_, ix, _ := t.freshTextIndex() // script asks on the worker, which may wait for a layout
	if ix == nil {
		return rawRangeText(start, end)
	}
	var b strings.Builder
	var prev *render.TextBox
	textsInRange(start, end, func(n *html.Node, from, to int) {
		runes := []rune(n.Data)
		for _, tb := range ix.Boxes(n) {
			if tb.NoSelect || tb.Ctl {
				continue
			}
			lo, hi := tb.FirstOff(), tb.LastOff()
			a, z := max(lo, from), min(hi, to)
			if z <= a {
				continue
			}
			if prev != nil {
				switch {
				case prev.Block != tb.Block:
					for i := 0; i < max(isBlockBreakTag(prev.Block), isBlockBreakTag(tb.Block)); i++ {
						b.WriteByte('\n')
					}
				case prev.Rect.Y != tb.Rect.Y && prev.Break == render.BreakForced:
					b.WriteByte('\n')
				}
			}
			i0, i1 := tb.StopAt(a), tb.StopAt(z)
			for i := i0; i < i1 && i < tb.Stops()-1; i++ {
				o := tb.Off(i)
				if o < 0 || o >= len(runes) {
					continue
				}
				r := runes[o]
				if tb.CollapseWS && (r == '\n' || r == '\t' || r == '\r' || r == '\f') && !(tb.KeepNL && r == '\n') {
					r = ' '
				}
				b.WriteRune(r)
			}
			prev = tb
		}
	})
	return b.String()
}

// rawRangeText concatenates the data of the text nodes in the range (Range.toString).
func rawRangeText(start, end selPoint) string {
	var b strings.Builder
	textsInRange(start, end, func(n *html.Node, from, to int) {
		r := []rune(n.Data)
		if from < len(r) {
			b.WriteString(string(r[from:min(to, len(r))]))
		}
	})
	return b.String()
}

// SelText is the text of the selection of the document, or of the focused text control when its selection is
// the one that counts.
func (t *Tab) SelText() string {
	env := t.selEnv()
	t.mu.RLock()
	d := t.dom
	t.mu.RUnlock()
	if env == nil || d == nil {
		return ""
	}
	s := &env.sel
	s.mu.Lock()
	foc := s.focus
	var c ctlSel
	hasCtl := false
	if foc != nil && isTextControl(foc) && s.ctl[foc] != nil {
		c, hasCtl = *s.ctl[foc], true
	}
	start, end, ok := s.orderedLocked()
	s.mu.Unlock()
	if hasCtl && c.start != c.end {
		return ctlSlice(d, foc, c.start, c.end)
	}
	if !ok || comparePoints(start, end) == 0 {
		return ""
	}
	return t.rangeText(start, end)
}

func isTextControl(n *html.Node) bool {
	if n == nil || n.Type != html.ElementNode {
		return false
	}
	switch n.Data {
	case "textarea":
		return true
	case "input":
		typ := strings.ToLower(attrOf(n, "type"))
		switch typ {
		case "", "text", "search", "url", "tel", "email", "password", "number":
			return true
		}
	}
	return false
}

func ctlValue(d *fnet.Doc, n *html.Node) string { return d.GetValue(d.IDOf(n)) }

func ctlSlice(d *fnet.Doc, n *html.Node, s, e int) string {
	v := ctlValue(d, n)
	if s > e {
		s, e = e, s
	}
	a, b := u16ToRune(v, s), u16ToRune(v, e)
	r := []rune(v)
	if b > len(r) {
		b = len(r)
	}
	if a > b {
		a = b
	}
	if n.Data == "input" && strings.EqualFold(attrOf(n, "type"), "password") {
		return "" // a password is never copied
	}
	return string(r[a:b])
}

// ---- highlights for painting ---------------------------------------------------------------------------

// Colours of the highlights, as Chromium draws them.
var (
	selFill      = render.Color{R: 0x33, G: 0x90, B: 0xff, A: 255}
	selText      = render.Color{R: 255, G: 255, B: 255, A: 255}
	findFill     = render.Color{R: 0xff, G: 0xff, B: 0x00, A: 255}
	findActive   = render.Color{R: 0xff, G: 0x96, B: 0x32, A: 255}
	findTextCol  = render.Color{R: 0, G: 0, B: 0, A: 255}
	caretDefault = render.Color{R: 0, G: 0, B: 0, A: 255}
)

// highlights builds what to draw over the layout's display list for the current selection, find matches and
// caret. It reads the layout's text records only.
func (t *Tab) highlights(res *render.Result, ix *render.TextIndex, d *fnet.Doc) (hl []render.Highlight, carets []render.Caret) {
	env := t.selEnv()
	if env == nil {
		return
	}
	s := &env.sel
	s.mu.Lock()
	start, end, ok := s.orderedLocked()
	foc := s.focus
	var ctl *ctlSel
	if foc != nil && s.ctl[foc] != nil {
		c := *s.ctl[foc]
		ctl = &c
	}
	fnd := s.find
	var fmatches []findMatch
	fact := -1
	if fnd != nil {
		fmatches, fact = fnd.matches, fnd.active
	}
	s.mu.Unlock()
	attached := func(n *html.Node) bool { return attachedTo(d, n) }
	for i, m := range fmatches {
		if !attached(m.start.n) || !attached(m.end.n) {
			continue
		}
		fill := findFill
		if i == fact {
			fill = findActive
		}
		textsInRange(m.start, m.end, func(n *html.Node, from, to int) {
			hl = append(hl, render.Highlight{Node: n, From: from, To: to, Fill: fill, Text: findTextCol, HasText: true})
		})
	}
	editableFocus := foc != nil && isEditableHost(foc)
	if ok && attached(start.n) && attached(end.n) && comparePoints(start, end) != 0 {
		var lastNode *html.Node
		textsInRange(start, end, func(n *html.Node, from, to int) { lastNode = n })
		textsInRange(start, end, func(n *html.Node, from, to int) {
			hl = append(hl, render.Highlight{Node: n, From: from, To: to, Fill: selFill, Text: selText, HasText: true, Newline: n != lastNode})
		})
	} else if ok && attached(start.n) && editableFocus && inside(foc, start.n) {
		// a collapsed selection in an editable element is the caret
		if start.n.Type == html.TextNode {
			carets = append(carets, render.Caret{Node: start.n, Off: u16ToRune(start.n.Data, start.off), Color: caretColor(res, foc)})
		}
	}
	if foc != nil && isTextControl(foc) {
		v := ctlValue(d, foc)
		cs, ce := len([]rune(v)), len([]rune(v))
		if ctl != nil {
			cs, ce = u16ToRune(v, ctl.start), u16ToRune(v, ctl.end)
		}
		if cs > ce {
			cs, ce = ce, cs
		}
		if ce > cs {
			hl = append(hl, render.Highlight{Node: foc, From: cs, To: ce, Fill: selFill, Text: selText, HasText: true})
		} else {
			carets = append(carets, render.Caret{Node: foc, Off: ce, Color: caretColor(res, foc)})
		}
	}
	return
}

func inside(host, n *html.Node) bool {
	for ; n != nil; n = n.Parent {
		if n == host {
			return true
		}
	}
	return false
}

func isEditableHost(n *html.Node) bool {
	for ; n != nil; n = n.Parent {
		if n.Type == html.ElementNode {
			switch strings.ToLower(attrOf(n, "contenteditable")) {
			case "", "true", "plaintext-only":
				if hasAttrNode(n, "contenteditable") {
					return true
				}
			case "false":
				return false
			}
		}
	}
	return false
}

func hasAttrNode(n *html.Node, k string) bool {
	for _, a := range n.Attr {
		if a.Key == k {
			return true
		}
	}
	return false
}

func caretColor(res *render.Result, n *html.Node) render.Color {
	if b := res.Boxes[n]; b != nil && b.Style != nil {
		if b.Style.CaretSet {
			return b.Style.CaretColor
		}
		return b.Style.Color
	}
	return caretDefault
}

// ---- word and block boundaries -----------------------------------------------------------------------

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || unicode.Is(unicode.M, r)
}

// WordAt returns the word around rune offset off of s: the run of letters and digits, the run of blanks, or the
// single other character.
func wordAt(rs []rune, off int) (int, int) {
	if len(rs) == 0 {
		return 0, 0
	}
	if off >= len(rs) {
		off = len(rs) - 1
	}
	if off < 0 {
		off = 0
	}
	class := func(r rune) int {
		switch {
		case isWordRune(r):
			return 1
		case unicode.IsSpace(r):
			return 2
		}
		return 3
	}
	c := class(rs[off])
	a, b := off, off+1
	if c == 3 {
		return a, b
	}
	for a > 0 && class(rs[a-1]) == c {
		a--
	}
	for b < len(rs) && class(rs[b]) == c {
		b++
	}
	return a, b
}
