package font

import (
	"math"
	"os"
	"testing"
)

func TestMatchAndMeasure(t *testing.T) {
	f := Get(Style{Families: []string{"Arial", "sans-serif"}, Size: 16})
	if f.Family() == "" {
		t.Fatal("no family")
	}
	w := f.Measure("Hello world")
	if w < 50 || w > 120 {
		t.Fatalf("implausible width %v for %s", w, f.Family())
	}
	m := f.Metrics()
	if m.Ascent <= 0 || m.Descent <= 0 || m.LineHeight < 16 {
		t.Fatalf("bad metrics %+v", m)
	}
	if mk := f.GlyphMask(f.GlyphIndex('H'), 0, 1); mk == nil || mk.Alpha.Bounds().Dx() == 0 {
		t.Fatal("no mask")
	}
	b := Get(Style{Families: []string{"Arial"}, Weight: 700, Size: 16})
	if b.Measure("Hello world") <= w {
		t.Fatal("bold should be wider")
	}
}

func TestWeightDistance(t *testing.T) {
	if weightDistance(400, 500) >= weightDistance(400, 300) {
		t.Fatal("400 prefers 500 before lighter")
	}
	if weightDistance(700, 900) >= weightDistance(700, 600) {
		t.Fatal("700 prefers heavier first")
	}
	_ = math.Abs
}

func TestRegisterWebFont(t *testing.T) {
	data, err := os.ReadFile("/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf")
	if err != nil {
		t.Skip("no test font")
	}
	if err := RegisterWebFont("My Brand Font", 400, false, data); err != nil {
		t.Fatal(err)
	}
	f := Get(Style{Families: []string{"My Brand Font", "serif"}, Size: 16})
	if !HasWebFont("my brand font") || f.Measure("iiii") != f.Measure("MMMM") {
		t.Fatalf("web font not used: %s", f.Family())
	}
	if RegisterWebFont("Bad", 400, false, []byte("nope")) == nil {
		t.Fatal("garbage accepted")
	}
}
