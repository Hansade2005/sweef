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
	web       bool           // the family list has web font families: missing glyphs may need a download
	groups    [][]*font.Face // web only: the loaded faces of each family of the list, in list order
	demanded  map[rune]bool
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
	fst := font.Style{Weight: st.FontWeight, Italic: st.FontStyle != 0, OpticalSize: st.FontSize}
	fs := &fontSpec{size: st.FontSize, fam: st.FontFamily, st: fst, fallback: map[rune]*font.Face{}, web: font.HasScoped(st.FontFamily)}
	for _, f := range st.FontFamily {
		if fs.web {
			// a web font family has one face per unicode-range subset
			g := font.LookupAll(f, fst)
			fs.groups = append(fs.groups, g)
			fs.faces = append(fs.faces, g...)
		} else if face := font.Lookup(f, fst); face != nil {
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
	if fs.web {
		return fs.webFaceFor(r)
	}
	for _, f := range fs.faces {
		if f.HasGlyph(r) {
			return f
		}
	}
	return fs.fallbackFor(r)
}

// webFaceFor walks the family list in order. A web family without a loaded
// face for r asks for the download (font.DemandFamily: only the first family
// that is on its way is waited for) and the character is shown in the next
// family meanwhile, so text is never invisible.
func (fs *fontSpec) webFaceFor(r rune) *font.Face {
	waiting := false
	for i, g := range fs.groups {
		for _, f := range g {
			if f.HasGlyph(r) {
				return f
			}
		}
		if !waiting && i < len(fs.fam) && strings.IndexByte(fs.fam[i], 0) >= 0 && !fs.demanded[r] {
			if font.DemandFamily(fs.fam[i], fs.st, r) {
				waiting = true
			}
		}
	}
	if fs.demanded == nil {
		fs.demanded = map[rune]bool{}
	}
	fs.demanded[r] = true
	return fs.fallbackFor(r)
}

func (fs *fontSpec) fallbackFor(r rune) *font.Face {
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
	kn    []float64 // kerning applied before rune i (glyph origin offset); may be nil
	// Optional OpenType/Arabic shaping results (nil for plain text):
	form []rune  // Arabic presentation form shown for rune i; 0 = consumed (lam-alef tail)
	gid  []int32 // ligature glyph id when rune i heads a ligature
	nlig []uint8 // runes covered by the ligature headed at rune i (0/1 = none)
}

func (sh *shaped) kernAt(i int) float64 {
	if sh == nil || i < 0 || i >= len(sh.kn) {
		return 0
	}
	return sh.kn[i]
}

// shape computes glyph positions for runes in the given style.
func (c *ctx) shape(fs *fontSpec, st *style.Computed, rs []rune) *shaped {
	return c.shapeWith(fs, st, rs, 0, nil)
}

func isArabicRune(r rune) bool { return r >= 0x0600 && r <= 0x06ff }

// shapeWith shapes rs continuing a text run whose previous glyph was prev in face prevFace.
func (c *ctx) shapeWith(fs *fontSpec, st *style.Computed, rs []rune, prev rune, prevFace *font.Face) *shaped {
	n := len(rs)
	sh := &shaped{r: rs, faces: make([]*font.Face, n), x: make([]float64, n+1), kn: make([]float64, n)}
	kern := st.FontKerning != "none" && st.TextRendering != "optimizespeed"
	liga := !st.FontLigOff && st.LetterSpacing == 0 && st.TextRendering != "optimizespeed"
	// Arabic letters take their contextual forms
	forms := rs
	for _, r := range rs {
		if isArabicRune(r) {
			forms = font.ShapeArabicKeep(rs)
			sh.form = forms
			break
		}
	}
	for i, r := range rs {
		fr := forms[i]
		if fr == 0 {
			sh.faces[i] = fs.faceFor(r)
			continue
		}
		f := fs.faceFor(fr)
		if fr != r && (f == nil || !f.HasGlyph(fr)) {
			// the face lacks the presentation form: show the plain letter
			forms[i] = r
			f = fs.faceFor(r)
		}
		sh.faces[i] = f
	}
	x := 0.0
	prevG := -1 // glyph id of the previous glyph when it was a ligature (else -1)
	for i := 0; i < n; i++ {
		r := rs[i]
		f := sh.faces[i]
		adv := 0.0
		cells := 1
		curG := -1
		switch {
		case forms[i] == 0:
			adv = 0
		case r == '\t':
			adv = fs.spaceW
		case isZeroWidth(r) && r != 0xad:
			adv = 0
		case r == 0xad:
			adv = 0
		case f != nil:
			dr := forms[i]
			// standard ligatures (fi, fl, ...) within one face
			if liga && i+1 < n && f.HasLigatures() && !isArabicRune(r) {
				j := i + 1
				lm := f.LigMax()
				for j < n && j < i+lm && sh.faces[j] == f && forms[j] != ' ' && !isZeroWidth(rs[j]) {
					j++
				}
				if j-i >= 2 && r != ' ' {
					if g, cc := f.Ligature(forms[i:j]); cc >= 2 {
						if sh.gid == nil {
							sh.gid = make([]int32, n)
							sh.nlig = make([]uint8, n)
						}
						sh.gid[i], sh.nlig[i] = int32(g), uint8(cc)
						adv = f.AdvanceGID(g, fs.size)
						if kern && prev != 0 && prevFace == f {
							var kv float64
							if prevG >= 0 {
								kv = f.KernGIDs(prevG, g, fs.size)
							} else {
								kv = f.KernGIDs(f.GID(prev), g, fs.size)
							}
							adv += kv
							sh.kn[i] = kv
						}
						cells = cc
						curG = g
					}
				}
			}
			if cells == 1 {
				a := f.AdvanceF(dr, fs.size)
				if a < 0 {
					a = fs.size * 0.5
				}
				adv = a
				if st.VertFrame != 0 && st.TextOrientation == "upright" {
					adv = fs.size // upright glyphs advance by an em in vertical text
				}
				if kern && prev != 0 && prevFace == f {
					var kv float64
					if prevG >= 0 {
						kv = f.KernGIDs(prevG, f.GID(dr), fs.size)
					} else {
						kv = f.KernF(prev, dr, fs.size)
					}
					adv += kv
					sh.kn[i] = kv
				}
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
		for k := 0; k < cells; k++ {
			x += adv / float64(cells)
			sh.x[i+k+1] = x
			if k > 0 {
				sh.faces[i+k] = f
			}
		}
		last := i + cells - 1
		if forms[last] != 0 {
			prev, prevFace = forms[last], sh.faces[last]
		}
		prevG = curG
		i = last
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
		if i > 0 && rs[i-1] >= 0x5d0 && rs[i-1] <= 0x5ea {
			return false // LB21a: no break after a Hebrew letter and hyphen
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
