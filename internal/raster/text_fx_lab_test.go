package raster

import (
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/sweefbrowser/sweef/internal/font"
)

// Glyph lab: single glyphs rendered by Chromium (testdata/glyphlab/ref.png,
// captured from testdata/glyphlab/lab.html) against glyphMask + the preblend
// table. Regenerate the page with GLYPHLAB_GEN=1 go test -run GlyphLabGen.

type labFace struct {
	name, css, family string
	weight            int
	italic            bool
	regular           bool // use the regular face and synthesize bold / italic
}

const labSynthCSS = `@font-face{font-family:SynthSans;src:url('file:///usr/share/fonts/truetype/liberation/LiberationSans-Regular.ttf')}
@font-face{font-family:SynthSerif;src:url('file:///usr/share/fonts/truetype/liberation/LiberationSerif-Regular.ttf')}
@font-face{font-family:SynthDejaVu;src:url('file:///usr/share/fonts/truetype/dejavu/DejaVuSans.ttf')}
@font-face{font-family:SynthMono;src:url('file:///usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf')}
`

// labCfg selects the page variant: subpixel offset, fractional sizes,
// synthetic bold / italic faces.
type labCfg struct {
	sub         int
	frac, synth bool
	maxSize     float64 // 0: all sizes
	tone        int     // 0 black on white, 1 white on black, 2 #777 on white, 3 #bbb on #222
}

// labTones holds the text and background gray levels of each tone.
var labTones = [][2]int{{0, 255}, {255, 0}, {0x77, 255}, {0xbb, 0x22}}

func labFG() int { return labTones[labCur.tone][0] }
func labBG() int { return labTones[labCur.tone][1] }

// labBlend composites gray text of coverage m over the background.
func labBlend(m uint8) int {
	f, b := labFG(), labBG()
	return int(float64(b) + float64(f-b)*float64(m)/255 + 0.5)
}

func labLut() *[256]uint8 {
	g := float64(labFG()) / 255
	return lutForLum(textLum(Color{g, g, g, 1}))
}

var labCur = labCfg{maxSize: labEnvMax(), sub: labEnvSub(), tone: labEnvTone(), frac: os.Getenv("GLYPHLAB_FRAC") != "", synth: os.Getenv("GLYPHLAB_SYNTH") != ""}

func labEnvMax() float64 {
	n := 0.0
	fmt.Sscan(os.Getenv("GLYPHLAB_MAX"), &n)
	return n
}

func labEnvTone() int {
	n := 0
	fmt.Sscan(os.Getenv("GLYPHLAB_TONE"), &n)
	return n
}

func labEnvSub() int {
	n := 0
	fmt.Sscan(os.Getenv("GLYPHLAB_SUB"), &n)
	return n
}

var labFaces = labFaceSet()

func labUse(c labCfg) {
	labCur = c
	labFaces = labFaceSet()
	labSizes = labSizeSet()
	labOY = map[[2]int]int{}
}

func labFaceSet() []labFace {
	if labCur.synth {
		return []labFace{
			{"SynthBoldSans", "700 SZpx SynthSans", "Liberation Sans", 700, false, true},
			{"SynthItalicSans", "italic 400 SZpx SynthSans", "Liberation Sans", 400, true, true},
			{"SynthBoldItalicSans", "italic 700 SZpx SynthSans", "Liberation Sans", 700, true, true},
			{"SynthBoldSerif", "700 SZpx SynthSerif", "Liberation Serif", 700, false, true},
			{"SynthItalicSerif", "italic 400 SZpx SynthSerif", "Liberation Serif", 400, true, true},
			{"SynthBoldDejaVu", "700 SZpx SynthDejaVu", "DejaVu Sans", 700, false, true},
			{"SynthBoldMono", "700 SZpx SynthMono", "DejaVu Sans Mono", 700, false, true},
		}
	}
	return labFacesDefault
}

func labFont(f labFace) *font.Face {
	if f.regular {
		return font.Lookup(f.family, font.Style{Weight: 400})
	}
	return font.Lookup(f.family, font.Style{Weight: f.weight, Italic: f.italic})
}

var labFacesDefault = []labFace{
	{"LibSans", "400 SZpx 'Liberation Sans'", "Liberation Sans", 400, false, false},
	{"LibSansBold", "700 SZpx 'Liberation Sans'", "Liberation Sans", 700, false, false},
	{"LibSansItalic", "italic 400 SZpx 'Liberation Sans'", "Liberation Sans", 400, true, false},
	{"LibSerif", "400 SZpx 'Liberation Serif'", "Liberation Serif", 400, false, false},
	{"LibSerifBold", "700 SZpx 'Liberation Serif'", "Liberation Serif", 700, false, false},
	{"DejaVuSans", "400 SZpx 'DejaVu Sans'", "DejaVu Sans", 400, false, false},
	{"DejaVuSansBold", "700 SZpx 'DejaVu Sans'", "DejaVu Sans", 700, false, false},
	{"DejaVuMono", "400 SZpx 'DejaVu Sans Mono'", "DejaVu Sans Mono", 400, false, false},
	{"DejaVuSerifSynthItalic", "italic 400 SZpx 'DejaVu Serif'", "DejaVu Serif", 400, true, false},
}

var labOY = map[[2]int]int{}

var (
	labSizes  = labSizeSet()
	labGlyphs = []rune("oesg8OQSa")
)

const labPitch = 96

func labRowH(size float64) int { return int(size*1.5) + 10 }

func labRowTop(fi, si int) int {
	y := 4
	for a := 0; a < fi; a++ {
		for _, s := range labSizeSetFull() {
			y += labRowH(s)
		}
	}
	for b := 0; b < si; b++ {
		y += labRowH(labSizes[b])
	}
	return y
}

func labHeight() int { return labRowTop(len(labFaces), 0) + 4 }

func TestGlyphLabGen(t *testing.T) {
	if os.Getenv("GLYPHLAB_GEN") == "" {
		t.Skip("set GLYPHLAB_GEN=1")
	}
	var b strings.Builder
	b.WriteString("<!doctype html><html><head><meta charset=utf-8><style>" + labSynthCSS + "html,body{margin:0;background:" + fmt.Sprintf("#%02x%02x%02x", labBG(), labBG(), labBG()) + "}div{position:absolute;color:" + fmt.Sprintf("#%02x%02x%02x", labFG(), labFG(), labFG()) + ";white-space:nowrap}</style></head><body>\n")
	for fi, f := range labFaces {
		for si, sz := range labSizes {
			top := labRowTop(fi, si)
			lh := labRowH(sz)
			css := strings.Replace(f.css, "SZ", fmt.Sprint(sz), 1)
			for gi, g := range labGlyphs {
				fmt.Fprintf(&b, "<div style=\"left:%gpx;top:%dpx;font:%s;line-height:%dpx\">%c</div>\n", float64(8+gi*labPitch)+float64(labCur.sub)/4, top, css, lh, g)
			}
		}
	}
	b.WriteString("</body></html>\n")
	if err := os.WriteFile(fmt.Sprintf("testdata/glyphlab/lab%s%s.html", labFracSuffix(), labSubSuffix()), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("viewport 600 x %d", labHeight())
}

type labStat struct {
	n, ink       int
	sum, max     int
	g8, g24, g64 int
	dx, dy       int
	name         string
	face         string
	size         float64
	r            rune
}

func labGray(im image.Image, x, y int) int {
	r, _, _, _ := im.At(x, y).RGBA()
	return int(r >> 8)
}

// labCompare renders one glyph cell and returns its stats.
func labCompare(ref image.Image, f labFace, fi, si int, gi int, face *font.Face) labStat {
	size := labSizes[si]
	r := labGlyphs[gi]
	top := labRowTop(fi, si)
	rh := labRowH(size)
	x0 := 8 + gi*labPitch
	synthSkew := f.italic && !face.Style.Italic
	synthBold := f.weight >= 600 && face.Style.Weight < 600
	m := labMask(Glyph{R: r, Face: face}, size, labCur.sub, synthBold, synthSkew)
	st := labStat{face: f.name, size: size, r: r, name: fmt.Sprintf("%s/%g/%c", f.name, size, r)}
	if m == nil {
		return st
	}
	lut := labLut()
	cw := labPitch
	// ref cell region: [x0-4, x0-4+cw) x [top, top+rh)
	rx0, ry0 := x0-4, top
	best := math.MaxInt
	var bestG []int
	var bdx, bdy int
	lo, hi := int(size*0.6), min(rh, int(size*1.35)+4)
	if v, ok := labOY[[2]int{fi, si}]; ok {
		lo, hi = max(0, v-1), min(rh, v+1)
	}
	for oy := lo; oy <= hi; oy++ {
		for ox := 3; ox <= 5; ox++ {
			g := make([]int, cw*rh)
			for i := range g {
				g[i] = labBG()
			}
			for y := m.B.Y0; y < m.B.Y1; y++ {
				yy := oy + y
				if yy < 0 || yy >= rh {
					continue
				}
				for i, v := range m.Row(y) {
					xx := ox + m.B.X0 + i
					if v == 0 || xx < 0 || xx >= cw {
						continue
					}
					g[yy*cw+xx] = labBlend(lut[v])
				}
			}
			sum := 0
			for y := 0; y < rh; y++ {
				for x := 0; x < cw; x++ {
					d := g[y*cw+x] - labGray(ref, rx0+x, ry0+y)
					if d < 0 {
						d = -d
					}
					sum += d
				}
			}
			if sum < best {
				best, bestG, bdx, bdy = sum, g, ox-4, oy
			}
		}
	}
	st.dx, st.dy = bdx, bdy
	if _, ok := labOY[[2]int{fi, si}]; !ok {
		labOY[[2]int{fi, si}] = bdy
	}
	for y := 0; y < rh; y++ {
		for x := 0; x < cw; x++ {
			a := bestG[y*cw+x]
			b := labGray(ref, rx0+x, ry0+y)
			if a == labBG() && b == labBG() {
				continue
			}
			d := a - b
			if d < 0 {
				d = -d
			}
			st.ink++
			st.sum += d
			if d > st.max {
				st.max = d
			}
			if d > 8 {
				st.g8++
			}
			if d > 24 {
				st.g24++
			}
			if d > 64 {
				st.g64++
			}
		}
	}
	return st
}

func TestGlyphLab(t *testing.T) {
	if os.Getenv("GLYPHLAB_DUMP") != "" || os.Getenv("GLYPHLAB_V") != "" || os.Getenv("GLYPHLAB_LIST") != "" {
		glyphLab(t) // interactive use: the variant comes from the environment
		return
	}
	defer labUse(labCur)
	for _, c := range []struct {
		name string
		cfg  labCfg
	}{
		{"plain", labCfg{}},
		{"subpixel2", labCfg{sub: 2, maxSize: 32}},
		{"fractional", labCfg{frac: true}},
		{"synthetic", labCfg{synth: true, maxSize: 48}},
		{"white-on-black", labCfg{tone: 1, maxSize: 24}},
		{"gray", labCfg{tone: 2, maxSize: 24}},
	} {
		t.Run(c.name, func(t *testing.T) {
			labUse(c.cfg)
			glyphLab(t)
		})
	}
}

func glyphLab(t *testing.T) {
	fh, err := os.Open(labRefPath())
	if err != nil {
		t.Skip("no reference capture ", labRefPath())
	}
	ref, err := png.Decode(fh)
	fh.Close()
	if err != nil {
		t.Fatal(err)
	}
	labApplyEnv()
	if os.Getenv("GLYPHLAB_DUMP") != "" {
		labDump(t, ref, os.Getenv("GLYPHLAB_DUMP"))
		return
	}
	var all []labStat
	type agg struct{ ink, sum, g8, g24, g64, max int }
	byFace := map[string]*agg{}
	bySize := map[float64]*agg{}
	tot := &agg{}
	add := func(a *agg, s labStat) {
		a.ink += s.ink
		a.sum += s.sum
		a.g8 += s.g8
		a.g24 += s.g24
		a.g64 += s.g64
		if s.max > a.max {
			a.max = s.max
		}
	}
	for fi, f := range labFaces {
		face := labFont(f)
		if face == nil {
			continue
		}
		for si := range labSizes {
			for gi := range labGlyphs {
				s := labCompare(ref, f, fi, si, gi, face)
				all = append(all, s)
				if byFace[f.name] == nil {
					byFace[f.name] = &agg{}
				}
				if bySize[s.size] == nil {
					bySize[s.size] = &agg{}
				}
				add(byFace[f.name], s)
				add(bySize[s.size], s)
				add(tot, s)
			}
		}
	}
	line := func(name string, a *agg) string {
		if a.ink == 0 {
			return name
		}
		return fmt.Sprintf("%-26s ink %6d mean %5.2f max %3d  >8 %5.2f%%  >24 %5.2f%%  >64 %5.2f%%", name, a.ink, float64(a.sum)/float64(a.ink), a.max,
			100*float64(a.g8)/float64(a.ink), 100*float64(a.g24)/float64(a.ink), 100*float64(a.g64)/float64(a.ink))
	}
	var sb strings.Builder
	sb.WriteString("\n")
	for _, f := range labFaces {
		if a := byFace[f.name]; a != nil {
			sb.WriteString(line(f.name, a) + "\n")
		}
	}
	for _, s := range labSizes {
		if a := bySize[s]; a != nil {
			sb.WriteString(line(fmt.Sprintf("size %g", s), a) + "\n")
		}
	}
	sb.WriteString(line("TOTAL", tot) + "\n")

	if p := os.Getenv("GLYPHLAB_LIST"); p != "" {
		var lb strings.Builder
		for _, s := range all {
			fmt.Fprintf(&lb, "%s %d %d\n", s.name, s.g24, s.sum)
		}
		os.WriteFile(p, []byte(lb.String()), 0o644)
	}
	if os.Getenv("GLYPHLAB_V") != "" {
		sort.Slice(all, func(i, j int) bool { return all[i].g24 > all[j].g24 })
		for i := 0; i < 40 && i < len(all); i++ {
			s := all[i]
			fmt.Fprintf(&sb, "  %-28s ink %4d mean %5.2f max %3d >24 %3d  off(%d,%d)\n", s.name, s.ink, float64(s.sum)/float64(max(1, s.ink)), s.max, s.g24, s.dx, s.dy)
		}
	}
	t.Log(sb.String())
	if !textFxLegacy && tot.ink > 0 {
		// regression guard: the legacy float polygons score 4.6% / 1.0% here
		if f := float64(tot.g8) / float64(tot.ink); f > 0.018 {
			t.Errorf("share of glyph pixels off by more than 8/255: %.2f%%", 100*f)
		}
		if f := float64(tot.g24) / float64(tot.ink); f > 0.007 {
			t.Errorf("share of glyph pixels off by more than 24/255: %.2f%%", 100*f)
		}
	}
}

// labDump prints ours/ref/diff for "Face/size/glyph".
func labDump(t *testing.T, ref image.Image, spec string) {
	p := strings.Split(spec, "/")
	var fi, si, gi = -1, -1, -1
	for i, f := range labFaces {
		if f.name == p[0] {
			fi = i
		}
	}
	var sz float64
	fmt.Sscan(p[1], &sz)
	for i, s := range labSizes {
		if s == sz {
			si = i
		}
	}
	for i, g := range labGlyphs {
		if string(g) == p[2] {
			gi = i
		}
	}
	if fi < 0 || si < 0 || gi < 0 {
		t.Fatal("bad spec")
	}
	f := labFaces[fi]
	face := labFont(f)
	st := labCompare(ref, f, fi, si, gi, face)
	synthSkew := f.italic && !face.Style.Italic
	synthBold := f.weight >= 600 && face.Style.Weight < 600
	m := labMask(Glyph{R: labGlyphs[gi], Face: face}, sz, labCur.sub, synthBold, synthSkew)
	lut := labLut()
	top := labRowTop(fi, si)
	x0 := 8 + gi*labPitch - 4
	var sb strings.Builder
	fmt.Fprintf(&sb, "\n%s off(%d,%d) mask %+v\n", st.name, st.dx, st.dy, m.B)
	for y := m.B.Y0; y < m.B.Y1; y++ {
		for x := m.B.X0 - 1; x < m.B.X1+1; x++ {
			ours := labBlend(lut[m.At(x, y)])
			r := labGray(ref, x0+4+st.dx+x, top+st.dy+y)
			d := ours - r
			if d == 0 {
				sb.WriteString("   .")
			} else {
				fmt.Fprintf(&sb, "%4d", d)
			}
		}
		sb.WriteString("\n")
	}
	if os.Getenv("GLYPHLAB_RAW") != "" {
		for _, which := range []string{"ours", "ref"} {
			fmt.Fprintf(&sb, "%s (coverage)\n", which)
			for y := m.B.Y0; y < m.B.Y1; y++ {
				for x := m.B.X0 - 1; x < m.B.X1+1; x++ {
					v := int(lut[m.At(x, y)])
					if which == "ref" {
						v = 255 - labGray(ref, x0+4+st.dx+x, top+st.dy+y)
					}
					fmt.Fprintf(&sb, "%4d", v)
				}
				sb.WriteString("\n")
			}
		}
	}
	t.Log(sb.String())
}

func labApplyEnv() {

	if os.Getenv("GLYPHLAB_LEGACY") != "" {
		textFxLegacy = true
	}
}

func labRefPath() string {
	if v := os.Getenv("GLYPHLAB_REF"); v != "" {
		return v
	}
	return fmt.Sprintf("testdata/glyphlab/ref%s%s.png", labFracSuffix(), labSubSuffix())
}

func labSubSuffix() string {
	if labCur.sub == 0 {
		return ""
	}
	return fmt.Sprintf("_sub%d", labCur.sub)
}

func labSizeSetFull() []float64 {
	if labCur.frac {
		return []float64{10.5, 11.7, 13.33, 14.4, 15.5, 17.6, 21.3, 36.8}
	}
	return []float64{9, 10, 11, 12, 13, 14, 16, 18, 20, 24, 32, 48, 72, 100}
}

func labSizeSet() []float64 {
	all := labSizeSetFull()
	if labCur.maxSize > 0 && !labCur.frac {
		for i, v := range all {
			if v > labCur.maxSize {
				return all[:i]
			}
		}
	}
	return all
}

func labFracSuffix() string {
	r := ""
	if labCur.frac {
		r += "_frac"
	}
	if labCur.synth {
		r += "_synth"
	}
	if labCur.tone != 0 {
		r += fmt.Sprintf("_tone%d", labCur.tone)
	}
	return r
}

// labMask renders one glyph the way glyphMask does, calling the synthetic
// bold rasterizer directly.
func labMask(g Glyph, size float64, sub int, bold, skew bool) *Mask {
	if bold {
		return skiaGlyphBold(g, size, size, sub, skew)
	}
	return glyphMask(g, size, size, sub, false, skew)
}
