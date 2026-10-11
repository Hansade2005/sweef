package engine

// JS runtime abstraction.
//
// Sweef embeds QuickJS (cgo build tag `quickjs`) for real ES2020 execution —
// ~1.5 MiB idle heap vs V8's ~8+ MiB, and it compiles to static binaries that
// run on 1-core ARM boxes. When built without cgo (pure-Go fallback used in CI
// and ultra-tiny targets), we ship a small expression evaluator sufficient for
// document.title / querySelector reads so tooling still functions.
//
// The API is deliberately tiny: one context per tab, synchronous eval, JSON
// in/out. That keeps the bridge surface auditable (~200 LoC) which matters
// when an AI agent is going to be running arbitrary page scripts.

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

type jsRuntime struct {
	disabled bool
}

var globalRuntime = &jsRuntime{}

func runtimeInit(opts *Options) {
	globalRuntime.disabled = opts.DisableJS
	initCgoRuntime(opts) // no-op in pure-Go builds
}

func shutdownRuntime() { shutdownCgoRuntime() }

func runtimeCreateContext(tabID string) any { return createCgoContext(tabID) }
func runtimeDestroyContext(ctx any)         { destroyCgoContext(ctx) }

// runtimeEval returns (jsonResult, consoleErrors). In the cgo build this calls
// into QuickJS with DOM-bridge globals installed; here we provide the fallback.
func runtimeEval(ctx any, src, name string) (string, []string) {
	if globalRuntime.disabled || strings.TrimSpace(src) == "" {
		return "", nil
	}
	if cgoAvailable() {
		return cgoEval(ctx, src, name)
	}
	return fallbackEval(src)
}

// currentTab is resolved by the DOM bridge so the fallback evaluator can
// answer common document.* expressions without QuickJS linked in.
var currentTab func() *Tab

// SetEvalTabHook wires the fallback evaluator to a tab's live DOM. The RPC/MCP
// layer calls this around every evaluate so pure-Go builds still return real
// values for the expressions agents use most.
func SetEvalTabHook(f func() *Tab) { currentTab = f }

// fallbackEval implements a minimal read-only interpreter for the handful of
// expressions agents use most: JSON literals, document.title/URL, selector
// reads, and small string helpers. Anything unhandled reports as unsupported
// rather than silently returning wrong data — safety over cleverness.
func fallbackEval(src string) (string, []string) {
	s := strings.TrimSpace(src)
	// trailing semicolons are common from injected code
	s = strings.TrimRight(s, ";")
	s = strings.TrimSpace(s)

	t := ""
	if currentTab != nil {
		if tab := currentTab(); tab != nil {
			t = tab.Title()
		}
	}

	switch {
	case s == "":
		return "", nil
	case strings.HasPrefix(s, "console.log(") || strings.HasPrefix(s, "console.info(") ||
		strings.HasPrefix(s, "console.warn(") || strings.HasPrefix(s, "console.error(") ||
		strings.HasPrefix(s, "console.debug("):
		level := "log"
		switch {
		case strings.HasPrefix(s, "console.warn("):
			level = "warning"
		case strings.HasPrefix(s, "console.error("):
			level = "error"
		}
		msg := evalExprToString(indexParen(s))
		if currentTab != nil {
			if tab := currentTab(); tab != nil {
				tab.LogConsole(level, msg)
			}
		}
		return "null", nil
	case looksLikeExpr(s):
		if v, ok := evalNumber(s); ok {
			b, _ := json.Marshal(v)
			return string(b), nil
		}
		return jsonOr(evalExprToString(s)), nil
	case s == "document.title" || s == "window.document.title":
		return jsonOr(t), nil
	case s == "location.href" || s == "document.URL" || s == "window.location.href":
		u := ""
		if currentTab != nil {
			if tab := currentTab(); tab != nil {
				u = tab.URL()
			}
		}
		return jsonOr(u), nil
	case strings.HasPrefix(s, "document.querySelector(") && strings.HasSuffix(s, ")"):
		arg := extractStringArg(s, "document.querySelector(")
		if arg == "" {
			return "null", nil
		}
		if currentTab != nil {
			if tab := currentTab(); tab != nil {
				if ni, err := tab.QuerySelector(arg); err == nil {
					return jsonOr(ni.Text), nil
				}
			}
		}
		return "null", nil
	case strings.HasPrefix(s, "document.querySelectorAll(") && strings.HasSuffix(s, ")"):
		arg := extractStringArg(s, "document.querySelectorAll(")
		out := []string{}
		if arg != "" && currentTab != nil {
			if tab := currentTab(); tab != nil {
				if list, err := tab.QuerySelectorAll(arg); err == nil {
					for _, ni := range list {
						out = append(out, ni.Text)
					}
				}
			}
		}
		b, _ := json.Marshal(out)
		return string(b), nil
	case strings.HasPrefix(s, "JSON.parse("):
		var v any
		inner := strings.TrimSuffix(strings.TrimPrefix(s, "JSON.parse("), ")")
		if err := json.Unmarshal([]byte(inner), &v); err != nil {
			return "", []string{"SyntaxError: " + err.Error()}
		}
		b, _ := json.Marshal(v)
		return string(b), nil
	case s == "navigator.userAgent":
		ua := ""
		if currentTab != nil {
			if tab := currentTab(); tab != nil {
				ua = tab.UserAgent()
			}
		}
		return jsonOr(ua), nil
	default:
		return "null", []string{"Sweef: expression needs full JS; rebuild with -tags quickjs (read-only subset handled natively)"}
	}
}

// extractStringArg pulls the first quoted string argument out of a call like
// document.querySelector("div > p").
func extractStringArg(s, prefix string) string {
	rest := strings.TrimSuffix(strings.TrimPrefix(s, prefix), ")")
	rest = strings.TrimSpace(rest)
	if len(rest) >= 2 {
		q := rest[0]
		if q == '"' || q == '\'' {
			if end := strings.IndexByte(rest[1:], q); end >= 0 {
				return rest[1 : 1+end]
			}
		}
	}
	return ""
}

func jsonOr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// indexParen returns s starting at the first '(' (for "console.log(...)").
func indexParen(s string) string {
	if i := strings.IndexByte(s, '('); i >= 0 {
		return s[i+1:]
	}
	return s
}

// looksLikeExpr reports whether src is a small literal/numeric/string
// expression we can evaluate natively (e.g. "1+2", "'a'+'b'", "3*(4-2)").
func looksLikeExpr(s string) bool {
	if s == "" {
		return false
	}
	// must not be a known DOM call or identifier expression
	if strings.ContainsAny(s, "[]{};") || strings.HasPrefix(s, "document.") ||
		strings.HasPrefix(s, "window.") || strings.HasPrefix(s, "location.") ||
		strings.HasPrefix(s, "navigator.") || strings.HasPrefix(s, "JSON.") {
		return false
	}
	ok := true
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c == '.' || c == '+' || c == '-' || c == '*' ||
			c == '/' || c == '%' || c == '(' || c == ')' || c == ' ' || c == '\'' ||
			c == '"' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			ok = false
			break
		}
	}
	if !ok {
		return false
	}
	// numeric if it has operators and only digits/parens/spaces/operators
	hasOp := strings.ContainsAny(s, "+-*/%")
	onlyNum := true
	for _, c := range s {
		if !((c >= '0' && c <= '9') || c == '.' || c == ' ' || c == '(' || c == ')' ||
			c == '+' || c == '-' || c == '*' || c == '/' || c == '%') {
			onlyNum = false
			break
		}
	}
	if hasOp && onlyNum {
		return true
	}
	// string concat of quoted literals
	return hasOp && (strings.ContainsRune(s, '\'') || strings.ContainsRune(s, '"'))
}

// evalNumber evaluates + - * / % over float64 with parentheses via recursive
// descent. Returns ok=false when parsing fails.
func evalNumber(s string) (float64, bool) {
	p := &numParser{src: []rune(strings.ReplaceAll(s, " ", ""))}
	v, ok := p.expr()
	if !ok || p.pos != len(p.src) {
		return 0, false
	}
	return v, true
}

type numParser struct {
	src []rune
	pos int
}

func (p *numParser) expr() (float64, bool) { // + -
	v, ok := p.term()
	if !ok {
		return 0, false
	}
	for p.pos < len(p.src) && (p.src[p.pos] == '+' || p.src[p.pos] == '-') {
		op := p.src[p.pos]
		p.pos++
		r, ok := p.term()
		if !ok {
			return 0, false
		}
		if op == '+' {
			v += r
		} else {
			v -= r
		}
	}
	return v, ok
}

func (p *numParser) term() (float64, bool) { // * / %
	v, ok := p.unary()
	if !ok {
		return 0, false
	}
	for p.pos < len(p.src) && (p.src[p.pos] == '*' || p.src[p.pos] == '/' || p.src[p.pos] == '%') {
		op := p.src[p.pos]
		p.pos++
		r, ok := p.unary()
		if !ok {
			return 0, false
		}
		switch op {
		case '*':
			v *= r
		case '/':
			if r == 0 {
				return 0, false
			}
			v /= r
		case '%':
			if r == 0 {
				return 0, false
			}
			v = math.Mod(v, r)
		}
	}
	return v, ok
}

func (p *numParser) unary() (float64, bool) {
	if p.pos < len(p.src) && p.src[p.pos] == '-' {
		p.pos++
		v, ok := p.unary()
		return -v, ok
	}
	if p.pos < len(p.src) && p.src[p.pos] == '+' {
		p.pos++
		return p.unary()
	}
	if p.pos < len(p.src) && p.src[p.pos] == '(' {
		p.pos++
		v, ok := p.expr()
		if !ok || p.pos >= len(p.src) || p.src[p.pos] != ')' {
			return 0, false
		}
		p.pos++
		return v, ok
	}
	start := p.pos
	for p.pos < len(p.src) && (p.src[p.pos] >= '0' && p.src[p.pos] <= '9' || p.src[p.pos] == '.') {
		p.pos++
	}
	if start == p.pos {
		return 0, false
	}
	f, err := strconv.ParseFloat(string(p.src[start:p.pos]), 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

// splitTop splits an argument list on commas that are not inside quotes/parens.
func splitTop(s string) []string {
	var parts []string
	depth := 0
	var quote byte
	cur := strings.Builder{}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			cur.WriteByte(c)
			if c == quote {
				quote = 0
			}
			continue
		case c == '\'' || c == '"':
			quote = c
		case c == '(' || c == '[':
			depth++
		case c == ')' || c == ']':
			depth--
		case c == ',' && depth == 0:
			parts = append(parts, strings.TrimSpace(cur.String()))
			cur.Reset()
			continue
		}
		cur.WriteByte(c)
	}
	parts = append(parts, strings.TrimSpace(cur.String()))
	return parts
}

// evalExprToString renders a console.log-style argument list to text,
// evaluating numbers and stripping quotes from string literals.
func evalExprToString(args string) string {
	parts := splitTop(args)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if v, ok := evalNumber(p); ok {
			out = append(out, formatNum(v))
			continue
		}
		if len(p) >= 2 && (p[0] == '\'' || p[0] == '"') && p[len(p)-1] == p[0] {
			out = append(out, p[1:len(p)-1])
			continue
		}
		if currentTab != nil {
			if tab := currentTab(); tab != nil {
				switch p {
				case "document.title":
					out = append(out, tab.Title())
					continue
				case "location.href", "document.URL":
					out = append(out, tab.URL())
					continue
				}
			}
		}
		out = append(out, p)
	}
	return strings.Join(out, " ")
}

func formatNum(f float64) string {
	if f == math.Trunc(f) && math.Abs(f) < 1e15 {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}
