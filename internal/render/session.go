package render

import (
	"strings"
	"unsafe"

	"golang.org/x/net/html"

	"github.com/sweefbrowser/sweef/internal/style"
)

// Session carries work from one layout pass over a document to the next:
// the style engine and computed styles (style.Persist) and resolved inline SVG
// scenes. The caller (one document's layout provider) owns it and runs one
// pass at a time. A nil *Session in Options means every pass starts from
// scratch, which is always correct; a Session must produce results identical
// to that (SWEEF_LAYOUT_CHECK in internal/engine verifies it).
type Session struct {
	Style *style.Persist
	svg   svgSess
}

// NewSession returns an empty session.
func NewSession() *Session { return &Session{Style: style.NewPersist()} }

// svgSess caches resolved scenes of inline <svg> elements across passes.
type svgSess struct {
	sc     *svgCascade
	scSig  svgSig // inputs the cascade was built from
	docs   map[*html.Node]*svgEnt
	union  *svgUnion
	Hits   int
	Builds int
}

type svgSig struct {
	sheets []Sheet
	env    style.Env
}

type svgEnt struct {
	doc  *SVGDoc
	sig  uint64
	used bool // looked up or stored in the current pass
	// merged is doc with the ids of the page's other svg elements, valid
	// while the union is mergedFor
	merged    *SVGDoc
	mergedFor *svgUnion
}

// svgUnion is the set of ids defined by all the page's inline svg elements.
type svgUnion struct {
	ids map[string]*SVGNode
}

func sameIDs(a, b map[string]*SVGNode) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// svgUnionLocked computes (once per pass) the union of the ids of all inline
// svg elements. ok is false when the session path must not be used: the legacy
// in-place merge then runs on scenes of this pass only (a document with the
// same id defined in two svg elements resolves it by build order).
func (c *ctx) svgUnionLocked(n *html.Node) (*svgUnion, bool) {
	if c.svgNoSess {
		return nil, false
	}
	if c.svgUDone {
		return c.svgU, true
	}
	c.svgUDone = true
	svgShared.mergeRootsLocked(n)
	roots := svgShared.roots
	if len(roots) <= 1 || len(roots) > 400 {
		return nil, true
	}
	ids := map[string]*SVGNode{}
	for _, o := range roots {
		d := c.svgBuildLocked(o, c.styles[o])
		if d == nil {
			continue
		}
		for id, nd := range d.ByID {
			if prev, dup := ids[id]; dup && prev != nd {
				c.svgNoSess = true
				c.sess.svg.docs = nil // scenes built so far are about to be merged in place
				c.sess.svg.union = nil
				return nil, false
			}
			ids[id] = nd
		}
	}
	if u := c.sess.svg.union; u != nil && sameIDs(u.ids, ids) {
		c.svgU = u
	} else {
		c.svgU = &svgUnion{ids: ids}
		c.sess.svg.union = c.svgU
	}
	return c.svgU, true
}

// svgMergeSessLocked returns raw with the page's ids merged in, without
// touching raw (it is shared with earlier results and with the next pass).
func (c *ctx) svgMergeSessLocked(n *html.Node, raw *SVGDoc, u *svgUnion) *SVGDoc {
	if u == nil || sameIDs(raw.ByID, u.ids) {
		return raw
	}
	ent := c.sess.svg.docs[n]
	if ent != nil && ent.doc != raw {
		ent = nil
	}
	if ent != nil && ent.merged != nil && ent.mergedFor == u {
		return ent.merged
	}
	m := *raw
	m.ByID = u.ids
	m.merged = true
	m.sigV, m.sigState = 0, 0
	if ent != nil {
		ent.merged, ent.mergedFor = &m, u
	}
	return &m
}

// sweep ends a pass: scenes of nodes the pass did not reach are dropped.
func (s *svgSess) sweep() {
	used := 0
	for _, e := range s.docs {
		if e.used {
			used++
		}
	}
	if len(s.docs) > 2*used+64 {
		for n, e := range s.docs {
			if !e.used {
				delete(s.docs, n)
			}
		}
	}
	for _, e := range s.docs {
		e.used = false
	}
}

func sameSheets(a, b []Sheet) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Text != b[i].Text || a[i].Media != b[i].Media || a[i].UserAgent != b[i].UserAgent {
			return false
		}
	}
	return true
}

// cascade returns the SVG cascade for this pass, reusing the previous one when
// the sheets and environment are the same. Its per-pass memo is reset.
func (s *svgSess) cascade(sheets []Sheet, env *style.Env, keyFn func(*html.Node) (uint64, uint64, bool)) *svgCascade {
	if s.sc != nil && s.scSig.env == *env && sameSheets(s.scSig.sheets, sheets) {
		s.sc.memo = nil
		s.sc.keyFn = keyFn
		return s.sc
	}
	s.sc = newSVGCascade(sheets, env, nil)
	s.sc.keyFn = keyFn
	s.scSig = svgSig{sheets: append([]Sheet(nil), sheets...), env: *env}
	s.docs = nil
	s.union = nil
	return s.sc
}

// ---- cross-pass scene cache ---------------------------------------------------------

// svgSubtree hashes the markup of an inline svg element (tags, attributes,
// text) and reports whether the scene can be kept across passes: it must not
// depend on anything resolved at layout time (external references,
// foreignObject content). refs reports a '#' in an attribute or text, i.e. a
// possible reference to an id elsewhere on the page.
func svgSubtree(n *html.Node) (h uint64, cacheable, refs, hasID bool) {
	h = style.FNVOffset
	cacheable = true
	var walk func(x *html.Node)
	walk = func(x *html.Node) {
		switch x.Type {
		case html.TextNode:
			h = style.HashString(h, x.Data)
			if strings.Contains(x.Data, "#") {
				refs = true
			}
			return
		case html.ElementNode:
		default:
			return
		}
		h = style.HashString(h, x.Data)
		tag := strings.ToLower(x.Data)
		if tag == "foreignobject" || tag == "image" || tag == "feimage" {
			cacheable = false
		}
		for _, a := range x.Attr {
			if a.Key == "id" && a.Val != "" {
				hasID = true
			}
			h = style.HashString(style.HashString(style.HashString(h, a.Namespace), a.Key), a.Val)
			if strings.Contains(a.Val, "#") {
				refs = true
			}
			if tag == "use" && (a.Key == "href" || a.Key == "xlink:href") && !strings.HasPrefix(strings.TrimSpace(a.Val), "#") {
				cacheable = false
			}
			if strings.Contains(a.Val, "url(") && !strings.Contains(a.Val, "url(#") {
				cacheable = false // external or data url in a presentation attribute
			}
		}
		h = style.HashMix(h, 0xfeed)
		for ch := x.FirstChild; ch != nil; ch = ch.NextSibling {
			walk(ch)
		}
		h = style.HashMix(h, 0xbeef)
	}
	walk(n)
	return
}

// svgKey is the cache key of the scene of n for this pass; ok is false when it
// cannot be cached.
func (c *ctx) svgKey(n *html.Node, st *style.Computed) (uint64, bool) {
	if c.sess == nil {
		return 0, false
	}
	anc, own, ok := c.sess.Style.NodeKey(n)
	if !ok {
		return 0, false
	}
	sh, cacheable, _, _ := svgSubtree(n)
	if !cacheable {
		return 0, false
	}
	k := style.HashMix(style.HashMix(anc, own), sh)
	k = style.HashMix(k, uint64(uintptr(unsafe.Pointer(st))))
	k = style.HashString(k, c.opts.BaseURL)
	return k, true
}
