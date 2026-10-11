package net

// layout.go — a small flow layout shared by screenshots, element boxes
// (getBoundingClientRect, boundingBox) and coordinate hit-testing, so the
// three always agree.
//
// It is not a CSS layout engine. It implements block and inline flow with
// line wrapping at the viewport width, display from the cascade (none,
// block, inline, inline-block, flex rows, table rows as equal columns,
// list items with markers), font sizes for headings, and intrinsic sizes for
// form controls and images. Text metrics match the bitmap font used by the
// screenshot renderer: a glyph cell is 6x8 px at scale 1, and a font size of
// F px renders at scale round(F/8).

import (
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// WireItem is one paintable thing in document order.
type WireItem struct {
	Kind   string // text | link | heading | button | input | checkbox | radio | select | img | hr | marker | box
	X, Y   int
	W, H   int
	Text   string
	Scale  int // glyph scale for text
	Bold   bool
	Filled bool // checkbox/radio state
}

// Layout is the result of one layout pass.
type Layout struct {
	Width, Height int
	Boxes         map[*html.Node][4]int
	Items         []WireItem
}

type lctx struct {
	d     *Doc
	l     *Layout
	x, y  int // pen position
	left  int // current block's left edge
	right int // current block's right edge
	lineH int // tallest thing on the current line
	scale int
	bold  bool
	link  bool
}

const (
	cellW = 6 // glyph advance at scale 1
	cellH = 8 // glyph height at scale 1 (plus leading added per line)
)

func headingScale(tag string) (int, bool) {
	switch tag {
	case "h1":
		return 4, true
	case "h2":
		return 3, true
	case "h3", "h4":
		return 2, true
	case "h5", "h6":
		return 2, true
	}
	return 0, false
}

// display resolves the effective display of an element.
func (d *Doc) display(n *html.Node) string {
	disp := d.ComputedDisplay(n)
	if disp == "" {
		if blockTags[n.Data] {
			return "block"
		}
		return "inline"
	}
	return disp
}

// Layout lays the document out at the given viewport width.
func (d *Doc) Layout(width int) *Layout {
	if width <= 0 {
		width = 1280
	}
	d.layoutMu.Lock()
	defer d.layoutMu.Unlock()
	gen := d.layoutGen()
	if d.layoutCache != nil && d.layoutCacheW == width && d.layoutCacheGen == gen {
		return d.layoutCache
	}
	l := &Layout{Width: width, Boxes: map[*html.Node][4]int{}}
	c := &lctx{d: d, l: l, x: 8, y: 8, left: 8, right: width - 8, scale: 2}
	body := findFirst(d.Root, "body")
	if body == nil {
		body = d.Root
	}
	c.block(body)
	c.newline()
	l.Height = c.y + 8
	d.layoutCache, d.layoutCacheW, d.layoutCacheGen = l, width, gen
	return l
}

func findFirst(n *html.Node, tag string) *html.Node {
	if n.Type == html.ElementNode && n.Data == tag {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if f := findFirst(c, tag); f != nil {
			return f
		}
	}
	return nil
}

func (c *lctx) lineHeight() int { return cellH*c.scale + 4*c.scale/2 + 2 }

func (c *lctx) newline() {
	if c.x > c.left || c.lineH > 0 {
		h := c.lineH
		if h == 0 {
			h = c.lineHeight()
		}
		c.y += h
	}
	c.x = c.left
	c.lineH = 0
}

func (c *lctx) bump(h int) {
	if h > c.lineH {
		c.lineH = h
	}
}

// place reserves a w×h inline box at the pen, wrapping if needed.
func (c *lctx) place(w, h int) (int, int) {
	if c.x > c.left && c.x+w > c.right {
		c.newline()
	}
	x, y := c.x, c.y
	c.x += w
	c.bump(h)
	return x, y
}

// text flows words onto lines, emitting one item per line fragment.
func (c *lctx) text(s string, kind string) (minX, minY, maxX, maxY int) {
	s = normWS(s)
	minX, minY = 1<<30, 1<<30
	if s == "" {
		return 0, 0, 0, 0
	}
	adv := cellW * c.scale
	lh := c.lineHeight()
	words := strings.Fields(s)
	var frag strings.Builder
	fragX, fragY := c.x, c.y
	flush := func() {
		if frag.Len() == 0 {
			return
		}
		t := frag.String()
		w := len([]rune(t)) * adv
		c.l.Items = append(c.l.Items, WireItem{Kind: kind, X: fragX, Y: fragY, W: w, H: lh, Text: t, Scale: c.scale, Bold: c.bold})
		if fragX < minX {
			minX = fragX
		}
		if fragY < minY {
			minY = fragY
		}
		if fragX+w > maxX {
			maxX = fragX + w
		}
		if fragY+lh > maxY {
			maxY = fragY + lh
		}
		frag.Reset()
	}
	if c.x > c.left {
		// a space between this run and what precedes it on the line
		c.x += adv
	}
	fragX, fragY = c.x, c.y
	for _, w := range words {
		ww := len([]rune(w)) * adv
		sp := 0
		if frag.Len() > 0 {
			sp = adv
		}
		if c.x+sp+ww > c.right && c.x > c.left {
			flush()
			c.bump(lh)
			c.newline()
			fragX, fragY = c.x, c.y
			sp = 0
		}
		if sp > 0 {
			frag.WriteByte(' ')
		}
		frag.WriteString(w)
		c.x += sp + ww
		c.bump(lh)
	}
	flush()
	return
}

// block lays out n's children as a block formatting context.
func (c *lctx) block(n *html.Node) {
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		c.node(ch)
	}
}

func (c *lctx) node(n *html.Node) {
	switch n.Type {
	case html.TextNode:
		if n.Parent != nil && (n.Parent.Data == "script" || n.Parent.Data == "style") {
			return
		}
		kind := "text"
		if c.link {
			kind = "link"
		} else if c.bold && c.scale > 2 {
			kind = "heading"
		}
		c.text(n.Data, kind)
		return
	case html.ElementNode:
	default:
		return
	}
	d := c.d
	if d.pruned(n) {
		return
	}
	disp := d.display(n)
	if disp == "none" || disp == "contents" && n.FirstChild == nil {
		return
	}
	if n.Data == "li" && (disp == "block" || disp == "") {
		disp = "list-item"
	}
	startX, startY := c.x, c.y
	saved := *c
	record := func(x, y, w, h int) {
		if w < 1 {
			w = 1
		}
		if h < 1 {
			h = 1
		}
		c.l.Boxes[n] = [4]int{x, y, w, h}
	}

	// replaced / control elements: fixed intrinsic boxes
	switch n.Data {
	case "input":
		typ := strings.ToLower(attr(n, "type"))
		switch typ {
		case "hidden":
			return
		case "checkbox", "radio":
			x, y := c.place(14*c.scale/2+4, c.lineHeight())
			c.l.Items = append(c.l.Items, WireItem{Kind: typ, X: x, Y: y + 2, W: 7 * c.scale, H: 7 * c.scale, Filled: hasAttr(n, "checked")})
			record(x, y, 7*c.scale, 7*c.scale)
			c.x += 4
			return
		case "submit", "button", "reset", "image":
			label := attr(n, "value")
			if label == "" {
				label = map[string]string{"submit": "Submit", "reset": "Reset"}[typ]
			}
			c.control("button", label, len(label)*cellW*c.scale+16, n, record)
			return
		}
		v := inputValue(n)
		if v == "" {
			v = attr(n, "placeholder")
		}
		c.control("input", v, 160*c.scale/2+40, n, record)
		return
	case "textarea":
		w, h := 240*c.scale/2+40, c.lineHeight()*3
		x, y := c.place(w, h)
		c.l.Items = append(c.l.Items, WireItem{Kind: "input", X: x, Y: y, W: w, H: h - 4, Text: inputValue(n), Scale: c.scale})
		record(x, y, w, h-4)
		c.x += 6
		return
	case "select":
		c.control("select", inputValue(n), 140*c.scale/2+40, n, record)
		return
	case "button":
		c.control("button", normWS(innerText(n)), len([]rune(normWS(innerText(n))))*cellW*c.scale+16, n, record)
		return
	case "img", "svg", "canvas", "video", "iframe", "picture":
		w, _ := strconv.Atoi(attr(n, "width"))
		h, _ := strconv.Atoi(attr(n, "height"))
		if w <= 0 || w > c.right-c.left {
			w = 64
		}
		if h <= 0 || h > 600 {
			h = 48
		}
		if n.Data == "iframe" {
			w, h = min(300, c.right-c.left), 150
		}
		x, y := c.place(w, h)
		c.l.Items = append(c.l.Items, WireItem{Kind: "img", X: x, Y: y, W: w, H: h, Text: attr(n, "alt")})
		record(x, y, w, h)
		c.x += 4
		return
	case "hr":
		c.newline()
		c.y += 6
		c.l.Items = append(c.l.Items, WireItem{Kind: "hr", X: c.left, Y: c.y, W: c.right - c.left, H: 1})
		record(c.left, c.y, c.right-c.left, 1)
		c.y += 8
		return
	case "br":
		c.bump(c.lineHeight())
		c.newline()
		return
	}

	if s, ok := headingScale(n.Data); ok {
		c.scale, c.bold = s, true
	}
	switch n.Data {
	case "a":
		if hasAttr(n, "href") {
			c.link = true
		}
	case "b", "strong", "th":
		c.bold = true
	case "small", "sub", "sup":
		c.scale = max(1, c.scale-1)
	}

	isBlock := disp == "block" || disp == "list-item" || disp == "table" || disp == "flex" || disp == "grid" ||
		disp == "table-row" || disp == "table-row-group" || disp == "table-header-group" || disp == "table-footer-group" ||
		disp == "flow-root" || disp == "table-caption"
	if !isBlock {
		// inline / inline-block: flow children in the current line
		c.block(n)
		c.scale, c.bold, c.link = saved.scale, saved.bold, saved.link
		endX, endY := c.x, c.y
		h := endY - startY + max(c.lineH, c.lineHeight())
		w := endX - startX
		if endY != startY {
			startX, w = c.left, c.right-c.left
		}
		record(startX, startY, w, h)
		return
	}

	// block: start on a fresh line, possibly indented
	c.newline()
	margin := 0
	switch n.Data {
	case "p", "ul", "ol", "dl", "blockquote", "table", "form", "h1", "h2", "h3", "h4", "h5", "h6", "pre", "figure":
		margin = 4 * c.scale
	}
	c.y += margin
	top := c.y
	oldLeft, oldRight := c.left, c.right
	switch n.Data {
	case "ul", "ol", "menu", "dd":
		c.left += 20 * c.scale / 2 * 2
	case "blockquote", "figure":
		c.left += 40
		c.right -= 40
	}
	c.x = c.left
	if disp == "list-item" {
		c.l.Items = append(c.l.Items, WireItem{Kind: "marker", X: c.left - 8*c.scale, Y: c.y + c.lineHeight()/2 - c.scale, W: 3 * c.scale, H: 3 * c.scale})
	}
	switch {
	case disp == "table-row" || n.Data == "tr":
		cells := elementChildren(n)
		if len(cells) > 0 {
			colW := (c.right - c.left) / len(cells)
			rowTop, rowBottom := c.y, c.y
			for i, cell := range cells {
				if d.pruned(cell) {
					continue
				}
				c.left, c.right = oldLeft+i*colW, oldLeft+(i+1)*colW-6
				c.x, c.y, c.lineH = c.left, rowTop, 0
				cs := *c
				if cell.Data == "th" {
					c.bold = true
				}
				c.block(cell)
				c.newline()
				c.bold = cs.bold
				c.l.Boxes[cell] = [4]int{c.left, rowTop, c.right - c.left, max(c.y-rowTop, c.lineHeight())}
				if c.y > rowBottom {
					rowBottom = c.y
				}
			}
			c.left, c.right, c.y = oldLeft, oldRight, rowBottom
		}
	case disp == "flex":
		// row flex: children flow inline, each child as an inline-block
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			if ch.Type == html.ElementNode && !d.pruned(ch) {
				cs := *c
				itemX, itemY := c.x, c.y
				c.block(ch)
				c.scale, c.bold, c.link = cs.scale, cs.bold, cs.link
				if _, ok := c.l.Boxes[ch]; !ok {
					c.l.Boxes[ch] = [4]int{itemX, itemY, max(c.x-itemX, 1), max(c.lineH, c.lineHeight())}
				}
				c.x += 12
			} else {
				c.node(ch)
			}
		}
	default:
		c.block(n)
	}
	c.newline()
	bottom := c.y
	c.y += margin
	c.left, c.right = oldLeft, oldRight
	c.x = c.left
	c.scale, c.bold, c.link = saved.scale, saved.bold, saved.link
	record(oldLeft, top, oldRight-oldLeft, max(bottom-top, 1))
}

func (c *lctx) control(kind, label string, w int, n *html.Node, record func(x, y, w, h int)) {
	if w > c.right-c.left {
		w = c.right - c.left
	}
	h := cellH*c.scale + 10
	x, y := c.place(w, h+4)
	c.l.Items = append(c.l.Items, WireItem{Kind: kind, X: x, Y: y, W: w, H: h, Text: label, Scale: c.scale})
	record(x, y, w, h)
	c.x += 6
}

func elementChildren(n *html.Node) []*html.Node {
	var out []*html.Node
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode {
			out = append(out, c)
		}
	}
	return out
}

// BoxRow is one element's box in document order, for fidelity scoring and
// diagnostics (every element appears; Has is false when it has no box).
type BoxRow struct {
	Tag        string
	ID         string
	X, Y, W, H float64
	Has        bool
}

// ElementBoxes lists every element in document order with its layout box.
func (d *Doc) ElementBoxes(width int) []BoxRow {
	l := d.Layout(width)
	var out []BoxRow
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			r := BoxRow{Tag: n.Data, ID: attr(n, "id")}
			if b, ok := l.Boxes[n]; ok {
				r.X, r.Y, r.W, r.H, r.Has = float64(b[0]), float64(b[1]), float64(b[2]), float64(b[3]), true
			}
			out = append(out, r)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(d.Root)
	return out
}
