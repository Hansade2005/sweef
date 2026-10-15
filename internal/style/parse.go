package style

import (
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/sweefbrowser/sweef/internal/font"
	snet "github.com/sweefbrowser/sweef/internal/net"
)

// Origin of a rule in the cascade.
type Origin uint8

// Cascade origins.
const (
	OriginUA   Origin = iota
	OriginHint        // presentational attributes
	OriginAuthor
)

// Decl is one declaration. Name is lower-cased (except custom properties).
type Decl struct {
	Name string
	Val  string
	L    string // lower-cased Val (shared with Val when already lower)
	Imp  bool
}

// Entry is one comma-separated selector alternative of a rule.
type Entry struct {
	Sel    snet.SelectorEntry
	Spec   uint32
	Pseudo string // "", before, after, marker, first-line, first-letter, placeholder, ...
	Rule   *Rule
	key    string // bucket key (for debugging)
	// dynamic: the selector uses :has(), so whether it matches depends on the element's descendants (or later
	// siblings), which a cached style does not observe (persist.go)
	dynamic bool
}

// Rule is a style rule with its declarations.
type Rule struct {
	Decls   []Decl
	Entries []*Entry
	Origin  Origin
	Layer   int // 0 unlayered; larger = later layer
	Cont    *ContCond
	Scope   *ScopeCond
	Order   int
	id      int
}

// FontFace is a parsed @font-face block.
type FontFace struct {
	Family string
	Weight string
	Style  string
	Src    string
	// Desc holds every descriptor parsed (fontface.go); source URLs are
	// relative to the stylesheet.
	Desc font.FaceDesc
}

// Env describes the environment media queries and viewport units see.
type Env struct {
	Width, Height float64
	DPR           float64
	ColorScheme   string // light | dark
	Print         bool
	ReducedMotion bool
}

// Parsed is the result of parsing one stylesheet text.
type Parsed struct {
	Rules []*Rule
	Fonts []FontFace
}

type parser struct {
	env       *Env
	origin    Origin
	order     *int
	layers    map[string]int
	layerSeq  *int
	curLayer  int
	out       *Parsed
	supportsF func(name, val string) bool
	depth     int
	cont      *ContCond
	scope     *ScopeCond
	quirks    bool
}

// ParseSheet parses CSS text. order is a shared counter that gives rules a
// global source order across sheets; layers is shared between sheets so
// @layer ordering is document-wide.
func ParseSheet(text string, env *Env, origin Origin, order *int, layers map[string]int) *Parsed {
	return ParseSheetQ(text, env, origin, order, layers, false)
}

// ParseSheetQ is ParseSheet for a document in quirks mode (class and id
// selectors compare case-insensitively).
func ParseSheetQ(text string, env *Env, origin Origin, order *int, layers map[string]int, quirks bool) *Parsed {
	if len(text) < 256 || len(text) > 8<<20 || env == nil {
		return parseSheetQ(text, env, origin, order, layers, quirks)
	}
	// A page repeats the same stylesheets on every layout; parsing is a pure
	// function of the text, the environment and the shared counters, so the
	// parsed rules (immutable once built) are reused.
	k := parseKey{text: text, env: *env, origin: origin, quirks: quirks, order: *order, layers: layersKey(layers)}
	parseCache.Lock()
	if e, ok := parseCache.m[k]; ok {
		parseCache.Unlock()
		*order = e.order
		for n, v := range e.layers {
			layers[n] = v
		}
		return e.out
	}
	parseCache.Unlock()
	before := make(map[string]int, len(layers))
	for n, v := range layers {
		before[n] = v
	}
	out := parseSheetQ(text, env, origin, order, layers, quirks)
	e := &parseEntry{out: out, order: *order, layers: map[string]int{}}
	for n, v := range layers {
		if bv, ok := before[n]; !ok || bv != v {
			e.layers[n] = v
		}
	}
	parseCache.Lock()
	if parseCache.m == nil {
		parseCache.m = map[parseKey]*parseEntry{}
	}
	if len(parseCache.order) >= 24 {
		delete(parseCache.m, parseCache.order[0])
		parseCache.order = parseCache.order[1:]
	}
	parseCache.m[k] = e
	parseCache.order = append(parseCache.order, k)
	parseCache.Unlock()
	return out
}

type parseKey struct {
	text   string
	env    Env
	origin Origin
	quirks bool
	order  int
	layers string
}

type parseEntry struct {
	out    *Parsed
	order  int
	layers map[string]int
}

var parseCache struct {
	sync.Mutex
	m     map[parseKey]*parseEntry
	order []parseKey
}

func layersKey(l map[string]int) string {
	if len(l) == 0 {
		return ""
	}
	ks := make([]string, 0, len(l))
	for k, v := range l {
		ks = append(ks, k+"\x00"+strconv.Itoa(v))
	}
	sort.Strings(ks)
	return strings.Join(ks, "\x01")
}

func parseSheetQ(text string, env *Env, origin Origin, order *int, layers map[string]int, quirks bool) *Parsed {
	p := &parser{env: env, origin: origin, order: order, layers: layers, out: &Parsed{}, quirks: quirks}
	n := len(layers)
	p.layerSeq = &n
	p.supportsF = Supports
	p.parseRules(stripComments(text), "")
	return p.out
}

func stripComments(s string) string {
	if !strings.Contains(s, "/*") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '"' || c == '\'' {
			j := i + 1
			for j < len(s) && s[j] != c {
				if s[j] == '\\' {
					j++
				}
				j++
			}
			if j >= len(s) {
				j = len(s) - 1
			}
			b.WriteString(s[i : j+1])
			i = j
			continue
		}
		if c == '/' && i+1 < len(s) && s[i+1] == '*' {
			j := strings.Index(s[i+2:], "*/")
			if j < 0 {
				break
			}
			i += j + 3
			b.WriteByte(' ')
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// readUntil reads from i until a top-level terminator in terms (';' or '{'
// or '}'), returning the text and index of the terminator (len(s) if none).
func readUntil(s string, i int, terms string) (string, int) {
	depth := 0
	start := i
	for ; i < len(s); i++ {
		c := s[i]
		switch c {
		case '\\':
			i++
		case '"', '\'':
			j := i + 1
			for j < len(s) && s[j] != c {
				if s[j] == '\\' {
					j++
				}
				j++
			}
			i = j
		case '(', '[':
			depth++
		case ')', ']':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 && strings.IndexByte(terms, c) >= 0 {
				return s[start:i], i
			}
		}
	}
	return s[start:], len(s)
}

// readBlock reads a {...} block starting at s[i]=='{'; returns inner text and
// the index after the closing brace.
func readBlock(s string, i int) (string, int) {
	depth := 0
	start := i + 1
	for ; i < len(s); i++ {
		c := s[i]
		switch c {
		case '\\':
			i++
		case '"', '\'':
			j := i + 1
			for j < len(s) && s[j] != c {
				if s[j] == '\\' {
					j++
				}
				j++
			}
			i = j
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start:i], i + 1
			}
		}
	}
	return s[start:], len(s)
}

func (p *parser) parseRules(s, parentSel string) {
	i := 0
	for i < len(s) {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r' || s[i] == '\f') {
			i++
		}
		if i >= len(s) {
			return
		}
		if strings.HasPrefix(s[i:], "<!--") {
			i += 4
			continue
		}
		if strings.HasPrefix(s[i:], "-->") {
			i += 3
			continue
		}
		if s[i] == '}' || s[i] == ';' {
			i++
			continue
		}
		prelude, j := readUntil(s, i, ";{")
		if j >= len(s) {
			return
		}
		if s[j] == ';' {
			p.atStatement(strings.TrimSpace(prelude))
			i = j + 1
			continue
		}
		body, k := readBlock(s, j)
		i = k
		prelude = strings.TrimSpace(prelude)
		if prelude != "" && prelude[0] == '@' {
			p.atBlock(prelude, body, parentSel)
			continue
		}
		p.styleRule(prelude, body, parentSel)
	}
}

func (p *parser) atStatement(prelude string) {
	l := strings.ToLower(prelude)
	if strings.HasPrefix(l, "@layer") {
		for _, n := range strings.Split(prelude[6:], ",") {
			p.layerRank(strings.TrimSpace(n))
		}
	}
}

func (p *parser) layerRank(name string) int {
	if name == "" {
		*p.layerSeq++
		return *p.layerSeq
	}
	if r, ok := p.layers[name]; ok {
		return r
	}
	*p.layerSeq++
	p.layers[name] = *p.layerSeq
	return *p.layerSeq
}

func (p *parser) atBlock(prelude, body, parentSel string) {
	sp := strings.IndexAny(prelude, " \t\n({")
	name := strings.ToLower(prelude)
	args := ""
	if sp > 0 {
		name = strings.ToLower(prelude[:sp])
		args = strings.TrimSpace(prelude[sp:])
	}
	switch name {
	case "@media":
		if MediaMatches(args, p.env) {
			p.nestedBody(body, parentSel)
		}
	case "@supports":
		if SupportsCond(args) {
			p.nestedBody(body, parentSel)
		}
	case "@layer":
		old := p.curLayer
		if args == "" {
			p.curLayer = p.layerRank("")
		} else {
			p.curLayer = p.layerRank(args)
		}
		p.nestedBody(body, parentSel)
		p.curLayer = old
	case "@font-face":
		if ff, ok := parseFontFaceDecls(parseDecls(body)); ok {
			p.out.Fonts = append(p.out.Fonts, ff)
		}
	case "@container":
		if cc, ok := parseContainerPrelude(args); ok {
			old := p.cont
			cc.Outer = old
			p.cont = cc
			p.nestedBody(body, parentSel)
			p.cont = old
		}
	case "@scope":
		if sc := parseScopePrelude(args, p.quirks); sc != nil {
			old := p.scope
			sc.Outer = old
			p.scope = sc
			p.nestedBody(body, parentSel)
			p.scope = old
		}
	case "@document", "@-moz-document":
	case "@starting-style", "@keyframes", "@-webkit-keyframes", "@page", "@property", "@font-feature-values", "@counter-style", "@view-transition", "@position-try":
	}
}

// nestedBody parses the contents of a conditional group rule: normally rules,
// but inside a style rule it may be declarations for the parent selector.
func (p *parser) nestedBody(body, parentSel string) {
	if parentSel == "" {
		p.parseRules(body, "")
		return
	}
	decls, nested := splitBlock(body)
	if len(decls) > 0 {
		p.addRule(parentSel, decls)
	}
	for _, n := range nested {
		if strings.HasPrefix(n.prelude, "@") {
			p.atBlock(n.prelude, n.body, parentSel)
		} else {
			p.styleRule(n.prelude, n.body, parentSel)
		}
	}
}

type nestedItem struct{ prelude, body string }

// splitBlock separates a style-rule body into declarations and nested rules.
func splitBlock(body string) ([]Decl, []nestedItem) {
	var decls []Decl
	var nested []nestedItem
	i := 0
	for i < len(body) {
		for i < len(body) && (body[i] == ' ' || body[i] == '\t' || body[i] == '\n' || body[i] == '\r' || body[i] == ';') {
			i++
		}
		if i >= len(body) {
			break
		}
		text, j := readUntil(body, i, ";{}")
		if j < len(body) && body[j] == '{' {
			// custom property with a {} value is a declaration
			if t := strings.TrimSpace(text); strings.HasPrefix(t, "--") && strings.HasSuffix(t, ":") {
				blk, k := readBlock(body, j)
				decls = append(decls, makeDecl(strings.TrimSuffix(t, ":"), "{"+blk+"}"))
				i = k
				continue
			}
			blk, k := readBlock(body, j)
			nested = append(nested, nestedItem{strings.TrimSpace(text), blk})
			i = k
			continue
		}
		if d, ok := parseDecl(text); ok {
			decls = append(decls, d)
		}
		i = j + 1
	}
	return decls, nested
}

func makeDecl(name, val string) Decl {
	name = strings.TrimSpace(name)
	if !strings.HasPrefix(name, "--") {
		name = strings.ToLower(name)
	}
	val = strings.TrimSpace(val)
	d := Decl{Name: name, Val: val}
	if i := strings.LastIndexByte(val, '!'); i >= 0 && strings.EqualFold(strings.TrimSpace(val[i+1:]), "important") {
		d.Imp = true
		d.Val = strings.TrimSpace(val[:i])
	}
	if len(d.Val) > 4096 {
		d.L = d.Val
	} else {
		d.L = strings.ToLower(d.Val)
	}
	return d
}

func parseDecl(text string) (Decl, bool) {
	i := strings.IndexByte(text, ':')
	if i <= 0 {
		return Decl{}, false
	}
	name := strings.TrimSpace(text[:i])
	if name == "" || strings.ContainsAny(name, " \t\n{}()") {
		return Decl{}, false
	}
	d := makeDecl(name, text[i+1:])
	if d.Val == "" && !strings.HasPrefix(name, "--") {
		return Decl{}, false
	}
	return d, true
}

func parseDecls(body string) []Decl {
	d, _ := splitBlock(body)
	return d
}

// ParseInline parses a style="" attribute value.
func ParseInline(s string) []Decl { return parseDecls(stripComments(s)) }

func (p *parser) styleRule(prelude, body, parentSel string) {
	sel := prelude
	if parentSel != "" {
		sel = nestSelector(prelude, parentSel)
	}
	decls, nested := splitBlock(body)
	if len(decls) > 0 || len(nested) == 0 {
		p.addRule(sel, decls)
	}
	for _, n := range nested {
		if strings.HasPrefix(n.prelude, "@") {
			p.atBlock(n.prelude, n.body, sel)
		} else {
			p.styleRule(n.prelude, n.body, sel)
		}
	}
}

// nestSelector rewrites a nested selector list against its parent.
func nestSelector(nested, parent string) string {
	parts := splitTop(nested, ',')
	for i, a := range parts {
		a = strings.TrimSpace(a)
		if strings.Contains(a, "&") {
			a = strings.ReplaceAll(a, "&", ":is("+parent+")")
		} else if a != "" && (a[0] == '>' || a[0] == '+' || a[0] == '~') {
			a = ":is(" + parent + ") " + a
		} else {
			a = ":is(" + parent + ") " + a
		}
		parts[i] = a
	}
	return strings.Join(parts, ", ")
}

func (p *parser) addRule(selText string, decls []Decl) {
	if len(decls) == 0 {
		return
	}
	r := &Rule{Decls: decls, Origin: p.origin, Layer: p.curLayer, Cont: p.cont, Scope: p.scope}
	*p.order++
	r.Order = *p.order
	for _, alt := range splitTop(selText, ',') {
		alt = strings.TrimSpace(alt)
		if alt == "" {
			continue
		}
		base, pseudo, ok := splitPseudo(alt)
		if !ok {
			continue
		}
		if base == "" {
			base = "*"
		}
		cs, err := snet.CompileSelectorQ(base, p.quirks)
		if err != nil {
			continue
		}
		for _, e := range cs.Entries() {
			r.Entries = append(r.Entries, &Entry{Sel: e, Spec: e.Spec, Pseudo: pseudo, Rule: r, key: base, dynamic: e.Features().Has})
		}
	}
	if len(r.Entries) == 0 {
		return
	}
	p.out.Rules = append(p.out.Rules, r)
}

var pseudoElems = map[string]string{
	"before": "before", "after": "after", "marker": "marker", "first-line": "first-line", "first-letter": "first-letter",
	"placeholder": "placeholder", "-webkit-input-placeholder": "placeholder", "-moz-placeholder": "placeholder",
	"file-selector-button": "file-selector-button", "-webkit-file-upload-button": "file-selector-button",
}

// splitPseudo removes a trailing pseudo-element from one selector. ok is
// false when the pseudo-element is one we do not model (the selector is dropped).
func splitPseudo(sel string) (base, pseudo string, ok bool) {
	depth := 0
	for i := 0; i < len(sel); i++ {
		c := sel[i]
		switch c {
		case '\\':
			i++
		case '(', '[':
			depth++
		case ')', ']':
			depth--
		case '"', '\'':
			j := i + 1
			for j < len(sel) && sel[j] != c {
				if sel[j] == '\\' {
					j++
				}
				j++
			}
			i = j
		case ':':
			if depth != 0 {
				continue
			}
			dbl := i+1 < len(sel) && sel[i+1] == ':'
			j := i + 1
			if dbl {
				j++
			}
			k := j
			for k < len(sel) && (sel[k] == '-' || sel[k] == '_' || sel[k] >= 'a' && sel[k] <= 'z' || sel[k] >= 'A' && sel[k] <= 'Z' || sel[k] >= '0' && sel[k] <= '9') {
				k++
			}
			name := strings.ToLower(sel[j:k])
			if !dbl {
				switch name {
				case "before", "after", "first-line", "first-letter":
				default:
					i = k - 1
					continue
				}
			}
			if k < len(sel) && sel[k] == '(' {
				d := 0
				for ; k < len(sel); k++ {
					if sel[k] == '(' {
						d++
					} else if sel[k] == ')' {
						d--
						if d == 0 {
							k++
							break
						}
					}
				}
			}
			pe, known := pseudoElems[name]
			if !known {
				return "", "", false
			}
			return strings.TrimSpace(sel[:i] + sel[k:]), pe, true
		}
	}
	return sel, "", true
}

// ---- media queries ------------------------------------------------------------------

// MediaMatches evaluates a media query list.
func MediaMatches(q string, env *Env) bool {
	q = strings.TrimSpace(strings.ToLower(q))
	if q == "" {
		return true
	}
	for _, alt := range splitTop(q, ',') {
		if mediaAlt(strings.TrimSpace(alt), env) {
			return true
		}
	}
	return false
}

func mediaAlt(q string, env *Env) bool {
	if q == "" {
		return false
	}
	neg := false
	if strings.HasPrefix(q, "not ") {
		neg = true
		q = strings.TrimSpace(q[4:])
	} else if strings.HasPrefix(q, "only ") {
		q = strings.TrimSpace(q[5:])
	}
	res := true
	toks := splitCond(q, "and")
	for idx, t := range toks {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if idx == 0 && t[0] != '(' {
			switch t {
			case "all", "screen":
				res = !env.Print || t == "all"
				if t == "screen" {
					res = !env.Print
				}
			case "print":
				res = env.Print
			default:
				if f := strings.Fields(t); len(f) > 0 && !strings.HasPrefix(t, "(") {
					res = false
				}
			}
			continue
		}
		if !mediaCond(t, env) {
			res = false
		}
	}
	return res != neg
}

// splitCond splits on a top-level keyword (and/or) surrounded by spaces.
func splitCond(s, word string) []string {
	var out []string
	depth := 0
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
		}
		if depth == 0 && i > 0 && s[i] == ' ' && strings.HasPrefix(s[i+1:], word+" ") {
			out = append(out, s[start:i])
			i += len(word) + 1
			start = i + 1
		}
	}
	return append(out, s[start:])
}

func mediaCond(t string, env *Env) bool {
	t = strings.TrimSpace(t)
	if strings.HasPrefix(t, "not ") {
		return !mediaCond(t[4:], env)
	}
	if ors := splitCond(t, "or"); len(ors) > 1 {
		for _, o := range ors {
			if mediaCond(o, env) {
				return true
			}
		}
		return false
	}
	if ands := splitCond(t, "and"); len(ands) > 1 {
		for _, o := range ands {
			if !mediaCond(o, env) {
				return false
			}
		}
		return true
	}
	if len(t) < 2 || t[0] != '(' || t[len(t)-1] != ')' {
		return false
	}
	inner := strings.TrimSpace(t[1 : len(t)-1])
	if strings.HasPrefix(inner, "(") || strings.HasPrefix(inner, "not ") {
		return mediaCond(inner, env)
	}
	return mediaFeature(inner, env)
}

func mediaFeature(f string, env *Env) bool {
	if strings.ContainsAny(f, "<>=") && !strings.Contains(f, ":") {
		return mediaRange(f, env)
	}
	name, val := f, ""
	if k := strings.IndexByte(f, ':'); k >= 0 {
		name, val = strings.TrimSpace(f[:k]), strings.TrimSpace(f[k+1:])
	}
	uc := &unitCtx{em: 16, rem: 16, vw: env.Width, vh: env.Height}
	px := func() (float64, bool) {
		l, ok := uc.parseLength(val, false)
		return l.V, ok && l.K == LPx
	}
	dpr := env.DPR
	if dpr == 0 {
		dpr = 1
	}
	cmp := func(have float64, kind string, want float64) bool {
		switch kind {
		case "min":
			return have >= want
		case "max":
			return have <= want
		}
		return have == want
	}
	kind := ""
	base := name
	if strings.HasPrefix(name, "min-") {
		kind, base = "min", name[4:]
	} else if strings.HasPrefix(name, "max-") {
		kind, base = "max", name[4:]
	}
	switch base {
	case "width", "device-width":
		if val == "" {
			return env.Width > 0
		}
		v, ok := px()
		return ok && cmp(env.Width, kind, v)
	case "height", "device-height":
		if val == "" {
			return env.Height > 0
		}
		v, ok := px()
		return ok && cmp(env.Height, kind, v)
	case "aspect-ratio", "device-aspect-ratio":
		w, h := ratio(val)
		return h > 0 && cmp(env.Width/env.Height, kind, w/h)
	case "orientation":
		if env.Width >= env.Height {
			return val == "landscape"
		}
		return val == "portrait"
	case "resolution":
		v, rest, ok := numPrefix(val)
		if !ok {
			return false
		}
		switch rest {
		case "dpi":
			v /= 96
		case "dpcm":
			v *= 2.54 / 96
		}
		return cmp(dpr, kind, v)
	case "-webkit-device-pixel-ratio":
		v, ok := parseNumber(val)
		return ok && cmp(dpr, kind, v)
	case "prefers-color-scheme":
		return val == env.ColorScheme || (val == "light" && env.ColorScheme == "")
	case "prefers-reduced-motion":
		if env.ReducedMotion {
			return val == "reduce"
		}
		return val == "no-preference"
	case "prefers-contrast", "forced-colors", "prefers-reduced-transparency", "prefers-reduced-data":
		return val == "no-preference" || val == "none"
	case "inverted-colors", "scan":
		return false
	case "hover", "any-hover":
		// headless Chromium has no pointing device
		return val == "none"
	case "pointer", "any-pointer":
		return val == "none"
	case "color":
		if val == "" {
			return true
		}
		v, ok := parseNumber(val)
		return ok && cmp(8, kind, v)
	case "color-index", "monochrome":
		if val == "" {
			return false
		}
		v, ok := parseNumber(val)
		return ok && cmp(0, kind, v)
	case "color-gamut":
		return val == "srgb"
	case "display-mode":
		return val == "browser"
	case "scripting":
		return val == "enabled"
	case "update":
		return val == "fast"
	case "overflow-block":
		return val == "scroll"
	case "overflow-inline":
		return val == "scroll"
	case "grid":
		return val == "0"
	}
	return false
}

func ratio(s string) (float64, float64) {
	parts := strings.Split(s, "/")
	w, _ := parseNumber(parts[0])
	h := 1.0
	if len(parts) > 1 {
		h, _ = parseNumber(parts[1])
	}
	return w, h
}

func mediaRange(f string, env *Env) bool {
	toks := fields(f)
	// forms: name op value | value op name | value op name op value
	var vals []string
	var ops []string
	for _, t := range toks {
		switch t {
		case "<", ">", "<=", ">=", "=":
			ops = append(ops, t)
		default:
			vals = append(vals, t)
		}
	}
	// the parser above splits on whitespace only; handle glued operators
	if len(ops) == 0 {
		for _, op := range []string{"<=", ">=", "<", ">", "="} {
			if i := strings.Index(f, op); i >= 0 {
				ops = append(ops, op)
				vals = []string{strings.TrimSpace(f[:i]), strings.TrimSpace(f[i+len(op):])}
				break
			}
		}
	}
	uc := &unitCtx{em: 16, rem: 16, vw: env.Width, vh: env.Height}
	feat := func(n string) (float64, bool) {
		switch n {
		case "width":
			return env.Width, true
		case "height":
			return env.Height, true
		case "aspect-ratio":
			return env.Width / env.Height, true
		case "resolution":
			if env.DPR == 0 {
				return 1, true
			}
			return env.DPR, true
		}
		return 0, false
	}
	value := func(s string) (float64, bool) {
		if strings.Contains(s, "/") {
			w, h := ratio(s)
			return w / h, h != 0
		}
		if l, ok := uc.parseLength(s, false); ok && l.K == LPx {
			return l.V, true
		}
		if v, rest, ok := numPrefix(s); ok && (rest == "dppx" || rest == "x" || rest == "") {
			return v, true
		}
		return 0, false
	}
	test := func(a float64, op string, b float64) bool {
		switch op {
		case "<":
			return a < b
		case "<=":
			return a <= b
		case ">":
			return a > b
		case ">=":
			return a >= b
		}
		return a == b
	}
	if len(vals) == 2 && len(ops) == 1 {
		if v, ok := feat(vals[0]); ok {
			r, ok2 := value(vals[1])
			return ok2 && test(v, ops[0], r)
		}
		if v, ok := feat(vals[1]); ok {
			l, ok2 := value(vals[0])
			return ok2 && test(l, ops[0], v)
		}
	}
	if len(vals) == 3 && len(ops) == 2 {
		v, ok := feat(vals[1])
		l, ok1 := value(vals[0])
		r, ok2 := value(vals[2])
		return ok && ok1 && ok2 && test(l, ops[0], v) && test(v, ops[1], r)
	}
	return false
}

// ---- @supports -----------------------------------------------------------------------

// SupportsCond evaluates an @supports condition.
func SupportsCond(c string) bool {
	c = strings.TrimSpace(c)
	l := strings.ToLower(c)
	if strings.HasPrefix(l, "not ") {
		return !SupportsCond(c[4:])
	}
	if ors := splitCond(l, "or"); len(ors) > 1 {
		for _, o := range ors {
			if SupportsCond(o) {
				return true
			}
		}
		return false
	}
	if ands := splitCond(l, "and"); len(ands) > 1 {
		for _, o := range ands {
			if !SupportsCond(o) {
				return false
			}
		}
		return true
	}
	if strings.HasPrefix(l, "selector(") && strings.HasSuffix(l, ")") {
		_, err := snet.CompileSelector(c[9 : len(c)-1])
		return err == nil
	}
	if strings.HasPrefix(l, "font-tech(") || strings.HasPrefix(l, "font-format(") {
		return true
	}
	if len(c) > 1 && c[0] == '(' && c[len(c)-1] == ')' {
		inner := strings.TrimSpace(c[1 : len(c)-1])
		il := strings.ToLower(inner)
		if strings.HasPrefix(il, "(") || strings.HasPrefix(il, "not ") || strings.Contains(il, ") and ") || strings.Contains(il, ") or ") {
			return SupportsCond(inner)
		}
		d, ok := parseDecl(inner)
		if !ok {
			return false
		}
		return Supports(d.Name, d.Val)
	}
	return false
}
