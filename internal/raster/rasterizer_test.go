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

func solidCtx(w, h int) *Context {
	c := NewContext(w, h)
	return c
}

func px(c *Context, x, y int) [4]uint8 {
	d := c.GetImageData(x, y, 1, 1)
	return [4]uint8{d[0], d[1], d[2], d[3]}
}

func TestBlendModesNumeric(t *testing.T) {
	// backdrop rgb(128,0,0); source rgb(255,0,0)
	for _, tc := range []struct {
		op   string
		want uint8 // red channel
	}{{"multiply", 128}, {"screen", 255}, {"difference", 127}, {"darken", 128}, {"lighten", 255}, {"source-over", 255}, {"lighter", 255}, {"xor", 0}, {"source-in", 255}} {
		c := solidCtx(2, 2)
		c.SetFill(Brush{Color: RGBA8(128, 0, 0, 255)})
		c.FillRect(0, 0, 2, 2)
		op, _ := ParseOp(tc.op)
		c.SetOp(op)
		c.SetFill(Brush{Color: RGBA8(255, 0, 0, 255)})
		c.FillRect(0, 0, 2, 2)
		got := px(c, 0, 0)[0]
		if d := int(got) - int(tc.want); d < -1 || d > 1 {
			t.Errorf("%s: red=%d want %d", tc.op, got, tc.want)
		}
	}
}

func TestFilters(t *testing.T) {
	c := solidCtx(4, 4)
	c.SetFill(Brush{Color: RGBA8(255, 0, 0, 255)})
	c.FillRect(0, 0, 4, 4)
	fl, ok := ParseFilterList("grayscale(1)", Black)
	if !ok {
		t.Fatal("parse")
	}
	ApplyFilters(c.Surface(), fl)
	p := px(c, 1, 1)
	if p[0] != p[1] || p[1] != p[2] || p[0] < 50 || p[0] > 60 {
		t.Fatalf("grayscale red = %v", p)
	}
	if _, ok := ParseFilterList("blur(2px) drop-shadow(1px 2px 3px red) hue-rotate(.5turn)", Black); !ok {
		t.Fatal("parse list")
	}
	if _, ok := ParseFilterList("bogus(1)", Black); ok {
		t.Fatal("accepted bogus")
	}
}

func TestBlurConservesMass(t *testing.T) {
	c := solidCtx(64, 64)
	c.SetFill(Brush{Color: RGBA8(0, 0, 0, 255)})
	c.FillRect(28, 28, 8, 8)
	var before int
	for _, v := range c.Surface().Pix {
		before += int(v)
	}
	blurPlane(c.Surface().Pix, 64, 64, c.Surface().Stride, 4, 5, 5)
	var after int
	for _, v := range c.Surface().Pix {
		after += int(v)
	}
	if d := float64(after-before) / float64(before); d > 0.03 || d < -0.03 {
		t.Fatalf("blur changed mass by %.1f%%", d*100)
	}
	if px(c, 32, 32)[3] >= 200 {
		t.Fatal("blur did not spread")
	}
}

func TestStrokeCoverage(t *testing.T) {
	// a 2px wide horizontal line centred on a pixel row boundary covers two rows at 100%
	p := NewPath()
	p.MoveTo(0, 5)
	p.LineTo(10, 5)
	m := StrokeMask(p, Identity, StrokeStyle{Width: 2, Cap: CapButt, Join: JoinMiter, MiterLimit: 10}, IRect{0, 0, 10, 10})
	if m.At(5, 4) != 255 || m.At(5, 5) != 255 || m.At(5, 3) != 0 || m.At(5, 6) != 0 {
		t.Fatalf("rows: %d %d %d %d", m.At(5, 3), m.At(5, 4), m.At(5, 5), m.At(5, 6))
	}
	// 1px line centred inside a row covers half of two rows... i.e. 128 each
	p = NewPath()
	p.MoveTo(0, 5.5)
	p.LineTo(10, 5.5)
	m = StrokeMask(p, Identity, StrokeStyle{Width: 1, MiterLimit: 10}, IRect{0, 0, 10, 10})
	if m.At(5, 5) != 255 || m.At(5, 4) != 0 {
		t.Fatalf("aligned line: %d %d", m.At(5, 5), m.At(5, 4))
	}
	// dashes: 4 on / 4 off over 16 px
	p = NewPath()
	p.MoveTo(0, 0.5)
	p.LineTo(16, 0.5)
	m = StrokeMask(p, Identity, StrokeStyle{Width: 1, MiterLimit: 10, Dash: []float64{4, 4}}, IRect{0, 0, 16, 2})
	if m.At(1, 0) != 255 || m.At(5, 0) != 0 || m.At(9, 0) != 255 || m.At(13, 0) != 0 {
		t.Fatalf("dash pattern wrong: %d %d %d %d", m.At(1, 0), m.At(5, 0), m.At(9, 0), m.At(13, 0))
	}
}

func TestClipAA(t *testing.T) {
	c := solidCtx(8, 8)
	c.ClipRect(1.5, 0, 4, 8)
	c.SetFill(Brush{Color: RGBA8(0, 0, 0, 255)})
	c.FillRect(0, 0, 8, 8)
	if a := px(c, 1, 3)[3]; a < 126 || a > 129 {
		t.Fatalf("half-covered clip edge alpha = %d", a)
	}
	if px(c, 3, 3)[3] != 255 || px(c, 6, 3)[3] != 0 {
		t.Fatal("clip interior/exterior wrong")
	}
}

func TestGradientPremul(t *testing.T) {
	// CSS premultiplied interpolation from opaque red to transparent blue stays red-ish
	d := GradientDef{Kind: LinearGrad, X0: 0, X1: 10, Premul: true, Stops: []GradStop{{0, Color{1, 0, 0, 1}}, {1, Color{0, 0, 1, 0}}}}
	c := solidCtx(10, 1)
	c.FillRectShader(0, 0, 10, 1, NewGradientShader(d, Identity, 1))
	p := px(c, 5, 0)
	if p[2] > 5 || p[0] < 120 {
		t.Fatalf("premul gradient midpoint = %v", p)
	}
	d.Premul = false
	c = solidCtx(10, 1)
	c.FillRectShader(0, 0, 10, 1, NewGradientShader(d, Identity, 1))
	p = px(c, 5, 0)
	if p[2] < 50 {
		t.Fatalf("straight gradient midpoint = %v", p)
	}
}

func TestSVGPathParse(t *testing.T) {
	p, ok := ParseSVGPath("M10 10 L 20,10 l0 10 H 10 v-10 Z m5 5 a5 5 0 1 0 10 0 q 1 2 3 4 t 5 5 c 1 1 2 2 3 3 s 1 1 2 2")
	if !ok || p.Empty() {
		t.Fatal("parse failed")
	}
	if _, ok := ParseSVGPath("L 10 10"); ok {
		t.Fatal("must start with moveto")
	}
}
