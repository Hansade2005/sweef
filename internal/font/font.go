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
}

// Metrics are in pixels at the requested size.
type Metrics struct {
	Ascent, Descent, LineGap float64
	XHeight, CapHeight       float64
	UnitsPerEm               int
}

func newFace(family string, st Style, data []byte, mono bool) (*Face, error) {
	f, err := sfnt.Parse(data)
	if err != nil {
		return nil, err
	}
	return &Face{Family: family, Style: st, Mono: mono, font: f, upm: int(f.UnitsPerEm()), data: data,
		adv: map[rune]float64{}, kern: map[[2]rune]float64{}}, nil
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
	if gi, err := f.font.GlyphIndex(&f.buf, r); err == nil && gi != 0 {
		if a, err := f.font.GlyphAdvance(&f.buf, gi, fixed.Int26_6(f.upm*64), xfont.HintingNone); err == nil {
			v = float64(a) / 64
		}
	}
	f.adv[r] = v
	return v
}

// AdvanceF is the exact (unrounded) advance of r at size px, or -1.
func (f *Face) AdvanceF(r rune, size float64) float64 {
	u := f.AdvanceUnits(r)
	if u < 0 {
		return -1
	}
	return u * size / float64(f.upm)
}

// KernF is the exact kerning adjustment between a and b at size px.
func (f *Face) KernF(a, b rune, size float64) float64 {
	f.mu.Lock()
	k, ok := f.kern[[2]rune{a, b}]
	if !ok {
		k = 0
		ga, err1 := f.font.GlyphIndex(&f.buf, a)
		gb, err2 := f.font.GlyphIndex(&f.buf, b)
		if err1 == nil && err2 == nil && ga != 0 && gb != 0 {
			if v, err := f.font.Kern(&f.buf, ga, gb, fixed.Int26_6(f.upm*64), xfont.HintingNone); err == nil {
				k = float64(v) / 64
			}
		}
		f.kern[[2]rune{a, b}] = k
	}
	f.mu.Unlock()
	return k * size / float64(f.upm)
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
	return
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

// Metrics returns vertical metrics scaled to size (px).
func (f *Face) Metrics(size float64) Metrics {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.font.Metrics(&f.buf, f.scale(size), xfont.HintingNone)
	if err != nil {
		return Metrics{Ascent: size * 0.9, Descent: size * 0.2, UnitsPerEm: f.upm}
	}
	px := func(v fixed.Int26_6) float64 { return float64(v) / 64 }
	return Metrics{Ascent: px(m.Ascent), Descent: px(m.Descent), LineGap: px(m.Height - m.Ascent - m.Descent),
		XHeight: px(m.XHeight), CapHeight: px(m.CapHeight), UnitsPerEm: f.upm}
}

// Advance is the horizontal advance of r at size (px), unrounded.
func (f *Face) Advance(r rune, size float64) float64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	gi, err := f.font.GlyphIndex(&f.buf, r)
	if err != nil || gi == 0 {
		return -1
	}
	adv, err := f.font.GlyphAdvance(&f.buf, gi, f.scale(size), xfont.HintingNone)
	if err != nil {
		return -1
	}
	return float64(adv) / 64
}

// HasGlyph reports whether the face defines r.
func (f *Face) HasGlyph(r rune) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	gi, err := f.font.GlyphIndex(&f.buf, r)
	return err == nil && gi != 0
}

// Kern returns the kerning adjustment between two runes in px.
func (f *Face) Kern(a, b rune, size float64) float64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	ga, err1 := f.font.GlyphIndex(&f.buf, a)
	gb, err2 := f.font.GlyphIndex(&f.buf, b)
	if err1 != nil || err2 != nil {
		return 0
	}
	k, err := f.font.Kern(&f.buf, ga, gb, f.scale(size), xfont.HintingNone)
	if err != nil {
		return 0
	}
	return float64(k) / 64
}

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
	f.mu.Lock()
	defer f.mu.Unlock()
	gi, err := f.font.GlyphIndex(&f.buf, r)
	if err != nil || gi == 0 {
		return nil
	}
	segs, err := f.font.LoadGlyph(&f.buf, gi, f.scale(size), nil)
	if err != nil {
		return nil
	}
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
}

var (
	mu      sync.Mutex
	loaded  bool
	entries []*entry
	web     []*entry
)

func embedded() []*entry {
	return []*entry{
		{family: "go", style: Style{400, false}, embed: goregular.TTF},
		{family: "go", style: Style{700, false}, embed: gobold.TTF},
		{family: "go", style: Style{400, true}, embed: goitalic.TTF},
		{family: "go", style: Style{700, true}, embed: gobolditalic.TTF},
		{family: "go mono", style: Style{400, false}, embed: gomono.TTF, mono: true},
		{family: "go mono", style: Style{700, false}, embed: gomonobold.TTF, mono: true},
		{family: "go mono", style: Style{400, true}, embed: gomonoitalic.TTF, mono: true},
		{family: "go mono", style: Style{700, true}, embed: gomonobolditalic.TTF, mono: true},
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
				if ext != ".ttf" && ext != ".otf" {
					return nil
				}
				if e := probe(p); e != nil {
					entries = append(entries, e)
				}
				return nil
			})
		}
	}
	entries = append(entries, embedded()...)
	// stable order: system first (as discovered), embedded last
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].embed == nil && entries[j].embed != nil })
}

// probe reads just enough of a font file to learn family and style.
func probe(path string) *entry {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	f, err := sfnt.Parse(data)
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
	return &entry{family: strings.ToLower(fam), style: st, path: path, mono: mono}
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
	f, err := newFace(e.family, e.style, data, e.mono)
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
	web = append(web, &entry{family: strings.ToLower(family), style: st, face: f})
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

// Fallback finds any face that has a glyph for r, preferring the given list.
func Fallback(r rune, families []string, st Style) *Face {
	for _, f := range families {
		if face := Lookup(f, st); face != nil && face.HasGlyph(r) {
			return face
		}
	}
	load()
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
