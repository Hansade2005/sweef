package style

import (
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func parse(t *testing.T, s string) *html.Node {
	d, err := html.Parse(strings.NewReader(s))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func find(n *html.Node, tag string) *html.Node {
	if n.Type == html.ElementNode && n.Data == tag {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if r := find(c, tag); r != nil {
			return r
		}
	}
	return nil
}

func TestCascade(t *testing.T) {
	doc := parse(t, `<html><head><style>
:root{--gap: 10px}
p.a, #x { color: red; margin: var(--gap) 2em }
p { color: blue !important; font-size: 20px }
div > p:first-child { padding: calc(1px + 2px) }
@media (min-width: 2000px) { p { color: green } }
@media (max-width: 2000px) { p { background: #00ff00 } }
</style></head><body><div><p class=a id=x style="color: yellow">hi</p></div></body></html>`)
	e := NewEngine(&Env{Width: 1280, Height: 800}, []SheetIn{{Text: strings.Join(extractStyles(doc), "\n")}})
	html := find(doc, "html")
	hc := e.Compute(html, nil, "")
	body := e.Compute(find(doc, "body"), hc, "")
	div := e.Compute(find(doc, "div"), body, "")
	p := e.Compute(find(doc, "p"), div, "")
	if p.Color != (Color{0, 0, 255, 255}) {
		t.Errorf("color %v", p.Color)
	}
	if p.Margin[0].V != 10 || p.Margin[1].V != 40 {
		t.Errorf("margin %v", p.Margin)
	}
	if p.Padding[0].V != 3 {
		t.Errorf("padding %v", p.Padding[0])
	}
	if p.BgLayers == nil || p.BgColor != (Color{0, 255, 0, 255}) {
		t.Errorf("bg %v", p.BgColor)
	}
	if body.Margin[0].V != 8 {
		t.Errorf("body margin")
	}
}

func extractStyles(n *html.Node) []string {
	var out []string
	if n.Type == html.ElementNode && n.Data == "style" && n.FirstChild != nil {
		out = append(out, n.FirstChild.Data)
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		out = append(out, extractStyles(c)...)
	}
	return out
}
