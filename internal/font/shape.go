package font

import (
	xfont "golang.org/x/image/font"
	"golang.org/x/image/math/fixed"
)

// ShapedGlyph is one glyph of a run shaped with ShapeRun.
type ShapedGlyph struct {
	GID     int     // glyph id in the face; 0 when the face lacks the character
	Cluster int     // index of the first rune of the cluster in the input
	NRunes  int     // runes consumed (2 or 3 for a ligature)
	X       float64 // pen position of the glyph in px, kerning included
	Adv     float64 // advance in px
}

// Options for ShapeRun.
type ShapeOptions struct {
	Kern bool // apply GPOS (or legacy kern table) pair kerning
	Liga bool // apply standard GSUB ligatures (liga, clig, rlig)
}

func (f *Face) gid(r rune) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	gi, err := f.font.GlyphIndex(&f.buf, r)
	if err != nil {
		return 0
	}
	return int(gi)
}

// AdvanceGIDUnits is the advance of glyph gid in font units.
func (f *Face) AdvanceGIDUnits(gid int) float64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, err := f.font.GlyphAdvance(&f.buf, glyphIndex(gid), fixed.Int26_6(f.upm*64), xfont.HintingNone)
	if err != nil {
		return 0
	}
	return float64(a) / 64
}

// kernGID is the pair adjustment between two glyph ids in font units.
func (f *Face) kernGID(a, b int) float64 {
	if a == 0 || b == 0 {
		return 0
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if ot := f.loadOT(); ot.kern != nil {
		return float64(ot.kern.pairAdjust(a, b))
	}
	k, err := f.font.Kern(&f.buf, glyphIndex(a), glyphIndex(b), fixed.Int26_6(f.upm*64), xfont.HintingNone)
	if err != nil {
		return 0
	}
	return float64(k) / 64
}

// ShapeRun shapes rs (one script, one face) at size px the way HarfBuzz does
// for Latin: standard ligatures first, then pair kerning, advances linear in
// the FreeType-quantized size (see FTSize). Runes the face lacks yield glyph
// 0 with a zero advance; callers handle fallback before shaping.
func (f *Face) ShapeRun(rs []rune, size float64, o ShapeOptions) []ShapedGlyph {
	k := FTSize(size) / float64(f.upm)
	gids := make([]int, len(rs))
	for i, r := range rs {
		gids[i] = f.gid(r)
	}
	var lig *otLayout
	if o.Liga {
		f.mu.Lock()
		lig = f.loadOT().liga
		f.mu.Unlock()
	}
	out := make([]ShapedGlyph, 0, len(rs))
	x := 0.0
	prev := 0
	for i := 0; i < len(rs); {
		g, n := gids[i], 1
		if lig != nil && g != 0 && i+1 < len(rs) {
			end := i + 4
			if end > len(rs) {
				end = len(rs)
			}
			f.mu.Lock()
			lg, cc := lig.ligature(gids[i:end])
			f.mu.Unlock()
			if cc >= 2 {
				g, n = lg, cc
			}
		}
		if o.Kern && prev != 0 && g != 0 {
			x += f.kernGID(prev, g) * k
		}
		adv := 0.0
		if g != 0 {
			adv = f.AdvanceGIDUnits(g) * k
		}
		out = append(out, ShapedGlyph{GID: g, Cluster: i, NRunes: n, X: x, Adv: adv})
		x += adv
		prev = g
		i += n
	}
	return out
}

// OutlineGID is Outline for a glyph id.
func (f *Face) OutlineGID(gid int, size float64) []Segment {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.outlineLocked(glyphIndex(gid), size)
}
