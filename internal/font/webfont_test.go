package font

import (
	"math"
	"sync/atomic"
	"testing"

	"github.com/sweefbrowser/sweef/internal/font/fonttest"
)

func desc(fam string, wmin, wmax int, style uint8, ranges ...URange) FaceDesc {
	return FaceDesc{Family: fam, WMin: wmin, WMax: wmax, Style: style, Ranges: ranges, Src: []Source{{URL: "u/" + fam}}}
}

func TestFontMatching(t *testing.T) {
	type c struct {
		name  string
		faces []FaceDesc
		want  Style
		pick  int // index of the expected face in faces
	}
	n, i, o := uint8(StyleNormal), uint8(StyleItalic), uint8(StyleOblique)
	cases := []c{
		{"exact", []FaceDesc{desc("a", 400, 400, n), desc("a", 700, 700, n)}, Style{Weight: 700}, 1},
		{"500 prefers 400 over 700", []FaceDesc{desc("a", 300, 300, n), desc("a", 400, 400, n), desc("a", 700, 700, n)}, Style{Weight: 500}, 1},
		{"450 prefers 500", []FaceDesc{desc("a", 400, 400, n), desc("a", 500, 500, n)}, Style{Weight: 450}, 1},
		{"600 goes up", []FaceDesc{desc("a", 300, 300, n), desc("a", 400, 400, n), desc("a", 700, 700, n)}, Style{Weight: 600}, 2},
		{"800 goes up then down", []FaceDesc{desc("a", 300, 300, n), desc("a", 400, 400, n), desc("a", 700, 700, n)}, Style{Weight: 800}, 2},
		{"900 with only light", []FaceDesc{desc("a", 300, 300, n), desc("a", 400, 400, n)}, Style{Weight: 900}, 1},
		{"200 goes down then up", []FaceDesc{desc("a", 300, 300, n), desc("a", 400, 400, n)}, Style{Weight: 200}, 0},
		{"350 prefers lighter", []FaceDesc{desc("a", 300, 300, n), desc("a", 400, 400, n)}, Style{Weight: 350}, 0},
		{"variable range", []FaceDesc{desc("a", 400, 400, n), desc("a", 100, 900, n)}, Style{Weight: 650}, 1},
		{"italic beats weight", []FaceDesc{desc("a", 400, 400, n), desc("a", 700, 700, i)}, Style{Weight: 400, Italic: true}, 1},
		{"oblique stands in for italic", []FaceDesc{desc("a", 400, 400, n), desc("a", 400, 400, o)}, Style{Weight: 400, Italic: true}, 1},
		{"italic used for normal when nothing else", []FaceDesc{desc("a", 400, 400, i)}, Style{Weight: 400}, 0},
		{"normal preferred for normal", []FaceDesc{desc("a", 400, 400, i), desc("a", 400, 400, n)}, Style{Weight: 400}, 1},
	}
	for _, tc := range cases {
		got := matchFaces(wfs(tc.faces), tc.want)
		if len(got) != 1 || got[0].Desc.Key() != func() string { d := tc.faces[tc.pick]; d.Normalize(); return d.Key() }() {
			t.Errorf("%s: picked %v", tc.name, descsOf(got))
		}
	}
}

func wfs(ds []FaceDesc) []*WebFace {
	var out []*WebFace
	for _, d := range ds {
		out = append(out, NewWebFace(d))
	}
	return out
}

func descsOf(fs []*WebFace) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Desc.Key())
	}
	return out
}

func TestUnicodeRangeSubsetsFormOneComposite(t *testing.T) {
	latin := desc("a", 400, 400, 0, URange{0, 0xff})
	cyr := desc("a", 400, 400, 0, URange{0x400, 0x4ff})
	bold := desc("a", 700, 700, 0, URange{0, 0xff})
	got := matchFaces(wfs([]FaceDesc{latin, cyr, bold}), Style{Weight: 400})
	if len(got) != 2 || got[0].Desc.Ranges[0].Lo != 0x400 || got[1].Desc.Ranges[0].Lo != 0 {
		t.Fatalf("composite: %v (later declarations first)", descsOf(got))
	}
}

func TestWebSetDemandLoadsOnlyTheNeededSubset(t *testing.T) {
	var loads []string
	var changed atomic.Int32
	s := NewWebSet(func(f *WebFace) { loads = append(loads, f.Desc.Src[0].URL) }, func() { changed.Add(1) })
	defer s.Close()
	s.SetSheetFaces([]FaceDesc{
		{Family: "Sub", Src: []Source{{URL: "latin"}}, Ranges: []URange{{0, 0xff}}},
		{Family: "Sub", Src: []Source{{URL: "cyr"}}, Ranges: []URange{{0x400, 0x4ff}}},
		{Family: "Sub", Src: []Source{{URL: "cjk"}}, Ranges: []URange{{0x4e00, 0x9fff}}},
	})
	sc, ok := s.Scope("sub")
	if !ok {
		t.Fatal("family not declared")
	}
	if _, ok := s.Scope("other"); ok {
		t.Fatal("undeclared family scoped")
	}
	st := Style{Weight: 400}
	if got := LookupAll(sc, st); len(got) != 0 {
		t.Fatal("faces before they are loaded")
	}
	Demand([]string{sc, "serif"}, st, 'a')
	Demand([]string{sc, "serif"}, st, 'b') // same face, no second load
	Demand([]string{sc}, st, 0x416)
	if len(loads) != 2 || loads[0] != "latin" || loads[1] != "cyr" {
		t.Fatalf("loads %v: only the subsets of the used characters", loads)
	}
	if s.Pending() != 2 {
		t.Fatalf("pending %d", s.Pending())
	}
	// the latin subset arrives
	bx := fonttest.Boxes("Sub", "Regular", 400, false).SFNT()
	sf, _, err := Decode(bx, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	var latin *WebFace
	for _, f := range s.Faces() {
		if f.Desc.Src[0].URL == "latin" {
			latin = f
		}
	}
	g0 := s.Gen()
	if err := latin.SetData(sf); err != nil {
		t.Fatal(err)
	}
	if s.Gen() == g0 || changed.Load() == 0 {
		t.Fatal("arrival did not bump the generation / notify")
	}
	got := LookupAll(sc, st)
	if len(got) != 1 {
		t.Fatalf("faces after load: %d", len(got))
	}
	if !got[0].HasGlyph('a') || got[0].HasGlyph(0x416) || got[0].HasGlyph(0x4e2d) {
		t.Fatal("unicode-range not applied to the face")
	}
	if got[0].AdvanceUnits(0x416) != -1 || got[0].GID(0x416) != 0 || got[0].Outline(0x416, 20) != nil {
		t.Fatal("character outside the range is not treated as absent")
	}
	// a loaded face that covers the character ends the search
	n := len(loads)
	Demand([]string{sc, "serif"}, st, 'z')
	if len(loads) != n {
		t.Fatal("demand for a loaded character loaded something")
	}
	// the cjk subset is never requested
	for _, l := range loads {
		if l == "cjk" {
			t.Fatal("unused subset requested")
		}
	}
}

func TestWebSetKeepsStateAcrossIdenticalDeclarations(t *testing.T) {
	s := NewWebSet(func(f *WebFace) {}, nil)
	defer s.Close()
	d := []FaceDesc{{Family: "K", Src: []Source{{URL: "k"}}}}
	if !s.SetSheetFaces(d) {
		t.Fatal("first declaration reported unchanged")
	}
	dg := s.DeclGen()
	f0 := s.Faces()[0]
	f0.SetFailed(nil)
	if s.SetSheetFaces([]FaceDesc{{Family: "K", Src: []Source{{URL: "k"}}}}) || s.DeclGen() != dg {
		t.Fatal("identical declarations changed the set")
	}
	if s.Faces()[0] != f0 {
		t.Fatal("face identity lost")
	}
	if !s.SetSheetFaces([]FaceDesc{{Family: "K", Src: []Source{{URL: "k2"}}}}) || s.DeclGen() == dg {
		t.Fatal("changed declaration not noticed")
	}
}

func TestDemandMovesOnWhenAFaceFails(t *testing.T) {
	var loads []string
	s := NewWebSet(func(f *WebFace) { loads = append(loads, f.Desc.Family) }, nil)
	defer s.Close()
	s.SetSheetFaces([]FaceDesc{{Family: "One", Src: []Source{{URL: "1"}}}, {Family: "Two", Src: []Source{{URL: "2"}}}})
	a, _ := s.Scope("one")
	b, _ := s.Scope("two")
	st := Style{Weight: 400}
	Demand([]string{a, b}, st, 'x')
	if len(loads) != 1 || loads[0] != "One" {
		t.Fatalf("only the first family is waited for: %v", loads)
	}
	for _, f := range s.Faces() {
		if f.Desc.Family == "One" {
			f.SetFailed(errBadFixture)
		}
	}
	Demand([]string{a, b}, st, 'x')
	if len(loads) != 2 || loads[1] != "Two" {
		t.Fatalf("after the first failed the second is tried: %v", loads)
	}
}

var errBadFixture = fontError("test failure")

func TestClosedSetResolvesToNothing(t *testing.T) {
	s := NewWebSet(nil, nil)
	s.SetSheetFaces([]FaceDesc{{Family: "C", Src: []Source{{URL: "c"}}}})
	sc, _ := s.Scope("c")
	s.Close()
	s.Close()
	if LookupAll(sc, Style{Weight: 400}) != nil || Lookup(sc, Style{Weight: 400}) != nil {
		t.Fatal("closed set still resolves")
	}
	Demand([]string{sc}, Style{Weight: 400}, 'a') // must not panic
}

func TestLongLigaturesOfIconFonts(t *testing.T) {
	icons := fonttest.Icons("Icony")
	wf := NewWebFace(FaceDesc{Family: "Icony"})
	sf, _, err := Decode(icons.SFNT(), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if err := wf.SetData(sf); err != nil {
		t.Fatal(err)
	}
	f := wf.Face(400)
	if got := f.LigMax(); got != len("check_circle_outline") {
		t.Fatalf("LigMax %d", got)
	}
	for i, name := range []string{"home", "arrow_back", "menu", "check_circle_outline"} {
		rs := []rune(name + " trailing")
		g, n := f.Ligature(rs)
		if n != len(name) || g != f.GID(rune(0xe900+i)) {
			t.Errorf("%s: ligature gid %d over %d runes, want gid %d over %d", name, g, n, f.GID(rune(0xe900+i)), len(name))
		}
		gl := f.ShapeRun([]rune(name), 24, ShapeOptions{Liga: true})
		if len(gl) != 1 || gl[0].NRunes != len(name) || gl[0].GID != f.GID(rune(0xe900+i)) {
			t.Errorf("%s: shaped into %+v", name, gl)
		}
	}
	// a prefix of a ligature name is not a ligature; a longer word that contains one is (as in HarfBuzz)
	if _, n := f.Ligature([]rune("hom")); n != 0 {
		t.Errorf("hom ligated over %d runes", n)
	}
	if _, n := f.Ligature([]rune("homer")); n != 4 {
		t.Errorf("homer: ligature over %d runes, want 4", n)
	}
}

func TestSizeAdjustAndMetricOverrides(t *testing.T) {
	base := fonttest.Boxes("Fb", "Regular", 400, false)
	sf, _, err := Decode(base.SFNT(), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	plain := NewWebFace(FaceDesc{Family: "Fb"})
	if err := plain.SetData(sf); err != nil {
		t.Fatal(err)
	}
	adj := NewWebFace(FaceDesc{Family: "Fb", SizeAdjust: 1.25, AscentOverride: 0.9, DescentOverride: 0.2, Overrides: OvrAscent | OvrDescent})
	if err := adj.SetData(sf); err != nil {
		t.Fatal(err)
	}
	p, a := plain.Face(400), adj.Face(400)
	if got, want := a.AdvanceF('m', 20), p.AdvanceF('m', 20)*1.25; got != want {
		t.Errorf("advance %v want %v", got, want)
	}
	if got, want := maxX(a.Outline('m', 100)), maxX(p.Outline('m', 125)); math.Abs(got-want) > 1e-9 {
		t.Errorf("outline scaled %v want %v", got, want)
	}
	m := a.Metrics(16) // adjusted size 20
	pm := p.Metrics(20)
	if math.Abs(m.Ascent-0.9*20) > 1e-9 || math.Abs(m.Descent-0.2*20) > 1e-9 || m.LineGap != pm.LineGap || math.Abs(m.XHeight-pm.XHeight) > 1e-9 {
		t.Errorf("metrics %+v vs plain at 20px %+v", m, pm)
	}
	if got, want := a.KernF('A', 'V', 16), p.KernF('A', 'V', 16)*1.25; got != want {
		t.Errorf("kern %v want %v", got, want)
	}
}

func TestFontFaceFeatureSettingsDescriptorCanTurnLigaturesOff(t *testing.T) {
	sf, _, err := Decode(fonttest.Icons("I").SFNT(), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	on := NewWebFace(FaceDesc{Family: "I"})
	off := NewWebFace(FaceDesc{Family: "I", Features: `"kern" 1, "liga" 0`})
	_ = on.SetData(sf)
	_ = off.SetData(sf)
	if !on.Face(400).HasLigatures() || off.Face(400).HasLigatures() {
		t.Fatal("liga 0 in the @font-face rule must disable ligatures of that face")
	}
	if _, n := off.Face(400).Ligature([]rune("home")); n != 0 {
		t.Fatal("ligature applied")
	}
	if gl := off.Face(400).ShapeRun([]rune("home"), 20, ShapeOptions{Liga: true}); len(gl) != 4 {
		t.Fatalf("shaped into %d glyphs", len(gl))
	}
}
