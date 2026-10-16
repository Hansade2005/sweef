package engine

// innertext.go: HTMLElement.innerText from the rendered page. It used to return textContent, so text of hidden
// elements was included and nothing separated block-level elements: "The Future of" and "Workplace Readiness"
// in two block spans read "The Future ofWorkplace Readiness". innerText is defined on the rendering (like a
// browser, it lays the page out first): display:none subtrees and visibility:hidden text are left out, block
// boxes are separated by a line break, paragraphs by a blank line, <br> is a line break, table cells are
// separated by tabs, and white space collapses unless the style preserves it.

import (
	"strings"

	"golang.org/x/net/html"

	fnet "github.com/sweefbrowser/sweef/internal/net"
	"github.com/sweefbrowser/sweef/internal/render"
	"github.com/sweefbrowser/sweef/internal/style"
)

func innerTextOf(d *fnet.Doc, id int64) string {
	n := d.NodeByID(id)
	if n == nil {
		return ""
	}
	l := d.Layout(d.LayoutWidth())
	ext, _ := l.Ext.(*renderExt)
	if ext == nil || ext.res == nil {
		return d.TextContent(id)
	}
	boxes := ext.res.Boxes
	if b := boxes[n]; n.Type == html.ElementNode && (b == nil || b.Style == nil || b.Display == "none") {
		return d.TextContent(id) // not being rendered: the spec's answer is textContent
	}
	w := &itWriter{atStart: true}
	w.walk(n, boxes, true)
	return strings.TrimRight(w.sb.String(), " \t\n")
}

type itWriter struct {
	sb        strings.Builder
	pendingLB int
	atStart   bool
	lastSpace bool
}

func (w *itWriter) requireLB(k int) {
	if k > w.pendingLB {
		w.pendingLB = k
	}
}

func (w *itWriter) flushLB() bool {
	if w.pendingLB == 0 {
		return false
	}
	k := w.pendingLB
	w.pendingLB = 0
	if w.atStart {
		return false
	}
	s := strings.TrimRight(w.sb.String(), " \t")
	w.sb.Reset()
	w.sb.WriteString(s)
	w.sb.WriteString(strings.Repeat("\n", k))
	return true
}

func (w *itWriter) text(s string, ws string) {
	if s == "" {
		return
	}
	pre := ws == "pre" || ws == "pre-wrap" || ws == "break-spaces"
	if !pre {
		keepNL := ws == "pre-line"
		var b strings.Builder
		space := false
		for _, r := range s {
			if r == '\n' && keepNL {
				b.WriteByte('\n')
				space = false
				continue
			}
			if r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\f' {
				if !space {
					b.WriteByte(' ')
				}
				space = true
				continue
			}
			space = false
			b.WriteRune(r)
		}
		s = b.String()
	}
	if !pre && strings.TrimSpace(s) == "" && !strings.Contains(s, "\n") {
		// collapsible white space alone: a space between inline content, nothing at a line boundary
		if w.pendingLB == 0 && !w.atStart && !w.lastSpace {
			w.sb.WriteByte(' ')
			w.lastSpace = true
		}
		return
	}
	lineStart := w.flushLB() || w.atStart
	if !pre && (lineStart || w.lastSpace) {
		s = strings.TrimLeft(s, " ")
	}
	if s == "" {
		return
	}
	w.sb.WriteString(s)
	w.atStart = false
	w.lastSpace = strings.HasSuffix(s, " ")
}

func blockLevel(display string) bool {
	switch display {
	case "block", "flex", "grid", "list-item", "table", "flow-root", "table-caption", "-webkit-box", "table-row-group",
		"table-header-group", "table-footer-group":
		return true
	}
	return false
}

func (w *itWriter) walk(n *html.Node, boxes map[*html.Node]*render.Box, root bool) {
	switch n.Type {
	case html.TextNode:
		var st *style.Computed
		if p := n.Parent; p != nil {
			if b := boxes[p]; b != nil {
				st = b.Style
			}
		}
		if st == nil || st.Visibility != "visible" {
			return
		}
		w.text(n.Data, st.WhiteSpace)
		return
	case html.ElementNode:
	case html.DocumentNode:
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			w.walk(c, boxes, false)
		}
		return
	default:
		return
	}
	switch n.Data {
	case "script", "style", "noscript", "template", "head", "title", "meta", "link", "base":
		return
	}
	b := boxes[n]
	if b == nil || b.Style == nil || b.Display == "none" {
		return
	}
	if n.Data == "br" {
		if b.Style.Visibility == "visible" {
			w.flushLB()
			w.sb.WriteByte('\n')
			w.atStart = false
			w.lastSpace = true
		}
		return
	}
	disp := b.Style.Display
	lb := 0
	switch {
	case n.Data == "p":
		lb = 2
	case blockLevel(disp), disp == "table-row":
		lb = 1
	}
	if lb > 0 && !root {
		w.requireLB(lb)
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		w.walk(c, boxes, false)
	}
	if disp == "table-cell" && n.NextSibling != nil && !root {
		w.flushLB()
		w.sb.WriteByte('\t')
		w.lastSpace = true
	}
	if lb > 0 && !root {
		w.requireLB(lb)
	}
}
