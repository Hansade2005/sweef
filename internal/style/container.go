package style

import (
	"math"
	"strings"

	"golang.org/x/net/html"
)

// Container queries.
//
// @container rules are parsed into Rules carrying a ContCond. Whether such a
// rule applies to an element depends on the size of the element's nearest
// query container, which is only known after layout; the renderer runs layout
// repeatedly, feeding back the container sizes measured by the previous pass
// (Engine.SetContainerSizes) until they are stable.

func parseContainerNames(l string) []string {
	if l == "" || l == "none" || l == "normal" {
		return nil
	}
	return strings.Fields(l)
}

// ContSize is the content-box size of a query container.
type ContSize struct{ W, H float64 }

// contInfo is what the engine remembers about a container element.
type contInfo struct {
	typ      string
	names    []string
	fontSize float64
	rootFont float64
	vertical bool
}

// ContCond is the condition of one @container rule (conditions of nested
// @container blocks are chained through Outer).
type ContCond struct {
	Name  string
	Expr  *cqExpr
	Outer *ContCond
}

type cqExpr struct {
	op   byte // '&' and, '|' or, '!' not, 'f' feature, 'r' range, 's' style(), 'u' unknown(false)
	kids []*cqExpr
	feat string
	// range comparisons: lhs op value [op value]
	cmp1, cmp2   string
	val1, val2   cqVal
	has1, has2   bool
	flip         bool // feature written on the right of the first comparison
	plainKeyword string
}

type cqVal struct {
	num  float64
	unit string
	num2 float64 // ratio denominator
	kw   string
}

// ---- parsing ------------------------------------------------------------------------------

func parseContainerPrelude(args string) (*ContCond, bool) {
	args = strings.TrimSpace(args)
	if args == "" {
		return nil, false
	}
	name := ""
	// optional container name before the condition
	if args[0] != '(' && !strings.HasPrefix(strings.ToLower(args), "not") && !strings.HasPrefix(strings.ToLower(args), "style(") {
		i := strings.IndexAny(args, " \t\n(")
		if i < 0 {
			return nil, false
		}
		name = args[:i]
		args = strings.TrimSpace(args[i:])
	} else if strings.HasPrefix(strings.ToLower(args), "not") && len(args) > 3 && args[3] != ' ' && args[3] != '(' {
		i := strings.IndexAny(args, " \t\n(")
		if i < 0 {
			return nil, false
		}
		name = args[:i]
		args = strings.TrimSpace(args[i:])
	}
	e := parseCQExpr(args)
	if e == nil {
		return nil, false
	}
	return &ContCond{Name: name, Expr: e}, true
}

func parseCQExpr(s string) *cqExpr {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	ls := strings.ToLower(s)
	if strings.HasPrefix(ls, "not ") || strings.HasPrefix(ls, "not(") {
		k := parseCQTerm(strings.TrimSpace(s[3:]))
		if k == nil {
			return nil
		}
		return &cqExpr{op: '!', kids: []*cqExpr{k}}
	}
	// split on top-level and / or
	parts, op := splitCQ(s)
	if len(parts) == 1 {
		return parseCQTerm(parts[0])
	}
	e := &cqExpr{op: op}
	for _, p := range parts {
		k := parseCQTerm(p)
		if k == nil {
			return nil
		}
		e.kids = append(e.kids, k)
	}
	return e
}

func splitCQ(s string) ([]string, byte) {
	var parts []string
	depth := 0
	start := 0
	op := byte('&')
	ls := strings.ToLower(s)
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
		case ' ':
			if depth == 0 {
				for _, kw := range []string{" and ", " or "} {
					if strings.HasPrefix(ls[i:], kw) {
						parts = append(parts, strings.TrimSpace(s[start:i]))
						start = i + len(kw)
						i = start - 1
						if kw == " or " {
							op = '|'
						} else {
							op = '&'
						}
						break
					}
				}
			}
		}
	}
	parts = append(parts, strings.TrimSpace(s[start:]))
	return parts, op
}

func parseCQTerm(s string) *cqExpr {
	s = strings.TrimSpace(s)
	ls := strings.ToLower(s)
	if strings.HasPrefix(ls, "style(") {
		return &cqExpr{op: 's', feat: s[6 : len(s)-1]}
	}
	if strings.HasPrefix(ls, "not ") {
		k := parseCQTerm(s[4:])
		if k == nil {
			return nil
		}
		return &cqExpr{op: '!', kids: []*cqExpr{k}}
	}
	if len(s) < 2 || s[0] != '(' || s[len(s)-1] != ')' {
		return &cqExpr{op: 'u'}
	}
	in := strings.TrimSpace(s[1 : len(s)-1])
	li := strings.ToLower(in)
	// nested condition
	if strings.HasPrefix(li, "(") || strings.HasPrefix(li, "not ") {
		return parseCQExpr(in)
	}
	if strings.Contains(in, " and ") || strings.Contains(in, " or ") {
		if (strings.HasPrefix(li, "(")) || !strings.ContainsAny(in, "<>=:") {
			return parseCQExpr(in)
		}
	}
	// plain feature: (feature: value)
	if i := strings.IndexByte(in, ':'); i > 0 && !strings.ContainsAny(in[:i], "<>=") {
		feat := strings.TrimSpace(strings.ToLower(in[:i]))
		val := strings.TrimSpace(strings.ToLower(in[i+1:]))
		e := &cqExpr{op: 'r', feat: feat}
		switch {
		case strings.HasPrefix(feat, "min-"):
			e.feat, e.cmp1 = feat[4:], ">="
		case strings.HasPrefix(feat, "max-"):
			e.feat, e.cmp1 = feat[4:], "<="
		default:
			e.cmp1 = "="
		}
		e.val1, e.has1 = parseCQVal(val)
		if !e.has1 {
			return &cqExpr{op: 'u'}
		}
		return e
	}
	// boolean feature, e.g. (orientation) - unsupported-> treat true if known
	if !strings.ContainsAny(in, "<>=") {
		return &cqExpr{op: 'u'}
	}
	// range syntax: a op b [op c]
	toks := tokenizeRange(in)
	e := &cqExpr{op: 'r'}
	switch len(toks) {
	case 3:
		if isCQFeature(toks[0]) {
			e.feat, e.cmp1 = toks[0], toks[1]
			e.val1, e.has1 = parseCQVal(toks[2])
		} else if isCQFeature(toks[2]) {
			e.feat, e.cmp1 = toks[2], flipCmp(toks[1])
			e.val1, e.has1 = parseCQVal(toks[0])
		} else {
			return &cqExpr{op: 'u'}
		}
		if !e.has1 {
			return &cqExpr{op: 'u'}
		}
	case 5:
		if !isCQFeature(toks[2]) {
			return &cqExpr{op: 'u'}
		}
		e.feat = toks[2]
		// v1 op1 feat op2 v2   =>  feat flip(op1) v1 and feat op2 v2
		e.cmp1 = flipCmp(toks[1])
		e.val1, e.has1 = parseCQVal(toks[0])
		e.cmp2 = toks[3]
		e.val2, e.has2 = parseCQVal(toks[4])
		if !e.has1 || !e.has2 {
			return &cqExpr{op: 'u'}
		}
	default:
		return &cqExpr{op: 'u'}
	}
	return e
}

func isCQFeature(s string) bool {
	switch s {
	case "width", "height", "inline-size", "block-size", "aspect-ratio", "orientation":
		return true
	}
	return false
}

func flipCmp(c string) string {
	switch c {
	case "<":
		return ">"
	case "<=":
		return ">="
	case ">":
		return "<"
	case ">=":
		return "<="
	}
	return c
}

func tokenizeRange(s string) []string {
	var toks []string
	cur := strings.Builder{}
	flush := func() {
		if t := strings.TrimSpace(cur.String()); t != "" {
			toks = append(toks, strings.ToLower(t))
		}
		cur.Reset()
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '<' || c == '>' || c == '=' {
			flush()
			op := string(c)
			if c != '=' && i+1 < len(s) && s[i+1] == '=' {
				op += "="
				i++
			}
			toks = append(toks, op)
			continue
		}
		cur.WriteByte(c)
	}
	flush()
	return toks
}

func parseCQVal(s string) (cqVal, bool) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "landscape" || s == "portrait" {
		return cqVal{kw: s}, true
	}
	if i := strings.IndexByte(s, '/'); i > 0 {
		a, ok1 := parseNumber(strings.TrimSpace(s[:i]))
		b, ok2 := parseNumber(strings.TrimSpace(s[i+1:]))
		if ok1 && ok2 && b != 0 {
			return cqVal{num: a, num2: b, unit: "/"}, true
		}
		return cqVal{}, false
	}
	v, rest, ok := numPrefix(s)
	if !ok {
		return cqVal{}, false
	}
	if rest == "" {
		return cqVal{num: v, unit: ""}, true
	}
	return cqVal{num: v, unit: rest}, true
}

// ---- evaluation ---------------------------------------------------------------------------

func (v cqVal) px(ci *contInfo, env *Env) (float64, bool) {
	uc := unitCtx{em: ci.fontSize, rem: ci.rootFont, vw: env.Width, vh: env.Height}
	p, pct, ok := uc.unitPx(v.num, v.unit)
	if !ok || pct {
		return 0, false
	}
	return p, true
}

func cmpF(a float64, op string, b float64) bool {
	const eps = 0.001
	switch op {
	case "=":
		return math.Abs(a-b) < eps
	case "<":
		return a < b-eps
	case "<=":
		return a <= b+eps
	case ">":
		return a > b+eps
	case ">=":
		return a >= b-eps
	}
	return false
}

func (e *cqExpr) eval(ci *contInfo, sz ContSize, env *Env) bool {
	switch e.op {
	case '&':
		for _, k := range e.kids {
			if !k.eval(ci, sz, env) {
				return false
			}
		}
		return true
	case '|':
		for _, k := range e.kids {
			if k.eval(ci, sz, env) {
				return true
			}
		}
		return false
	case '!':
		return !e.kids[0].eval(ci, sz, env)
	case 'r':
		w, h := sz.W, sz.H
		if ci.vertical {
			w, h = h, w
		}
		var x float64
		switch e.feat {
		case "width":
			x = w
		case "height":
			x = h
		case "inline-size":
			x = sz.W
			if ci.vertical {
				x = sz.H
			}
		case "block-size":
			x = sz.H
			if ci.vertical {
				x = sz.W
			}
		case "aspect-ratio":
			if h == 0 {
				return false
			}
			x = w / h
		case "orientation":
			ori := "landscape"
			if h > w {
				ori = "portrait"
			}
			return e.val1.kw == ori
		default:
			return false
		}
		eval1 := func(op string, v cqVal) bool {
			if e.feat == "aspect-ratio" {
				if v.unit == "/" {
					return cmpF(x, op, v.num/v.num2)
				}
				return cmpF(x, op, v.num)
			}
			p, ok := v.px(ci, env)
			if !ok {
				return false
			}
			return cmpF(x, op, p)
		}
		if !eval1(e.cmp1, e.val1) {
			return false
		}
		if e.has2 && !eval1(e.cmp2, e.val2) {
			return false
		}
		return true
	}
	return false
}

// ---- engine plumbing ----------------------------------------------------------------------

// SetContainerSizes supplies the content-box sizes of query containers from
// a previous layout pass.
func (e *Engine) SetContainerSizes(m map[*html.Node]ContSize) { e.contSize = m }

// HasContainerQueries reports whether any @container rule was parsed.
func (e *Engine) HasContainerQueries() bool { return e.hasCQ }

// ContainerNodes returns the elements that are query containers.
func (e *Engine) ContainerNodes() []*html.Node {
	var out []*html.Node
	for n := range e.conts {
		out = append(out, n)
	}
	return out
}

func (e *Engine) noteContainer(n *html.Node, c *Computed) {
	if c.ContainerType == "" || c.ContainerType == "normal" {
		return
	}
	if e.conts == nil {
		e.conts = map[*html.Node]*contInfo{}
	}
	e.conts[n] = &contInfo{typ: c.ContainerType, names: c.ContainerName, fontSize: c.FontSize, rootFont: e.RootFont,
		vertical: c.WritingMode == "vertical-rl" || c.WritingMode == "vertical-lr" || c.WritingMode == "sideways-rl" || c.WritingMode == "sideways-lr"}
}

// nearestContainer finds the closest ancestor container usable for a query.
func (e *Engine) nearestContainer(n *html.Node, name string, needBlock bool) (*html.Node, *contInfo) {
	if len(e.conts) == 0 {
		return nil, nil
	}
	for a := n.Parent; a != nil; a = a.Parent {
		ci := e.conts[a]
		if ci == nil {
			continue
		}
		if name != "" {
			found := false
			for _, nm := range ci.names {
				if nm == name {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		if needBlock && ci.typ != "size" {
			continue
		}
		return a, ci
	}
	return nil, nil
}

func (cc *ContCond) needsBlock() bool {
	var f func(x *cqExpr) bool
	f = func(x *cqExpr) bool {
		if x == nil {
			return false
		}
		if x.op == 'r' && (x.feat == "height" || x.feat == "block-size" || x.feat == "aspect-ratio" || x.feat == "orientation") {
			return true
		}
		for _, k := range x.kids {
			if f(k) {
				return true
			}
		}
		return false
	}
	return f(cc.Expr)
}

// contMatches evaluates a rule's container condition for element n (for a
// pseudo-element, n is the originating element and the query may also
// target n itself).
func (e *Engine) contMatches(n *html.Node, cc *ContCond, pseudo string) bool {
	e.cqEvals++
	for ; cc != nil; cc = cc.Outer {
		start := n
		if pseudo != "" {
			// pseudo-elements query containers from the originating element up
			if ci := e.conts[n]; ci != nil && (cc.Name == "" || hasName(ci.names, cc.Name)) && (!cc.needsBlock() || ci.typ == "size") {
				sz := e.contSize[n]
				if !cc.Expr.eval(ci, sz, e.env) {
					return false
				}
				continue
			}
		}
		an, ci := e.nearestContainer(start, cc.Name, cc.needsBlock())
		if an == nil {
			return false
		}
		sz := e.contSize[an]
		if !cc.Expr.eval(ci, sz, e.env) {
			return false
		}
	}
	return true
}

func hasName(names []string, n string) bool {
	for _, x := range names {
		if x == n {
			return true
		}
	}
	return false
}
