package raster

import (
	"encoding/json"
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

// Rotated-glyph lab: single glyphs drawn by Chromium through canvas
// setTransform + fillText (so the device origin is known exactly) against the
// transformed glyph pipeline (text_xf.go). Groups:
//
//	plain   Liberation Sans / Serif, DejaVu Sans, Liberation Sans Italic, 12 to 40 px
//	synth   synthetic italic and bold (regular face + CSS style), 12 to 40 px
//	large   257 and 400 px, drawn as paths
//	cff     Inter (CFF outlines: cubics), 12 to 40 px
//
// each at the angles 0.5 5 15 30 45 60 89 90 90.5 135 180 270, skewX 20,
// skewY 15, scale(1.5,0.8), scale(2.3) and a general matrix, with
// pseudo-random subpixel origins. Regenerate a page with
//
//	XFLAB_GEN=1 XFLAB_GROUP=plain go test -run XfLabGen ./internal/raster
//
// and capture it at the viewport the test logs, saving
// testdata/glyphlab/xf<group>.png (see capture.sh style in the README of the
// glyph labs: open the page in Chromium 1194, set the viewport, screenshot).

type xfFace struct {
	name, css, family string
	weight            int
	italic            bool
	regular           bool // use the regular face and synthesize bold / italic
}

const xfSynthCSS = `@font-face{font-family:SynthSans;src:url('file:///usr/share/fonts/truetype/liberation/LiberationSans-Regular.ttf')}
@font-face{font-family:SynthSerif;src:url('file:///usr/share/fonts/truetype/liberation/LiberationSerif-Regular.ttf')}
@font-face{font-family:SynthDejaVu;src:url('file:///usr/share/fonts/truetype/dejavu/DejaVuSans.ttf')}
`

type xfTf struct {
	name       string
	a, b, c, d float64
}

func xfRot(deg float64) xfTf {
	r := deg * math.Pi / 180
	return xfTf{fmt.Sprintf("rot%g", deg), math.Cos(r), math.Sin(r), -math.Sin(r), math.Cos(r)}
}

var xfTfsAll = []xfTf{
	{"id", 1, 0, 0, 1},
	xfRot(0.5), xfRot(5), xfRot(15), xfRot(30), xfRot(45), xfRot(60), xfRot(89), xfRot(90), xfRot(90.5), xfRot(135), xfRot(180), xfRot(270),
	{"skewX20", 1, 0, math.Tan(20 * math.Pi / 180), 1},
	{"skewY15", 1, math.Tan(15 * math.Pi / 180), 0, 1},
	{"scale1.5x.8", 1.5, 0, 0, 0.8},
	{"scale2.3", 2.3, 0, 0, 2.3},
	{"matrix", 1.2, 0.3, -0.2, 0.9},
}

type xfGroup struct {
	name   string
	cell   int
	cols   int
	large  bool
	faces  []xfFace
	sizes  []float64
	tfs    []xfTf
	glyphs []rune
	// guards: maximum share of ink pixels off by more than 8/255 and 24/255
	max8, max24 float64
}

var xfGroups = []xfGroup{
	{"plain", 128, 24, false, []xfFace{
		{"LibSans", "400 SZpx 'Liberation Sans'", "Liberation Sans", 400, false, false},
		{"LibSerif", "400 SZpx 'Liberation Serif'", "Liberation Serif", 400, false, false},
		{"DejaVuSans", "400 SZpx 'DejaVu Sans'", "DejaVu Sans", 400, false, false},
		{"LibSansItalic", "italic 400 SZpx 'Liberation Sans'", "Liberation Sans", 400, true, false},
	}, []float64{12, 16, 24, 40}, xfTfsAll, []rune("oRgQe"), 0.006, 0.001},
	{"synth", 128, 24, false, []xfFace{
		{"SynthItalicSans", "italic 400 SZpx SynthSans", "Liberation Sans", 400, true, true},
		{"SynthItalicSerif", "italic 400 SZpx SynthSerif", "Liberation Serif", 400, true, true},
		{"SynthBoldSans", "700 SZpx SynthSans", "Liberation Sans", 700, false, true},
		{"SynthBoldItalicDejaVu", "italic 700 SZpx SynthDejaVu", "DejaVu Sans", 700, true, true},
	}, []float64{12, 16, 24, 40}, xfTfsAll, []rune("oRgQe"), 0.008, 0.002},
	{"large", 800, 5, true, []xfFace{
		{"LibSans", "400 SZpx 'Liberation Sans'", "Liberation Sans", 400, false, false},
		{"LibSerif", "400 SZpx 'Liberation Serif'", "Liberation Serif", 400, false, false},
	}, []float64{257, 400}, []xfTf{{"id", 1, 0, 0, 1}, xfRot(15), xfRot(45), xfRot(90), {"skewX20", 1, 0, math.Tan(20 * math.Pi / 180), 1}, {"scale1.3x.8", 1.3, 0, 0, 0.8}}, []rune("Rg"), 0.002, 0.0005},
	{"cff", 128, 24, false, []xfFace{
		{"InterRegular", "400 SZpx 'Inter'", "Inter", 400, false, false},
		{"InterBold", "700 SZpx 'Inter'", "Inter", 700, false, false},
		{"InterItalic", "italic 400 SZpx 'Inter'", "Inter", 400, true, false},
	}, []float64{12, 16, 24, 40}, xfTfsAll, []rune("oRgQe"), 0.006, 0.001},
}

// xfCur is the group the helpers below work on.
var xfCur = &xfGroups[0]

func xfUse(name string) *xfGroup {
	for i := range xfGroups {
		if xfGroups[i].name == name {
			xfCur = &xfGroups[i]
			return xfCur
		}
	}
	panic("no group " + name)
}

func xfEnvGroup() string {
	if g := os.Getenv("XFLAB_GROUP"); g != "" {
		return g
	}
	return "plain"
}

func xfLookup(f xfFace) *font.Face {
	if f.regular {
		return font.Lookup(f.family, font.Style{Weight: 400})
	}
	return font.Lookup(f.family, font.Style{Weight: f.weight, Italic: f.italic})
}

type xfCellSpec struct {
	face   xfFace
	size   float64
	tf     xfTf
	r      rune
	e, f   float64
	cx, cy int
}

func xfCells() []xfCellSpec {
	g := xfCur
	var out []xfCellSpec
	i := 0
	for _, fc := range g.faces {
		for _, sz := range g.sizes {
			for _, tf := range g.tfs {
				for _, r := range g.glyphs {
					cx, cy := (i%g.cols)*g.cell, (i/g.cols)*g.cell
					// pseudo-random subpixel origin
					fx := float64((i*37)%100) / 100
					fy := float64((i*53+17)%100) / 100
					out = append(out, xfCellSpec{fc, sz, tf, r, float64(cx+g.cell/2) + fx, float64(cy+g.cell/2) + fy, cx, cy})
					i++
				}
			}
		}
	}
	return out
}

func xfPage(ext string) string { return "testdata/glyphlab/xf" + xfCur.name + ext }

func TestXfLabGen(t *testing.T) {
	if os.Getenv("XFLAB_GEN") == "" {
		t.Skip("set XFLAB_GEN=1")
	}
	g := xfUse(xfEnvGroup())
	cells := xfCells()
	type js struct {
		F string
		M [6]float64
		C string
	}
	var items []js
	for _, c := range cells {
		items = append(items, js{strings.Replace(c.face.css, "SZ", fmt.Sprint(c.size), 1), [6]float64{c.tf.a, c.tf.b, c.tf.c, c.tf.d, c.e, c.f}, string(c.r)})
	}
	data, _ := json.Marshal(items)
	rows := (len(cells) + g.cols - 1) / g.cols
	html := fmt.Sprintf(`<!doctype html><html><head><meta charset=utf-8><style>%s</style></head><body style="margin:0;background:#fff;overflow:hidden"><canvas style="display:block" id=c width=%d height=%d></canvas><script>
window.__done=false;
const items=%s;const x=document.getElementById('c').getContext('2d');x.fillStyle='#000';
const fonts=[...new Set(items.map(i=>i.F))];
Promise.all(fonts.map(f=>document.fonts.load(f))).then(()=>{
for(const it of items){x.setTransform(1,0,0,1,0,0);x.font=it.F;x.setTransform(...it.M);x.fillText(it.C,0,0);}
window.__done=true;});
</script></body></html>`, xfSynthCSS, g.cols*g.cell, rows*g.cell, data)
	if err := os.WriteFile(xfPage(".html"), []byte(html), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("viewport %d x %d", g.cols*g.cell, rows*g.cell)
}

// xfRender draws one cell the way textMask would: a cached mask on the
// quarter-pixel grid through the preblend table, or a path for big glyphs.
func xfRender(c xfCellSpec, face *font.Face, lut *[256]uint8) (img []int, w, h int) {
	g := xfCur
	w, h = g.cell, g.cell
	img = make([]int, w*h)
	for i := range img {
		img[i] = 255
	}
	italic := c.face.italic && !face.Style.Italic
	bold := c.face.weight >= 600 && face.Style.Weight < 600
	fe, ff := c.e-float64(c.cx), c.f-float64(c.cy)
	if g.large {
		p := xfPathGlyph(Glyph{R: c.r, Face: face}, c.size, Matrix{c.tf.a, c.tf.b, c.tf.c, c.tf.d, fe, ff}, italic, 0, 0)
		if m := fxFillPath(p, IRect{0, 0, w, h}); m != nil {
			for y := m.B.Y0; y < m.B.Y1; y++ {
				for i, v := range m.Row(y) {
					img[y*w+m.B.X0+i] = 255 - int(v)
				}
			}
		}
		return
	}
	ix, iy, sx, sy := xfPlace(fe, ff, xfAxis(c.tf.a, c.tf.b, c.tf.c, c.tf.d))
	m := xfGlyphMask(Glyph{R: c.r, Face: face}, c.size, c.tf.a, c.tf.b, c.tf.c, c.tf.d, bold, italic, sx, sy)
	if m == nil {
		return
	}
	for y := m.B.Y0; y < m.B.Y1; y++ {
		for i, v := range m.Row(y) {
			xx, yy := ix+m.B.X0+i, iy+y
			if v == 0 || xx < 0 || yy < 0 || xx >= w || yy >= h {
				continue
			}
			img[yy*w+xx] = 255 - int(lut[v])
		}
	}
	return
}

func xfBorderInk(ref image.Image, c xfCellSpec) bool {
	n := xfCur.cell
	for i := 0; i < n; i++ {
		for _, p := range [][2]int{{i, 0}, {i, n - 1}, {0, i}, {n - 1, i}} {
			if labGray(ref, c.cx+p[0], c.cy+p[1]) != 255 {
				return true
			}
		}
	}
	return false
}

func xfLoadRef(t *testing.T) image.Image {
	fh, err := os.Open(xfPage(".png"))
	if err != nil {
		t.Skip("no reference capture ", xfPage(".png"))
	}
	defer fh.Close()
	ref, err := png.Decode(fh)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

type xfAgg struct{ ink, sum, g8, g24, max int }

func (a *xfAgg) add(b xfAgg) {
	a.ink += b.ink
	a.sum += b.sum
	a.g8 += b.g8
	a.g24 += b.g24
	if b.max > a.max {
		a.max = b.max
	}
}

func (a xfAgg) String() string {
	return fmt.Sprintf("ink %7d mean %5.2f max %3d  >8 %6.2f%%  >24 %6.2f%%", a.ink, float64(a.sum)/float64(max(1, a.ink)), a.max, 100*float64(a.g8)/float64(max(1, a.ink)), 100*float64(a.g24)/float64(max(1, a.ink)))
}

func TestXfLab(t *testing.T) {
	only := os.Getenv("XFLAB_GROUP")
	for _, g := range xfGroups {
		if only != "" && only != g.name {
			continue
		}
		t.Run(g.name, func(t *testing.T) {
			xfUse(g.name)
			ref := xfLoadRef(t)
			lut := lutForLum(0)
			by := map[string]*xfAgg{}
			tot := &xfAgg{}
			type worst struct {
				name string
				g8   int
			}
			var ws []worst
			for _, c := range xfCells() {
				face := xfLookup(c.face)
				if face == nil || xfBorderInk(ref, c) {
					continue // a neighbour's glyph spills into this cell
				}
				img, w, h := xfRender(c, face, lut)
				var st xfAgg
				for y := 0; y < h; y++ {
					for x := 0; x < w; x++ {
						a := img[y*w+x]
						r := labGray(ref, c.cx+x, c.cy+y)
						if a == 255 && r == 255 {
							continue
						}
						d := a - r
						if d < 0 {
							d = -d
						}
						st.ink++
						st.sum += d
						if d > 8 {
							st.g8++
						}
						if d > 24 {
							st.g24++
						}
						st.max = max(st.max, d)
					}
				}
				ws = append(ws, worst{fmt.Sprintf("%s/%g/%s/%c", c.face.name, c.size, c.tf.name, c.r), st.g8})
				for _, k := range []string{"tf " + c.tf.name, fmt.Sprintf("size %g", c.size), "face " + c.face.name} {
					if by[k] == nil {
						by[k] = &xfAgg{}
					}
					by[k].add(st)
				}
				tot.add(st)
			}
			var keys []string
			for k := range by {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			var sb strings.Builder
			for _, k := range keys {
				fmt.Fprintf(&sb, "%-22s %v\n", k, by[k])
			}
			fmt.Fprintf(&sb, "%-22s %v\n", "TOTAL", tot)
			if os.Getenv("XFLAB_V") != "" {
				sort.Slice(ws, func(i, j int) bool { return ws[i].g8 > ws[j].g8 })
				for i := 0; i < 25 && i < len(ws); i++ {
					fmt.Fprintf(&sb, "  %s %d\n", ws[i].name, ws[i].g8)
				}
			}
			t.Log("\n" + sb.String())
			if tot.ink == 0 {
				t.Fatal("no ink compared")
			}
			if f := float64(tot.g8) / float64(tot.ink); f > g.max8 {
				t.Errorf("share of glyph pixels off by more than 8/255: %.2f%% (limit %.2f%%)", 100*f, 100*g.max8)
			}
			if f := float64(tot.g24) / float64(tot.ink); f > g.max24 {
				t.Errorf("share of glyph pixels off by more than 24/255: %.2f%% (limit %.2f%%)", 100*f, 100*g.max24)
			}
		})
	}
}

// TestXfLabNums prints ours-ref coverage for XFLAB_NUMS=face/size/tf/glyph.
func TestXfLabNums(t *testing.T) {
	spec := os.Getenv("XFLAB_NUMS")
	if spec == "" {
		t.Skip()
	}
	xfUse(xfEnvGroup())
	ref := xfLoadRef(t)
	for _, c := range xfCells() {
		if fmt.Sprintf("%s/%g/%s/%c", c.face.name, c.size, c.tf.name, c.r) != spec {
			continue
		}
		img, w, h := xfRender(c, xfLookup(c.face), lutForLum(0))
		x0, y0, x1, y1 := w, h, 0, 0
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				if img[y*w+x] != 255 || labGray(ref, c.cx+x, c.cy+y) != 255 {
					x0, y0, x1, y1 = min(x0, x), min(y0, y), max(x1, x+1), max(y1, y+1)
				}
			}
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "\nours-ref coverage, origin %.3f,%.3f\n", c.e-float64(c.cx), c.f-float64(c.cy))
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				fmt.Fprintf(&sb, "%4d", (255-img[y*w+x])-(255-labGray(ref, c.cx+x, c.cy+y)))
			}
			sb.WriteString("\n")
		}
		t.Log(sb.String())
	}
}
