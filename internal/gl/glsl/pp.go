package glsl

import (
	"fmt"
	"strconv"
	"strings"
)

type macro struct {
	name   string
	fn     bool
	params []string
	body   []Tok
	line   bool // __LINE__
}

type ppCond struct {
	active   bool // this branch currently emitting
	taken    bool // some branch already taken
	parentOn bool
	seenElse bool
}

type ppState struct {
	macros  map[string]*macro
	out     []Tok
	errs    []string
	version int
	exts    []ExtDirective
	conds   []ppCond
}

// ExtDirective records an #extension line.
type ExtDirective struct {
	Name, Behavior string
	Line           int
}

func (p *ppState) errorf(line int, tok, f string, a ...any) {
	p.errs = append(p.errs, fmt.Sprintf("ERROR: 0:%d: '%s' : %s", line, tok, fmt.Sprintf(f, a...)))
}

func (p *ppState) active() bool {
	return len(p.conds) == 0 || p.conds[len(p.conds)-1].active
}

func identTok(s string, line int) Tok { return Tok{K: TIdent, S: s, Line: line} }

func preprocess(toks []Tok, predefined map[string]string, supported map[string]bool) ([]Tok, *ppState) {
	p := &ppState{macros: map[string]*macro{}, version: 100}
	for k, v := range predefined {
		sub, _ := lex(v)
		if len(sub) > 0 {
			sub = sub[:len(sub)-1]
		}
		p.macros[k] = &macro{name: k, body: sub}
	}
	p.macros["__LINE__"] = &macro{name: "__LINE__", line: true}
	p.macros["__FILE__"] = &macro{name: "__FILE__", body: []Tok{{K: TInt, S: "0"}}}
	i := 0
	first := true
	for i < len(toks) && toks[i].K != TEOF {
		t := toks[i]
		if t.BOL && t.K == TPunct && t.S == "#" {
			j := i + 1
			for j < len(toks) && !toks[j].BOL && toks[j].K != TEOF {
				j++
			}
			line := toks[i : j]
			p.directive(line, first, supported)
			first = false
			i = j
			continue
		}
		first = false
		if !p.active() {
			i++
			continue
		}
		if t.K == TIdent {
			if m := p.macros[t.S]; m != nil && !t.hide[t.S] {
				repl, end, ok := p.expandAt(toks, i, m)
				if ok {
					nt := make([]Tok, 0, len(toks)-i+len(repl))
					nt = append(nt, repl...)
					nt = append(nt, toks[end:]...)
					toks = append(toks[:i:i], nt...)
					continue
				}
			}
		}
		p.out = append(p.out, t)
		i++
	}
	if len(p.conds) > 0 {
		p.errorf(toks[len(toks)-1].Line, "#if", "unexpected end of file found in conditional block")
	}
	line := 1
	if len(toks) > 0 {
		line = toks[len(toks)-1].Line
	}
	p.out = append(p.out, Tok{K: TEOF, Line: line})
	return p.out, p
}

// expandAt expands the macro invocation at toks[i]; ok=false when a
// function-like macro name is not followed by '('.
func (p *ppState) expandAt(toks []Tok, i int, m *macro) (repl []Tok, end int, ok bool) {
	t := toks[i]
	if m.line {
		return []Tok{{K: TInt, S: strconv.Itoa(t.Line), Line: t.Line, Space: t.Space}}, i + 1, true
	}
	hide := map[string]bool{m.name: true}
	for k := range t.hide {
		hide[k] = true
	}
	if !m.fn {
		out := make([]Tok, len(m.body))
		for k, b := range m.body {
			b.Line, b.BOL = t.Line, false
			b.hide = unionHide(b.hide, hide)
			out[k] = b
		}
		if len(out) > 0 {
			out[0].Space = t.Space
		}
		return out, i + 1, true
	}
	if i+1 >= len(toks) || toks[i+1].K != TPunct || toks[i+1].S != "(" {
		return nil, 0, false
	}
	// collect arguments
	var args [][]Tok
	depth := 0
	j := i + 2
	cur := []Tok{}
	closed := false
	for ; j < len(toks) && toks[j].K != TEOF; j++ {
		x := toks[j]
		if x.K == TPunct {
			if x.S == "(" {
				depth++
			} else if x.S == ")" {
				if depth == 0 {
					closed = true
					j++
					break
				}
				depth--
			} else if x.S == "," && depth == 0 {
				args = append(args, cur)
				cur = []Tok{}
				continue
			}
		}
		x.BOL = false
		cur = append(cur, x)
	}
	if !closed {
		p.errorf(t.Line, m.name, "unterminated macro invocation")
		return nil, i + 1, false
	}
	args = append(args, cur)
	if len(m.params) == 0 && len(args) == 1 && len(args[0]) == 0 {
		args = nil
	}
	if len(args) != len(m.params) {
		p.errorf(t.Line, m.name, "wrong number of macro arguments")
		return []Tok{}, j, true
	}
	exp := make([][]Tok, len(args))
	for k, a := range args {
		exp[k] = p.expandList(a)
	}
	var out []Tok
	for k := 0; k < len(m.body); k++ {
		b := m.body[k]
		if b.K == TIdent {
			if pi := indexOf(m.params, b.S); pi >= 0 {
				// token pasting with neighbours uses raw args
				raw := k+1 < len(m.body) && m.body[k+1].S == "##" || k > 0 && m.body[k-1].S == "##"
				src := exp[pi]
				if raw {
					src = args[pi]
				}
				for _, a := range src {
					a.Line = t.Line
					out = append(out, a)
				}
				continue
			}
		}
		b.Line, b.BOL = t.Line, false
		out = append(out, b)
	}
	// ## pasting
	for k := 0; k < len(out); k++ {
		if out[k].K == TPunct && out[k].S == "##" && k > 0 && k+1 < len(out) {
			s := out[k-1].S + out[k+1].S
			nt, err := lex(s)
			if err == nil && len(nt) >= 2 {
				v := nt[0]
				v.Line = t.Line
				out = append(append(out[:k-1:k-1], v), out[k+2:]...)
				k--
			}
		}
	}
	for k := range out {
		out[k].hide = unionHide(out[k].hide, hide)
	}
	if len(out) > 0 {
		out[0].Space = t.Space
	}
	return out, j, true
}

func unionHide(a, b map[string]bool) map[string]bool {
	if len(a) == 0 {
		return b
	}
	if len(b) == 0 {
		return a
	}
	r := make(map[string]bool, len(a)+len(b))
	for k := range a {
		r[k] = true
	}
	for k := range b {
		r[k] = true
	}
	return r
}

func indexOf(s []string, x string) int {
	for i, v := range s {
		if v == x {
			return i
		}
	}
	return -1
}

// expandList fully macro-expands a token list (no directives).
func (p *ppState) expandList(toks []Tok) []Tok {
	toks = append([]Tok(nil), toks...)
	var out []Tok
	guard := 0
	for i := 0; i < len(toks); {
		t := toks[i]
		if t.K == TIdent {
			if m := p.macros[t.S]; m != nil && !t.hide[t.S] && guard < 100000 {
				repl, end, ok := p.expandAt(toks, i, m)
				if ok {
					guard++
					nt := append([]Tok{}, repl...)
					nt = append(nt, toks[end:]...)
					toks = nt
					i = 0
					continue
				}
			}
		}
		out = append(out, t)
		toks = toks[i+1:]
		i = 0
	}
	return out
}

func (p *ppState) directive(line []Tok, first bool, supported map[string]bool) {
	if len(line) < 2 {
		return // null directive
	}
	d := line[1]
	rest := line[2:]
	ln := d.Line
	if d.K != TIdent {
		p.errorf(ln, d.S, "invalid directive")
		return
	}
	// conditionals are processed even when inactive
	switch d.S {
	case "if", "ifdef", "ifndef":
		parentOn := p.active()
		on := false
		if parentOn {
			switch d.S {
			case "ifdef", "ifndef":
				if len(rest) == 0 {
					p.errorf(ln, d.S, "no macro name given in #%s directive", d.S)
				} else {
					_, def := p.macros[rest[0].S]
					on = def == (d.S == "ifdef")
				}
			default:
				on = p.evalIf(rest, ln) != 0
			}
		}
		p.conds = append(p.conds, ppCond{active: parentOn && on, taken: on, parentOn: parentOn})
		return
	case "elif":
		if len(p.conds) == 0 {
			p.errorf(ln, "#elif", "unexpected #elif")
			return
		}
		c := &p.conds[len(p.conds)-1]
		if c.seenElse {
			p.errorf(ln, "#elif", "unexpected #elif after #else")
		}
		if c.taken || !c.parentOn {
			c.active = false
		} else {
			on := p.evalIf(rest, ln) != 0
			c.active, c.taken = on, on
		}
		return
	case "else":
		if len(p.conds) == 0 {
			p.errorf(ln, "#else", "unexpected #else")
			return
		}
		c := &p.conds[len(p.conds)-1]
		if c.seenElse {
			p.errorf(ln, "#else", "unexpected #else")
		}
		c.seenElse = true
		c.active = c.parentOn && !c.taken
		c.taken = true
		return
	case "endif":
		if len(p.conds) == 0 {
			p.errorf(ln, "#endif", "unexpected #endif")
			return
		}
		p.conds = p.conds[:len(p.conds)-1]
		return
	}
	if !p.active() {
		return
	}
	switch d.S {
	case "version":
		if !first {
			p.errorf(ln, "#version", "#version directive must occur before anything else, except for comments and white space")
			return
		}
		if len(rest) == 0 {
			p.errorf(ln, "#version", "version number missing")
			return
		}
		switch rest[0].S {
		case "100":
			p.version = 100
		case "300":
			if len(rest) < 2 || rest[1].S != "es" {
				p.errorf(ln, "#version", "invalid version directive")
			}
			p.version = 300
		default:
			p.errorf(ln, "#version", "version number not supported")
		}
		p.macros["__VERSION__"] = &macro{name: "__VERSION__", body: []Tok{{K: TInt, S: strconv.Itoa(p.version)}}}
	case "define":
		if len(rest) == 0 || rest[0].K != TIdent {
			p.errorf(ln, "#define", "macro name expected")
			return
		}
		m := &macro{name: rest[0].S}
		body := rest[1:]
		if len(body) > 0 && body[0].K == TPunct && body[0].S == "(" && !body[0].Space {
			m.fn = true
			k := 1
			for k < len(body) && body[k].S != ")" {
				if body[k].K == TIdent {
					m.params = append(m.params, body[k].S)
				}
				k++
			}
			body = body[min(k+1, len(body)):]
		}
		if strings.HasPrefix(m.name, "GL_") || strings.Contains(m.name, "__") {
			p.errorf(ln, m.name, "names containing consecutive underscores or starting with GL_ are reserved")
			return
		}
		m.body = body
		p.macros[m.name] = m
	case "undef":
		if len(rest) > 0 {
			delete(p.macros, rest[0].S)
		}
	case "extension":
		// #extension name : behavior
		if len(rest) < 3 || rest[1].S != ":" {
			p.errorf(ln, "#extension", "invalid extension directive")
			return
		}
		name, beh := rest[0].S, rest[2].S
		p.exts = append(p.exts, ExtDirective{name, beh, ln})
		if name != "all" && !supported[name] {
			if beh == "require" {
				p.errorf(ln, "#extension", "extension '%s' is not supported", name)
			}
		}
	case "pragma", "line":
	case "error":
		var sb strings.Builder
		for _, t := range rest {
			sb.WriteString(t.S + " ")
		}
		p.errorf(ln, "#error", "%s", strings.TrimSpace(sb.String()))
	default:
		p.errorf(ln, d.S, "invalid directive name")
	}
}

// evalIf evaluates a preprocessor #if expression.
func (p *ppState) evalIf(toks []Tok, line int) int64 {
	// resolve defined()
	var pre []Tok
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if t.K == TIdent && t.S == "defined" {
			j := i + 1
			paren := j < len(toks) && toks[j].S == "("
			if paren {
				j++
			}
			if j < len(toks) {
				_, ok := p.macros[toks[j].S]
				v := "0"
				if ok {
					v = "1"
				}
				pre = append(pre, Tok{K: TInt, S: v, Line: line})
				i = j
				if paren && i+1 < len(toks) && toks[i+1].S == ")" {
					i++
				}
				continue
			}
		}
		pre = append(pre, t)
	}
	pre = p.expandList(pre)
	pe := &ppExpr{toks: pre, p: p, line: line}
	v := pe.ternary()
	if pe.i < len(pe.toks) {
		p.errorf(line, "#if", "invalid expression")
	}
	return v
}

type ppExpr struct {
	toks []Tok
	i    int
	p    *ppState
	line int
}

func (e *ppExpr) peek() string {
	if e.i < len(e.toks) {
		return e.toks[e.i].S
	}
	return ""
}

func (e *ppExpr) ternary() int64 {
	c := e.binary(0)
	if e.peek() == "?" {
		e.i++
		a := e.ternary()
		if e.peek() == ":" {
			e.i++
		}
		b := e.ternary()
		if c != 0 {
			return a
		}
		return b
	}
	return c
}

var ppPrec = map[string]int{"||": 1, "&&": 2, "|": 3, "^": 4, "&": 5, "==": 6, "!=": 6, "<": 7, ">": 7, "<=": 7, ">=": 7, "<<": 8, ">>": 8, "+": 9, "-": 9, "*": 10, "/": 10, "%": 10}

func (e *ppExpr) binary(min int) int64 {
	l := e.unary()
	for {
		op := e.peek()
		pr, ok := ppPrec[op]
		if !ok || pr <= min {
			return l
		}
		e.i++
		r := e.binary(pr)
		b := func(x bool) int64 {
			if x {
				return 1
			}
			return 0
		}
		switch op {
		case "||":
			l = b(l != 0 || r != 0)
		case "&&":
			l = b(l != 0 && r != 0)
		case "|":
			l |= r
		case "^":
			l ^= r
		case "&":
			l &= r
		case "==":
			l = b(l == r)
		case "!=":
			l = b(l != r)
		case "<":
			l = b(l < r)
		case ">":
			l = b(l > r)
		case "<=":
			l = b(l <= r)
		case ">=":
			l = b(l >= r)
		case "<<":
			l <<= uint(r & 63)
		case ">>":
			l >>= uint(r & 63)
		case "+":
			l += r
		case "-":
			l -= r
		case "*":
			l *= r
		case "/":
			if r == 0 {
				e.p.errorf(e.line, "/", "division by zero in preprocessor expression")
			} else {
				l /= r
			}
		case "%":
			if r == 0 {
				e.p.errorf(e.line, "%%", "division by zero in preprocessor expression")
			} else {
				l %= r
			}
		}
	}
}

func (e *ppExpr) unary() int64 {
	if e.i >= len(e.toks) {
		e.p.errorf(e.line, "#if", "invalid expression")
		return 0
	}
	t := e.toks[e.i]
	e.i++
	switch {
	case t.K == TPunct && t.S == "(":
		v := e.ternary()
		if e.peek() == ")" {
			e.i++
		}
		return v
	case t.K == TPunct && t.S == "!":
		if e.unary() == 0 {
			return 1
		}
		return 0
	case t.K == TPunct && t.S == "-":
		return -e.unary()
	case t.K == TPunct && t.S == "+":
		return e.unary()
	case t.K == TPunct && t.S == "~":
		return ^e.unary()
	case t.K == TInt || t.K == TUint:
		v, err := strconv.ParseInt(t.S, 0, 64)
		if err != nil {
			u, err2 := strconv.ParseUint(t.S, 0, 64)
			if err2 != nil {
				e.p.errorf(e.line, t.S, "invalid integer")
			}
			return int64(u)
		}
		return v
	case t.K == TIdent:
		return 0
	}
	e.p.errorf(e.line, t.S, "invalid expression")
	return 0
}
