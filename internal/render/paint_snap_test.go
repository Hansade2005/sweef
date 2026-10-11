package render

import (
	"math"
	"testing"
)

// The transform of a box with a fractional position is applied about the
// rounded origin and the box's content snaps on its own lattice.
func TestPaintOffsetSnap(t *testing.T) {
	doc := parseHTML(t, `<body style="margin:0"><div id="a" style="position:absolute;left:10.3px;top:20.6px;width:30px;height:16px;background:#f00;transform:scale(2);transform-origin:0 0"><i style="position:absolute;left:5.4px;top:5.6px;width:10px;height:5px;background:#00f"></i></div>
<div id="b" style="position:absolute;left:100.3px;top:20.3px;width:30px;height:16px;background:#f00;transform:translate(0.4px,0.4px)"></div>
<div id="c" style="position:absolute;left:200.5px;top:20.5px;width:30px;height:16px;background:#f00;will-change:transform"></div></body>`)
	res := Layout(doc, Options{Width: 400, Height: 200})
	type frame struct {
		m          [6]float64
		lat        bool
		latX, latY float64
	}
	var frames []frame
	for _, op := range res.List {
		if p, ok := op.(PushTransform); ok {
			frames = append(frames, frame{p.M, p.Lattice, p.LatX, p.LatY})
		}
	}
	if len(frames) != 3 {
		t.Fatalf("got %d transform frames", len(frames))
	}
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-9 }
	// scale(2) about the rounded origin (10, 21): the point (10.3, 20.6) maps to (10, 21)
	a := frames[0]
	if x, y := a.m[0]*10.3+a.m[4], a.m[3]*20.6+a.m[5]; !near(x, 10) || !near(y, 21) || !a.lat || a.latX != 10.3 || a.latY != 20.6 {
		t.Errorf("scaled frame maps the origin to (%v,%v), lattice %v (%v,%v)", x, y, a.lat, a.latX, a.latY)
	}
	// translate-only keeps the content lattice of the parent and adds the translation
	b := frames[1]
	if !near(b.m[4], 0.4) || !near(b.m[5], 0.4) || b.latX != 0 || b.latY != 0 {
		t.Errorf("translate frame %+v", b)
	}
	// will-change: transform rounds the origin and opens a lattice without a matrix
	c := frames[2]
	if !near(c.m[4], 0.5) || !near(c.m[5], 0.5) || c.latX != 200.5 {
		t.Errorf("will-change frame %+v", c)
	}
}

func TestTranslationOnly(t *testing.T) {
	for _, m := range [][6]float64{{1, 0, 0, 1, 5, 5}, {1, 1e-12, 0, 1, 0, 0}, {math.Cos(2 * math.Pi), math.Sin(2 * math.Pi), -math.Sin(2 * math.Pi), math.Cos(2 * math.Pi), 0, 0}} {
		if !translationOnly(m) {
			t.Errorf("%v should be translation-only", m)
		}
	}
	for _, m := range [][6]float64{{1.001, 0, 0, 1, 0, 0}, {0, 1, -1, 0, 0, 0}, {1, 0, 0.1, 1, 0, 0}} {
		if translationOnly(m) {
			t.Errorf("%v should not be translation-only", m)
		}
	}
}
