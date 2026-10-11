package net

import "golang.org/x/net/html"

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
