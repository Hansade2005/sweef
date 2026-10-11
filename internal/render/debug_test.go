package render

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func collectSheets(n *html.Node, out *[]Sheet) {
	if n.Type == html.ElementNode && n.Data == "style" {
		var sb strings.Builder
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.TextNode {
				sb.WriteString(c.Data)
			}
		}
		*out = append(*out, Sheet{Text: sb.String(), Media: attr(n, "media")})
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		collectSheets(c, out)
	}
}

// TestDump prints the layout of $SWEEF_DUMP (a file) for debugging.
func TestDump(t *testing.T) {
	p := os.Getenv("SWEEF_DUMP")
	if p == "" {
		t.Skip()
	}
	src, _ := os.ReadFile(p)
	doc, _ := html.Parse(strings.NewReader(string(src)))
	var sheets []Sheet
	collectSheets(doc, &sheets)
	w, h := 1280.0, 800.0
	if v := os.Getenv("SWEEF_W"); v != "" {
		fmt.Sscan(v, &w)
	}
	res := Layout(doc, Options{Width: w, Height: h, DPR: 1, Sheets: sheets})
	fmt.Printf("doc %vx%v viewport %v\n", res.Width, res.Height, res.Viewport)
	var walk func(n *html.Node, d int)
	walk = func(n *html.Node, d int) {
		if n.Type == html.ElementNode {
			if r, ok := res.ClientRect(n); ok {
				fmt.Printf("%s<%s class=%q> %.2f %.2f %.2f %.2f\n", strings.Repeat(" ", d), n.Data, attr(n, "class"), r.X, r.Y, r.W, r.H)
			} else {
				fmt.Printf("%s<%s> none\n", strings.Repeat(" ", d), n.Data)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c, d+1)
		}
	}
	walk(doc, 0)
}
