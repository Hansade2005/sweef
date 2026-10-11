package raster

import "testing"

func TestHalfCoveredPixel(t *testing.T) {
	p := NewPath()
	p.Rect(0.5, 0, 0.5, 1) // covers right half of pixel (0,0)
	m := FillMask(p, Identity, NonZero, IRect{0, 0, 4, 4})
	if got := m.At(0, 0); got != 128 && got != 127 {
		t.Fatalf("half pixel coverage = %d", got)
	}
	if m.At(1, 0) != 0 {
		t.Fatal("neighbour covered")
	}
}

func TestQuarterAndTriangle(t *testing.T) {
	p := NewPath()
	p.Rect(0.5, 0.5, 0.5, 0.5)
	m := FillMask(p, Identity, NonZero, IRect{0, 0, 2, 2})
	if g := m.At(0, 0); g < 63 || g > 65 {
		t.Fatalf("quarter = %d", g)
	}
	// triangle (0,0)-(1,0)-(0,1): half of the pixel
	q := NewPath()
	q.MoveTo(0, 0)
	q.LineTo(1, 0)
	q.LineTo(0, 1)
	q.Close()
	m = FillMask(q, Identity, NonZero, IRect{0, 0, 2, 2})
	if g := m.At(0, 0); g < 127 || g > 128 {
		t.Fatalf("triangle = %d", g)
	}
}

func TestEvenOddHole(t *testing.T) {
	p := NewPath()
	p.Rect(0, 0, 10, 10)
	p.Rect(2, 2, 6, 6) // same winding direction
	nz := FillMask(p, Identity, NonZero, IRect{0, 0, 10, 10})
	eo := FillMask(p, Identity, EvenOdd, IRect{0, 0, 10, 10})
	if nz.At(5, 5) != 255 {
		t.Fatal("nonzero should fill the inner rect")
	}
	if eo.At(5, 5) != 0 || eo.At(1, 1) != 255 {
		t.Fatalf("evenodd hole wrong: %d %d", eo.At(5, 5), eo.At(1, 1))
	}
}

func TestClipBounds(t *testing.T) {
	p := NewPath()
	p.Rect(-100, -100, 1000, 1000)
	m := FillMask(p, Identity, NonZero, IRect{2, 2, 6, 6})
	for y := 2; y < 6; y++ {
		for x := 2; x < 6; x++ {
			if m.At(x, y) != 255 {
				t.Fatalf("(%d,%d)=%d", x, y, m.At(x, y))
			}
		}
	}
}

func TestParseColor(t *testing.T) {
	cases := map[string]string{
		"red": "#ff0000", "#0f0": "#00ff00", "rgb(1,2,3)": "#010203", "rgba(0,0,0,.5)": "rgba(0, 0, 0, 0.5)",
		"hsl(120 100% 50%)": "#00ff00", "rgb(0 0 255 / 50%)": "rgba(0, 0, 255, 0.5)", "hwb(0 0% 0%)": "#ff0000",
		"oklch(0.628 0.2577 29.23)": "#ff0000", "color(display-p3 1 0 0)": "#ff0000", "lab(54.29 80.8 69.89)": "#ff0000",
	}
	for in, want := range cases {
		c, ok := ParseColor(in, Black)
		if !ok {
			t.Errorf("%q failed", in)
			continue
		}
		if got := c.CSS(); got != want {
			t.Errorf("%q = %s want %s", in, got, want)
		}
	}
	if _, ok := ParseColor("nonsense", Black); ok {
		t.Error("accepted nonsense")
	}
}
