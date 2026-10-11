package render

import (
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func layoutHTML(t *testing.T, src string) *Result {
	t.Helper()
	doc, _ := html.Parse(strings.NewReader(src))
	var sheets []Sheet
	collectSheets(doc, &sheets)
	return Layout(doc, Options{Width: 400, Height: 300, DPR: 1, Sheets: sheets})
}

func textRuns(res *Result) []TextRun {
	var out []TextRun
	for _, op := range res.List {
		if tr, ok := op.(TextRun); ok {
			out = append(out, tr)
		}
	}
	return out
}

func TestLigatureGlyph(t *testing.T) {
	res := layoutHTML(t, `<body style="margin:0;font:20px 'DejaVu Sans'"><p>office</p></body>`)
	n := 0
	for _, tr := range textRuns(res) {
		for _, g := range tr.Glyphs {
			if g.GID != 0 {
				n++
			}
		}
	}
	if n != 1 { // "ffi" is one ligature glyph in DejaVu Sans
		t.Fatalf("want 1 ligature glyph, got %d", n)
	}
	res = layoutHTML(t, `<body style="margin:0;font:20px 'DejaVu Sans';letter-spacing:1px"><p>office</p></body>`)
	for _, tr := range textRuns(res) {
		for _, g := range tr.Glyphs {
			if g.GID != 0 {
				t.Fatal("letter-spacing must disable ligatures")
			}
		}
	}
	res = layoutHTML(t, `<body style="margin:0;font:20px 'DejaVu Sans';font-variant-ligatures:none"><p>office</p></body>`)
	for _, tr := range textRuns(res) {
		for _, g := range tr.Glyphs {
			if g.GID != 0 {
				t.Fatal("font-variant-ligatures: none must disable ligatures")
			}
		}
	}
}

func TestRTLVisualOrder(t *testing.T) {
	res := layoutHTML(t, `<body style="margin:0;font:20px 'DejaVu Sans'" dir="rtl"><p>abc אבג def</p></body>`)
	runs := textRuns(res)
	if len(runs) < 3 {
		t.Fatalf("want 3 text runs, got %d", len(runs))
	}
	// visual order left to right: "def" run, the Hebrew run, "abc" run
	var xs [3]float64
	for _, tr := range runs {
		switch tr.Glyphs[0].R {
		case 'a':
			xs[0] = tr.X
		case 'd':
			xs[2] = tr.X
		default:
			xs[1] = tr.X
		}
	}
	if !(xs[2] < xs[1] && xs[1] < xs[0]) {
		t.Fatalf("rtl paragraph must show def, hebrew, abc left to right; x = %v", xs)
	}
}
