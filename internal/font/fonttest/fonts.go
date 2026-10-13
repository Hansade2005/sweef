package fonttest

// Ready-made test fonts. Their glyphs are boxes, triangles and quadratic
// circles whose widths and heights depend on the character, so text set in
// them cannot be confused with text in any real font. All of this is
// generated code: no third party font data is involved.

func circle(cx, cy, r int) []Pt {
	k := r
	// four on-curve points and four off-curve corners: a rounded diamond-square
	return []Pt{
		{X: cx + r, Y: cy}, {X: cx + k, Y: cy + k, Off: true},
		{X: cx, Y: cy + r}, {X: cx - k, Y: cy + k, Off: true},
		{X: cx - r, Y: cy}, {X: cx - k, Y: cy - k, Off: true},
		{X: cx, Y: cy - r}, {X: cx + k, Y: cy - k, Off: true},
	}
}

func triangle(x0, y0, x1, y1 int) []Pt {
	return []Pt{{X: x0, Y: y0}, {X: x1, Y: y0}, {X: (x0 + x1) / 2, Y: y1}}
}

// Adv is the advance of printable ASCII character c in the Boxes fonts.
func Adv(c rune) int { return 520 + int(c%5)*40 }

// Boxes returns a font with a glyph for every printable ASCII character plus
// U+00E9 (a composite of 'e' and an accent), U+0416 and U+4E2D (so unicode
// range tests have distinct scripts). unitsPerEm is 1000, ascent 800, descent
// 200. Glyph height depends on the character: 'x'-height-like for lowercase,
// taller for capitals and digits. weight changes the horizontal thickness only
// through the advance (bold faces are 8% wider) so the face can be told apart.
func Boxes(family, style string, weight int, italic bool) *Font {
	f := &Font{Family: family, Style: style, UPM: 1000, Ascent: 800, Descent: 200, XHeight: 500, Cap: 700,
		Weight: weight, Italic: italic, Cmap: map[rune]int{}, ShortLoca: true}
	wscale := 100
	if weight >= 600 {
		wscale = 108
	}
	f.Glyphs = append(f.Glyphs, Glyph{Contours: [][]Pt{Rect(50, 0, 450, 700)}, Adv: 500}) // .notdef
	add := func(r rune, g Glyph) int {
		f.Cmap[r] = len(f.Glyphs)
		f.Glyphs = append(f.Glyphs, g)
		return f.Cmap[r]
	}
	for c := rune(0x20); c < 0x7f; c++ {
		adv := Adv(c) * wscale / 100
		if c == ' ' {
			add(c, Glyph{Adv: adv})
			continue
		}
		h := 500
		switch {
		case c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			h = 700
		case c == 'g' || c == 'p' || c == 'q' || c == 'y' || c == 'j':
			h = 500
		case c == 'b' || c == 'd' || c == 'f' || c == 'h' || c == 'k' || c == 'l' || c == 't' || c == 'i':
			h = 720
		}
		var cs [][]Pt
		switch {
		case c == 'o' || c == 'O' || c == '0':
			cs = [][]Pt{circle(adv/2, h/2, h/2-10)}
		case c == 'A' || c == 'V' || c == 'v' || c == 'a':
			cs = [][]Pt{triangle(40, 0, adv-40, h)}
		default:
			cs = [][]Pt{Rect(60, 0, adv-60, h)}
		}
		add(c, Glyph{Contours: cs, Adv: adv})
	}
	// descenders sit below the baseline
	for _, c := range "gpqyj" {
		g := &f.Glyphs[f.Cmap[c]]
		g.Contours = [][]Pt{Rect(60, -200, g.Adv-60, 500)}
	}
	acc := len(f.Glyphs)
	f.Glyphs = append(f.Glyphs, Glyph{Contours: [][]Pt{Rect(0, 0, 120, 80)}, Adv: 0}) // accent glyph (no cmap)
	add(0xe9, Glyph{Comps: []Component{{GID: f.Cmap['e'], DX: 0, DY: 0}, {GID: acc, DX: 200, DY: 600}}, Adv: Adv('e') * wscale / 100})
	add(0x416, Glyph{Contours: [][]Pt{Rect(60, 0, 700, 700), {{X: 200, Y: 100}, {X: 200, Y: 600}, {X: 560, Y: 600}, {X: 560, Y: 100}}}, Adv: 760})
	add(0x4e2d, Glyph{Contours: [][]Pt{Rect(40, -100, 960, 800)}, Adv: 1000})
	return f
}

// Icons returns an icon font in the style of Material Icons: private use code
// points (U+E900..) draw icons, and ligatures turn the icon names ("home",
// "arrow_back", ...) into the same glyphs. The letters themselves draw nothing
// and have a tiny advance, as in the real font.
func Icons(family string) *Font {
	f := &Font{Family: family, Style: "Regular", UPM: 1000, Ascent: 800, Descent: 200, XHeight: 500, Cap: 700,
		Weight: 400, Cmap: map[rune]int{}, ShortLoca: true}
	f.Glyphs = append(f.Glyphs, Glyph{Adv: 1000})
	add := func(r rune, g Glyph) int {
		f.Cmap[r] = len(f.Glyphs)
		f.Glyphs = append(f.Glyphs, g)
		return f.Cmap[r]
	}
	for c := 'a'; c <= 'z'; c++ {
		add(c, Glyph{Adv: 10})
	}
	add('_', Glyph{Adv: 10})
	add(' ', Glyph{Adv: 300})
	icons := []struct {
		name string
		g    Glyph
	}{
		{"home", Glyph{Contours: [][]Pt{triangle(100, 400, 900, 800), Rect(200, 0, 800, 400)}, Adv: 1000}},
		{"arrow_back", Glyph{Contours: [][]Pt{{{X: 100, Y: 400}, {X: 500, Y: 800}, {X: 500, Y: 560}, {X: 900, Y: 560}, {X: 900, Y: 240}, {X: 500, Y: 240}, {X: 500, Y: 0}}}, Adv: 1000}},
		{"menu", Glyph{Contours: [][]Pt{Rect(100, 600, 900, 700), Rect(100, 350, 900, 450), Rect(100, 100, 900, 200)}, Adv: 1000}},
		{"check_circle_outline", Glyph{Contours: [][]Pt{circle(500, 400, 400)}, Adv: 1000}},
	}
	for i, ic := range icons {
		gid := add(rune(0xe900+i), ic.g)
		var comps []int
		for _, c := range ic.name {
			comps = append(comps, f.Cmap[c])
		}
		f.Ligatures = append(f.Ligatures, Lig{Comps: comps, To: gid})
	}
	return f
}
