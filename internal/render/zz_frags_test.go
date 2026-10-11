package render

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func TestZZFrags(t *testing.T) {
	p := os.Getenv("ZZ_FILE")
	if p == "" {
		t.Skip()
	}
	src, _ := os.ReadFile(p)
	doc, _ := html.Parse(strings.NewReader(string(src)))
	var sheets []Sheet
	collectSheets(doc, &sheets)
	res := Layout(doc, Options{Width: 1280, Height: 800, DPR: 1, Sheets: sheets})
	var walk func(n *html.Node)
	i := 0
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if b := res.Boxes[n]; b != nil {
				fmt.Printf("%d %s", i, n.Data)
				if len(b.Frags) > 0 {
					fmt.Printf(" frags")
					for _, f := range b.Frags {
						fmt.Printf(" [%.2f %.2f %.2f %.2f]", f.X, f.Y, f.W, f.H)
					}
				} else {
					fmt.Printf(" box [%.2f %.2f %.2f %.2f]", b.Border.X, b.Border.Y, b.Border.W, b.Border.H)
				}
				fmt.Println()
			}
			i++
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
}
