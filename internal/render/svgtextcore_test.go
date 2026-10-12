package render

import (
	"math"
	"testing"
)

func near(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

// Reference positions come from Chromium 1194 (getStartPositionOfChar) for
// 20px Liberation Sans on path M20 50 L220 50 L220 150.
func TestLayoutSVGTextOnPath(t *testing.T) {
	path := func(start float64) *SVGTextPath {
		sp := NewSVGTextPolyline([][][2]float64{{{20, 50}, {220, 50}, {220, 150}}})
		sp.Start = start
		return sp
	}
	tp := &SVGNode{Tag: "textpath"}
	advs := []float64{13.34375, 13.34375, 14.453125, 14.453125, 13.34375}
	mk := func(pm *SVGTextPath, n int, dy float64) []SVGTextIn {
		var in []SVGTextIn
		for i := 0; i < n; i++ {
			c := SVGTextIn{SVGTextChar: SVGTextChar{R: 'A' + rune(i), Path: tp}, Adv: advs[i], PathM: pm}
			if i == 0 {
				c.DY = dy
			}
			in = append(in, c)
		}
		return in
	}
	got := LayoutSVGText(mk(path(0), 3, 0), nil)
	for i, want := range [][2]float64{{20, 50}, {33.34, 50}, {46.69, 50}} {
		if !near(got[i].X, want[0], 0.02) || !near(got[i].Y, want[1], 0.02) || got[i].Hidden || !near(got[i].Angle, 0, 1e-9) {
			t.Errorf("char %d: %+v want %v", i, got[i], want)
		}
	}
	// start 190, dy 30: crosses the corner and turns 90 degrees
	got = LayoutSVGText(mk(path(190), 5, 30), nil)
	want := [][3]float64{{210, 80, 0}, {190, 53.34, 90}, {190, 66.69, 90}, {190, 81.14, 90}, {190, 95.59, 90}}
	for i, w := range want {
		if !near(got[i].X, w[0], 0.02) || !near(got[i].Y, w[1], 0.02) || !near(got[i].Angle*180/math.Pi, w[2], 0.01) {
			t.Errorf("corner char %d: %+v want %v", i, got[i], w)
		}
	}
	// a glyph whose midpoint lies before the path start is not rendered
	got = LayoutSVGText(mk(path(-15), 3, 0), nil)
	if !got[0].Hidden || got[1].Hidden || !near(got[1].X, 18.34, 0.02) {
		t.Errorf("negative startOffset: %+v", got)
	}
}

func TestLayoutSVGTextChunksAndLength(t *testing.T) {
	in := []SVGTextIn{
		{SVGTextChar: SVGTextChar{R: 'a', HasX: true, X: 100, HasY: true, Y: 10}, Adv: 10, Anchor: 1},
		{SVGTextChar: SVGTextChar{R: 'b'}, Adv: 10, Anchor: 1},
		{SVGTextChar: SVGTextChar{R: 'c', HasX: true, X: 200}, Adv: 20, Anchor: 2},
	}
	g := LayoutSVGText(in, nil)
	// first chunk is 20 wide, middle anchored; second chunk 20 wide, end anchored
	if g[0].X != 90 || g[1].X != 100 || g[2].X != 180 || g[2].Y != 10 {
		t.Errorf("anchors: %+v", g)
	}
	// textLength spacing: 3 chars of 10 stretched to 60 -> two gaps of 15
	in = []SVGTextIn{{Adv: 10}, {Adv: 10}, {Adv: 10}}
	g = LayoutSVGText(in, []SVGTextLen{{From: 0, To: 3, Length: 60}})
	if g[1].X != 25 || g[2].X != 50 {
		t.Errorf("spacing: %+v", g)
	}
	// spacingAndGlyphs scales the advances and the glyphs
	g = LayoutSVGText(in, []SVGTextLen{{From: 0, To: 3, Length: 60, Glyphs: true}})
	if g[1].X != 20 || g[2].X != 40 || g[1].ScaleX != 2 {
		t.Errorf("glyph scaling: %+v", g)
	}
}

func TestCollectSVGTextLists(t *testing.T) {
	root := &SVGNode{Tag: "text", A: map[string]string{"x": "10 20 30", "rotate": "5 10", "dx": "1"}, Kids: []*SVGNode{
		{Tag: "#text", Text: "  ab  "},
		{Tag: "tspan", A: map[string]string{"y": "50", "dy": "2 3"}, Kids: []*SVGNode{{Tag: "#text", Text: "cd "}}},
		{Tag: "#text", Text: " e"},
	}}
	chars, _ := CollectSVGText(root, func(n *SVGNode, s string, axis byte) float64 { return svgNum(s, 0) })
	var s string
	for _, c := range chars {
		s += string(c.R)
	}
	if s != "ab cd e" {
		t.Fatalf("collapsed text %q", s)
	}
	if !chars[0].HasX || chars[0].X != 10 || chars[2].X != 30 || chars[3].HasX {
		t.Errorf("x list: %+v", chars)
	}
	if chars[0].Rot != 5 || chars[1].Rot != 10 || chars[6].Rot != 10 {
		t.Errorf("rotate repeats the last value: %v %v %v", chars[0].Rot, chars[1].Rot, chars[6].Rot)
	}
	if !chars[3].HasY || chars[3].Y != 50 || chars[3].DY != 2 || chars[4].DY != 3 || chars[4].HasY {
		t.Errorf("tspan lists: %+v", chars[3:6])
	}
}
