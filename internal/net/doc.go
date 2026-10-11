// Package net is Sweef's document + network layer: HTML5 parsing into a
// stable node tree, CSS selector matching, form modelling, HAR records and
// route interception. No cgo, no deps beyond golang.org/x/net/html.
package net

import (
	"fmt"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/net/html"
)

// Doc owns one parsed HTML tree. Node identity is a deterministic FNV hash of
// the element's path-in-tree (see nodeIDFor), so no side tables are needed and
// memory stays O(document) — critical for 50 MB targets.
type Doc struct {
	Root *html.Node
	base string

	mu        sync.RWMutex
	listeners []func(m Mutation)
}

// Mutation describes a DOM change in Playwright-mutation-like terms. The engine
// subscribes so it can re-run init hooks / notify agents without reparsing.
type Mutation struct {
	Op        string `json:"op"` // set-attr | remove-attr | set-value | inner-html | check | select
	BackendID int64  `json:"backendNodeId"`
	Key       string `json:"key,omitempty"`
	Value     string `json:"value,omitempty"`
}

// SetMutationListener installs (or clears, with nil) a callback fired on every
// tree mutation performed through this layer.
func (d *Doc) SetMutationListener(fn func(Mutation)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if fn == nil {
		d.listeners = nil
		return
	}
	d.listeners = []func(Mutation){fn}
}

func (d *Doc) note(m Mutation) {
	d.mu.RLock()
	ls := append([]func(Mutation){}, d.listeners...)
	d.mu.RUnlock()
	for _, f := range ls {
		f(m)
	}
}

func ParseDocument(src, base string) (*Doc, error) {
	hn, err := html.Parse(strings.NewReader(src))
	if err != nil {
		return nil, err
	}
	return &Doc{Root: hn, base: base}, nil
}

// ---- helpers on html.Node -------------------------------------------------

func attr(n *html.Node, k string) string {
	for _, a := range n.Attr {
		if a.Key == k {
			return a.Val
		}
	}
	return ""
}

func hasAttr(n *html.Node, k string) bool {
	for _, a := range n.Attr {
		if a.Key == k {
			return true
		}
	}
	return false
}

// ---- text / title ----------------------------------------------------------

func (d *Doc) Title() string {
	var t string
	var walk func(*html.Node) bool
	walk = func(n *html.Node) bool {
		if n.Type == html.ElementNode && n.Data == "title" {
			if n.FirstChild != nil {
				t = n.FirstChild.Data
			}
			return true
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if walk(c) {
				return true
			}
		}
		return false
	}
	walk(d.Root)
	return t
}

// innerText: whitespace-normalised visible text under a node.
func innerText(n *html.Node) string {
	var sb strings.Builder
	var skip = map[string]bool{"script": true, "style": true, "noscript": true}
	var walk func(*html.Node)
	walk = func(x *html.Node) {
		if x.Type == html.ElementNode && skip[x.Data] {
			return
		}
		if x.Type == html.TextNode {
			sb.WriteString(x.Data)
			sb.WriteByte(' ')
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return normWS(sb.String())
}

func normWS(s string) string {
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}

func (d *Doc) Text(sel string) (string, error) {
	n, err := d.queryOne(sel)
	if err != nil {
		return "", err
	}
	return innerText(n), nil
}

func (d *Doc) Serialize(w io.Writer) error {
	return html.Render(w, d.Root)
}

// ---- selectors (subset of CSS3 that agents actually use) -------------------
// Supported: tag, #id, .class, [attr], [attr=val], descendant combinator,
// child combinator '>', comma groups, :first-child, :not(tag).

type simpleSel struct {
	tag       string
	universal bool
	id        string
	classes   []string
	attrs     []kv
	notTag    string
	first     bool
}

type kv struct{ k, v string }

func parseSelector(sel string) [][]simpleSel {
	parts := splitTop(sel, ',')
	out := make([][]simpleSel, 0, len(parts))
	for _, p := range parts {
		out = append(out, parseCompoundChain(p))
	}
	return out
}

func splitTop(s string, sep byte) []string {
	var res []string
	depth := 0
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '[':
			depth++
		case ']':
			depth--
		case sep:
			if depth == 0 {
				res = append(res, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		}
	}
	res = append(res, strings.TrimSpace(s[start:]))
	return res
}

func parseCompoundChain(s string) []simpleSel {
	var chain []simpleSel
	for _, tk := range tokenizeCombinators(s) {
		if tk == "" || tk == ">" {
			continue
		}
		chain = append(chain, parseSimple(tk))
	}
	return chain
}

// tokenizeCombinators splits a selector into compound tokens, honoring
// descendant (whitespace) and child ('>') combinators. Bracketed attribute
// selectors and quoted values are kept intact.
func tokenizeCombinators(s string) []string {
	var out []string
	cur := strings.Builder{}
	depth := 0
	flush := func() {
		t := strings.TrimSpace(cur.String())
		if t != "" {
			out = append(out, t)
		}
		cur.Reset()
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '[', '(':
			depth++
		case ']', ')':
			if depth > 0 {
				depth--
			}
		case '"', '\'':
			// consume quoted run verbatim
			cur.WriteByte(c)
			i++
			for i < len(s) && s[i] != c {
				cur.WriteByte(s[i])
				i++
			}
			if i < len(s) {
				cur.WriteByte(s[i]) // closing quote
			}
			continue
		}
		if depth == 0 && (c == ' ' || c == '\t' || c == '>' || c == '+' || c == '~') {
			flush()
			continue
		}
		cur.WriteByte(c)
	}
	flush()
	return out
}

func parseSimple(s string) simpleSel {
	var sl simpleSel
	i := 0
	flush := func(buf string) {
		if buf == "" {
			return
		}
		switch buf[0] {
		case '#':
			sl.id = buf[1:]
		case '.':
			sl.classes = append(sl.classes, buf[1:])
		case '[':
			body := strings.TrimSuffix(buf[1:], "]")
			if eq := strings.SplitN(body, "=", 2); len(eq) == 2 {
				sl.attrs = append(sl.attrs, kv{eq[0], strings.Trim(eq[1], `"'`)})
			} else {
				sl.attrs = append(sl.attrs, kv{body, ""})
			}
		case ':':
			switch buf {
			case ":first-child":
				sl.first = true
			default:
				if strings.HasPrefix(buf, ":not(") {
					sl.notTag = strings.TrimSuffix(buf[5:], ")")
				}
			}
		default:
			sl.tag = strings.ToLower(buf)
		}
	}
	for i < len(s) {
		switch s[i] {
		case '#', '.', '[', ':':
			flush(s[:i]) // tag prefix, if any (only meaningful at start)
			j := i + 1
			if s[i] == '[' {
				for j < len(s) && s[j] != ']' {
					j++
				}
				if j < len(s) {
					j++ // include ']'
				}
			} else if s[i] == ':' {
				if j < len(s) && s[j] == ':' {
					j++ // pseudo-element '::xxx' — consume second colon
				}
				for j < len(s) && s[j] != '#' && s[j] != '.' && s[j] != '[' && s[j] != ':' {
					if s[j] == '(' { // :not(...) style functional pseudo
						for j < len(s) && s[j] != ')' {
							j++
						}
						if j < len(s) {
							j++
						}
						continue
					}
					j++
				}
			} else {
				for j < len(s) && s[j] != '#' && s[j] != '.' && s[j] != '[' && s[j] != ':' {
					j++
				}
			}
			parseRest := s[i:j]
			// handle bracket/pseudo content specially
			if strings.HasPrefix(parseRest, "[") {
				sl.attrs = append(sl.attrs, parseBracket(parseRest))
			} else if strings.HasPrefix(parseRest, ":") {
				switch {
				case strings.HasPrefix(parseRest, ":first-child"):
					sl.first = true
				case strings.HasPrefix(parseRest, ":not("):
					end := strings.Index(parseRest, ")")
					if end >= 0 {
						sl.notTag = parseRest[5:end]
					}
				}
			} else {
				flush(parseRest)
			}
			i = j
		default:
			i++
		}
	}
	if sl.tag == "" && s != "" && s[0] != '#' && s[0] != '.' && s[0] != '[' && s[0] != ':' {
		end := len(s)
		for k := 0; k < len(s); k++ {
			if s[k] == '#' || s[k] == '.' || s[k] == '[' || s[k] == ':' {
				end = k
				break
			}
		}
		flush(s[:end])
	}
	if s == "*" {
		sl.universal = true
		sl.tag = ""
	}
	return sl
}

func parseBracket(b string) kv {
	body := strings.TrimSuffix(strings.TrimPrefix(b, "["), "]")
	if eq := strings.SplitN(body, "=", 2); len(eq) == 2 {
		return kv{eq[0], strings.Trim(eq[1], `"'`)}
	}
	return kv{body, ""}
}

func matchSimple(n *html.Node, sl simpleSel) bool {
	if n.Type != html.ElementNode {
		return false
	}
	// "*" compiles to an empty simpleSel; honor it explicitly.
	if sl.universal && (sl.id != "" || len(sl.classes) > 0 || len(sl.attrs) > 0 || sl.notTag != "" || sl.first) {
		sl.universal = false // fall through to the other constraints below
	} else if sl.universal {
		return true
	}
	if sl.tag != "" && n.Data != sl.tag {
		return false
	}
	if sl.notTag != "" && n.Data == sl.notTag {
		return false
	}
	if sl.id != "" && attr(n, "id") != sl.id {
		return false
	}
	if len(sl.classes) > 0 {
		cls := " " + strings.ReplaceAll(attr(n, "class"), "\n", " ") + " "
		for _, c := range sl.classes {
			if !strings.Contains(cls, " "+c+" ") {
				return false
			}
		}
	}
	for _, a := range sl.attrs {
		v := attr(n, a.k)
		if a.v == "" && !hasAttr(n, a.k) {
			return false
		}
		if a.v != "" && v != a.v {
			return false
		}
	}
	if sl.first {
		p := n.Parent
		if p == nil || p.FirstChild != n {
			return false
		}
	}
	return true
}

// matchesChain checks last compound against node with ancestor walk for earlier ones.
func matchesChain(n *html.Node, chain []simpleSel) bool {
	if len(chain) == 0 {
		return false
	}
	if !matchSimple(n, chain[len(chain)-1]) {
		return false
	}
	cur := n.Parent
	for i := len(chain) - 2; i >= 0; i-- {
		for cur != nil {
			if matchSimple(cur, chain[i]) {
				cur = cur.Parent
				break
			}
			cur = cur.Parent
		}
		if cur == nil && i > 0 {
			return false
		}
	}
	return true
}

func (d *Doc) queryAll(sel string) []*html.Node {
	chains := parseSelector(sel)
	var out []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		for _, ch := range chains {
			if matchesChain(n, ch) {
				out = append(out, n)
				break
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(d.Root)
	return out
}

func (d *Doc) queryOne(sel string) (*html.Node, error) {
	all := d.queryAll(sel)
	if len(all) == 0 {
		return nil, fmt.Errorf("no node matches %q", sel)
	}
	return all[0], nil
}

func nodeToInfo(n *html.Node) *Info {
	role := implicitRole(n)
	txt := innerText(n)
	if txt == "" {
		txt = attr(n, "aria-label")
		if txt == "" {
			txt = attr(n, "placeholder")
		}
		if txt == "" {
			txt = attr(n, "alt")
		}
	}
	return &Info{
		NodeID:    nodeIDFor(n),
		BackendID: nodeIDFor(n),
		Name:      n.Data,
		ID:        attr(n, "id"),
		Type:      attr(n, "type"),
		Role:      role,
		Text:      txt,
		Href:      attr(n, "href"),
		Visible:   attr(n, "hidden") == "" && !hasAttr(n, "hidden"),
		Disabled:  hasAttr(n, "disabled"),
		Checked:   hasAttr(n, "checked"),
		Focusable: isFocusable(n),
	}
}

func isFocusable(n *html.Node) bool {
	switch n.Data {
	case "a", "button", "input", "select", "textarea":
		return !hasAttr(n, "disabled")
	}
	return hasAttr(n, "tabindex")
}

func implicitRole(n *html.Node) string {
	switch n.Data {
	case "a":
		if hasAttr(n, "href") {
			return "link"
		}
	case "button":
		return "button"
	case "input":
		switch attr(n, "type") {
		case "checkbox":
			return "checkbox"
		case "radio":
			return "radio"
		case "submit", "button":
			return "button"
		default:
			return "textbox"
		}
	case "select":
		return "combobox"
	case "textarea":
		return "textbox"
	case "h1", "h2", "h3", "h4", "h5", "h6":
		return "heading"
	case "nav":
		return "navigation"
	case "main":
		return "main"
	case "form":
		return "form"
	case "img":
		return "img"
	}
	return ""
}

// nodeIDFor returns a deterministic id derived from the element's path in the
// tree (tag + preceding-sibling index at each level), hashed with FNV-1a. It is
// stable across queries for the lifetime of one parsed document and needs zero
// extra memory — important on 50 MB machines.
func nodeIDFor(n *html.Node) int64 {
	var segs []string
	for x := n; x != nil && x.Type == html.ElementNode; x = x.Parent {
		cnt := 0
		if x.Parent != nil {
			for c := x.Parent.FirstChild; c != nil && c != x; c = c.NextSibling {
				if c.Type == html.ElementNode {
					cnt++
				}
			}
		}
		segs = append(segs, x.Data+":"+strconv.Itoa(cnt))
	}
	h := int64(1469598103934665603)
	for _, s := range segs {
		for i := 0; i < len(s); i++ {
			h ^= int64(s[i])
			h *= 1099511628211
		}
	}
	if h < 0 {
		h = -h
	}
	return h
}

// ---- attribute / state accessors (Playwright locator API surface) ---------

// GetAttribute returns the value of attr k on the first node matching sel.
// For input/textarea controls, "value" falls back to the live current value.
func (d *Doc) GetAttribute(backendID int64, k string) (string, bool, error) {
	n := d.find(backendID)
	if n == nil {
		return "", false, fmt.Errorf("no node with backendNodeId %d", backendID)
	}
	if v, ok := liveValue(n, k); ok {
		return v, true, nil
	}
	for _, a := range n.Attr {
		if a.Key == k {
			return a.Val, true, nil
		}
	}
	return "", false, nil
}

func liveValue(n *html.Node, k string) (string, bool) {
	if k != "value" || n.Type != html.ElementNode {
		return "", false
	}
	switch n.Data {
	case "input":
		return currentVal(n), true
	case "textarea":
		if n.FirstChild != nil {
			return n.FirstChild.Data, true
		}
		return "", true
	}
	return "", false
}

func (d *Doc) SetAttribute(backendID int64, k, v string) error {
	n := d.find(backendID)
	if n == nil {
		return fmt.Errorf("no node with backendNodeId %d", backendID)
	}
	if val, ok := liveValue(n, k); ok && (n.Data == "input" || n.Data == "textarea") {
		_ = val
		return d.SetValue(backendID, v)
	}
	setAttr(n, k, v)
	d.note(Mutation{Op: "set-attr", BackendID: backendID, Key: k, Value: v})
	return nil
}

func (d *Doc) RemoveAttribute(backendID int64, k string) error {
	n := d.find(backendID)
	if n == nil {
		return fmt.Errorf("no node with backendNodeId %d", backendID)
	}
	removeAttr(n, k)
	d.note(Mutation{Op: "remove-attr", BackendID: backendID, Key: k})
	return nil
}

func (d *Doc) InnerHTML(backendID int64) (string, error) {
	n := d.find(backendID)
	if n == nil {
		return "", fmt.Errorf("no node with backendNodeId %d", backendID)
	}
	var sb strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if err := html.Render(&sb, c); err != nil {
			return "", err
		}
	}
	return sb.String(), nil
}

func (d *Doc) OuterHTML(backendID int64) (string, error) {
	n := d.find(backendID)
	if n == nil {
		return "", fmt.Errorf("no node with backendNodeId %d", backendID)
	}
	var sb strings.Builder
	if err := html.Render(&sb, n); err != nil {
		return "", err
	}
	return sb.String(), nil
}

func (d *Doc) SetInnerHTML(backendID int64, frag string) error {
	n := d.find(backendID)
	if n == nil {
		return fmt.Errorf("no node with backendNodeId %d", backendID)
	}
	clearChildren(n)
	sub, err := html.ParseFragment(strings.NewReader(frag), &html.Node{Type: html.ElementNode, Data: "body"})
	if err != nil {
		return err
	}
	for _, s := range sub {
		n.AppendChild(s)
	}
	return nil
}

func (d *Doc) TextByBackend(backendID int64) (string, error) {
	n := d.find(backendID)
	if n == nil {
		return "", fmt.Errorf("no node with backendNodeId %d", backendID)
	}
	return innerText(n), nil
}

// InputTarget reports whether a backend node is a fillable form control and
// what kind (input/textarea/select).
func (d *Doc) InputTarget(backendID int64) (tag, typ string, isSelect bool, err error) {
	n := d.find(backendID)
	if n == nil {
		return "", "", false, fmt.Errorf("no node with backendNodeId %d", backendID)
	}
	switch n.Data {
	case "input", "textarea":
		return n.Data, attr(n, "type"), false, nil
	case "select":
		return n.Data, "", true, nil
	}
	return n.Data, "", false, fmt.Errorf("element <%s> cannot receive a value", n.Data)
}

// SelectOptions lists option values under a <select>.
func (d *Doc) SelectOptions(backendID int64) ([]string, error) {
	n := d.find(backendID)
	if n == nil || n.Data != "select" {
		return nil, fmt.Errorf("not a select element")
	}
	var out []string
	for o := n.FirstChild; o != nil; o = o.NextSibling {
		if o.Type == html.ElementNode && o.Data == "option" {
			v := attr(o, "value")
			if v == "" {
				v = innerText(o)
			}
			// Playwright semantics: return only *selected* options; if none
			// carry an explicit selected attribute the browser default is the
			// first option.
			if hasAttr(o, "selected") {
				out = append(out, v)
			}
		}
	}
	if len(out) == 0 {
		for o := n.FirstChild; o != nil; o = o.NextSibling {
			if o.Type == html.ElementNode && o.Data == "option" {
				v := attr(o, "value")
				if v == "" {
					v = innerText(o)
				}
				out = append(out, v)
				break
			}
		}
	}
	return out, nil
}

// SetValueByLabel matches <option> by visible text if no exact value match.
func (d *Doc) SelectOption(backendID int64, want string) error {
	n := d.find(backendID)
	if n == nil || n.Data != "select" {
		return fmt.Errorf("not a select element")
	}
	for o := n.FirstChild; o != nil; o = o.NextSibling {
		if o.Type != html.ElementNode || o.Data != "option" {
			continue
		}
		if attr(o, "value") == want || innerText(o) == want {
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				if c.Type == html.ElementNode && c.Data == "option" {
					removeAttr(c, "selected")
				}
			}
			setAttr(o, "selected", "")
			return nil
		}
	}
	return fmt.Errorf("no option matches %q", want)
}

// CheckState toggles checkbox/radio state in the tree.
func (d *Doc) SetChecked(backendID int64, checked bool) error {
	n := d.find(backendID)
	if n == nil {
		return fmt.Errorf("no node with backendNodeId %d", backendID)
	}
	switch attr(n, "type") {
	case "checkbox":
		if checked {
			setAttr(n, "checked", "")
		} else {
			removeAttr(n, "checked")
		}
		return nil
	case "radio":
		if !checked {
			return fmt.Errorf("radios cannot be unchecked by clicking")
		}
		group := attr(n, "name")
		for x := n.Parent; x != nil; x = x.Parent {
			if x.Type == html.ElementNode && x.Data == "form" {
				var walk func(*html.Node)
				walk = func(m *html.Node) {
					if m.Type == html.ElementNode && m.Data == "input" && attr(m, "type") == "radio" && attr(m, "name") == group {
						removeAttr(m, "checked")
					}
					for c := m.FirstChild; c != nil; c = c.NextSibling {
						walk(c)
					}
				}
				walk(x)
				break
			}
		}
		setAttr(n, "checked", "")
		return nil
	}
	return fmt.Errorf("element is not checkable (type=%q)", attr(n, "type"))
}

// IsVisible approximates CSS visibility: display:none anywhere in the
// ancestor chain (plus the hidden attribute) means invisible.
func (d *Doc) IsVisible(backendID int64) (bool, error) {
	n := d.find(backendID)
	if n == nil {
		return false, fmt.Errorf("no node with backendNodeId %d", backendID)
	}
	for x := n; x != nil && x.Type == html.ElementNode; x = x.Parent {
		if hasAttr(x, "hidden") {
			return false, nil
		}
		st := attr(x, "style")
		low := strings.ToLower(st)
		if strings.Contains(low, "display:none") || strings.Contains(low, "display: none") {
			return false, nil
		}
		if strings.Contains(low, "visibility:hidden") || strings.Contains(low, "visibility: hidden") {
			return false, nil
		}
	}
	return true, nil
}

// BoundingBox derives a deterministic pseudo-layout box from the node path so
// hover/click coordinates are stable across calls (agents use them for logs,
// not pixels — we are layout-free by design).
func (d *Doc) BoundingBox(backendID int64) ([4]int, error) {
	n := d.find(backendID)
	if n == nil {
		return [4]int{}, fmt.Errorf("no node with backendNodeId %d", backendID)
	}
	h := uint32(nodeIDFor(n))
	h ^= h >> 15
	h *= 2246822519
	h ^= h >> 13
	x := int(h%720) + 8
	y := int((h/720)%1400) + 8
	w := 40 + int(h%180)
	ht := 16 + int((h/181)%24)
	return [4]int{x, y, w, ht}, nil
}

// Count returns how many nodes match a selector (waitForSelector count rule).
func (d *Doc) Count(sel string) int { return len(d.queryAll(sel)) }

// Predicate evaluates a waiting condition against the current tree.
func (d *Doc) Predicate(kind, sel string, backendID int64) (bool, error) {
	switch kind {
	case "attached":
		if backendID != 0 {
			return d.find(backendID) != nil, nil
		}
		return d.Count(sel) > 0, nil
	case "detached":
		if backendID != 0 {
			return d.find(backendID) == nil, nil
		}
		return d.Count(sel) == 0, nil
	case "visible":
		id := backendID
		if id == 0 {
			n, err := d.queryOne(sel)
			if err != nil {
				return false, nil
			}
			id = nodeIDFor(n)
		}
		return d.IsVisible(id)
	case "hidden":
		vis, err := d.Predicate("visible", sel, backendID)
		if err != nil {
			return false, nil // treat vanished node as hidden
		}
		return !vis, nil
	case "enabled":
		id := backendID
		if id == 0 {
			n, err := d.queryOne(sel)
			if err != nil {
				return false, err
			}
			id = nodeIDFor(n)
		}
		n := d.find(id)
		if n == nil {
			return false, fmt.Errorf("no node with backendNodeId %d", backendID)
		}
		return !hasAttr(n, "disabled"), nil
	default:
		return false, fmt.Errorf("unknown predicate %q", kind)
	}
}

// find locates an element by its hashed path id — O(nodes), zero side tables.
func (d *Doc) find(backendID int64) *html.Node {
	var found *html.Node
	var walk func(*html.Node) bool
	walk = func(n *html.Node) bool {
		if n.Type == html.ElementNode && nodeIDFor(n) == backendID {
			found = n
			return true
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if walk(c) {
				return true
			}
		}
		return false
	}
	walk(d.Root)
	return found
}

func (d *Doc) QuerySelector(sel string) (*Info, error) {
	n, err := d.queryOne(sel)
	if err != nil {
		return nil, err
	}
	return nodeToInfo(n), nil
}

func (d *Doc) QuerySelectorAll(sel string) ([]*Info, error) {
	ns := d.queryAll(sel)
	out := make([]*Info, 0, len(ns))
	for _, n := range ns {
		out = append(out, nodeToInfo(n))
	}
	return out, nil
}

// AriaSnapshot walks the tree emitting only interesting interactive/content
// nodes — this is the token-cheap view LLMs consume.
func (d *Doc) AriaSnapshot() ([]*Info, error) {
	var out []*Info
	var walk func(*html.Node, int)
	walk = func(n *html.Node, depth int) {
		if n.Type == html.ElementNode {
			role := implicitRole(n)
			interesting := role != "" || n.Data == "p" || n.Data == "li" || n.Data == "td"
			if interesting {
				i := nodeToInfo(n)
				out = append(out, i)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c, depth+1)
		}
	}
	walk(d.Root, 0)
	if len(out) == 0 {
		return nil, fmt.Errorf("empty document")
	}
	return out, nil
}

// ARIAInfo carries one accessibility-tree entry for the YAML snapshot.
type ARIAInfo struct {
	Role      string
	Name      string // accessible name (aria-label > alt > placeholder > text)
	Checked   bool
	Disabled  bool
	Expanded  bool
	Selected  bool
	Href      string
	Value     string // input/textarea value
	Key       int    // stable backend node id for interaction
	Multiline bool
}

var ariaLandmarkRoles = map[string]string{
	"header": "banner", "footer": "contentinfo", "main": "main",
	"nav": "navigation", "aside": "complementary", "form": "form",
	"section": "region", "article": "article",
}

// explicitAriaRole returns a valid role="..." attribute value.
func explicitAriaRole(n *html.Node) string {
	r := strings.TrimSpace(strings.ToLower(attr(n, "role")))
	if r == "" || r == "generic" || r == "none" || r == "presentation" {
		return ""
	}
	return r
}

// accessibleName implements the core WAI-ARIA accname algorithm subset:
// aria-label > aria-labelledby > <label for> > alt > title > placeholder >
// button/link text.
func accessibleName(n *html.Node) string {
	if v := attr(n, "aria-label"); v != "" {
		return normWS(v)
	}
	if ids := attr(n, "aria-labelledby"); ids != "" {
		var parts []string
		for _, id := range strings.Fields(ids) {
			if ln := findByID(d_rootOf(n), id); ln != nil {
				parts = append(parts, innerText(ln))
			}
		}
		if s := normWS(strings.Join(parts, " ")); s != "" {
			return s
		}
	}
	if n.Data == "input" || n.Data == "textarea" || n.Data == "select" {
		if lab := labelForControl(n); lab != "" {
			return lab
		}
	}
	if v := attr(n, "alt"); v != "" && n.Data != "img" {
		return normWS(v)
	}
	if v := attr(n, "placeholder"); v != "" {
		return normWS(v)
	}
	switch n.Data {
	case "button", "a":
		return truncate(normWS(innerText(n)), 80)
	case "img":
		v := attr(n, "alt")
		if v == "" {
			v = "(image)"
		}
		return normWS(v)
	}
	if v := attr(n, "title"); v != "" {
		return normWS(v)
	}
	return ""
}

func d_rootOf(n *html.Node) *html.Node {
	for n.Parent != nil {
		n = n.Parent
	}
	return n
}

func findByID(root *html.Node, id string) *html.Node {
	var res *html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if res != nil || n.Type != html.ElementNode {
			return
		}
		if attr(n, "id") == id {
			res = n
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	return res
}

func controlID(n *html.Node) string { return attr(n, "id") }

// labelForControl finds <label for=id>, wrapping labels, or aria-labelledby-less
// adjacent label text.
func labelForControl(n *html.Node) string {
	root := d_rootOf(n)
	id := controlID(n)
	if id != "" {
		var lab *html.Node
		var walk func(*html.Node)
		walk = func(c *html.Node) {
			if lab != nil || c.Type != html.ElementNode {
				return
			}
			if c.Data == "label" && attr(c, "for") == id {
				lab = c
				return
			}
			for k := c.FirstChild; k != nil; k = k.NextSibling {
				walk(k)
			}
		}
		walk(root)
		if lab != nil {
			return truncate(normWS(innerText(lab)), 60)
		}
	}
	// wrapping label
	for p := n.Parent; p != nil; p = p.Parent {
		if p.Type == html.ElementNode && p.Data == "label" {
			return truncate(normWS(innerText(p)), 60)
		}
	}
	return ""
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func inputValue(n *html.Node) string {
	switch n.Data {
	case "textarea":
		return normWS(innerText(n))
	case "input":
		return attr(n, "value")
	case "select":
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Data == "option" && hasAttr(c, "selected") {
				return normWS(innerText(c))
			}
		}
	}
	return ""
}

// ariaNode classifies an element for the accessibility tree.
type ariaNode struct {
	role, name string
	ref        int64
	href       string
	value      string
	multiline  bool
	attrs      []string // level=N, checked, disabled, expanded, selected, type=password
	leaf       bool     // don't descend into children (button/link/textbox/img...)
	container  bool     // increases indentation depth for children
}

func classifyAria(n *html.Node) (*ariaNode, bool) {
	if n.Type != html.ElementNode {
		return nil, false
	}
	if attr(n, "aria-hidden") == "true" || hasAttr(n, "hidden") {
		return nil, false
	}
	st := strings.ToLower(attr(n, "style"))
	if strings.Contains(st, "display:none") || strings.Contains(st, "display: none") ||
		strings.Contains(st, "visibility:hidden") || strings.Contains(st, "visibility: hidden") {
		return nil, false
	}
	role := explicitAriaRole(n)
	if role == "" {
		role = implicitRole(n)
	}
	if role == "" {
		if lr, ok := ariaLandmarkRoles[n.Data]; ok {
			role = lr
		}
	}
	if len(n.Data) == 2 && n.Data[0] == 'h' && n.Data[1] >= '1' && n.Data[1] <= '6' && role == "" {
		role = "heading"
	}
	if role == "" {
		return nil, false
	}
	a := &ariaNode{role: role, ref: nodeIDFor(n), href: attr(n, "href")}
	name := accessibleName(n)
	if name == "" && (role == "paragraph" || role == "listitem" || role == "cell") {
		name = truncate(normWS(innerText(n)), 100)
	}
	a.name = name
	if len(n.Data) == 2 && n.Data[0] == 'h' && n.Data[1] >= '1' && n.Data[1] <= '6' {
		a.attrs = append(a.attrs, "level="+string(n.Data[1]))
	}
	if role == "checkbox" || role == "radio" || hasAttr(n, "checked") {
		if hasAttr(n, "checked") {
			a.attrs = append(a.attrs, "checked")
		}
	}
	if hasAttr(n, "disabled") {
		a.attrs = append(a.attrs, "disabled")
	}
	if attr(n, "aria-expanded") == "true" {
		a.attrs = append(a.attrs, "expanded")
	}
	if hasAttr(n, "selected") || attr(n, "aria-selected") == "true" {
		a.attrs = append(a.attrs, "selected")
	}
	if attr(n, "type") == "password" {
		a.attrs = append(a.attrs, "type=password")
	}
	if attr(n, "required") != "" {
		a.attrs = append(a.attrs, "required")
	}
	switch n.Data {
	case "input", "textarea":
		a.value = inputValue(n)
		a.multiline = n.Data == "textarea"
	case "select":
		a.value = inputValue(n)
	}
	switch role {
	case "button", "link", "textbox", "combobox", "listbox", "menuitem", "checkbox", "radio", "img", "separator", "slider", "progressbar", "option":
		a.leaf = true
	case "navigation", "banner", "main", "contentinfo", "complementary", "region", "form", "list", "table", "row", "grid", "tree", "tablist", "menu", "dialog", "article":
		a.container = true
	}
	return a, true
}

func yamlEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s)
}

// AriaSnapshotYAML renders the page as a Playwright-style accessibility tree in
// YAML — exactly the format AI agents consume to reason about and interact
// with pages:
//
// - heading "Pipilot" [level=1 ref=e3]
// - navigation [ref=e5]:
//   - link "Docs" [ref=e6]:
//   - /url: /docs
//
// - textbox "Email" [ref=e9]
// - button "Sign in" [ref=e12]
//
// Every node carries [ref=eN], its stable backend node id; pass it back to any
// action tool as selector "eN" (or css "#id") to click/fill/select it.
func (d *Doc) AriaSnapshotYAML() (string, error) {
	var b strings.Builder
	var walk func(n *html.Node, indent int)
	walk = func(n *html.Node, indent int) {
		childIndent := indent
		if a, ok := classifyAria(n); ok {
			pad := strings.Repeat("  ", indent)
			b.WriteString(pad)
			b.WriteString("- ")
			b.WriteString(a.role)
			if a.name != "" {
				b.WriteString(" \"" + yamlEscape(a.name) + "\"")
			}
			all := append([]string{}, a.attrs...)
			all = append(all, fmt.Sprintf("ref=e%d", a.ref))
			b.WriteString(" [" + strings.Join(all, " ") + "]")
			hasChildLines := a.href != "" || a.value != ""
			if hasChildLines {
				b.WriteString(":\n")
				if a.href != "" {
					b.WriteString(pad + "  - /url: " + a.href + "\n")
				}
				if a.value != "" {
					v := a.value
					if a.multiline {
						lines := strings.Split(v, "\n")
						b.WriteString(pad + "  - text: |\n")
						for _, l := range lines {
							b.WriteString(pad + "    " + l + "\n")
						}
					} else {
						b.WriteString(pad + "  - text: " + v + "\n")
					}
				}
			} else {
				b.WriteString("\n")
			}
			if !a.leaf {
				if a.container {
					childIndent = indent + 1
				}
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					walk(c, childIndent)
				}
			}
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c, indent)
		}
	}
	walk(d.Root, 0)
	out := b.String()
	if strings.TrimSpace(out) == "" {
		return "", fmt.Errorf("empty document")
	}
	return out, nil
}

func isContainer(*html.Node) bool { return false }
func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// SetValue writes value into input/textarea/select option matching.
func (d *Doc) SetValue(backendID int64, value string) error {
	var target *html.Node
	var walk func(*html.Node) bool
	walk = func(n *html.Node) bool {
		if n.Type == html.ElementNode && nodeIDFor(n) == backendID {
			target = n
			return true
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if walk(c) {
				return true
			}
		}
		return false
	}
	walk(d.Root)
	if target == nil {
		return fmt.Errorf("node %d vanished", backendID)
	}
	switch target.Data {
	case "input", "textarea":
		setAttr(target, "value", value)
		if target.Data == "textarea" {
			clearChildren(target)
			target.AppendChild(&html.Node{Type: html.TextNode, Data: value})
		}
		d.note(Mutation{Op: "set-value", BackendID: backendID, Value: value})
	case "select":
		for c := target.FirstChild; c != nil; c = c.NextSibling {
			if c.Data == "option" {
				if normWS(innerText(c)) == value || attr(c, "value") == value {
					setAttr(c, "selected", "")
				} else {
					removeAttr(c, "selected")
				}
			}
		}
		d.note(Mutation{Op: "select", BackendID: backendID, Value: value})
	default:
		return fmt.Errorf("cannot set value on <%s>", target.Data)
	}
	return nil
}

// AppendHTML parses frag and appends it as children of the element matching
// sel (Playwright locator.evaluateHandle insertAdjacentHTML('beforeend')).
func (d *Doc) AppendHTML(sel, frag string) error {
	n, err := d.queryOne(sel)
	if err != nil {
		return err
	}
	sub, err := html.ParseFragment(strings.NewReader(frag), n)
	if err != nil {
		return err
	}
	for _, s := range sub {
		n.AppendChild(s)
	}
	d.note(Mutation{Op: "inner-html", BackendID: nodeIDFor(n), Value: frag})
	return nil
}

// RemoveNode detaches the element matching sel from its parent.
func (d *Doc) RemoveNode(sel string) error {
	n, err := d.queryOne(sel)
	if err != nil {
		return err
	}
	if n.Parent != nil {
		n.Parent.RemoveChild(n)
	}
	d.note(Mutation{Op: "remove", BackendID: nodeIDFor(n)})
	return nil
}

// DispatchEvent is a semantic stand-in for page.dispatchEvent: Sweef's
// engine has no JS event listeners in pure-Go builds, so we translate the
// events agents actually rely on into tree mutations that keep state coherent.
func (d *Doc) DispatchEvent(backendID int64, typ string, detail string) error {
	n := d.find(backendID)
	if n == nil {
		return fmt.Errorf("no node with backendNodeId %d", backendID)
	}
	switch typ {
	case "input", "change":
		if n.Data == "input" || n.Data == "textarea" {
			if detail != "" {
				return d.SetValue(backendID, detail)
			}
			d.note(Mutation{Op: "event", BackendID: backendID, Key: typ})
		}
	case "focus", "blur", "click", "mousedown", "mouseup", "keydown", "keyup":
		d.note(Mutation{Op: "event", BackendID: backendID, Key: typ, Value: detail})
	default:
		d.note(Mutation{Op: "event", BackendID: backendID, Key: typ})
	}
	return nil
}

func setAttr(n *html.Node, k, v string) {
	for i := range n.Attr {
		if n.Attr[i].Key == k {
			n.Attr[i].Val = v
			return
		}
	}
	n.Attr = append(n.Attr, html.Attribute{Key: k, Val: v})
}

func removeAttr(n *html.Node, k string) {
	out := n.Attr[:0]
	for _, a := range n.Attr {
		if a.Key != k {
			out = append(out, a)
		}
	}
	n.Attr = out
}

func clearChildren(n *html.Node) {
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling
		n.RemoveChild(c)
		c = next
	}
}

// FormFor finds the enclosing form of sel and collects name=value pairs.
func (d *Doc) FormFor(sel string) (method, action string, values map[string]string, err error) {
	n, err := d.queryOne(sel)
	if err != nil {
		return
	}
	form := n
	for form != nil && !(form.Type == html.ElementNode && form.Data == "form") {
		form = form.Parent
	}
	values = map[string]string{}
	method = "get"
	action = ""
	if form != nil {
		method = attr(form, "method")
		if method == "" {
			method = "get"
		}
		action = attr(form, "action")
		collectControls(form, values)
		return
	}
	// no form: just this control if named
	name := attr(n, "name")
	if name != "" {
		values[name] = currentVal(n)
	}
	return
}

func collectControls(form *html.Node, values map[string]string) {
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			name := attr(n, "name")
			switch n.Data {
			case "input":
				if name != "" {
					switch attr(n, "type") {
					case "checkbox", "radio":
						if hasAttr(n, "checked") {
							values[name] = attr(n, "value")
							if values[name] == "" {
								values[name] = "on"
							}
						}
					case "submit", "button", "reset":
					default:
						values[name] = currentVal(n)
					}
				}
			case "textarea":
				if name != "" {
					values[name] = innerText(n)
				}
			case "select":
				if name != "" {
					for c := n.FirstChild; c != nil; c = c.NextSibling {
						if c.Data == "option" && hasAttr(c, "selected") {
							v := attr(c, "value")
							if v == "" {
								v = innerText(c)
							}
							values[name] = v
						}
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(form)
}

func currentVal(n *html.Node) string {
	v := attr(n, "value")
	return v
}

// Info mirrors engine.NodeInfo without import cycle (engine converts).
type Info struct {
	NodeID    int64  `json:"nodeId"`
	BackendID int64  `json:"backendNodeId"`
	Name      string `json:"name"`
	ID        string `json:"id,omitempty"`
	Type      string `json:"type,omitempty"`
	Role      string `json:"role,omitempty"`
	Text      string `json:"text,omitempty"`
	Href      string `json:"href,omitempty"`
	Visible   bool   `json:"visible"`
	Disabled  bool   `json:"disabled"`
	Checked   bool   `json:"checked,omitempty"`
	Focusable bool   `json:"focusable"`
}

// CompiledScripts is mirrored here to avoid engine->net cycle noise.
type Script struct {
	Source string
	Name   string
}

func (d *Doc) Scripts() []Script {
	var out []Script
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "script" {
			src := attr(n, "src")
			if src != "" {
				out = append(out, Script{Name: src})
			} else if n.FirstChild != nil {
				out = append(out, Script{Source: n.FirstChild.Data, Name: "<inline>"})
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(d.Root)
	return out
}

// RenderText draws a crude but useful text-mode page render.
func (d *Doc) RenderText(width int) string {
	var lines []string
	wrap := func(prefix, s string) {
		s = normWS(s)
		if s == "" {
			return
		}
		for len(s) > width {
			cut := width
			if i := strings.LastIndexByte(s[:cut], ' '); i > 0 {
				cut = i
			}
			lines = append(lines, prefix+s[:cut])
			s = strings.TrimLeft(s[cut:], " ")
		}
		lines = append(lines, prefix+s)
	}
	var walk func(*html.Node, int)
	walk = func(n *html.Node, depth int) {
		ind := strings.Repeat("  ", depth)
		switch {
		case n.Type == html.ElementNode && strings.HasPrefix(n.Data, "h"):
			wrap(ind+"# ", innerText(n))
			return
		case n.Type == html.ElementNode && n.Data == "li":
			wrap(ind+"- ", innerText(n))
			return
		case n.Type == html.ElementNode && n.Data == "a" && hasAttr(n, "href"):
			wrap(ind+"→ ", innerText(n)+" ("+attr(n, "href")+")")
			return
		case n.Type == html.ElementNode && (n.Data == "input" || n.Data == "textarea" || n.Data == "select"):
			label := attr(n, "placeholder")
			if label == "" {
				label = attr(n, "name")
			}
			wrap(ind+"[", fmt.Sprintf("%s: %s]", n.Data, label))
			return
		case n.Type == html.ElementNode && n.Data == "button":
			wrap(ind+"( ", innerText(n)+" )")
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c, depth)
		}
	}
	walk(d.Root, 0)
	return strings.Join(lines, "\n")
}

// ---- network records + routes ---------------------------------------------

type RequestRecord struct {
	Method          string            `json:"request.method"`
	URL             string            `json:"request.url"`
	FinalURL        string            `json:"response.url,omitempty"`
	Status          int               `json:"response.status"`
	Headers         map[string]string `json:"request.headers,omitempty"`
	ResponseHeaders map[string]string `json:"response.headers,omitempty"`
	DurationMs      int64             `json:"time"`
}

// Route implements Playwright-style interception: glob patterns matched on URL
// path; handler returns (status, body, handled).
type Route struct {
	Pattern string
	Handler func(*http.Request) (int, string, bool)
}

func NewRoute(glob string, h func(*http.Request) (int, string, bool)) *Route {
	return &Route{Pattern: glob, Handler: h}
}

func (r *Route) Match(u string) bool {
	parsed, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return false
	}
	ok, _ := path.Match(r.Pattern, parsed.URL.Path)
	if ok {
		return true
	}
	return strings.Contains(u, strings.Trim(r.Pattern, "*"))
}

// MatchRoute runs a route's handler against a request if the URL matches.
func MatchRoute(r *Route, req *http.Request) (int, string, bool) {
	if r == nil || r.Handler == nil || !r.Match(req.URL.String()) {
		return 0, "", false
	}
	code, body, handled := r.Handler(req)
	return code, body, handled
}

// InfoByBackend returns the serialisable node info for a backend id, or nil.
func (d *Doc) InfoByBackend(backendID int64) *Info {
	n := d.find(backendID)
	if n == nil {
		return nil
	}
	return nodeToInfo(n)
}

// BackendForSelector resolves a selector to its stable backend node id.
func (d *Doc) BackendForSelector(sel string) (int64, error) {
	n, err := d.queryOne(sel)
	if err != nil {
		return 0, err
	}
	return nodeIDFor(n), nil
}

// NthForSelector resolves the nth (0-based) match of a selector.
func (d *Doc) NthForSelector(sel string, nth int) (int64, error) {
	all := d.queryAll(sel)
	if nth < 0 || nth >= len(all) {
		return 0, fmt.Errorf("no node matches %q at index %d", sel, nth)
	}
	return nodeIDFor(all[nth]), nil
}

// ---- Playwright-style locator engines --------------------------------------
// These extend plain CSS with the semantic selectors agents use most. They
// run as post-filters over queryAll results, so they cost zero extra memory.

// ByText returns nodes whose normalised inner text equals (or contains) s.
func (d *Doc) ByText(s string, exact bool) []*Info {
	var all []*html.Node
	for _, n := range d.queryAll("*") {
		t := innerText(n)
		if (exact && t == s) || (!exact && strings.Contains(strings.ToLower(t), strings.ToLower(s))) {
			all = append(all, n)
		}
	}
	// Like Playwright: keep only the deepest matching elements so a text
	// locator resolves to the anchor/leaf, not every ancestor.
	var out []*Info
	for _, n := range all {
		hasMatchChild := false
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			for _, m := range all {
				if htmlSubtreeContains(c, m) {
					hasMatchChild = true
					break
				}
			}
			if hasMatchChild {
				break
			}
		}
		if !hasMatchChild {
			out = append(out, nodeToInfo(n))
		}
	}
	return out
}

func htmlSubtreeContains(root, n *html.Node) bool {
	for x := n; x != nil; x = x.Parent {
		if x == root {
			return true
		}
	}
	return false
}

// ByRole returns nodes whose computed ARIA role matches role, optionally
// filtered by accessible name (text/aria-label).
func (d *Doc) ByRole(role, name string) []*Info {
	var out []*Info
	for _, n := range d.queryAll("*") {
		r := attr(n, "role")
		if r == "" {
			r = implicitRole(n)
		}
		if r != role {
			continue
		}
		if name != "" {
			// Match against the WAI-ARIA accessible name (aria-label,
			// labelledby, <label>, placeholder, title, text content...),
			// falling back to visible text — like Playwright's getByRole({name}).
			an := accessibleName(n)
			if an == "" {
				an = innerText(n)
			}
			want := strings.ToLower(strings.TrimSpace(name))
			got := strings.ToLower(strings.TrimSpace(an))
			exact := false
			if strings.HasPrefix(name, "i:") {
				// no-op, keep case-insensitive always for now
			}
			if exact {
				if got != want {
					continue
				}
			} else if !strings.Contains(got, want) && got != want {
				continue
			}
		}
		out = append(out, nodeToInfo(n))
	}
	return out
}

// ByLabel matches form controls by their associated <label for=>, aria-label,
// placeholder or title.
func (d *Doc) ByLabel(label string) []*Info {
	var out []*Info
	for _, n := range d.queryAll("input, textarea, select, button") {
		l := attr(n, "aria-label")
		if l == "" {
			l = attr(n, "placeholder")
		}
		if l == "" {
			l = attr(n, "title")
		}
		if l == "" {
			id := attr(n, "id")
			if id != "" {
				for _, ln := range d.queryAll("label[for=" + id + "]") {
					l = innerText(ln)
					break
				}
			}
		}
		if strings.Contains(strings.ToLower(l), strings.ToLower(label)) {
			out = append(out, nodeToInfo(n))
		}
	}
	return out
}

// ByPlaceholder matches controls by placeholder text.
func (d *Doc) ByPlaceholder(p string) []*Info {
	var out []*Info
	for _, n := range d.queryAll("input, textarea") {
		if strings.Contains(strings.ToLower(attr(n, "placeholder")), strings.ToLower(p)) {
			out = append(out, nodeToInfo(n))
		}
	}
	return out
}

// ByAlt matches images by alt text.
func (d *Doc) ByAlt(a string) []*Info {
	var out []*Info
	for _, n := range d.queryAll("img") {
		if strings.Contains(strings.ToLower(attr(n, "alt")), strings.ToLower(a)) {
			out = append(out, nodeToInfo(n))
		}
	}
	return out
}

// ByTestId matches data-testid / data-test / test-id attributes.
func (d *Doc) ByTestId(id string) []*Info {
	var out []*Info
	for _, n := range d.queryAll("*") {
		v := attr(n, "data-testid")
		if v == "" {
			v = attr(n, "data-test")
		}
		if v == "" {
			v = attr(n, "test-id")
		}
		if v == id {
			out = append(out, nodeToInfo(n))
		}
	}
	return out
}

// ResolveEngine parses Playwright selector syntax: css default, plus
// text=..., text="exact", role=button[name="Ok"], label=..., placeholder=...,
// alt=..., testid=..., and engine prefixes css=/xpath-less variants.
func (d *Doc) ResolveEngine(sel string) ([]*Info, error) {
	s := strings.TrimSpace(sel)
	// ref handles from AriaSnapshotYAML: "e42", "ref=e42", or "backend://e42"
	refStr := ""
	switch {
	case strings.HasPrefix(s, "backend://"):
		refStr = strings.TrimPrefix(s, "backend://")
	case strings.HasPrefix(s, "ref="):
		refStr = strings.TrimPrefix(s, "ref=")
	case len(s) > 1 && s[0] == 'e' && s[1] >= '0' && s[1] <= '9':
		refStr = s[1:]
	}
	if refStr != "" {
		var id int64
		if _, err := fmt.Sscanf(refStr, "%d", &id); err == nil {
			if info := d.InfoByBackend(id); info != nil {
				return []*Info{info}, nil
			}
			return nil, fmt.Errorf("ref %s not found (stale snapshot?)", s)
		}
	}
	switch {
	case strings.HasPrefix(s, "text=is:"):
		return d.ByText(strings.TrimPrefix(s, "text=is:"), false), nil
	case strings.HasPrefix(s, "text=:"):
		return d.ByText(strings.TrimPrefix(s, "text=:"), true), nil
	case strings.HasPrefix(s, "text="):
		return d.ByText(strings.TrimPrefix(s, "text="), false), nil
	case strings.HasPrefix(s, "role="):
		body := strings.TrimPrefix(s, "role=")
		role, name := body, ""
		if i := strings.Index(body, "[name="); i >= 0 {
			role = body[:i]
			name = strings.Trim(body[i+len("[name="):], `"]`)
		}
		return d.ByRole(role, name), nil
	case strings.HasPrefix(s, "label="):
		return d.ByLabel(strings.TrimPrefix(s, "label=")), nil
	case strings.HasPrefix(s, "placeholder="):
		return d.ByPlaceholder(strings.TrimPrefix(s, "placeholder=")), nil
	case strings.HasPrefix(s, "alt="):
		return d.ByAlt(strings.TrimPrefix(s, "alt=")), nil
	case strings.HasPrefix(s, "testid="):
		return d.ByTestId(strings.TrimPrefix(s, "testid=")), nil
	case strings.HasPrefix(s, "css="):
		s = strings.TrimPrefix(s, "css=")
	}
	var out []*Info
	for _, n := range d.queryAll(s) {
		out = append(out, nodeToInfo(n))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no node matches %q", sel)
	}
	return out, nil
}

// BackendByEngine resolves a Playwright-style selector to the first backend id.
func (d *Doc) BackendByEngine(sel string) (int64, string, error) {
	infos, err := d.ResolveEngine(sel)
	if err != nil {
		return 0, "", err
	}
	return infos[0].BackendID, sel, nil
}
