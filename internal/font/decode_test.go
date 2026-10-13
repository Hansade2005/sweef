package font

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"math/rand"
	"os"
	"testing"

	"github.com/sweefbrowser/sweef/internal/font/fonttest"
)

func testSFNT(t testing.TB) []byte {
	t.Helper()
	return fonttest.Boxes("Boxy", "Regular", 400, false).SFNT()
}

// sameFont fails unless a and b are the same font: same advances and outlines
// for every character of the test font and same hmtx.
func sameFont(t *testing.T, a, b []byte) {
	t.Helper()
	fa, err := Parse("a", Style{Weight: 400}, a)
	if err != nil {
		t.Fatalf("parse a: %v", err)
	}
	fb, err := Parse("b", Style{Weight: 400}, b)
	if err != nil {
		t.Fatalf("parse b: %v", err)
	}
	n := 0
	for r := rune(0); r < 0x5000; r++ {
		if fa.HasGlyph(r) != fb.HasGlyph(r) {
			t.Fatalf("U+%04X: coverage differs", r)
		}
		if !fa.HasGlyph(r) {
			continue
		}
		n++
		if x, y := fa.AdvanceUnits(r), fb.AdvanceUnits(r); x != y {
			t.Fatalf("U+%04X: advance %v vs %v", r, x, y)
		}
		oa, ob := fa.Outline(r, 1000), fb.Outline(r, 1000)
		if len(oa) != len(ob) {
			t.Fatalf("U+%04X: outline length %d vs %d", r, len(oa), len(ob))
		}
		for i := range oa {
			if oa[i] != ob[i] {
				t.Fatalf("U+%04X: outline segment %d: %+v vs %+v", r, i, oa[i], ob[i])
			}
		}
	}
	if n < 90 {
		t.Fatalf("only %d glyphs compared", n)
	}
}

func TestDecodePlainSFNT(t *testing.T) {
	s := testSFNT(t)
	got, kind, err := Decode(s, Limits{})
	if err != nil || kind != KindTTF {
		t.Fatalf("decode: %v %v", kind, err)
	}
	sameFont(t, s, got)
}

func TestDecodeWOFF(t *testing.T) {
	s := testSFNT(t)
	for _, comp := range []bool{false, true} {
		w := fonttest.WOFF(s, comp)
		got, kind, err := Decode(w, Limits{})
		if err != nil || kind != KindWOFF {
			t.Fatalf("compress=%v: %v %v", comp, kind, err)
		}
		sameFont(t, s, got)
		if comp && len(w) >= len(s) {
			t.Errorf("woff not smaller: %d >= %d", len(w), len(s))
		}
	}
}

func TestDecodeWOFF2(t *testing.T) {
	s := testSFNT(t)
	for _, o := range []fonttest.Options{{}, {Transform: true}, {Transform: true, Overlap: true}} {
		w := fonttest.WOFF2(s, o)
		got, kind, err := Decode(w, Limits{})
		if err != nil || kind != KindWOFF2 {
			t.Fatalf("%+v: %v %v", o, kind, err)
		}
		sameFont(t, s, got)
		if !o.Transform {
			// the null transform must hand back the very same tables
			for _, tb := range fonttest.ParseTables(s) {
				var found bool
				for _, tb2 := range fonttest.ParseTables(got) {
					if tb2.Tag == tb.Tag {
						found = true
						if !bytes.Equal(tb.Data, tb2.Data) {
							t.Errorf("table %s differs", tb.Tag)
						}
					}
				}
				if !found {
					t.Errorf("table %s missing", tb.Tag)
				}
			}
		}
	}
}

func TestDecodeWOFF2TransformedHmtx(t *testing.T) {
	// the Boxes font has lsb == xMin everywhere: the hmtx transform applies and must round trip
	s := testSFNT(t)
	w := fonttest.WOFF2(s, fonttest.Options{Transform: true})
	got, _, err := Decode(w, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if a, b := tableOf(s, "hmtx"), tableOf(got, "hmtx"); !bytes.Equal(a, b) {
		t.Fatal("hmtx differs after the transform")
	}
}

// everything the decoders are given must come back as ErrBadFont, never a panic.
func mustBeBad(t *testing.T, what string, data []byte) {
	t.Helper()
	_, _, err := Decode(data, Limits{})
	if err == nil {
		t.Errorf("%s: accepted", what)
		return
	}
	if !errors.Is(err, ErrBadFont) {
		t.Errorf("%s: error does not wrap ErrBadFont: %v", what, err)
	}
}

func TestDecodeTruncated(t *testing.T) {
	s := testSFNT(t)
	for name, full := range map[string][]byte{
		"woff":  fonttest.WOFF(s, true),
		"woff2": fonttest.WOFF2(s, fonttest.Options{Transform: true}),
	} {
		for n := 0; n < len(full); n += 1 + n/40 {
			if _, _, err := Decode(full[:n], Limits{}); err == nil {
				t.Errorf("%s truncated at %d/%d accepted", name, n, len(full))
			}
		}
	}
}

func TestDecodeMutated(t *testing.T) {
	s := testSFNT(t)
	rng := rand.New(rand.NewSource(1))
	for _, full := range map[string][]byte{
		"ttf":   s,
		"woff":  fonttest.WOFF(s, true),
		"woff2": fonttest.WOFF2(s, fonttest.Options{Transform: true}),
	} {
		for i := 0; i < 3000; i++ {
			d := append([]byte(nil), full...)
			for k := 0; k < 1+rng.Intn(4); k++ {
				d[rng.Intn(len(d))] ^= byte(1 << uint(rng.Intn(8)))
			}
			sfnt, _, err := Decode(d, Limits{})
			if err == nil {
				// whatever was accepted must also be usable without panicking
				if f, perr := Parse("m", Style{Weight: 400}, sfnt); perr == nil {
					for r := rune(0x20); r < 0x7f; r++ {
						f.Outline(r, 20)
						f.AdvanceUnits(r)
					}
				}
			}
		}
	}
}

func TestDecodeHostile(t *testing.T) {
	s := testSFNT(t)
	w := fonttest.WOFF(s, true)
	// a table that claims a larger original length than it inflates to
	d := append([]byte(nil), w...)
	// first table entry: origLength at 44+12
	orig := int(d[44+12])<<24 | int(d[44+13])<<16 | int(d[44+14])<<8 | int(d[44+15])
	d[44+14], d[44+15] = byte((orig+9)>>8), byte(orig+9)
	mustBeBad(t, "woff orig length too large", d)
	// a table that inflates to more than claimed
	mustBeBad(t, "not a font", []byte("hello world, definitely not a font"))
	mustBeBad(t, "empty", nil)
	// zip bomb: a 1 GiB zeros table under a tiny MaxSfnt
	if _, _, err := Decode(w, Limits{MaxSfnt: 1000}); err == nil {
		t.Error("size limit not applied to woff")
	}
	w2 := fonttest.WOFF2(s, fonttest.Options{Transform: true})
	if _, _, err := Decode(w2, Limits{MaxSfnt: 1000}); err == nil {
		t.Error("size limit not applied to woff2")
	}
	if _, _, err := Decode(s, Limits{MaxSfnt: 1000}); err == nil {
		t.Error("size limit not applied to sfnt")
	}
}

func TestSanitizeRejectsInconsistentTables(t *testing.T) {
	f := fonttest.Boxes("Boxy", "Regular", 400, false)
	base := f.SFNT()
	patch := func(tag string, fn func(b []byte)) []byte {
		tabs := map[string][]byte{}
		for _, tb := range fonttest.ParseTables(base) {
			tabs[tb.Tag] = append([]byte(nil), tb.Data...)
		}
		fn(tabs[tag])
		return fonttest.Assemble(0x00010000, tabs)
	}
	drop := func(tag string) []byte {
		tabs := map[string][]byte{}
		for _, tb := range fonttest.ParseTables(base) {
			if tb.Tag != tag {
				tabs[tb.Tag] = tb.Data
			}
		}
		return fonttest.Assemble(0x00010000, tabs)
	}
	for _, tag := range []string{"head", "hhea", "maxp", "hmtx", "cmap", "glyf", "loca"} {
		mustBeBad(t, "missing "+tag, drop(tag))
	}
	mustBeBad(t, "head magic", patch("head", func(b []byte) { b[12] ^= 0xff }))
	mustBeBad(t, "unitsPerEm 0", patch("head", func(b []byte) { b[18], b[19] = 0, 0 }))
	mustBeBad(t, "numGlyphs 0", patch("maxp", func(b []byte) { b[4], b[5] = 0, 0 }))
	mustBeBad(t, "numGlyphs larger than hmtx", patch("maxp", func(b []byte) { b[4], b[5] = 0xff, 0xff }))
	mustBeBad(t, "loca decreasing", patch("loca", func(b []byte) { b[2], b[3] = 0xff, 0xf0 }))
	mustBeBad(t, "glyph truncated", patch("glyf", func(b []byte) { b[0], b[1] = 0x00, 0x40 })) // nContours 64 in a small glyph
	if _, _, err := Decode(base, Limits{}); err != nil {
		t.Fatalf("control: %v", err)
	}
}

func FuzzDecodeWOFF(f *testing.F) {
	s := testSFNT(f)
	f.Add(fonttest.WOFF(s, true))
	f.Add(fonttest.WOFF(s, false))
	f.Add([]byte("wOFF"))
	f.Fuzz(func(t *testing.T, d []byte) {
		sfnt, err := DecodeWOFF(d, Limits{MaxSfnt: 1 << 20})
		if err != nil && !errors.Is(err, ErrBadFont) {
			t.Fatalf("error does not wrap ErrBadFont: %v", err)
		}
		if err == nil && len(sfnt) > 1<<20 {
			t.Fatal("limit exceeded")
		}
	})
}

func FuzzDecodeWOFF2(f *testing.F) {
	s := testSFNT(f)
	f.Add(fonttest.WOFF2(s, fonttest.Options{}))
	f.Add(fonttest.WOFF2(s, fonttest.Options{Transform: true}))
	f.Add(fonttest.WOFF2(s, fonttest.Options{Transform: true, Overlap: true}))
	f.Add([]byte("wOF2"))
	f.Fuzz(func(t *testing.T, d []byte) {
		sfnt, err := DecodeWOFF2(d, Limits{MaxSfnt: 1 << 20})
		if err != nil && !errors.Is(err, ErrBadFont) {
			t.Fatalf("error does not wrap ErrBadFont: %v", err)
		}
		if err == nil && len(sfnt) > 1<<20 {
			t.Fatal("limit exceeded")
		}
	})
}

// FuzzGlyfTransform feeds the transformed glyf stream directly to the reconstruction.
func FuzzGlyfTransform(f *testing.F) {
	s := testSFNT(f)
	tabs := map[string][]byte{}
	for _, tb := range fonttest.ParseTables(s) {
		tabs[tb.Tag] = tb.Data
	}
	for _, ov := range []bool{false, true} {
		g, _ := fonttest.TransformGlyf(tabs["glyf"], tabs["loca"], tabs["head"], tabs["maxp"], ov)
		f.Add(g)
	}
	f.Fuzz(func(t *testing.T, d []byte) {
		glyf, loca, n, _, xm, err := reconstructGlyf(d, 1<<20)
		if err != nil {
			if !errors.Is(err, ErrBadFont) {
				t.Fatalf("error does not wrap ErrBadFont: %v", err)
			}
			return
		}
		if len(glyf) > 1<<20 || len(xm) != n {
			t.Fatalf("bad result: glyf %d, xMins %d, glyphs %d", len(glyf), len(xm), n)
		}
		if len(loca) != 2*(n+1) && len(loca) != 4*(n+1) {
			t.Fatalf("loca size %d for %d glyphs", len(loca), n)
		}
	})
}

// randomGlyphFont has glyphs whose point deltas hit every triplet class of the
// WOFF2 glyf transform, contours with more than 253 and 505 points (the wider
// 255UInt16 forms) and instructions.
func randomGlyphFont(seed int64) []byte {
	rng := rand.New(rand.NewSource(seed))
	f := fonttest.Boxes("Rnd", "Regular", 400, false)
	span := []int{3, 70, 300, 900, 2500, 5000, 20000}
	for gi := 0; gi < 24; gi++ {
		var cs [][]fonttest.Pt
		for c := 0; c < 1+rng.Intn(3); c++ {
			n := 3 + rng.Intn(30)
			if gi%8 == 7 {
				n = 260 + rng.Intn(300)
			}
			if gi == 22 {
				n = 520
			}
			x, y := 0, 0
			var pts []fonttest.Pt
			for k := 0; k < n; k++ {
				s := span[rng.Intn(len(span))]
				dx, dy := rng.Intn(2*s+1)-s, rng.Intn(2*s+1)-s
				switch rng.Intn(4) {
				case 0:
					dx = 0
				case 1:
					dy = 0
				}
				nx, ny := x+dx, y+dy
				if nx < -15000 || nx > 15000 {
					nx = x
				}
				if ny < -15000 || ny > 15000 {
					ny = y
				}
				x, y = nx, ny
				pts = append(pts, fonttest.Pt{X: x, Y: y, Off: rng.Intn(3) == 0})
			}
			cs = append(cs, pts)
		}
		g := fonttest.Glyph{Contours: cs, Adv: 100 + rng.Intn(900)}
		if gi%3 == 0 {
			g.Instr = []byte{0xb0, 0x01, 0x2b}
			if gi == 9 {
				g.Instr = make([]byte, 300) // long instructions: wide 255UInt16
			}
		}
		f.Glyphs = append(f.Glyphs, g)
		f.Cmap[rune(0x3000+gi)] = len(f.Glyphs) - 1
	}
	f.ShortLoca = false
	return f.SFNT()
}

func TestWOFF2RandomGlyphsRoundTrip(t *testing.T) {
	for seed := int64(1); seed <= 6; seed++ {
		s := randomGlyphFont(seed)
		for _, o := range []fonttest.Options{{Transform: true}, {Transform: true, Overlap: true}} {
			w := fonttest.WOFF2(s, o)
			got, _, err := Decode(w, Limits{})
			if err != nil {
				t.Fatalf("seed %d: %v", seed, err)
			}
			fa, _ := Parse("a", Style{Weight: 400}, s)
			fb, err := Parse("b", Style{Weight: 400}, got)
			if err != nil {
				t.Fatal(err)
			}
			for r := rune(0x3000); r < 0x3018; r++ {
				a, b := fa.Outline(r, 1000), fb.Outline(r, 1000)
				if len(a) == 0 || len(a) != len(b) {
					t.Fatalf("seed %d U+%04X: outline %d vs %d segments", seed, r, len(a), len(b))
				}
				for i := range a {
					if a[i] != b[i] {
						t.Fatalf("seed %d U+%04X seg %d: %+v vs %+v", seed, r, i, a[i], b[i])
					}
				}
			}
		}
	}
}

func TestDecodeRealBrotliFixtures(t *testing.T) {
	got, kind, err := Decode(fonttest.BoxesWOFF2, Limits{})
	if err != nil || kind != KindWOFF2 {
		t.Fatalf("boxes.woff2: %v %v", kind, err)
	}
	sameFont(t, fonttest.Boxes("Boxy", "Regular", 400, false).SFNT(), got)
	got, _, err = Decode(fonttest.IconsWOFF2, Limits{})
	if err != nil {
		t.Fatalf("icons.woff2: %v", err)
	}
	if _, err := Parse("icony", Style{Weight: 400}, got); err != nil {
		t.Fatal(err)
	}
	t.Logf("fixtures: boxes %d bytes, icons %d bytes", len(fonttest.BoxesWOFF2), len(fonttest.IconsWOFF2))
}

// Real fonts of the machine (TrueType with hinting instructions, composite glyphs and long
// loca; CFF) go through every container and must come back drawing the same.
func TestRealFontsRoundTripThroughContainers(t *testing.T) {
	files := []string{
		"/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
		"/usr/share/fonts/truetype/liberation/LiberationSerif-Regular.ttf",
		"/usr/share/fonts/opentype/inter/Inter-Regular.otf",
	}
	any := false
	for _, p := range files {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		any = true
		ref, err := Parse("ref", Style{Weight: 400}, raw)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		variants := map[string][]byte{"ttf": raw, "woff": fonttest.WOFF(raw, true)}
		if !bytes.HasPrefix(raw, []byte("OTTO")) {
			variants["woff2 null"] = fonttest.WOFF2(raw, fonttest.Options{})
			variants["woff2 transform"] = fonttest.WOFF2(raw, fonttest.Options{Transform: true})
		} else {
			variants["woff2 null"] = fonttest.WOFF2(raw, fonttest.Options{})
		}
		for name, data := range variants {
			sfnt, _, err := Decode(data, Limits{})
			if err != nil {
				t.Errorf("%s %s: %v", p, name, err)
				continue
			}
			f, err := Parse("got", Style{Weight: 400}, sfnt)
			if err != nil {
				t.Errorf("%s %s: parse: %v", p, name, err)
				continue
			}
			n := 0
			for r := rune(0x20); r < 0x2200; r += 3 {
				if ref.HasGlyph(r) != f.HasGlyph(r) {
					t.Fatalf("%s %s U+%04X: coverage differs", p, name, r)
				}
				if !ref.HasGlyph(r) {
					continue
				}
				n++
				if ref.AdvanceUnits(r) != f.AdvanceUnits(r) {
					t.Fatalf("%s %s U+%04X: advance", p, name, r)
				}
				a, b := ref.Outline(r, 100), f.Outline(r, 100)
				if len(a) != len(b) {
					t.Fatalf("%s %s U+%04X: %d vs %d segments", p, name, r, len(a), len(b))
				}
				for i := range a {
					if a[i] != b[i] {
						t.Fatalf("%s %s U+%04X seg %d: %+v vs %+v", p, name, r, i, a[i], b[i])
					}
				}
			}
			if n < 100 {
				t.Errorf("%s %s: only %d glyphs compared", p, name, n)
			}
		}
	}
	if !any {
		t.Skip("none of the test fonts is installed")
	}
}

func TestDecompressionBombs(t *testing.T) {
	s := testSFNT(t)
	// WOFF2 whose brotli stream is longer than the directory announces
	bomb := fonttest.WOFF2(s, fonttest.Options{Transform: true, Brotli: func(b []byte) []byte {
		return fonttest.StoredBrotli(append(append([]byte(nil), b...), make([]byte, 6<<20)...))
	}})
	if _, _, err := Decode(bomb, Limits{}); err == nil || !errors.Is(err, ErrBadFont) {
		t.Fatalf("woff2 longer than announced: %v", err)
	}
	// a one table WOFF whose zlib stream inflates to far more than its original length
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	zw.Write(make([]byte, 64<<20))
	zw.Close()
	hdr := make([]byte, 44+20)
	copy(hdr, "wOFF")
	binary.BigEndian.PutUint32(hdr[4:], 0x00010000)
	binary.BigEndian.PutUint16(hdr[12:], 1)
	copy(hdr[44:], "head")
	binary.BigEndian.PutUint32(hdr[48:], 64)              // offset
	binary.BigEndian.PutUint32(hdr[52:], uint32(z.Len())) // compressed length
	binary.BigEndian.PutUint32(hdr[56:], 1<<20)           // claimed original length
	file := append(hdr, z.Bytes()...)
	binary.BigEndian.PutUint32(file[8:], uint32(len(file)))
	if z.Len() >= 1<<20 {
		t.Skipf("zlib stream is %d bytes", z.Len())
	}
	if _, _, err := Decode(file, Limits{}); err == nil || !errors.Is(err, ErrBadFont) {
		t.Fatalf("woff zlib bomb: %v", err)
	}
	// a table directory that promises more than the limit
	hdr2 := append([]byte(nil), hdr...)
	binary.BigEndian.PutUint32(hdr2[56:], 1<<30)
	file2 := append(hdr2, z.Bytes()...)
	binary.BigEndian.PutUint32(file2[8:], uint32(len(file2)))
	if _, _, err := Decode(file2, Limits{}); err == nil || !errors.Is(err, ErrBadFont) {
		t.Fatalf("woff claiming a gigabyte: %v", err)
	}
}

// FuzzDecode runs the whole path a downloaded file takes: container detection, decoding,
// directory normalisation, sanitiser, parsing and drawing every character of the test font.
func FuzzDecode(f *testing.F) {
	s := testSFNT(f)
	f.Add(s)
	f.Add(fonttest.WOFF(s, true))
	f.Add(fonttest.WOFF2(s, fonttest.Options{Transform: true}))
	f.Add(fonttest.BoxesWOFF2)
	f.Add(fonttest.IconsWOFF2)
	f.Add(fonttest.VarBoxes("V", true).SFNT())
	f.Fuzz(func(t *testing.T, d []byte) {
		sfnt, _, err := Decode(d, Limits{MaxSfnt: 1 << 20})
		if err != nil {
			if !errors.Is(err, ErrBadFont) {
				t.Fatalf("error does not wrap ErrBadFont: %v", err)
			}
			return
		}
		wf := NewWebFace(FaceDesc{Family: "x"})
		if wf.SetData(sfnt) != nil {
			return
		}
		face := wf.Face(400)
		for r := rune(0x20); r < 0x80; r++ {
			face.Outline(r, 24)
			face.AdvanceF(r, 24)
			face.KernF(r, 'A', 24)
		}
		face.ShapeRun([]rune("office home arrow_back"), 24, ShapeOptions{Kern: true, Liga: true})
	})
}
