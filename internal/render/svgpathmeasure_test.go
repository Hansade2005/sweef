package render

import (
	"math"
	"testing"
)

// Positions on paths measured in Chromium 1194: a zero-width character on a
// <textPath> with startOffset s reports the point at distance s (Skia's
// contour measure, conics for arcs) through getStartPositionOfChar.
func TestSVGTextPathMeasureMatchesChromium(t *testing.T) {
	type pt struct{ s, x, y float64 }
	cases := []struct {
		name, d string
		pts     []pt
	}{
		{"s1", "M 50 100 C 100 20 150 20 200 100 S 300 180 350 100", []pt{
			{0, 50, 100},
			{85, 110.319, 42.2991},
			{170, 182.685, 75.4942},
			{255, 234.713, 142.687},
			{340, 311.665, 145.661},
		}},
		{"s2", "M 50 100 Q 100 20 150 100 T 250 100 T 350 100", []pt{
			{0, 50, 100},
			{85, 117.466, 64.8812},
			{170, 173.142, 128.458},
			{255, 243.569, 109.628},
			{340, 306.742, 60.7272},
		}},
		{"s4", "m 50 50 c 20 -30 60 -30 80 0 s 60 30 80 0 q 20 -40 40 0 t 40 0", []pt{
			{0, 50, 50},
			{85, 123.376, 41.835},
			{170, 195.33, 64.7962},
			{255, 252.57, 54.8091},
		}},
		{"s7", "M 100 100 A 40 20 45 1 1 180 140 L 250 250 a 30 30 0 0 0 40 40", []pt{
			{0, 100, 100},
			{85, 162.318, 106.536},
			{170, 205.086, 179.421},
			{255, 249.464, 251.188},
		}},
		{"s8", "M 50 300 C 50 100 400 100 400 300 C 400 500 50 500 50 300 Z", []pt{
			{0, 50, 300},
			{85, 73.778, 219.701},
			{170, 139.311, 167.286},
			{255, 222.008, 150.019},
			{340, 305.159, 164.966},
			{425, 372.718, 214.84},
		}},
	}
	for _, c := range cases {
		p := NewSVGTextPath(c.d, 1)
		for _, q := range c.pts {
			x, y, _, _, ok := p.At(q.s)
			if !ok {
				t.Errorf("%s: no point at %g", c.name, q.s)
				continue
			}
			if math.Hypot(x-q.x, y-q.y) > 2e-3 {
				t.Errorf("%s at %g: got (%.4f, %.4f) want (%.4f, %.4f)", c.name, q.s, x, y, q.x, q.y)
			}
		}
	}
}
