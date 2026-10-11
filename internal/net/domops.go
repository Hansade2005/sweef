package net

import (
	"fmt"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// domops.go — id-addressed DOM operations used by the JavaScript bridge.
// Every function takes node ids from the Doc id table (see ids.go) and
// returns ids, so the JS side never holds Go pointers.

// DocID returns the id of the Document node.
func (d *Doc) DocID() int64 { return d.idOf(d.Root) }

// NodeType returns the DOM nodeType constant (1 element, 3 text, 8 comment,
// 9 document, 10 doctype) or 0 for an unknown id.
func (d *Doc) NodeType(id int64) int {
	n := d.find(id)
	if n == nil {
		return 0
	}
	switch n.Type {
	case html.ElementNode:
		return 1
	case html.TextNode:
		return 3
	case html.CommentNode:
		return 8
	case html.DocumentNode:
		return 9
	case html.DoctypeNode:
		return 10
	}
	return 0
}

func (d *Doc) TagName(id int64) string {
	n := d.find(id)
	if n == nil || n.Type != html.ElementNode {
		return ""
	}
	return n.Data
}

func (d *Doc) Namespace(id int64) string {
	n := d.find(id)
	if n == nil {
		return ""
	}
	return n.Namespace
}

func (d *Doc) ParentID(id int64) int64 {
	n := d.find(id)
	if n == nil || n.Parent == nil {
		return 0
	}
	return d.idOf(n.Parent)
}

func (d *Doc) ChildIDs(id int64) []int64 {
	n := d.find(id)
	if n == nil {
		return nil
	}
	var out []int64
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		out = append(out, d.idOf(c))
	}
	return out
}

func (d *Doc) FirstChildID(id int64) int64 {
	if n := d.find(id); n != nil && n.FirstChild != nil {
		return d.idOf(n.FirstChild)
	}
	return 0
}

func (d *Doc) LastChildID(id int64) int64 {
	if n := d.find(id); n != nil && n.LastChild != nil {
		return d.idOf(n.LastChild)
	}
	return 0
}

func (d *Doc) NextSiblingID(id int64) int64 {
	if n := d.find(id); n != nil && n.NextSibling != nil {
		return d.idOf(n.NextSibling)
	}
	return 0
}

func (d *Doc) PrevSiblingID(id int64) int64 {
	if n := d.find(id); n != nil && n.PrevSibling != nil {
		return d.idOf(n.PrevSibling)
	}
	return 0
}

// ---- creation / structure ---------------------------------------------------

func (d *Doc) CreateElement(tag, ns string) int64 {
	n := &html.Node{Type: html.ElementNode, Data: strings.ToLower(tag), Namespace: ns}
	if ns == "svg" || ns == "http://www.w3.org/2000/svg" {
		n.Namespace = "svg"
		n.Data = tag
	}
	// html.Render and ParseFragment require DataAtom to agree with Data.
	n.DataAtom = atom.Lookup([]byte(n.Data))
	return d.idOf(n)
}

func (d *Doc) CreateText(data string) int64 {
	return d.idOf(&html.Node{Type: html.TextNode, Data: data})
}

func (d *Doc) CreateComment(data string) int64 {
	return d.idOf(&html.Node{Type: html.CommentNode, Data: data})
}

// CreateFragment makes a detached container used for DocumentFragment.
func (d *Doc) CreateFragment() int64 {
	return d.idOf(&html.Node{Type: html.DocumentNode})
}

func (d *Doc) AppendChild(parentID, childID int64) error {
	p, c := d.find(parentID), d.find(childID)
	if p == nil || c == nil {
		return fmt.Errorf("appendChild: unknown node")
	}
	if c.Type == html.DocumentNode { // fragment: move its children
		for k := c.FirstChild; k != nil; {
			next := k.NextSibling
			c.RemoveChild(k)
			p.AppendChild(k)
			k = next
		}
		d.note(Mutation{Op: "insert", BackendID: parentID})
		return nil
	}
	for x := p; x != nil; x = x.Parent {
		if x == c {
			return fmt.Errorf("HierarchyRequestError: cannot append an ancestor")
		}
	}
	if c.Parent != nil {
		c.Parent.RemoveChild(c)
	}
	p.AppendChild(c)
	d.note(Mutation{Op: "insert", BackendID: childID})
	return nil
}

func (d *Doc) InsertBefore(parentID, childID, refID int64) error {
	if refID == 0 {
		return d.AppendChild(parentID, childID)
	}
	p, c, r := d.find(parentID), d.find(childID), d.find(refID)
	if p == nil || c == nil || r == nil {
		return fmt.Errorf("insertBefore: unknown node")
	}
	if r.Parent != p {
		return fmt.Errorf("NotFoundError: reference node is not a child")
	}
	if c.Type == html.DocumentNode {
		for k := c.FirstChild; k != nil; {
			next := k.NextSibling
			c.RemoveChild(k)
			p.InsertBefore(k, r)
			k = next
		}
		d.note(Mutation{Op: "insert", BackendID: parentID})
		return nil
	}
	if c == r {
		return nil
	}
	if c.Parent != nil {
		c.Parent.RemoveChild(c)
	}
	p.InsertBefore(c, r)
	d.note(Mutation{Op: "insert", BackendID: childID})
	return nil
}

func (d *Doc) RemoveChild(parentID, childID int64) error {
	p, c := d.find(parentID), d.find(childID)
	if p == nil || c == nil {
		return fmt.Errorf("removeChild: unknown node")
	}
	if c.Parent != p {
		return fmt.Errorf("NotFoundError: node is not a child")
	}
	p.RemoveChild(c)
	d.note(Mutation{Op: "remove", BackendID: childID})
	return nil
}

func (d *Doc) CloneNode(id int64, deep bool) int64 {
	n := d.find(id)
	if n == nil {
		return 0
	}
	var clone func(*html.Node) *html.Node
	clone = func(x *html.Node) *html.Node {
		c := &html.Node{Type: x.Type, Data: x.Data, Namespace: x.Namespace, DataAtom: x.DataAtom}
		c.Attr = append([]html.Attribute(nil), x.Attr...)
		if deep {
			for k := x.FirstChild; k != nil; k = k.NextSibling {
				c.AppendChild(clone(k))
			}
		}
		return c
	}
	return d.idOf(clone(n))
}

// ---- content ---------------------------------------------------------------

func (d *Doc) NodeValue(id int64) string {
	n := d.find(id)
	if n == nil || (n.Type != html.TextNode && n.Type != html.CommentNode) {
		return ""
	}
	return n.Data
}

func (d *Doc) SetNodeValue(id int64, s string) {
	if n := d.find(id); n != nil && (n.Type == html.TextNode || n.Type == html.CommentNode) {
		n.Data = s
		d.note(Mutation{Op: "characterData", BackendID: id})
	}
}

// TextContent is the raw concatenation of descendant text (no whitespace
// normalisation; scripts/styles included, per spec).
func (d *Doc) TextContent(id int64) string {
	n := d.find(id)
	if n == nil {
		return ""
	}
	if n.Type == html.TextNode || n.Type == html.CommentNode {
		return n.Data
	}
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(x *html.Node) {
		if x.Type == html.TextNode {
			sb.WriteString(x.Data)
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return sb.String()
}

func (d *Doc) SetTextContent(id int64, s string) {
	n := d.find(id)
	if n == nil {
		return
	}
	if n.Type == html.TextNode || n.Type == html.CommentNode {
		n.Data = s
		return
	}
	clearChildren(n)
	if s != "" {
		n.AppendChild(&html.Node{Type: html.TextNode, Data: s})
	}
	d.note(Mutation{Op: "inner-html", BackendID: id})
}

// SetInnerHTMLCtx parses frag in the context of the element itself (so
// <tr> inside <table> parses correctly) and replaces its children.
func (d *Doc) SetInnerHTMLCtx(id int64, frag string) error {
	n := d.find(id)
	if n == nil {
		return fmt.Errorf("no node with id %d", id)
	}
	ctx := n
	if n.Type != html.ElementNode {
		ctx = &html.Node{Type: html.ElementNode, Data: "body"}
	}
	if n.Type == html.ElementNode && n.Data == "template" {
		ctx = &html.Node{Type: html.ElementNode, Data: "body"}
	}
	sub, err := html.ParseFragment(strings.NewReader(frag), ctx)
	if err != nil {
		return err
	}
	clearChildren(n)
	for _, s := range sub {
		n.AppendChild(s)
	}
	d.note(Mutation{Op: "inner-html", BackendID: id})
	return nil
}

func (d *Doc) InsertAdjacentHTML(id int64, position, frag string) error {
	n := d.find(id)
	if n == nil {
		return fmt.Errorf("no node with id %d", id)
	}
	ctx := n
	if position == "beforebegin" || position == "afterend" {
		if n.Parent != nil && n.Parent.Type == html.ElementNode {
			ctx = n.Parent
		} else {
			ctx = &html.Node{Type: html.ElementNode, Data: "body"}
		}
	}
	sub, err := html.ParseFragment(strings.NewReader(frag), ctx)
	if err != nil {
		return err
	}
	switch position {
	case "beforebegin":
		if n.Parent == nil {
			return fmt.Errorf("NoModificationAllowedError")
		}
		for _, s := range sub {
			n.Parent.InsertBefore(s, n)
		}
	case "afterbegin":
		first := n.FirstChild
		for _, s := range sub {
			if first != nil {
				n.InsertBefore(s, first)
			} else {
				n.AppendChild(s)
			}
		}
	case "beforeend":
		for _, s := range sub {
			n.AppendChild(s)
		}
	case "afterend":
		if n.Parent == nil {
			return fmt.Errorf("NoModificationAllowedError")
		}
		next := n.NextSibling
		for _, s := range sub {
			if next != nil {
				n.Parent.InsertBefore(s, next)
			} else {
				n.Parent.AppendChild(s)
			}
		}
	default:
		return fmt.Errorf("SyntaxError: bad position %q", position)
	}
	d.note(Mutation{Op: "insert", BackendID: id})
	return nil
}

// ---- attributes ------------------------------------------------------------

func (d *Doc) GetAttr(id int64, name string) (string, bool) {
	n := d.find(id)
	if n == nil {
		return "", false
	}
	name = strings.ToLower(name)
	for _, a := range n.Attr {
		if a.Key == name {
			return a.Val, true
		}
	}
	return "", false
}

func (d *Doc) SetAttr(id int64, name, val string) {
	if n := d.find(id); n != nil && n.Type == html.ElementNode {
		setAttr(n, strings.ToLower(name), val)
		d.note(Mutation{Op: "set-attr", BackendID: id, Key: name, Value: val})
	}
}

func (d *Doc) RemoveAttr(id int64, name string) {
	if n := d.find(id); n != nil {
		removeAttr(n, strings.ToLower(name))
		d.note(Mutation{Op: "remove-attr", BackendID: id, Key: name})
	}
}

func (d *Doc) AttrNames(id int64) []string {
	n := d.find(id)
	if n == nil {
		return nil
	}
	out := make([]string, 0, len(n.Attr))
	for _, a := range n.Attr {
		out = append(out, a.Key)
	}
	return out
}

// ---- queries ---------------------------------------------------------------

func (d *Doc) QueryFrom(rootID int64, sel string) (int64, error) {
	root := d.Root
	if rootID != 0 {
		if root = d.find(rootID); root == nil {
			return 0, fmt.Errorf("unknown root")
		}
	}
	ns, err := d.queryAllFrom(root, sel)
	if err != nil || len(ns) == 0 {
		return 0, err
	}
	return d.idOf(ns[0]), nil
}

func (d *Doc) QueryAllFrom(rootID int64, sel string) ([]int64, error) {
	root := d.Root
	if rootID != 0 {
		if root = d.find(rootID); root == nil {
			return nil, fmt.Errorf("unknown root")
		}
	}
	ns, err := d.queryAllFrom(root, sel)
	if err != nil {
		return nil, err
	}
	out := make([]int64, 0, len(ns))
	for _, n := range ns {
		out = append(out, d.idOf(n))
	}
	return out, nil
}

func (d *Doc) Matches(id int64, sel string) (bool, error) {
	n := d.find(id)
	if n == nil || n.Type != html.ElementNode {
		return false, nil
	}
	return matchesSelector(n, sel)
}

func (d *Doc) ElementByID(id string) int64 {
	if n := findByID(d.Root, id); n != nil {
		return d.idOf(n)
	}
	return 0
}

func (d *Doc) ElementsBy(rootID int64, kind, want string) []int64 {
	root := d.Root
	if rootID != 0 {
		if root = d.find(rootID); root == nil {
			return nil
		}
	}
	var out []int64
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.ElementNode {
				ok := false
				switch kind {
				case "tag":
					ok = want == "*" || strings.EqualFold(c.Data, want)
				case "class":
					cls := " " + strings.Join(strings.Fields(attr(c, "class")), " ") + " "
					ok = true
					for _, w := range strings.Fields(want) {
						if !strings.Contains(cls, " "+w+" ") {
							ok = false
							break
						}
					}
				case "name":
					ok = attr(c, "name") == want
				}
				if ok {
					out = append(out, d.idOf(c))
				}
			}
			walk(c)
		}
	}
	walk(root)
	return out
}

// ---- form state --------------------------------------------------------------

func (d *Doc) GetValue(id int64) string {
	n := d.find(id)
	if n == nil {
		return ""
	}
	switch n.Data {
	case "input":
		return attr(n, "value")
	case "textarea":
		return d.TextContent(id)
	case "select":
		for _, o := range optionsOf(n) {
			if hasAttr(o, "selected") {
				if v, ok := d.GetAttr(d.idOf(o), "value"); ok {
					return v
				}
				return normWS(innerText(o))
			}
		}
		if os := optionsOf(n); len(os) > 0 {
			if v, ok := d.GetAttr(d.idOf(os[0]), "value"); ok {
				return v
			}
			return normWS(innerText(os[0]))
		}
		return ""
	case "option":
		if v, ok := d.GetAttr(id, "value"); ok {
			return v
		}
		return normWS(innerText(n))
	}
	return attr(n, "value")
}

func (d *Doc) GetChecked(id int64) bool {
	n := d.find(id)
	return n != nil && hasAttr(n, "checked")
}

func (d *Doc) SetCheckedRaw(id int64, v bool) {
	n := d.find(id)
	if n == nil {
		return
	}
	if v {
		setAttr(n, "checked", "")
		if attr(n, "type") == "radio" {
			d.uncheckRadioGroup(n)
		}
	} else {
		removeAttr(n, "checked")
	}
	d.note(Mutation{Op: "check", BackendID: id})
}

func (d *Doc) uncheckRadioGroup(n *html.Node) {
	group := attr(n, "name")
	if group == "" {
		return
	}
	scope := d.Root
	for x := n.Parent; x != nil; x = x.Parent {
		if x.Type == html.ElementNode && x.Data == "form" {
			scope = x
			break
		}
	}
	var walk func(*html.Node)
	walk = func(m *html.Node) {
		if m != n && m.Type == html.ElementNode && m.Data == "input" && attr(m, "type") == "radio" && attr(m, "name") == group {
			removeAttr(m, "checked")
		}
		for c := m.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(scope)
}

func (d *Doc) SelectedIndex(id int64) int {
	n := d.find(id)
	if n == nil {
		return -1
	}
	os := optionsOf(n)
	for i, o := range os {
		if hasAttr(o, "selected") {
			return i
		}
	}
	if len(os) > 0 && !hasAttr(n, "multiple") {
		return 0
	}
	return -1
}

func (d *Doc) SetSelectedIndex(id int64, i int) {
	n := d.find(id)
	if n == nil {
		return
	}
	for k, o := range optionsOf(n) {
		if k == i {
			setAttr(o, "selected", "")
		} else {
			removeAttr(o, "selected")
		}
	}
	d.note(Mutation{Op: "select", BackendID: id})
}

// FormOf returns the id of the enclosing <form> (or the form= target), 0 if none.
func (d *Doc) FormOf(id int64) int64 {
	n := d.find(id)
	if n == nil {
		return 0
	}
	if n.Data == "form" {
		return id
	}
	if f := attr(n, "form"); f != "" {
		return d.ElementByID(f)
	}
	for x := n.Parent; x != nil; x = x.Parent {
		if x.Type == html.ElementNode && x.Data == "form" {
			return d.idOf(x)
		}
	}
	return 0
}

// SerializeNode renders one node (outerHTML) or "" when unknown.
func (d *Doc) SerializeNode(id int64) string {
	n := d.find(id)
	if n == nil {
		return ""
	}
	var sb strings.Builder
	if n.Type == html.DocumentNode {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			html.Render(&sb, c)
		}
		return sb.String()
	}
	html.Render(&sb, n)
	return sb.String()
}

// IsConnected reports whether the node is attached to the document root.
func (d *Doc) IsConnected(id int64) bool {
	n := d.find(id)
	return n != nil && d.Attached(n)
}

// ScriptEl describes one <script> element for the loader.
type ScriptEl struct {
	ID       int64
	Src      string
	Type     string
	Async    bool
	Defer    bool
	Module   bool
	NoModule bool
	Source   string
}

func (d *Doc) scriptEl(n *html.Node) ScriptEl {
	typ := attr(n, "type")
	s := ScriptEl{
		ID: d.idOf(n), Src: strings.TrimSpace(attr(n, "src")), Type: typ,
		Async: hasAttr(n, "async"), Defer: hasAttr(n, "defer"),
		Module: strings.EqualFold(strings.TrimSpace(typ), "module"), NoModule: hasAttr(n, "nomodule"),
	}
	if s.Src == "" {
		var sb strings.Builder
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.TextNode {
				sb.WriteString(c.Data)
			}
		}
		s.Source = sb.String()
	}
	return s
}

// ScriptElements lists every <script> in document order.
func (d *Doc) ScriptElements() []ScriptEl {
	var out []ScriptEl
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "script" {
			out = append(out, d.scriptEl(n))
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(d.Root)
	return out
}

// ScriptElement describes one script by id, or nil.
func (d *Doc) ScriptElement(id int64) *ScriptEl {
	n := d.find(id)
	if n == nil || n.Type != html.ElementNode || n.Data != "script" {
		return nil
	}
	s := d.scriptEl(n)
	return &s
}
