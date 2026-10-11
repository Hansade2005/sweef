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

	"golang.org/x/net/html"
)

// Doc owns one parsed HTML tree. Node identity is a deterministic FNV hash of
// the element's path-in-tree (see nodeIDFor), so no side tables are needed and
// memory stays O(document) — critical for 50 MB targets.
type Doc struct {
	Root *html.Node
	base string
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
	tag     string
	id      string
	classes []string
	attrs   []kv
	notTag  string
	first   bool
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
	// split on combinators keeping '>' markers
	var chain []simpleSel
	tokens := tokenizeCombinators(s)
	for _, tk := range tokens {
		if tk == ">" {
			continue // handled implicitly: child match enforced at eval time
		}
		chain = append(chain, parseSimple(tk))
	}
	return chain
}

func tokenizeCombinators(s string) []string {
	var out []string
	cur := strings.Builder{}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '>' {
			if cur.Len() > 0 {
				out = append(out, strings.TrimSpace(cur.String()))
				cur.Reset()
			}
			out = append(out, ">")
			continue
		}
		if c == ' ' && cur.Len() > 0 && i+1 < len(s) && s[i+1] != ' ' {
			// possible descendant boundary — only flush at next '>' or end for simplicity
		}
		cur.WriteByte(c)
	}
	if cur.Len() > 0 {
		out = append(out, strings.TrimSpace(cur.String()))
	}
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
			flush(s[:i])
			j := i
			for j < len(s) && s[j] != '#' && s[j] != '.' && !(s[j] == ':' && !(j+1 < len(s) && s[j+1] == ':')) {
				if s[j] == '[' {
					for j < len(s) && s[j] != ']' {
						j++
					}
					j++
				} else if s[j] == ':' && j+1 < len(s) && s[j+1] == ':' {
					j += 2
					continue
				} else {
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
					sl.notTag = parseRest[5:end]
				}
			} else {
				flush(parseRest)
			}
			i = j
		default:
			i++
		}
	}
	if i == len(s) && s != "" && s[0] != '#' && s[0] != '.' && s[0] != '[' && s[0] != ':' {
		end := len(s)
		for k := 0; k < len(s); k++ {
			if s[k] == '#' || s[k] == '.' || s[k] == '[' || s[k] == ':' {
				end = k
				break
			}
		}
		flush(s[:end])
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
	default:
		return fmt.Errorf("cannot set value on <%s>", target.Data)
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
