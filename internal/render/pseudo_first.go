package render

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"

	"github.com/sweefbrowser/sweef/internal/style"
)

// ---- ::first-letter ----------------------------------------------------------------------

// applyFirstLetter splits the first letter (with surrounding punctuation) of
// block container b off into a ::first-letter box when a rule matches.
func (c *ctx) applyFirstLetter(b *lbox, n *html.Node, st *style.Computed) {
	switch st.Display {
	case "block", "list-item", "inline-block", "flow-root", "table-cell", "table-caption":
	default:
		return
	}
	pst := c.eng.Compute(n, st, "first-letter")
	if pst == nil {
		return
	}
	holder, idx, ok := findFirstLetterText(b, 0)
	if !ok {
		return
	}
	t := holder.kids[idx]
	pre, letter, rest, ok := splitFirstLetter(t.text, collapsibleWS(t.st))
	if !ok {
		return
	}
	kind, inner := classifyDisplay(pst.Display)
	if pst.Float == "none" && pst.Position != "absolute" && pst.Position != "fixed" {
		kind, inner = kInline, iFlow
	}
	fb := newBox(n, pst, kind)
	fb.inner = inner
	fb.pseudo = "first-letter"
	fb.floated = pst.Float != "none"
	fb.oof = pst.Position == "absolute" || pst.Position == "fixed"
	fb.inlineLv = fb.kind == kInline && !fb.floated && !fb.oof
	tb := newBox(t.n, pst, kText)
	tb.text = letter
	tb.srcOff = t.srcOff + utf8.RuneCountInString(pre)
	tb.inlineLv = true
	fb.addKid(tb)
	c.fixupChildren(fb)
	var repl []*lbox
	if pre != "" {
		pb := *t
		pb.text = pre
		repl = append(repl, &pb)
	}
	repl = append(repl, fb)
	if rest != "" {
		rb := *t
		rb.text = rest
		rb.srcOff = t.srcOff + utf8.RuneCountInString(pre) + utf8.RuneCountInString(letter)
		repl = append(repl, &rb)
	}
	nk := make([]*lbox, 0, len(holder.kids)+2)
	nk = append(nk, holder.kids[:idx]...)
	nk = append(nk, repl...)
	nk = append(nk, holder.kids[idx+1:]...)
	holder.kids = nk
	for _, k := range repl {
		k.parent = holder
	}
}

// findFirstLetterText locates the first text box that can supply a first
// letter: text directly or inside inline boxes and first block children.
// stop reports that a non-text first item (image, ...) makes the search end.
func findFirstLetterText(parent *lbox, depth int) (*lbox, int, bool) {
	h, i, ok, _ := firstLetterScan(parent, depth)
	return h, i, ok
}

func firstLetterScan(parent *lbox, depth int) (holder *lbox, idx int, ok, stop bool) {
	if depth > 12 {
		return nil, 0, false, true
	}
	for i, k := range parent.kids {
		switch {
		case k.oof || k.floated:
			continue
		case k.kind == kText:
			if onlyWhitespace(k.text) && collapsibleWS(k.st) {
				continue
			}
			return parent, i, true, false
		case k.kind == kMarker:
			continue
		case k.kind == kInline:
			if h, j, ok, stop := firstLetterScan(k, depth+1); ok || stop {
				return h, j, ok, stop
			}
		case k.kind == kBlock && (k.inner == iFlow) && k.pseudo != "first-letter":
			if k.st.Display == "list-item" || k.st.Display == "flex" || k.st.Display == "grid" {
				return nil, 0, false, true
			}
			h, j, ok, stop := firstLetterScan(k, depth+1)
			if ok || stop {
				return h, j, ok, stop
			}
			if len(k.kids) > 0 {
				return nil, 0, false, true
			}
		default:
			return nil, 0, false, true
		}
	}
	return nil, 0, false, false
}

func isFLPunct(r rune) bool {
	if unicode.IsPunct(r) {
		return true
	}
	return false
}

// splitFirstLetter splits s into leading collapsible whitespace, the first
// letter with adjoining punctuation, and the rest.
func splitFirstLetter(s string, collapse bool) (pre, letter, rest string, ok bool) {
	i := 0
	if collapse {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r' || s[i] == '\f') {
			i++
		}
	}
	start := i
	// leading punctuation
	for i < len(s) {
		r, sz := utf8.DecodeRuneInString(s[i:])
		if !isFLPunct(r) {
			break
		}
		i += sz
	}
	if i >= len(s) {
		return "", "", "", false
	}
	r, sz := utf8.DecodeRuneInString(s[i:])
	if unicode.IsSpace(r) {
		return "", "", "", false
	}
	i += sz
	// combining marks stay with the letter
	for i < len(s) {
		r, sz := utf8.DecodeRuneInString(s[i:])
		if !unicode.Is(unicode.M, r) {
			break
		}
		i += sz
	}
	// trailing punctuation
	for i < len(s) {
		r, sz := utf8.DecodeRuneInString(s[i:])
		if !isFLPunct(r) {
			break
		}
		i += sz
	}
	return s[:start], s[start:i], s[i:], true
}

// ---- ::first-line ------------------------------------------------------------------------

// setFirstLine records the ::first-line style on the inline formatting
// context that holds the first formatted line of b.
func (c *ctx) setFirstLine(b *lbox, fls *style.Computed) {
	if b.ifc {
		b.flSt = fls
		return
	}
	for _, k := range b.kids {
		if k.kind == kBlock && !k.floated && !k.oof {
			if k.inner == iFlow && k.flSt == nil {
				c.setFirstLine(k, fls)
			}
			return
		}
	}
}

// flStyleFor derives the first-line style of inline box k (a descendant of
// the IFC root) from its parent's first-line style.
func (c *ctx) flStyleFor(root, k *lbox, pst *style.Computed) *style.Computed {
	if root.flMap == nil {
		root.flMap = map[*lbox]*style.Computed{}
	}
	if s, ok := root.flMap[k]; ok {
		return s
	}
	var s *style.Computed
	if k.pseudo != "" && k.pseudo != "first-letter" {
		s = c.eng.Compute(k.n, pst, k.pseudo)
	} else if k.pseudo == "" && k.n != nil {
		s = c.eng.Compute(k.n, pst, "")
	}
	if s == nil {
		s = k.st
	}
	root.flMap[k] = s
	return s
}

// buildAtomsFL flattens b's content with the ::first-line style applied.
func (c *ctx) buildAtomsFL(b *lbox) []atom {
	if b.atomsFL != nil {
		return b.atomsFL
	}
	fl := &flatState{c: c, root: b, prevSpace: true, flRoot: b}
	fl.walk(b, b)
	b.atomsFL = fl.atoms
	c.assignBidi(b, b.atomsFL)
	return b.atomsFL
}

func (f *flatState) flStyleOf(owner *lbox) *style.Computed {
	if owner == f.root {
		return f.root.flSt
	}
	var pst *style.Computed
	par := owner.parent
	for par != nil && par != f.root && par.kind != kInline {
		par = par.parent
	}
	if par == nil || par == f.root {
		pst = f.root.flSt
	} else {
		pst = f.flStyleOf(par)
	}
	return f.c.flStyleFor(f.root, owner, pst)
}

// atomKey identifies the source position of an atom.
func atomPos(a atom) (*lbox, int) {
	if a.kind == aText {
		return a.it.src, a.it.off + a.a
	}
	return a.box, 0
}

// remapAtoms finds the position in dst matching src[i], splitting a text
// atom when the position falls inside it.
func remapAtoms(src []atom, i int, dst []atom) ([]atom, int) {
	if i >= len(src) {
		return dst, len(dst)
	}
	key, off := atomPos(src[i])
	kind := src[i].kind
	for j, d := range dst {
		if d.kind == aText && kind == aText {
			lo := off - d.it.off
			if d.it.src == key && d.a <= lo && lo < d.e {
				if d.a == lo {
					return dst, j
				}
				a1, a2 := d, d
				a1.e = lo
				a1.w = d.it.sh.x[lo] - d.it.sh.x[d.a]
				a1.trail = 0
				a1.brk = true
				a2.a = lo
				a2.w = d.it.sh.x[d.e] - d.it.sh.x[lo]
				na := make([]atom, 0, len(dst)+1)
				na = append(na, dst[:j]...)
				na = append(na, a1, a2)
				na = append(na, dst[j+1:]...)
				return na, j + 1
			}
			continue
		}
		if d.kind == kind && d.box == key && kind != aText {
			return dst, j
		}
	}
	return dst, len(dst)
}

var _ = strings.TrimSpace
