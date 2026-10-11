package render

import (
	"math"
	"strconv"
	"strings"
	"unicode"

	"github.com/sweefbrowser/sweef/internal/font"
	"github.com/sweefbrowser/sweef/internal/style"
)

// fontSpec is a resolved font for a style: the face chain and the metrics
// Chromium derives from the primary face.
type fontSpec struct {
	faces     []*font.Face
	primary   *font.Face
	size      float64
	fam       []string
	st        font.Style
	asc       float64 // rounded ascent
	desc      float64 // rounded descent
	gap       float64 // rounded line gap
	lineSp    float64 // asc+desc+gap
	xh        float64
	capH      float64
	spaceW    float64
	fallback  map[rune]*font.Face
	rawAsc    float64
	synthBold bool
}

func round(v float64) float64 { return math.Floor(v + 0.5) }

func floor64(v float64) float64 { return math.Floor(v*64) / 64 }

func (c *ctx) spec(st *style.Computed) *fontSpec {
	if fs, ok := c.specPtr[st]; ok {
		return fs
	}
	key := strings.Join(st.FontFamily, ",") + "|" + strconv.FormatFloat(st.FontSize, 'f', 4, 64) + "|" + strconv.Itoa(st.FontWeight) + "|" + strconv.Itoa(int(st.FontStyle))
	if fs, ok := c.specs[key]; ok {
		c.specPtr[st] = fs
		return fs
	}
	fst := font.Style{Weight: st.FontWeight, Italic: st.FontStyle != 0}
	fs := &fontSpec{size: st.FontSize, fam: st.FontFamily, st: fst, fallback: map[rune]*font.Face{}}
	for _, f := range st.FontFamily {
		if face := font.Lookup(f, fst); face != nil {
			fs.faces = append(fs.faces, face)
		}
	}
	if len(fs.faces) == 0 {
		if f := font.Resolve(st.FontFamily, fst); f != nil {
			fs.faces = append(fs.faces, f)
		}
	}
	if len(fs.faces) > 0 {
		fs.primary = fs.faces[0]
		m := fs.primary.Metrics(st.FontSize)
		fs.asc, fs.desc, fs.gap = round(m.Ascent), round(m.Descent), round(m.LineGap)
		fs.rawAsc = m.Ascent
		fs.xh, fs.capH = m.XHeight, m.CapHeight
		fs.lineSp = fs.asc + fs.desc + fs.gap
		fs.spaceW = fs.primary.AdvanceF(' ', st.FontSize)
		if fs.spaceW < 0 {
			fs.spaceW = st.FontSize / 4
		}
		if st.FontWeight >= 600 && fs.primary.Style.Weight < 600 {
			fs.synthBold = true
		}
	} else {
		fs.asc, fs.desc = round(st.FontSize*0.9), round(st.FontSize*0.2)
		fs.lineSp = fs.asc + fs.desc
		fs.spaceW = st.FontSize / 4
	}
	c.specs[key] = fs
	c.specPtr[st] = fs
	return fs
}

func (fs *fontSpec) faceFor(r rune) *font.Face {
	for _, f := range fs.faces {
		if f.HasGlyph(r) {
			return f
		}
	}
	if f, ok := fs.fallback[r]; ok {
		return f
	}
	var f *font.Face
	if r > 0x20 {
		f = font.Fallback(r, fs.fam, fs.st)
	}
	if f == nil {
		f = fs.primary
	}
	fs.fallback[r] = f
	return f
}

func isZeroWidth(r rune) bool {
	switch {
	case r == 0x200b || r == 0x200c || r == 0x200d || r == 0xfeff || r == 0xad || r == 0x2060:
		return true
	case r >= 0xfe00 && r <= 0xfe0f:
		return true
	case r >= 0x0300 && r <= 0x036f:
		return true
	case r < 0x20 && r != '\t':
		return true
	}
	return false
}

type shaped struct {
	r     []rune
	faces []*font.Face
	x     []float64 // cumulative advance; x[i] = start of rune i; len = n+1
}

// shape computes glyph positions for runes in the given style.
func (c *ctx) shape(fs *fontSpec, st *style.Computed, rs []rune) *shaped {
	sh := &shaped{r: rs, faces: make([]*font.Face, len(rs)), x: make([]float64, len(rs)+1)}
	kern := st.LetterSpacing == 0 && st.FontKerning != "none" && st.TextRendering != "optimizespeed"
	var prev rune
	var prevFace *font.Face
	x := 0.0
	for i, r := range rs {
		f := fs.faceFor(r)
		sh.faces[i] = f
		adv := 0.0
		switch {
		case r == '\t':
			adv = fs.spaceW
		case isZeroWidth(r) && r != 0xad:
			adv = 0
		case r == 0xad:
			adv = 0
		case f != nil:
			a := f.AdvanceF(r, fs.size)
			if a < 0 {
				a = fs.size * 0.5
			}
			adv = a
			if kern && prev != 0 && prevFace == f {
				adv += f.KernF(prev, r, fs.size)
			}
		default:
			adv = fs.size * 0.5
		}
		// kerning pairs are attributed to the second glyph's advance, which
		// matches shaping the pair together
		if r == ' ' || r == 0xa0 {
			adv += st.WordSpacing
		}
		adv += st.LetterSpacing
		x += adv
		sh.x[i+1] = x
		prev, prevFace = r, f
	}
	return sh
}

// transformText applies text-transform; prev is the preceding rune (for capitalize).
func transformText(s string, tt string, prev rune) string {
	switch tt {
	case "uppercase":
		return strings.ToUpper(s)
	case "lowercase":
		return strings.ToLower(s)
	case "capitalize":
		var b strings.Builder
		p := prev
		for _, r := range s {
			if (p == 0 || unicode.IsSpace(p) || p == '-' && false || isPunctStart(p)) && unicode.IsLetter(r) {
				b.WriteRune(unicode.ToTitle(r))
			} else {
				b.WriteRune(r)
			}
			p = r
		}
		return b.String()
	case "full-width":
		return s
	}
	return s
}

func isPunctStart(r rune) bool {
	switch r {
	case '(', '[', '{', '"', '\'', '“', '‘', '/':
		return true
	}
	return false
}

// ---- break opportunities -----------------------------------------------------------

func isCJK(r rune) bool {
	return r >= 0x2e80 && r <= 0xa4cf || r >= 0xac00 && r <= 0xd7a3 || r >= 0xf900 && r <= 0xfaff || r >= 0xfe30 && r <= 0xfe4f || r >= 0xff00 && r <= 0xffef || r >= 0x20000 && r <= 0x2fa1f
}

// breakAfter reports whether a line break is allowed after rs[i] (before rs[i+1]).
func breakAfter(rs []rune, i int, st *style.Computed) bool {
	r := rs[i]
	var nx rune
	if i+1 < len(rs) {
		nx = rs[i+1]
	}
	if st.WordBreak == "break-all" {
		if !isZeroWidth(r) && nx != 0 && !unicode.IsSpace(nx) {
			return true
		}
	}
	if st.LineBreak == "anywhere" {
		return nx != 0
	}
	switch r {
	case ' ', '\t', '\n':
		return nx != ' ' && nx != '\t' || true
	case 0x200b:
		return true
	case 0xad:
		return true
	case 0xa0, 0x202f, 0x2060, 0xfeff, 0x2007:
		return false
	case '-', 0x2010, 0x2013:
		if nx == 0 {
			return true
		}
		if unicode.IsDigit(nx) && i > 0 && unicode.IsDigit(rs[i-1]) {
			return false
		}
		if i > 0 && (unicode.IsSpace(rs[i-1])) {
			return false
		}
		return unicode.IsLetter(nx) || unicode.IsDigit(nx) && false
	case 0x2014:
		return true
	case '/':
		if nx == 0 {
			return false
		}
		return unicode.IsLetter(nx) && !(i > 0 && rs[i-1] == '/')
	}
	if st.WordBreak != "keep-all" {
		if isCJK(r) {
			if nx == 0 {
				return true
			}
			switch nx {
			case '。', '、', '，', '．', '）', '」', '』', '！', '？', '：', '；', ',', '.', ')', '!', '?':
				return false
			}
			return true
		}
		if nx != 0 && isCJK(nx) {
			switch r {
			case '（', '「', '『', '(':
				return false
			}
			return true
		}
	}
	return false
}
