package render

import (
	"encoding/base64"
	"image"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/net/html"

	"github.com/sweefbrowser/sweef/internal/style"
)

// SVGNode is one element of a resolved SVG scene. A holds the element's
// attributes (lower-cased keys, xlink:href normalised to "href") overlaid by
// the declarations of the page cascade (author rules, then the style
// attribute) for every SVG property, so the rasterizer sees one flat map.
// Text content appears as children with Tag "#text".
type SVGNode struct {
	Tag  string
	A    map[string]string
	Kids []*SVGNode
	Text string
	dom  *html.Node
	// External resolution done at layout time through the Env: Img is the
	// decoded raster of an <image>/<feImage> href, Sub the parsed SVG of an
	// external .svg (SubID is the fragment of <use href="other.svg#id">).
	Img   image.Image
	Sub   *SVGDoc
	SubID string
	// Ops is the display list of a <foreignObject>'s HTML content in the
	// object's user space; the rasterizer plays it where the element sits in
	// the SVG paint order, under the ancestors' transforms, clips and effects.
	Ops []Op

	sigV     uint64 // content signature, see opsig.go
	sigState uint32 // 0 unknown, 1 sigV valid, 2 not signable
}

// SVGDoc is a resolved SVG scene.
type SVGDoc struct {
	Root *SVGNode
	ByID map[string]*SVGNode
	// Inherit carries inherited SVG properties declared on HTML ancestors of
	// an inline <svg> (fill, stroke, ...).
	Inherit map[string]string
	// IntrW/IntrH are the intrinsic size from width/height (0 = none) and
	// Ratio the viewBox aspect ratio (0 = none).
	IntrW, IntrH, Ratio float64
	dom                 map[*html.Node]*SVGNode
	base                *style.Computed
	// Base is the URL relative references of this scene resolve against; Src
	// the markup of a standalone image (kept to re-evaluate @media for the
	// size it is painted at); depth the nesting level of external documents.
	Base     string
	Src      string
	HasMedia bool
	depth    int
	extDone  bool
	merged   bool
	sigV     uint64
	sigState uint32
}

// SVGDraw paints an SVG scene. The root's viewBox / preserveAspectRatio map
// onto Dst (CSS px). This op was added for SVG painting; it is additive.
type SVGDraw struct {
	Dst      Rect
	Doc      *SVGDoc
	Color    Color // computed `color` of the embedding element (currentColor)
	FontSize float64
	Family   string
	Weight   int
	Italic   bool
}

func (SVGDraw) isOp() {}

// BackdropFilter blurs/filters what is already painted behind Rect (clipped
// to the rounded rect) before the element's own content is drawn.
// Additive op for backdrop-filter.
type BackdropFilter struct {
	Rect   Rect
	Radii  Radii
	Filter []Filter
}

func (BackdropFilter) isOp() {}

// SVGSourcer is an optional Env extension: it returns the markup of an SVG
// resource (for <img src=*.svg> and CSS backgrounds) when the host has
// fetched one.
type SVGSourcer interface {
	SVGSource(url string) (string, bool)
}

// svgPropNames are the declarations copied from the cascade into SVGNode.A.
var svgPropNames = map[string]bool{
	"fill": true, "fill-opacity": true, "fill-rule": true, "stroke": true, "stroke-width": true, "stroke-opacity": true,
	"stroke-linecap": true, "stroke-linejoin": true, "stroke-miterlimit": true, "stroke-dasharray": true, "stroke-dashoffset": true,
	"opacity": true, "color": true, "display": true, "visibility": true, "transform": true, "transform-origin": true, "transform-box": true,
	"clip-path": true, "clip-rule": true, "mask": true, "filter": true, "mix-blend-mode": true, "isolation": true,
	"font-size": true, "font-family": true, "font-weight": true, "font-style": true, "text-anchor": true, "letter-spacing": true,
	"word-spacing": true, "dominant-baseline": true, "alignment-baseline": true, "baseline-shift": true, "text-decoration": true,
	"stop-color": true, "stop-opacity": true, "flood-color": true, "flood-opacity": true, "lighting-color": true,
	"marker": true, "marker-start": true, "marker-mid": true, "marker-end": true, "paint-order": true, "vector-effect": true,
	"shape-rendering": true, "color-interpolation-filters": true, "color-interpolation": true, "overflow": true,
	"cx": true, "cy": true, "r": true, "rx": true, "ry": true, "x": true, "y": true, "width": true, "height": true, "d": true,
	"mask-type": true, "image-rendering": true, "font": true, "writing-mode": true, "text-orientation": true, "text-rendering": true,
}

var svgInheritedProps = map[string]bool{
	"fill": true, "fill-opacity": true, "fill-rule": true, "stroke": true, "stroke-width": true, "stroke-opacity": true,
	"stroke-linecap": true, "stroke-linejoin": true, "stroke-miterlimit": true, "stroke-dasharray": true, "stroke-dashoffset": true,
	"visibility": true, "clip-rule": true, "text-anchor": true, "letter-spacing": true, "word-spacing": true,
	"marker-start": true, "marker-mid": true, "marker-end": true, "paint-order": true, "shape-rendering": true,
	"color-interpolation-filters": true, "image-rendering": true, "writing-mode": true, "text-orientation": true, "text-rendering": true,
}

// svgCascade matches author rules against SVG elements.
type svgCascade struct {
	rules  []*style.Rule
	custom map[string]string
	idx    *style.EntryIndex
	rank   map[*style.Rule]int // position of each rule in rules
	memo   map[*html.Node][]style.Decl
	// keep carries declsFor answers across passes of a session for elements
	// whose cascade inputs are known (keyFn, from the style cache)
	keep  map[*html.Node]*declKept
	keyFn func(n *html.Node) (anc, own uint64, ok bool)
}

type declKept struct {
	d        []style.Decl
	anc, own uint64
}

func newSVGCascade(sheets []Sheet, env *style.Env, extra []string) *svgCascade {
	sc := &svgCascade{}
	order := 0
	layers := map[string]int{}
	for _, s := range sheets {
		if s.UserAgent {
			continue
		}
		if s.Media != "" && !style.MediaMatches(s.Media, env) {
			continue
		}
		sc.rules = append(sc.rules, style.ParseSheet(s.Text, env, style.OriginAuthor, &order, layers).Rules...)
	}
	for _, t := range extra {
		sc.rules = append(sc.rules, style.ParseSheet(t, env, style.OriginAuthor, &order, layers).Rules...)
	}
	sc.idx = style.NewEntryIndex(sc.rules)
	sc.rank = make(map[*style.Rule]int, len(sc.rules))
	for i, r := range sc.rules {
		sc.rank[r] = i
	}
	return sc
}

type svgDecl struct {
	d    style.Decl
	imp  bool
	lay  int
	spec uint32
	ord  int
}

// declsFor returns the cascaded declarations for n, lowest priority first.
func (sc *svgCascade) declsFor(n *html.Node) []style.Decl {
	// a scene build asks for the same ancestors again and again (the inherited
	// properties of every svg element walk up the whole tree); the DOM does not
	// change during one layout, so the answer is kept for this cascade's life.
	// Callers only read the result.
	if d, ok := sc.memo[n]; ok {
		return d
	}
	if sc.memo == nil {
		sc.memo = map[*html.Node][]style.Decl{}
	}
	if sc.keyFn != nil {
		if anc, own, ok := sc.keyFn(n); ok {
			if k := sc.keep[n]; k != nil && k.anc == anc && k.own == own {
				sc.memo[n] = k.d
				return k.d
			}
			d := sc.declsForUncached(n)
			if sc.keep == nil || len(sc.keep) > 8192 {
				sc.keep = map[*html.Node]*declKept{}
			}
			sc.keep[n] = &declKept{d: d, anc: anc, own: own}
			sc.memo[n] = d
			return d
		}
	}
	d := sc.declsForUncached(n)
	sc.memo[n] = d
	return d
}

func (sc *svgCascade) declsForUncached(n *html.Node) []style.Decl {
	var ds []svgDecl
	// the rules whose selectors can match n, each with its best matching
	// specificity, then walked in sheet order (the order the full scan used)
	type hit struct {
		rank int
		r    *style.Rule
		best uint32
	}
	var hits []hit
	var byRule map[*style.Rule]int
	for _, en := range sc.idx.Candidates(n, nil) {
		if en.Pseudo != "" || !en.Sel.Match(n, nil) {
			continue
		}
		if byRule == nil {
			byRule = map[*style.Rule]int{}
		}
		if i, ok := byRule[en.Rule]; ok {
			if en.Spec > hits[i].best {
				hits[i].best = en.Spec
			}
			continue
		}
		byRule[en.Rule] = len(hits)
		hits = append(hits, hit{rank: sc.rank[en.Rule], r: en.Rule, best: en.Spec})
	}
	if len(hits) == 0 && !hasAttr(n, "style") {
		return nil // no rule and no inline style: callers only read the result
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].rank < hits[j].rank })
	for _, h := range hits {
		r, best := h.r, h.best
		lay := r.Layer
		if lay == 0 {
			lay = 1 << 30
		}
		for _, d := range r.Decls {
			ds = append(ds, svgDecl{d: d, imp: d.Imp, lay: lay, spec: best, ord: r.Order})
		}
	}
	sort.SliceStable(ds, func(i, j int) bool {
		a, b := ds[i], ds[j]
		if a.imp != b.imp {
			return !a.imp
		}
		if a.lay != b.lay {
			if a.imp {
				return a.lay > b.lay
			}
			return a.lay < b.lay
		}
		if a.spec != b.spec {
			return a.spec < b.spec
		}
		return a.ord < b.ord
	})
	out := make([]style.Decl, 0, len(ds)+4)
	for _, d := range ds {
		out = append(out, d.d)
	}
	for _, a := range n.Attr {
		if a.Key == "style" {
			var norm, imp []style.Decl
			for _, d := range style.ParseInline(a.Val) {
				if d.Imp {
					imp = append(imp, d)
				} else {
					norm = append(norm, d)
				}
			}
			// inline style beats author rules except !important rules
			var impRules, rest []style.Decl
			for i, d := range out {
				if ds[i].imp {
					impRules = append(impRules, d)
				} else {
					rest = append(rest, d)
				}
			}
			out = append(append(append(rest, norm...), impRules...), imp...)
		}
	}
	return out
}

func substVars(v string, custom map[string]string, depth int) string {
	for depth < 8 {
		i := strings.Index(v, "var(")
		if i < 0 {
			return v
		}
		d, j := 1, i+4
		for j < len(v) && d > 0 {
			switch v[j] {
			case '(':
				d++
			case ')':
				d--
			}
			j++
		}
		body := v[i+4 : j-1]
		name, fb := body, ""
		if k := strings.IndexByte(body, ','); k >= 0 {
			name, fb = body[:k], strings.TrimSpace(body[k+1:])
		}
		name = strings.TrimSpace(name)
		val, ok := custom[name]
		if !ok {
			val = fb
		}
		v = v[:i] + val + v[j:]
		depth++
	}
	return v
}

// buildSVGScene converts the DOM subtree rooted at root into a scene.
func buildSVGScene(root *html.Node, sc *svgCascade) *SVGDoc {
	doc := &SVGDoc{ByID: map[string]*SVGNode{}, dom: map[*html.Node]*SVGNode{}}
	doc.Root = sc.node(root, doc)
	if doc.Root == nil {
		return nil
	}
	w, okw := svgAbsLen(doc.Root.A["width"])
	h, okh := svgAbsLen(doc.Root.A["height"])
	if okw {
		doc.IntrW = w
	}
	if okh {
		doc.IntrH = h
	}
	if vb := strings.Fields(strings.ReplaceAll(doc.Root.A["viewbox"], ",", " ")); len(vb) == 4 {
		vw, vh := svgNum(vb[2], 0), svgNum(vb[3], 0)
		if vw > 0 && vh > 0 {
			doc.Ratio = vw / vh
		}
	}
	return doc
}

func svgAbsLen(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasSuffix(s, "%") {
		return 0, false
	}
	v := svgLen(s, 0, -1)
	if v < 0 {
		return 0, false
	}
	return v, true
}

func (sc *svgCascade) node(n *html.Node, doc *SVGDoc) *SVGNode {
	if n.Type == html.TextNode {
		return &SVGNode{Tag: "#text", Text: n.Data}
	}
	if n.Type != html.ElementNode {
		return nil
	}
	tag := strings.ToLower(n.Data)
	switch tag {
	case "style", "script", "title", "desc", "metadata":
		return nil
	}
	sn := &SVGNode{Tag: tag, A: map[string]string{}, dom: n}
	doc.dom[n] = sn
	var cased map[string]bool // keys that came from a mixed-case attribute (viewBox, preserveAspectRatio, ...)
	for _, a := range n.Attr {
		k := strings.ToLower(a.Key)
		if a.Namespace == "xlink" || k == "xlink:href" {
			k = "href"
		}
		if k == "href" && sn.A["href"] != "" && a.Namespace == "xlink" {
			continue
		}
		// SVG attribute names are case-sensitive: a script that also calls setAttribute("viewbox", ...) leaves a second,
		// meaningless attribute next to the real viewBox, and the lowercase one must not override it.
		if a.Key != k {
			if cased == nil {
				cased = map[string]bool{}
			}
			cased[k] = true
		} else if cased[k] {
			continue
		}
		sn.A[k] = a.Val
	}
	// "font" shorthand is reduced to size/family below
	for _, d := range sc.declsFor(n) {
		name := d.Name
		if !svgPropNames[name] {
			continue
		}
		v := d.Val
		if strings.Contains(v, "var(") {
			v = substVars(v, sc.custom, 0)
		}
		v = strings.TrimSpace(v)
		if name == "font" {
			parseFontShorthand(v, sn.A)
			continue
		}
		if name == "marker" {
			sn.A["marker-start"], sn.A["marker-mid"], sn.A["marker-end"] = v, v, v
			continue
		}
		sn.A[name] = v
	}
	if id := sn.A["id"]; id != "" {
		if _, dup := doc.ByID[id]; !dup {
			doc.ByID[id] = sn
		}
	}
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		if k := sc.node(ch, doc); k != nil {
			sn.Kids = append(sn.Kids, k)
		}
	}
	return sn
}

func parseFontShorthand(v string, a map[string]string) {
	toks := strings.Fields(v)
	for i, t := range toks {
		sz := t
		if k := strings.IndexByte(t, '/'); k > 0 {
			sz = t[:k]
		}
		if len(sz) > 0 && (sz[0] >= '0' && sz[0] <= '9' || sz[0] == '.') && (strings.HasSuffix(sz, "px") || strings.HasSuffix(sz, "pt") || strings.HasSuffix(sz, "em") || strings.HasSuffix(sz, "rem") || strings.HasSuffix(sz, "%")) {
			a["font-size"] = sz
			a["font-family"] = strings.Join(toks[i+1:], " ")
			for _, p := range toks[:i] {
				switch {
				case p == "italic" || p == "oblique":
					a["font-style"] = p
				case p == "bold" || p == "bolder" || p == "lighter" || (len(p) == 3 && p[0] >= '1' && p[0] <= '9' && p[1:] == "00"):
					a["font-weight"] = p
				}
			}
			return
		}
	}
}

// ---- entry points used by the painter ------------------------------------------------

type svgSharedT struct {
	sync.Mutex
	c        *ctx
	sc       *svgCascade
	docs     map[*html.Node]*SVGDoc
	imgs     map[string]*SVGDoc
	sized    map[string]*SVGDoc
	roots    []*html.Node
	extDepth int
}

var svgShared svgSharedT

// svgDocFor returns the (cached) resolved scene of an inline <svg> element.
// Ids defined in the page's other inline <svg> elements (icon sprites) resolve
// too, as they do in a browser.
func (c *ctx) svgDocFor(n *html.Node, st *style.Computed) *SVGDoc {
	svgShared.Lock()
	c.svgInitLocked()
	doc := c.svgBuildLocked(n, st)
	if c.sess != nil && doc != nil {
		// session scenes are never modified: the ids of the page's other svg
		// elements are merged into a copy that is reused while they stay the same
		if u, ok := c.svgUnionLocked(n); ok {
			doc = c.svgMergeSessLocked(n, doc, u)
			svgShared.Unlock()
			c.svgEnsureExt(doc)
			return doc
		}
	}
	if doc != nil && !doc.merged {
		doc.merged = true
		svgShared.mergeRootsLocked(n)
		if len(svgShared.roots) > 1 && len(svgShared.roots) <= 400 {
			for _, o := range svgShared.roots {
				if o == n {
					continue
				}
				d2 := c.svgBuildLocked(o, c.styles[o])
				if d2 == nil {
					continue
				}
				for id, nd := range d2.ByID {
					if _, ok := doc.ByID[id]; !ok {
						doc.ByID[id] = nd
					}
				}
			}
		}
	}
	svgShared.Unlock()
	c.svgEnsureExt(doc)
	return doc
}

// svgInitLocked starts the per-pass shared state (cascade, scene table) the
// first time a pass asks for a scene.
func (c *ctx) svgInitLocked() {
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
}

// mergeRootsLocked lists the page's inline svg elements (outermost ones, in
// document order) the first time it is needed in a pass.
func (s *svgSharedT) mergeRootsLocked(n *html.Node) {
	if s.roots != nil {
		return
	}
	top := n
	for top.Parent != nil {
		top = top.Parent
	}
	s.roots = []*html.Node{}
	var walk func(x *html.Node)
	walk = func(x *html.Node) {
		if x.Type == html.ElementNode && strings.EqualFold(x.Data, "svg") {
			s.roots = append(s.roots, x)
			return
		}
		for ch := x.FirstChild; ch != nil; ch = ch.NextSibling {
			walk(ch)
		}
	}
	walk(top)
}

// svgBuildLocked builds and caches the scene of n (the caller holds
// svgShared).
func (c *ctx) svgBuildLocked(n *html.Node, st *style.Computed) *SVGDoc {
	if d, ok := svgShared.docs[n]; ok {
		return d
	}
	key, keyed := uint64(0), false
	if !c.svgNoSess {
		key, keyed = c.svgKey(n, st)
	}
	if keyed {
		if e := c.sess.svg.docs[n]; e != nil && e.sig == key {
			e.used = true
			svgShared.docs[n] = e.doc
			c.sess.svg.Hits++
			return e.doc
		}
	}
	sc := svgShared.sc
	if st != nil {
		sc.custom = st.Custom
	} else {
		sc.custom = nil
	}
	doc := buildSVGScene(n, sc)
	if doc != nil {
		var chain []*html.Node
		for a := n.Parent; a != nil && a.Type == html.ElementNode; a = a.Parent {
			chain = append([]*html.Node{a}, chain...)
		}
		inh := map[string]string{}
		for _, a := range chain {
			for _, d := range sc.declsFor(a) {
				if svgInheritedProps[d.Name] {
					v := d.Val
					if strings.Contains(v, "var(") {
						v = substVars(v, sc.custom, 0)
					}
					inh[d.Name] = strings.TrimSpace(v)
				}
			}
		}
		doc.Inherit = inh
		doc.base = st
		doc.Base = c.opts.BaseURL
	}
	svgShared.docs[n] = doc
	if keyed && doc != nil {
		if c.sess.svg.docs == nil {
			c.sess.svg.docs = map[*html.Node]*svgEnt{}
		}
		doc.extDone = true // cacheable scenes have no external references to resolve
		c.sess.svg.docs[n] = &svgEnt{doc: doc, sig: key, used: true}
		c.sess.svg.Builds++
	}
	return doc
}

// newSVGCascade returns the page cascade for SVG property resolution: the
// session's, when the sheets are unchanged since the previous pass.
func (c *ctx) newSVGCascade(env *style.Env) *svgCascade {
	if c.sess != nil {
		return c.sess.svg.cascade(c.opts.Sheets, env, c.sess.Style.NodeKey)
	}
	return newSVGCascade(c.opts.Sheets, env, nil)
}

// inlineSVGDoc builds (and caches) the scene of an inline <svg> element.
func (p *painter) inlineSVGDoc(n *html.Node, st *style.Computed) *SVGDoc {
	return p.c.svgDocFor(n, st)
}

// svgResolveURL resolves href against base ("" when it cannot be resolved).
func svgResolveURL(base, href string) string {
	href = strings.TrimSpace(href)
	if href == "" {
		return ""
	}
	r, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if r.IsAbs() {
		return r.String()
	}
	if base == "" {
		return ""
	}
	b, err := url.Parse(base)
	if err != nil {
		return ""
	}
	return b.ResolveReference(r).String()
}

// svgEnsureExt resolves external references of a scene through the Env: the
// decoded bitmap of <image>/<feImage> (Env.Image) and external SVG documents
// of <image>, <feImage> and <use href="other.svg#id"> (SVGSourcer).
func (c *ctx) svgEnsureExt(doc *SVGDoc) {
	if doc == nil || doc.extDone || c.env == nil {
		return
	}
	doc.extDone = true
	svgShared.Lock()
	svgShared.extDepth++
	d := svgShared.extDepth
	svgShared.Unlock()
	defer func() {
		svgShared.Lock()
		svgShared.extDepth--
		svgShared.Unlock()
	}()
	if d > 4 {
		return
	}
	var walk func(n *SVGNode)
	walk = func(n *SVGNode) {
		switch n.Tag {
		case "image", "use", "feimage":
			href := strings.TrimSpace(n.A["href"])
			if href == "" || href[0] == '#' || strings.HasPrefix(href, "data:") {
				break
			}
			abs := svgResolveURL(doc.Base, href)
			if abs == "" {
				break
			}
			frag := ""
			if i := strings.IndexByte(abs, '#'); i >= 0 {
				abs, frag = abs[:i], abs[i+1:]
			}
			isSVG := strings.HasSuffix(strings.ToLower(strings.SplitN(strings.SplitN(abs, "?", 2)[0], "#", 2)[0]), ".svg")
			if n.Tag != "use" && !isSVG {
				if im := c.env.Image(abs); im != nil {
					n.Img = im
					break
				}
			}
			if sub := c.svgImageDocFor(abs); sub != nil {
				n.Sub, n.SubID = sub, frag
			}
		}
		for _, k := range n.Kids {
			walk(k)
		}
	}
	if doc.Root != nil {
		walk(doc.Root)
	}
}

// svgImageDocFor resolves a url to an SVG scene (data: URIs, or the Env's
// SVGSourcer), cached per layout pass. nil when the url is not an SVG
// resource.
func (c *ctx) svgImageDocFor(u string) *SVGDoc {
	if !strings.HasPrefix(u, "data:") {
		if ru := svgResolveURL(c.opts.BaseURL, u); ru != "" {
			u = ru
		}
	}
	svgShared.Lock()
	if svgShared.c != c {
		svgShared.c, svgShared.sc, svgShared.docs = c, nil, map[*html.Node]*SVGDoc{}
		svgShared.imgs, svgShared.sized = nil, nil
	}
	if d, ok := svgShared.imgs[u]; ok {
		svgShared.Unlock()
		return d
	}
	if svgShared.imgs == nil {
		svgShared.imgs = map[string]*SVGDoc{}
	}
	// reserve the slot so reference cycles between documents terminate
	svgShared.imgs[u] = nil
	svgShared.Unlock()
	var markup string
	found := false
	if strings.HasPrefix(u, "data:") {
		if i := strings.IndexByte(u, ','); i > 0 {
			meta, body := u[5:i], u[i+1:]
			if strings.Contains(meta, "image/svg+xml") {
				if strings.Contains(meta, ";base64") {
					if b, err := base64.StdEncoding.DecodeString(body); err == nil {
						markup, found = string(b), true
					}
				} else if s, err := url.PathUnescape(body); err == nil {
					markup, found = s, true
				}
			}
		}
	} else if ss, ok := c.env.(SVGSourcer); ok {
		markup, found = ss.SVGSource(u)
	}
	var doc *SVGDoc
	if found {
		doc = parseStandaloneSVGEnv(markup, c, 0, 0, u)
	}
	svgShared.Lock()
	if svgShared.c == c {
		svgShared.imgs[u] = doc
	}
	svgShared.Unlock()
	// SVG used as an image cannot load external resources (browser rule), so
	// its references to other files stay unresolved
	if doc != nil {
		doc.extDone = true
	}
	return doc
}

// svgImageDocSized is svgImageDocFor with the scene's media queries (width,
// height, prefers-color-scheme) evaluated for the size it is painted at, the
// way a browser sizes the viewport of an SVG image.
func (c *ctx) svgImageDocSized(u string, w, h float64) *SVGDoc {
	d := c.svgImageDocFor(u)
	if d == nil || !d.HasMedia || w <= 0 || h <= 0 {
		return d
	}
	key := u + "|" + strconv.FormatFloat(math.Round(w), 'f', 0, 64) + "x" + strconv.FormatFloat(math.Round(h), 'f', 0, 64)
	svgShared.Lock()
	if sd, ok := svgShared.sized[key]; ok {
		svgShared.Unlock()
		return sd
	}
	if svgShared.sized == nil {
		svgShared.sized = map[string]*SVGDoc{}
	}
	svgShared.Unlock()
	sd := parseStandaloneSVGEnv(d.Src, c, math.Round(w), math.Round(h), u)
	if sd != nil {
		sd.extDone = true
	}
	svgShared.Lock()
	svgShared.sized[key] = sd
	svgShared.Unlock()
	return sd
}

// svgImageIntrinsic reports the intrinsic dimensions of an SVG image for
// replaced-element sizing (width/height attributes, else the viewBox ratio).
// Layout can call this from loadImage when Env.Image returned nil:
//
//	if w, h, hasW, hasH, ratio, ok := c.svgImageIntrinsic(r.url); ok { ... }
func (c *ctx) svgImageIntrinsic(u string) (w, h float64, hasW, hasH bool, ratio float64, ok bool) {
	d := c.svgImageDocFor(u)
	if d == nil {
		return
	}
	w, h, hasW, hasH, ratio = svgIntrinsic(d)
	if !hasW && !hasH {
		w, h = 300, 150
		hasW, hasH = true, true
		if ratio > 0 {
			h = 300 / ratio
		}
	}
	return w, h, hasW, hasH, ratio, true
}

// ParseSVG parses standalone SVG markup (an .svg file, a data: URI payload or
// an <img>/canvas source) into a scene. Returns nil when no <svg> root is
// found. The scene is styled only by its own <style> elements and attributes.
func ParseSVG(markup string) *SVGDoc {
	return parseStandaloneSVG(markup, nil, nil)
}

// IntrinsicSize reports the image's natural size the way <img>/drawImage see
// it (width/height, else viewBox ratio against 300x150).
func (d *SVGDoc) IntrinsicSize() (w, h float64) { return svgImageSize(d) }

func parseStandaloneSVG(markup string, outer *svgCascade, c *ctx) *SVGDoc {
	return parseStandaloneSVGEnv(markup, c, 0, 0, "")
}

// parseStandaloneSVGEnv parses an SVG image. Its <style> media queries see a
// viewport of w x h (the page viewport when zero) and the page's color
// scheme; base resolves its relative references.
func parseStandaloneSVGEnv(markup string, c *ctx, w, h float64, base string) *SVGDoc {
	dn, err := html.Parse(strings.NewReader(markup))
	if err != nil {
		return nil
	}
	var root *html.Node
	var styles []string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if n.Data == "svg" && root == nil {
				root = n
			}
			if n.Data == "style" {
				var sb strings.Builder
				for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
					sb.WriteString(ch.Data)
				}
				styles = append(styles, sb.String())
			}
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			walk(ch)
		}
	}
	walk(dn)
	if root == nil {
		return nil
	}
	env := &style.Env{Width: 1280, Height: 800, DPR: 1, ColorScheme: "light"}
	if c != nil {
		scheme := c.opts.ColorScheme
		if scheme == "" {
			scheme = "light"
		}
		env = &style.Env{Width: c.opts.Width, Height: c.opts.Height, DPR: c.opts.DPR, ColorScheme: scheme}
		if env.DPR <= 0 {
			env.DPR = 1
		}
	}
	if w > 0 && h > 0 {
		env.Width, env.Height = w, h
	}
	hasMedia := false
	for _, s := range styles {
		if strings.Contains(s, "@media") || strings.Contains(s, "prefers-") {
			hasMedia = true
		}
	}
	// standalone SVG images do not see the embedding page's rules
	sc := newSVGCascade(nil, env, styles)
	sc.custom = map[string]string{}
	doc := buildSVGScene(root, sc)
	if doc != nil {
		doc.Base = base
		doc.Src = markup
		doc.HasMedia = hasMedia
	}
	return doc
}

// svgIntrinsic returns the intrinsic size of an SVG image the way <img>
// sizing sees it: width/height when absolute, otherwise derived from the
// viewBox ratio, falling back to 300x150.
func svgIntrinsic(d *SVGDoc) (w, h float64, hasW, hasH bool, ratio float64) {
	w, h = d.IntrW, d.IntrH
	hasW, hasH = w > 0, h > 0
	ratio = d.Ratio
	if ratio == 0 && hasW && hasH {
		ratio = w / h
	}
	switch {
	case hasW && !hasH && ratio > 0:
		h, hasH = w/ratio, true
	case hasH && !hasW && ratio > 0:
		w, hasW = h*ratio, true
	}
	return
}

// svgImageSize is the concrete default object size (CSS 2.1 rules).
func svgImageSize(d *SVGDoc) (float64, float64) {
	w, h, hw, hh, ratio := svgIntrinsic(d)
	if hw && hh {
		return w, h
	}
	if ratio > 0 {
		return math.Round(300), math.Round(300 / ratio)
	}
	return 300, 150
}
