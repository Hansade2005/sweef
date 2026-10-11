// Package font finds, matches, measures and rasterises fonts for the render
// pipeline. Layout, painting and canvas 2D all measure text through it, so
// the width a line was broken at is exactly the width that gets painted.
//
// Fonts come from the system font directories (the same files Chromium uses
// through fontconfig on Linux), with the Go fonts embedded as a fallback so
// rendering works on a machine with no fonts installed. Family matching
// follows CSS Fonts 4 §5.2: the first family in the list that exists wins,
// then the nearest weight and the requested style within it; a rune the
// chosen face lacks falls back per character to any face that has it.
package font

import (
	"math"
	"strings"
	"sync"
	"unicode"

	"golang.org/x/image/font"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// Style asks for a face: a CSS font-family list (already split, unquoted),
// a numeric weight (100-900), italic/oblique and a size in CSS px.
type Style struct {
	Families []string
	Weight   int
	Italic   bool
	Size     float64
}

// Metrics are a sized face's vertical metrics in CSS px. Ascent and Descent
// are both positive; LineHeight is what `line-height: normal` resolves to.
type Metrics struct {
	Ascent, Descent, LineGap float64
	LineHeight               float64
	XHeight, CapHeight       float64
	UnderlinePos             float64 // distance below the baseline
	UnderlineThick           float64
}

// Face is one font file at one size. It is safe for concurrent use.
type Face struct {
	src  *source
	Size float64
	// Synthetic styling when the family has no real bold or italic.
	FakeBold   bool
	FakeItalic bool

	scale   float64 // px per font unit
	metrics Metrics
	style   Style // the request this face was chosen for (fallback uses it)

	advMu sync.RWMutex
	adv   map[sfnt.GlyphIndex]float64
}

// Glyph is one positioned glyph from Shape. X is the pen position relative
// to the start of the run; Face may be a fallback face.
type Glyph struct {
	Face    *Face
	ID      sfnt.GlyphIndex
	Rune    rune
	X       float64
	Advance float64
	// Cluster is the byte offset in the shaped string this glyph came from.
	Cluster int
}

var (
	faceMu    sync.Mutex
	faceCache = map[faceKey]*Face{}
)

type faceKey struct {
	src        *source
	size       float64
	bold, ital bool
}

// Get returns the face for st, cached. It never returns nil: when nothing
// matches, the embedded Go fonts are used.
func Get(st Style) *Face {
	if st.Size <= 0 {
		st.Size = 16
	}
	if st.Weight == 0 {
		st.Weight = 400
	}
	src := match(st)
	return sized(src, st)
}

func sized(src *source, st Style) *Face {
	src.load()
	fb := st.Weight >= 600 && src.weight < 600 && !src.color
	fi := st.Italic && !src.italic
	k := faceKey{src, st.Size, fb, fi}
	faceMu.Lock()
	defer faceMu.Unlock()
	if f, ok := faceCache[k]; ok {
		return f
	}
	f := &Face{src: src, Size: st.Size, FakeBold: fb, FakeItalic: fi, style: st,
		adv: map[sfnt.GlyphIndex]float64{}}
	f.scale = st.Size / float64(src.upem)
	f.metrics = src.metrics(f.scale)
	faceCache[k] = f
	return f
}

// Family is the family name of the font file behind the face.
func (f *Face) Family() string { return f.src.family }

// Metrics returns the face's vertical metrics.
func (f *Face) Metrics() Metrics { return f.metrics }

// HasRune reports whether the face's font maps r to a glyph.
func (f *Face) HasRune(r rune) bool { return f.src.has(r) }

// GlyphIndex maps r in this face (0 when missing).
func (f *Face) GlyphIndex(r rune) sfnt.GlyphIndex { return f.src.index(r) }

// GlyphAdvance is the horizontal advance of glyph id in px.
func (f *Face) GlyphAdvance(id sfnt.GlyphIndex) float64 {
	f.advMu.RLock()
	a, ok := f.adv[id]
	f.advMu.RUnlock()
	if ok {
		return a
	}
	a = f.src.advanceUnits(id) * f.scale
	if f.FakeBold {
		a += f.Size / 24
	}
	f.advMu.Lock()
	f.adv[id] = a
	f.advMu.Unlock()
	return a
}

// Kern returns the kerning adjustment between two glyphs in px.
func (f *Face) Kern(a, b sfnt.GlyphIndex) float64 { return f.src.kern(a, b) * f.scale }

// faceFor returns the face that should draw r: f itself when it has the
// glyph, else the first fallback face that does.
func (f *Face) faceFor(r rune) *Face {
	if f.src.has(r) || isIgnorable(r) {
		return f
	}
	if src := fallbackFor(r, f.style); src != nil {
		return sized(src, f.style)
	}
	return f
}

func isIgnorable(r rune) bool {
	return r < 0x20 || r == 0x200b || r == 0x200c || r == 0x200d || r == 0xfeff || r == 0xad
}

// Shape lays out s as a single run: per-rune font fallback, advances and
// pair kerning. letterSpacing and wordSpacing are CSS px added after each
// glyph and after each space.
func (f *Face) Shape(s string, letterSpacing, wordSpacing float64) []Glyph {
	out := make([]Glyph, 0, len(s))
	x := 0.0
	var prev *Glyph
	for i, r := range s {
		if r == '\t' {
			r = ' '
		}
		fc := f.faceFor(r)
		id := fc.src.index(r)
		if r == 0xad || r == 0x200b || r == 0x200c || r == 0x200d || r == 0xfeff {
			out = append(out, Glyph{Face: fc, ID: 0, Rune: r, X: x, Cluster: i})
			continue
		}
		if prev != nil && prev.Face == fc && f.src.kerning {
			k := fc.Kern(prev.ID, id)
			x += k
			prev.Advance += k
		}
		adv := fc.GlyphAdvance(id)
		g := Glyph{Face: fc, ID: id, Rune: r, X: x, Advance: adv, Cluster: i}
		adv += letterSpacing
		if r == ' ' || r == 0xa0 {
			adv += wordSpacing
		}
		g.Advance = adv
		x += adv
		out = append(out, g)
		prev = &out[len(out)-1]
	}
	return out
}

// Measure returns the advance width of s in px.
func (f *Face) Measure(s string) float64 {
	return f.MeasureSpaced(s, 0, 0)
}

// MeasureSpaced is Measure with CSS letter-spacing and word-spacing.
func (f *Face) MeasureSpaced(s string, letterSpacing, wordSpacing float64) float64 {
	gs := f.Shape(s, letterSpacing, wordSpacing)
	if len(gs) == 0 {
		return 0
	}
	last := gs[len(gs)-1]
	return last.X + last.Advance
}

// Outline returns glyph id's outline in px, y down, origin on the baseline
// at the pen position. Synthetic italic is applied as a 1/4 horizontal skew;
// synthetic bold is left to the rasteriser (see GlyphMask).
func (f *Face) Outline(id sfnt.GlyphIndex) []Segment {
	segs := f.src.outline(id)
	out := make([]Segment, len(segs))
	sk := 0.0
	if f.FakeItalic {
		sk = 0.2
	}
	for i, s := range segs {
		o := Segment{Op: s.Op}
		for j := 0; j < s.n; j++ {
			x, y := s.pts[j][0]*f.scale, s.pts[j][1]*f.scale
			o.Pts[j] = [2]float64{x - y*sk, y}
		}
		out[i] = o
	}
	return out
}

// SegOp is a path verb in an outline.
type SegOp uint8

const (
	MoveTo SegOp = iota
	LineTo
	QuadTo
	CubeTo
)

// Segment is one outline verb with up to three points (px, y down).
type Segment struct {
	Op  SegOp
	Pts [3][2]float64
}

// N is the number of points the segment uses.
func (s Segment) N() int {
	switch s.Op {
	case QuadTo:
		return 2
	case CubeTo:
		return 3
	}
	return 1
}

// ---- sources

type source struct {
	family  string
	sub     string
	file    string
	idx     int
	weight  int
	italic  bool
	mono    bool
	color   bool // colour bitmap font (emoji): only used as a last resort
	kerning bool

	once  sync.Once
	font  *sfnt.Font
	data  []byte
	upem  int
	err   error
	bufMu sync.Mutex
	buf   sfnt.Buffer

	cmapMu sync.RWMutex
	cmap   map[rune]sfnt.GlyphIndex

	outMu  sync.Mutex
	outs   map[sfnt.GlyphIndex][]rawSeg
	advs   map[sfnt.GlyphIndex]float64
	hhea   font.Metrics
	os2    os2Info
	loaded bool
}

type rawSeg struct {
	Op  SegOp
	n   int
	pts [3][2]float64 // font units, y down
}

func (s *source) load() bool {
	s.once.Do(func() {
		f, data, err := openSource(s)
		if err != nil {
			s.err = err
			return
		}
		s.font, s.data = f, data
		s.upem = int(f.UnitsPerEm())
		if s.upem == 0 {
			s.upem = 1000
		}
		s.cmap = map[rune]sfnt.GlyphIndex{}
		s.outs = map[sfnt.GlyphIndex][]rawSeg{}
		s.advs = map[sfnt.GlyphIndex]float64{}
		ppem := fixed.Int26_6(s.upem << 6)
		s.bufMu.Lock()
		s.hhea, _ = f.Metrics(&s.buf, ppem, font.HintingNone)
		a, _ := f.GlyphIndex(&s.buf, 'A')
		v, _ := f.GlyphIndex(&s.buf, 'V')
		if a != 0 && v != 0 {
			if k, err := f.Kern(&s.buf, a, v, ppem, font.HintingNone); err == nil && k != 0 {
				s.kerning = true
			}
		}
		s.bufMu.Unlock()
		s.os2 = readOS2(data, s.idx)
		s.loaded = true
	})
	return s.loaded
}

func (s *source) index(r rune) sfnt.GlyphIndex {
	if !s.load() {
		return 0
	}
	s.cmapMu.RLock()
	g, ok := s.cmap[r]
	s.cmapMu.RUnlock()
	if ok {
		return g
	}
	s.bufMu.Lock()
	g, _ = s.font.GlyphIndex(&s.buf, r)
	s.bufMu.Unlock()
	s.cmapMu.Lock()
	s.cmap[r] = g
	s.cmapMu.Unlock()
	return g
}

func (s *source) has(r rune) bool {
	if r == ' ' || r == 0xa0 {
		return s.index(' ') != 0 || s.index(r) != 0
	}
	return s.index(r) != 0
}

func (s *source) advanceUnits(id sfnt.GlyphIndex) float64 {
	if !s.load() {
		return 0
	}
	s.outMu.Lock()
	a, ok := s.advs[id]
	s.outMu.Unlock()
	if ok {
		return a
	}
	s.bufMu.Lock()
	adv, err := s.font.GlyphAdvance(&s.buf, id, fixed.Int26_6(s.upem<<6), font.HintingNone)
	s.bufMu.Unlock()
	if err != nil {
		adv = 0
	}
	a = float64(adv) / 64
	s.outMu.Lock()
	s.advs[id] = a
	s.outMu.Unlock()
	return a
}

func (s *source) kern(a, b sfnt.GlyphIndex) float64 {
	if !s.load() || !s.kerning {
		return 0
	}
	s.bufMu.Lock()
	k, err := s.font.Kern(&s.buf, a, b, fixed.Int26_6(s.upem<<6), font.HintingNone)
	s.bufMu.Unlock()
	if err != nil {
		return 0
	}
	return float64(k) / 64
}

func (s *source) outline(id sfnt.GlyphIndex) []rawSeg {
	if !s.load() {
		return nil
	}
	s.outMu.Lock()
	o, ok := s.outs[id]
	s.outMu.Unlock()
	if ok {
		return o
	}
	s.bufMu.Lock()
	segs, err := s.font.LoadGlyph(&s.buf, id, fixed.Int26_6(s.upem<<6), nil)
	if err == nil {
		o = make([]rawSeg, len(segs))
		for i, sg := range segs {
			r := rawSeg{}
			switch sg.Op {
			case sfnt.SegmentOpMoveTo:
				r.Op, r.n = MoveTo, 1
			case sfnt.SegmentOpLineTo:
				r.Op, r.n = LineTo, 1
			case sfnt.SegmentOpQuadTo:
				r.Op, r.n = QuadTo, 2
			case sfnt.SegmentOpCubeTo:
				r.Op, r.n = CubeTo, 3
			}
			for j := 0; j < r.n; j++ {
				r.pts[j] = [2]float64{float64(sg.Args[j].X) / 64, float64(sg.Args[j].Y) / 64}
			}
			o[i] = r
		}
	}
	s.bufMu.Unlock()
	s.outMu.Lock()
	s.outs[id] = o
	s.outMu.Unlock()
	return o
}

func (s *source) metrics(scale float64) Metrics {
	if !s.load() {
		sz := scale * 1000
		return Metrics{Ascent: sz * 0.8, Descent: sz * 0.2, LineHeight: sz * 1.15, XHeight: sz * 0.5, CapHeight: sz * 0.7, UnderlinePos: sz * 0.1, UnderlineThick: sz / 16}
	}
	u := func(v fixed.Int26_6) float64 { return float64(v) / 64 * scale }
	m := Metrics{}
	// Skia/FreeType on Linux use hhea unless the font asks for typo
	// metrics (fsSelection bit 7) or hhea is empty.
	asc, desc, gap := u(s.hhea.Ascent), u(s.hhea.Descent), u(s.hhea.Height)-u(s.hhea.Ascent)-u(s.hhea.Descent)
	if (s.os2.useTypo || asc+desc == 0) && s.os2.ok {
		asc = float64(s.os2.typoAsc) * scale
		desc = -float64(s.os2.typoDesc) * scale
		gap = float64(s.os2.typoGap) * scale
	}
	if asc+desc == 0 && s.os2.ok {
		asc, desc, gap = float64(s.os2.winAsc)*scale, float64(s.os2.winDesc)*scale, 0
	}
	if gap < 0 {
		gap = 0
	}
	m.Ascent, m.Descent, m.LineGap = asc, desc, gap
	// Chromium rounds ascent and descent separately before summing.
	m.LineHeight = math.Round(asc) + math.Round(desc) + math.Round(gap)
	m.XHeight = u(s.hhea.XHeight)
	m.CapHeight = u(s.hhea.CapHeight)
	if m.XHeight == 0 {
		m.XHeight = asc * 0.5
	}
	if m.CapHeight == 0 {
		m.CapHeight = asc * 0.7
	}
	if pt := s.font.PostTable(); pt != nil && pt.UnderlineThickness != 0 {
		m.UnderlinePos = -float64(pt.UnderlinePosition) * scale
		m.UnderlineThick = float64(pt.UnderlineThickness) * scale
	} else {
		m.UnderlinePos = (asc + desc) / 12
		m.UnderlineThick = (asc + desc) / 16
	}
	return m
}

// ---- matching

// genericAliases mirrors Chromium's Linux defaults: generic families resolve
// to the configured font prefs, which fontconfig then maps to metric
// compatible fonts (Arial -> Liberation Sans and so on).
var genericAliases = map[string][]string{
	"serif":           {"times new roman"},
	"sans-serif":      {"arial"},
	"monospace":       {"dejavu sans mono", "liberation mono", "courier new"},
	"cursive":         {"comic sans ms", "dejavu sans"},
	"fantasy":         {"impact", "dejavu sans"},
	"system-ui":       {"inter", "dejavu sans", "liberation sans"},
	"ui-sans-serif":   {"arial"},
	"ui-serif":        {"times new roman"},
	"ui-monospace":    {"dejavu sans mono"},
	"-apple-system":   {"inter", "dejavu sans", "liberation sans"},
	"blinkmacsystemfont": {"inter", "dejavu sans", "liberation sans"},
	"math":            {"dejavu serif"},
	"emoji":           {"noto color emoji"},
}

// metricAliases are fontconfig's metric-compatible substitutions.
var metricAliases = map[string][]string{
	"arial":           {"liberation sans", "arimo"},
	"helvetica":       {"liberation sans", "arimo"},
	"helvetica neue":  {"liberation sans", "arimo"},
	"times new roman": {"liberation serif", "tinos"},
	"times":           {"liberation serif", "tinos"},
	"courier new":     {"liberation mono", "cousine"},
	"courier":         {"liberation mono", "cousine"},
	"monospace":       {"dejavu sans mono"},
	"sans":            {"dejavu sans"},
	"segoe ui":        {"dejavu sans"},
	"roboto":          {"dejavu sans"},
	"verdana":         {"dejavu sans"},
	"georgia":         {"dejavu serif"},
}

// IsGeneric reports whether name is a CSS generic family keyword.
func IsGeneric(name string) bool {
	_, ok := genericAliases[strings.ToLower(name)]
	return ok
}

// IsMonospaceGeneric reports whether a family list resolves through the
// `monospace` generic first, which changes the default font size (13px).
func IsMonospaceGeneric(families []string) bool {
	return len(families) > 0 && strings.EqualFold(strings.TrimSpace(families[0]), "monospace")
}

func match(st Style) *source {
	cat := catalog()
	tried := map[string]bool{}
	var try func(name string, depth int) *source
	try = func(name string, depth int) *source {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" || tried[name] || depth > 3 {
			return nil
		}
		tried[name] = true
		if fam := cat.families[name]; len(fam) > 0 {
			return pickStyle(fam, st)
		}
		for _, a := range genericAliases[name] {
			if s := try(a, depth+1); s != nil {
				return s
			}
		}
		for _, a := range metricAliases[name] {
			if s := try(a, depth+1); s != nil {
				return s
			}
		}
		return nil
	}
	for _, f := range st.Families {
		if s := try(f, 0); s != nil {
			return s
		}
	}
	// The default font is Chromium's "standard" font, Times New Roman.
	for _, f := range []string{"serif", "liberation serif", "dejavu serif", "dejavu sans", "go"} {
		if s := try(f, 0); s != nil {
			return s
		}
	}
	return pickStyle(cat.families["go"], st)
}

// pickStyle implements the CSS font-style then font-weight matching steps
// within one family.
func pickStyle(fam []*source, st Style) *source {
	var cands []*source
	for _, s := range fam {
		if s.italic == st.Italic {
			cands = append(cands, s)
		}
	}
	if len(cands) == 0 {
		cands = fam
	}
	w := st.Weight
	best, bestD := cands[0], 1<<30
	for _, s := range cands {
		d := weightDistance(w, s.weight)
		if d < bestD {
			best, bestD = s, d
		}
	}
	return best
}

// weightDistance orders candidate weights per CSS Fonts §5.2 step 4.
func weightDistance(want, have int) int {
	if have == want {
		return 0
	}
	switch {
	case want >= 400 && want <= 500:
		if have >= want && have <= 500 {
			return have - want
		}
		if have < want {
			return 1000 + want - have
		}
		return 2000 + have - want
	case want < 400:
		if have <= want {
			return want - have
		}
		return 1000 + have - want
	default:
		if have >= want {
			return have - want
		}
		return 1000 + want - have
	}
}

var fallbackCache sync.Map // fallbackKey -> *source (nil stored as noSource)

type fallbackKey struct {
	r    rune
	bold bool
	ital bool
}

var noSource = &source{}

// fallbackFor finds a face for a rune the requested font lacks. Faces of
// the same broad class (sans/serif/mono) are preferred, then any face, then
// colour emoji fonts.
func fallbackFor(r rune, st Style) *source {
	k := fallbackKey{r, st.Weight >= 600, st.Italic}
	if v, ok := fallbackCache.Load(k); ok {
		if v.(*source) == noSource {
			return nil
		}
		return v.(*source)
	}
	cat := catalog()
	var found *source
	for _, name := range cat.fallbackOrder {
		fam := cat.families[name]
		s := pickStyle(fam, st)
		if s.color && !unicode.Is(unicode.So, r) {
			continue
		}
		if s.has(r) {
			found = s
			break
		}
	}
	if found == nil {
		fallbackCache.Store(k, noSource)
		return nil
	}
	fallbackCache.Store(k, found)
	return found
}
