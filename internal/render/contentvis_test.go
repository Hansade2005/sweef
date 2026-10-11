package render

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func cvDoc(t *testing.T, css, body string) *html.Node {
	t.Helper()
	return parseHTML(t, `<!doctype html><html><head><style>body{margin:0}`+css+`</style></head><body>`+body+`</body></html>`)
}

func cvLayout(doc *html.Node, o Options) *Result {
	var sheets []Sheet
	collectSheets(doc, &sheets)
	o.Sheets = sheets
	if o.Width == 0 {
		o.Width, o.Height = 800, 400
	}
	return Layout(doc, o)
}

// Absolutely positioned sections keep their position whatever their siblings
// do, so the boundary of relevance is directly visible: Chromium opens an auto
// subtree whose box starts above 2.5 viewport heights (measured: 800x400
// viewport, sections every 100px, the first skipped one is at 1000px).
func TestContentVisibilityAutoViewportMargin(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 20; i++ {
		fmt.Fprintf(&sb, `<section id="s%d" style="top:%dpx"><div>x</div></section>`, i, i*100)
	}
	doc := cvDoc(t, `section{position:absolute;left:0;width:100px;content-visibility:auto;contain-intrinsic-size:auto 50px}section>div{height:100px}`, sb.String())
	res := cvLayout(doc, Options{})
	for i := 0; i < 20; i++ {
		r, _ := res.ClientRect(findByID(doc, fmt.Sprintf("s%d", i)))
		want := 100.0
		if i >= 10 {
			want = 50
		}
		if math.Abs(r.H-want) > 0.01 {
			t.Errorf("section %d (top %d): height %v, want %v", i, i*100, r.H, want)
		}
	}
	// a taller viewport opens more
	res = cvLayout(doc, Options{Width: 800, Height: 800})
	if r, _ := res.ClientRect(findByID(doc, "s19")); r.H != 100 {
		t.Errorf("800px viewport: s19 height %v, want 100", r.H)
	}
	// scrolled viewport opens the sections around it and not those far above
	res = cvLayout(doc, Options{Width: 800, Height: 400, ScrollY: 1500})
	for i, want := range map[int]float64{0: 50, 8: 50, 9: 100, 19: 100} {
		if r, _ := res.ClientRect(findByID(doc, fmt.Sprintf("s%d", i))); r.H != want {
			t.Errorf("scrollY 1500: s%d height %v, want %v", i, r.H, want)
		}
	}
}

// In-flow auto sections shrink when skipped, which pulls later ones into view:
// the fixed point matches Chromium's (section 10 starts at 2001 > 2000).
func TestContentVisibilityAutoFlow(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(`<div style="height:100px"></div>`)
	for i := 0; i < 20; i++ {
		fmt.Fprintf(&sb, `<section id="s%d"><div>x</div></section>`, i)
	}
	doc := cvDoc(t, `section{content-visibility:auto;contain-intrinsic-size:auto 50px;border-bottom:1px solid}section>div{height:200px}`, sb.String())
	res := cvLayout(doc, Options{})
	open := 0
	for i := 0; i < 20; i++ {
		r, _ := res.ClientRect(findByID(doc, fmt.Sprintf("s%d", i)))
		if r.H == 201 {
			open++
		} else if r.H != 51 {
			t.Errorf("section %d height %v", i, r.H)
		}
	}
	if open != 18 {
		t.Errorf("opened %d sections, want 18", open)
	}
	if res.Height != 100+18*201+2*51 {
		t.Errorf("document height %v, want %d", res.Height, 100+18*201+2*51)
	}
}

func TestContentVisibilityHiddenAutoAndHook(t *testing.T) {
	doc := cvDoc(t, `.a{content-visibility:auto;contain-intrinsic-size:auto 40px;margin-top:3000px}.a p{height:90px;margin:0}
.h{content-visibility:hidden;contain-intrinsic-size:60px 30px;background:#eee}.h p{margin:0;height:80px;background:#f00}`,
		`<div id="h" class="h"><p id="hp">hidden</p><p id="hq">two</p></div><div id="a" class="a"><p id="ap">far away</p><p id="aq">two</p></div>`)
	res := cvLayout(doc, Options{})
	if r, _ := res.ClientRect(findByID(doc, "h")); r.H != 30 {
		t.Errorf("hidden uses contain-intrinsic-size: height %v", r.H)
	}
	// descendants keep their normal flow geometry (forced layout)
	if r, _ := res.ClientRect(findByID(doc, "hq")); r.Y != 80 || r.H != 80 {
		t.Errorf("descendant of hidden box: %v", r)
	}
	if r, _ := res.ClientRect(findByID(doc, "a")); r.H != 40 {
		t.Errorf("far auto box should be skipped: height %v", r.H)
	}
	// skipped contents neither paint nor hit-test
	for _, op := range res.List {
		if f, ok := op.(FillRect); ok && f.Color.R == 255 && f.Color.G == 0 {
			t.Errorf("skipped content painted: %v", f)
		}
	}
	if n := res.Hit(10, 10); n == nil || attr(n, "id") != "h" {
		t.Errorf("hit inside skipped contents returned %v", n)
	}
	// content that is relevant to the user (focus, selection) is not skipped
	opened := cvLayout(doc, Options{CVRelevant: func(n *html.Node) bool { return attr(n, "id") == "a" }})
	if r, _ := opened.ClientRect(findByID(doc, "a")); r.H != 180 {
		t.Errorf("relevant box height %v, want 180", r.H)
	}
}
