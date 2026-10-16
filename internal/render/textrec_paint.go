package render

import (
	"math"
	"unicode/utf8"

	"golang.org/x/net/html"

	"github.com/sweefbrowser/sweef/internal/style"
)

// clipNow is the intersection of the clip rectangles in force.
func (p *painter) clipNow() (Rect, bool) {
	if len(p.clips) == 0 {
		return Rect{}, false
	}
	r := p.clips[0].r
	for _, c := range p.clips[1:] {
		x0, y0 := math.Max(r.X, c.r.X), math.Max(r.Y, c.r.Y)
		x1, y1 := math.Min(r.X+r.W, c.r.X+c.r.W), math.Min(r.Y+r.H, c.r.Y+c.r.H)
		if x1 < x0 {
			x1 = x0
		}
		if y1 < y0 {
			y1 = y0
		}
		r = Rect{x0, y0, x1 - x0, y1 - y0}
	}
	return r, true
}

// recordTextFrag notes where the run fr of line ln of the inline formatting context b is drawn: its caret stops
// and the document characters they stand for. e is the end of the part that is drawn (trailing spaces that hang
// off the line are not); x, y is the run origin and baseline, op the index in the display list of the text op.
func (p *painter) recordTextFrag(b *lbox, ln *line, fr *frag, e int, x, y, w float64, op int) {
	it := fr.it
	src := it.src
	if src == nil || src.n == nil || src.n.Type != html.TextNode || src.pseudo != "" || fr.e <= fr.a {
		return
	}
	sh := it.sh
	n := fr.e - fr.a
	xs := make([]float32, n+1)
	nsp := 0
	if fr.justSp != 0 {
		for k := fr.a; k < e; k++ {
			if it.runes[k] == ' ' {
				nsp++
			}
		}
	}
	wtot := sh.x[e] - sh.x[fr.a] + fr.justSp*float64(nsp)
	extra := 0.0
	for i := 0; i <= n; i++ {
		k := fr.a + i
		kk := k
		if kk > e {
			kk = e
		}
		if i > 0 && k-1 < e && k-1 >= fr.a && it.runes[k-1] == ' ' {
			extra += fr.justSp
		}
		d := sh.x[kk] - sh.x[fr.a] + extra
		if fr.rtl {
			xs[i] = float32(wtot - d)
		} else {
			xs[i] = float32(d)
		}
	}
	tb := TextBox{Node: src.n, Rect: Rect{x, b.ay + ln.y, w, ln.h}, Base: y, Xs: xs, Op: op, RTL: fr.rtl,
		NoSelect: it.st.UserSelect == "none" || it.st.UserSelect == "contain", Order: len(p.texts)}
	if tb.Rect.H <= 0 {
		tb.Rect.H = it.fs.asc + it.fs.desc
		tb.Rect.Y = y - it.fs.asc
	}
	if it.smap == nil {
		tb.Start = it.srcAt(fr.a)
	} else {
		tb.Src = make([]int32, n+1)
		for i := 0; i < n; i++ {
			tb.Src[i] = int32(it.srcAt(fr.a + i))
		}
		tb.Src[n] = int32(it.srcAt(fr.e-1) + 1)
		tb.Start = int(tb.Src[0])
	}
	for x := b; x != nil; x = x.parent {
		if x.n != nil && x.n.Type == html.ElementNode {
			tb.Block = x.n
			break
		}
	}
	switch it.st.WhiteSpace {
	case "normal", "nowrap", "":
		tb.CollapseWS = true
	case "pre-line":
		tb.CollapseWS, tb.KeepNL = true, true
	}
	// the last run of its line
	tb.LineEnd = true
	seen := false
	for _, f := range ln.frags {
		if f == fr {
			seen = true
			continue
		}
		if seen && f.kind == aText && f.e > f.a {
			tb.LineEnd = false
			break
		}
	}
	if tb.LineEnd {
		tb.Break = BreakSoft
		if ln.forced {
			tb.Break = BreakForced
		}
	}
	if c, ok := p.clipNow(); ok {
		tb.Clip, tb.HasClip = c, true
	}
	if p.hasM {
		tb.M, tb.HasM = p.m, true
	}
	p.texts = append(p.texts, tb)
}

// ctlScroll is how far the text of a text control is scrolled: horizontally for an input, vertically (and
// horizontally when it does not wrap) for a textarea.
func (p *painter) ctlScroll(b *lbox) (float64, float64) {
	if p.c.scrollEnv == nil || b.n == nil {
		return 0, 0
	}
	x, y := p.c.scrollEnv.ScrollOffset(b.n)
	return math.Max(0, x), math.Max(0, y)
}

// recordCtlLine notes one visual line of the value of text control b: shown are its characters (offsets into the
// value start at start), x the left edge of the text, base its baseline, top and h the line box.
func (p *painter) recordCtlLine(b *lbox, st *style.Computed, fs *fontSpec, shown []rune, start int, x, base, top, h float64, clip Rect, op int) {
	sh := p.c.shape(fs, st, shown)
	xs := make([]float32, len(shown)+1)
	for i := range xs {
		if i < len(sh.x) {
			xs[i] = float32(sh.x[i])
		}
	}
	w := 0.0
	if n := len(sh.x); n > 0 {
		w = sh.x[n-1]
	}
	p.texts = append(p.texts, TextBox{Node: b.n, Ctl: true, Rect: Rect{x, top, w, h}, Base: base, Start: start, Xs: xs, Op: op,
		LineEnd: true, Clip: clip, HasClip: true, Order: len(p.texts)})
	if p.hasM {
		t := &p.texts[len(p.texts)-1]
		t.M, t.HasM = p.m, true
	}
}

// wrapControlText breaks the text of a textarea into visual lines of at most width: [start, end) rune ranges.
// A newline ends a line (and belongs to none); a long line breaks after its last space that fits, or in the
// middle of a word that is too long.
func (c *ctx) wrapControlText(fs *fontSpec, st *style.Computed, rs []rune, width float64, nowrap bool) [][2]int {
	var out [][2]int
	if len(rs) == 0 {
		return [][2]int{{0, 0}}
	}
	var sh *shaped
	if !nowrap {
		sh = c.shape(fs, st, rs)
	}
	start := 0
	for start <= len(rs) {
		end := start
		for end < len(rs) && rs[end] != '\n' {
			end++
		}
		// [start, end) is one paragraph
		if nowrap || end == start {
			out = append(out, [2]int{start, end})
		} else {
			ls := start
			for ls < end {
				brk, last := -1, ls
				i := ls
				for i < end {
					if sh.x[i+1]-sh.x[ls] > width && i > ls {
						break
					}
					if rs[i] == ' ' {
						brk = i + 1
					}
					i++
					last = i
				}
				if i >= end {
					out = append(out, [2]int{ls, end})
					ls = end
					break
				}
				cut := last
				if brk > ls {
					cut = brk
				}
				out = append(out, [2]int{ls, cut})
				ls = cut
			}
		}
		if end >= len(rs) {
			break
		}
		start = end + 1
		if start == len(rs) { // the text ends with a newline: an empty last line
			out = append(out, [2]int{start, start})
			break
		}
	}
	return out
}

var _ = utf8.RuneLen
