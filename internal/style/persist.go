package style

import (
	"unsafe"

	"golang.org/x/net/html"
)

// persist.go: computed styles that survive from one layout pass to the next.
//
// A layout pass used to build a style engine and compute the style of every
// element from scratch. For a page that changes a little at a time (typing in
// the browser's own chrome page) nearly all of that work repeats an identical
// answer. Persist keeps the engine (parsed sheets, rule index, sharing table)
// and the computed style of each element, and reuses an element's style when
// nothing it can depend on has changed:
//
//   - the element's own tag and the attributes some selector or presentational
//     hint can read (the "relevant" attributes, derived from the rules);
//   - the same attributes of every ancestor (selectors with descendant and
//     child combinators, table hints), folded into a chain value A;
//   - the parent's computed style, by identity (inheritance, em/rem);
//   - when some selector uses sibling combinators or structural pseudo-classes
//     (:nth-child, :first-child, + ~ ...), the identity and relevant attributes
//     of all the element children of its parent; when a selector uses them on
//     an ancestor-side compound, those of every ancestor's siblings too;
//   - whether it has children, when :empty is used.
//
// Nothing here observes mutations: a pass recomputes these values from the
// tree it is given, so a change that bypasses any DOM notification is still
// seen. Documents whose rules use :has(), :scope, @scope or @container are not
// cached (the engine is rebuilt every pass, as before), and so are documents
// that declare container-type. SWEEF_LAYOUT_CHECK=1 compares every pass with a
// from-scratch one.
type Persist struct {
	eng    *Engine
	env    Env
	quirks bool
	sheets []SheetIn

	usable bool // false: the engine may be reused but styles are not cached
	bad    bool // a container-type was seen: stop caching with this engine
	rel    map[string]struct{}
	sib    bool // some selector's subject depends on siblings
	ancSib bool // an ancestor-side compound does
	empty  bool // :empty is used

	epoch   uint32
	ents    map[*html.Node]*pent
	pents   map[pkey]*pent
	sibMemo map[*html.Node]uint64
	visited int

	// Stats (tests, SWEEF_STAGES): elements answered from the cache / computed.
	Hits, Misses int
}

type pkey struct {
	n      *html.Node
	pseudo string
}

type pent struct {
	c      *Computed
	parent *Computed
	own    uint64
	anc    uint64
	childA uint64
	epoch  uint32
}

// NewPersist returns an empty cache. It is used by one document's layout
// provider, which runs one pass at a time.
func NewPersist() *Persist { return &Persist{} }

// presentational attributes read by the hint code (ua.go) and needsHints.
var hintAttrs = []string{"width", "height", "bgcolor", "align", "valign", "border", "cellpadding", "cellspacing", "nowrap", "color",
	"face", "size", "text", "link", "background", "hspace", "vspace", "type", "dir", "frameborder", "marginwidth", "marginheight",
	"rows", "cols", "span", "noshade", "start", "value", "bordercolor", "leftmargin", "topmargin", "style", "class", "id", "lang"}

func sheetsEqual(a, b []SheetIn) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Media != b[i].Media || a[i].Text != b[i].Text {
			return false
		}
	}
	return true
}

// Acquire returns the engine for a pass over a document with these inputs and
// starts the pass. The engine is the previous one when the inputs are the
// same. Its Compute answers from the cache when it can.
func (p *Persist) Acquire(env *Env, sheets []SheetIn, quirks bool) *Engine {
	if p.eng != nil && !p.bad && !p.eng.hasCQ && p.quirks == quirks && p.env == *env && sheetsEqual(p.sheets, sheets) && len(p.eng.share) < 60000 {
		p.beginPass()
		return p.eng
	}
	p.env = *env
	p.quirks = quirks
	p.sheets = append(p.sheets[:0:0], sheets...)
	p.bad = false
	e := NewEngineQ(&p.env, p.sheets, quirks)
	p.eng = e
	p.analyze(e)
	p.ents = map[*html.Node]*pent{}
	p.pents = map[pkey]*pent{}
	p.sibMemo = map[*html.Node]uint64{}
	p.epoch = 0
	if p.usable {
		e.persist = p
	}
	p.beginPass()
	return e
}

func (p *Persist) beginPass() {
	p.epoch++
	p.visited = 0
	clear(p.sibMemo)
}

// analyze derives, from the engine's rules, which inputs styles depend on.
func (p *Persist) analyze(e *Engine) {
	p.usable = !e.hasCQ
	p.sib, p.ancSib, p.empty = false, false, false
	p.rel = make(map[string]struct{}, 64)
	for _, a := range hintAttrs {
		p.rel[a] = struct{}{}
	}
	for _, r := range e.rules {
		if r.Scope != nil || r.Cont != nil {
			p.usable = false
		}
		for _, en := range r.Entries {
			f := en.Sel.Features()
			if f.Complex {
				p.usable = false
			}
			for _, a := range f.Attrs {
				p.rel[a] = struct{}{}
			}
			p.sib = p.sib || f.Sib
			p.ancSib = p.ancSib || f.AncSib
			p.empty = p.empty || f.Empty
		}
	}
}

// EndPass releases entries of nodes the last pass did not reach.
func (p *Persist) EndPass() {
	if p.eng == nil || !p.usable {
		return
	}
	if len(p.ents)+len(p.pents) > 2*p.visited+512 {
		ne := make(map[*html.Node]*pent, p.visited)
		for n, e := range p.ents {
			if e.epoch == p.epoch {
				ne[n] = e
			}
		}
		np := make(map[pkey]*pent)
		for k, e := range p.pents {
			if e.epoch == p.epoch {
				np[k] = e
			}
		}
		p.ents, p.pents = ne, np
	}
}

const (
	fnvOff   = 14695981039346656037
	fnvPrime = 1099511628211
)

func hashStr(h uint64, s string) uint64 {
	for i := 0; i < len(s); i++ {
		h = (h ^ uint64(s[i])) * fnvPrime
	}
	return (h ^ 0xff) * fnvPrime
}

func hashMix(h, v uint64) uint64 {
	h ^= v + 0x9E3779B97F4A7C15 + h<<6 + h>>2
	return h * fnvPrime
}

// attrsHash hashes the relevant attributes of n, in document order.
func (p *Persist) attrsHash(n *html.Node) uint64 {
	h := uint64(fnvOff)
	for i := range n.Attr {
		a := &n.Attr[i]
		if _, ok := p.rel[a.Key]; !ok {
			continue
		}
		h = hashStr(h, a.Key)
		h = hashStr(h, a.Val)
	}
	return h
}

// sibSig identifies the element children of parent: which nodes, in which
// order, with which relevant attributes.
func (p *Persist) sibSig(parent *html.Node) uint64 {
	if parent == nil {
		return 0
	}
	if v, ok := p.sibMemo[parent]; ok {
		return v
	}
	h := uint64(fnvOff)
	for c := parent.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode {
			continue
		}
		h = hashMix(h, uint64(uintptr(unsafe.Pointer(c))))
		h = hashMix(h, p.attrsHash(c))
		if p.empty {
			h = hashMix(h, emptyFlag(c))
		}
	}
	p.sibMemo[parent] = h
	return h
}

// emptyFlag mirrors the matcher's :empty test.
func emptyFlag(n *html.Node) uint64 {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode || (c.Type == html.TextNode && c.Data != "") {
			return 0
		}
	}
	return 1
}

// compute is Engine.Compute with the cache.
func (p *Persist) compute(e *Engine, n *html.Node, parent *Computed, pseudo string) *Computed {
	if parent == nil {
		parent = e.initial
	}
	if p.bad || n.Type != html.ElementNode {
		return e.computeNoCache(n, parent, pseudo)
	}
	var anc, own, attrsH uint64
	var ent *pent
	if pseudo == "" {
		if par := n.Parent; par != nil && par.Type == html.ElementNode {
			pe := p.ents[par]
			if pe == nil || pe.epoch != p.epoch {
				return e.computeNoCache(n, parent, pseudo)
			}
			anc = pe.childA
		}
		attrsH = p.attrsHash(n)
		own = hashMix(hashStr(fnvOff, n.Data), attrsH)
		if p.sib {
			own = hashMix(own, p.sibSig(n.Parent))
		}
		if p.empty {
			own = hashMix(own, emptyFlag(n))
		}
		ent = p.ents[n]
	} else {
		he := p.ents[n]
		if he == nil || he.epoch != p.epoch {
			return e.computeNoCache(n, parent, pseudo)
		}
		anc, own = he.anc, he.own
		ent = p.pents[pkey{n, pseudo}]
	}
	if ent != nil && ent.own == own && ent.anc == anc && ent.parent == parent {
		ent.epoch = p.epoch
		p.visited++
		p.Hits++
		return ent.c
	}
	p.Misses++
	c := e.computeNoCache(n, parent, pseudo)
	if pseudo == "" && c != nil && c.ContainerType != "" && c.ContainerType != "normal" {
		p.bad = true
		return c
	}
	if ent == nil {
		ent = &pent{}
		if pseudo == "" {
			p.ents[n] = ent
		} else {
			p.pents[pkey{n, pseudo}] = ent
		}
	}
	ent.c, ent.parent, ent.own, ent.anc, ent.epoch = c, parent, own, anc, p.epoch
	if pseudo == "" {
		ca := hashMix(hashMix(anc, hashStr(fnvOff, n.Data)), attrsH)
		if p.ancSib {
			ca = hashMix(ca, p.sibSig(n.Parent))
		}
		ent.childA = ca
	}
	p.visited++
	return c
}

// NodeKey identifies everything the cascade saw when it last computed the style
// of element n in the current pass: anc covers its ancestors, own the element
// itself (and its siblings when selectors use them). Equal keys (and an equal
// parent style) mean the same style. ok is false when n was not computed in
// this pass or the cache is not in use.
func (p *Persist) NodeKey(n *html.Node) (anc, own uint64, ok bool) {
	if p == nil || !p.usable || p.bad || p.eng == nil {
		return 0, 0, false
	}
	e := p.ents[n]
	if e == nil || e.epoch != p.epoch {
		return 0, 0, false
	}
	return e.anc, e.own, true
}

// HashString and HashMix expose the cache's hashing to callers that derive
// keys from NodeKey.
func HashString(h uint64, s string) uint64 { return hashStr(h, s) }
func HashMix(h, v uint64) uint64           { return hashMix(h, v) }

// FNVOffset is the starting value for HashString chains.
const FNVOffset = fnvOff
