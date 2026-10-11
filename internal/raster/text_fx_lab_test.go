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
}

var labFaces = []labFace{
	{"LibSans", "400 SZpx 'Liberation Sans'", "Liberation Sans", 400, false},
	{"LibSansBold", "700 SZpx 'Liberation Sans'", "Liberation Sans", 700, false},
	{"LibSansItalic", "italic 400 SZpx 'Liberation Sans'", "Liberation Sans", 400, true},
	{"LibSerif", "400 SZpx 'Liberation Serif'", "Liberation Serif", 400, false},
	{"LibSerifBold", "700 SZpx 'Liberation Serif'", "Liberation Serif", 700, false},
	{"DejaVuSans", "400 SZpx 'DejaVu Sans'", "DejaVu Sans", 400, false},
	{"DejaVuSansBold", "700 SZpx 'DejaVu Sans'", "DejaVu Sans", 700, false},
	{"DejaVuMono", "400 SZpx 'DejaVu Sans Mono'", "DejaVu Sans Mono", 400, false},
	{"DejaVuSerifSynthItalic", "italic 400 SZpx 'DejaVu Serif'", "DejaVu Serif", 400, true},
}

var labQuad [2][2]int

var (
	labSizes  = []int{9, 10, 11, 12, 13, 14, 16, 18, 20, 24, 32, 48, 72, 100}
	labGlyphs = []rune("oesg8OQSa")
)

const labPitch = 96

func labRowH(size int) int { return size*3/2 + 10 }

func labRowTop(fi, si int) int {
	y := 4
	for a := 0; a < fi; a++ {
		for _, s := range labSizes {
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
	b.WriteString("<!doctype html><html><head><meta charset=utf-8><style>html,body{margin:0;background:#fff}div{position:absolute;color:#000;white-space:nowrap}</style></head><body>\n")
	for fi, f := range labFaces {
		for si, sz := range labSizes {
			top := labRowTop(fi, si)
			lh := labRowH(sz)
			css := strings.Replace(f.css, "SZ", fmt.Sprint(sz), 1)
			for gi, g := range labGlyphs {
				fmt.Fprintf(&b, "<div style=\"left:%dpx;top:%dpx;font:%s;line-height:%dpx\">%c</div>\n", 8+gi*labPitch, top, css, lh, g)
			}
		}
	}
	b.WriteString("</body></html>\n")
	if err := os.WriteFile("testdata/glyphlab/lab.html", []byte(b.String()), 0o644); err != nil {
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
	size         int
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
	m := glyphMask(Glyph{R: r, Face: face}, float64(size), float64(size), 0, synthBold, synthSkew)
	st := labStat{face: f.name, size: size, r: r, name: fmt.Sprintf("%s/%d/%c", f.name, size, r)}
	if m == nil {
		return st
	}
	lut := lutForLum(0)
	cw := labPitch
	// ref cell region: [x0-4, x0-4+cw) x [top, top+rh)
	rx0, ry0 := x0-4, top
	best := math.MaxInt
	var bestG []int
	var bdx, bdy int
	for oy := 0; oy <= rh; oy++ {
		for ox := 2; ox <= 6; ox++ {
			g := make([]int, cw*rh)
			for i := range g {
				g[i] = 255
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
					g[yy*cw+xx] = 255 - int(lut[v])
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
	for y := 0; y < rh; y++ {
		for x := 0; x < cw; x++ {
			a := bestG[y*cw+x]
			b := labGray(ref, rx0+x, ry0+y)
			if a == 255 && b == 255 {
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
				qy, qx := 0, 0
				if y-bdy >= (m.B.Y0+m.B.Y1)/2 {
					qy = 1
				}
				if x-(bdx+4) >= (m.B.X0+m.B.X1)/2 {
					qx = 1
				}
				labQuad[qy][qx]++
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
	fh, err := os.Open("testdata/glyphlab/ref.png")
	if err != nil {
		t.Skip("no testdata/glyphlab/ref.png")
	}
	ref, err := png.Decode(fh)
	fh.Close()
	if err != nil {
		t.Fatal(err)
	}
	if v := os.Getenv("GLYPHLAB_MODE"); v != "" {
		fmt.Sscan(v, &textFxMode)
	}
	labApplyEnv()
	if os.Getenv("GLYPHLAB_DUMP") != "" {
		labDump(t, ref, os.Getenv("GLYPHLAB_DUMP"))
		return
	}
	var all []labStat
	type agg struct{ ink, sum, g8, g24, g64, max int }
	byFace := map[string]*agg{}
	bySize := map[int]*agg{}
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
		face := font.Lookup(f.family, font.Style{Weight: f.weight, Italic: f.italic})
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
			sb.WriteString(line(fmt.Sprintf("size %d", s), a) + "\n")
		}
	}
	sb.WriteString(line("TOTAL", tot) + "\n")
	fmt.Fprintf(&sb, "cell-half counts >24 (rows top/bottom of cell, cols left/right): %v\n", labQuad)
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
	var sz int
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
	face := font.Lookup(f.family, font.Style{Weight: f.weight, Italic: f.italic})
	st := labCompare(ref, f, fi, si, gi, face)
	synthSkew := f.italic && !face.Style.Italic
	synthBold := f.weight >= 600 && face.Style.Weight < 600
	m := glyphMask(Glyph{R: labGlyphs[gi], Face: face}, float64(sz), float64(sz), 0, synthBold, synthSkew)
	lut := lutForLum(0)
	top := labRowTop(fi, si)
	x0 := 8 + gi*labPitch - 4
	var sb strings.Builder
	fmt.Fprintf(&sb, "\n%s off(%d,%d) mask %+v\n", st.name, st.dx, st.dy, m.B)
	for y := m.B.Y0; y < m.B.Y1; y++ {
		for x := m.B.X0 - 1; x < m.B.X1+1; x++ {
			ours := 255 - int(lut[m.At(x, y)])
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
	if v := os.Getenv("GLYPHLAB_MODE"); v != "" {
		fmt.Sscan(v, &textFxMode)
	}
	fxEvenRound = os.Getenv("FX_EVEN") != ""
	fxBigSnap = os.Getenv("FX_NOBIG") == ""
	if v := os.Getenv("FX_BV"); v != "" {
		fmt.Sscan(v, &fxBigVariant)
	}
	if v := os.Getenv("FX_SB"); v != "" {
		fmt.Sscan(v, &fxShiftBias)
	}
	fxQuadOffset = os.Getenv("FX_QOFF") != ""
	if v := os.Getenv("FX_DS"); v != "" {
		fmt.Sscan(v, &fxDS)
	}
	fxQSnap = os.Getenv("FX_QNOSNAP") == ""
	fxQNoSnapMid = os.Getenv("FX_NOMID") != ""
	if v := os.Getenv("FX_QOM"); v != "" {
		fmt.Sscan(v, &fxQOffMode)
	}
	fxShiftMap = nil
	if v := os.Getenv("FX_SMAP"); v != "" {
		for _, f := range strings.Split(v, ",") {
			var n int
			fmt.Sscan(f, &n)
			fxShiftMap = append(fxShiftMap, n)
		}
	}
	if v := os.Getenv("FX_RM"); v != "" {
		fmt.Sscan(v, &fxRMode)
	}
	if v := os.Getenv("FX_SM"); v != "" {
		fmt.Sscan(v, &fxStartMode)
	}
	fxSmallSlide = os.Getenv("FX_SLIDE") != ""
	if v := os.Getenv("FX_YOFF"); v != "" {
		fmt.Sscan(v, &textFxYOff)
	}
	if v := os.Getenv("FX_SR"); v != "" {
		fmt.Sscan(v, &fxSmallRound)
	}
	if v := os.Getenv("FX_CMIN"); v != "" {
		fmt.Sscan(v, &fxCrossMin)
	}
	if v := os.Getenv("FX_CROSS"); v != "" {
		fmt.Sscan(v, &fxCross)
	}
	if v := os.Getenv("FX_THRESH"); v != "" {
		var f float64
		fmt.Sscan(v, &f)
		fxBigThresh = int(f * fxOne)
	}
}
