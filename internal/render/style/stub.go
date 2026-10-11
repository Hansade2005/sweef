package style

import "golang.org/x/net/html"

// resolverImpl is a placeholder until the cascade lands: block-level tags
// are blocks, everything else inline, with initial values throughout.
type resolverImpl struct {
	r     *Resolver
	cache map[*html.Node]*ComputedStyle
}

func newResolverImpl(r *Resolver) *resolverImpl {
	return &resolverImpl{r: r, cache: map[*html.Node]*ComputedStyle{}}
}

func (ri *resolverImpl) style(n *html.Node) *ComputedStyle {
	if cs, ok := ri.cache[n]; ok {
		return cs
	}
	cs := Initial()
	switch ri.r.Doc.ComputedDisplay(n) {
	case "none":
		cs.Display = Display{Outer: OuterNone}
	case "block", "list-item", "flex", "grid", "table":
		cs.Display = Display{Outer: OuterBlock, Inner: InnerFlow}
	}
	ri.cache[n] = cs
	return cs
}

func (ri *resolverImpl) pseudo(n *html.Node, p string) *ComputedStyle { return nil }
