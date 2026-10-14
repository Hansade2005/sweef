package font

import (
	"encoding/binary"
	"reflect"
	"testing"

	xfont "golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/gobolditalic"
	"golang.org/x/image/font/gofont/goitalic"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/gofont/gomonobold"
	"golang.org/x/image/font/gofont/gomonobolditalic"
	"golang.org/x/image/font/gofont/gomonoitalic"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// TestGoFontArchive: every embedded face is the x/image Go font it replaces, minus its TrueType hinting program
// (tools/zpack/dehint.go, the instructions are more than half of the glyph data and Sweef never runs them). "Is" is
// checked on everything the browser reads: the glyph count, every outline, advance and bounding box, the mapping of
// every code point in the first three planes, the names and the metrics. The registry names all eight.
func TestGoFontArchive(t *testing.T) {
	want := map[string][]byte{
		"gobold": gobold.TTF, "gobolditalic": gobolditalic.TTF, "goitalic": goitalic.TTF, "gomono": gomono.TTF,
		"gomonobold": gomonobold.TTF, "gomonobolditalic": gomonobolditalic.TTF, "gomonoitalic": gomonoitalic.TTF,
		"goregular": goregular.TTF,
	}
	for name, orig := range want {
		got := goFont(name)
		if len(got) == 0 || len(got) >= len(orig) {
			t.Errorf("%s: archive has %d bytes, x/image has %d (run `go generate ./internal/font`)", name, len(got), len(orig))
			continue
		}
		sameGoFont(t, name, orig, got)
		checkGoFontSums(t, name, got)
	}
	if goFont("nope") != nil {
		t.Error("an unknown font was found")
	}
	if len(embedded()) != len(want) {
		t.Fatalf("embedded() lists %d faces, want %d", len(embedded()), len(want))
	}
	for _, e := range embedded() {
		if _, ok := want[e.embed]; !ok {
			t.Errorf("embedded() names %q, which is not in the archive", e.embed)
		}
		if f := e.get(); f == nil || f.UnitsPerEm() == 0 {
			t.Errorf("%s did not load", e.embed)
		}
	}
	// A face is built from a private copy: scribbling on one cannot corrupt the next load.
	a := goFont("goregular")
	a[0] ^= 0xff
	if b := goFont("goregular"); b[0] == a[0] {
		t.Error("goFont returned shared memory")
	}
}

func sameGoFont(t *testing.T, name string, a, b []byte) {
	t.Helper()
	fa, err1 := sfnt.Parse(a)
	fb, err2 := sfnt.Parse(b)
	if err1 != nil || err2 != nil {
		t.Fatalf("%s: parse: %v / %v", name, err1, err2)
	}
	if fa.NumGlyphs() != fb.NumGlyphs() || fa.UnitsPerEm() != fb.UnitsPerEm() {
		t.Fatalf("%s: %d glyphs / %d upm, original %d / %d", name, fb.NumGlyphs(), fb.UnitsPerEm(), fa.NumGlyphs(), fa.UnitsPerEm())
	}
	if fa.NumGlyphs() < 500 {
		t.Fatalf("%s: only %d glyphs; the comparison would be vacuous", name, fa.NumGlyphs())
	}
	var ba, bb sfnt.Buffer
	ppem := fixed.Int26_6(int(fa.UnitsPerEm()) * 64) // 1 unit = 1 pixel: the outline in font units
	for g := 0; g < fa.NumGlyphs(); g++ {
		gi := sfnt.GlyphIndex(g)
		sa, e1 := fa.LoadGlyph(&ba, gi, ppem, nil)
		sb, e2 := fb.LoadGlyph(&bb, gi, ppem, nil)
		if (e1 == nil) != (e2 == nil) || !reflect.DeepEqual(append(sfnt.Segments(nil), sa...), append(sfnt.Segments(nil), sb...)) {
			t.Fatalf("%s: glyph %d outline differs (%v / %v)", name, g, e1, e2)
		}
		adA, e1 := fa.GlyphAdvance(&ba, gi, ppem, xfont.HintingNone)
		adB, e2 := fb.GlyphAdvance(&bb, gi, ppem, xfont.HintingNone)
		if adA != adB || (e1 == nil) != (e2 == nil) {
			t.Fatalf("%s: glyph %d advance %v / %v", name, g, adA, adB)
		}
		bdA, _, e1 := fa.GlyphBounds(&ba, gi, ppem, xfont.HintingNone)
		bdB, _, e2 := fb.GlyphBounds(&bb, gi, ppem, xfont.HintingNone)
		if bdA != bdB || (e1 == nil) != (e2 == nil) {
			t.Fatalf("%s: glyph %d bounds %v / %v", name, g, bdA, bdB)
		}
	}
	for r := rune(0); r < 0x30000; r++ {
		ga, _ := fa.GlyphIndex(&ba, r)
		gb, _ := fb.GlyphIndex(&bb, r)
		if ga != gb {
			t.Fatalf("%s: U+%04X maps to glyph %d, original %d", name, r, gb, ga)
		}
	}
	for id := sfnt.NameID(0); id < 26; id++ {
		na, e1 := fa.Name(&ba, id)
		nb, e2 := fb.Name(&bb, id)
		if na != nb || (e1 == nil) != (e2 == nil) {
			t.Fatalf("%s: name %d is %q, original %q", name, id, nb, na)
		}
	}
	ma, e1 := fa.Metrics(&ba, ppem, xfont.HintingNone)
	mb, e2 := fb.Metrics(&bb, ppem, xfont.HintingNone)
	if ma != mb || (e1 == nil) != (e2 == nil) {
		t.Fatalf("%s: metrics %+v / %+v", name, mb, ma)
	}
	pa, pb := fa.PostTable(), fb.PostTable()
	if (pa == nil) != (pb == nil) || (pa != nil && pa.IsFixedPitch != pb.IsFixedPitch) {
		t.Fatalf("%s: post table differs", name)
	}
	// The hinting program is what is gone.
	for _, tag := range []string{"fpgm", "prep", "cvt "} {
		if goFontHas(b, tag) {
			t.Errorf("%s still has %q", name, tag)
		}
	}
	for _, tag := range []string{"head", "hhea", "maxp", "hmtx", "cmap", "name", "post", "OS/2", "glyf", "loca"} {
		if !goFontHas(b, tag) {
			t.Errorf("%s lost %q", name, tag)
		}
	}
}

func goFontHas(b []byte, tag string) bool {
	n := int(binary.BigEndian.Uint16(b[4:]))
	for i := 0; i < n; i++ {
		if string(b[12+16*i:12+16*i+4]) == tag {
			return true
		}
	}
	return false
}

// checkGoFontSums verifies the table checksums and the head.checkSumAdjustment of a font the generator wrote, as
// font validators do.
func checkGoFontSums(t *testing.T, name string, b []byte) {
	t.Helper()
	sum := func(p []byte) uint32 {
		var s uint32
		for i := 0; i < len(p); i += 4 {
			var w [4]byte
			copy(w[:], p[i:])
			s += binary.BigEndian.Uint32(w[:])
		}
		return s
	}
	n := int(binary.BigEndian.Uint16(b[4:]))
	for i := 0; i < n; i++ {
		e := b[12+16*i:]
		off, ln := int(binary.BigEndian.Uint32(e[8:])), int(binary.BigEndian.Uint32(e[12:]))
		tab := append([]byte(nil), b[off:off+ln]...)
		if string(e[:4]) == "head" {
			binary.BigEndian.PutUint32(tab[8:], 0)
		}
		if got, want := sum(tab), binary.BigEndian.Uint32(e[4:]); got != want {
			t.Errorf("%s: table %q checksum %08x, directory says %08x", name, e[:4], got, want)
		}
	}
	if got := sum(b); got != 0xB1B0AFBA {
		t.Errorf("%s: whole-file checksum %08x, want B1B0AFBA", name, got)
	}
}
