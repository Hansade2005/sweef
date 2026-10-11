package style

import (
	"strings"

	"golang.org/x/net/html"

	snet "github.com/sweefbrowser/sweef/internal/net"
)

// ScopeCond is the condition of rules inside an @scope block.
type ScopeCond struct {
	Root, Limit *snet.Selector // Root nil: the document root
	Outer       *ScopeCond
}

// parseScopePrelude parses "(root) to (limit)" / "to (limit)" / "(root)".
func parseScopePrelude(args string, quirks bool) *ScopeCond {
	args = strings.TrimSpace(args)
	sc := &ScopeCond{}
	take := func() (string, bool) {
		if args == "" || args[0] != '(' {
			return "", false
		}
		depth := 0
		for i := 0; i < len(args); i++ {
			switch args[i] {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					in := args[1:i]
					args = strings.TrimSpace(args[i+1:])
					return in, true
				}
			}
		}
		return "", false
	}
	if in, ok := take(); ok {
		sel, err := snet.CompileSelectorQ(in, quirks)
		if err != nil {
			return nil
		}
		sc.Root = sel
	}
	if strings.HasPrefix(strings.ToLower(args), "to") {
		args = strings.TrimSpace(args[2:])
		in, ok := take()
		if !ok {
			return nil
		}
		sel, err := snet.CompileSelectorQ(in, quirks)
		if err != nil {
			return nil
		}
		sc.Limit = sel
	}
	if args != "" {
		return nil
	}
	return sc
}

// scopeRootFor finds the closest scoping root of n for the condition (nil
// when n is outside every scope).
func scopeRootFor(n *html.Node, sc *ScopeCond) *html.Node {
	var root *html.Node
	for c := sc; c != nil; c = c.Outer {
		r := scopeRoot1(n, c)
		if r == nil {
			return nil
		}
		if root == nil {
			root = r
		}
	}
	return root
}

func scopeRoot1(n *html.Node, sc *ScopeCond) *html.Node {
	for a := n; a != nil && a.Type == html.ElementNode; a = a.Parent {
		isRoot := false
		if sc.Root == nil {
			isRoot = a.Data == "html" && (a.Parent == nil || a.Parent.Type == html.DocumentNode)
		} else {
			isRoot = sc.Root.Match(a, nil)
		}
		if !isRoot {
			continue
		}
		if sc.Limit != nil {
			// no scoping limit may sit between the root and n (limits are matched with :scope = root)
			for l := n; l != nil && l != a; l = l.Parent {
				if l.Type == html.ElementNode && sc.Limit.Match(l, a) {
					goto next
				}
			}
		}
		return a
	next:
	}
	return nil
}
