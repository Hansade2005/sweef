package render

import (
	"math"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/sweefbrowser/sweef/internal/font"
	"github.com/sweefbrowser/sweef/internal/style"
)

type atomKind uint8

const (
	aText atomKind = iota
	aOpen
	aClose
	aAtomic
	aFloat
	aOOF
	aBreak
)

type textItem struct {
	owner  *lbox // nearest inline box (or the IFC root)
	src    *lbox
	runes  []rune
	sh     *shaped
	fs     *fontSpec
	st     *style.Computed
	marker bool
	lv     []uint8 // bidi level per rune (nil: not a bidi paragraph)
	off    int     // offset of runes within the source text box's collapsed text (small-caps segments)
}

type atom struct {
	kind   atomKind
	box    *lbox
	it     *textItem
	a, e   int
	w      float64
	trail  float64
	brk    bool
	forced bool
	space  bool // chunk consists only of collapsible spaces
	tab    bool
	bidi   bool  // the paragraph has bidi levels
	lv     uint8 // bidi level of an atomic inline
}

type frag struct {
	kind   atomKind
	box    *lbox
	it     *textItem
	a, e   int
	x, w   float64 // x relative to the line's left edge
	base   float64 // baseline offset from line top
	justSp float64 // extra width added to each space (justify)
	nsp    int
	trail  float64
	rtl    bool // right-to-left level: glyphs run right to left inside the fragment
}

type line struct {
	x, y       float64 // content-box relative
	w          float64 // available width
	contentW   float64
	h          float64
	baseline   float64
	frags      []*frag
	forced     bool
	boxes      []lineBox
	hasContent bool
}

type lineBox struct {
	box          *lbox
	x0, x1       float64
	base         float64
	contL, contR bool // continues from previous line / to the next
	rect         Rect // absolute border-box fragment (filled by placeInline)
}

// ---- flatten ------------------------------------------------------------------------------

type flatState struct {
	c         *ctx
	root      *lbox
	atoms     []atom
	prevSpace bool
	prevRune  rune
	lastRune  rune
	lastFace  *font.Face
	open      []*lbox
	any       bool
	flRoot    *lbox // building the ::first-line variant of this IFC root
}

func (c *ctx) buildAtoms(b *lbox) []atom {
	if b.atomsDone {
		return b.atoms
	}
	fl := &flatState{c: c, root: b, prevSpace: true}
	fl.walk(b, b)
	// text-transform / collapse across boundaries is done during walk
	b.atoms, b.atomsDone = fl.atoms, true
	c.assignBidi(b, b.atoms)
	return b.atoms
}

func (f *flatState) walk(parent, owner *lbox) {
	for _, k := range parent.kids {
		switch {
		case k.oof:
			f.atoms = append(f.atoms, atom{kind: aOOF, box: k})
		case k.floated:
			f.atoms = append(f.atoms, atom{kind: aFloat, box: k})
		case k.kind == kText:
			f.text(k, owner)
		case k.kind == kMarker:
		case k.kind == kBreak:
			f.lastRune, f.lastFace = 0, nil
			f.atoms = append(f.atoms, atom{kind: aBreak, box: k, forced: true})
			f.prevSpace = true
			f.prevRune = '\n'
		case k.kind == kInline:
			f.atoms = append(f.atoms, atom{kind: aOpen, box: k})
			if hasInlineEdge(k, true) {
				f.lastRune, f.lastFace = 0, nil
			}
			f.walk(k, k)
			f.atoms = append(f.atoms, atom{kind: aClose, box: k})
			if hasInlineEdge(k, false) {
				f.lastRune, f.lastFace = 0, nil
			}
		case k.kind == kAtomic:
			f.lastRune, f.lastFace = 0, nil
			f.atoms = append(f.atoms, atom{kind: aAtomic, box: k})
			f.prevSpace = false
			f.prevRune = 0xfffc
		case k.kind == kBlock:
			// should not happen in an IFC (blocks were wrapped); treat as atomic
			f.atoms = append(f.atoms, atom{kind: aAtomic, box: k})
			f.prevSpace = false
		}
	}
}

func (f *flatState) text(t *lbox, owner *lbox) {
	st := t.st
	if f.flRoot != nil {
		st = f.flStyleOf(owner)
	}
	s := t.text
	if st.TextTransform != "none" && st.TextTransform != "" {
		s = transformText(s, st.TextTransform, f.prevRune)
	}
	ws := st.WhiteSpace
	var out []rune
	switch ws {
	case "pre", "pre-wrap", "break-spaces":
		s = strings.ReplaceAll(s, "\r\n", "\n")
		out = []rune(s)
		if len(out) > 0 {
			f.prevSpace = false
			f.prevRune = out[len(out)-1]
		}
	default:
		keepNL := ws == "pre-line"
		out = make([]rune, 0, len(s))
		for _, r := range s {
			if r == '\n' && keepNL {
				for len(out) > 0 && out[len(out)-1] == ' ' && !f.prevSpaceFromOutside(len(out)) {
					out = out[:len(out)-1]
				}
				out = append(out, '\n')
				f.prevSpace = true
				continue
			}
			if r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\f' {
				if f.prevSpace {
					continue
				}
				out = append(out, ' ')
				f.prevSpace = true
				continue
			}
			out = append(out, r)
			f.prevSpace = false
		}
		if len(out) > 0 {
			f.prevRune = out[len(out)-1]
		}
	}
	if len(out) == 0 {
		return
	}
	if st.SmallCaps {
		// synthesized small caps: lowercase letters become capitals at 0.7 size
		small := f.c.smallCapsStyle(st)
		for a := 0; a < len(out); {
			lower := unicode.IsLower(out[a])
			e := a
			for e < len(out) && unicode.IsLower(out[e]) == lower {
				e++
			}
			seg := append([]rune(nil), out[a:e]...)
			sst := st
			if lower {
				for k, r := range seg {
					seg[k] = unicode.ToUpper(r)
				}
				sst = small
			}
			sfs := f.c.spec(sst)
			sh := f.c.shapeWith(sfs, sst, seg, f.lastRune, f.lastFace)
			f.lastRune, f.lastFace = seg[len(seg)-1], sh.faces[len(seg)-1]
			f.addTextAtoms(&textItem{owner: owner, src: t, runes: seg, sh: sh, fs: sfs, st: sst, off: a})
			a = e
		}
		return
	}
	fs := f.c.spec(st)
	sh := f.c.shapeWith(fs, st, out, f.lastRune, f.lastFace)
	if n := len(out); n > 0 {
		f.lastRune, f.lastFace = out[n-1], sh.faces[n-1]
	}
	it := &textItem{owner: owner, src: t, runes: out, sh: sh, fs: fs, st: st}
	f.addTextAtoms(it)
}

// smallCapsStyle is st with the font size of synthesized small capitals.
func (c *ctx) smallCapsStyle(st *style.Computed) *style.Computed {
	if c.smallCaps == nil {
		c.smallCaps = map[*style.Computed]*style.Computed{}
	}
	if s, ok := c.smallCaps[st]; ok {
		return s
	}
	cp := *st
	cp.FontSize = st.FontSize * 0.7
	c.smallCaps[st] = &cp
	return &cp
}

func (f *flatState) prevSpaceFromOutside(n int) bool { return false }

func (f *flatState) addTextAtoms(it *textItem) {
	rs := it.runes
	st := it.st
	ws := st.WhiteSpace
	collapse := ws == "normal" || ws == "nowrap" || ws == "pre-line"
	noWrap := ws == "nowrap" || ws == "pre" || st.TextWrap == "nowrap"
	start := 0
	n := len(rs)
	for start < n {
		end := start
		forced := false
		brk := false
		for end < n {
			r := rs[end]
			if r == '\n' && (ws == "pre" || ws == "pre-wrap" || ws == "pre-line" || ws == "break-spaces") {
				forced = true
				break
			}
			if r == '\t' && (ws == "pre" || ws == "pre-wrap" || ws == "break-spaces") {
				if end == start {
					end++
					brk = !noWrap && ws != "pre"
					break
				}
				brk = false
				break
			}
			if !noWrap && breakAfter(rs, end, st) {
				end++
				brk = true
				break
			}
			end++
		}
		if end > n {
			end = n
		}
		a := atom{kind: aText, it: it, a: start, e: end, brk: brk && !noWrap}
		if end > start && rs[start] == '\t' && end-start == 1 {
			a.tab = true
		}
		a.w = it.sh.x[end] - it.sh.x[start]
		// trailing spaces
		te := end
		for te > start && (rs[te-1] == ' ' || rs[te-1] == 0xad && false) {
			te--
		}
		if collapse || ws == "pre-wrap" || ws == "break-spaces" {
			if ws != "break-spaces" {
				a.trail = it.sh.x[end] - it.sh.x[te]
			}
		}
		if te == start && collapse && end > start {
			a.space = true
		}
		if rs[end-1] == 0xad && end-1 >= start {
			// soft hyphen: zero width unless the break happens here (handled when finishing the line)
		}
		f.atoms = append(f.atoms, a)
		if forced {
			f.atoms = append(f.atoms, atom{kind: aBreak, box: it.src, forced: true, it: it})
			end++
		}
		start = end
	}
}

// ---- line layout --------------------------------------------------------------------------

func lhOf(st *style.Computed, fs *fontSpec) float64 {
	lh := st.LineHeight
	switch lh.K {
	case style.LNumber:
		return floor64(lh.V * st.FontSize)
	case style.LPx:
		return floor64(lh.V)
	case style.LPct:
		return floor64(lh.V * st.FontSize / 100)
	}
	return fs.lineSp
}

type ibox struct {
	box         *lbox
	fs          *fontSpec
	ascL, descL float64
	shift       float64 // baseline offset (up positive) relative to root baseline
	used        bool
	valign      string
}

// layoutInlineContent lays out the inline formatting context of b. cx, cy are
// the BFC coordinates of b's content box. It returns the content height and
// positions atomic children and floats relative to b's content box.
func (c *ctx) layoutInlineContent(b *lbox, cw, cx, cy float64, bfc *bfcState, cbh float64) float64 {
	atoms := c.buildAtoms(b)
	b.lines = b.lines[:0]
	b.hasBase = false
	st := b.st
	strut := func(s *style.Computed) (*fontSpec, float64, float64) {
		fs := c.spec(s)
		lh := lhOf(s, fs)
		asc, desc := fs.asc, fs.desc
		half := (lh - (asc + desc)) / 2
		fl := math.Floor(half)
		asc += fl
		desc = lh - asc
		return fs, asc, desc
	}
	rootFS, rootAsc, rootDesc := strut(st)
	inFL := false
	var flRootFS *fontSpec
	var flAsc, flDesc float64
	var baseAtoms []atom
	if b.flSt != nil {
		inFL = true
		baseAtoms = atoms
		atoms = c.buildAtomsFL(b)
		flRootFS, flAsc, flDesc = strut(b.flSt)
		rootFS, rootAsc, rootDesc = flRootFS, flAsc, flDesc
		c.curFL = b
	}
	setAtoms := func(na []atom) {
		atoms = na
		if inFL {
			b.atomsFL = na
		} else {
			b.atoms = na
		}
	}
	defer func() { c.curFL = nil }()
	// resolve edges of inline boxes
	for _, a := range atoms {
		if a.kind == aOpen {
			c.edges(a.box, cw)
		}
	}
	y := 0.0
	i := 0
	first := true
	var openStack []*lbox
	justify := st.TextAlign == "justify" || st.TextAlign == "justify-all"
	indent := 0.0
	if st.TextIndent.K != style.LAuto {
		indent = rl(st.TextIndent, cw)
	}
	nlines := 0
	lastForced := true
	var lstarts []int
	var floatsDeferred []*lbox
	for i < len(atoms) {
		// leading skip: whitespace-only text at line start
		ln := &line{y: y}
		probeH := math.Max(rootAsc+rootDesc, 1)
		availL, availR := bfc.band(cy+y, cy+y+probeH, cx, cx+cw)
		ln.x, ln.w = availL-cx, availR-availL
		lineIndent := 0.0
		if (first || lastForced && st.TextIndentHang == false && false) && indent != 0 {
			lineIndent = indent
		}
		if st.TextIndentHang && !first {
			lineIndent = indent
		} else if st.TextIndentHang && first {
			lineIndent = 0
		}
		curX := lineIndent
		var items []atom
		type bp struct {
			n, next int
			x       float64
		}
		lastBP := bp{n: -1}
		hasContent := false
		lineOpen := append([]*lbox(nil), openStack...)
		var openAtBP []*lbox
		var lineOOF []*lbox
		_ = openAtBP
		endLine := false
		startI := i
		moved := false
		for i < len(atoms) && !endLine {
			a := atoms[i]
			if b.force != nil && b.force[i] && i > startI && (hasContent || len(items) > 0) {
				endLine = true
				continue
			}
			switch a.kind {
			case aOpen:
				bx := a.box
				curX += bx.mar[3] + bx.bor[3] + bx.pad[3]
				if bx.cont > 0 && false {
					curX += 0
				}
				items = append(items, a)
				lineOpen = append(lineOpen, bx)
				if bx.mar[3]+bx.bor[3]+bx.pad[3]+bx.mar[1]+bx.bor[1]+bx.pad[1] > 0 {
					hasContent = true
				}
				i++
			case aClose:
				bx := a.box
				curX += bx.mar[1] + bx.bor[1] + bx.pad[1]
				items = append(items, a)
				if len(lineOpen) > 0 {
					lineOpen = lineOpen[:len(lineOpen)-1]
				}
				i++
			case aOOF:
				a.box.staticX = b.bor[3] + b.pad[3] + ln.x + curX
				a.box.staticY = b.bor[0] + b.pad[0] + y
				a.box.lp = b
				a.box.staticSet = true
				if !a.box.st.OrigInline {
					a.box.staticX = b.bor[3] + b.pad[3]
					if hasContent {
						lineOOF = append(lineOOF, a.box)
					}
				}
				c.addOOF(a.box)
				i++
			case aFloat:
				fb := a.box
				c.edges(fb, cw)
				fin := layoutIn{cbw: cw, cbh: cbh, fixW: -1, fixH: -1, shrink: true, availW: cw, bfc: &bfcState{}}
				c.layoutBox(fb, fin)
				mw := fb.w + fb.mar[1] + fb.mar[3]
				mh := fb.h + fb.mar[0] + fb.mar[2]
				// fits on the current line?
				fitsHere := !hasContent && len(items) == 0 || curX+mw <= ln.w+0.01
				if fb.st.Clear != "none" {
					if cyc := bfc.clearY(fb.st.Clear); cyc > cy+y {
						fitsHere = false
					}
				}
				if fitsHere {
					px, py := bfc.place(mw, mh, cy+y, cx, cx+cw, fb.st.Float == "left")
					fb.x = px - cx + fb.mar[3]
					fb.y = py - cy + fb.mar[0]
					fb.lp = b
					b.hasFloat = true
					// the float narrows the current line
					nl, nr := bfc.band(cy+y, cy+y+probeH, cx, cx+cw)
					if fb.st.Float == "left" && nl-cx > ln.x {
						shift := nl - cx - ln.x
						ln.x = nl - cx
						ln.w = nr - nl
						_ = shift
					} else {
						ln.w = nr - nl
					}
				} else {
					floatsDeferred = append(floatsDeferred, fb)
				}
				i++
			case aBreak:
				items = append(items, a)
				hasContent = true
				i++
				endLine = true
				ln.forced = true
			case aAtomic:
				ab := a.box
				c.edges(ab, cw)
				if ab.kind == kBlock {
					// block inside inline context (should not occur)
				}
				if ab.hasLast == false || true {
					c.layoutAtomic(ab, cw, cbh)
				}
				mw := ab.w + ab.mar[1] + ab.mar[3]
				if hasContent && lastBP.n < 0 && len(items) > 0 {
					// allow a break before an atomic inline
				}
				if curX+mw > ln.w+0.01 && hasContent {
					// try to break before this atom
					if lastBP.n >= 0 {
						items = items[:lastBP.n+1]
						i = lastBP.next
						endLine = true
						continue
					}
					if c.hasTextOrAtomicContent(items) {
						// break right before the atomic inline
						endLine = true
						continue
					}
				}
				if !hasContent && curX+mw > ln.w+0.01 && !moved {
					if ny, ok := c.nextFloatEdge(bfc, cy+y, cx, cw, mw); ok {
						y = ny - cy
						ln.y = y
						availL, availR = bfc.band(cy+y, cy+y+probeH, cx, cx+cw)
						ln.x, ln.w = availL-cx, availR-availL
						continue
					}
				}
				// break opportunity before: remember position
				if hasContent {
					lastBP = bp{n: len(items) - 1, next: i, x: curX}
				}
				items = append(items, a)
				curX += mw
				hasContent = true
				// break opportunity after
				lastBP = bp{n: len(items) - 1, next: i + 1, x: curX}
				i++
			case aText:
				if a.space && !hasTextContent(items) && !c.hasAtomic(items) && !preservesSpace(a.it.st) {
					// collapsible space at line start
					i++
					continue
				}
				wfit := a.w - a.trail
				if a.tab {
					a.w = c.tabWidth(a, curX)
					wfit = a.w
				}
				if curX+wfit > ln.w+0.01 {
					if lastBP.n >= 0 && !(a.it.st.WhiteSpace == "nowrap") {
						items = items[:lastBP.n+1]
						i = lastBP.next
						endLine = true
						continue
					}
					if !hasTextContent(items) && !c.hasAtomic(items) && !moved {
						if ny, ok := c.nextFloatEdge(bfc, cy+y, cx, cw, wfit); ok {
							y = ny - cy
							ln.y = y
							availL, availR = bfc.band(cy+y, cy+y+probeH, cx, cx+cw)
							ln.x, ln.w = availL-cx, availR-availL
							continue
						}
					}
					// overflow-wrap / word-break: split the chunk
					if canSplitWord(a.it.st) && wfit > 0 {
						room := ln.w - curX
						if room < 0 {
							room = 0
						}
						if hasTextContent(items) && lastBP.n < 0 && !(preservesSpace(a.it.st) && a.it.st.WhiteSpace == "pre") {
							// the chunk follows unbreakable content; break at character level inside this chunk
						}
						cut := c.splitChunk(a, room, !hasTextContent(items) && !c.hasAtomic(items))
						if cut > a.a && cut < a.e {
							a1 := a
							a1.e = cut
							a1.w = a.it.sh.x[cut] - a.it.sh.x[a.a]
							a1.trail = 0
							a1.brk = true
							rest := a
							rest.a = cut
							rest.w = a.it.sh.x[a.e] - a.it.sh.x[cut]
							// replace atom i by the two parts
							na := make([]atom, 0, len(atoms)+1)
							na = append(na, atoms[:i]...)
							na = append(na, a1, rest)
							na = append(na, atoms[i+1:]...)
							setAtoms(na)
							continue
						}
					}
				}
				items = append(items, a)
				if !a.space || preservesSpace(a.it.st) {
					hasContent = true
				}
				curX += a.w
				if a.brk {
					lastBP = bp{n: len(items) - 1, next: i + 1, x: curX}
				} else if i+1 < len(atoms) && atoms[i+1].kind == aAtomic {
					lastBP = bp{n: len(items) - 1, next: i + 1, x: curX}
				}
				i++
			}
		}
		if i == startI && !endLine {
			break
		}
		if len(items) == 0 && !ln.forced && i >= len(atoms) {
			break
		}
		// an iteration that made no progress would loop forever; force one atom
		if i == startI && len(items) == 0 {
			i++
		}
		c.finishLine(b, ln, items, openStack, openStack, cw, curX-lineIndent, lineIndent, rootAsc, rootDesc, rootFS, justify && !ln.forced && i < len(atoms), first)
		if len(ln.frags) > 0 || ln.hasContent {
			ln.y = y
			for _, ob := range lineOOF {
				ob.staticY = b.bor[0] + b.pad[0] + y + ln.h
			}
			b.lines = append(b.lines, ln)
			lstarts = append(lstarts, startI)
			// place floats that were deferred below this line
			y += ln.h
			nlines++
			for _, fb := range floatsDeferred {
				mw := fb.w + fb.mar[1] + fb.mar[3]
				mh := fb.h + fb.mar[0] + fb.mar[2]
				px, py := bfc.place(mw, mh, cy+y, cx, cx+cw, fb.st.Float == "left")
				fb.x = px - cx + fb.mar[3]
				fb.y = py - cy + fb.mar[0]
				fb.lp = b
				b.hasFloat = true
			}
			floatsDeferred = nil
			first = false
			if inFL {
				// the remaining lines use the normal style
				inFL = false
				c.curFL = nil
				ncur := atoms
				var ni int
				ni = i
				nb, j := remapAtoms(ncur, ni, baseAtoms)
				if len(nb) != len(baseAtoms) {
					b.atoms = nb
				}
				atoms = nb
				i = j
				rootFS, rootAsc, rootDesc = strut(st)
			}
		}
		// recompute the open stack after this line
		openStack = c.stackAfter(openStack, items)
		lastForced = ln.forced
	}
	for _, fb := range floatsDeferred {
		mw := fb.w + fb.mar[1] + fb.mar[3]
		mh := fb.h + fb.mar[0] + fb.mar[2]
		px, py := bfc.place(mw, mh, cy+y, cx, cx+cw, fb.st.Float == "left")
		fb.x = px - cx + fb.mar[3]
		fb.y = py - cy + fb.mar[0]
		fb.lp = b
		b.hasFloat = true
	}
	if b.hasFloat {
		b.touchesBFC = true
	}
	// text-align and baseline of the block
	if len(b.lines) > 0 {
		b.hasBase = true
		b.baseline = b.lines[0].y + b.lines[0].baseline
		l := b.lines[len(b.lines)-1]
		b.lastBase = l.y + l.baseline
	}
	b.mtopOut, b.mbotOut = b.mar[0], b.mar[2]
	if (st.TextWrap == "balance" || st.TextWrap == "pretty") && len(b.lines) > 1 && !b.balancing && b.force == nil {
		plan, ok := c.planBreaks(b, atoms, lstarts, cw, indent, st.TextWrap, bfc)
		switch {
		case plan != nil:
			b.balancing = true
			b.force = plan
			y2 := c.layoutInlineContent(b, cw, cx, cy, bfc, cbh)
			b.force = nil
			b.balancing = false
			return y2
		case !ok && st.TextWrap == "balance" && len(b.lines) <= 6:
			c.balance(b, cw, cx, cy, bfc, cbh, len(b.lines), y)
		}
	}
	return y
}

func (c *ctx) balance(b *lbox, cw, cx, cy float64, bfc *bfcState, cbh float64, nlines int, y float64) {
	b.balancing = true
	defer func() { b.balancing = false }()
	lo, hi := 0.0, cw
	best := cw
	savedAtoms := b.atoms
	for it := 0; it < 14 && hi-lo > 1; it++ {
		mid := (lo + hi) / 2
		b.atoms = savedAtoms
		n := len(c.countLines(b, mid, cx, cy, bfc, cbh))
		if n <= nlines {
			best = mid
			hi = mid
		} else {
			lo = mid
		}
	}
	b.atoms = savedAtoms
	if best < cw {
		// relayout at the balanced width but keep the box width
		save := *bfc
		_ = save
		c.layoutNarrow(b, best, cw, cx, cy, bfc, cbh)
	}
}

func (c *ctx) countLines(b *lbox, w, cx, cy float64, bfc *bfcState, cbh float64) []*line {
	savedLines := b.lines
	b.lines = nil
	nb := &bfcState{floats: append([]floatRec(nil), bfc.floats...), lastTop: bfc.lastTop}
	c.layoutInlineWidth(b, w, cx, cy, nb, cbh)
	ls := b.lines
	b.lines = savedLines
	return ls
}

func (c *ctx) layoutNarrow(b *lbox, w, cw, cx, cy float64, bfc *bfcState, cbh float64) {
	c.layoutInlineWidth(b, w, cx, cy, bfc, cbh)
	// center narrowed lines according to text-align
	if b.st.TextAlign == "center" || b.st.TextAlign == "-webkit-center" {
		for _, l := range b.lines {
			l.x += (cw - w) / 2
		}
	}
}

func (c *ctx) layoutInlineWidth(b *lbox, w, cx, cy float64, bfc *bfcState, cbh float64) {
	b.balancing = true
	c.layoutInlineContent(b, w, cx, cy, bfc, cbh)
	b.balancing = false
}

func preservesSpace(st *style.Computed) bool {
	switch st.WhiteSpace {
	case "pre", "pre-wrap", "break-spaces":
		return true
	}
	return false
}

func hasTextContent(items []atom) bool {
	for _, a := range items {
		if a.kind == aText && (!a.space || preservesSpace(a.it.st)) || a.kind == aBreak {
			return true
		}
	}
	return false
}

func (c *ctx) hasAtomic(items []atom) bool {
	for _, a := range items {
		if a.kind == aAtomic {
			return true
		}
	}
	return false
}

func (c *ctx) hasTextOrAtomicContent(items []atom) bool {
	return hasTextContent(items) || c.hasAtomic(items)
}

func canSplitWord(st *style.Computed) bool {
	return st.OverflowWrap == "break-word" || st.OverflowWrap == "anywhere" || st.WordBreak == "break-word" || st.WordBreak == "break-all"
}

// splitChunk returns the rune index at which chunk a can be cut to fit room.
func (c *ctx) splitChunk(a atom, room float64, emptyLine bool) int {
	sh := a.it.sh
	cut := a.a
	for k := a.a + 1; k <= a.e; k++ {
		if sh.x[k]-sh.x[a.a] > room+0.01 {
			break
		}
		cut = k
	}
	if cut == a.a && emptyLine {
		cut = a.a + 1
	}
	return cut
}

func (c *ctx) tabWidth(a atom, curX float64) float64 {
	st := a.it.st
	ts := st.TabSize
	if ts == 0 {
		ts = 8
	}
	var tw float64
	if st.TabSizePx {
		tw = ts
	} else {
		tw = ts * (a.it.fs.spaceW + st.LetterSpacing + st.WordSpacing)
	}
	if tw <= 0 {
		return 0
	}
	next := (math.Floor(curX/tw+1e-9) + 1) * tw
	w := next - curX
	if w < a.it.fs.spaceW*0.0001 {
		w = tw
	}
	return w
}

// nextFloatEdge finds the next y below which a box of width w fits.
func (c *ctx) nextFloatEdge(bfc *bfcState, y, cx, cw, w float64) (float64, bool) {
	next := math.Inf(1)
	for _, f := range bfc.floats {
		if f.y1 > y && f.y1 < next && f.y1 > f.y0 {
			next = f.y1
		}
	}
	if math.IsInf(next, 1) {
		return 0, false
	}
	if w > cw {
		// can never fit; only move below the floats if the band is narrowed
		l, r := bfc.band(y, y+1, cx, cx+cw)
		if r-l >= cw {
			return 0, false
		}
	}
	return next, true
}

func (c *ctx) stackAfter(stack []*lbox, items []atom) []*lbox {
	st := append([]*lbox(nil), stack...)
	for _, a := range items {
		switch a.kind {
		case aOpen:
			st = append(st, a.box)
		case aClose:
			if len(st) > 0 {
				st = st[:len(st)-1]
			}
		}
	}
	return st
}

// layoutAtomic lays out an atomic inline-level box (inline-block, replaced, ...).
func (c *ctx) layoutAtomic(ab *lbox, cw, cbh float64) {
	in := layoutIn{cbw: cw, cbh: cbh, fixW: -1, fixH: -1, shrink: true, availW: cw, bfc: &bfcState{}}
	c.layoutBox(ab, in)
}

// finishLine positions the accepted atoms of one line and computes its metrics.
func (c *ctx) finishLine(b *lbox, ln *line, items []atom, openAtStart, openStack []*lbox, cw, usedW, indent, rootAsc, rootDesc float64, rootFS *fontSpec, justify, first bool) {
	st := b.st
	rtlBase := st.Direction == "rtl"
	if st.UnicodeBidi == "plaintext" {
		rtlBase = paraRTL(b, b.atoms)
	}
	// remove trailing collapsible space from the last text atom
	lastText := -1
	endsWithContent := false
	for i := len(items) - 1; i >= 0; i-- {
		a := items[i]
		if a.kind == aClose || a.kind == aOpen {
			continue
		}
		if a.kind == aText {
			lastText = i
		}
		endsWithContent = a.kind != aBreak
		_ = endsWithContent
		break
	}
	// positions
	x := indent
	var frs []*frag
	stack := append([]*lbox(nil), openAtStart[:min(len(openAtStart), len(openAtStart))]...)
	_ = stack
	// metrics per inline box
	boxes := map[*lbox]*ibox{}
	var order []*lbox
	getBox := func(bx *lbox) *ibox {
		if ib, ok := boxes[bx]; ok {
			return ib
		}
		bst := bx.st
		if c.curFL != nil {
			if o, ok := c.curFL.flMap[bx]; ok {
				bst = o
			}
		}
		fs := c.spec(bst)
		lh := lhOf(bst, fs)
		asc, desc := fs.asc, fs.desc
		half := math.Floor((lh - (asc + desc)) / 2)
		asc += half
		desc = lh - asc
		ib := &ibox{box: bx, fs: fs, ascL: asc, descL: desc, valign: bst.VerticalAlign}
		boxes[bx] = ib
		order = append(order, bx)
		return ib
	}
	rootIB := &ibox{box: b, fs: rootFS, ascL: rootAsc, descL: rootDesc, used: true}
	boxes[b] = rootIB
	// continuing boxes from previous lines
	var cur []*lbox
	cur = append(cur, openAtStart...)
	startX := map[*lbox]float64{}
	contL := map[*lbox]bool{}
	for _, bx := range cur {
		startX[bx] = 0
		contL[bx] = true
		getBox(bx)
	}
	var pl []placed
	nsp := 0
	hasBreak := false
	brOwners := map[*lbox]bool{}
	for idx, a := range items {
		owner := b
		if len(cur) > 0 {
			owner = cur[len(cur)-1]
		}
		switch a.kind {
		case aOpen:
			bx := a.box
			x += bx.mar[3]
			startX[bx] = x
			x += bx.bor[3] + bx.pad[3]
			cur = append(cur, bx)
			getBox(bx)
		case aClose:
			bx := a.box
			x += bx.bor[1] + bx.pad[1]
			ib := getBox(bx)
			ib.used = true
			ln.boxes = append(ln.boxes, lineBox{box: bx, x0: startX[bx], x1: x, contL: contL[bx]})
			x += bx.mar[1]
			if len(cur) > 0 {
				cur = cur[:len(cur)-1]
			}
			delete(startX, bx)
		case aText:
			w := a.w
			trail := 0.0
			if idx == lastText && (a.trail > 0) {
				// trailing collapsible spaces hang / are removed
				tail := true
				for _, o := range items[idx+1:] {
					if o.kind != aClose && o.kind != aOpen {
						tail = false
					}
				}
				if tail {
					if a.it.st.WhiteSpace == "pre-wrap" {
						trail = a.trail
					} else {
						w -= a.trail
						trail = a.trail
					}
				}
			}
			ib := rootIB
			if owner != b {
				ib = getBox(owner)
			}
			ib.used = true
			fr := &frag{kind: aText, it: a.it, a: a.a, e: a.e, x: x, w: w, trail: trail, box: owner}
			nsp += fr.justSpaces()
			frs = append(frs, fr)
			pl = append(pl, placed{fr: fr, ib: ib})
			x += w
			if a.a < a.e && !a.space {
				ln.hasContent = true
			}
		case aAtomic:
			ab := a.box
			x += ab.mar[3]
			fr := &frag{kind: aAtomic, box: ab, x: x, w: ab.w}
			x += ab.w + ab.mar[1]
			ib := rootIB
			if owner != b {
				ib = getBox(owner)
			}
			ib.used = true
			frs = append(frs, fr)
			pl = append(pl, placed{fr: fr, ib: ib, atom: ab})
			ln.hasContent = true
		case aBreak:
			hasBreak = true
			ln.hasContent = true
			ib := rootIB
			if owner != b {
				ib = getBox(owner)
			}
			ib.used = true
			brOwners[owner] = true
			if a.box != nil && a.box.kind == kBreak {
				fr := &frag{kind: aBreak, box: a.box, x: x}
				frs = append(frs, fr)
				pl = append(pl, placed{fr: fr, ib: ib})
			}
		}
	}
	// boxes still open at the end of the line continue on the next one
	for _, bx := range cur {
		ib := getBox(bx)
		ln.boxes = append(ln.boxes, lineBox{box: bx, x0: startX[bx], x1: x, contL: contL[bx], contR: true})
		_ = ib
	}
	_ = hasBreak
	if lineHasBidi(items) {
		var nx float64
		pl, ln.boxes, nx = reorderLine(items, pl, indent, rtlBase, ln.boxes)
		frs = frs[:0]
		for _, p := range pl {
			frs = append(frs, p.fr)
		}
		x = nx
	}
	ln.contentW = x - indent
	if len(frs) == 0 && !ln.hasContent {
		// check inline boxes with edges
		edge := false
		for _, a := range items {
			if a.kind == aOpen || a.kind == aClose {
				edge = edge || a.box.hExtra()+a.box.mar[1]+a.box.mar[3] > 0
			}
		}
		if !edge {
			return
		}
		ln.hasContent = true
	}
	// text-align
	avail := ln.w
	free := avail - x
	if x > avail {
		free = 0
		if first && false {
			free = 0
		}
	}
	shift := 0.0
	align := st.TextAlign
	lineLast := !justify
	if align == "justify" || align == "justify-all" {
		if lineLast {
			align = "start"
			if st.TextAlignLast != "" && st.TextAlignLast != "auto" {
				align = st.TextAlignLast
			}
		}
	}
	if align == "start" || align == "match-parent" {
		if rtlBase {
			align = "right"
		} else {
			align = "left"
		}
	}
	if align == "end" {
		if rtlBase {
			align = "left"
		} else {
			align = "right"
		}
	}
	switch align {
	case "center", "-webkit-center", "-internal-center":
		shift = free / 2
	case "right", "-webkit-right":
		shift = free
	case "justify", "justify-all":
		if nsp > 0 && free > 0 {
			extra := free / float64(nsp)
			acc := 0.0
			for _, p := range pl {
				if p.fr.kind == aText {
					p.fr.x += acc
					// a hanging trailing space is not justified
					cnt := p.fr.justSpaces()
					p.fr.justSp = extra
					p.fr.nsp = cnt
					add := extra * float64(cnt)
					p.fr.w += add
					acc += add
				} else {
					p.fr.x += acc
				}
			}
			for i := range ln.boxes {
				ln.boxes[i].x1 += acc
			}
			x += acc
			ln.contentW = x - indent
		}
	}
	if shift < 0 {
		shift = 0
	}
	if align == "right" || align == "center" || align == "-webkit-center" || align == "-webkit-right" || align == "-internal-center" {
		if free < 0 {
			shift = 0
		}
	}
	if shift != 0 {
		for _, p := range pl {
			p.fr.x += shift
		}
		for i := range ln.boxes {
			ln.boxes[i].x0 += shift
			ln.boxes[i].x1 += shift
		}
	}
	_ = shift
	// fonts actually used: with line-height normal, fallback fonts widen the inline box
	for _, p := range pl {
		if p.fr.kind != aText || p.ib == nil {
			continue
		}
		ib := p.ib
		if ib.box.st.LineHeight.K != style.LAuto || ib.fs.primary == nil {
			continue
		}
		var seen [4]*font.Face
		ns := 0
		for k := p.fr.a; k < p.fr.e; k++ {
			f := p.fr.it.sh.faces[k]
			if f == nil || f == ib.fs.primary {
				continue
			}
			dup := false
			for _, s := range seen[:ns] {
				if s == f {
					dup = true
				}
			}
			if dup {
				continue
			}
			if ns < len(seen) {
				seen[ns] = f
				ns++
			}
			m := f.Metrics(ib.fs.size)
			asc, desc, gap := round(m.Ascent), round(m.Descent), round(m.LineGap)
			lsp := asc + desc + gap
			a2 := asc + math.Floor((lsp-(asc+desc))/2)
			d2 := lsp - a2
			ib.ascL = math.Max(ib.ascL, a2)
			ib.descL = math.Max(ib.descL, d2)
		}
	}
	// vertical metrics
	rootIB.shift = 0
	// compute shift for each box (parent chain by DOM order: use box.parent chain)
	var calcShift func(bx *lbox) float64
	calcShift = func(bx *lbox) float64 {
		if bx == b || bx == nil {
			return 0
		}
		ib := getBox(bx)
		par := bx.parent
		for par != nil && par != b && par.kind != kInline {
			par = par.parent
		}
		var pib *ibox
		if par == nil || par == b {
			pib = rootIB
		} else {
			pib = getBox(par)
		}
		base := calcShift(par)
		ib.shift = base + vshift(bx, ib, pib)
		return ib.shift
	}
	for _, bx := range order {
		calcShift(bx)
	}
	T, B := rootIB.ascL, -rootIB.descL
	directText := map[*lbox]bool{}
	if c.quirks {
		for _, p := range pl {
			if p.fr.kind == aText {
				if p.fr.it != nil && allSpaces(p.fr.it.runes[p.fr.a:p.fr.e]) {
					continue
				}
				directText[p.fr.box] = true
			}
		}
		if len(directText) == 0 {
			for o := range brOwners {
				directText[o] = true
			}
		}
		if !directText[b] {
			T, B = 0, 0
		}
	}
	atomTop := func(p placed) (float64, float64, float64, string) {
		ab := p.atom
		mh := ab.h + ab.mar[0] + ab.mar[2]
		return mh, atomBO(ab), 0, ab.st.VerticalAlign
	}
	type vt struct {
		h    float64
		kind string
		fr   *frag
		p    placed
	}
	var tops []vt
	for _, p := range pl {
		ib := p.ib
		if p.atom != nil {
			mh, bo, _, va := atomTop(p)
			vshiftA := 0.0
			switch va {
			case "top", "bottom":
				tops = append(tops, vt{h: mh, kind: va, fr: p.fr, p: p})
				continue
			case "baseline":
			case "middle":
				vshiftA = ib.fs.xh/2 + mh/2 - bo
			case "sub":
				vshiftA = -(ib.fs.size/5 + 1)
			case "super":
				vshiftA = ib.fs.size/3 + 1
			case "text-top":
				vshiftA = ib.fs.asc - bo
			case "text-bottom":
				vshiftA = -ib.fs.desc + (mh - bo)
			case "length":
				vshiftA = c.vaLen(p.atom.st, ib.fs, mh)
			}
			s := ib.shift + vshiftA
			p.fr.base = s
			T = math.Max(T, s+bo)
			B = math.Min(B, s-(mh-bo))
			continue
		}
		if p.fr.kind == aText || p.fr.kind == aBreak {
			// text contributes through its inline box
		}
	}
	for _, bx := range order {
		ib := boxes[bx]
		if !ib.used && !c.boxHasEdges(bx) {
			continue
		}
		if c.quirks && !directText[bx] {
			continue
		}
		if ib.valign == "top" || ib.valign == "bottom" {
			tops = append(tops, vt{h: ib.ascL + ib.descL, kind: ib.valign})
			continue
		}
		T = math.Max(T, ib.shift+ib.ascL)
		B = math.Min(B, ib.shift-ib.descL)
	}
	H := T - B
	for _, t := range tops {
		if t.h > H {
			extra := t.h - H
			if t.kind == "top" {
				B -= extra
			} else {
				T += extra
			}
			H = t.h
		}
	}
	ln.h = H
	ln.baseline = T
	for _, t := range tops {
		if t.fr != nil {
			if t.kind == "top" {
				t.fr.base = T - (t.p.atom.baseline + t.p.atom.mar[0])
				if !t.p.atom.hasBase {
					t.fr.base = T - (t.h)
				}
				// place the margin box at the line top
				bo := atomBO(t.p.atom)
				t.fr.base = T - bo
			} else {
				bo := atomBO(t.p.atom)
				t.fr.base = B + (t.h - bo)
			}
		}
	}
	for _, bx := range order {
		ib := boxes[bx]
		if ib.valign == "top" {
			ib.shift = T - ib.ascL
		} else if ib.valign == "bottom" {
			ib.shift = B + ib.descL
		}
	}
	for _, p := range pl {
		if p.atom == nil {
			if p.ib == rootIB {
				p.fr.base = 0
			} else {
				p.fr.base = p.ib.shift
			}
		}
	}
	for i := range ln.boxes {
		lb := &ln.boxes[i]
		lb.base = boxes[lb.box].shift
	}
	ln.frags = frs
	if len(ln.frags) == 0 && !ln.hasContent {
		ln.h = 0
	}
	ln.x += 0
	// store fragment baselines relative to the line top
	for _, fr := range ln.frags {
		fr.base = T - fr.base
	}
	for i := range ln.boxes {
		ln.boxes[i].base = T - ln.boxes[i].base
		if ib := boxes[ln.boxes[i].box]; ib != nil {
			_ = ib
		}
	}
	// position atomic boxes and record float-free results on boxes
	for _, fr := range ln.frags {
		if fr.kind == aAtomic {
			ab := fr.box
			bo := atomBO(ab)
			ab.x = ln.x + fr.x
			ab.y = ln.y + fr.base - bo + ab.mar[0]
			ab.lp = b
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (c *ctx) boxHasEdges(bx *lbox) bool {
	return bx.hExtra()+bx.mar[1]+bx.mar[3] > 0
}

func (c *ctx) vaLen(st *style.Computed, fs *fontSpec, mh float64) float64 {
	l := st.VerticalAlignLen
	if l.K == style.LPct {
		return l.V * lhOf(st, fs) / 100
	}
	return l.Resolve(0)
}

// vshift computes the baseline shift (up positive) of an inline box relative to its parent.
func vshift(bx *lbox, ib, parent *ibox) float64 {
	switch bx.st.VerticalAlign {
	case "sub":
		return -(parent.fs.size/5 + 1)
	case "super":
		return parent.fs.size/3 + 1
	case "middle":
		// center of box at parent baseline + x-height/2
		mid := (ib.ascL - ib.descL) / 2
		return parent.fs.xh/2 - mid
	case "text-top":
		return parent.fs.asc - ib.fs.asc - (ib.ascL - ib.fs.asc)
	case "text-bottom":
		return -parent.fs.desc + ib.fs.desc + (ib.descL - ib.fs.desc)
	case "length":
		l := bx.st.VerticalAlignLen
		if l.K == style.LPct {
			return l.V * lhOf(bx.st, ib.fs) / 100
		}
		return l.Resolve(0)
	}
	return 0
}

func runesToString(r []rune) string { return string(r) }

var _ = utf8.RuneLen
var _ = font.Style{}

// atomBO is the baseline offset of an atomic inline from its margin-box top.
func atomBO(ab *lbox) float64 {
	mh := ab.h + ab.mar[0] + ab.mar[2]
	switch {
	case ab.inner == iControl:
		return ab.ctrlBase + ab.mar[0]
	case ab.inner == iReplaced:
		return mh
	case ab.inner == iFlow:
		if ab.hasBase && ab.overflowY() == "visible" {
			return ab.lastBase + ab.mar[0]
		}
		return mh
	}
	if ab.hasBase {
		return ab.baseline + ab.mar[0]
	}
	return mh
}

func allSpaces(r []rune) bool {
	for _, x := range r {
		if x != ' ' && x != '\t' && x != '\n' {
			return false
		}
	}
	return true
}

// hasInlineEdge reports whether an inline box has horizontal margin, border or
// padding on its start (or end) side; such edges end a shaping run.
func hasInlineEdge(b *lbox, start bool) bool {
	st := b.st
	i := 1
	if start {
		i = 3
	}
	if st.BorderWidth[i] > 0 {
		return true
	}
	for _, l := range []style.Length{st.Margin[i], st.Padding[i]} {
		if l.K == style.LPct || l.K == style.LCalc || l.V != 0 {
			return true
		}
	}
	return false
}

// justSpaces counts the spaces of a fragment that take part in justification:
// a removed trailing space at the end of the line does not.
func (fr *frag) justSpaces() int {
	e := fr.e
	if fr.trail > 0 && fr.it.st.WhiteSpace != "pre-wrap" {
		for e > fr.a && fr.it.runes[e-1] == ' ' {
			e--
		}
	}
	n := 0
	for k := fr.a; k < e; k++ {
		if fr.it.runes[k] == ' ' {
			n++
		}
	}
	return n
}
