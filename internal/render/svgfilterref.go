package render

import (
	"container/list"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/net/html"

	"github.com/sweefbrowser/sweef/internal/style"
)

// CSS `filter: url(#id)` on HTML (and SVG) elements. The style engine drops
// url() entries, so the cascade is consulted here: an element whose filter
// list contains url(#id) pointing at an SVG <filter> of the document gets
// FilterFn entries named "svgref:<key>" whose Args carry the element's
// border box (x, y, w, h in the element's own coordinate system, filled in
// once layout is final). The rasterizer resolves the name with
// LookupSVGFilter and runs the filter on the element's layer.

// SVGFilterRef is a resolved url(#id) filter reference.
type SVGFilterRef struct {
	Doc  *SVGDoc
	Node *SVGNode
	ID   string
}

const svgRefPrefix = "svgref:"

var svgRefs struct {
	sync.Mutex
	byKey map[string]*list.Element
	lru   *list.List
	seq   int
}

type svgRefEnt struct {
	key string
	ref *SVGFilterRef
}

func registerSVGFilterRef(ref *SVGFilterRef) string {
	svgRefs.Lock()
	defer svgRefs.Unlock()
	if svgRefs.byKey == nil {
		svgRefs.byKey = map[string]*list.Element{}
		svgRefs.lru = list.New()
	}
	svgRefs.seq++
	key := fmt.Sprintf("%s%d", svgRefPrefix, svgRefs.seq)
	svgRefs.byKey[key] = svgRefs.lru.PushFront(&svgRefEnt{key, ref})
	for svgRefs.lru.Len() > 4096 {
		last := svgRefs.lru.Back()
		delete(svgRefs.byKey, last.Value.(*svgRefEnt).key)
		svgRefs.lru.Remove(last)
	}
	return key
}

// LookupSVGFilter resolves a Filter.Name produced for filter: url(#id).
func LookupSVGFilter(name string) (*SVGFilterRef, bool) {
	if !strings.HasPrefix(name, svgRefPrefix) {
		return nil, false
	}
	svgRefs.Lock()
	defer svgRefs.Unlock()
	if e, ok := svgRefs.byKey[name]; ok {
		return e.Value.(*svgRefEnt).ref, true
	}
	return nil, false
}

// IsSVGFilterRef reports whether a Filter.Name is an SVG filter reference.
func IsSVGFilterRef(name string) bool { return strings.HasPrefix(name, svgRefPrefix) }

// splitFilterTokens splits a filter list into its top-level functions.
func splitFilterTokens(v string) []string {
	var out []string
	depth, start := 0, -1
	for i := 0; i < len(v); i++ {
		ch := v[i]
		switch {
		case ch == '(':
			depth++
		case ch == ')':
			if depth > 0 {
				depth--
			}
			if depth == 0 && start >= 0 {
				out = append(out, strings.TrimSpace(v[start:i+1]))
				start = -1
			}
		case (ch == ' ' || ch == '\t' || ch == '\n') && depth == 0:
		default:
			if start < 0 {
				start = i
			}
		}
	}
	if start >= 0 {
		out = append(out, strings.TrimSpace(v[start:]))
	}
	return out
}

// svgFilterRefs rewrites st when the element's filter list references SVG
// filters.
func (c *ctx) svgFilterRefs(n *html.Node, st *style.Computed) *style.Computed {
	if n.Namespace == "svg" {
		return st
	}
	// cheap pre-check: some stylesheet or the style attribute mentions url(
	if c.urlFilterHint == 0 {
		c.urlFilterHint = 2
		for _, s := range c.opts.Sheets {
			if !s.UserAgent && strings.Contains(s.Text, "url(") && strings.Contains(s.Text, "filter") {
				c.urlFilterHint = 1
				break
			}
		}
	}
	inline := false
	for _, a := range n.Attr {
		if a.Key == "style" && strings.Contains(a.Val, "url(") && strings.Contains(a.Val, "filter") {
			inline = true
		}
	}
	if c.urlFilterHint != 1 && !inline {
		return st
	}
	sc := c.svgCascadeFor()
	var val string
	for _, d := range sc.declsFor(n) {
		if d.Name == "filter" {
			val = d.Val
		}
	}
	if !strings.Contains(val, "url(") {
		return st
	}
	toks := splitFilterTokens(val)
	nonURL := 0
	for _, t := range toks {
		if !strings.HasPrefix(strings.ToLower(t), "url(") {
			nonURL++
		}
	}
	if nonURL != len(st.Filter) {
		return st
	}
	var out []style.FilterFn
	k := 0
	any := false
	for _, t := range toks {
		if !strings.HasPrefix(strings.ToLower(t), "url(") {
			out = append(out, st.Filter[k])
			k++
			continue
		}
		if !strings.HasSuffix(t, ")") {
			continue
		}
		id := strings.TrimSpace(t[4 : len(t)-1])
		id = strings.Trim(id, "'\"")
		if i := strings.IndexByte(id, '#'); i >= 0 {
			id = id[i+1:]
		}
		if id == "" {
			continue
		}
		ref := c.findFilterByID(n, id)
		if ref == nil {
			// a missing reference makes the whole list invalid in browsers
			// for the element; leave the element unfiltered
			continue
		}
		out = append(out, style.FilterFn{Name: registerSVGFilterRef(ref), Args: make([]float64, 4)})
		any = true
	}
	if !any {
		return st
	}
	cp := *st
	cp.Filter = out
	if c.filterEls == nil {
		c.filterEls = map[*html.Node]*style.Computed{}
	}
	c.filterEls[n] = &cp
	return &cp
}

// findFilterByID resolves <filter id> in any inline svg of the document.
func (c *ctx) findFilterByID(from *html.Node, id string) *SVGFilterRef {
	top := from
	for top.Parent != nil {
		top = top.Parent
	}
	var found *html.Node
	var walk func(x *html.Node) bool
	walk = func(x *html.Node) bool {
		if x.Type == html.ElementNode && strings.EqualFold(x.Data, "filter") && attr(x, "id") == id {
			found = x
			return true
		}
		for ch := x.FirstChild; ch != nil; ch = ch.NextSibling {
			if walk(ch) {
				return true
			}
		}
		return false
	}
	walk(top)
	if found == nil {
		return nil
	}
	root := found
	for p := found.Parent; p != nil; p = p.Parent {
		if p.Type == html.ElementNode && strings.EqualFold(p.Data, "svg") {
			root = p
		}
	}
	if !strings.EqualFold(root.Data, "svg") {
		return nil
	}
	doc := c.svgDocFor(root, c.styles[root])
	if doc == nil {
		return nil
	}
	nd := doc.ByID[id]
	if nd == nil || nd.Tag != "filter" {
		return nil
	}
	return &SVGFilterRef{Doc: doc, Node: nd, ID: id}
}

// svgCascadeFor returns the page cascade used for SVG property resolution.
func (c *ctx) svgCascadeFor() *svgCascade {
	svgShared.Lock()
	defer svgShared.Unlock()
	if svgShared.c != c || svgShared.sc == nil {
		scheme := c.opts.ColorScheme
		if scheme == "" {
			scheme = "light"
		}
		env := &style.Env{Width: c.opts.Width, Height: c.opts.Height, DPR: c.opts.DPR, ColorScheme: scheme, Print: c.opts.Print}
		if svgShared.c != c {
			svgShared.imgs, svgShared.sized = nil, nil
		}
		svgShared.c = c
		svgShared.sc = c.newSVGCascade(env)
		svgShared.docs = map[*html.Node]*SVGDoc{}
		svgShared.roots = nil
	}
	return svgShared.sc
}

// finishFilterRefs stores the border box of each element with an SVG filter
// reference in the filter's Args once layout is final.
func (c *ctx) finishFilterRefs() {
	for n, st := range c.filterEls {
		bs := c.byNode[n]
		var b *lbox
		for _, x := range bs {
			if x.kind != kInline && x.pseudo == "" {
				b = x
				break
			}
		}
		if b == nil {
			continue
		}
		for i := range st.Filter {
			if strings.HasPrefix(st.Filter[i].Name, svgRefPrefix) {
				st.Filter[i].Args = []float64{b.ax, b.ay, b.w, b.h}
			}
		}
	}
}
