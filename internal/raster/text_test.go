package raster

import (
	"math"
	"testing"

	"github.com/sweefbrowser/sweef/internal/font"
)

func liberation(t *testing.T, family string) *font.Face {
	f := font.Lookup(family, font.Style{Weight: 400})
	if f == nil || f.Family != "liberation sans" && f.Family != "liberation serif" {
		t.Skipf("%s not installed", family)
	}
	return f
}

// The expected rows are Chromium's glyph coverage for an 'H' of 16px
// Liberation Sans before Skia's preblend table: stems on the 26.6 grid are
// exact horizontally and the crossbar edges are snapped to quarter pixels.
func TestGlyphCoverageMatchesSkiaAAA(t *testing.T) {
	f := liberation(t, "Liberation Sans")
	m := skiaGlyph(Glyph{R: 'H', Face: f}, 16, 16, 0, false)
	if m == nil || m.B.H() != 11 || m.B.W() != 10 {
		t.Fatalf("mask bounds %+v", m)
	}
	rows := map[int][]uint8{
		-11: {175, 207, 0, 0, 0, 0, 0, 60, 255, 64}, // stems
		-7:  {175, 219, 64, 64, 64, 64, 64, 109, 255, 64},
		-6:  {175, 255, 255, 255, 255, 255, 255, 255, 255, 64},
	}
	for y, want := range rows {
		got := m.Row(y)
		for i := range want {
			if d := int(got[i]) - int(want[i]); d < -1 || d > 1 {
				t.Errorf("row %d: got %v want %v", y, got, want)
				break
			}
		}
	}
}

func TestQuarterPixelSubpositions(t *testing.T) {
	f := liberation(t, "Liberation Sans")
	a := skiaGlyph(Glyph{R: 'l', Face: f}, 16, 16, 0, false)
	b := skiaGlyph(Glyph{R: 'l', Face: f}, 16, 16, 2, false)
	if a == nil || b == nil || a.B.X0 == b.B.X0 && string(a.A) == string(b.A) {
		t.Fatal("subpixel offset has no effect")
	}
	sum := func(m *Mask) (s int) {
		for _, v := range m.A {
			s += int(v)
		}
		return
	}
	if d := sum(a) - sum(b); d < -8 || d > 8 {
		t.Fatalf("shifting changed total ink: %d vs %d", sum(a), sum(b))
	}
}

func TestPreblendTables(t *testing.T) {
	black, white := lutForLum(0), lutForLum(255)
	if black[0] != 0 || black[255] != 255 || white[0] != 0 || white[255] != 255 {
		t.Fatal("endpoints must be fixed")
	}
	if !(black[128] < 128 && white[128] > 128) {
		t.Fatalf("dark text gets lighter and light text heavier: %d %d", black[128], white[128])
	}
	for i := 1; i < 256; i++ {
		if black[i] < black[i-1] || white[i] < white[i-1] {
			t.Fatalf("not monotonic at %d", i)
		}
	}
	if textLum(Color{0, 0, 0, 1}) != 0 || textLum(Color{1, 1, 1, 1}) != 255 {
		t.Fatal("luminance of black/white")
	}
}

func TestCanvasTextBaselineUsesTypoMetrics(t *testing.T) {
	f := font.Lookup("DejaVu Sans", font.Style{Weight: 400})
	if f == nil || f.Family != "dejavu sans" {
		t.Skip("DejaVu Sans not installed")
	}
	c := NewContext(10, 10)
	c.SetFont(`16px "DejaVu Sans"`)
	// values from Chromium 1194 measureText on the reference machine
	for _, tc := range []struct {
		baseline string
		alpha    float64
	}{{"top", -12.15625}, {"middle", -4.15625}, {"bottom", 3.84375}, {"alphabetic", 0}, {"hanging", -12}} {
		c.SetTextBaseline(tc.baseline)
		if got := c.MeasureText("top").Alphabetic; got != tc.alpha {
			t.Errorf("%s: alphabeticBaseline %v, Chromium %v", tc.baseline, got, tc.alpha)
		}
	}
}

func TestShapeLigaturesAndKerning(t *testing.T) {
	f := font.Lookup("DejaVu Sans", font.Style{Weight: 400})
	if f == nil || f.Family != "dejavu sans" {
		t.Skip("DejaVu Sans not installed")
	}
	spec, _ := ParseFont(`100px "DejaVu Sans"`, 16)
	if w := Shape(spec, "fi", 0, 0, true).Width; w < 62.9 || w > 63.1 {
		t.Errorf("fi width %v, Chromium 62.99", w)
	}
	// letter-spacing disables ligatures but keeps kerning
	lig := Shape(spec, "fi", 0, 0, true)
	if len(lig.Glyphs) != 1 || lig.Glyphs[0].GID == 0 {
		t.Errorf("fi must be one ligature glyph: %+v", lig.Glyphs)
	}
	if sp := Shape(spec, "fi", 1, 0, true); len(sp.Glyphs) != 2 {
		t.Errorf("letter-spacing must split the ligature: %+v", sp.Glyphs)
	}
	// canvas shapes word by word: no kerning across a space
	if a, b := Shape(spec, "A V", 0, 0, true).Width, Shape(spec, "A", 0, 0, true).Width+Shape(spec, " ", 0, 0, true).Width+Shape(spec, "V", 0, 0, true).Width; a != b {
		t.Errorf("kerning across a space: %v vs %v", a, b)
	}
}

func TestXfPlacementRules(t *testing.T) {
	// a rotation by 90 degrees is an exact axis swap after sk_relax
	c, s := math.Cos(math.Pi/2), math.Sin(math.Pi/2)
	if got := xfAxis(c, s, -s, c); got != 2 {
		t.Errorf("rot90 axis %d, want 2 (subpixel along y only)", got)
	}
	if got := xfAxis(-1, math.Sin(math.Pi), -math.Sin(math.Pi), -1); got != 1 {
		t.Errorf("rot180 axis %d, want 1", got)
	}
	if got := xfAxis(math.Cos(0.1), math.Sin(0.1), -math.Sin(0.1), math.Cos(0.1)); got != 0 {
		t.Errorf("rot0.1 rad axis %d, want 0", got)
	}
	// both axes: +1/8 then truncate to a quarter; one axis rounds to a pixel
	ix, iy, sx, sy := xfPlace(10.37, 20.62, 0)
	if ix != 10 || sx != 1 || iy != 20 || sy != 2 {
		t.Errorf("place both axes = %d.%d %d.%d", ix, sx, iy, sy)
	}
	ix, iy, sx, sy = xfPlace(10.37, 20.62, 1)
	if ix != 10 || sx != 1 || iy != 21 || sy != 0 {
		t.Errorf("place kX = %d.%d %d.%d", ix, sx, iy, sy)
	}
}

func TestRotatedGlyphMask(t *testing.T) {
	f := font.Lookup("Liberation Sans", font.Style{Weight: 400})
	if f == nil {
		t.Skip("no Liberation Sans")
	}
	g := Glyph{R: 'L', Face: f}
	// rotated by 90 degrees the L's stem lies along x: the mask is the
	// transposed, flipped identity mask up to quarter-pixel placement
	id := xfGlyphMask(g, 24, 1, 0, 0, 1, false, false, 0, 0)
	r := xfGlyphMask(g, 24, math.Cos(math.Pi/2), math.Sin(math.Pi/2), -math.Sin(math.Pi/2), math.Cos(math.Pi/2), false, false, 0, 0)
	if id == nil || r == nil {
		t.Fatal("no mask")
	}
	if id.B.W() < 3 || r.B.H() < 3 || abs(id.B.W()-r.B.H()) > 1 || abs(id.B.H()-r.B.W()) > 1 {
		t.Errorf("rot90 bounds %v vs identity %v are not swapped", r.B, id.B)
	}
	// big glyphs come out as paths and still have ink
	big := xfPathGlyph(g, 300, Matrix{1, 0, 0, 1, 10, 300}, false, 0, 0)
	if m := fxFillPath(big, IRect{0, 0, 400, 400}); m == nil || m.At(10+40, 250) == 0 {
		t.Errorf("large glyph path has no ink in the stem")
	}
}

func abs(a int) int {
	if a < 0 {
		return -a
	}
	return a
}
