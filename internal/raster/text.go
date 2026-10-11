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
	f *font.Face
	r rune
}

var (
	gpMu    sync.RWMutex
	gpCache = map[gkey]*Path{}
)

// glyphPath returns the unit-size (1 px em) outline of r.
func glyphPath(f *font.Face, r rune) *Path {
	k := gkey{f, r}
	gpMu.RLock()
	p, ok := gpCache[k]
	gpMu.RUnlock()
	if ok {
		return p
	}
	segs := f.Outline(r, refSize)
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
}

// Shaped is a laid-out run.
type Shaped struct {
	Glyphs []Glyph
	Width  float64
	Fake   bool // synthetic bold
	Skew   bool // synthetic italic
	Size   float64
	Set    *faceSet
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

// Shape lays text out with the font spec.
func Shape(spec FontSpec, text string, letterSpacing, wordSpacing float64, kern bool) *Shaped {
	fs := resolveFaces(spec.Families, spec.Weight, spec.Italic)
	sh := &Shaped{Size: spec.Size, Set: fs}
	if len(fs.faces) > 0 {
		f0 := fs.faces[0]
		sh.Fake = spec.Weight >= 600 && f0.Style.Weight < 600
		sh.Skew = spec.Italic && !f0.Style.Italic
	}
	var x float64
	var prev rune
	var prevFace *font.Face
	for _, r := range text {
		if spec.SmallCaps && unicode.IsLower(r) {
			r = unicode.ToUpper(r)
		}
		f := fs.faceFor(r)
		if f == nil {
			continue
		}
		if kern && prevFace == f {
			x += f.Kern(prev, r, spec.Size)
		}
		adv := f.Advance(r, spec.Size)
		if adv < 0 {
			adv = spec.Size * 0.5
		}
		sh.Glyphs = append(sh.Glyphs, Glyph{R: r, X: x, Face: f})
		x += adv + letterSpacing
		if r == ' ' {
			x += wordSpacing
		}
		prev, prevFace = r, f
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
	sh := Shape(t.font, normalizeText(text), t.letterSpacing, t.wordSpacing, t.kerning != "none")
	asc, desc := sh.FontMetrics()
	var m TextMetrics
	m.Width = sh.Width
	m.FontAscent, m.FontDescent = asc, desc
	m.EmAscent, m.EmDescent = asc, desc
	dx, dy := c.alignOffset(sh, t)
	x0, x1 := math.Inf(1), math.Inf(-1)
	y0, y1 := math.Inf(1), math.Inf(-1)
	for _, g := range sh.Glyphs {
		p := glyphPath(g.Face, g.R)
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
	m.ActualLeft = -(x0 + dx)
	m.ActualRight = x1 + dx
	m.ActualAscent = -(y0 + dy)
	m.ActualDescent = y1 + dy
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
	var dy float64
	switch t.baseline {
	case "top":
		dy = asc
	case "hanging":
		dy = asc * 0.8
	case "middle":
		dy = (asc - desc) / 2
	case "ideographic", "bottom":
		dy = -desc
	}
	return dx, dy
}

// ---- drawing ---------------------------------------------------------------------------

type gmKey struct {
	f    *font.Face
	r    rune
	size int32
	sub  uint8
	bold bool
	skew bool
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

func glyphMask(g Glyph, size float64, sub int, bold, skew bool) *Mask {
	k := gmKey{g.Face, g.R, int32(size*64 + 0.5), uint8(sub), bold, skew}
	gmMu.Lock()
	if m, ok := gmCache[k]; ok {
		gmMu.Unlock()
		return m
	}
	gmMu.Unlock()
	p := glyphPath(g.Face, g.R)
	var m *Mask
	if p != nil {
		sk := 0.0
		if skew {
			sk = -0.25
		}
		mat := Matrix{size, 0, sk * size, size, float64(sub) / 4, 0}
		big := IRect{-2048, -2048, 2048, 2048}
		m = FillMask(p, mat, NonZero, big)
		if bold && m != nil {
			ex := fakeBoldExtra(size)
			sp := p.Transform(mat)
			sm := StrokeMask(sp, Identity, StrokeStyle{Width: ex, Join: JoinMiter, MiterLimit: 4}, big)
			m = unionMasks(m, sm)
		}
		if m != nil {
			m = applyTextGamma(m)
		}
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

// textGamma boosts coverage slightly the way Skia's contrast/gamma tables do.
var textLUT [256]uint8

func init() {
	for i := range textLUT {
		textLUT[i] = uint8(i)
	}
}

func applyTextGamma(m *Mask) *Mask { return m }

// textMaskFor assembles a coverage mask for a shaped run placed at (x,y) in
// user space. Returns nil if nothing visible.
func (c *Context) textMask(sh *Shaped, x, y, sx float64) *Mask {
	m := c.st.ctm
	simple := m[1] == 0 && m[2] == 0 && m[0] > 0 && m[3] > 0 && math.Abs(m[0]-m[3]) < 1e-9
	clip := c.drawBounds()
	if simple {
		scale := m[0]
		dsize := sh.Size * scale
		if dsize > 400 {
			simple = false
		}
		var out *Mask
		type placed struct {
			m      *Mask
			ox, oy int
		}
		var ps []placed
		bb := IRect{}
		for _, g := range sh.Glyphs {
			gx := (x+g.X*sx)*scale + m[4]
			gy := math.Round(y*scale + m[5])
			ix := math.Floor(gx)
			sub := int(math.Floor((gx - ix) * 4))
			gm := glyphMask(g, dsize, sub, sh.Fake, sh.Skew)
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
	return FillMask(p, m, NonZero, clip)
}

// textPath builds the combined outline of a run (user space).
func (c *Context) textPath(sh *Shaped, x, y, sx float64) *Path {
	p := NewPath()
	for _, g := range sh.Glyphs {
		gp := glyphPath(g.Face, g.R)
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
	sh := Shape(t.font, normalizeText(text), t.letterSpacing, t.wordSpacing, t.kerning != "none")
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
	c.StrokePath(p)
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
