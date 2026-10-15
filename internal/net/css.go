package net

import (
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// css.go — a minimal CSS cascade for visibility decisions.
//
// Sweef has no layout engine, but agents still expect `.menu{display:none}`
// or `[hidden]{display:none}` style rules to keep hidden content out of
// snapshots. This file parses stylesheet text into rules (selector list +
// the handful of declarations we care about), evaluates @media against a
// viewport, and resolves per-element computed values for display and
// visibility by the usual order: inline !important > rule !important >
// inline > rule, then specificity, then source order. Results are cached per
// node and invalidated by a generation counter bumped on every DOM mutation.

// cssProp enumerates the declarations we keep. Everything else is dropped at
// parse time so a page with thousands of colour rules costs nothing later.
type cssProp uint8

const (
	propDisplay cssProp = iota
	propVisibility
	propOpacity
	propContentVisibility
	propPosition
	propLeft
	propTop
	propWidth
	propHeight
	propClip
	propClipPath
	propFontSize
	propOverflow
	numProps
)

var cssPropNames = map[string]cssProp{
	"display": propDisplay, "visibility": propVisibility, "opacity": propOpacity,
	"content-visibility": propContentVisibility, "position": propPosition,
	"left": propLeft, "top": propTop, "width": propWidth, "height": propHeight,
	"clip": propClip, "clip-path": propClipPath, "font-size": propFontSize,
	"overflow": propOverflow,
}

type cssDecl struct {
	prop      cssProp
	val       string // lower-cased, trimmed
	important bool
}

// cssRule is one complex selector with its declarations. A selector list
// `a, .b { ... }` becomes two rules sharing decls, each with its own
// specificity.
type cssRule struct {
	sel   complexSel
	spec  uint32 // (ids<<20 | classes<<10 | tags)
	order int    // source order across all sheets
	decls []cssDecl
	need  bloom // tags/ids/classes some ancestor of a match must carry
}

// bloom is a 128-bit set of hashed tag/id/class names used to skip rules
// whose ancestor compounds cannot possibly match (the classic ancestor
// filter): if a rule needs `.menu` somewhere above the element and no
// ancestor has a class hashing to that bit, the rule is never matched.
type bloom [2]uint64

func bloomBit(s string) bloom {
	h := uint32(2166136261)
	for i := 0; i < len(s); i++ {
		h = (h ^ uint32(s[i])) * 16777619
	}
	var b bloom
	b[(h>>6)&1] |= 1 << (h & 63)
	return b
}

func (b *bloom) add(o bloom) { b[0] |= o[0]; b[1] |= o[1] }

// subset reports whether every bit of b is set in o.
func (b bloom) subset(o bloom) bool { return b[0]&^o[0] == 0 && b[1]&^o[1] == 0 }

// selfBloom hashes the element's own tag, id and classes.
func selfBloom(n *html.Node) bloom {
	b := bloomBit(n.Data)
	for _, a := range n.Attr {
		switch a.Key {
		case "id":
			if a.Val != "" {
				b.add(bloomBit(a.Val))
			}
		case "class":
			for _, c := range strings.Fields(a.Val) {
				b.add(bloomBit(c))
			}
		}
	}
	return b
}

// needBloom collects the names an ancestor of the subject must carry: every
// compound joined to its right-hand neighbour by a descendant or child
// combinator is an ancestor of the subject (a compound to the right of it
// is the subject, an ancestor, or a sibling of one of those).
func needBloom(c complexSel) bloom {
	var b bloom
	for j := 0; j+1 < len(c.parts); j++ {
		if cb := c.parts[j+1].comb; cb != ' ' && cb != '>' {
			continue
		}
		p := c.parts[j]
		if p.tag != "" && p.tag != "*" {
			b.add(bloomBit(p.tag))
		}
		if p.id != "" {
			b.add(bloomBit(p.id))
		}
		for _, k := range p.classes {
			b.add(bloomBit(k))
		}
	}
	return b
}

// cssState is the compiled cascade plus its per-generation cache.
type cssState struct {
	rules     []*cssRule
	byID      map[string][]*cssRule
	byClass   map[string][]*cssRule
	byTag     map[string][]*cssRule
	byAttr    map[string][]*cssRule
	universal []*cssRule
	hasSheets bool

	gen   uint64
	cache map[*html.Node]*computedStyle
}

// computedStyle holds the cascaded values for one element. Empty string
// means "not specified by any inline or stylesheet declaration".
type computedStyle struct {
	vals [numProps]string
	// resolved values after inheritance / UA defaults
	display    string
	visibility string
	self, anc  bloom // this element's names; the union of its ancestors'
}

// ---- Doc plumbing ----------------------------------------------------------

func (d *Doc) cssLocked() *cssState {
	if d.css == nil {
		d.css = &cssState{}
	}
	return d.css
}

// invalidateStyles drops the computed-style cache. Called on every mutation.
func (d *Doc) invalidateStyles() {
	d.contentGen.Add(1)
	d.invalidateStylesOnly()
}

// invalidateStylesOnly is invalidateStyles for a mutation that does not count as a content change (ContentGen).
func (d *Doc) invalidateStylesOnly() {
	d.mutGen.Add(1)
	d.cssMu.Lock()
	if d.css != nil {
		d.css.gen++
		d.css.cache = nil
	}
	d.cssMu.Unlock()
}

// SetStylesheets parses sheets (in cascade order: inline <style> contents and
// fetched <link rel=stylesheet> bodies, document order) and installs the
// resulting rules. @media is evaluated against viewportWidth (px; 0 means
// 1280) and colorScheme ("light"/"dark"; "" means light). Calling it again
// replaces the previous rules and clears the cache.
func (d *Doc) SetStylesheets(sheets []string, viewportWidth int, colorScheme string) {
	if viewportWidth <= 0 {
		viewportWidth = 1280
	}
	if colorScheme == "" {
		colorScheme = "light"
	}
	st := &cssState{
		byID: map[string][]*cssRule{}, byClass: map[string][]*cssRule{},
		byTag: map[string][]*cssRule{}, byAttr: map[string][]*cssRule{},
		hasSheets: true,
	}
	p := &cssParser{vw: viewportWidth, scheme: strings.ToLower(colorScheme)}
	for _, s := range sheets {
		p.parseSheet(s)
	}
	for _, r := range p.rules {
		st.index(r)
	}
	st.rules = p.rules
	d.cssMu.Lock()
	if d.css != nil {
		st.gen = d.css.gen + 1
	}
	d.css = st
	d.cssMu.Unlock()
}

// index files a rule under the cheapest pre-filter key of its rightmost
// compound so that most rules are never fully matched against an element.
func (st *cssState) index(r *cssRule) {
	last := r.sel.parts[len(r.sel.parts)-1]
	switch {
	case last.id != "":
		st.byID[last.id] = append(st.byID[last.id], r)
	case len(last.classes) > 0:
		st.byClass[last.classes[0]] = append(st.byClass[last.classes[0]], r)
	case last.tag != "" && last.tag != "*":
		st.byTag[last.tag] = append(st.byTag[last.tag], r)
	case len(last.attrs) > 0:
		st.byAttr[last.attrs[0].name] = append(st.byAttr[last.attrs[0].name], r)
	default:
		st.universal = append(st.universal, r)
	}
}

// ComputedDisplay returns the element's display value after the cascade:
// the winning declaration if any, else "none" for UA-hidden elements
// (head, script, style, template, …, and the hidden attribute), else "block"
// or "inline" from a tag table. Non-elements return "".
func (d *Doc) ComputedDisplay(n *html.Node) string {
	if n == nil || n.Type != html.ElementNode {
		return ""
	}
	d.cssMu.Lock()
	defer d.cssMu.Unlock()
	return d.styleLocked(n).display
}

// ComputedVisibility returns "visible", "hidden" or "collapse". visibility
// inherits from the parent when the element does not set it.
func (d *Doc) ComputedVisibility(n *html.Node) string {
	if n == nil || n.Type != html.ElementNode {
		return ""
	}
	d.cssMu.Lock()
	defer d.cssMu.Unlock()
	return d.styleLocked(n).visibility
}

// IsScreenReaderOnly reports whether n carries one of the common
// visually-hidden-but-accessible patterns (.sr-only / .visually-hidden):
// position:absolute with clip:rect(0 0 0 0) / clip-path:inset(50%), or a
// 1px×1px box with overflow:hidden. Such nodes stay in snapshots, as in
// Playwright; this is an informational helper only.
func (d *Doc) IsScreenReaderOnly(n *html.Node) bool {
	if n == nil || n.Type != html.ElementNode {
		return false
	}
	d.cssMu.Lock()
	cs := d.styleLocked(n)
	d.cssMu.Unlock()
	if cs.vals[propPosition] != "absolute" && cs.vals[propPosition] != "fixed" {
		return false
	}
	clip := strings.ReplaceAll(cs.vals[propClip], " ", "")
	switch clip {
	case "rect(0,0,0,0)", "rect(0000)", "rect(0px,0px,0px,0px)", "rect(0px0px0px0px)", "rect(1px,1px,1px,1px)", "rect(1px1px1px1px)":
		return true
	}
	if cp := strings.ReplaceAll(cs.vals[propClipPath], " ", ""); cp == "inset(50%)" || cp == "inset(100%)" || cp == "polygon(0 0,0 0,0 0)" {
		return true
	}
	tiny := func(v string) bool { return v == "1px" || v == "0" || v == "0px" }
	return tiny(cs.vals[propWidth]) && tiny(cs.vals[propHeight]) && cs.vals[propOverflow] == "hidden"
}

// styleLocked returns the cached computed style for n, computing it (and
// its ancestors', for inheritance) on first use. cssMu must be held.
func (d *Doc) styleLocked(n *html.Node) *computedStyle {
	st := d.cssLocked()
	if st.cache == nil {
		st.cache = map[*html.Node]*computedStyle{}
	}
	if cs, ok := st.cache[n]; ok {
		return cs
	}
	var ps *computedStyle
	if p := n.Parent; p != nil && p.Type == html.ElementNode {
		ps = d.styleLocked(p)
	}
	var anc bloom
	if ps != nil {
		anc = ps.anc
		anc.add(ps.self)
	}
	cs := st.cascade(n, anc)
	cs.anc, cs.self = anc, selfBloom(n)
	// display
	switch v := cs.vals[propDisplay]; v {
	case "", "initial", "unset", "revert", "revert-layer":
		cs.display = uaDisplay(n)
	case "inherit":
		if ps != nil {
			cs.display = ps.display
		} else {
			cs.display = "block"
		}
	default:
		cs.display = v
	}
	// visibility (inherited)
	switch v := cs.vals[propVisibility]; v {
	case "visible", "hidden", "collapse":
		cs.visibility = v
	case "initial", "revert", "revert-layer":
		cs.visibility = "visible"
	default: // "", inherit, unset
		if ps != nil {
			cs.visibility = ps.visibility
		} else {
			cs.visibility = "visible"
		}
	}
	st.cache[n] = cs
	return cs
}

// uaDisplay is the user-agent default when nothing else sets display.
func uaDisplay(n *html.Node) string {
	switch n.Data {
	case "head", "script", "style", "template", "title", "meta", "link", "base", "noscript":
		return "none"
	}
	if hasAttr(n, "hidden") {
		return "none"
	}
	if blockTags[n.Data] {
		return "block"
	}
	return "inline"
}

// Cascade rank of one declaration, compared as a plain integer:
// bit 63 !important, bit 62 inline style, bits 24-53 specificity,
// bits 0-23 source order + 1 (so a winning key is never zero).
const (
	keyImportant = uint64(1) << 63
	keyInline    = uint64(1) << 62
	keyOrderMax  = 1<<24 - 2
)

func ruleKey(r *cssRule) uint64 {
	o := r.order
	if o > keyOrderMax {
		o = keyOrderMax
	}
	return uint64(r.spec)<<24 | uint64(o+1)
}

// cascade resolves the specified value of every kept property for n from
// matching rules and the inline style attribute. anc is the bloom of the
// element's ancestors, used to skip rules that cannot match.
func (st *cssState) cascade(n *html.Node, anc bloom) *computedStyle {
	cs := &computedStyle{}
	var best [numProps]uint64
	apply := func(decls []cssDecl, base uint64) {
		for _, dc := range decls {
			key := base
			if dc.important {
				key |= keyImportant
			}
			if key > best[dc.prop] {
				best[dc.prop] = key
				cs.vals[dc.prop] = dc.val
			}
		}
	}
	if len(st.rules) > 0 {
		try := func(rs []*cssRule) {
			for _, r := range rs {
				if r.need.subset(anc) && matchComplex(n, r.sel, len(r.sel.parts)-1, nil) {
					apply(r.decls, ruleKey(r))
				}
			}
		}
		for _, a := range n.Attr {
			switch a.Key {
			case "id":
				try(st.byID[a.Val])
			case "class":
				for _, c := range strings.Fields(a.Val) {
					try(st.byClass[c])
				}
			}
			if rs := st.byAttr[a.Key]; rs != nil {
				try(rs)
			}
		}
		try(st.byTag[n.Data])
		try(st.universal)
	}
	if s := attr(n, "style"); s != "" {
		apply(parseDeclarations(s), keyInline|1)
	}
	return cs
}

// specificity computes (ids, classes/attrs/pseudo-classes, tags) packed as
// ids<<20 | classes<<10 | tags for one complex selector.
func specificity(c complexSel) uint32 {
	var a, b, t uint32
	for _, part := range c.parts {
		pa, pb, pt := compoundSpecificity(part)
		a, b, t = a+pa, b+pb, t+pt
	}
	return pack(a, b, t)
}

func pack(a, b, t uint32) uint32 {
	if a > 1023 {
		a = 1023
	}
	if b > 1023 {
		b = 1023
	}
	if t > 1023 {
		t = 1023
	}
	return a<<20 | b<<10 | t
}

func compoundSpecificity(p compoundSel) (a, b, t uint32) {
	if p.id != "" {
		a++
	}
	b += uint32(len(p.classes) + len(p.attrs))
	if p.tag != "" && p.tag != "*" {
		t++
	}
	for _, ps := range p.pseudos {
		switch ps.name {
		case "::":
			t++
		case "where":
		case "not", "is", "has", "matches", "-webkit-any":
			var ma, mb, mt uint32
			var best uint32
			for _, sub := range ps.sub {
				var sa, sb, stt uint32
				for _, part := range sub.parts {
					pa, pb, pt := compoundSpecificity(part)
					sa, sb, stt = sa+pa, sb+pb, stt+pt
				}
				if k := pack(sa, sb, stt); k >= best {
					best, ma, mb, mt = k, sa, sb, stt
				}
			}
			a, b, t = a+ma, b+mb, t+mt
		default:
			b++
		}
	}
	return
}

// ---- parsing -----------------------------------------------------------------

type cssParser struct {
	vw     int
	scheme string
	rules  []*cssRule
	order  int
}

func (p *cssParser) parseSheet(src string) {
	src = stripCSSComments(src)
	p.parseRules(src)
}

// parseRules consumes a sequence of rules / at-rules.
func (p *cssParser) parseRules(s string) {
	i := 0
	for i < len(s) {
		i = skipCSSWS(s, i)
		if i >= len(s) {
			return
		}
		switch s[i] {
		case '}', ';':
			i++
			continue
		case '@':
			j := i + 1
			for j < len(s) && isNameChar(s[j]) {
				j++
			}
			name := strings.ToLower(s[i+1 : j])
			prelude, end, term := readPrelude(s, j)
			if term != '{' {
				i = end
				continue
			}
			body, after := readBlock(s, end)
			i = after
			switch name {
			case "media":
				if evalMediaQuery(prelude, p.vw, p.scheme) {
					p.parseRules(body)
				}
			case "layer", "scope", "starting-style":
				// cascade layers / scopes: ordering ignored, rules kept
				p.parseRules(body)
			default:
				// @supports, @keyframes, @font-face, @container, @page, ...
			}
		default:
			prelude, end, term := readPrelude(s, i)
			if term != '{' {
				i = end
				continue
			}
			body, after := readBlock(s, end)
			i = after
			p.addRule(prelude, body)
		}
	}
}

func (p *cssParser) addRule(prelude, body string) {
	prelude = strings.Map(func(r rune) rune {
		if r == '\r' || r == '\f' {
			return ' '
		}
		return r
	}, prelude)
	p.addDecls(prelude, body)
	if strings.IndexByte(body, '{') >= 0 {
		p.addNested(prelude, body)
	}
}

// addNested adds the rules nested in a style rule's body (CSS nesting): `@media Q { decls }` applies the
// declarations to the parent selector when Q matches (Tailwind v4 writes every responsive variant this way:
// `.lg\:block { @media (width >= 64rem) { display: block } }`), and `&:hover { }` / `.child { }` are rules
// relative to the parent.
func (p *cssParser) addNested(parent, body string) {
	i := 0
	for i < len(body) {
		i = skipCSSWS(body, i)
		if i >= len(body) {
			return
		}
		text, end, term := readPrelude(body, i)
		if term != '{' {
			i = end
			if term == '}' {
				i++
			}
			continue
		}
		inner, after := readBlock(body, end)
		i = after
		if strings.HasPrefix(text, "@") {
			j := 1
			for j < len(text) && isNameChar(text[j]) {
				j++
			}
			switch strings.ToLower(text[1:j]) {
			case "media":
				if evalMediaQuery(text[j:], p.vw, p.scheme) {
					p.addRule(parent, inner)
				}
			case "layer", "scope", "starting-style":
				p.addRule(parent, inner)
			}
			continue
		}
		p.addRule(nestSelector(parent, text), inner)
	}
}

// nestSelector resolves a nested selector against its parent: `&` is the parent, and a selector without `&` is
// a descendant of it.
func nestSelector(parent, sel string) string {
	par := strings.TrimSpace(parent)
	if len(splitTopLevel(par, ',')) > 1 {
		par = ":is(" + par + ")"
	}
	var out []string
	for _, s := range splitTopLevel(sel, ',') {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if strings.Contains(s, "&") {
			out = append(out, strings.ReplaceAll(s, "&", par))
		} else {
			out = append(out, par+" "+s)
		}
	}
	return strings.Join(out, ", ")
}

func (p *cssParser) addDecls(prelude, body string) {
	decls := parseDeclarations(body)
	if len(decls) == 0 {
		return
	}
	g, err := compileSelector(prelude)
	if err != nil {
		return
	}
	for _, c := range g {
		if len(c.parts) == 0 {
			continue
		}
		p.rules = append(p.rules, &cssRule{sel: c, spec: specificity(c), order: p.order, decls: decls, need: needBloom(c)})
	}
	p.order++
}

func skipCSSWS(s string, i int) int {
	for i < len(s) {
		switch s[i] {
		case ' ', '\t', '\n', '\r', '\f':
			i++
		default:
			return i
		}
	}
	return i
}

// skipString advances past a quoted string starting at s[i] (a quote).
func skipString(s string, i int) int {
	q := s[i]
	i++
	for i < len(s) {
		switch s[i] {
		case '\\':
			i += 2
			continue
		case q:
			return i + 1
		case '\n':
			return i // unterminated string ends at newline
		}
		i++
	}
	return i
}

// readPrelude reads up to the next top-level '{' or ';' (or EOF) and
// returns the text, the index just past the terminator, and the terminator
// byte (0 at EOF).
func readPrelude(s string, i int) (string, int, byte) {
	start := i
	depth := 0
	for i < len(s) {
		switch s[i] {
		case '\\': // an escape (`.content-\[\'x\'\]`) is never a quote or a bracket
			i += 2
			continue
		case '"', '\'':
			i = skipString(s, i)
			continue
		case '(', '[':
			depth++
		case ')', ']':
			if depth > 0 {
				depth--
			}
		case '{':
			if depth == 0 {
				return strings.TrimSpace(s[start:i]), i + 1, '{'
			}
		case ';':
			if depth == 0 {
				return strings.TrimSpace(s[start:i]), i + 1, ';'
			}
		case '}':
			if depth == 0 {
				return strings.TrimSpace(s[start:i]), i, '}'
			}
		}
		i++
	}
	return strings.TrimSpace(s[start:i]), i, 0
}

// readBlock reads the body of a block whose opening '{' was consumed, up to
// the matching '}', returning the inner text and the index after it.
func readBlock(s string, i int) (string, int) {
	start := i
	depth := 1
	for i < len(s) {
		switch s[i] {
		case '\\':
			i += 2
			continue
		case '"', '\'':
			i = skipString(s, i)
			continue
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start:i], i + 1
			}
		}
		i++
	}
	return s[start:i], i
}

// stripCSSComments removes /* */ comments outside strings.
func stripCSSComments(s string) string {
	if !strings.Contains(s, "/*") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s):
			b.WriteString(s[i : i+2])
			i += 2
		case c == '"' || c == '\'':
			j := skipString(s, i)
			b.WriteString(s[i:j])
			i = j
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			end := strings.Index(s[i+2:], "*/")
			if end < 0 {
				return b.String()
			}
			i += 2 + end + 2
			b.WriteByte(' ')
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// parseDeclarations parses `prop: value; …` (a rule body or a style
// attribute) keeping only the properties in cssPropNames. Nested rules
// (CSS nesting) inside the body are skipped.
func parseDeclarations(body string) []cssDecl {
	var out []cssDecl
	i := 0
	for i < len(body) {
		i = skipCSSWS(body, i)
		if i >= len(body) {
			break
		}
		if body[i] == ';' {
			i++
			continue
		}
		text, end, term := readPrelude(body, i)
		if term == '{' { // nested rule: skip its block
			_, i = readBlock(body, end)
			continue
		}
		i = end
		if term == '}' {
			i++
		}
		colon := strings.IndexByte(text, ':')
		if colon <= 0 {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(text[:colon]))
		prop, ok := cssPropNames[name]
		if !ok {
			continue
		}
		val := strings.TrimSpace(text[colon+1:])
		important := false
		if k := strings.LastIndexByte(val, '!'); k >= 0 && strings.EqualFold(strings.TrimSpace(val[k+1:]), "important") {
			important = true
			val = strings.TrimSpace(val[:k])
		}
		if val == "" {
			continue
		}
		out = append(out, cssDecl{prop: prop, val: strings.ToLower(val), important: important})
	}
	return out
}

// ---- media queries -------------------------------------------------------------

// evalMediaQuery evaluates a media query list against a viewport width and
// colour scheme. Supported: `screen`, `all`, `not`/`only`, `and`, `,`/`or`,
// (min-width|max-width|width[: v]) incl. range syntax, prefers-color-scheme,
// orientation. Unknown media types and features never match.
func evalMediaQuery(q string, vw int, scheme string) bool {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return true
	}
	for _, alt := range splitTopLevel(q, ',') {
		if evalMediaOr(strings.TrimSpace(alt), vw, scheme) {
			return true
		}
	}
	return false
}

func evalMediaOr(q string, vw int, scheme string) bool {
	parts := splitTopLevelWord(q, "or")
	if len(parts) > 1 {
		for _, p := range parts {
			if evalMediaAnd(strings.TrimSpace(p), vw, scheme) {
				return true
			}
		}
		return false
	}
	return evalMediaAnd(q, vw, scheme)
}

func evalMediaAnd(q string, vw int, scheme string) bool {
	negate := false
	if strings.HasPrefix(q, "not ") || q == "not" {
		negate = true
		q = strings.TrimSpace(q[3:])
	} else if strings.HasPrefix(q, "only ") {
		q = strings.TrimSpace(q[4:])
	}
	res := true
	for _, term := range splitTopLevelWord(q, "and") {
		term = strings.TrimSpace(term)
		if term == "" {
			continue
		}
		if !evalMediaTerm(term, vw, scheme) {
			res = false
			break
		}
	}
	if negate {
		return !res
	}
	return res
}

func evalMediaTerm(term string, vw int, scheme string) bool {
	if term[0] != '(' {
		switch term {
		case "all", "screen":
			return true
		}
		return false
	}
	// strip one level of parens; nested groups like ((a) and (b))
	if term[len(term)-1] != ')' {
		return false
	}
	inner := strings.TrimSpace(term[1 : len(term)-1])
	if strings.HasPrefix(inner, "(") || strings.HasPrefix(inner, "not ") {
		return evalMediaOr(inner, vw, scheme)
	}
	if strings.ContainsAny(inner, "<>=") && !strings.Contains(inner, ":") {
		return evalMediaRange(inner, vw)
	}
	name, val := inner, ""
	if k := strings.IndexByte(inner, ':'); k >= 0 {
		name, val = strings.TrimSpace(inner[:k]), strings.TrimSpace(inner[k+1:])
	}
	switch name {
	case "min-width", "min-device-width":
		px, ok := cssLengthPx(val)
		return ok && vw >= px
	case "max-width", "max-device-width":
		px, ok := cssLengthPx(val)
		return ok && vw <= px
	case "width", "device-width":
		if val == "" {
			return vw > 0
		}
		px, ok := cssLengthPx(val)
		return ok && vw == px
	case "prefers-color-scheme":
		return val == scheme
	case "orientation":
		return val == "landscape"
	case "color":
		return true
	}
	return false
}

// evalMediaRange handles `width <= 600px`, `600px < width`, and
// `400px <= width <= 800px`.
func evalMediaRange(expr string, vw int) bool {
	toks := strings.Fields(expr)
	cmp := func(l int, op string, r int) bool {
		switch op {
		case "<":
			return l < r
		case "<=":
			return l <= r
		case ">":
			return l > r
		case ">=":
			return l >= r
		case "=":
			return l == r
		}
		return false
	}
	isWidth := func(t string) bool { return t == "width" || t == "device-width" }
	switch len(toks) {
	case 3:
		if isWidth(toks[0]) {
			px, ok := cssLengthPx(toks[2])
			return ok && cmp(vw, toks[1], px)
		}
		if isWidth(toks[2]) {
			px, ok := cssLengthPx(toks[0])
			return ok && cmp(px, toks[1], vw)
		}
	case 5:
		if isWidth(toks[2]) {
			lo, ok1 := cssLengthPx(toks[0])
			hi, ok2 := cssLengthPx(toks[4])
			return ok1 && ok2 && cmp(lo, toks[1], vw) && cmp(vw, toks[3], hi)
		}
	}
	return false
}

// cssLengthPx converts px/em/rem lengths (16px per em) to integer pixels.
func cssLengthPx(v string) (int, bool) {
	v = strings.TrimSpace(v)
	mul := 1.0
	switch {
	case strings.HasSuffix(v, "px"):
		v = v[:len(v)-2]
	case strings.HasSuffix(v, "rem"):
		v, mul = v[:len(v)-3], 16
	case strings.HasSuffix(v, "em"):
		v, mul = v[:len(v)-2], 16
	case v == "0":
	default:
		return 0, false
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil {
		return 0, false
	}
	return int(f * mul), true
}

// splitTopLevel splits on sep outside parentheses.
func splitTopLevel(s string, sep byte) []string {
	var out []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case sep:
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	return append(out, s[start:])
}

// splitTopLevelWord splits on a whole word (e.g. "and") outside parentheses.
func splitTopLevelWord(s, word string) []string {
	var out []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case ' ', '\t', '\n':
			if depth == 0 && i+len(word)+1 < len(s) && s[i+1:i+1+len(word)] == word {
				after := s[i+1+len(word)]
				if after == ' ' || after == '(' || after == '\t' || after == '\n' {
					out = append(out, s[start:i])
					start = i + 1 + len(word)
					i = start - 1
				}
			}
		}
	}
	return append(out, s[start:])
}

// ---- stylesheet discovery --------------------------------------------------------

func printOnly(n *html.Node) bool {
	return strings.EqualFold(strings.TrimSpace(attr(n, "media")), "print")
}

// InlineStyleSheets returns the text of every <style> element in document
// order, skipping media="print". A <style> with another non-trivial media
// attribute is wrapped in an equivalent @media block so SetStylesheets can
// evaluate it.
func (d *Doc) InlineStyleSheets() []string {
	var out []string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "style" {
			if printOnly(n) {
				return
			}
			var sb strings.Builder
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				if c.Type == html.TextNode {
					sb.WriteString(c.Data)
				}
			}
			txt := sb.String()
			if m := strings.ToLower(strings.TrimSpace(attr(n, "media"))); m != "" && m != "all" && m != "screen" {
				txt = "@media " + m + " {\n" + txt + "\n}"
			}
			out = append(out, txt)
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(d.Root)
	return out
}

// StylesheetLinks returns the href of every <link rel=stylesheet> in
// document order, skipping media="print", alternate and disabled sheets.
// rel="preload" as="style" is not a stylesheet and is not listed.
func (d *Doc) StylesheetLinks() []string {
	var out []string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "link" {
			isSheet, alternate := false, false
			for _, tok := range strings.Fields(strings.ToLower(attr(n, "rel"))) {
				switch tok {
				case "stylesheet":
					isSheet = true
				case "alternate":
					alternate = true
				}
			}
			href := strings.TrimSpace(attr(n, "href"))
			if isSheet && !alternate && !printOnly(n) && !hasAttr(n, "disabled") && href != "" {
				out = append(out, href)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(d.Root)
	return out
}
