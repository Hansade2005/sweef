package render

import (
	"sort"
	"unicode"

	"github.com/sweefbrowser/sweef/internal/font"
	"github.com/sweefbrowser/sweef/internal/style"
)

// Bidirectional text in inline formatting contexts (UAX #9).
//
// paraRTL is the base direction of the paragraph of IFC root b: its
// direction, or for unicode-bidi: plaintext the direction of the first
// strong character.
func paraRTL(b *lbox, atoms []atom) bool {
	if b.st.UnicodeBidi == "plaintext" {
		for _, a := range atoms {
			if a.kind != aText {
				continue
			}
			for _, r := range a.it.runes[a.a:a.e] {
				if font.IsRTLRune(r) {
					return true
				}
				if unicode.IsLetter(r) {
					return false
				}
			}
		}
	}
	return b.st.Direction == "rtl"
}

// assignBidi resolves embedding levels for the whole paragraph (the IFC) once
// after flattening; reorderLine applies rule L2 to the pieces of one line.
// Contexts without right-to-left content and with direction: ltr never get
// levels, so ordinary text takes the unchanged left-to-right path.

const (
	chLRE = '‪'
	chRLE = '‫'
	chPDF = '‬'
	chLRO = '‭'
	chRLO = '‮'
	chLRI = '⁦'
	chRLI = '⁧'
	chFSI = '⁨'
	chPDI = '⁩'
)

func isBidiControl(r rune) bool {
	return r >= 0x202a && r <= 0x202e || r >= 0x2066 && r <= 0x2069 || r == 0x200e || r == 0x200f || r == 0x061c
}

// bidiMode resolves the effective unicode-bidi of an inline box, including
// the user agent defaults of bdi and bdo.
func bidiMode(bx *lbox) (mode string, rtl bool) {
	rtl = bx.st.Direction == "rtl"
	mode = bx.st.UnicodeBidi
	if bx.n != nil && (mode == "" || mode == "normal") {
		switch bx.n.Data {
		case "bdi":
			mode = "isolate"
			hasDir := false
			for _, a := range bx.n.Attr {
				if a.Key == "dir" && (a.Val == "ltr" || a.Val == "rtl") {
					hasDir = true
				}
			}
			if !hasDir {
				mode = "plaintext"
			}
		case "bdo":
			mode = "isolate-override"
		}
	}
	return
}

func bidiOpen(mode string, rtl bool) []rune {
	pick := func(l, r rune) rune {
		if rtl {
			return r
		}
		return l
	}
	switch mode {
	case "embed":
		return []rune{pick(chLRE, chRLE)}
	case "bidi-override":
		return []rune{pick(chLRO, chRLO)}
	case "isolate":
		return []rune{pick(chLRI, chRLI)}
	case "isolate-override":
		return []rune{pick(chLRI, chRLI), pick(chLRO, chRLO)}
	case "plaintext":
		return []rune{chFSI}
	}
	return nil
}

func bidiClose(mode string) []rune {
	switch mode {
	case "embed", "bidi-override":
		return []rune{chPDF}
	case "isolate", "plaintext":
		return []rune{chPDI}
	case "isolate-override":
		return []rune{chPDF, chPDI}
	}
	return nil
}

// assignBidi resolves bidi levels for the atoms of IFC root b and stores
// them on the text items and atomic atoms. It does nothing for contexts that
// are entirely left to right.
func (c *ctx) assignBidi(b *lbox, atoms []atom) {
	base := paraRTL(b, atoms)
	need := base
	if !need {
		for _, a := range atoms {
			switch a.kind {
			case aText:
				for _, r := range a.it.runes[a.a:a.e] {
					if font.IsRTLRune(r) || isBidiControl(r) {
						need = true
					}
				}
			case aOpen:
				if m, _ := bidiMode(a.box); m != "" && m != "normal" {
					need = true
				}
			}
			if need {
				break
			}
		}
	}
	if !need {
		return
	}
	var P []rune
	type span struct {
		a    atom
		at   int // offset in P
		idx  int
		kind atomKind
	}
	var spans []span
	var modes []string
	for i, a := range atoms {
		switch a.kind {
		case aText:
			spans = append(spans, span{a: a, at: len(P), idx: i, kind: aText})
			P = append(P, a.it.runes[a.a:a.e]...)
		case aAtomic:
			spans = append(spans, span{at: len(P), idx: i, kind: aAtomic})
			P = append(P, 0xfffc)
		case aBreak:
			P = append(P, 0x2028)
		case aOpen:
			m, rtl := bidiMode(a.box)
			modes = append(modes, m)
			P = append(P, bidiOpen(m, rtl)...)
		case aClose:
			m := ""
			if len(modes) > 0 {
				m = modes[len(modes)-1]
				modes = modes[:len(modes)-1]
			}
			P = append(P, bidiClose(m)...)
		}
	}
	lv := font.BidiLevels(P, base)
	bl := uint8(0)
	if base {
		bl = 1
	}
	for _, sp := range spans {
		if sp.kind == aAtomic {
			atoms[sp.idx].lv, atoms[sp.idx].bidi = lv[sp.at], true
			continue
		}
		it := sp.a.it
		if it.lv == nil {
			it.lv = make([]uint8, len(it.runes))
			for k := range it.lv {
				it.lv[k] = bl
			}
		}
		copy(it.lv[sp.a.a:sp.a.e], lv[sp.at:sp.at+(sp.a.e-sp.a.a)])
	}
	// embedding level of boxes with a unicode-bidi embedding: their edges sit
	// at that level, around the (possibly nested) content
	stack := []uint8{bl}
	for i := range atoms {
		switch atoms[i].kind {
		case aText:
			atoms[i].bidi = true
		case aOpen:
			top := stack[len(stack)-1]
			m, rtl := bidiMode(atoms[i].box)
			if m == "" || m == "normal" {
				stack = append(stack, top)
				continue
			}
			if m == "plaintext" {
				rtl = firstStrongRTLAfter(atoms, i)
			}
			var nl uint8
			if rtl {
				nl = (top + 1) | 1
			} else {
				nl = (top + 2) &^ 1
			}
			stack = append(stack, nl)
			atoms[i].lv, atoms[i].bidi = nl, true
		case aClose:
			if len(stack) > 1 {
				top := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				if m, _ := bidiMode(atoms[i].box); m != "" && m != "normal" {
					atoms[i].lv, atoms[i].bidi = top, true
				}
			}
		}
	}
}

// firstStrongRTLAfter reports whether the first strong character after atom i is right to left.
func firstStrongRTLAfter(atoms []atom, i int) bool {
	for _, a := range atoms[i+1:] {
		if a.kind != aText {
			continue
		}
		for _, r := range a.it.runes[a.a:a.e] {
			if font.IsRTLRune(r) {
				return true
			}
			if unicode.IsLetter(r) {
				return false
			}
		}
	}
	return false
}

var mirrorPairs = map[rune]rune{'(': ')', ')': '(', '[': ']', ']': '[', '{': '}', '}': '{', '<': '>', '>': '<',
	'«': '»', '»': '«', '‹': '›', '›': '‹', '⟨': '⟩', '⟩': '⟨', '「': '」', '」': '「', '『': '』', '』': '『'}

func mirrorRune(r rune) rune {
	if m, ok := mirrorPairs[r]; ok {
		return m
	}
	return r
}

// piece is one unit of a line for visual reordering: a (part of a) text
// fragment, an atomic inline with its margins, or the start or end edge of an
// inline box. Edges carry the paragraph level, like open and close tags in
// Chromium's inline item list, so that a box's start edge stays on the box's
// start side even around content of the opposite direction.
type piece struct {
	fr     *frag
	ib     *ibox
	atom   *lbox
	lv     uint8
	edge   *lbox
	open   bool // edge is the start edge of its box
	inner  bool // the box has a unicode-bidi embedding: its edges sit inside it
	edgeW  float64
	mL, mR float64 // margin part of an edge on its physical left / right
	cx, cw float64 // content position and width (visual)
	x0, x1 float64 // full extent including margins / edge
}

func startEdge(bx *lbox) float64 {
	if bx.st.Direction == "rtl" {
		return bx.mar[1] + bx.bor[1] + bx.pad[1]
	}
	return bx.mar[3] + bx.bor[3] + bx.pad[3]
}

func endEdge(bx *lbox) float64 {
	if bx.st.Direction == "rtl" {
		return bx.mar[3] + bx.bor[3] + bx.pad[3]
	}
	return bx.mar[1] + bx.bor[1] + bx.pad[1]
}

// lineHasBidi reports whether any text of the line carries bidi levels.
func lineHasBidi(items []atom) bool {
	for _, a := range items {
		if a.kind == aText && a.bidi {
			return true
		}
	}
	return false
}

var _ = style.LAuto

// placed is a fragment of a line with its inline box metrics.
type placed struct {
	fr   *frag
	ib   *ibox
	atom *lbox
}

// reorderLine lays the logical-order fragments of one line out in visual
// order (rule L2). Text fragments are split where the embedding level
// changes. It returns the fragments in visual order, the line's inline box
// fragments (a box whose pieces are visually separated yields several) and
// the used width.
func reorderLine(items []atom, pl []placed, indent float64, base bool, boxes []lineBox) ([]placed, []lineBox, float64) {
	bl := uint8(0)
	if base {
		bl = 1
	}
	var pcs []*piece
	first := map[*lbox]int{}
	last := map[*lbox]int{}
	pi := 0
	for _, a := range items {
		switch a.kind {
		case aOpen:
			first[a.box] = len(pcs)
			pc := &piece{edge: a.box, edgeW: startEdge(a.box), lv: bl, open: true, inner: boxEmbeds(a.box)}
			if pc.inner && a.bidi {
				pc.lv = a.lv
			}
			if a.box.st.Direction == "rtl" {
				pc.mR = a.box.mar[1]
			} else {
				pc.mL = a.box.mar[3]
			}
			pcs = append(pcs, pc)
		case aClose:
			pc := &piece{edge: a.box, edgeW: endEdge(a.box), lv: bl, inner: boxEmbeds(a.box)}
			if pc.inner && a.bidi {
				pc.lv = a.lv
			}
			if a.box.st.Direction == "rtl" {
				pc.mL = a.box.mar[3]
			} else {
				pc.mR = a.box.mar[1]
			}
			pcs = append(pcs, pc)
			last[a.box] = len(pcs) - 1
		case aText:
			p0 := pl[pi]
			pi++
			fr := p0.fr
			it := fr.it
			k0 := fr.a
			used := 0.0
			for k0 < fr.e {
				k1 := fr.e
				lvl := bl
				if it.lv != nil {
					lvl = it.lv[k0]
					k1 = k0 + 1
					for k1 < fr.e && it.lv[k1] == lvl {
						k1++
					}
				}
				nf := *fr
				nf.a, nf.e = k0, k1
				if k1 == fr.e {
					nf.w = fr.w - used
				} else {
					nf.w = it.sh.x[k1] - it.sh.x[k0]
					nf.trail = 0
				}
				used += nf.w
				pcs = append(pcs, &piece{fr: &nf, ib: p0.ib, lv: lvl})
				k0 = k1
			}
			if fr.a >= fr.e {
				pcs = append(pcs, &piece{fr: fr, ib: p0.ib, lv: bl})
			}
		case aAtomic:
			p0 := pl[pi]
			pi++
			pcs = append(pcs, &piece{fr: p0.fr, ib: p0.ib, atom: p0.atom, lv: a.lv})
		case aBreak:
			if a.box != nil && a.box.kind == kBreak {
				p0 := pl[pi]
				pi++
				pcs = append(pcs, &piece{fr: p0.fr, ib: p0.ib, lv: bl})
			}
		}
	}
	// an open edge takes the level of the content before it and a close edge
	// that of the content after it (the paragraph level at the ends of the
	// line): the box sits in its surroundings, which keeps runs of one
	// direction together and a box's edges on its own start and end sides
	prevLv := bl
	for _, p := range pcs {
		if p.edge == nil {
			prevLv = p.lv
		} else if p.open && !p.inner {
			p.lv = prevLv // open edge of a plain box
		}
	}
	nextLv := bl
	for k := len(pcs) - 1; k >= 0; k-- {
		p := pcs[k]
		if p.edge == nil {
			nextLv = p.lv
		} else if !p.open && !p.inner {
			p.lv = nextLv // close edge of a plain box
		}
	}
	// L1: trailing white space takes the paragraph level
	for k := len(pcs) - 1; k >= 0; k-- {
		p := pcs[k]
		if p.edge != nil || p.fr.kind == aBreak {
			continue
		}
		if p.fr.kind != aText || !allSpaces(p.fr.it.runes[p.fr.a:p.fr.e]) {
			break
		}
		p.lv = bl
	}
	levels := make([]uint8, len(pcs))
	for i, p := range pcs {
		levels[i] = p.lv
	}
	order := font.ReorderVisual(levels)
	x := indent
	out := make([]placed, 0, len(pl))
	vis := make([]int, len(pcs)) // visual position of each logical piece
	for vi, li := range order {
		vis[li] = vi
		p := pcs[li]
		p.x0 = x
		switch {
		case p.edge != nil:
			x += p.edgeW
		case p.atom != nil:
			x += p.atom.mar[3]
			p.fr.x = x
			p.cx, p.cw = x, p.fr.w
			x += p.fr.w + p.atom.mar[1]
			out = append(out, placed{fr: p.fr, ib: p.ib, atom: p.atom})
		default:
			p.fr.x = x
			p.cx, p.cw = x, p.fr.w
			if p.fr.kind == aText {
				p.fr.rtl = p.lv&1 == 1
			}
			x += p.fr.w
			out = append(out, placed{fr: p.fr, ib: p.ib})
		}
		p.x1 = x
	}
	// inline box fragments: one per visually contiguous group of members
	var nb []lineBox
	for _, lb := range boxes {
		f, ok := first[lb.box]
		if !ok || lb.contL {
			f = 0
		}
		l, ok := last[lb.box]
		if !ok || lb.contR {
			l = len(pcs) - 1
		}
		if l >= len(pcs) {
			l = len(pcs) - 1
		}
		if f > l {
			nb = append(nb, lb)
			continue
		}
		vp := make([]int, 0, l-f+1)
		for k := f; k <= l; k++ {
			vp = append(vp, vis[k])
		}
		sort.Ints(vp)
		for g := 0; g < len(vp); {
			h := g + 1
			for h < len(vp) && vp[h] == vp[h-1]+1 {
				h++
			}
			lo, hi := pcs[order[vp[g]]], pcs[order[vp[h-1]]]
			nbx := lb
			nbx.x0, nbx.x1 = lo.x0, hi.x1
			if lo.edge == lb.box {
				nbx.x0 += lo.mL
			}
			if hi.edge == lb.box {
				nbx.x1 -= hi.mR
			}
			// a side keeps its border and rounding only where this box's own edge sits
			nbx.contL = !(lo.edge == lb.box)
			nbx.contR = !(hi.edge == lb.box)
			nb = append(nb, nbx)
			g = h
		}
	}
	return out, nb, x
}

func boxEmbeds(bx *lbox) bool {
	m, _ := bidiMode(bx)
	return m != "" && m != "normal"
}
