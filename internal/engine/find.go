package engine

import (
	"strings"
	"unicode"

	"golang.org/x/net/html"

	"github.com/sweefbrowser/sweef/internal/render"
)

// find.go: find in page. The matches are looked for in the text the page shows (not in hidden elements or
// markup), across the boundaries of inline elements inside a paragraph, and are highlighted by the same pass that
// paints the selection: yellow for all, orange for the active one.

type findMatch struct {
	start, end selPoint // boundary points, UTF-16 units
}

type findState struct {
	query   string
	matches []findMatch
	active  int
}

// maxFindMatches bounds the work on pages with a huge number of hits.
const maxFindMatches = 5000

// FindResult describes the state after a find call.
type FindResult struct {
	Count int
	Index int     // 0-based index of the active match, -1 when none
	Top   float64 // document y (CSS px) of the active match's line, -1 when none
	Left  float64
	H     float64
}

type flatRune struct {
	r   rune
	n   *html.Node
	off int // rune offset in n's data
	sep bool
}

// visibleText flattens the rendered text of the document in tree order into runes that remember where they came
// from. Different blocks are separated by a rune that matches nothing.
func (t *Tab) visibleText(ix *render.TextIndex, root *html.Node) []flatRune {
	var out []flatRune
	var prev *render.TextBox
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			rs := []rune(n.Data)
			for _, tb := range ix.Boxes(n) {
				if tb.NoSelect || tb.Ctl {
					continue
				}
				if prev != nil && prev.Block != tb.Block {
					out = append(out, flatRune{r: '\n', sep: true})
				}
				prev = tb
				for i := 0; i < tb.Stops()-1; i++ {
					o := tb.Off(i)
					if o < 0 || o >= len(rs) {
						continue
					}
					r := rs[o]
					if tb.CollapseWS && (r == '\n' || r == '\t' || r == '\r' || r == '\f') {
						r = ' '
					}
					out = append(out, flatRune{r: r, n: n, off: o})
				}
			}
			return
		}
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "style", "noscript", "template", "head":
				return
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	return out
}

func foldRune(r rune) rune { return unicode.ToLower(r) }

// FindStart looks for query in the page and makes the first match at or after the top of the view active. It
// reports the number of matches. Empty queries clear the highlights.
func (t *Tab) FindStart(query string, fromY float64) FindResult {
	env := t.selEnv()
	if env == nil {
		return FindResult{Index: -1, Top: -1}
	}
	if query == "" {
		t.FindClear()
		return FindResult{Index: -1, Top: -1}
	}
	res, ix, d := t.freshTextIndex()
	if res == nil {
		return FindResult{Index: -1, Top: -1}
	}
	flat := t.visibleText(ix, d.Root)
	q := []rune(query)
	for i := range q {
		q[i] = foldRune(q[i])
	}
	hay := make([]rune, len(flat))
	for i, f := range flat {
		hay[i] = foldRune(f.r)
	}
	var ms []findMatch
	for i := 0; i+len(q) <= len(hay) && len(ms) < maxFindMatches; {
		ok := true
		for k := range q {
			if hay[i+k] != q[k] || flat[i+k].sep {
				ok = false
				break
			}
		}
		if !ok {
			i++
			continue
		}
		first, last := flat[i], flat[i+len(q)-1]
		ms = append(ms, findMatch{
			start: selPoint{first.n, runeToU16(first.n.Data, first.off)},
			end:   selPoint{last.n, runeToU16(last.n.Data, last.off+1)},
		})
		i += len(q)
	}
	st := &findState{query: query, matches: ms, active: -1}
	// the first match at or below the view
	for i, m := range ms {
		if top, _, _, ok := matchRect(ix, m); ok && top >= fromY-1 {
			st.active = i
			break
		}
	}
	if st.active < 0 && len(ms) > 0 {
		st.active = 0
	}
	s := &env.sel
	s.mu.Lock()
	s.find = st
	s.mu.Unlock()
	t.selChanged()
	return t.findResult(st, ix)
}

func matchRect(ix *render.TextIndex, m findMatch) (top, left, h float64, ok bool) {
	textsInRange(m.start, m.end, func(n *html.Node, from, to int) {
		for _, r := range ix.RectsFor(n, from, to) {
			if !ok {
				top, left, h, ok = r.Y, r.X, r.H, true
			}
		}
	})
	return
}

func (t *Tab) findResult(st *findState, ix *render.TextIndex) FindResult {
	fr := FindResult{Count: len(st.matches), Index: st.active, Top: -1, Left: -1}
	if st.active >= 0 && st.active < len(st.matches) {
		if top, left, h, ok := matchRect(ix, st.matches[st.active]); ok {
			fr.Top, fr.Left, fr.H = top, left, h
		}
	}
	return fr
}

// FindStep makes match idx (wrapping around) the active one.
func (t *Tab) FindStep(idx int) FindResult {
	env := t.selEnv()
	if env == nil {
		return FindResult{Index: -1, Top: -1}
	}
	_, ix, _ := t.freshTextIndex()
	s := &env.sel
	s.mu.Lock()
	st := s.find
	if st == nil || len(st.matches) == 0 {
		s.mu.Unlock()
		return FindResult{Index: -1, Top: -1}
	}
	n := len(st.matches)
	st.active = ((idx % n) + n) % n
	s.mu.Unlock()
	t.selChanged()
	return t.findResult(st, ix)
}

// FindClear removes the find highlights.
func (t *Tab) FindClear() {
	env := t.selEnv()
	if env == nil {
		return
	}
	s := &env.sel
	s.mu.Lock()
	had := s.find != nil
	s.find = nil
	s.mu.Unlock()
	if had {
		t.selChanged()
	}
}

var _ = strings.ToLower
