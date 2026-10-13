package font

import (
	"math"
	"math/rand"
	"testing"

	"github.com/sweefbrowser/sweef/internal/font/fonttest"
)

func varFace(t testing.TB, f *fonttest.Font, wmin, wmax int, vars string) *WebFace {
	t.Helper()
	sfnt, _, err := Decode(f.SFNT(), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	wf := NewWebFace(FaceDesc{Family: "V", WMin: wmin, WMax: wmax, Variations: vars})
	if err := wf.SetData(sfnt); err != nil {
		t.Fatal(err)
	}
	return wf
}

func maxX(segs []Segment) float64 {
	m := math.Inf(-1)
	for _, s := range segs {
		for _, v := range []float64{s.X0, s.X1, s.X2} {
			m = math.Max(m, v)
		}
	}
	return m
}

func TestVariableFontWeights(t *testing.T) {
	for _, hvar := range []bool{true, false} {
		wf := varFace(t, fonttest.VarBoxes("Var", hvar), 100, 900, "")
		base := fonttest.Adv('m') // 680
		for _, c := range []struct {
			w    int
			want float64 // advance delta
		}{{400, 0}, {900, 160}, {650, 80}, {100, -100}, {250, -50}, {2000, 160}, {1, -100}} {
			f := wf.Face(c.w)
			if f == nil {
				t.Fatal("no face")
			}
			if got := f.AdvanceUnits('m'); got != float64(base)+c.want {
				t.Errorf("hvar=%v wght %d: advance %v, want %v", hvar, c.w, got, float64(base)+c.want)
			}
			// the right edge of the box follows the advance (units at 1000px: 1 unit = 1px)
			wantEdge := float64(base) - 60 + c.want
			if got := maxX(f.Outline('m', 1000)); math.Abs(got-wantEdge) > 1.0/64 {
				t.Errorf("hvar=%v wght %d: right edge %v, want %v", hvar, c.w, got, wantEdge)
			}
		}
		if f := wf.Face(700); f.Style.Weight != 700 {
			t.Errorf("instance style weight %d", f.Style.Weight)
		}
		if a, b := wf.Face(400), wf.Face(400); a != b {
			t.Error("instances are not cached")
		}
	}
}

func TestVariableDefaultInstanceMatchesStaticOutlines(t *testing.T) {
	vf := fonttest.VarBoxes("Var", true)
	wf := varFace(t, vf, 100, 900, "")
	stat, err := Parse("s", Style{Weight: 400}, vf.SFNT())
	if err != nil {
		t.Fatal(err)
	}
	f := wf.Face(400)
	n := 0
	for r := rune(0x20); r < 0x4f00; r++ {
		if !stat.HasGlyph(r) {
			continue
		}
		n++
		a, b := stat.Outline(r, 100), f.Outline(r, 100)
		if len(a) != len(b) {
			t.Fatalf("U+%04X: %d vs %d segments\n%v\n%v", r, len(a), len(b), a, b)
		}
		for i := range a {
			if a[i].Op != b[i].Op || math.Abs(a[i].X0-b[i].X0) > 1.0/64 || math.Abs(a[i].Y0-b[i].Y0) > 1.0/64 ||
				math.Abs(a[i].X1-b[i].X1) > 1.0/64 || math.Abs(a[i].Y1-b[i].Y1) > 1.0/64 {
				t.Fatalf("U+%04X segment %d: %+v vs %+v", r, i, a[i], b[i])
			}
		}
		if stat.AdvanceUnits(r) != f.AdvanceUnits(r) {
			t.Fatalf("U+%04X advance", r)
		}
	}
	if n < 90 {
		t.Fatalf("compared %d glyphs", n)
	}
}

func TestVariableWeightClampedToDeclaredRange(t *testing.T) {
	wf := varFace(t, fonttest.VarBoxes("Var", true), 300, 600, "")
	if got, want := wf.Face(900).AdvanceUnits('m'), float64(fonttest.Adv('m'))+160*0.4; math.Abs(got-want) > 1e-9 {
		t.Errorf("clamped to 600: advance %v, want %v", got, want) // 600 -> coordinate 0.4
	}
	if got, want := wf.Face(100).AdvanceUnits('m'), float64(fonttest.Adv('m'))-100*(100.0/300); math.Abs(got-want) > 1e-9 {
		t.Errorf("clamped to 300: advance %v, want %v", got, want)
	}
}

func TestVariationSettingsDescriptor(t *testing.T) {
	// font-variation-settings in @font-face overrides the weight
	wf := varFace(t, fonttest.VarBoxes("Var", true), 100, 900, `"wght" 900`)
	if got := wf.Face(400).AdvanceUnits('m'); got != float64(fonttest.Adv('m'))+160 {
		t.Errorf("advance %v", got)
	}
}

func TestVariableFontCompositeAndSparseIUP(t *testing.T) {
	f := fonttest.Boxes("Sp", "Regular", 400, false)
	f.ShortLoca = false
	// glyph 'A' is a triangle (3 points): touch points 0 and 2 only; point 1 is inferred
	gA := f.Cmap['A']
	// composite 'é': move the accent component up at the bold end
	gE := f.Cmap[0xe9]
	vs := fonttest.VarSpec{
		Axes:   []fonttest.Axis{{Tag: "wght", Min: 400, Def: 400, Max: 800}},
		Shared: [][]float64{{1}},
		Glyphs: map[int]fonttest.GlyphVar{
			gA: {Tuples: []fonttest.Tuple{
				{SharedIdx: 0, Points: []int{0, 2}, DX: []int{-20, 40}, DY: []int{0, 100}},
			}},
			gE: {Tuples: []fonttest.Tuple{{SharedIdx: 0, Points: []int{1}, DX: []int{30}, DY: []int{100}}}},
		},
		LongGvar: true,
	}
	f.AddVariations(vs)
	wf := varFace(t, f, 400, 800, "")
	tri := f.Glyphs[gA].Contours[0] // (40,0) (adv-40,0) (mid,h)
	out := wf.Face(800).Outline('A', 1000)
	// segments: M p0, L p1, L p2, (close L p0). Expected points: p0+(-20,0), p2+(40,100), p1 inferred
	px := func(s Segment) (float64, float64) { return s.X0, -s.Y0 }
	x0, y0 := px(out[0])
	if x0 != float64(tri[0].X)-20 || y0 != 0 {
		t.Errorf("touched point 0 moved to %v,%v", x0, y0)
	}
	x1, y1 := px(out[1])
	// p1 lies between touched p2 and p0 around the contour: x interpolated between their x deltas by position
	wantDX := iupDelta(float64(tri[1].X), float64(tri[2].X), float64(tri[0].X), 40, -20)
	wantDY := iupDelta(float64(tri[1].Y), float64(tri[2].Y), float64(tri[0].Y), 100, 0)
	if math.Abs(x1-(float64(tri[1].X)+wantDX)) > 1.0/64 || math.Abs(y1-(float64(tri[1].Y)+wantDY)) > 1.0/64 {
		t.Errorf("inferred point 1 at %v,%v want delta %v,%v", x1, y1, wantDX, wantDY)
	}
	x2, y2 := px(out[2])
	if x2 != float64(tri[2].X)+40 || y2 != float64(tri[2].Y)+100 {
		t.Errorf("touched point 2 at %v,%v", x2, y2)
	}
	// half way the deltas are half
	h := wf.Face(600).Outline('A', 1000)
	if hx, _ := px(h[0]); hx != float64(tri[0].X)-10 {
		t.Errorf("half weight point 0 at %v", hx)
	}
	// composite: the accent component moved by (30,100) at 800
	def := wf.Face(400).Outline(0xe9, 1000)
	bold := wf.Face(800).Outline(0xe9, 1000)
	if len(def) != len(bold) || len(def) == 0 {
		t.Fatalf("composite outlines %d vs %d", len(def), len(bold))
	}
	last := len(def) - 1 // accent is the last contour of the composite
	if dx, dy := bold[last].X0-def[last].X0, bold[last].Y0-def[last].Y0; dx != 30 || dy != -100 {
		t.Errorf("accent moved by %v,%v (y is down)", dx, dy)
	}
	if bold[0] != def[0] {
		t.Errorf("base letter of the composite changed: %+v vs %+v", bold[0], def[0])
	}
}

func TestVariableIntermediateRegionAndAvar(t *testing.T) {
	f := fonttest.Boxes("Im", "Regular", 400, false)
	f.ShortLoca = false
	gm := f.Cmap['m']
	n := 4
	dx := make([]int, n+4)
	dx[n+1] = 100 // advance +100 at the peak of the tuple
	vs := fonttest.VarSpec{
		Axes: []fonttest.Axis{{Tag: "wght", Min: 400, Def: 400, Max: 800}},
		// avar: user 0.5 -> normalised 0.2 (a slow start)
		Avar: [][][2]float64{{{-1, -1}, {0, 0}, {0.5, 0.2}, {1, 1}}},
		Glyphs: map[int]fonttest.GlyphVar{
			gm: {HasShared: true, Tuples: []fonttest.Tuple{
				// intermediate: starts at 0.1, peaks at 0.5, ends at 1.0; shared (all) points
				{SharedIdx: -1, Peak: []float64{0.5}, Start: []float64{0.1}, End: []float64{1}, UseSharedPts: true, DX: dx, DY: make([]int, n+4)},
			}},
		},
	}
	f.AddVariations(vs)
	wf := varFace(t, f, 400, 800, "")
	base := float64(fonttest.Adv('m'))
	// user wght 600 -> 0.5 -> avar 0.2 -> scalar (0.2-0.1)/(0.5-0.1) = 0.25 -> +25
	if got := wf.Face(600).AdvanceUnits('m'); math.Abs(got-(base+25)) > 0.05 {
		t.Errorf("avar+intermediate: advance %v, want %v", got, base+25)
	}
	// user 700 -> 0.75 -> avar 0.2+0.8*0.5=0.6 -> scalar (1-0.6)/(1-0.5)=0.8 -> +80
	if got := wf.Face(700).AdvanceUnits('m'); math.Abs(got-(base+80)) > 0.05 {
		t.Errorf("advance %v, want %v", got, base+80)
	}
	// outside the region (at the default) nothing changes
	if got := wf.Face(400).AdvanceUnits('m'); got != base {
		t.Errorf("default advance %v", got)
	}
}

func TestIUP(t *testing.T) {
	g := &rawGlyph{pts: []gpt{{x: 0, y: 0}, {x: 10, y: 0}, {x: 20, y: 0}, {x: 30, y: 10}, {x: 40, y: 20}}, ends: []int{4}}
	touched := []bool{true, false, false, false, true}
	tx := []float64{0, 0, 0, 0, 40}
	ty := make([]float64, 5)
	iup(g, touched, tx, ty)
	want := []float64{0, 10, 20, 30, 40}
	for i := range want {
		if math.Abs(tx[i]-want[i]) > 1e-9 {
			t.Errorf("tx = %v, want %v", tx, want)
			break
		}
	}
	// a single touched point shifts the whole contour
	touched = []bool{false, false, true, false, false}
	tx = []float64{0, 0, 7, 0, 0}
	ty = []float64{0, 0, -3, 0, 0}
	iup(g, touched, tx, ty)
	for i := range tx {
		if tx[i] != 7 || ty[i] != -3 {
			t.Fatalf("single touched point: %v %v", tx, ty)
		}
	}
}

func TestVariationHostileTables(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	base := fonttest.VarBoxes("Var", true)
	tabs := map[string][]byte{}
	for _, tb := range fonttest.ParseTables(base.SFNT()) {
		tabs[tb.Tag] = append([]byte(nil), tb.Data...)
	}
	for i := 0; i < 1500; i++ {
		tt := map[string][]byte{}
		for k, v := range tabs {
			tt[k] = append([]byte(nil), v...)
		}
		for _, tag := range []string{"gvar", "fvar", "HVAR", "avar"} {
			b := tt[tag]
			if b == nil {
				continue
			}
			for k := 0; k < 1+rng.Intn(6); k++ {
				b[rng.Intn(len(b))] = byte(rng.Intn(256))
			}
		}
		sfnt, _, err := Decode(fonttest.Assemble(0x00010000, tt), Limits{})
		if err != nil {
			continue
		}
		wf := NewWebFace(FaceDesc{Family: "x", WMin: 100, WMax: 900})
		if wf.SetData(sfnt) != nil {
			continue
		}
		for _, w := range []int{100, 400, 700, 900} {
			f := wf.Face(w)
			for r := rune(0x20); r < 0x7f; r++ {
				f.Outline(r, 40)
				f.AdvanceUnits(r)
			}
			f.Outline(0xe9, 40)
		}
	}
}

// FuzzVariableFont feeds arbitrary sfnt bytes through the decoder, the sanitiser and the variation
// code; whatever is accepted must be usable without panicking.
func FuzzVariableFont(f *testing.F) {
	f.Add(fonttest.VarBoxes("Var", true).SFNT())
	f.Add(fonttest.VarBoxes("Var", false).SFNT())
	f.Fuzz(func(t *testing.T, d []byte) {
		sfnt, _, err := Decode(d, Limits{MaxSfnt: 1 << 20})
		if err != nil {
			return
		}
		wf := NewWebFace(FaceDesc{Family: "x", WMin: 100, WMax: 900})
		if wf.SetData(sfnt) != nil {
			return
		}
		for _, w := range []int{100, 700} {
			face := wf.Face(w)
			for r := rune(0x20); r < 0x60; r++ {
				face.Outline(r, 32)
				face.AdvanceUnits(r)
			}
			face.Outline(0xe9, 32)
		}
	})
}
