// Package font loads fonts, resolves CSS font-family lists and provides the
// metrics and glyph outlines that layout (advance widths, line boxes) and
// rasterization (outlines) both need, from one source so they agree.
//
// Resolution order for a family name: an explicitly registered web font,
// then system fonts found on disk (the same files a browser on this machine
// would pick), then embedded Go fonts as a last resort so rendering never
// fails and is reproducible when SWEEF_FONTS=embedded is set.
package font

import (
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

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

// Style selects a face within a family.
type Style struct {
	Weight int // 100..900; 400 normal, 700 bold
	Italic bool
}

// Face is one loaded font file.
// Data returns the raw sfnt bytes this face reads glyph tables from. The slice
// is shared; callers must not modify it.
func (f *Face) Data() []byte { return f.data }

type Face struct {
	Family string
	Style  Style
	Mono   bool
	font   *sfnt.Font
	mu     sync.Mutex
	buf    sfnt.Buffer
	upm    int
	data   []byte
	adv    map[rune]float64 // advance in font units (-1 = no glyph)
	kern   map[[2]rune]float64
	gids   map[rune]int
	idx    int
	ot     otCache
	mOK    bool
	mU     [5]float64
	// ur, when set, limits the face to the code points of its @font-face
	// unicode-range; a character outside it is treated as absent.
	ur []URange
	// adj is the @font-face size-adjust factor (0: none) and ovr the metric
	// overrides, as fractions of the adjusted size; negative: not set.
	adj                     float64
	ovrAsc, ovrDesc, ovrGap float64
	hasOvr                  bool
	// vi, when set, is the variable font instance this face draws (see variation.go).
	vi *instanceData
	// noLiga: the @font-face font-feature-settings turns standard ligatures off for this face.
	noLiga bool
}

// Metrics are in pixels at the requested size.
type Metrics struct {
	Ascent, Descent, LineGap float64
	XHeight, CapHeight       float64
	UnitsPerEm               int
}

func newFace(family string, st Style, data []byte, mono bool) (*Face, error) {
	return newFaceIdx(family, st, data, mono, -1)
}

func newFaceIdx(family string, st Style, data []byte, mono bool, idx int) (*Face, error) {
	f, data, err := parseSFNT(data, idx)
	if err != nil {
		return nil, err
	}
	return &Face{Family: family, Style: st, Mono: mono, font: f, upm: int(f.UnitsPerEm()), data: data,
		adv: map[rune]float64{}, kern: map[[2]rune]float64{}, idx: -1}, nil
}

// parseSFNT parses font idx of data (idx < 0: a single font file). Collections
// and fonts with unaligned tables are rewritten into a standalone sfnt first;
// the returned bytes are the ones the font reads from.
func parseSFNT(data []byte, idx int) (*sfnt.Font, []byte, error) {
	if idx < 0 && !(len(data) >= 4 && string(data[:4]) == "ttcf") {
		if f, err := sfnt.Parse(data); err == nil {
			return f, data, nil
		}
	}
	if d, ok := extractSFNT(data, idx); ok {
		f, err := sfnt.Parse(d)
		return f, d, err
	}
	f, err := sfnt.Parse(data)
	return f, data, err
}

// UnitsPerEm of the face.
func (f *Face) UnitsPerEm() int { return f.upm }

// AdvanceUnits is the horizontal advance of r in font units, or -1 when the
// face has no glyph for r.
func (f *Face) AdvanceUnits(r rune) float64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	if v, ok := f.adv[r]; ok {
		return v
	}
	v := -1.0
	if f.inRange(r) {
		if gi, err := f.font.GlyphIndex(&f.buf, r); err == nil && gi != 0 {
			v = f.advanceGIDLocked(int(gi))
		}
	}
	f.adv[r] = v
	return v
}

// sa is the size-adjust factor of the face (1 for ordinary fonts).
func (f *Face) sa() float64 {
	if f.adj == 0 {
		return 1
	}
	return f.adj
}

// inRange reports whether r is inside the face's unicode-range.
func (f *Face) inRange(r rune) bool { return f.ur == nil || inRanges(f.ur, r) }

// advanceGIDLocked is the advance of a glyph in font units (variable fonts apply their deltas).
func (f *Face) advanceGIDLocked(gi int) float64 {
	if f.vi != nil {
		return f.vi.advance(gi) * f.sa()
	}
	a, err := f.font.GlyphAdvance(&f.buf, glyphIndex(gi), fixed.Int26_6(f.upm*64), xfont.HintingNone)
	if err != nil {
		return 0
	}
	return float64(a) / 64 * f.sa()
}

// FTSize is the font size as FreeType (and so Skia and Chromium) sees it:
// requested in 26.6 fixed point with truncation, e.g. 21.6px is 21.59375px.
// Advances and kerning are linear in this size, so layout and painting that
// want to agree with Chromium to a fraction of a pixel over a long line must
// use it.
func FTSize(size float64) float64 {
	if size <= 0 || size != size {
		return size
	}
	return math.Floor(size*64) / 64
}

// AdvanceF is the exact (unrounded) advance of r at size px, or -1. The size
// is quantized like FreeType's (see FTSize).
func (f *Face) AdvanceF(r rune, size float64) float64 {
	u := f.AdvanceUnits(r)
	if u < 0 {
		return -1
	}
	return u * FTSize(size) / float64(f.upm)
}

// kernUnits is the pair kerning between a and b in font units: GPOS pair
// adjustment when the font has a kern feature (as HarfBuzz), else the legacy
// kern table.
func (f *Face) kernUnits(a, b rune) float64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := [2]rune{a, b}
	if k, ok := f.kern[key]; ok {
		return k
	}
	k := 0.0
	ga, err1 := f.font.GlyphIndex(&f.buf, a)
	gb, err2 := f.font.GlyphIndex(&f.buf, b)
	if err1 == nil && err2 == nil && ga != 0 && gb != 0 {
		if ot := f.loadOT(); ot.kern != nil {
			k = float64(ot.kern.pairAdjust(int(ga), int(gb)))
		} else if v, err := f.font.Kern(&f.buf, ga, gb, fixed.Int26_6(f.upm*64), xfont.HintingNone); err == nil {
			k = float64(v) / 64
		}
	}
	k *= f.sa()
	f.kern[key] = k
	return k
}

// KernF is the exact kerning adjustment between a and b at size px.
func (f *Face) KernF(a, b rune, size float64) float64 {
	return f.kernUnits(a, b) * FTSize(size) / float64(f.upm)
}

// RawMetrics returns unscaled hhea ascent/descent/lineGap and OS/2 values in font units.
func (f *Face) RawMetrics() (ascent, descent, lineGap, avgChar, maxAdvance float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.font.Metrics(&f.buf, fixed.Int26_6(f.upm*64), xfont.HintingNone)
	if err == nil {
		ascent, descent = float64(m.Ascent)/64, float64(m.Descent)/64
		lineGap = float64(m.Height-m.Ascent-m.Descent) / 64
	}
	avgChar, maxAdvance = tableAvgMax(f.data)
	if a := f.sa(); a != 1 {
		ascent, descent, lineGap, avgChar, maxAdvance = ascent*a, descent*a, lineGap*a, avgChar*a, maxAdvance*a
	}
	return
}

// BBoxWidth is the width of the font's global bounding box (head xMax-xMin) in font units.
func (f *Face) BBoxWidth() float64 {
	d := f.data
	if len(d) < 12 {
		return 0
	}
	n := int(d[4])<<8 | int(d[5])
	for i := 0; i < n; i++ {
		o := 12 + i*16
		if o+16 > len(d) {
			break
		}
		if string(d[o:o+4]) == "head" {
			off := int(d[o+8])<<24 | int(d[o+9])<<16 | int(d[o+10])<<8 | int(d[o+11])
			if off+44 <= len(d) {
				xmin := int16(uint16(d[off+36])<<8 | uint16(d[off+37]))
				xmax := int16(uint16(d[off+40])<<8 | uint16(d[off+41]))
				return float64(xmax) - float64(xmin)
			}
		}
	}
	return 0
}

func tableAvgMax(d []byte) (avg, maxAdv float64) {
	if len(d) < 12 {
		return
	}
	n := int(d[4])<<8 | int(d[5])
	for i := 0; i < n; i++ {
		o := 12 + i*16
		if o+16 > len(d) {
			break
		}
		tag := string(d[o : o+4])
		off := int(d[o+8])<<24 | int(d[o+9])<<16 | int(d[o+10])<<8 | int(d[o+11])
		switch tag {
		case "OS/2":
			if off+4 <= len(d) {
				v := int16(uint16(d[off+2])<<8 | uint16(d[off+3]))
				avg = float64(v)
			}
		case "hhea":
			if off+12 <= len(d) {
				maxAdv = float64(uint16(d[off+10])<<8 | uint16(d[off+11]))
			}
		}
	}
	return
}

// Parse loads a font from raw TTF/OTF bytes (used for @font-face).
func Parse(family string, st Style, data []byte) (*Face, error) {
	return newFace(family, st, data, false)
}

func (f *Face) scale(size float64) fixed.Int26_6 { return fixed.Int26_6(size * 64) }

// Metrics returns vertical metrics scaled to size (px), computed exactly from
// font units (not from 26.6 fixed-point values) so rounding matches browsers.
func (f *Face) Metrics(size float64) Metrics {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.mOK {
		m, err := f.font.Metrics(&f.buf, fixed.Int26_6(f.upm*64), xfont.HintingNone)
		if err != nil {
			return Metrics{Ascent: size * 0.9, Descent: size * 0.2, UnitsPerEm: f.upm}
		}
		u := func(v fixed.Int26_6) float64 { return float64(v) / 64 }
		f.mU = [5]float64{u(m.Ascent), u(m.Descent), u(m.Height - m.Ascent - m.Descent), u(m.XHeight), u(m.CapHeight)}
		f.mOK = true
	}
	asz := size * f.sa() // the size-adjusted font size
	k := asz / float64(f.upm)
	m := Metrics{Ascent: f.mU[0] * k, Descent: f.mU[1] * k, LineGap: f.mU[2] * k,
		XHeight: f.mU[3] * k, CapHeight: f.mU[4] * k, UnitsPerEm: f.upm}
	if f.hasOvr { // @font-face ascent-override, descent-override, line-gap-override: fractions of the adjusted size
		if f.ovrAsc >= 0 {
			m.Ascent = f.ovrAsc * asz
		}
		if f.ovrDesc >= 0 {
			m.Descent = f.ovrDesc * asz
		}
		if f.ovrGap >= 0 {
			m.LineGap = f.ovrGap * asz
		}
	}
	return m
}

// Advance is the horizontal advance of r at size (px), unrounded.
func (f *Face) Advance(r rune, size float64) float64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.inRange(r) {
		return -1
	}
	gi, err := f.font.GlyphIndex(&f.buf, r)
	if err != nil || gi == 0 {
		return -1
	}
	if f.vi != nil {
		return f.vi.advance(int(gi)) * size * f.sa() / float64(f.upm)
	}
	adv, err := f.font.GlyphAdvance(&f.buf, gi, f.scale(size*f.sa()), xfont.HintingNone)
	if err != nil {
		return -1
	}
	return float64(adv) / 64
}

// HasGlyph reports whether the face defines r.
func (f *Face) HasGlyph(r rune) bool {
	if !f.inRange(r) {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	gi, err := f.font.GlyphIndex(&f.buf, r)
	return err == nil && gi != 0
}

// Kern returns the kerning adjustment between two runes in px.
func (f *Face) Kern(a, b rune, size float64) float64 { return f.KernF(a, b, size) }

// Segment is one outline command in pixel space, y down, origin at the
// baseline-left of the glyph.
type Segment struct {
	Op     byte // 'M' move, 'L' line, 'Q' quad, 'C' cubic
	X0, Y0 float64
	X1, Y1 float64
	X2, Y2 float64
}

// Outline returns the glyph outline for r at size, or nil when undefined.
func (f *Face) Outline(r rune, size float64) []Segment {
	if !f.inRange(r) {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	gi, err := f.font.GlyphIndex(&f.buf, r)
	if err != nil || gi == 0 {
		return nil
	}
	return f.outlineLocked(gi, size)
}

func (f *Face) outlineLocked(gi sfnt.GlyphIndex, size float64) (out []Segment) {
	size *= f.sa()
	defer func() {
		if recover() != nil {
			out = nil // a hostile glyph must not take the browser down
		}
	}()
	if f.vi != nil {
		return f.vi.outline(int(gi), size)
	}
	segs, err := f.font.LoadGlyph(&f.buf, gi, f.scale(size), nil)
	if err != nil {
		return nil
	}
	return convertSegments(segs)
}

// convertSegments turns golang.org/x/image/font/sfnt segments (26.6 fixed point) into Segments in pixels.
func convertSegments(segs sfnt.Segments) []Segment {
	px := func(v fixed.Int26_6) float64 { return float64(v) / 64 }
	out := make([]Segment, 0, len(segs))
	for _, s := range segs {
		switch s.Op {
		case sfnt.SegmentOpMoveTo:
			out = append(out, Segment{Op: 'M', X0: px(s.Args[0].X), Y0: px(s.Args[0].Y)})
		case sfnt.SegmentOpLineTo:
			out = append(out, Segment{Op: 'L', X0: px(s.Args[0].X), Y0: px(s.Args[0].Y)})
		case sfnt.SegmentOpQuadTo:
			out = append(out, Segment{Op: 'Q', X0: px(s.Args[0].X), Y0: px(s.Args[0].Y), X1: px(s.Args[1].X), Y1: px(s.Args[1].Y)})
		case sfnt.SegmentOpCubeTo:
			out = append(out, Segment{Op: 'C', X0: px(s.Args[0].X), Y0: px(s.Args[0].Y), X1: px(s.Args[1].X), Y1: px(s.Args[1].Y), X2: px(s.Args[2].X), Y2: px(s.Args[2].Y)})
		}
	}
	return out
}

// ---- registry -----------------------------------------------------------------

type entry struct {
	family string
	style  Style
	path   string
	face   *Face
	mono   bool
	embed  []byte
	idx    int    // font index within a .ttc, -1 otherwise
	full   string // lower-case full font name and PostScript name (for local())
	ps     string
}

var (
	mu      sync.Mutex
	loaded  bool
	entries []*entry
	web     []*entry
)

func embedded() []*entry {
	return []*entry{
		{family: "go", style: Style{400, false}, embed: goregular.TTF, idx: -1},
		{family: "go", style: Style{700, false}, embed: gobold.TTF, idx: -1},
		{family: "go", style: Style{400, true}, embed: goitalic.TTF, idx: -1},
		{family: "go", style: Style{700, true}, embed: gobolditalic.TTF, idx: -1},
		{family: "go mono", style: Style{400, false}, embed: gomono.TTF, mono: true, idx: -1},
		{family: "go mono", style: Style{700, false}, embed: gomonobold.TTF, mono: true, idx: -1},
		{family: "go mono", style: Style{400, true}, embed: gomonoitalic.TTF, mono: true, idx: -1},
		{family: "go mono", style: Style{700, true}, embed: gomonobolditalic.TTF, mono: true, idx: -1},
	}
}

func systemDirs() []string {
	dirs := []string{"/usr/share/fonts", "/usr/local/share/fonts", "/Library/Fonts", "/System/Library/Fonts", "C:\\Windows\\Fonts"}
	if h, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(h, ".fonts"), filepath.Join(h, ".local/share/fonts"), filepath.Join(h, "Library/Fonts"))
	}
	if d := os.Getenv("SWEEF_FONT_DIR"); d != "" {
		dirs = append([]string{d}, dirs...)
	}
	return dirs
}

func load() {
	mu.Lock()
	defer mu.Unlock()
	if loaded {
		return
	}
	loaded = true
	if os.Getenv("SWEEF_FONTS") != "embedded" {
		for _, d := range systemDirs() {
			_ = filepath.WalkDir(d, func(p string, de os.DirEntry, err error) error {
				if err != nil || de.IsDir() {
					return nil
				}
				ext := strings.ToLower(filepath.Ext(p))
				if ext != ".ttf" && ext != ".otf" && ext != ".ttc" {
					return nil
				}
				entries = append(entries, probeAll(p, ext == ".ttc")...)
				return nil
			})
		}
	}
	entries = append(entries, embedded()...)
	// stable order: system first (as discovered), embedded last
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].embed == nil && entries[j].embed != nil })
}

// probeAll lists the fonts of a file (several for a .ttc collection).
func probeAll(path string, coll bool) []*entry {
	if !coll {
		if e := probe(path, -1); e != nil {
			return []*entry{e}
		}
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	c, err := sfnt.ParseCollection(data)
	if err != nil {
		return nil
	}
	var out []*entry
	for i := 0; i < c.NumFonts(); i++ {
		if e := probe(path, i); e != nil {
			out = append(out, e)
		}
	}
	return out
}

// probe reads just enough of a font file to learn family and style.
func probe(path string, idx int) *entry {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	f, _, err := parseSFNT(data, idx)
	if err != nil {
		return nil
	}
	var b sfnt.Buffer
	fam, _ := f.Name(&b, sfnt.NameIDTypographicFamily)
	if fam == "" {
		fam, _ = f.Name(&b, sfnt.NameIDFamily)
	}
	sub, _ := f.Name(&b, sfnt.NameIDTypographicSubfamily)
	if sub == "" {
		sub, _ = f.Name(&b, sfnt.NameIDSubfamily)
	}
	if fam == "" {
		return nil
	}
	st := Style{Weight: 400}
	ls := strings.ToLower(sub)
	switch {
	case strings.Contains(ls, "thin") || strings.Contains(ls, "hairline"):
		st.Weight = 100
	case strings.Contains(ls, "extralight") || strings.Contains(ls, "ultralight"):
		st.Weight = 200
	case strings.Contains(ls, "light"):
		st.Weight = 300
	case strings.Contains(ls, "medium"):
		st.Weight = 500
	case strings.Contains(ls, "semibold") || strings.Contains(ls, "demibold"):
		st.Weight = 600
	case strings.Contains(ls, "extrabold") || strings.Contains(ls, "ultrabold"):
		st.Weight = 800
	case strings.Contains(ls, "black") || strings.Contains(ls, "heavy"):
		st.Weight = 900
	case strings.Contains(ls, "bold"):
		st.Weight = 700
	}
	if strings.Contains(ls, "italic") || strings.Contains(ls, "oblique") {
		st.Italic = true
	}
	mono := false
	if p := f.PostTable(); p != nil {
		mono = p.IsFixedPitch
	}
	full, _ := f.Name(&b, sfnt.NameIDFull)
	ps, _ := f.Name(&b, sfnt.NameIDPostScript)
	return &entry{family: strings.ToLower(fam), style: st, path: path, mono: mono, idx: idx, full: strings.ToLower(full), ps: strings.ToLower(ps)}
}

func (e *entry) get() *Face {
	mu.Lock()
	defer mu.Unlock()
	if e.face != nil {
		return e.face
	}
	var data []byte
	if e.embed != nil {
		data = e.embed
	} else {
		var err error
		if data, err = os.ReadFile(e.path); err != nil {
			return nil
		}
	}
	f, err := newFaceIdx(e.family, e.style, data, e.mono, e.idx)
	if err != nil {
		return nil
	}
	e.face = f
	return f
}

// RegisterWeb adds an @font-face font that takes priority over system fonts.
func RegisterWeb(family string, st Style, data []byte) error {
	f, err := newFace(strings.ToLower(family), st, data, false)
	if err != nil {
		return err
	}
	mu.Lock()
	web = append(web, &entry{family: strings.ToLower(family), style: st, face: f, idx: -1})
	mu.Unlock()
	return nil
}

// generic CSS families map to what Chromium on this machine resolves them to
// (measured). A generic that Chromium cannot resolve is absent, so the next
// family in the list (and finally the default serif) is used.
var generics = map[string][]string{
	"serif":         {"liberation serif", "times new roman", "dejavu serif", "go"},
	"sans-serif":    {"liberation sans", "arial", "dejavu sans", "go"},
	"monospace":     {"dejavu sans mono", "liberation mono", "go mono"},
	"system-ui":     {"inter", "liberation sans", "go"},
	"ui-sans-serif": {"inter", "liberation sans", "go"},
	"-apple-system": {"inter"}, "blinkmacsystemfont": {"inter"},
	"segoe ui": {"inter"}, "helvetica neue": {"inter"}, "sf pro text": {"inter"}, "sf pro display": {"inter"},
}

// aliases are metric-compatible substitutions fontconfig performs.
var aliases = map[string]string{
	"helvetica": "liberation sans", "arial": "liberation sans", "arimo": "liberation sans",
	"times": "liberation serif", "times new roman": "liberation serif", "tinos": "liberation serif",
	"courier": "liberation mono", "courier new": "liberation mono", "cousine": "liberation mono",
	"calibri": "carlito", "cambria": "caladea",
}

func match(list []*entry, family string, st Style) *entry {
	var best *entry
	bestScore := 1 << 30
	for _, e := range list {
		if e.family != family {
			continue
		}
		score := abs(e.style.Weight - st.Weight)
		if e.style.Italic != st.Italic {
			score += 1000
		}
		if score < bestScore {
			best, bestScore = e, score
		}
	}
	return best
}

func abs(a int) int {
	if a < 0 {
		return -a
	}
	return a
}

// Lookup resolves a single family name (or generic keyword) and style.
func Lookup(family string, st Style) *Face {
	if strings.IndexByte(family, 0) >= 0 {
		if l := LookupAll(family, st); len(l) > 0 {
			return l[0]
		}
		return nil
	}
	load()
	family = strings.ToLower(strings.Trim(strings.TrimSpace(family), `"'`))
	if st.Weight == 0 {
		st.Weight = 400
	}
	mu.Lock()
	w := append([]*entry(nil), web...)
	mu.Unlock()
	if e := match(w, family, st); e != nil {
		return e.get()
	}
	if gl, ok := generics[family]; ok {
		for _, g := range gl {
			if e := match(entries, g, st); e != nil {
				if f := e.get(); f != nil {
					return f
				}
			}
		}
		return nil
	}
	if e := match(entries, family, st); e != nil {
		return e.get()
	}
	if a, ok := aliases[family]; ok {
		if e := match(entries, a, st); e != nil {
			return e.get()
		}
	}
	return nil
}

// Resolve picks the first available face from a CSS font-family list.
func Resolve(families []string, st Style) *Face {
	for _, f := range families {
		if face := Lookup(f, st); face != nil {
			return face
		}
	}
	return Lookup("serif", st)
}

// fallback preference orders (what fontconfig's configuration on the reference
// machine yields for characters the requested families lack).
var (
	fbLatin = []string{"inter", "dejavu sans", "liberation sans", "dejavu serif", "liberation serif", "dejavu sans mono"}
	fbCJK   = []string{"wenquanyi zen hei", "wenquanyi zen hei mono", "ipagothic", "ipapgothic", "inter", "dejavu sans"}
	fbEmoji = []string{"noto color emoji", "dejavu sans", "inter"}
)

func isCJKRune(r rune) bool {
	return r >= 0x2e80 && r <= 0xa4cf || r >= 0xac00 && r <= 0xd7a3 || r >= 0xf900 && r <= 0xfaff || r >= 0xfe30 && r <= 0xfe4f || r >= 0xff00 && r <= 0xffef || r >= 0x20000 && r <= 0x2fa1f || r >= 0x3000 && r <= 0x30ff
}

func isEmojiRune(r rune) bool {
	return r >= 0x1f300 && r <= 0x1faff || r >= 0x2600 && r <= 0x27bf && r != 0x2713 && r != 0x2605
}

// Fallback finds a face that has a glyph for r, preferring the given list.
func Fallback(r rune, families []string, st Style) *Face {
	for _, f := range families {
		if strings.IndexByte(f, 0) >= 0 {
			for _, face := range LookupAll(f, st) {
				if face.HasGlyph(r) {
					return face
				}
			}
			continue
		}
		if face := Lookup(f, st); face != nil && face.HasGlyph(r) {
			return face
		}
	}
	load()
	order := fbLatin
	switch {
	case isEmojiRune(r):
		order = fbEmoji
	case isCJKRune(r):
		order = fbCJK
	}
	for _, name := range order {
		if face := Lookup(name, st); face != nil && face.HasGlyph(r) {
			return face
		}
	}
	for _, e := range entries {
		if e.style.Weight == st.Weight && e.style.Italic == st.Italic {
			if f := e.get(); f != nil && f.HasGlyph(r) {
				return f
			}
		}
	}
	for _, e := range entries {
		if f := e.get(); f != nil && f.HasGlyph(r) {
			return f
		}
	}
	return nil
}

// Families lists installed family names (for diagnostics).
func Families() []string {
	load()
	seen := map[string]bool{}
	var out []string
	for _, e := range entries {
		if !seen[e.family] {
			seen[e.family] = true
			out = append(out, e.family)
		}
	}
	sort.Strings(out)
	return out
}

// TypoMetrics returns the OS/2 typographic ascender and descender (the
// descender as a positive distance below the baseline) scaled to size px,
// unrounded; ok is false when the face has no usable OS/2 table. Chromium
// derives the canvas "top", "middle" and "bottom" text baselines from them.
func (f *Face) TypoMetrics(size float64) (asc, desc float64, ok bool) {
	os2 := findTable(f.data, f.idx, "OS/2")
	if len(os2) < 72 {
		return 0, 0, false
	}
	a, d := bei16(os2, 68), bei16(os2, 70)
	if a == 0 && d == 0 {
		return 0, 0, false
	}
	k := FTSize(size) * f.sa() / float64(f.upm)
	return float64(a) * k, float64(-d) * k, true
}

func be16(b []byte, o int) int {
	if o < 0 || o+2 > len(b) {
		return 0
	}
	return int(b[o])<<8 | int(b[o+1])
}

func bei16(b []byte, o int) int { return int(int16(be16(b, o))) }

func glyphIndex(g int) sfnt.GlyphIndex { return sfnt.GlyphIndex(g) }

// Local finds an installed font by the name a CSS local() source gives: its
// full name or PostScript name, as browsers do, or (a convenience browsers
// lack but fontconfig makes natural) a family name, in which case the face
// closest to st is used. nil when no such font is installed.
func Local(name string, st Style) *Face {
	load()
	key := strings.ToLower(strings.Join(strings.Fields(strings.Trim(name, `"'`)), " "))
	if key == "" {
		return nil
	}
	for _, e := range entries {
		if e.embed == nil && (e.full == key || e.ps == key) {
			return e.get()
		}
	}
	if st.Weight == 0 {
		st.Weight = 400
	}
	if e := match(entries, key, st); e != nil && e.embed == nil {
		return e.get()
	}
	if a, ok := aliases[key]; ok {
		if e := match(entries, a, st); e != nil && e.embed == nil {
			return e.get()
		}
	}
	return nil
}
