package font

import (
	"os"
	"testing"
)

func TestTTCWenQuanYi(t *testing.T) {
	const p = "/usr/share/fonts/truetype/wqy/wqy-zenhei.ttc"
	if _, err := os.Stat(p); err != nil {
		t.Skip("no wenquanyi")
	}
	f := Lookup("wenquanyi zen hei", Style{Weight: 400})
	if f == nil {
		t.Fatal("wenquanyi zen hei not found")
	}
	if !f.HasGlyph('中') || f.AdvanceF('中', 16) <= 0 {
		t.Fatal("CJK glyph missing")
	}
	if len(f.Outline('中', 16)) == 0 {
		t.Fatal("no outline")
	}
}

func TestShapeArabicKeepLength(t *testing.T) {
	in := []rune("لا سلام")
	out := ShapeArabicKeep(in)
	if len(out) != len(in) {
		t.Fatalf("length changed: %d -> %d", len(in), len(out))
	}
	if out[1] != 0 {
		t.Fatalf("alef after lam must be consumed, got %U", out[1])
	}
	if out[0] == in[0] {
		t.Fatal("lam-alef must be the ligature form")
	}
}

func TestLigatureLookup(t *testing.T) {
	f := Lookup("dejavu sans", Style{Weight: 400})
	if f == nil {
		t.Skip("no DejaVu Sans")
	}
	g, n := f.Ligature([]rune("fi!"))
	if n != 2 || g == 0 {
		t.Fatalf("fi ligature: gid %d n %d", g, n)
	}
	if _, n := f.Ligature([]rune("ab")); n != 0 {
		t.Fatal("ab is no ligature")
	}
}
