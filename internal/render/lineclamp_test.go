package render

import (
	"strings"
	"testing"

	"golang.org/x/net/html"
)

// -webkit-line-clamp on a vertical -webkit-box keeps the first N lines (Tailwind line-clamp-N).
func TestLineClamp(t *testing.T) {
	src := `<html><body style="margin:0"><div id="c" style="width:120px;font:16px/20px sans-serif;overflow:hidden;display:-webkit-box;-webkit-box-orient:vertical;-webkit-line-clamp:2">one two three four five six seven eight nine ten eleven twelve thirteen fourteen</div>
<div id="f" style="width:120px;font:16px/20px sans-serif">one two three four five six seven eight nine ten eleven twelve thirteen fourteen</div></body></html>`
	doc, _ := html.Parse(strings.NewReader(src))
	res := Layout(doc, Options{Width: 400, Height: 300})
	h := map[string]float64{}
	for n, b := range res.Boxes {
		for _, a := range n.Attr {
			if a.Key == "id" {
				h[a.Val] = b.Border.H
			}
		}
	}
	if h["c"] != 40 || h["f"] <= 40 {
		t.Fatalf("clamped %v, free %v", h["c"], h["f"])
	}
}
