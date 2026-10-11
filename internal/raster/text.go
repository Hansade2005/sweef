package raster

import (
	"math"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/sweefbrowser/sweef/internal/font"
)

// FontSpec is a parsed CSS font.
type FontSpec struct {
	Families  []string
	Size      float64
	Weight    int
	Italic    bool
	SmallCaps bool
	Stretch   string
}

type textState struct {
	font          FontSpec
	fontStr       string
	align         string
	baseline      string
	direction     string
	letterSpacing float64
	wordSpacing   float64
	kerning       string
	baseSize      float64 // font-size used to resolve em/% in font shorthand
}

func defaultTextState() textState {
	return textState{
		font:      FontSpec{Families: []string{"sans-serif"}, Size: 10, Weight: 400},
		fontStr:   "10px sans-serif",
		align:     "start",
		baseline:  "alphabetic",
		direction: "ltr",
		kerning:   "auto",
		baseSize:  16,
	}
}

var sizeKeywords = map[string]float64{"xx-small": 9, "x-small": 10, "small": 13, "medium": 16, "large": 18, "x-large": 24, "xx-large": 32, "xxx-large": 48}

// ParseFont parses a CSS font shorthand as accepted by canvas (system fonts
// and "inherit"-style globals are rejected). baseSize resolves em and %.
func ParseFont(s string, baseSize float64) (FontSpec, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return FontSpec{}, false
	}
	spec := FontSpec{Weight: 400}
	toks := splitFontTokens(s)
	i := 0
	for i < len(toks) {
		t := strings.ToLower(toks[i])
		switch {
		case t == "normal":
		case t == "italic" || t == "oblique":
			spec.Italic = true
			if i+1 < len(toks) && strings.HasSuffix(strings.ToLower(toks[i+1]), "deg") {
				i++
			}
		case t == "small-caps":
			spec.SmallCaps = true
		case t == "bold":
			spec.Weight = 700
		case t == "bolder":
			spec.Weight = 700
		case t == "lighter":
			spec.Weight = 300
		case isStretch(t):
			spec.Stretch = t
		default:
			if n, err := strconv.Atoi(t); err == nil && n >= 1 && n <= 1000 && !strings.ContainsAny(t, ".") {
				// a bare number before the size is a weight
				if i+1 < len(toks) {
					spec.Weight = n
					i++
					continue
				}
			}
			goto size
		}
		i++
	}
	return FontSpec{}, false
size:
	if i >= len(toks) {
		return FontSpec{}, false
	}
	{
		sz := strings.ToLower(toks[i])
		if j := strings.IndexByte(sz, '/'); j >= 0 {
			sz = sz[:j]
		}
		v, ok := parseFontSize(sz, baseSize)
		if !ok {
			return FontSpec{}, false
		}
		spec.Size = v
		i++
		if i < len(toks) && toks[i] == "/" {
			i += 2
		} else if i < len(toks) && strings.HasPrefix(toks[i], "/") {
			i++
		}
	}
	if i >= len(toks) {
		return FontSpec{}, false
	}
	fam := strings.Join(toks[i:], " ")
	for _, f := range strings.Split(fam, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			return FontSpec{}, false
		}
		if (f[0] == '"' || f[0] == '\'') && len(f) >= 2 && f[len(f)-1] == f[0] {
			f = f[1 : len(f)-1]
		}
		spec.Families = append(spec.Families, f)
	}
	return spec, true
}

func isStretch(t string) bool {
	switch t {
	case "ultra-condensed", "extra-condensed", "condensed", "semi-condensed", "semi-expanded", "expanded", "extra-expanded", "ultra-expanded":
		return true
	}
	return false
}

func splitFontTokens(s string) []string {
	var out []string
	var cur strings.Builder
	var q byte
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case q != 0:
			cur.WriteByte(ch)
			if ch == q {
				q = 0
			}
		case ch == '"' || ch == '\'':
			q = ch
			cur.WriteByte(ch)
		case ch == ' ' || ch == '\t' || ch == '\n':
			flush()
		case ch == ',':
			flush()
			out = append(out, ",")
		default:
			cur.WriteByte(ch)
		}
	}
	flush()
	// glue commas back to neighbours so family joins work
	var res []string
	for _, t := range out {
		if t == "," && len(res) > 0 {
			res[len(res)-1] += ","
		} else if len(res) > 0 && strings.HasSuffix(res[len(res)-1], ",") {
			res[len(res)-1] += " " + t
		} else {
			res = append(res, t)
		}
	}
	return res
}

func parseFontSize(s string, base float64) (float64, bool) {
	if v, ok := sizeKeywords[s]; ok {
		return v, true
	}
	switch s {
	case "larger":
		return base * 1.2, true
	case "smaller":
		return base / 1.2, true
	}
	units := []struct {
		suf string
		mul float64
	}{{"px", 1}, {"pt", 4.0 / 3}, {"pc", 16}, {"in", 96}, {"cm", 96 / 2.54}, {"mm", 96 / 25.4}, {"rem", 16}, {"em", base}, {"ex", base / 2}, {"%", base / 100}, {"q", 96 / 101.6}}
	for _, u := range units {
		if strings.HasSuffix(s, u.suf) {
			v, err := strconv.ParseFloat(s[:len(s)-len(u.suf)], 64)
			if err != nil || v < 0 {
				return 0, false
			}
			return v * u.mul, true
		}
	}
	return 0, false
}

// ---- face resolution ------------------------------------------------------------

type faceSet struct {
	faces []*font.Face
	fams  []string
	st    font.Style
}

var (
	fsMu    sync.Mutex
	fsCache = map[string]*faceSet{}
)

func resolveFaces(fams []string, weight int, italic bool) *faceSet {
	key := strings.Join(fams, "\x00") + "|" + strconv.Itoa(weight) + "|" + strconv.FormatBool(italic)
	fsMu.Lock()
	defer fsMu.Unlock()
	if fs, ok := fsCache[key]; ok {
		return fs
	}
	st := font.Style{Weight: weight, Italic: italic}
	fs := &faceSet{fams: fams, st: st}
	for _, f := range fams {
		if face := font.Lookup(f, st); face != nil {
			fs.faces = append(fs.faces, face)
		}
	}
	if len(fs.faces) == 0 {
		if f := font.Resolve(fams, st); f != nil {
			fs.faces = append(fs.faces, f)
		}
	}
	if len(fsCache) > 512 {
		fsCache = map[string]*faceSet{}
	}
	fsCache[key] = fs
	return fs
}

func (fs *faceSet) faceFor(r rune) *font.Face {
	for _, f := range fs.faces {
		if f.HasGlyph(r) {
			return f
		}
	}
	if f := font.Fallback(r, fs.fams, fs.st); f != nil {
		return f
	}
	if len(fs.faces) > 0 {
		return fs.faces[0]
	}
	return nil
}

// ---- glyph outlines ---------------------------------------------------------------

const refSize = 1024.0

type gkey struct {
	f   *font.Face
	r   rune
	gid int // glyph id for ligatures, 0 to look up r
}

var (
	gpMu    sync.RWMutex
	gpCache = map[gkey]*Path{}
)

// glyphPath returns the unit-size (1 px em) outline of a glyph.
func glyphPath(g Glyph) *Path {
	f, r := g.Face, g.R
	k := gkey{f, r, g.GID}
	gpMu.RLock()
	p, ok := gpCache[k]
	gpMu.RUnlock()
	if ok {
		return p
	}
	var segs []font.Segment
	if g.GID != 0 {
		segs = f.OutlineGID(g.GID, refSize)
	} else {
		segs = f.Outline(r, refSize)
	}
	if segs != nil {
		p = NewPath()
		const s = 1 / refSize
		for _, sg := range segs {
			switch sg.Op {
			case 'M':
				if !p.Empty() {
					p.Close()
				}
				p.MoveTo(sg.X0*s, sg.Y0*s)
			case 'L':
				p.LineTo(sg.X0*s, sg.Y0*s)
			case 'Q':
				p.QuadTo(sg.X0*s, sg.Y0*s, sg.X1*s, sg.Y1*s)
			case 'C':
				p.CubicTo(sg.X0*s, sg.Y0*s, sg.X1*s, sg.Y1*s, sg.X2*s, sg.Y2*s)
			}
		}
		p.Close()
	}
	gpMu.Lock()
	gpCache[k] = p
	gpMu.Unlock()
	return p
}

// ---- shaping ------------------------------------------------------------------------

// Glyph is a positioned character.
type Glyph struct {
	R    rune
	X    float64 // pen offset from run origin
	Face *font.Face
	GID  int // glyph id when it is not simply the glyph of R (ligatures)
}

// Shaped is a laid-out run.
type Shaped struct {
	Glyphs []Glyph
	Width  float64
	Fake   bool // synthetic bold
	Skew   bool // synthetic italic
	Size   float64
	Set    *faceSet
	Lum    uint8 // text color luminance, selects the coverage preblend table
}

func normalizeText(s string) string {
	if !strings.ContainsAny(s, "\t\n\r\f\v") {
		return s
	}
	return strings.Map(func(r rune) rune {
		switch r {
		case '\t', '\n', '\r', '\f', '\v':
			return ' '
		}
		return r
	}, s)
}

// Shape lays text out with the font spec: pair kerning (when kern) and
// standard ligatures (when letterSpacing is zero, as in browsers) from the
// font's OpenType tables, shaped word by word like canvas text in Chromium.
func Shape(spec FontSpec, text string, letterSpacing, wordSpacing float64, kern bool) *Shaped {
	return ShapeDir(spec, text, letterSpacing, wordSpacing, kern, false)
}

var bracketMirror = map[rune]rune{'(': ')', ')': '(', '[': ']', ']': '[', '{': '}', '}': '{', '<': '>', '>': '<', '«': '»', '»': '«', '‹': '›', '›': '‹'}

// ShapeDir is Shape for a paragraph whose base direction is rtl when set.
// Text containing right-to-left characters is reordered with the Unicode
// bidirectional algorithm and Arabic letters take their joined forms; the
// glyphs of the result are in visual order.
func ShapeDir(spec FontSpec, text string, letterSpacing, wordSpacing float64, kern bool, rtl bool) *Shaped {
	fs := resolveFaces(spec.Families, spec.Weight, spec.Italic)
	sh := &Shaped{Size: spec.Size, Set: fs}
	if len(fs.faces) > 0 {
		f0 := fs.faces[0]
		sh.Fake = spec.Weight >= 600 && f0.Style.Weight < 600
		sh.Skew = spec.Italic && !f0.Style.Italic
	}
	rs := []rune(text)
	if spec.SmallCaps {
		for i, r := range rs {
			if unicode.IsLower(r) {
				rs[i] = unicode.ToUpper(r)
			}
		}
	}
	liga := letterSpacing == 0
	needBidi := rtl
	for _, r := range rs {
		if font.IsRTLRune(r) {
			needBidi = true
			break
		}
	}
	if needBidi {
		var vis []rune
		for _, run := range font.BidiRuns(rs, rtl) {
			seg := rs[run.Start:run.End]
			if !run.RTL {
				vis = append(vis, seg...)
				continue
			}
			seg = font.ShapeArabic(seg)
			for k := len(seg) - 1; k >= 0; k-- {
				r := seg[k]
				if m, ok := bracketMirror[r]; ok {
					r = m
				}
				vis = append(vis, r)
			}
			liga = false
		}
		rs = vis
	}
	opts := font.ShapeOptions{Kern: kern, Liga: liga}
	var x float64
	for i := 0; i < len(rs); {
		f := fs.faceFor(rs[i])
		if f == nil {
			i++
			continue
		}
		// a run is one word in one face; a space is a run of its own
		j := i + 1
		if rs[i] != ' ' {
			for j < len(rs) && rs[j] != ' ' && fs.faceFor(rs[j]) == f {
				j++
			}
		}
		if !f.HasGlyph(rs[i]) {
			// no glyph anywhere: advance by half an em like browsers' notdef box
			sh.Glyphs = append(sh.Glyphs, Glyph{R: rs[i], X: x, Face: f})
			x += spec.Size*0.5 + letterSpacing
			i++
			continue
		}
		run := f.ShapeRun(rs[i:j], spec.Size, opts)
		for _, g := range run {
			gl := Glyph{R: rs[i+g.Cluster], X: x + g.X + letterSpacing*float64(g.Cluster), Face: f}
			if g.NRunes > 1 {
				gl.GID = g.GID
			}
			sh.Glyphs = append(sh.Glyphs, gl)
		}
		end := run[len(run)-1]
		x += end.X + end.Adv + letterSpacing*float64(j-i)
		if rs[i] == ' ' {
			x += wordSpacing
		}
		i = j
	}
	sh.Width = x
	return sh
}

// FontMetrics returns the integer-rounded ascent/descent browsers report.
func (s *Shaped) FontMetrics() (asc, desc float64) {
	if len(s.Set.faces) == 0 {
		return 0, 0
	}
	m := s.Set.faces[0].Metrics(s.Size)
	return math.Round(m.Ascent), math.Round(m.Descent)
}

// TextMetrics mirrors the canvas TextMetrics members.
type TextMetrics struct {
	Width                          float64
	ActualLeft, ActualRight        float64
	ActualAscent, ActualDescent    float64
	FontAscent, FontDescent        float64
	EmAscent, EmDescent            float64
	Hanging, Alphabetic, Ideograph float64
}

// MeasureText measures text under the context's text state.
func (c *Context) MeasureText(text string) TextMetrics {
	t := &c.st.text
	sh := ShapeDir(t.font, normalizeText(text), t.letterSpacing, t.wordSpacing, t.kerning != "none", t.direction == "rtl")
	asc, desc := sh.FontMetrics()
	var m TextMetrics
	m.Width = sh.Width
	m.FontAscent, m.FontDescent = asc, desc
	m.EmAscent, m.EmDescent = asc, desc
	dx, dy := c.alignOffset(sh, t)
	x0, x1 := math.Inf(1), math.Inf(-1)
	y0, y1 := math.Inf(1), math.Inf(-1)
	for _, g := range sh.Glyphs {
		p := glyphPath(g)
		if p == nil {
			continue
		}
		b, ok := p.Bounds()
		if !ok {
			continue
		}
		gx := g.X
		x0 = math.Min(x0, gx+b.X*sh.Size)
		x1 = math.Max(x1, gx+(b.X+b.W)*sh.Size)
		y0 = math.Min(y0, b.Y*sh.Size)
		y1 = math.Max(y1, (b.Y+b.H)*sh.Size)
	}
	if math.IsInf(x0, 1) {
		x0, x1, y0, y1 = 0, 0, 0, 0
	}
	// ActualLeft is distance from the alignment point to the left of the box.
	m.ActualLeft = -math.Floor(x0 + dx)
	m.ActualRight = x1 + dx
	m.ActualAscent = math.Ceil(-(y0 + dy))
	m.ActualDescent = math.Ceil(y1 + dy)
	m.Hanging = asc*0.8 - dy
	m.Alphabetic = -dy
	m.Ideograph = -desc - dy
	m.FontAscent, m.FontDescent = asc-dy, desc+dy
	m.EmAscent, m.EmDescent = asc-dy, desc+dy
	return m
}

// alignOffset returns the shift (dx, dy) from the user's (x,y) to the run
// origin on the baseline.
func (c *Context) alignOffset(sh *Shaped, t *textState) (float64, float64) {
	var dx float64
	rtl := t.direction == "rtl"
	switch t.align {
	case "center":
		dx = -sh.Width / 2
	case "right":
		dx = -sh.Width
	case "end":
		if !rtl {
			dx = -sh.Width
		}
	case "start":
		if rtl {
			dx = -sh.Width
		}
	}
	asc, desc := sh.FontMetrics()
	// Blink derives the em box baselines from the OS/2 typographic metrics
	// (unrounded) when the font has them
	tasc, tdesc := asc, desc
	if len(sh.Set.faces) > 0 {
		if a, d, ok := sh.Set.faces[0].TypoMetrics(sh.Size); ok {
			tasc, tdesc = a, d
		}
	}
	var dy float64
	switch t.baseline {
	case "top":
		dy = tasc
	case "hanging":
		dy = asc * 0.8
	case "middle":
		dy = (tasc - tdesc) / 2
	case "bottom":
		dy = -tdesc
	case "ideographic":
		dy = -desc
	}
	return dx, dy
}

// ---- drawing ---------------------------------------------------------------------------

type gmKey struct {
	f      *font.Face
	r      rune
	gid    int
	sx, sy int32 // FreeType's 26.6 pixel sizes along x and y
	sub    uint8
	bold   bool
	skew   bool
}

var (
	gmMu    sync.Mutex
	gmCache = map[gmKey]*Mask{}
)

func fakeBoldExtra(size float64) float64 {
	switch {
	case size <= 9:
		return size / 24
	case size >= 36:
		return size / 32
	}
	return size * (1/24 + (size-9)/(36-9)*(1.0/32-1.0/24))
}

func glyphMask(g Glyph, sizeX, sizeY float64, sub int, bold, skew bool) *Mask {
	k := gmKey{g.Face, g.R, g.GID, int32(sizeX * 64), int32(sizeY * 64), uint8(sub), bold, skew}
	gmMu.Lock()
	if m, ok := gmCache[k]; ok {
		gmMu.Unlock()
		return m
	}
	gmMu.Unlock()
	var m *Mask
	if !bold {
		m = skiaGlyph(g, sizeX, sizeY, sub, skew)
	} else {
		m = skiaGlyphBold(g, sizeX, sizeY, sub, skew)
	}
	gmMu.Lock()
	if len(gmCache) > 20000 {
		gmCache = map[gmKey]*Mask{}
	}
	gmCache[k] = m
	gmMu.Unlock()
	return m
}

func unionMasks(a, b *Mask) *Mask {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	r := a.B.Union(b.B)
	o := newMask(r)
	for _, m := range []*Mask{a, b} {
		for y := m.B.Y0; y < m.B.Y1; y++ {
			src := m.Row(y)
			dst := o.Row(y)[m.B.X0-r.X0:]
			for i, v := range src {
				s := uint32(dst[i]) + uint32(v)
				if s > 255 {
					s = 255
				}
				dst[i] = uint8(s)
			}
		}
	}
	return o
}

// textMaskFor assembles a coverage mask for a shaped run placed at (x,y) in
// user space. Returns nil if nothing visible.
func (c *Context) textMask(sh *Shaped, x, y, sx float64) *Mask {
	m := c.st.ctm
	dsizeX, dsizeY := sh.Size*m[0]*sx, sh.Size*m[3]
	// glyphs up to 256px are cached masks placed on the quarter-pixel grid;
	// bigger ones, and rotated or skewed text, are drawn as paths
	simple := m[1] == 0 && m[2] == 0 && m[0] > 0 && m[3] > 0 && sx > 0 && dsizeX <= 256 && dsizeY <= 256
	clip := c.drawBounds()
	if simple {
		scale := m[0]
		var out *Mask
		type placed struct {
			m      *Mask
			ox, oy int
		}
		var ps []placed
		bb := IRect{}
		for _, g := range sh.Glyphs {
			gx := (x+g.X*sx)*scale + m[4]
			gy := math.Round(y*m[3] + m[5])
			gx += 0.125 // Skia rounds to the nearest quarter pixel
			ix := math.Floor(gx)
			sub := int(math.Floor((gx - ix) * 4))
			gm := glyphMask(g, dsizeX, dsizeY, sub, sh.Fake, sh.Skew)
			if gm == nil {
				continue
			}
			ox, oy := int(ix), int(gy)
			r := IRect{gm.B.X0 + ox, gm.B.Y0 + oy, gm.B.X1 + ox, gm.B.Y1 + oy}
			if r.Intersect(clip).Empty() {
				continue
			}
			ps = append(ps, placed{gm, ox, oy})
			bb = bb.Union(r)
		}
		if len(ps) == 0 {
			return nil
		}
		bb = bb.Intersect(clip)
		if bb.Empty() {
			return nil
		}
		out = newMask(bb)
		lut := lutForLum(sh.Lum)
		for _, p := range ps {
			for yy := max(p.m.B.Y0+p.oy, bb.Y0); yy < min(p.m.B.Y1+p.oy, bb.Y1); yy++ {
				src := p.m.Row(yy - p.oy)
				x0 := max(p.m.B.X0+p.ox, bb.X0)
				x1 := min(p.m.B.X1+p.ox, bb.X1)
				if x1 <= x0 {
					continue
				}
				srow := src[x0-p.ox-p.m.B.X0 : x1-p.ox-p.m.B.X0]
				drow := out.Row(yy)[x0-bb.X0 : x1-bb.X0]
				for i, v := range srow {
					if v == 0 {
						continue
					}
					v = lut[v]
					s := uint32(drow[i]) + uint32(v)
					if s > 255 {
						s = 255
					}
					drow[i] = uint8(s)
				}
			}
		}
		return out
	}
	// general transform: one big path
	p := c.textPath(sh, x, y, sx)
	if p == nil {
		return nil
	}
	// Chromium fills glyph outlines with its analytic AA scan converter even
	// when the text is rotated or scaled non-uniformly
	return fxFillPath(p.Transform(m), clip)
}

// textPath builds the combined outline of a run (user space).
func (c *Context) textPath(sh *Shaped, x, y, sx float64) *Path {
	p := NewPath()
	for _, g := range sh.Glyphs {
		gp := glyphPath(g)
		if gp == nil {
			continue
		}
		sk := 0.0
		if sh.Skew {
			sk = -0.25
		}
		p.AddPath(gp, Matrix{sh.Size * sx, 0, sk * sh.Size * sx, sh.Size, x + g.X*sx, y})
	}
	if p.Empty() {
		return nil
	}
	return p
}

func (c *Context) prepText(text string, x, y, maxWidth float64) (*Shaped, float64, float64, float64) {
	t := &c.st.text
	sh := ShapeDir(t.font, normalizeText(text), t.letterSpacing, t.wordSpacing, t.kerning != "none", t.direction == "rtl")
	sx := 1.0
	if maxWidth > 0 && sh.Width > maxWidth {
		sx = maxWidth / sh.Width
	}
	w := sh.Width
	sh.Width = w * sx
	dx, dy := c.alignOffset(sh, t)
	sh.Width = w
	return sh, x + dx, y + dy, sx
}

// FillText draws filled text.
func (c *Context) FillText(text string, x, y, maxWidth float64) {
	if maxWidth < 0 || math.IsNaN(maxWidth) {
		return
	}
	sh, ox, oy, sx := c.prepText(text, x, y, maxWidth)
	if len(sh.Glyphs) == 0 {
		return
	}
	if f := c.st.fill; f.Gradient == nil && f.Pattern == nil {
		sh.Lum = textLum(f.Color)
	}
	c.draw(c.textMask(sh, ox, oy, sx), c.shaderFor(c.st.fill, c.st.alpha))
}

// StrokeText draws text outlines.
func (c *Context) StrokeText(text string, x, y, maxWidth float64) {
	if maxWidth < 0 || math.IsNaN(maxWidth) {
		return
	}
	sh, ox, oy, sx := c.prepText(text, x, y, maxWidth)
	p := c.textPath(sh, ox, oy, sx)
	if p == nil {
		return
	}
	c.draw(c.strokeTextMask(p), c.shaderFor(c.st.stroke, c.st.alpha))
}

// strokeTextMask strokes a glyph path the way StrokeMask does but fills the
// outline with the snapped edges Chromium's scan converter uses.
func (c *Context) strokeTextMask(p *Path) *Mask {
	m := c.st.ctm
	sc := m.MaxScale()
	if sc == 0 || math.IsNaN(sc) {
		return nil
	}
	if ss := c.st.ss; len(ss.Dash) == 0 {
		// Skia's stroker, then the analytic scan converter on the device outline
		return fxFillPath(fxStrokePath(p, ss.Width, ss.Join, ss.Cap, ss.MiterLimit, sc).Transform(m), c.drawBounds())
	}
	tol := 0.05 / sc
	polys := StrokePolys(p.Flatten(tol), c.st.ss, tol)
	for i := range polys {
		for j, q := range polys[i].P {
			polys[i].P[j] = aaaPt(m.ApplyPt(q).X, m.ApplyPt(q).Y)
		}
	}
	return RasterizePolys(polys, NonZero, c.drawBounds())
}

// SetFont sets the font from a CSS shorthand; false if invalid.
func (c *Context) SetFont(s string) bool {
	spec, ok := ParseFont(s, c.st.text.baseSize)
	if !ok {
		return false
	}
	c.st.text.font, c.st.text.fontStr = spec, s
	return true
}

// Font returns the current font spec.
func (c *Context) Font() FontSpec { return c.st.text.font }

// SetTextOptions sets the remaining text attributes.
func (c *Context) SetTextAlign(a string)      { c.st.text.align = a }
func (c *Context) SetTextBaseline(b string)   { c.st.text.baseline = b }
func (c *Context) SetDirection(d string)      { c.st.text.direction = d }
func (c *Context) SetLetterSpacing(v float64) { c.st.text.letterSpacing = v }
func (c *Context) SetWordSpacing(v float64)   { c.st.text.wordSpacing = v }
func (c *Context) SetFontKerning(k string)    { c.st.text.kerning = k }
func (c *Context) SetBaseFontSize(v float64)  { c.st.text.baseSize = v }

// String serialises the font like browsers do for ctx.font.
func (f FontSpec) String() string {
	var parts []string
	if f.Italic {
		parts = append(parts, "italic")
	}
	if f.SmallCaps {
		parts = append(parts, "small-caps")
	}
	switch {
	case f.Weight == 700:
		parts = append(parts, "bold")
	case f.Weight != 400 && f.Weight != 0:
		parts = append(parts, strconv.Itoa(f.Weight))
	}
	if f.Stretch != "" {
		parts = append(parts, f.Stretch)
	}
	parts = append(parts, strconv.FormatFloat(f.Size, 'f', -1, 64)+"px")
	fam := make([]string, len(f.Families))
	for i, n := range f.Families {
		switch n {
		case "serif", "sans-serif", "monospace", "cursive", "fantasy", "system-ui", "ui-serif", "ui-sans-serif", "ui-monospace", "math", "emoji":
			fam[i] = n
		default:
			if strings.ContainsAny(n, " \t\"',") || n == "" {
				fam[i] = strconv.Quote(n)
			} else {
				fam[i] = n
			}
		}
	}
	parts = append(parts, strings.Join(fam, ", "))
	return strings.Join(parts, " ")
}
