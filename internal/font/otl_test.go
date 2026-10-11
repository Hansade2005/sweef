package font

import (
	"math"
	"testing"
)

// Kerning values are Chromium 1194 measurements (canvas measureText of the
// pair minus its parts at 1000px) on the reference machine.
func TestGPOSKerningMatchesChromium(t *testing.T) {
	cases := []struct {
		family string
		a, b   rune
		want   float64 // per 1000 em
	}{
		{"Liberation Serif", 'A', 'V', -128.90625},
		{"Liberation Serif", 'A', 'T', -110.83984375},
		{"Liberation Sans", 'A', 'V', -74.21875},
		{"Liberation Sans", 'A', 'y', -18.06640625},
		{"DejaVu Sans", 'A', 'V', -63.96484375},
		{"DejaVu Sans", 'A', 'A', 27.83203125},
		{"Inter", 'A', 'V', -68.359375},
		{"Inter", 'A', 'A', 14.6484375},
	}
	for _, c := range cases {
		f := Lookup(c.family, Style{Weight: 400})
		if f == nil || f.Family != lower(c.family) {
			t.Logf("%s not installed, skipping", c.family)
			continue
		}
		got := f.KernF(c.a, c.b, 1000)
		if math.Abs(got-c.want) > 0.6 {
			t.Errorf("%s %c%c: kern %.3f, Chromium %.3f", c.family, c.a, c.b, got, c.want)
		}
	}
}

func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

func TestFTSize(t *testing.T) {
	for _, c := range []struct{ in, want float64 }{{16, 16}, {21.6, 21.59375}, {13.3333, 13.328125}, {0.01, 0}, {-3, -3}} {
		if got := FTSize(c.in); got != c.want {
			t.Errorf("FTSize(%v)=%v want %v", c.in, got, c.want)
		}
	}
}

func TestTypoMetrics(t *testing.T) {
	f := Lookup("DejaVu Sans", Style{Weight: 400})
	if f == nil || f.Family != "dejavu sans" {
		t.Skip("DejaVu Sans not installed")
	}
	a, d, ok := f.TypoMetrics(16)
	if !ok || math.Abs(a-12.15625) > 1e-9 || math.Abs(d-3.84375) > 1e-9 {
		t.Fatalf("typo metrics %v %v %v", a, d, ok)
	}
}

func TestLigatureLookupReadsGSUB(t *testing.T) {
	f := Lookup("DejaVu Sans", Style{Weight: 400})
	if f == nil || f.Family != "dejavu sans" {
		t.Skip("DejaVu Sans not installed")
	}
	g := findTable(f.data, f.idx, "GSUB")
	l := newLayout(g, map[string]bool{"liga": true})
	if l == nil {
		t.Fatal("no liga lookups")
	}
	var gs []int
	for _, r := range "fi" {
		gi, _ := f.font.GlyphIndex(&f.buf, r)
		gs = append(gs, int(gi))
	}
	if lg, n := l.ligature(gs); lg == 0 || n != 2 {
		t.Fatalf("fi ligature: %d %d", lg, n)
	}
}

func TestShapeRunMatchesChromium(t *testing.T) {
	// widths at 100px from Chromium canvas measureText on the reference machine
	cases := []struct {
		family, text string
		want         float64
	}{
		{"DejaVu Sans", "fi", 62.98828125},
		{"DejaVu Sans", "ff", 68.896484375},
		{"DejaVu Sans", "ffi", 96.6796875},
		{"DejaVu Sans", "office", 274.365234375},
		{"DejaVu Serif", "ffl", 103.02734375},
		{"DejaVu Serif", "fj", 68.017578125},
		{"Liberation Serif", "fi", 61.083984375},
		{"Liberation Serif", "Ty", 104.1015625},
	}
	for _, c := range cases {
		f := Lookup(c.family, Style{Weight: 400})
		if f == nil || f.Family != lower(c.family) {
			t.Logf("%s not installed, skipping", c.family)
			continue
		}
		gl := f.ShapeRun([]rune(c.text), 100, ShapeOptions{Kern: true, Liga: true})
		last := gl[len(gl)-1]
		if w := last.X + last.Adv; math.Abs(w-c.want) > 0.05 {
			t.Errorf("%s %q: width %.3f, Chromium %.3f (%d glyphs)", c.family, c.text, w, c.want, len(gl))
		}
	}
}
