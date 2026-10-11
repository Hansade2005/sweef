package net

import (
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// selector.go — a CSS selector engine covering what pages and agents use:
// compound selectors, the four combinators (descendant, >, +, ~), comma
// groups, attribute operators (= ~= |= ^= $= *= with the i flag), and the
// pseudo-classes :not() :is() :where() :has() :first-child :last-child
// :only-child :nth-child(an+b) :nth-last-child() :first-of-type :last-of-type
// :nth-of-type() :only-of-type :empty :root :checked :disabled :enabled
// :required :optional :link :any-link :focus :scope. Unknown pseudo-classes
// match nothing; a malformed selector is a SyntaxError.

type selGroup []complexSel // comma-separated alternatives

type complexSel struct {
	parts []compoundSel // left to right; combinator on part i joins i-1 and i
}

type compoundSel struct {
	ci      bool // quirks mode: class and id selectors match ASCII case-insensitively
	comb    byte // 0 (first), ' ', '>', '+', '~'
	tag     string
	id      string
	classes []string
	attrs   []attrSel
	pseudos []pseudoSel
}

type attrSel struct {
	name, op, val string
	ci            bool
}

type pseudoSel struct {
	name string
	arg  string
	sub  selGroup // for :not/:is/:where/:has; the "of S" filter of :nth-child
	rel  []relSel // for :has
	a, b int      // for nth-*
}

// relSel is one relative selector of :has(): a leading combinator and the
// complex selector it applies to.
type relSel struct {
	lead byte // ' ', '>', '+', '~'
	sel  complexSel
}

type selParser struct {
	s      string
	pos    int
	quirks bool
}

// compileSelector parses sel or returns a SyntaxError.
func compileSelector(sel string) (selGroup, error) { return compileSelectorQ(sel, false) }

// compileSelectorQ is compileSelector with quirks-mode class/id matching.
func compileSelectorQ(sel string, quirks bool) (selGroup, error) {
	p := &selParser{s: strings.TrimSpace(sel), quirks: quirks}
	if p.s == "" {
		return nil, fmt.Errorf("SyntaxError: '%s' is not a valid selector", sel)
	}
	g, err := p.group()
	if err != nil {
		return nil, fmt.Errorf("SyntaxError: '%s' is not a valid selector: %v", sel, err)
	}
	if p.pos != len(p.s) {
		return nil, fmt.Errorf("SyntaxError: '%s' is not a valid selector (unexpected %q)", sel, p.s[p.pos:])
	}
	return g, nil
}

func (p *selParser) peek() byte {
	if p.pos < len(p.s) {
		return p.s[p.pos]
	}
	return 0
}

func (p *selParser) skipWS() bool {
	start := p.pos
	for p.pos < len(p.s) && (p.s[p.pos] == ' ' || p.s[p.pos] == '\t' || p.s[p.pos] == '\n') {
		p.pos++
	}
	return p.pos > start
}

func (p *selParser) group() (selGroup, error) {
	var g selGroup
	for {
		p.skipWS()
		c, err := p.complex()
		if err != nil {
			return nil, err
		}
		g = append(g, c)
		p.skipWS()
		if p.peek() == ',' {
			p.pos++
			continue
		}
		return g, nil
	}
}

func (p *selParser) complex() (complexSel, error) {
	var c complexSel
	first, err := p.compound()
	if err != nil {
		return c, err
	}
	c.parts = append(c.parts, first)
	for {
		hadWS := p.skipWS()
		ch := p.peek()
		var comb byte
		switch ch {
		case '>', '+', '~':
			comb = ch
			p.pos++
			p.skipWS()
		case 0, ',', ')':
			return c, nil
		default:
			if !hadWS {
				return c, fmt.Errorf("unexpected %q", string(ch))
			}
			comb = ' '
		}
		next, err := p.compound()
		if err != nil {
			return c, err
		}
		next.comb = comb
		c.parts = append(c.parts, next)
	}
}

func isNameChar(c byte) bool {
	return c == '-' || c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

func (p *selParser) ident() string {
	start := p.pos
	for p.pos < len(p.s) {
		c := p.s[p.pos]
		if isNameChar(c) {
			p.pos++
			continue
		}
		if c == '\\' && p.pos+1 < len(p.s) { // escaped char
			p.pos += 2
			continue
		}
		break
	}
	return unescapeCSS(p.s[start:p.pos])
}

func unescapeCSS(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func (p *selParser) compound() (compoundSel, error) {
	var c compoundSel
	c.ci = p.quirks
	any := false
	if p.peek() == '*' {
		p.pos++
		any = true
	} else if isNameChar(p.peek()) && p.peek() != '-' || p.peek() == '\\' {
		c.tag = strings.ToLower(p.ident())
		any = true
	}
	for {
		switch p.peek() {
		case '#':
			p.pos++
			c.id = p.ident()
		case '.':
			p.pos++
			c.classes = append(c.classes, p.ident())
		case '[':
			a, err := p.attr()
			if err != nil {
				return c, err
			}
			c.attrs = append(c.attrs, a)
		case ':':
			ps, err := p.pseudo()
			if err != nil {
				return c, err
			}
			if ps.name != "" {
				c.pseudos = append(c.pseudos, ps)
			}
		default:
			if !any && c.id == "" && len(c.classes) == 0 && len(c.attrs) == 0 && len(c.pseudos) == 0 {
				return c, fmt.Errorf("expected selector")
			}
			return c, nil
		}
		any = true
	}
}

func (p *selParser) attr() (attrSel, error) {
	var a attrSel
	p.pos++ // [
	p.skipWS()
	a.name = strings.ToLower(p.ident())
	if a.name == "" {
		return a, fmt.Errorf("attribute name expected")
	}
	p.skipWS()
	switch p.peek() {
	case ']':
		p.pos++
		return a, nil
	case '=':
		a.op = "="
		p.pos++
	case '~', '|', '^', '$', '*':
		a.op = string(p.peek()) + "="
		p.pos++
		if p.peek() != '=' {
			return a, fmt.Errorf("expected = in attribute selector")
		}
		p.pos++
	default:
		return a, fmt.Errorf("bad attribute selector")
	}
	p.skipWS()
	if q := p.peek(); q == '"' || q == '\'' {
		p.pos++
		start := p.pos
		for p.pos < len(p.s) && p.s[p.pos] != q {
			if p.s[p.pos] == '\\' {
				p.pos++
			}
			p.pos++
		}
		a.val = unescapeCSS(p.s[start:p.pos])
		p.pos++ // closing quote
	} else {
		a.val = p.ident()
	}
	p.skipWS()
	if p.peek() == 'i' || p.peek() == 'I' {
		a.ci = true
		p.pos++
		p.skipWS()
	} else if p.peek() == 's' || p.peek() == 'S' {
		p.pos++
		p.skipWS()
	}
	if p.peek() != ']' {
		return a, fmt.Errorf("expected ]")
	}
	p.pos++
	return a, nil
}

func (p *selParser) pseudo() (pseudoSel, error) {
	var ps pseudoSel
	p.pos++              // :
	if p.peek() == ':' { // pseudo-element: parse and ignore (matches nothing)
		p.pos++
		p.ident()
		if p.peek() == '(' {
			p.balanced()
		}
		return pseudoSel{name: "::"}, nil
	}
	ps.name = strings.ToLower(p.ident())
	if ps.name == "" {
		return ps, fmt.Errorf("pseudo-class name expected")
	}
	if p.peek() == '(' {
		arg := p.balanced()
		ps.arg = strings.TrimSpace(arg)
		switch ps.name {
		case "not", "is", "where", "matches", "-webkit-any":
			sub, err := compileSelectorQ(ps.arg, p.quirks)
			if err != nil {
				return ps, err
			}
			ps.sub = sub
		case "has":
			rels, err := p.relSelectors(ps.arg)
			if err != nil {
				return ps, err
			}
			ps.rel = rels
			// the plain group is kept for specificity (see compoundSpecificity)
			for _, r := range rels {
				ps.sub = append(ps.sub, r.sel)
			}
		case "nth-child", "nth-last-child", "nth-of-type", "nth-last-of-type":
			arg := ps.arg
			if (ps.name == "nth-child" || ps.name == "nth-last-child") && strings.Contains(strings.ToLower(arg), " of ") {
				i := strings.Index(strings.ToLower(arg), " of ")
				sub, err := compileSelectorQ(arg[i+4:], p.quirks)
				if err != nil {
					return ps, err
				}
				ps.sub = sub
				arg = arg[:i]
			}
			a, b, err := parseNth(arg)
			if err != nil {
				return ps, err
			}
			ps.a, ps.b = a, b
		}
	}
	return ps, nil
}

// relSelectors parses the argument of :has() into relative selectors.
func (p *selParser) relSelectors(arg string) ([]relSel, error) {
	var out []relSel
	depth := 0
	start := 0
	parts := []string{}
	for i := 0; i < len(arg); i++ {
		switch arg[i] {
		case '(', '[':
			depth++
		case ')', ']':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, arg[start:i])
				start = i + 1
			}
		}
	}
	parts = append(parts, arg[start:])
	for _, part := range parts {
		part = strings.TrimSpace(part)
		lead := byte(' ')
		if part != "" && (part[0] == '>' || part[0] == '+' || part[0] == '~') {
			lead = part[0]
			part = strings.TrimSpace(part[1:])
		}
		g, err := compileSelectorQ(part, p.quirks)
		if err != nil {
			return nil, err
		}
		for _, c := range g {
			out = append(out, relSel{lead: lead, sel: c})
		}
	}
	return out, nil
}

// balanced consumes "( ... )" with nesting and returns the inner text.
func (p *selParser) balanced() string {
	depth := 0
	start := p.pos + 1
	for p.pos < len(p.s) {
		switch p.s[p.pos] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				inner := p.s[start:p.pos]
				p.pos++
				return inner
			}
		case '"', '\'':
			q := p.s[p.pos]
			p.pos++
			for p.pos < len(p.s) && p.s[p.pos] != q {
				p.pos++
			}
		}
		p.pos++
	}
	return p.s[start:]
}

func parseNth(s string) (a, b int, err error) {
	s = strings.ToLower(strings.ReplaceAll(s, " ", ""))
	if i := strings.Index(s, "of"); i > 0 { // "2n+1 of .x" — selector list part ignored
		s = s[:i]
	}
	switch s {
	case "odd":
		return 2, 1, nil
	case "even":
		return 2, 0, nil
	}
	n := strings.IndexByte(s, 'n')
	if n < 0 {
		b, err = strconv.Atoi(s)
		return 0, b, err
	}
	as := s[:n]
	switch as {
	case "", "+":
		a = 1
	case "-":
		a = -1
	default:
		if a, err = strconv.Atoi(as); err != nil {
			return
		}
	}
	bs := s[n+1:]
	if bs != "" {
		if b, err = strconv.Atoi(strings.TrimPrefix(bs, "+")); err != nil {
			return
		}
	}
	return a, b, nil
}

// ---- matching -------------------------------------------------------------------

func matchGroup(n *html.Node, g selGroup, scope *html.Node) bool {
	for _, c := range g {
		if matchComplex(n, c, len(c.parts)-1, scope) {
			return true
		}
	}
	return false
}

func matchComplex(n *html.Node, c complexSel, i int, scope *html.Node) bool {
	if !matchCompound(n, c.parts[i], scope) {
		return false
	}
	if i == 0 {
		return true
	}
	switch c.parts[i].comb {
	case '>':
		p := n.Parent
		return p != nil && p.Type == html.ElementNode && matchComplex(p, c, i-1, scope)
	case '+':
		p := prevElement(n)
		return p != nil && matchComplex(p, c, i-1, scope)
	case '~':
		for p := prevElement(n); p != nil; p = prevElement(p) {
			if matchComplex(p, c, i-1, scope) {
				return true
			}
		}
		return false
	default: // descendant
		for p := n.Parent; p != nil && p.Type == html.ElementNode; p = p.Parent {
			if matchComplex(p, c, i-1, scope) {
				return true
			}
		}
		return false
	}
}

func prevElement(n *html.Node) *html.Node {
	for p := n.PrevSibling; p != nil; p = p.PrevSibling {
		if p.Type == html.ElementNode {
			return p
		}
	}
	return nil
}

func nextElement(n *html.Node) *html.Node {
	for p := n.NextSibling; p != nil; p = p.NextSibling {
		if p.Type == html.ElementNode {
			return p
		}
	}
	return nil
}

func matchCompound(n *html.Node, c compoundSel, scope *html.Node) bool {
	if n.Type != html.ElementNode {
		return false
	}
	if c.tag != "" && c.tag != "*" && n.Data != c.tag {
		return false
	}
	if c.id != "" {
		if id := attr(n, "id"); id != c.id && !(c.ci && strings.EqualFold(id, c.id)) {
			return false
		}
	}
	if len(c.classes) > 0 {
		cls := attr(n, "class")
		for _, k := range c.classes {
			if !hasClass(cls, k) && !(c.ci && hasClassFold(cls, k)) {
				return false
			}
		}
	}
	for _, a := range c.attrs {
		if !matchAttr(n, a) {
			return false
		}
	}
	for _, ps := range c.pseudos {
		if !matchPseudo(n, ps, scope) {
			return false
		}
	}
	return true
}

// hasClass reports whether the whitespace-separated class list cls contains
// k, without allocating (this sits on the hot path of the CSS cascade).
func hasClass(cls, k string) bool {
	if k == "" || len(cls) < len(k) {
		return false
	}
	isWS := func(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\f' }
	i := 0
	for i < len(cls) {
		for i < len(cls) && isWS(cls[i]) {
			i++
		}
		j := i
		for j < len(cls) && !isWS(cls[j]) {
			j++
		}
		if j-i == len(k) && cls[i:j] == k {
			return true
		}
		i = j
	}
	return false
}

func hasClassFold(cls, k string) bool {
	for _, f := range strings.Fields(cls) {
		if strings.EqualFold(f, k) {
			return true
		}
	}
	return false
}

func matchAttr(n *html.Node, a attrSel) bool {
	v, ok := "", false
	for _, at := range n.Attr {
		if at.Key == a.name {
			v, ok = at.Val, true
			break
		}
	}
	if !ok {
		return false
	}
	if a.op == "" {
		return true
	}
	want := a.val
	if a.ci {
		v, want = strings.ToLower(v), strings.ToLower(want)
	}
	switch a.op {
	case "=":
		return v == want
	case "~=":
		for _, f := range strings.Fields(v) {
			if f == want {
				return true
			}
		}
		return false
	case "|=":
		return v == want || strings.HasPrefix(v, want+"-")
	case "^=":
		return want != "" && strings.HasPrefix(v, want)
	case "$=":
		return want != "" && strings.HasSuffix(v, want)
	case "*=":
		return want != "" && strings.Contains(v, want)
	}
	return false
}

func nthMatches(a, b, idx int) bool {
	if a == 0 {
		return idx == b
	}
	k := idx - b
	return k%a == 0 && k/a >= 0
}

func childIndex(n *html.Node, fromEnd, ofType bool) int {
	idx := 1
	if fromEnd {
		for s := n.NextSibling; s != nil; s = s.NextSibling {
			if s.Type == html.ElementNode && (!ofType || s.Data == n.Data) {
				idx++
			}
		}
		return idx
	}
	for s := n.PrevSibling; s != nil; s = s.PrevSibling {
		if s.Type == html.ElementNode && (!ofType || s.Data == n.Data) {
			idx++
		}
	}
	return idx
}

// childIndexOf is the index of n among the siblings that match filter
// (:nth-child(an+b of S)); 0 when n itself does not match.
func childIndexOf(n *html.Node, fromEnd bool, filter selGroup, scope *html.Node) int {
	if !matchGroup(n, filter, scope) {
		return 0
	}
	idx := 1
	if fromEnd {
		for s := n.NextSibling; s != nil; s = s.NextSibling {
			if s.Type == html.ElementNode && matchGroup(s, filter, scope) {
				idx++
			}
		}
		return idx
	}
	for s := n.PrevSibling; s != nil; s = s.PrevSibling {
		if s.Type == html.ElementNode && matchGroup(s, filter, scope) {
			idx++
		}
	}
	return idx
}

func matchPseudo(n *html.Node, ps pseudoSel, scope *html.Node) bool {
	switch ps.name {
	case "::":
		return false
	case "not":
		return !matchGroup(n, ps.sub, scope)
	case "is", "where", "matches", "-webkit-any":
		return matchGroup(n, ps.sub, scope)
	case "has":
		for _, r := range ps.rel {
			if matchRelative(n, r, scope) {
				return true
			}
		}
		return false
	case "first-child":
		return prevElement(n) == nil
	case "last-child":
		return nextElement(n) == nil
	case "only-child":
		return prevElement(n) == nil && nextElement(n) == nil
	case "nth-child":
		if len(ps.sub) > 0 {
			i := childIndexOf(n, false, ps.sub, scope)
			return i > 0 && nthMatches(ps.a, ps.b, i)
		}
		return nthMatches(ps.a, ps.b, childIndex(n, false, false))
	case "nth-last-child":
		if len(ps.sub) > 0 {
			i := childIndexOf(n, true, ps.sub, scope)
			return i > 0 && nthMatches(ps.a, ps.b, i)
		}
		return nthMatches(ps.a, ps.b, childIndex(n, true, false))
	case "nth-of-type":
		return nthMatches(ps.a, ps.b, childIndex(n, false, true))
	case "nth-last-of-type":
		return nthMatches(ps.a, ps.b, childIndex(n, true, true))
	case "first-of-type":
		return childIndex(n, false, true) == 1
	case "last-of-type":
		return childIndex(n, true, true) == 1
	case "only-of-type":
		return childIndex(n, false, true) == 1 && childIndex(n, true, true) == 1
	case "empty":
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.ElementNode || (c.Type == html.TextNode && c.Data != "") {
				return false
			}
		}
		return true
	case "root":
		return n.Data == "html" && (n.Parent == nil || n.Parent.Type == html.DocumentNode)
	case "scope":
		return scope == nil || n == scope
	case "checked":
		return hasAttr(n, "checked") || (n.Data == "option" && hasAttr(n, "selected"))
	case "disabled":
		return hasAttr(n, "disabled")
	case "enabled":
		switch n.Data {
		case "input", "button", "select", "textarea", "option", "fieldset":
			return !hasAttr(n, "disabled")
		}
		return false
	case "required":
		return hasAttr(n, "required")
	case "optional":
		switch n.Data {
		case "input", "select", "textarea":
			return !hasAttr(n, "required")
		}
		return false
	case "link", "any-link":
		return (n.Data == "a" || n.Data == "area") && hasAttr(n, "href")
	case "focus", "focus-visible", "focus-within":
		return hasAttr(n, "data-sw-focused")
	case "hover", "active", "visited", "target", "placeholder-shown", "defined":
		return ps.name == "defined"
	case "read-only":
		return hasAttr(n, "readonly")
	case "read-write":
		return (n.Data == "input" || n.Data == "textarea") && !hasAttr(n, "readonly")
	case "lang", "dir":
		return true
	}
	return false
}

// ---- Doc entry points ---------------------------------------------------------

// queryAll returns every element in the document matching sel; a malformed
// selector matches nothing (ResolveEngine reports the error separately).
func (d *Doc) queryAll(sel string) []*html.Node {
	g, err := compileSelector(sel)
	if err != nil {
		return nil
	}
	return collectMatches(d.Root, g, nil)
}

func collectMatches(root *html.Node, g selGroup, scope *html.Node) []*html.Node {
	var out []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.ElementNode && matchGroup(c, g, scope) {
				out = append(out, c)
			}
			walk(c)
		}
	}
	walk(root)
	return out
}

func (d *Doc) queryOne(sel string) (*html.Node, error) {
	g, err := compileSelector(sel)
	if err != nil {
		return nil, err
	}
	all := collectMatches(d.Root, g, nil)
	if len(all) == 0 {
		return nil, fmt.Errorf("no node matches %q", sel)
	}
	return all[0], nil
}

// queryAllFrom walks the descendants of root (exclusive) in document order.
func (d *Doc) queryAllFrom(root *html.Node, sel string) ([]*html.Node, error) {
	g, err := compileSelector(sel)
	if err != nil {
		return nil, err
	}
	scope := root
	if root == d.Root {
		scope = nil
	}
	return collectMatches(root, g, scope), nil
}

// matchesSelector reports whether n matches sel.
func matchesSelector(n *html.Node, sel string) (bool, error) {
	g, err := compileSelector(sel)
	if err != nil {
		return false, err
	}
	return matchGroup(n, g, nil), nil
}

// matchRelative reports whether a relative selector of :has() matches with
// anchor as its left end.
func matchRelative(anchor *html.Node, r relSel, scope *html.Node) bool {
	last := len(r.sel.parts) - 1
	var try func(x *html.Node) bool
	try = func(x *html.Node) bool {
		return matchComplexRel(x, r.sel, last, scope, anchor, r.lead)
	}
	var walk func(x *html.Node) bool
	walk = func(x *html.Node) bool {
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			if c.Type != html.ElementNode {
				continue
			}
			if try(c) || walk(c) {
				return true
			}
		}
		return false
	}
	switch r.lead {
	case '+', '~':
		for s := anchor.NextSibling; s != nil; s = s.NextSibling {
			if s.Type != html.ElementNode {
				continue
			}
			if try(s) || walk(s) {
				return true
			}
			if r.lead == '+' && len(r.sel.parts) == 1 {
				break
			}
		}
		return false
	}
	return walk(anchor)
}

// matchComplexRel is matchComplex for a relative selector: once the leftmost
// compound is reached, its element must stand in the leading combinator's
// relation to anchor.
func matchComplexRel(n *html.Node, c complexSel, i int, scope, anchor *html.Node, lead byte) bool {
	if !matchCompound(n, c.parts[i], scope) {
		return false
	}
	if i == 0 {
		switch lead {
		case '>':
			return n.Parent == anchor
		case '+':
			return prevElement(n) == anchor
		case '~':
			for p := prevElement(n); p != nil; p = prevElement(p) {
				if p == anchor {
					return true
				}
			}
			return false
		default:
			for p := n.Parent; p != nil; p = p.Parent {
				if p == anchor {
					return true
				}
			}
			return false
		}
	}
	switch c.parts[i].comb {
	case '>':
		p := n.Parent
		return p != nil && p.Type == html.ElementNode && matchComplexRel(p, c, i-1, scope, anchor, lead)
	case '+':
		p := prevElement(n)
		return p != nil && matchComplexRel(p, c, i-1, scope, anchor, lead)
	case '~':
		for p := prevElement(n); p != nil; p = prevElement(p) {
			if matchComplexRel(p, c, i-1, scope, anchor, lead) {
				return true
			}
		}
		return false
	default:
		for p := n.Parent; p != nil && p.Type == html.ElementNode; p = p.Parent {
			if matchComplexRel(p, c, i-1, scope, anchor, lead) {
				return true
			}
		}
		return false
	}
}
