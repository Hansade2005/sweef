package engine

// devtools_dom.go: the Elements panel's view of a tab: the DOM tree, the CSS rules that match an
// element, its computed style, its box model and the page highlight, plus live edits of attributes,
// text and inline style.
//
// All of it reads the tab's own document; nothing is sent to the network. The functions touch the DOM
// and the layout, so the caller runs them where the tab's JavaScript runs (the app uses the tab's
// worker), like every other call that reads page state.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"golang.org/x/net/html"

	fnet "github.com/sweefbrowser/sweef/internal/net"
	"github.com/sweefbrowser/sweef/internal/render"
	"github.com/sweefbrowser/sweef/internal/style"
)

// DOMNode is one node of the tree shown in the Elements panel.
type DOMNode struct {
	ID       int64       `json:"id"`
	Type     int         `json:"type"` // DOM nodeType: 1 element, 3 text, 8 comment, 9 document, 10 doctype
	Name     string      `json:"name"` // tag name (lower case) or #text, #comment, #document
	Attrs    [][2]string `json:"attrs,omitempty"`
	Value    string      `json:"value,omitempty"` // text, comment and doctype data (cut at 2000 bytes)
	Kids     int         `json:"kids"`            // children that appear in the tree (blank text nodes do not)
	Children []DOMNode   `json:"children,omitempty"`
	Frame    bool        `json:"frame,omitempty"` // an iframe: its document is separate and not shown
}

const devMaxText = 2000

func (t *Tab) devDoc() (*fnet.Doc, error) {
	t.mu.RLock()
	d := t.dom
	t.mu.RUnlock()
	if d == nil {
		return nil, ErrNotFound
	}
	return d, nil
}

// devInternal marks the elements the browser itself adds to a page (the scrollbar style): Developer Tools does not show them.
func devInternal(n *html.Node) bool {
	return n.Type == html.ElementNode && n.Data == "style" && hasDevAttr(n, "data-sweef-internal")
}

func hasDevAttr(n *html.Node, k string) bool {
	for _, a := range n.Attr {
		if a.Key == k {
			return true
		}
	}
	return false
}

// devInlineStyles is the text of every <style> element of the document in tree order (the page's own).
func devInlineStyles(d *fnet.Doc) []string {
	var out []string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "style" {
			if !devInternal(n) {
				var sb strings.Builder
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					if c.Type == html.TextNode {
						sb.WriteString(c.Data)
					}
				}
				out = append(out, sb.String())
			}
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(d.Root)
	return out
}

func devVisibleKid(n *html.Node) bool {
	if devInternal(n) {
		return false
	}
	switch n.Type {
	case html.TextNode:
		return strings.TrimSpace(n.Data) != ""
	case html.ElementNode, html.CommentNode, html.DoctypeNode:
		return true
	}
	return false
}

func devKids(n *html.Node) []*html.Node {
	var out []*html.Node
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if devVisibleKid(c) {
			out = append(out, c)
		}
	}
	return out
}

func devNode(d *fnet.Doc, n *html.Node, depth int) DOMNode {
	out := DOMNode{ID: d.IDOf(n)}
	switch n.Type {
	case html.DocumentNode:
		out.Type, out.Name = 9, "#document"
	case html.ElementNode:
		out.Type, out.Name = 1, n.Data
		for _, a := range n.Attr {
			k := a.Key
			if a.Namespace != "" {
				k = a.Namespace + ":" + a.Key
			}
			out.Attrs = append(out.Attrs, [2]string{k, a.Val})
		}
		out.Frame = n.Data == "iframe" || n.Data == "frame"
	case html.TextNode:
		out.Type, out.Name, out.Value = 3, "#text", devCut(n.Data, devMaxText)
	case html.CommentNode:
		out.Type, out.Name, out.Value = 8, "#comment", devCut(n.Data, devMaxText)
	case html.DoctypeNode:
		out.Type, out.Name, out.Value = 10, "#doctype", n.Data
	}
	kids := devKids(n)
	out.Kids = len(kids)
	// a lone text child always comes along: the panel shows it inline, <h1>Title</h1>, without a request
	if depth > 0 || (n.Type == html.ElementNode && len(kids) == 1 && kids[0].Type == html.TextNode) {
		for _, k := range kids {
			out.Children = append(out.Children, devNode(d, k, depth-1))
		}
	}
	return out
}

func devCut(s string, n int) string {
	if len(s) > n {
		return strings.ToValidUTF8(s[:n], "") + "..."
	}
	return s
}

// DevDOMRoot returns the document with its first levels expanded (down to <body>'s children).
func (t *Tab) DevDOMRoot() (DOMNode, error) {
	d, err := t.devDoc()
	if err != nil {
		return DOMNode{}, err
	}
	return devNode(d, d.Root, 4), nil
}

// DevDOMChildren returns the children of a node, depth levels deep (0: just the children).
func (t *Tab) DevDOMChildren(id int64, depth int) ([]DOMNode, error) {
	d, err := t.devDoc()
	if err != nil {
		return nil, err
	}
	n := d.NodeByID(id)
	if n == nil {
		return nil, ErrNotFound
	}
	var out []DOMNode
	for _, k := range devKids(n) {
		out = append(out, devNode(d, k, depth))
	}
	return out, nil
}

// DevDOMPath returns the ids from the document down to the node, so the panel can expand to it.
func (t *Tab) DevDOMPath(id int64) ([]int64, error) {
	d, err := t.devDoc()
	if err != nil {
		return nil, err
	}
	n := d.NodeByID(id)
	if n == nil {
		return nil, ErrNotFound
	}
	var path []int64
	for p := n; p != nil; p = p.Parent {
		path = append([]int64{d.IDOf(p)}, path...)
	}
	return path, nil
}

// DevNodeAt returns the element at a point of the page (CSS px in the viewport), for "Inspect".
func (t *Tab) DevNodeAt(x, y int) (int64, error) { return t.hitTest(x, y) }

// DevDOMNode returns one node with its attributes (no children).
func (t *Tab) DevDOMNode(id int64) (DOMNode, error) {
	d, err := t.devDoc()
	if err != nil {
		return DOMNode{}, err
	}
	n := d.NodeByID(id)
	if n == nil {
		return DOMNode{}, ErrNotFound
	}
	return devNode(d, n, 0), nil
}

// ---- edits ---------------------------------------------------------------------------------------------

// devViaJS runs an edit in the page's JavaScript, as page script would make it: MutationObservers and custom
// elements see it. ran is false when the tab has no JavaScript (the caller edits the tree directly then).
func (t *Tab) devViaJS(js string) (ran bool, err error) {
	if _, jerr := t.ensureJS(); jerr != nil {
		return false, nil
	}
	if _, err := t.Evaluate(js); err != nil {
		return true, err
	}
	return true, nil
}

func devJSStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// DevSetAttr sets an attribute of an element (adds it when new).
func (t *Tab) DevSetAttr(id int64, name, value string) error {
	d, err := t.devDoc()
	if err != nil {
		return err
	}
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsAny(name, " \t\n\"'<>/=") {
		return fmt.Errorf("invalid attribute name %q", name)
	}
	n := d.NodeByID(id)
	if n == nil || n.Type != html.ElementNode {
		return ErrNotFound
	}
	if ran, err := t.devViaJS("__swDevNode(" + itoa(int(id)) + ").setAttribute(" + devJSStr(name) + "," + devJSStr(value) + ")"); ran {
		return err
	}
	d.SetAttr(id, name, value)
	return nil
}

// DevRemoveAttr removes an attribute.
func (t *Tab) DevRemoveAttr(id int64, name string) error {
	d, err := t.devDoc()
	if err != nil {
		return err
	}
	if n := d.NodeByID(id); n == nil || n.Type != html.ElementNode {
		return ErrNotFound
	}
	if ran, err := t.devViaJS("__swDevNode(" + itoa(int(id)) + ").removeAttribute(" + devJSStr(name) + ")"); ran {
		return err
	}
	d.RemoveAttr(id, name)
	return nil
}

// DevSetText replaces the data of a text or comment node.
func (t *Tab) DevSetText(id int64, text string) error {
	d, err := t.devDoc()
	if err != nil {
		return err
	}
	n := d.NodeByID(id)
	if n == nil || (n.Type != html.TextNode && n.Type != html.CommentNode) {
		return ErrNotFound
	}
	if ran, err := t.devViaJS("__swDevNode(" + itoa(int(id)) + ").data = " + devJSStr(text)); ran {
		return err
	}
	d.SetNodeValue(id, text)
	return nil
}

// DevRemoveNode deletes a node from the document (not the document, <html>, <head> or <body>).
func (t *Tab) DevRemoveNode(id int64) error {
	d, err := t.devDoc()
	if err != nil {
		return err
	}
	n := d.NodeByID(id)
	if n == nil || n.Parent == nil {
		return ErrNotFound
	}
	if n.Type == html.ElementNode && (n.Data == "html" || n.Data == "head" || n.Data == "body") {
		return fmt.Errorf("the %s element cannot be deleted", n.Data)
	}
	if ran, err := t.devViaJS("__swDevNode(" + itoa(int(id)) + ").remove()"); ran {
		return err
	}
	return d.RemoveChild(d.IDOf(n.Parent), id)
}

// ---- highlight -----------------------------------------------------------------------------------------

// DevHighlight marks an element on the page with the box-model overlay (0 clears it) and reports whether it changed.
// The overlay is drawn into the page's display list by PaintRGBAOpts; it is never part of the document.
func (t *Tab) DevHighlight(id int64) bool {
	if old := t.devHL.Swap(id); old == id {
		return false
	}
	if d, err := t.devDoc(); err == nil {
		d.TouchPaint()
	}
	return true
}

var (
	hlContent = style.Color{R: 111, G: 168, B: 220, A: 168}
	hlPadding = style.Color{R: 147, G: 196, B: 125, A: 140}
	hlBorder  = style.Color{R: 255, G: 229, B: 153, A: 168}
	hlMargin  = style.Color{R: 246, G: 178, B: 107, A: 168}
)

// highlightOps builds the overlay for a node, or nil.
func (t *Tab) highlightOps(res *render.Result, doc *fnet.Doc) []render.Op {
	id := t.devHL.Load()
	if id == 0 || res == nil {
		return nil
	}
	n := doc.NodeByID(id)
	if n == nil {
		return nil
	}
	b := res.Boxes[n]
	if b == nil || !b.Visible {
		return nil
	}
	mg := render.Rect{X: b.Border.X - b.Margin[3], Y: b.Border.Y - b.Margin[0],
		W: b.Border.W + b.Margin[1] + b.Margin[3], H: b.Border.H + b.Margin[0] + b.Margin[2]}
	var ops []render.Op
	ops = devRingOps(ops, mg, b.Border, hlMargin)
	ops = devRingOps(ops, b.Border, b.Padding, hlBorder)
	ops = devRingOps(ops, b.Padding, b.Content, hlPadding)
	ops = append(ops, render.FillRect{Rect: b.Content, Color: hlContent})
	return ops
}

// ringOps fills the area between the outer and the inner rectangle.
func devRingOps(ops []render.Op, o, i render.Rect, c style.Color) []render.Op {
	if o.W <= 0 || o.H <= 0 {
		return ops
	}
	add := func(r render.Rect) {
		if r.W > 0 && r.H > 0 {
			ops = append(ops, render.FillRect{Rect: r, Color: c})
		}
	}
	add(render.Rect{X: o.X, Y: o.Y, W: o.W, H: i.Y - o.Y})
	add(render.Rect{X: o.X, Y: i.Y + i.H, W: o.W, H: o.Y + o.H - (i.Y + i.H)})
	add(render.Rect{X: o.X, Y: i.Y, W: i.X - o.X, H: i.H})
	add(render.Rect{X: i.X + i.W, Y: i.Y, W: o.X + o.W - (i.X + i.W), H: i.H})
	return ops
}

// ---- style ---------------------------------------------------------------------------------------------

// DevDecl is one declaration of a rule.
type DevDecl struct {
	Name       string `json:"name"`
	Value      string `json:"value"`
	Important  bool   `json:"important,omitempty"`
	Overridden bool   `json:"overridden,omitempty"` // a rule of higher priority sets the property too
}

// DevRule is a rule that matches the selected element.
type DevRule struct {
	Selector string    `json:"selector"`
	Source   string    `json:"source"` // address or "<style> 2" or "element.style"
	Line     int       `json:"line,omitempty"`
	AtRules  []string  `json:"at,omitempty"` // @media / @supports conditions it sits in
	Inline   bool      `json:"inline,omitempty"`
	Decls    []DevDecl `json:"decls"`
	spec     int
	order    int
}

// DevBox is the box model of an element, in CSS px.
type DevBox struct {
	Margin  [4]float64 `json:"margin"` // top right bottom left
	Border  [4]float64 `json:"border"`
	Padding [4]float64 `json:"padding"`
	W       float64    `json:"w"` // content box
	H       float64    `json:"h"`
	X       float64    `json:"x"` // border box position in the document
	Y       float64    `json:"y"`
	BW      float64    `json:"bw"` // border box size
	BH      float64    `json:"bh"`
}

// DevStyle is everything the Styles / Computed panes show for one element.
type DevStyle struct {
	Rules    []DevRule   `json:"rules"`
	Computed [][2]string `json:"computed"`
	Box      *DevBox     `json:"box,omitempty"`
	Hidden   bool        `json:"hidden,omitempty"` // display:none: no box
}

// DevStyle returns the matched rules, the computed style and the box model of an element.
func (t *Tab) DevStyle(id int64) (DevStyle, error) {
	var out DevStyle
	d, err := t.devDoc()
	if err != nil {
		return out, err
	}
	n := d.NodeByID(id)
	if n == nil || n.Type != html.ElementNode {
		return out, ErrNotFound
	}
	vw, vh, _, _, _ := t.Viewport()
	env := &style.Env{Width: float64(vw), Height: float64(vh), DPR: 1, ColorScheme: t.ColorSchemeName()}
	rules := t.devMatchedRules(d, n, id, env)
	out.Rules = rules
	if cs := computedStyleOf(d, id); cs != nil {
		keys := make([]string, 0, len(cs))
		for k := range cs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			out.Computed = append(out.Computed, [2]string{k, fmt.Sprint(cs[k])})
		}
		l := d.Layout(d.LayoutWidth())
		if ext, _ := l.Ext.(*renderExt); ext != nil && ext.res != nil {
			if b := ext.res.Boxes[n]; b != nil && b.Style != nil {
				out.Computed = append(out.Computed, devExtraComputed(b.Style)...)
				sort.Slice(out.Computed, func(i, j int) bool { return out.Computed[i][0] < out.Computed[j][0] })
				if b.Display == "none" || !b.Visible && b.Border.W == 0 && b.Border.H == 0 {
					out.Hidden = b.Display == "none"
				}
				if b.Display != "none" {
					out.Box = &DevBox{
						Margin: b.Margin,
						Border: [4]float64{b.Padding.Y - b.Border.Y, b.Border.X + b.Border.W - (b.Padding.X + b.Padding.W),
							b.Border.Y + b.Border.H - (b.Padding.Y + b.Padding.H), b.Padding.X - b.Border.X},
						Padding: [4]float64{b.Content.Y - b.Padding.Y, b.Padding.X + b.Padding.W - (b.Content.X + b.Content.W),
							b.Padding.Y + b.Padding.H - (b.Content.Y + b.Content.H), b.Content.X - b.Padding.X},
						W: b.Content.W, H: b.Content.H, X: b.Border.X, Y: b.Border.Y, BW: b.Border.W, BH: b.Border.H,
					}
				}
			}
		}
	}
	return out, nil
}

// devExtraComputed adds properties the scripted getComputedStyle table leaves out and the panel's reader wants.
func devExtraComputed(st *style.Computed) [][2]string {
	var out [][2]string
	add := func(k, v string) {
		if v != "" {
			out = append(out, [2]string{k, v})
		}
	}
	add("float", st.Float)
	add("clear", st.Clear)
	add("overflow-x", st.OverflowX)
	add("overflow-y", st.OverflowY)
	add("flex-direction", st.FlexDirection)
	add("flex-wrap", st.FlexWrap)
	add("justify-content", st.JustifyContent)
	add("align-items", st.AlignItems)
	add("text-decoration-line", map[uint8]string{0: "none", 1: "underline", 2: "overline", 3: "underline overline", 4: "line-through", 5: "underline line-through"}[st.TextDecorLine])
	for i, s := range []string{"top", "right", "bottom", "left"} {
		if st.BorderWidth[i] > 0 {
			add("border-"+s+"-style", st.BorderStyle[i])
			add("border-"+s+"-color", cssColor(st.BorderColor[i]))
		}
	}
	return out
}

// ---- matched rules --------------------------------------------------------------------------------------

type devSheet struct {
	name string
	text string
}

// devSheets lists the stylesheets of the document in tree order: <style> elements with their text and <link
// rel=stylesheet> with the body the recorder kept. Called with t.mu held for reading.
func (t *Tab) devSheets(d *fnet.Doc, env *style.Env) []devSheet {
	var out []devSheet
	nstyle := 0
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "style":
				if devInternal(n) {
					return
				}
				nstyle++
				var sb strings.Builder
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					if c.Type == html.TextNode {
						sb.WriteString(c.Data)
					}
				}
				if m := devAttr(n, "media"); m == "" || style.MediaMatches(m, env) {
					out = append(out, devSheet{name: fmt.Sprintf("<style> %d", nstyle), text: sb.String()})
				}
				return
			case "link":
				rel := strings.Fields(strings.ToLower(devAttr(n, "rel")))
				sheet, alt := false, false
				for _, r := range rel {
					sheet = sheet || r == "stylesheet"
					alt = alt || r == "alternate"
				}
				href := strings.TrimSpace(devAttr(n, "href"))
				if sheet && !alt && href != "" {
					if m := devAttr(n, "media"); m == "" || style.MediaMatches(m, env) {
						if abs, err := resolveURL(d.BaseURL(), href); err == nil {
							out = append(out, devSheet{name: abs, text: t.devBodyByURL(abs)})
						}
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(d.Root)
	return out
}

func devAttr(n *html.Node, k string) string {
	for _, a := range n.Attr {
		if a.Key == k {
			return a.Val
		}
	}
	return ""
}

// devBodyByURL returns the recorded body of a stylesheet of the current navigation ("" when none was kept).
func (t *Tab) devBodyByURL(abs string) string {
	l := t.dev
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := len(l.list) - 1; i >= 0; i-- {
		e := l.list[i]
		if e.Nav == l.nav && e.body != nil && (e.URL == abs || e.FinalURL == abs) && e.Type == "style" {
			return toValidUTF8(e.body)
		}
	}
	return ""
}

func (t *Tab) devMatchedRules(d *fnet.Doc, n *html.Node, id int64, env *style.Env) []DevRule {
	var matched []DevRule
	order := 0
	for _, sh := range t.devSheets(d, env) {
		devScanCSS(sh.text, env, func(sel, body string, line int, at []string) {
			order++
			for _, s := range devSplitSelectors(sel) {
				if devPseudoElement(s) {
					continue
				}
				if ok, err := d.Matches(id, s); err != nil || !ok {
					continue
				}
				r := DevRule{Selector: strings.TrimSpace(s), Source: sh.name, Line: line, AtRules: at, spec: devSpecificity(s), order: order}
				r.Decls = devParseDecls(body)
				if len(r.Decls) > 0 {
					matched = append(matched, r)
				}
			}
		}, t.devImportLoader())
	}
	// inline style attribute: the strongest normal rule
	if st := devAttr(n, "style"); strings.TrimSpace(st) != "" {
		matched = append(matched, DevRule{Selector: "element.style", Source: "element.style", Inline: true, Decls: devParseDecls(st), spec: 1 << 24, order: 1 << 30})
	}
	// cascade: mark the declarations that lose
	sort.SliceStable(matched, func(i, j int) bool {
		if matched[i].spec != matched[j].spec {
			return matched[i].spec > matched[j].spec
		}
		return matched[i].order > matched[j].order
	})
	won := map[string]int{} // property -> 0 none, 1 normal won, 2 important won
	for pass := 2; pass >= 1; pass-- {
		for ri := range matched {
			for di := range matched[ri].Decls {
				dc := &matched[ri].Decls[di]
				if dc.Important != (pass == 2) {
					continue
				}
				if won[dc.Name] == 0 {
					won[dc.Name] = pass
				} else {
					dc.Overridden = true
				}
			}
		}
	}
	// a normal declaration also loses to an important one found later in the walk
	for ri := range matched {
		for di := range matched[ri].Decls {
			dc := &matched[ri].Decls[di]
			if !dc.Important && won[dc.Name] == 2 {
				dc.Overridden = true
			}
		}
	}
	return matched
}

func devPseudoElement(sel string) bool {
	s := strings.ToLower(sel)
	return strings.Contains(s, "::") || strings.Contains(s, ":before") || strings.Contains(s, ":after") ||
		strings.Contains(s, ":first-line") || strings.Contains(s, ":first-letter")
}

// devImportLoader serves @import from the recorder (the sheet was fetched when the page loaded).
func (t *Tab) devImportLoader() func(base, ref string) (string, string) {
	return func(base, ref string) (string, string) {
		abs, err := resolveURL(base, ref)
		if err != nil {
			return "", ""
		}
		return abs, t.devBodyByURL(abs)
	}
}

// ---- a small CSS rule scanner ---------------------------------------------------------------------------

// scanCSS walks style rules of a sheet and calls fn for each: selector text, declaration block text, the 1-based
// line of the selector and the at-rule conditions around it. @media blocks that do not match the environment are
// skipped. Keyframes, font-face and similar blocks are not rules for an element. Nested rules are not followed.
func devScanCSS(src string, env *style.Env, fn func(sel, body string, line int, at []string), imp func(base, ref string) (string, string)) {
	devScanCSSAt(src, 1, env, nil, fn, imp, "", 0)
}

func devScanCSSAt(src string, line0 int, env *style.Env, at []string, fn func(sel, body string, line int, at []string), imp func(base, ref string) (string, string), base string, depth int) {
	i, line := 0, line0
	n := len(src)
	skipSpace := func() {
		for i < n {
			c := src[i]
			if c == '\n' {
				line++
				i++
			} else if c == ' ' || c == '\t' || c == '\r' || c == '\f' {
				i++
			} else if c == '/' && i+1 < n && src[i+1] == '*' {
				j := strings.Index(src[i+2:], "*/")
				if j < 0 {
					i = n
					return
				}
				line += strings.Count(src[i:i+2+j+2], "\n")
				i += 2 + j + 2
			} else if c == '<' && strings.HasPrefix(src[i:], "<!--") {
				i += 4
			} else if c == '-' && strings.HasPrefix(src[i:], "-->") {
				i += 3
			} else {
				return
			}
		}
	}
	// readUntil reads up to a top-level stop byte ('{' or ';'), honouring strings, parens and comments
	readUntil := func(stop1, stop2 byte) (string, byte) {
		start, depth := i, 0
		for i < n {
			c := src[i]
			switch {
			case c == '"' || c == '\'':
				q := c
				i++
				for i < n && src[i] != q {
					if src[i] == '\\' {
						i++
					}
					if i < n && src[i] == '\n' {
						line++
					}
					i++
				}
			case c == '/' && i+1 < n && src[i+1] == '*':
				j := strings.Index(src[i+2:], "*/")
				if j < 0 {
					i = n
					return src[start:], 0
				}
				line += strings.Count(src[i:i+2+j+2], "\n")
				i += 2 + j + 2
				continue
			case c == '(' || c == '[':
				depth++
			case c == ')' || c == ']':
				if depth > 0 {
					depth--
				}
			case c == '\n':
				line++
			case depth == 0 && (c == stop1 || c == stop2):
				s := src[start:i]
				i++
				return s, c
			}
			i++
		}
		return src[start:], 0
	}
	// block returns the text between the braces that start at i-1 (the '{' was consumed) and moves past the '}'
	block := func() (string, int) {
		startLine := line
		start, depth := i, 1
		for i < n && depth > 0 {
			c := src[i]
			switch {
			case c == '"' || c == '\'':
				q := c
				i++
				for i < n && src[i] != q {
					if src[i] == '\\' {
						i++
					}
					if i < n && src[i] == '\n' {
						line++
					}
					i++
				}
			case c == '/' && i+1 < n && src[i+1] == '*':
				j := strings.Index(src[i+2:], "*/")
				if j < 0 {
					i = n
					continue
				}
				line += strings.Count(src[i:i+2+j+2], "\n")
				i += 2 + j + 2
				continue
			case c == '{':
				depth++
			case c == '}':
				depth--
			case c == '\n':
				line++
			}
			i++
		}
		end := i - 1
		if end < start {
			end = start
		}
		if depth > 0 {
			end = n
		}
		return src[start:end], startLine
	}
	for {
		skipSpace()
		if i >= n {
			return
		}
		selLine := line
		head, stop := readUntil('{', ';')
		head = strings.TrimSpace(head)
		if stop == 0 && head == "" {
			return
		}
		if strings.HasPrefix(head, "@") {
			name := strings.ToLower(strings.Fields(strings.TrimPrefix(head, "@") + " ")[0])
			if stop == ';' || stop == 0 {
				if name == "import" && imp != nil && depth < 4 {
					ref, media := devParseImport(head)
					if ref != "" && (media == "" || style.MediaMatches(media, env)) {
						if abs, body := imp(base, ref); body != "" {
							devScanCSSAt(body, 1, env, at, fn, imp, abs, depth+1)
						}
					}
				}
				continue
			}
			body, bodyLine := block()
			switch name {
			case "media":
				cond := strings.TrimSpace(head[len("@media"):])
				if style.MediaMatches(cond, env) {
					devScanCSSAt(body, bodyLine, env, append(append([]string(nil), at...), "@media "+cond), fn, imp, base, depth)
				}
			case "supports", "layer", "container", "scope", "document":
				devScanCSSAt(body, bodyLine, env, append(append([]string(nil), at...), head), fn, imp, base, depth)
			}
			continue
		}
		if stop != '{' {
			continue
		}
		body, _ := block()
		fn(head, body, selLine, at)
	}
}

// devParseImport reads `@import url(x) screen;` or `@import "x" print;`.
func devParseImport(head string) (ref, media string) {
	s := strings.TrimSpace(head[len("@import"):])
	switch {
	case strings.HasPrefix(strings.ToLower(s), "url("):
		j := strings.IndexByte(s, ')')
		if j < 0 {
			return "", ""
		}
		ref = strings.Trim(strings.TrimSpace(s[4:j]), `"'`)
		media = strings.TrimSpace(s[j+1:])
	case len(s) > 0 && (s[0] == '"' || s[0] == '\''):
		j := strings.IndexByte(s[1:], s[0])
		if j < 0 {
			return "", ""
		}
		ref = s[1 : 1+j]
		media = strings.TrimSpace(s[2+j:])
	}
	if k := strings.Index(strings.ToLower(media), "layer"); k == 0 {
		media = ""
	}
	return ref, media
}

// splitSelectors splits a selector list on its top-level commas.
func devSplitSelectors(s string) []string {
	var out []string
	depth, start := 0, 0
	var q byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case q != 0:
			if c == q {
				q = 0
			}
		case c == '"' || c == '\'':
			q = c
		case c == '(' || c == '[':
			depth++
		case c == ')' || c == ']':
			depth--
		case c == ',' && depth == 0:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// parseDecls splits a declaration block into name/value pairs.
func devParseDecls(body string) []DevDecl {
	var out []DevDecl
	depth, start := 0, 0
	var q byte
	flush := func(end int) {
		part := strings.TrimSpace(body[start:end])
		start = end + 1
		if part == "" {
			return
		}
		k := strings.IndexByte(part, ':')
		if k <= 0 {
			return
		}
		name := strings.TrimSpace(part[:k])
		val := strings.TrimSpace(part[k+1:])
		imp := false
		if j := strings.LastIndex(val, "!"); j >= 0 && strings.EqualFold(strings.TrimSpace(val[j+1:]), "important") {
			imp, val = true, strings.TrimSpace(val[:j])
		}
		if !strings.HasPrefix(name, "--") {
			name = strings.ToLower(name)
		}
		out = append(out, DevDecl{Name: name, Value: val, Important: imp})
	}
	for i := 0; i < len(body); i++ {
		c := body[i]
		switch {
		case q != 0:
			if c == '\\' {
				i++
			} else if c == q {
				q = 0
			}
		case c == '"' || c == '\'':
			q = c
		case c == '/' && i+1 < len(body) && body[i+1] == '*':
			if j := strings.Index(body[i+2:], "*/"); j >= 0 {
				body = body[:i] + strings.Repeat(" ", j+4) + body[i+2+j+2:]
			}
		case c == '(':
			depth++
		case c == ')':
			depth--
		case c == ';' && depth == 0:
			flush(i)
		}
	}
	flush(len(body))
	return out
}

// specificity returns a selector's (ids, classes, types) packed into one int, the most specific alternative of
// :is()/:not() counted, as the specification says.
func devSpecificity(sel string) int {
	a, b, c := 0, 0, 0
	s := sel
	for i := 0; i < len(s); {
		ch := s[i]
		switch {
		case ch == '#':
			a++
			i = devIdentEnd(s, i+1)
		case ch == '.':
			b++
			i = devIdentEnd(s, i+1)
		case ch == '[':
			b++
			j := strings.IndexByte(s[i:], ']')
			if j < 0 {
				return a<<16 | b<<8 | c
			}
			i += j + 1
		case ch == ':':
			if strings.HasPrefix(s[i:], "::") {
				c++
				i = devIdentEnd(s, i+2)
				break
			}
			j := devIdentEnd(s, i+1)
			name := strings.ToLower(s[i+1 : j])
			i = j
			if i < len(s) && s[i] == '(' {
				depth, k := 1, i+1
				for k < len(s) && depth > 0 {
					if s[k] == '(' {
						depth++
					} else if s[k] == ')' {
						depth--
					}
					k++
				}
				inner := s[i+1 : k-1]
				i = k
				switch name {
				case "is", "not", "matches", "has", "where":
					if name == "where" {
						break
					}
					best := 0
					for _, alt := range devSplitSelectors(inner) {
						if sp := devSpecificity(alt); sp > best {
							best = sp
						}
					}
					a, b, c = a+best>>16, b+(best>>8)&255, c+best&255
				default:
					b++
				}
			} else if name == "before" || name == "after" || name == "first-line" || name == "first-letter" {
				c++
			} else {
				b++
			}
		case ch == '*' || ch == ' ' || ch == '>' || ch == '+' || ch == '~' || ch == '\t' || ch == '\n':
			i++
		case ch == '|':
			i++
		default:
			if devIdentStart(ch) {
				c++
				i = devIdentEnd(s, i)
			} else {
				i++
			}
		}
	}
	return a<<16 | b<<8 | c
}

func devIdentStart(c byte) bool {
	return c == '_' || c == '-' || c >= 0x80 || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func devIdentEnd(s string, i int) int {
	for i < len(s) {
		c := s[i]
		if c == '\\' && i+1 < len(s) {
			i += 2
			continue
		}
		if devIdentStart(c) || c >= '0' && c <= '9' {
			i++
			continue
		}
		break
	}
	return i
}
