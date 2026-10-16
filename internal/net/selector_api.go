package net

import (
	"strings"

	"golang.org/x/net/html"
)

// Public selector and media-query API for packages that run their own
// cascade (internal/render): the same matcher the DOM, locators and
// querySelector use, so selectors behave identically everywhere.

// Selector is a compiled selector list ("a, b > c").
type Selector struct{ g selGroup }

// SelectorEntry is one comma-separated alternative with its specificity.
type SelectorEntry struct {
	c    complexSel
	Spec uint32 // ids<<20 | classes+attrs+pseudos<<10 | tags
}

// CompileSelector parses a selector list; a SyntaxError is returned when invalid.
func CompileSelector(s string) (*Selector, error) {
	g, err := compileSelector(s)
	if err != nil {
		return nil, err
	}
	return &Selector{g: g}, nil
}

// CompileSelectorQ is CompileSelector with quirks-mode matching: class and id
// selectors compare ASCII case-insensitively.
func CompileSelectorQ(s string, quirks bool) (*Selector, error) {
	g, err := compileSelectorQ(s, quirks)
	if err != nil {
		return nil, err
	}
	return &Selector{g: g}, nil
}

// Entries returns the alternatives of the list, each matchable on its own.
func (s *Selector) Entries() []SelectorEntry {
	out := make([]SelectorEntry, len(s.g))
	for i, c := range s.g {
		out[i] = SelectorEntry{c: c, Spec: specificity(c)}
	}
	return out
}

// Match reports whether n matches any alternative; scope is the :scope
// element (nil = document root).
func (s *Selector) Match(n *html.Node, scope *html.Node) bool { return matchGroup(n, s.g, scope) }

// Match reports whether n matches this alternative.
func (e SelectorEntry) Match(n *html.Node, scope *html.Node) bool {
	return matchComplex(n, e.c, len(e.c.parts)-1, scope)
}

// MediaMatches evaluates a media query list ("screen and (min-width: 600px)")
// against a viewport width and color scheme.
func MediaMatches(query string, viewportWidth int, colorScheme string) bool {
	if query == "" {
		return true
	}
	return evalMediaQuery(query, viewportWidth, colorScheme)
}

// Bloom is a 128-bit set of hashed, ASCII case-folded tag, id and class names: the
// ancestor filter of style engines. A rule whose selector needs `.menu` on some
// ancestor of the subject cannot match an element none of whose ancestors carries
// a name hashing to that bit, so the matcher is never run for it. Folding the case
// keeps the filter a superset of every comparison the matcher makes (quirks-mode
// class and id matching is case-insensitive).
type Bloom [2]uint64

func foldBit(s string) Bloom {
	h := uint32(2166136261)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		h = (h ^ uint32(c)) * 16777619
	}
	var b Bloom
	b[(h>>6)&1] |= 1 << (h & 63)
	return b
}

// Add sets the bits of o in b.
func (b *Bloom) Add(o Bloom) { b[0] |= o[0]; b[1] |= o[1] }

// Subset reports whether every bit of b is set in o.
func (b Bloom) Subset(o Bloom) bool { return b[0]&^o[0] == 0 && b[1]&^o[1] == 0 }

// NodeBloom hashes the element's own tag, id and classes.
func NodeBloom(n *html.Node) Bloom {
	b := foldBit(n.Data)
	for _, a := range n.Attr {
		switch a.Key {
		case "id":
			if a.Val != "" {
				b.Add(foldBit(a.Val))
			}
		case "class":
			for _, c := range strings.Fields(a.Val) { // the matcher's own split (matchCompound)
				b.Add(foldBit(c))
			}
		}
	}
	return b
}

// AncestorNeed collects the names some ancestor of a matching element must
// carry: every compound joined to its right-hand neighbour by a descendant or
// child combinator matches an ancestor of the subject (a compound to the right
// of it is the subject, an ancestor, or a sibling of one of those). Pseudo-classes
// (:is(), :not(), :has()...) add nothing, so the set only ever under-states.
func (e SelectorEntry) AncestorNeed() Bloom {
	var b Bloom
	c := e.c
	for j := 0; j+1 < len(c.parts); j++ {
		if cb := c.parts[j+1].comb; cb != ' ' && cb != '>' {
			continue
		}
		p := c.parts[j]
		if p.tag != "" && p.tag != "*" {
			b.Add(foldBit(p.tag))
		}
		if p.id != "" {
			b.Add(foldBit(p.id))
		}
		for _, k := range p.classes {
			b.Add(foldBit(k))
		}
	}
	return b
}
