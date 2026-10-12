package raster

import (
	"math"
	"testing"

	"github.com/sweefbrowser/sweef/internal/render"
)

// layoutSVGTextString lays out the first <text> of markup the way the
// painter does (scale factor k) and returns the placed glyphs.
func layoutSVGTextString(t *testing.T, markup string, k float64) []textGlyph {
	t.Helper()
	doc := render.ParseSVG(markup)
	if doc == nil || doc.Root == nil {
		t.Fatal("parse failed")
	}
	var n *svgNode
	for _, kid := range doc.Root.Kids {
		if kid.Tag == "text" {
			n = kid
			break
		}
	}
	if n == nil {
		t.Fatal("no text")
	}
	r := &svgR{c: NewContextSurface(NewSurface(64, 64)), doc: doc, active: map[*svgNode]bool{}, vw: 900, vh: 100}
	r.root = baseStyle(render.SVGDraw{FontSize: 16, Family: "Liberation Sans", Weight: 400})
	r.root = r.inherit(r.root, doc.Inherit)
	st := r.inherit(r.inherit(r.root, doc.Root.A), n.A)
	return r.layoutText(n, &st, k)
}

func TestSVGScaledSize(t *testing.T) {
	// font size times the scaling factor, truncated to 1/100 (Chromium
	// 1194: 15 * 1.0326 -> 15.48, 20 * 1.3 -> 26)
	for _, c := range []struct{ fs, k, want float64 }{{15, 1.0325876, 15.48}, {20, 1.3, 26}, {15, 1.071, 16.06}, {12, 1, 12}} {
		if got := svgScaledSize(c.fs, c.k); math.Abs(got-c.want) > 1e-5 {
			t.Errorf("svgScaledSize(%g, %g) = %g, want %g", c.fs, c.k, got, c.want)
		}
	}
	if k := svgScreenScale(Matrix{1, 0, math.Tan(20 * math.Pi / 180), 1, 0, 0}); math.Abs(k-1.0325876) > 1e-6 {
		t.Errorf("skewX(20) scale = %g", k)
	}
}

// Positions of the characters of "Glyph rot" (Liberation Sans 24px, x=30) as
// getStartPositionOfChar reports them in Chromium 1194, in 1/64 px relative
// to the first character. A rotate list makes every character a fragment of
// its own whose width is ceiled to 1/64; a dx list does that for the characters
// it lists; plain text keeps one fragment (the offsets inside are exact and the
// reported ones are floored).
func TestSVGTextFragmentAdvances(t *testing.T) {
	const head = `<svg xmlns="http://www.w3.org/2000/svg" width="900" height="100" font-family="Liberation Sans" font-size="24">`
	check := func(name, attrs string, want []int) {
		g := layoutSVGTextString(t, head+`<text x="30" y="50" `+attrs+`>Glyph rot</text></svg>`, 1)
		if len(g) != 9 {
			t.Fatalf("%s: %d glyphs", name, len(g))
		}
		for i, w := range want {
			got := (g[i+1].x - g[0].x) * 64
			if math.Abs(got-float64(w)) > 1e-6 {
				t.Errorf("%s: char %d at %.4f/64, want %d", name, i+1, got, w)
			}
		}
	}
	check("rotate", `rotate="5"`, []int{1195, 1537, 2305, 3160, 4015, 4442, 4954, 5809})
	// dx on the first three characters: [G] [l] [y] [phrot...] with the last
	// fragment laid out exactly
	check("dx 3", `dx="0 0 0"`, []int{1195, 1537, 2305})
}

func TestSVGTextKerningAcrossSpaces(t *testing.T) {
	// Chromium: "A A T Y" at 16px puts the second glyph 9.78125 after the first
	// (A advance 10.672 less the A-space kern 0.883, floored to 1/64) and the
	// third a further 3.5625 (space advance less the space-A kern).
	g := layoutSVGTextString(t, `<svg xmlns="http://www.w3.org/2000/svg" font-family="Liberation Sans" font-size="16"><text x="10" y="20">A A T Y</text></svg>`, 1)
	if len(g) != 7 {
		t.Fatalf("%d glyphs", len(g))
	}
	if d := g[1].x - g[0].x; math.Abs(d-9.78125) > 0.02 {
		t.Errorf("A to space = %.4f, want 9.78", d)
	}
	if d := g[2].x - g[1].x; math.Abs(d-3.5625) > 0.02 {
		t.Errorf("space to A = %.4f, want 3.56", d)
	}
}

func TestRenderSVGTextHostile(t *testing.T) {
	cases := []string{
		`<svg><path id="p" d="M 0 0 C 1e9 1e9 -1e9 1e9 1e9 -1e9 A 1e-9 1e9 400 1 1 5 5 Q"/><text><textPath href="#p">curve</textPath></text></svg>`,
		`<svg><path id="p" d="M 10 10 A 0 0 0 0 0 1 1 A 5 5 0 0 0 1 1 Z Z M"/><text><textPath href="#p" startOffset="-5%">x</textPath></text></svg>`,
		`<svg><text writing-mode="tb" rotate="1e30 nan" x="1e30" textLength="0" lengthAdjust="spacingAndGlyphs">vertical</text></svg>`,
		`<svg><text style="writing-mode:vertical-rl;text-orientation:upright" dx="1e308 -1e308">abc<tspan dy="1e308">d</tspan></text></svg>`,
		`<svg transform="scale(1e-30)"><g transform="matrix(1e30 0 0 1e-30 0 0)"><text rotate="45" x="3">hi</text></g></svg>`,
	}
	for i, m := range cases {
		doc := render.ParseSVG(m)
		if doc == nil {
			t.Errorf("case %d: nil doc", i)
			continue
		}
		_ = RenderSVG(doc, 32, 32, render.Color{A: 255})
	}
}

// Right-to-left text is shaped as a whole (reordered and joined) when painted.
func TestRenderSVGTextRTL(t *testing.T) {
	s := renderSVGString(t, `<svg xmlns="http://www.w3.org/2000/svg" width="120" height="40" font-family="DejaVu Sans" font-size="24"><text x="10" y="30">مرحبا</text></svg>`, 120, 40)
	ink := 0
	for y := 0; y < 40; y++ {
		for x := 0; x < 120; x++ {
			if c := svgPx(s, x, y); c[3] > 128 {
				ink++
			}
		}
	}
	if ink < 100 {
		t.Errorf("only %d dark pixels: RTL text did not paint", ink)
	}
}
