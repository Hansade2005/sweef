// Package style implements the CSS side of Sweef's renderer: parsing of
// stylesheets, selector matching (via internal/net), the cascade, inheritance
// and computed values for the properties that affect layout and paint, plus a
// Chromium-faithful user-agent stylesheet.
package style

import (
	"math"
	"strconv"
	"strings"
)

// Color is non-premultiplied 8-bit RGBA.
type Color struct{ R, G, B, A uint8 }

// LenKind says what a Length holds.
type LenKind uint8

// Length kinds. The zero value is "auto".
const (
	LAuto LenKind = iota
	LPx
	LPct
	LCalc
	LMinContent
	LMaxContent
	LFitContent // fit-content or fit-content(V px)
	LNone
	LNumber // unitless number (line-height multiplier, flex-grow-like uses)
)

// Length is a CSS <length-percentage> or keyword. Absolute units are resolved
// to px at computed-value time; percentages are kept for layout.
type Length struct {
	K LenKind
	V float64
	C *Calc
}

// Px builds an absolute length.
func Px(v float64) Length { return Length{K: LPx, V: v} }

// Pct builds a percentage length.
func Pct(v float64) Length { return Length{K: LPct, V: v} }

// IsAuto reports the auto keyword (also the zero value).
func (l Length) IsAuto() bool { return l.K == LAuto }

// IsNone reports none / unset max sizes.
func (l Length) IsNone() bool { return l.K == LNone || l.K == LAuto }

// HasPct reports whether resolving needs a percentage base.
func (l Length) HasPct() bool {
	switch l.K {
	case LPct:
		return true
	case LCalc:
		return l.C.hasPct()
	}
	return false
}

// IsIntrinsic reports min-content / max-content / fit-content.
func (l Length) IsIntrinsic() bool {
	return l.K == LMinContent || l.K == LMaxContent || l.K == LFitContent
}

// Resolve returns px for px / percentage / calc lengths given the percentage
// base. Keywords resolve to 0.
func (l Length) Resolve(base float64) float64 {
	switch l.K {
	case LPx, LNumber:
		return l.V
	case LPct:
		return l.V * base / 100
	case LCalc:
		return l.C.eval(base)
	}
	return 0
}

// Calc is a calc()/min()/max()/clamp() expression tree.
type Calc struct {
	Op     byte // 0 leaf length, 'n' number, '+', '-', '*', '/', 'm' min, 'M' max, 'c' clamp
	Px     float64
	Pct    float64
	A, B   *Calc
	List   []*Calc
	isNumb bool
	Fn     string // Op 'F': math function name
	Strat  string // round() strategy
}

func (c *Calc) hasPct() bool {
	if c == nil {
		return false
	}
	if c.Pct != 0 {
		return true
	}
	if c.A.hasPct() || c.B.hasPct() {
		return true
	}
	for _, x := range c.List {
		if x.hasPct() {
			return true
		}
	}
	return false
}

func (c *Calc) eval(base float64) float64 {
	switch c.Op {
	case 0, 'n':
		return c.Px + c.Pct*base/100
	case '+':
		return c.A.eval(base) + c.B.eval(base)
	case '-':
		return c.A.eval(base) - c.B.eval(base)
	case '*':
		return c.A.eval(base) * c.B.eval(base)
	case '/':
		d := c.B.eval(base)
		if d == 0 {
			return 0
		}
		return c.A.eval(base) / d
	case 'm':
		v := math.Inf(1)
		for _, x := range c.List {
			v = math.Min(v, x.eval(base))
		}
		return v
	case 'M':
		v := math.Inf(-1)
		for _, x := range c.List {
			v = math.Max(v, x.eval(base))
		}
		return v
	case 'c':
		lo, v, hi := c.List[0].eval(base), c.List[1].eval(base), c.List[2].eval(base)
		return math.Max(lo, math.Min(v, hi))
	case 'F':
		return c.evalFn(base)
	}
	return 0
}

func (c *Calc) evalFn(base float64) float64 {
	a := make([]float64, len(c.List))
	for i, x := range c.List {
		a[i] = x.eval(base)
	}
	arg := func(i int) float64 {
		if i < len(a) {
			return a[i]
		}
		return 0
	}
	switch c.Fn {
	case "sin":
		return math.Sin(arg(0))
	case "cos":
		return math.Cos(arg(0))
	case "tan":
		return math.Tan(arg(0))
	case "asin":
		return math.Asin(arg(0))
	case "acos":
		return math.Acos(arg(0))
	case "atan":
		return math.Atan(arg(0))
	case "atan2":
		return math.Atan2(arg(0), arg(1))
	case "sqrt":
		return math.Sqrt(arg(0))
	case "exp":
		return math.Exp(arg(0))
	case "log":
		if len(a) > 1 {
			return math.Log(arg(0)) / math.Log(arg(1))
		}
		return math.Log(arg(0))
	case "pow":
		return math.Pow(arg(0), arg(1))
	case "hypot":
		s := 0.0
		for _, v := range a {
			s += v * v
		}
		return math.Sqrt(s)
	case "abs":
		return math.Abs(arg(0))
	case "sign":
		v := arg(0)
		if v > 0 {
			return 1
		} else if v < 0 {
			return -1
		}
		return 0
	case "mod":
		d := arg(1)
		if d == 0 {
			return 0
		}
		r := math.Mod(arg(0), d)
		if r != 0 && (r < 0) != (d < 0) {
			r += d
		}
		return r
	case "rem":
		if arg(1) == 0 {
			return 0
		}
		return math.Mod(arg(0), arg(1))
	case "round":
		step := 1.0
		if len(a) > 1 {
			step = a[1]
		}
		if step == 0 {
			return 0
		}
		step = math.Abs(step)
		q := arg(0) / step
		switch c.Strat {
		case "up":
			q = math.Ceil(q)
		case "down":
			q = math.Floor(q)
		case "to-zero":
			q = math.Trunc(q)
		default:
			q = math.Floor(q + 0.5)
		}
		return q * step
	}
	return 0
}

// ---- tokenizing helpers ---------------------------------------------------------

// splitTop splits s on sep at nesting depth zero (outside parens, brackets
// and strings). Empty trailing parts are kept only when keepEmpty.
func splitTop(s string, sep byte) []string {
	var out []string
	depth := 0
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			if depth > 0 {
				depth--
			}
		case '"', '\'':
			q := s[i]
			i++
			for i < len(s) && s[i] != q {
				if s[i] == '\\' {
					i++
				}
				i++
			}
		case '\\':
			i++
		default:
			if s[i] == sep && depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	out = append(out, s[start:])
	return out
}

// fields splits on whitespace at depth zero, keeping functions and strings whole.
func fields(s string) []string {
	var out []string
	depth := 0
	start := -1
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f':
			if depth == 0 && start >= 0 {
				out = append(out, s[start:i])
				start = -1
			}
			continue
		case c == '(' || c == '[':
			depth++
		case c == ')' || c == ']':
			if depth > 0 {
				depth--
			}
		case c == '"' || c == '\'':
			if start < 0 {
				start = i
			}
			q := c
			i++
			for i < len(s) && s[i] != q {
				if s[i] == '\\' {
					i++
				}
				i++
			}
			continue
		case c == '/' && depth == 0:
			if start >= 0 {
				out = append(out, s[start:i])
				start = -1
			}
			out = append(out, "/")
			continue
		}
		if start < 0 {
			start = i
		}
	}
	if start >= 0 {
		out = append(out, s[start:])
	}
	return out
}

func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return unescapeStr(s[1 : len(s)-1])
	}
	return s
}

func unescapeStr(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		if s[i] == '\n' {
			continue
		}
		j := i
		for j < len(s) && j < i+6 && isHex(s[j]) {
			j++
		}
		if j > i {
			n, _ := strconv.ParseInt(s[i:j], 16, 32)
			if n == 0 {
				n = 0xFFFD
			}
			b.WriteRune(rune(n))
			if j < len(s) && s[j] == ' ' {
				j++
			}
			i = j - 1
		} else {
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

// funcArgs returns the name and inner text when s looks like name(...).
func funcArgs(s string) (name, args string, ok bool) {
	s = strings.TrimSpace(s)
	i := strings.IndexByte(s, '(')
	if i <= 0 || s[len(s)-1] != ')' {
		return "", "", false
	}
	// make sure the closing paren matches the first open paren
	depth := 0
	for j := i; j < len(s); j++ {
		switch s[j] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 && j != len(s)-1 {
				return "", "", false
			}
		}
	}
	return strings.ToLower(s[:i]), s[i+1 : len(s)-1], true
}

// ---- numbers and dimensions ------------------------------------------------------

// number parses a CSS number prefix of s and returns the value and the rest.
func numPrefix(s string) (float64, string, bool) {
	i := 0
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		i++
	}
	d := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
		d++
	}
	if i < len(s) && s[i] == '.' {
		i++
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
			d++
		}
	}
	if d == 0 {
		return 0, s, false
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		j := i + 1
		if j < len(s) && (s[j] == '+' || s[j] == '-') {
			j++
		}
		k := j
		for k < len(s) && s[k] >= '0' && s[k] <= '9' {
			k++
		}
		if k > j && (k == len(s) || !isIdentStart(s[k])) {
			i = k
		}
	}
	v, err := strconv.ParseFloat(s[:i], 64)
	if err != nil {
		return 0, s, false
	}
	return v, s[i:], true
}

func isIdentStart(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' || c == '%'
}

func parseNumber(s string) (float64, bool) {
	v, rest, ok := numPrefix(strings.TrimSpace(s))
	if !ok || rest != "" {
		return 0, false
	}
	return v, true
}

func parseInt(s string) (int, bool) {
	v, ok := parseNumber(s)
	if !ok {
		return 0, false
	}
	return int(v), true
}

// parseAngle returns degrees.
func parseAngle(s string) (float64, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	v, rest, ok := numPrefix(s)
	if !ok {
		return 0, false
	}
	switch rest {
	case "deg":
		return v, true
	case "rad":
		return v * 180 / math.Pi, true
	case "grad":
		return v * 0.9, true
	case "turn":
		return v * 360, true
	case "":
		if v == 0 {
			return 0, true
		}
	}
	return 0, false
}

// unitCtx holds what unit resolution needs.
type unitCtx struct {
	em, rem         float64 // font sizes
	vw, vh          float64 // viewport px
	exH, chW        float64 // x-height and "0" advance of the element font
	lh              float64 // computed line-height px (for the lh unit)
	capH            float64
	dvh, svh, lvh   float64
	containerW, cqH float64
	cqUsed          *bool // set when a container unit is resolved (nil: nobody asks)
}

func (u *unitCtx) noteCQ() {
	if u.cqUsed != nil {
		*u.cqUsed = true
	}
}

// unitPx converts value+unit to px; pct reports a percentage.
func (u *unitCtx) unitPx(v float64, unit string) (px float64, pct bool, ok bool) {
	switch unit {
	case "px", "":
		return v, false, true
	case "%":
		return v, true, true
	case "em":
		return v * u.em, false, true
	case "rem":
		return v * u.rem, false, true
	case "ex":
		return v * u.exH, false, true
	case "ch":
		return v * u.chW, false, true
	case "cap":
		return v * u.capH, false, true
	case "lh":
		return v * u.lh, false, true
	case "rlh":
		return v * u.rem * 1.2, false, true
	case "vw", "svw", "lvw", "dvw":
		return v * u.vw / 100, false, true
	case "vh", "svh", "lvh", "dvh":
		return v * u.vh / 100, false, true
	case "vmin", "svmin", "lvmin", "dvmin":
		return v * math.Min(u.vw, u.vh) / 100, false, true
	case "vmax", "svmax", "lvmax", "dvmax":
		return v * math.Max(u.vw, u.vh) / 100, false, true
	case "vi":
		return v * u.vw / 100, false, true
	case "vb":
		return v * u.vh / 100, false, true
	case "cm":
		return v * 96 / 2.54, false, true
	case "mm":
		return v * 96 / 25.4, false, true
	case "q":
		return v * 96 / 101.6, false, true
	case "in":
		return v * 96, false, true
	case "pt":
		return v * 96 / 72, false, true
	case "pc":
		return v * 16, false, true
	case "cqw", "cqi":
		u.noteCQ()
		return v * u.containerW / 100, false, true
	case "cqh", "cqb":
		u.noteCQ()
		return v * u.cqH / 100, false, true
	case "cqmin", "cqmax":
		u.noteCQ()
		if unit == "cqmin" {
			return v * math.Min(u.containerW, u.cqH) / 100, false, true
		}
		return v * math.Max(u.containerW, u.cqH) / 100, false, true
	}
	return 0, false, false
}

// parseLength parses a <length-percentage> (or keyword when allowed) into a
// Length. Unitless zero is accepted; other unitless numbers only when
// allowNum (quirks-style attributes such as width="100").
func (u *unitCtx) parseLength(s string, allowNum bool) (Length, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Length{}, false
	}
	ls := strings.ToLower(s)
	switch ls {
	case "auto":
		return Length{K: LAuto}, true
	case "none":
		return Length{K: LNone}, true
	case "min-content", "-webkit-min-content":
		return Length{K: LMinContent}, true
	case "max-content", "-webkit-max-content":
		return Length{K: LMaxContent}, true
	case "fit-content", "-webkit-fit-content", "stretch", "-webkit-fill-available", "-moz-available":
		if ls == "fit-content" || ls == "-webkit-fit-content" {
			return Length{K: LFitContent, V: -1}, true
		}
		return Length{K: LPct, V: 100}, true
	}
	if name, args, ok := funcArgs(ls); ok {
		switch name {
		case "calc", "min", "max", "clamp", "-webkit-calc", "abs", "round", "mod", "rem", "sign", "pow", "sqrt", "hypot", "sin", "cos", "tan":
			c := u.parseCalc(ls)
			if c == nil || c.isNumb && c.Op == 'n' && false {
				return Length{}, false
			}
			return u.foldCalc(c), true
		case "fit-content":
			l, ok := u.parseLength(args, false)
			if !ok {
				return Length{}, false
			}
			if l.K == LPct {
				return Length{K: LFitContent, V: -1}, true
			}
			return Length{K: LFitContent, V: l.V}, true
		}
		return Length{}, false
	}
	v, rest, ok := numPrefix(ls)
	if !ok {
		return Length{}, false
	}
	if rest == "" {
		if v == 0 || allowNum {
			return Px(v), true
		}
		return Length{}, false
	}
	px, pct, ok := u.unitPx(v, rest)
	if !ok {
		return Length{}, false
	}
	if pct {
		return Pct(px), true
	}
	return Px(px), true
}

func (u *unitCtx) foldCalc(c *Calc) Length {
	if !c.hasPct() {
		return Px(c.eval(0))
	}
	if c.Op == 0 && c.A == nil && c.List == nil && c.Px == 0 {
		return Pct(c.Pct)
	}
	return Length{K: LCalc, C: c}
}

// parseCalc parses calc()/min()/max()/clamp() (the full function text).
func (u *unitCtx) parseCalc(s string) *Calc {
	p := &calcParser{s: s, u: u}
	c := p.parseTerm()
	p.skip()
	if c == nil || p.pos != len(p.s) || p.err {
		return nil
	}
	return c
}

type calcParser struct {
	s   string
	pos int
	u   *unitCtx
	err bool
}

func (p *calcParser) skip() {
	for p.pos < len(p.s) && (p.s[p.pos] == ' ' || p.s[p.pos] == '\t' || p.s[p.pos] == '\n') {
		p.pos++
	}
}

// parseTerm parses either a function call or a parenthesized sum.
func (p *calcParser) parseTerm() *Calc {
	return p.parseSum()
}

func (p *calcParser) parseSum() *Calc {
	a := p.parseProd()
	for a != nil {
		p.skip()
		if p.pos >= len(p.s) {
			break
		}
		op := p.s[p.pos]
		if op != '+' && op != '-' {
			break
		}
		p.pos++
		b := p.parseProd()
		if b == nil {
			p.err = true
			return nil
		}
		a = &Calc{Op: op, A: a, B: b}
	}
	return a
}

func (p *calcParser) parseProd() *Calc {
	a := p.parseAtom()
	for a != nil {
		p.skip()
		if p.pos >= len(p.s) {
			break
		}
		op := p.s[p.pos]
		if op != '*' && op != '/' {
			break
		}
		p.pos++
		b := p.parseAtom()
		if b == nil {
			p.err = true
			return nil
		}
		a = &Calc{Op: op, A: a, B: b}
	}
	return a
}

func (p *calcParser) parseAtom() *Calc {
	p.skip()
	if p.pos >= len(p.s) {
		p.err = true
		return nil
	}
	c := p.s[p.pos]
	if c == '(' {
		p.pos++
		x := p.parseSum()
		p.skip()
		if p.pos < len(p.s) && p.s[p.pos] == ')' {
			p.pos++
		} else {
			p.err = true
		}
		return x
	}
	// function?
	j := p.pos
	for j < len(p.s) && (p.s[j] >= 'a' && p.s[j] <= 'z' || p.s[j] == '-') {
		j++
	}
	if j > p.pos && j < len(p.s) && p.s[j] == '(' {
		name := p.s[p.pos:j]
		p.pos = j + 1
		var args []*Calc
		strat := ""
		if name == "round" {
			p.skip()
			for _, k := range []string{"nearest", "up", "down", "to-zero"} {
				if strings.HasPrefix(p.s[p.pos:], k) {
					q := p.pos + len(k)
					for q < len(p.s) && p.s[q] == ' ' {
						q++
					}
					if q < len(p.s) && p.s[q] == ',' {
						strat = k
						p.pos = q + 1
					}
				}
			}
		}
		for {
			x := p.parseSum()
			if x == nil {
				p.err = true
				return nil
			}
			args = append(args, x)
			p.skip()
			if p.pos < len(p.s) && p.s[p.pos] == ',' {
				p.pos++
				continue
			}
			break
		}
		p.skip()
		if p.pos < len(p.s) && p.s[p.pos] == ')' {
			p.pos++
		} else {
			p.err = true
			return nil
		}
		switch name {
		case "calc", "-webkit-calc":
			return args[0]
		case "min":
			return &Calc{Op: 'm', List: args}
		case "max":
			return &Calc{Op: 'M', List: args}
		case "clamp":
			if len(args) == 3 {
				return &Calc{Op: 'c', List: args}
			}
		case "abs", "sign", "sqrt", "sin", "cos", "tan", "asin", "acos", "atan", "atan2", "exp", "log", "pow", "hypot", "mod", "rem", "round":
			return &Calc{Op: 'F', Fn: name, List: args, Strat: strat, isNumb: numFn[name]}
		}
		p.err = true
		return nil
	}
	// number with unit
	v, rest, ok := numPrefix(p.s[p.pos:])
	if !ok {
		// constants
		for _, k := range []string{"infinity", "-infinity", "nan", "pi", "e"} {
			if strings.HasPrefix(p.s[p.pos:], k) {
				p.pos += len(k)
				val := math.Pi
				switch k {
				case "e":
					val = math.E
				case "infinity":
					val = 1e9
				case "-infinity":
					val = -1e9
				case "nan":
					val = 0
				}
				return &Calc{Op: 'n', Px: val, isNumb: true}
			}
		}
		p.err = true
		return nil
	}
	n := len(p.s) - p.pos - len(rest)
	p.pos += n
	// unit
	k := p.pos
	for k < len(p.s) && (p.s[k] >= 'a' && p.s[k] <= 'z' || p.s[k] == '%') {
		k++
	}
	unit := p.s[p.pos:k]
	p.pos = k
	if unit == "" {
		return &Calc{Op: 'n', Px: v, isNumb: true}
	}
	switch unit {
	case "deg":
		return &Calc{Op: 'n', Px: v * math.Pi / 180, isNumb: true}
	case "rad":
		return &Calc{Op: 'n', Px: v, isNumb: true}
	case "grad":
		return &Calc{Op: 'n', Px: v * math.Pi / 200, isNumb: true}
	case "turn":
		return &Calc{Op: 'n', Px: v * 2 * math.Pi, isNumb: true}
	}
	px, pct, ok := p.u.unitPx(v, unit)
	if !ok {
		p.err = true
		return nil
	}
	if pct {
		return &Calc{Pct: px}
	}
	return &Calc{Px: px}
}

// ---- colors -----------------------------------------------------------------------

var namedColors = map[string]Color{}

var numFn = map[string]bool{"sign": true, "sin": true, "cos": true, "tan": true, "asin": true, "acos": true, "atan": true, "atan2": true, "exp": true, "log": true}

func init() {
	for k, v := range map[string]uint32{
		"aliceblue": 0xf0f8ff, "antiquewhite": 0xfaebd7, "aqua": 0x00ffff, "aquamarine": 0x7fffd4, "azure": 0xf0ffff,
		"beige": 0xf5f5dc, "bisque": 0xffe4c4, "black": 0x000000, "blanchedalmond": 0xffebcd, "blue": 0x0000ff,
		"blueviolet": 0x8a2be2, "brown": 0xa52a2a, "burlywood": 0xdeb887, "cadetblue": 0x5f9ea0, "chartreuse": 0x7fff00,
		"chocolate": 0xd2691e, "coral": 0xff7f50, "cornflowerblue": 0x6495ed, "cornsilk": 0xfff8dc, "crimson": 0xdc143c,
		"cyan": 0x00ffff, "darkblue": 0x00008b, "darkcyan": 0x008b8b, "darkgoldenrod": 0xb8860b, "darkgray": 0xa9a9a9,
		"darkgreen": 0x006400, "darkgrey": 0xa9a9a9, "darkkhaki": 0xbdb76b, "darkmagenta": 0x8b008b, "darkolivegreen": 0x556b2f,
		"darkorange": 0xff8c00, "darkorchid": 0x9932cc, "darkred": 0x8b0000, "darksalmon": 0xe9967a, "darkseagreen": 0x8fbc8f,
		"darkslateblue": 0x483d8b, "darkslategray": 0x2f4f4f, "darkslategrey": 0x2f4f4f, "darkturquoise": 0x00ced1,
		"darkviolet": 0x9400d3, "deeppink": 0xff1493, "deepskyblue": 0x00bfff, "dimgray": 0x696969, "dimgrey": 0x696969,
		"dodgerblue": 0x1e90ff, "firebrick": 0xb22222, "floralwhite": 0xfffaf0, "forestgreen": 0x228b22, "fuchsia": 0xff00ff,
		"gainsboro": 0xdcdcdc, "ghostwhite": 0xf8f8ff, "gold": 0xffd700, "goldenrod": 0xdaa520, "gray": 0x808080,
		"green": 0x008000, "greenyellow": 0xadff2f, "grey": 0x808080, "honeydew": 0xf0fff0, "hotpink": 0xff69b4,
		"indianred": 0xcd5c5c, "indigo": 0x4b0082, "ivory": 0xfffff0, "khaki": 0xf0e68c, "lavender": 0xe6e6fa,
		"lavenderblush": 0xfff0f5, "lawngreen": 0x7cfc00, "lemonchiffon": 0xfffacd, "lightblue": 0xadd8e6, "lightcoral": 0xf08080,
		"lightcyan": 0xe0ffff, "lightgoldenrodyellow": 0xfafad2, "lightgray": 0xd3d3d3, "lightgreen": 0x90ee90, "lightgrey": 0xd3d3d3,
		"lightpink": 0xffb6c1, "lightsalmon": 0xffa07a, "lightseagreen": 0x20b2aa, "lightskyblue": 0x87cefa, "lightslategray": 0x778899,
		"lightslategrey": 0x778899, "lightsteelblue": 0xb0c4de, "lightyellow": 0xffffe0, "lime": 0x00ff00, "limegreen": 0x32cd32,
		"linen": 0xfaf0e6, "magenta": 0xff00ff, "maroon": 0x800000, "mediumaquamarine": 0x66cdaa, "mediumblue": 0x0000cd,
		"mediumorchid": 0xba55d3, "mediumpurple": 0x9370db, "mediumseagreen": 0x3cb371, "mediumslateblue": 0x7b68ee,
		"mediumspringgreen": 0x00fa9a, "mediumturquoise": 0x48d1cc, "mediumvioletred": 0xc71585, "midnightblue": 0x191970,
		"mintcream": 0xf5fffa, "mistyrose": 0xffe4e1, "moccasin": 0xffe4b5, "navajowhite": 0xffdead, "navy": 0x000080,
		"oldlace": 0xfdf5e6, "olive": 0x808000, "olivedrab": 0x6b8e23, "orange": 0xffa500, "orangered": 0xff4500,
		"orchid": 0xda70d6, "palegoldenrod": 0xeee8aa, "palegreen": 0x98fb98, "paleturquoise": 0xafeeee, "palevioletred": 0xdb7093,
		"papayawhip": 0xffefd5, "peachpuff": 0xffdab9, "peru": 0xcd853f, "pink": 0xffc0cb, "plum": 0xdda0dd,
		"powderblue": 0xb0e0e6, "purple": 0x800080, "rebeccapurple": 0x663399, "red": 0xff0000, "rosybrown": 0xbc8f8f,
		"royalblue": 0x4169e1, "saddlebrown": 0x8b4513, "salmon": 0xfa8072, "sandybrown": 0xf4a460, "seagreen": 0x2e8b57,
		"seashell": 0xfff5ee, "sienna": 0xa0522d, "silver": 0xc0c0c0, "skyblue": 0x87ceeb, "slateblue": 0x6a5acd,
		"slategray": 0x708090, "slategrey": 0x708090, "snow": 0xfffafa, "springgreen": 0x00ff7f, "steelblue": 0x4682b4,
		"tan": 0xd2b48c, "teal": 0x008080, "thistle": 0xd8bfd8, "tomato": 0xff6347, "turquoise": 0x40e0d0,
		"violet": 0xee82ee, "wheat": 0xf5deb3, "white": 0xffffff, "whitesmoke": 0xf5f5f5, "yellow": 0xffff00, "yellowgreen": 0x9acd32,
	} {
		namedColors[k] = Color{uint8(v >> 16), uint8(v >> 8), uint8(v), 255}
	}
	namedColors["transparent"] = Color{}
	// system colors as Chromium light scheme
	for k, v := range map[string]Color{
		"canvas": {255, 255, 255, 255}, "canvastext": {0, 0, 0, 255}, "linktext": {0, 0, 238, 255}, "visitedtext": {85, 26, 139, 255},
		"activetext": {255, 0, 0, 255}, "buttonface": {239, 239, 239, 255}, "buttontext": {0, 0, 0, 255}, "buttonborder": {118, 118, 118, 255},
		"field": {255, 255, 255, 255}, "fieldtext": {0, 0, 0, 255}, "highlight": {181, 213, 255, 255}, "highlighttext": {0, 0, 0, 255},
		"graytext": {109, 109, 109, 255}, "mark": {255, 255, 0, 255}, "marktext": {0, 0, 0, 255}, "selecteditem": {181, 213, 255, 255},
		"selecteditemtext": {0, 0, 0, 255}, "accentcolor": {0, 117, 255, 255}, "accentcolortext": {255, 255, 255, 255},
		"-webkit-link": {0, 0, 238, 255}, "-webkit-activelink": {255, 0, 0, 255}, "-webkit-focus-ring-color": {16, 16, 16, 255},
		"window": {255, 255, 255, 255}, "windowtext": {0, 0, 0, 255}, "threedface": {239, 239, 239, 255}, "buttonhighlight": {255, 255, 255, 255},
		"buttonshadow": {118, 118, 118, 255}, "threedshadow": {118, 118, 118, 255}, "threedhighlight": {255, 255, 255, 255},
		"threedlightshadow": {227, 227, 227, 255}, "threeddarkshadow": {118, 118, 118, 255}, "infobackground": {255, 255, 255, 255},
		"infotext": {0, 0, 0, 255}, "menu": {255, 255, 255, 255}, "menutext": {0, 0, 0, 255}, "scrollbar": {255, 255, 255, 255},
		"background": {255, 255, 255, 255}, "appworkspace": {255, 255, 255, 255}, "captiontext": {0, 0, 0, 255},
		"inactiveborder": {255, 255, 255, 255}, "inactivecaption": {255, 255, 255, 255}, "inactivecaptiontext": {0, 0, 0, 255},
		"-internal-active-list-box-selection": {0, 0, 0, 0},
	} {
		namedColors[k] = v
	}
}

// colorCtx carries what color parsing needs besides the string.
type colorCtx struct {
	cur  Color // currentcolor
	dark bool  // color-scheme dark for light-dark()
}

func clamp8(v float64) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(math.Round(v))
}

func alpha8(v float64) uint8 {
	if v <= 0 {
		return 0
	}
	if v >= 1 {
		return 255
	}
	return uint8(math.Round(v * 255))
}

// parseColor parses a CSS color. isCur reports the currentcolor keyword (the
// returned Color is cc.cur then).
func parseColor(s string, cc colorCtx) (c Color, isCur bool, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return
	}
	if s[0] == '#' {
		h := s[1:]
		for i := 0; i < len(h); i++ {
			if !isHex(h[i]) {
				return
			}
		}
		hv := func(i, n int) uint8 {
			v, _ := strconv.ParseUint(h[i:i+n], 16, 8)
			if n == 1 {
				v = v*16 + v
			}
			return uint8(v)
		}
		switch len(h) {
		case 3:
			return Color{hv(0, 1), hv(1, 1), hv(2, 1), 255}, false, true
		case 4:
			return Color{hv(0, 1), hv(1, 1), hv(2, 1), hv(3, 1)}, false, true
		case 6:
			return Color{hv(0, 2), hv(2, 2), hv(4, 2), 255}, false, true
		case 8:
			return Color{hv(0, 2), hv(2, 2), hv(4, 2), hv(6, 2)}, false, true
		}
		return
	}
	ls := strings.ToLower(s)
	if ls == "currentcolor" {
		return cc.cur, true, true
	}
	if v, k := namedColors[ls]; k {
		return v, false, true
	}
	name, args, fok := funcArgs(ls)
	if !fok {
		return
	}
	// split args into components: commas or spaces, and "/" alpha
	var comps []string
	alphaS := ""
	hasAlpha := false
	if strings.Contains(args, ",") && name != "color-mix" && name != "light-dark" {
		parts := splitTop(args, ',')
		for _, p := range parts {
			comps = append(comps, strings.TrimSpace(p))
		}
		if len(comps) == 4 {
			alphaS = comps[3]
			hasAlpha = true
			comps = comps[:3]
		}
	} else if name != "color-mix" && name != "light-dark" {
		f := fields(args)
		for i := 0; i < len(f); i++ {
			if f[i] == "/" {
				if i+1 < len(f) {
					alphaS = f[i+1]
					hasAlpha = true
				}
				break
			}
			comps = append(comps, f[i])
		}
	}
	alphaV := 1.0
	if hasAlpha {
		if strings.HasSuffix(alphaS, "%") {
			v, ok2 := parseNumber(strings.TrimSuffix(alphaS, "%"))
			if !ok2 {
				return
			}
			alphaV = v / 100
		} else if alphaS == "none" {
			alphaV = 0
		} else {
			v, ok2 := parseNumber(alphaS)
			if !ok2 {
				return
			}
			alphaV = v
		}
	}
	num := func(x string, scale float64) (float64, bool) { // percent -> scale
		if x == "none" {
			return 0, true
		}
		if strings.HasSuffix(x, "%") {
			v, ok2 := parseNumber(strings.TrimSuffix(x, "%"))
			return v / 100 * scale, ok2
		}
		return parseNumber(x)
	}
	switch name {
	case "rgb", "rgba":
		if len(comps) != 3 {
			return
		}
		var v [3]float64
		for i := 0; i < 3; i++ {
			x, ok2 := num(comps[i], 255)
			if !ok2 {
				return
			}
			v[i] = x
		}
		return Color{clamp8(v[0]), clamp8(v[1]), clamp8(v[2]), alpha8(alphaV)}, false, true
	case "hsl", "hsla", "hwb":
		if len(comps) != 3 {
			return
		}
		h, ok2 := parseAngle(comps[0])
		if !ok2 {
			hh, ok3 := parseNumber(comps[0])
			if !ok3 {
				if comps[0] != "none" {
					return
				}
			}
			h = hh
		}
		a, ok2 := num(comps[1], 1)
		b, ok3 := num(comps[2], 1)
		if !ok2 || !ok3 {
			return
		}
		if !strings.HasSuffix(comps[1], "%") {
			a /= 100
		}
		if !strings.HasSuffix(comps[2], "%") {
			b /= 100
		}
		var r, g, bl float64
		if name == "hwb" {
			r, g, bl = hwbToRGB(h, a, b)
		} else {
			r, g, bl = hslToRGB(h, a, b)
		}
		return Color{clamp8(r * 255), clamp8(g * 255), clamp8(bl * 255), alpha8(alphaV)}, false, true
	case "lab", "oklab", "lch", "oklch":
		if len(comps) != 3 {
			return
		}
		var l, a, b float64
		okx := true
		parseL := func(x string, pctScale float64) float64 {
			v, o := num(x, pctScale)
			okx = okx && o
			return v
		}
		switch name {
		case "oklab":
			l = parseL(comps[0], 1)
			a = parseL(comps[1], 0.4)
			b = parseL(comps[2], 0.4)
		case "lab":
			l = parseL(comps[0], 100)
			a = parseL(comps[1], 125)
			b = parseL(comps[2], 125)
		case "oklch", "lch":
			sc := 1.0
			cs := 0.4
			if name == "lch" {
				sc, cs = 100, 150
			}
			l = parseL(comps[0], sc)
			cch := parseL(comps[1], cs)
			h, o := parseAngle(comps[2])
			if !o {
				h, o = parseNumber(comps[2])
				okx = okx && o
			}
			a = cch * math.Cos(h*math.Pi/180)
			b = cch * math.Sin(h*math.Pi/180)
		}
		if !okx {
			return
		}
		var r, g, bl float64
		if name == "oklab" || name == "oklch" {
			r, g, bl = oklabToSRGB(l, a, b)
		} else {
			r, g, bl = labToSRGB(l, a, b)
		}
		return Color{clamp8(r * 255), clamp8(g * 255), clamp8(bl * 255), alpha8(alphaV)}, false, true
	case "color":
		if len(comps) < 4 {
			return
		}
		sp := comps[0]
		var v [3]float64
		for i := 0; i < 3; i++ {
			x, ok2 := num(comps[i+1], 1)
			if !ok2 {
				return
			}
			v[i] = x
		}
		switch sp {
		case "srgb":
		case "srgb-linear":
			for i := range v {
				v[i] = linToSRGB(v[i])
			}
		case "display-p3":
			// approximate: treat as sRGB (slightly out of gamut colors clip)
			r, g, b := p3ToSRGB(v[0], v[1], v[2])
			v = [3]float64{r, g, b}
		default:
			return
		}
		return Color{clamp8(v[0] * 255), clamp8(v[1] * 255), clamp8(v[2] * 255), alpha8(alphaV)}, false, true
	case "light-dark":
		parts := splitTop(args, ',')
		if len(parts) != 2 {
			return
		}
		pick := parts[0]
		if cc.dark {
			pick = parts[1]
		}
		return parseColor(pick, cc)
	case "color-mix":
		return parseColorMix(args, cc)
	}
	return
}

func hslToRGB(h, s, l float64) (r, g, b float64) {
	h = math.Mod(h, 360)
	if h < 0 {
		h += 360
	}
	s = math.Max(0, math.Min(1, s))
	l = math.Max(0, math.Min(1, l))
	f := func(n float64) float64 {
		k := math.Mod(n+h/30, 12)
		a := s * math.Min(l, 1-l)
		return l - a*math.Max(-1, math.Min(math.Min(k-3, 9-k), 1))
	}
	return f(0), f(8), f(4)
}

func hwbToRGB(h, w, b float64) (float64, float64, float64) {
	if w+b >= 1 {
		g := w / (w + b)
		return g, g, g
	}
	r, g, bl := hslToRGB(h, 1, 0.5)
	k := 1 - w - b
	return r*k + w, g*k + w, bl*k + w
}

func linToSRGB(v float64) float64 {
	if v <= 0.0031308 {
		return 12.92 * v
	}
	return 1.055*math.Pow(v, 1/2.4) - 0.055
}

func srgbToLin(v float64) float64 {
	if v <= 0.04045 {
		return v / 12.92
	}
	return math.Pow((v+0.055)/1.055, 2.4)
}

func oklabToSRGB(L, a, b float64) (float64, float64, float64) {
	l_ := L + 0.3963377774*a + 0.2158037573*b
	m_ := L - 0.1055613458*a - 0.0638541728*b
	s_ := L - 0.0894841775*a - 1.2914855480*b
	l, m, s := l_*l_*l_, m_*m_*m_, s_*s_*s_
	r := 4.0767416621*l - 3.3077115913*m + 0.2309699292*s
	g := -1.2684380046*l + 2.6097574011*m - 0.3413193965*s
	bl := -0.0041960863*l - 0.7034186147*m + 1.7076147010*s
	return linToSRGB(r), linToSRGB(g), linToSRGB(bl)
}

func labToSRGB(L, a, b float64) (float64, float64, float64) {
	fy := (L + 16) / 116
	fx := a/500 + fy
	fz := fy - b/200
	f := func(t float64) float64 {
		if t*t*t > 216.0/24389 {
			return t * t * t
		}
		return (116*t - 16) / (24389.0 / 27)
	}
	X, Y, Z := 0.3457/0.3585*f(fx), f(fy), (1-0.3457-0.3585)/0.3585*f(fz)
	// D50 -> sRGB (Bradford adapted matrix)
	r := 3.1338561*X - 1.6168667*Y - 0.4906146*Z
	g := -0.9787684*X + 1.9161415*Y + 0.0334540*Z
	bl := 0.0719453*X - 0.2289914*Y + 1.4052427*Z
	return linToSRGB(r), linToSRGB(g), linToSRGB(bl)
}

func p3ToSRGB(r, g, b float64) (float64, float64, float64) {
	r, g, b = srgbToLin(r), srgbToLin(g), srgbToLin(b)
	R := 1.2249401*r - 0.2249404*g
	G := -0.0420569*r + 1.0420571*g
	B := -0.0196376*r - 0.0786361*g + 1.0982735*b
	return linToSRGB(R), linToSRGB(G), linToSRGB(B)
}

func parseColorMix(args string, cc colorCtx) (Color, bool, bool) {
	parts := splitTop(args, ',')
	if len(parts) != 3 {
		return Color{}, false, false
	}
	space := strings.Fields(strings.TrimSpace(parts[0]))
	if len(space) < 2 || space[0] != "in" {
		return Color{}, false, false
	}
	type item struct {
		c   Color
		p   float64
		has bool
	}
	var it [2]item
	for i := 0; i < 2; i++ {
		f := fields(strings.TrimSpace(parts[i+1]))
		var colorS string
		for _, w := range f {
			if strings.HasSuffix(w, "%") {
				v, ok := parseNumber(strings.TrimSuffix(w, "%"))
				if !ok {
					return Color{}, false, false
				}
				it[i].p, it[i].has = v/100, true
			} else {
				if colorS != "" {
					colorS += " "
				}
				colorS += w
			}
		}
		c, _, ok := parseColor(colorS, cc)
		if !ok {
			return Color{}, false, false
		}
		it[i].c = c
	}
	p1, p2 := it[0].p, it[1].p
	switch {
	case !it[0].has && !it[1].has:
		p1, p2 = 0.5, 0.5
	case it[0].has && !it[1].has:
		p2 = 1 - p1
	case !it[0].has && it[1].has:
		p1 = 1 - p2
	}
	sum := p1 + p2
	if sum <= 0 {
		return Color{}, false, false
	}
	alphaMul := 1.0
	if sum < 1 {
		alphaMul = sum
	}
	p1, p2 = p1/sum, p2/sum
	a1, a2 := float64(it[0].c.A)/255, float64(it[1].c.A)/255
	a := a1*p1 + a2*p2
	mix := func(x, y uint8) uint8 {
		if a == 0 {
			return 0
		}
		return clamp8((float64(x)*a1*p1 + float64(y)*a2*p2) / a)
	}
	// interpolate in srgb (the common case); other spaces approximated by srgb
	return Color{mix(it[0].c.R, it[1].c.R), mix(it[0].c.G, it[1].c.G), mix(it[0].c.B, it[1].c.B), alpha8(a * alphaMul)}, false, true
}

// ParseColor parses a CSS color for callers outside the cascade (light scheme).
func ParseColor(s string) (Color, bool, bool) {
	return parseColor(s, colorCtx{cur: Color{0, 0, 0, 255}})
}
