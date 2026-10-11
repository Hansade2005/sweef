package net

import (
	"net/url"
	"strings"

	"golang.org/x/net/html"

	"github.com/sweefbrowser/sweef/internal/pdf"
)

// PDFLines flattens the visible document into the structured lines the PDF
// renderer lays out: headings, paragraphs, list items, links (with absolute
// hrefs for clickable annotations), form controls and preformatted blocks.
func (d *Doc) PDFLines() []pdf.Line {
	var out []pdf.Line
	var walk func(n *html.Node, indent int)
	emitText := func(kind, text string, indent int, href string) {
		text = normWS(text)
		if text == "" && kind != "hr" {
			return
		}
		out = append(out, pdf.Line{Text: text, Kind: kind, Indent: indent, Href: href})
	}
	abs := func(href string) string {
		if href == "" || strings.HasPrefix(href, "#") || strings.HasPrefix(strings.ToLower(href), "javascript:") {
			return ""
		}
		return resolveAgainst(d.base, href)
	}
	walk = func(n *html.Node, indent int) {
		if n.Type == html.ElementNode {
			if d.pruned(n) {
				return
			}
			switch n.Data {
			case "h1", "h2", "h3":
				emitText(n.Data, innerText(n), indent, "")
				return
			case "h4", "h5", "h6":
				emitText("h3", innerText(n), indent, "")
				return
			case "p", "blockquote", "figcaption", "dt", "dd", "caption", "summary":
				// paragraphs keep inline links as separate link lines after the text
				emitText("p", innerText(n), indent, "")
				for _, a := range collectLinks(n) {
					if h := abs(attr(a, "href")); h != "" {
						emitText("link", innerText(a), indent+1, h)
					}
				}
				return
			case "li":
				emitText("li", directText(n), indent, "")
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					if c.Type == html.ElementNode {
						switch c.Data {
						case "ul", "ol", "a":
							walk(c, indent+1)
						}
					}
				}
				return
			case "a":
				if h := abs(attr(n, "href")); h != "" {
					emitText("link", innerText(n), indent, h)
					return
				}
			case "pre", "code":
				if n.Data == "pre" || n.Parent == nil || n.Parent.Data != "pre" {
					var sb strings.Builder
					var raw func(*html.Node)
					raw = func(x *html.Node) {
						if x.Type == html.TextNode {
							sb.WriteString(x.Data)
						}
						for c := x.FirstChild; c != nil; c = c.NextSibling {
							raw(c)
						}
					}
					raw(n)
					if t := strings.TrimRight(sb.String(), "\n"); strings.TrimSpace(t) != "" {
						if n.Data == "code" && !strings.Contains(t, "\n") {
							emitText("p", t, indent, "")
						} else {
							out = append(out, pdf.Line{Text: t, Kind: "pre", Indent: indent})
						}
					}
					return
				}
			case "hr":
				out = append(out, pdf.Line{Kind: "hr"})
				return
			case "input", "textarea", "select":
				label := accessibleName(n)
				v := inputValue(n)
				switch strings.ToLower(attr(n, "type")) {
				case "submit", "button", "reset":
					emitText("button", label, indent, "")
					return
				case "checkbox", "radio":
					mark := "[ ]"
					if hasAttr(n, "checked") {
						mark = "[x]"
					}
					emitText("p", mark+" "+label, indent, "")
					return
				}
				txt := label
				if v != "" {
					txt = strings.TrimSpace(label + ": " + v)
				}
				if txt == "" {
					txt = " "
				}
				out = append(out, pdf.Line{Text: txt, Kind: "input", Indent: indent})
				return
			case "button":
				emitText("button", innerText(n), indent, "")
				return
			case "tr":
				var cells []string
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					if c.Type == html.ElementNode && (c.Data == "td" || c.Data == "th") {
						cells = append(cells, normWS(innerText(c)))
					}
				}
				emitText("p", strings.Join(cells, "  |  "), indent, "")
				return
			case "img":
				if alt := attr(n, "alt"); alt != "" {
					emitText("p", "[image: "+alt+"]", indent, "")
				}
				return
			case "ul", "ol":
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					walk(c, indent)
				}
				return
			}
		}
		if n.Type == html.TextNode && n.Parent != nil {
			switch n.Parent.Data {
			case "body", "div", "section", "article", "main", "span", "td", "form", "label", "header", "footer", "nav", "aside":
				emitText("p", n.Data, indent, "")
			}
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c, indent)
		}
	}
	walk(d.Root, 0)
	return out
}

func collectLinks(n *html.Node) []*html.Node {
	var out []*html.Node
	var walk func(*html.Node)
	walk = func(x *html.Node) {
		if x.Type == html.ElementNode && x.Data == "a" && hasAttr(x, "href") {
			out = append(out, x)
			return
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walk(c)
	}
	return out
}

// directText is the text of n excluding nested lists.
func directText(n *html.Node) string {
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(x *html.Node) {
		if x.Type == html.ElementNode && (x.Data == "ul" || x.Data == "ol" || x.Data == "script" || x.Data == "style") {
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
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walk(c)
	}
	return sb.String()
}

// resolveAgainst resolves href against base, returning "" for unusable input.
func resolveAgainst(base, href string) string {
	b, err := url.Parse(base)
	if err != nil {
		return ""
	}
	r, err := url.Parse(strings.TrimSpace(href))
	if err != nil {
		return ""
	}
	return b.ResolveReference(r).String()
}

// BaseURL is the URL the document was parsed against.
func (d *Doc) BaseURL() string { return d.base }
