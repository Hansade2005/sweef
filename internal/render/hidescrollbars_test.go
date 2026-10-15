package render

import (
	"strings"
	"testing"

	"golang.org/x/net/html"
)

// With HideScrollbars a tall page keeps the full viewport width (no 15 px gutter), as headless Chromium does.
func TestHideScrollbars(t *testing.T) {
	doc, _ := html.Parse(strings.NewReader(`<html><body style="margin:0"><div id="d" style="height:3000px"></div></body></html>`))
	width := func(hide bool) float64 {
		res := Layout(doc, Options{Width: 400, Height: 300, HideScrollbars: hide})
		var w float64
		for n, b := range res.Boxes {
			if n.Type == html.ElementNode && n.Data == "div" {
				w = b.Border.W
			}
		}
		return w
	}
	if w := width(false); w != 385 {
		t.Fatalf("with scrollbars: %v", w)
	}
	if w := width(true); w != 400 {
		t.Fatalf("hidden scrollbars: %v", w)
	}
}
