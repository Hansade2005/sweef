package raster

import (
	"fmt"
	"image/png"
	"math"
	"math/rand"
	"os"
	"strings"
	"testing"

	"github.com/sweefbrowser/sweef/internal/font"
)

// Quad lab: closed shapes made of one quadratic curve and a chord, drawn by
// Chromium's canvas (Skia AAA) and by fxFillPath. Used to study the coverage
// walk. QUADLAB_GEN=1 writes testdata/glyphlab/quads.html.

const (
	qlCell = 100
	qlCols = 8
	qlN    = 288
)

type qlShape struct {
	x0, y0, x1, y1, x2, y2 float64
	rr                     bool // rounded rectangle: (x0,y0)-(x1,y1) box, x2 radius
}

func qlShapes() []qlShape {
	r := rand.New(rand.NewSource(7))
	var out []qlShape
	for i := 0; i < qlN; i++ {
		ox, oy := float64((i%qlCols)*qlCell), float64((i/qlCols)*qlCell)
		f := func(lo, hi float64) float64 { return float64(int((lo+r.Float64()*(hi-lo))*100)) / 100 }
		s := qlShape{ox + f(8, 30), oy + f(8, 30), ox + f(10, 90), oy + f(10, 90), ox + f(60, 92), oy + f(60, 92), false}
		if i%3 == 1 { // flat-ended curve like glyph overshoots
			s.y1 = s.y2
		}
		if i%3 == 2 {
			s.x1 = s.x0
		}
		if i >= 96 { // shallow curves: control point close to the chord
			a := qlShape{ox + f(8, 40), oy + f(8, 90), 0, 0, ox + f(55, 92), oy + f(8, 90), false}
			mx, my := (a.x0+a.x2)/2, (a.y0+a.y2)/2
			dx, dy := a.x2-a.x0, a.y2-a.y0
			l := math.Hypot(dx, dy)
			off := f(0.5, 14)
			if i%2 == 0 {
				off = -off
			}
			a.x1, a.y1 = float64(int((mx-dy/l*off)*100))/100, float64(int((my+dx/l*off)*100))/100
			s = a
		}
		if i >= 192 { // rounded rectangles with small corner radii, corners built from two quads each
			x0 := ox + f(5, 30)
			y0 := oy + f(5, 30)
			s = qlShape{x0, y0, x0 + f(30, 65), y0 + f(30, 65), f(2, 14), 0, true}
		}
		out = append(out, s)
	}
	return out
}

// qlPath builds the Path for a shape.
func qlPath(s qlShape) *Path {
	p := NewPath()
	if !s.rr {
		p.MoveTo(s.x0, s.y0)
		p.QuadTo(s.x1, s.y1, s.x2, s.y2)
		p.Close()
		return p
	}
	l, t, r, b, rad := s.x0, s.y0, s.x1, s.y1, s.x2
	k := rad * 0.4142135623730951 // control point of a two-quad quarter circle
	_ = k
	p.MoveTo(l+rad, t)
	p.LineTo(r-rad, t)
	p.QuadTo(r-rad*0.4142, t, r-rad*0.2929, t+rad*0.2929)
	p.QuadTo(r, t+rad*0.4142, r, t+rad)
	p.LineTo(r, b-rad)
	p.QuadTo(r, b-rad*0.4142, r-rad*0.2929, b-rad*0.2929)
	p.QuadTo(r-rad*0.4142, b, r-rad, b)
	p.LineTo(l+rad, b)
	p.QuadTo(l+rad*0.4142, b, l+rad*0.2929, b-rad*0.2929)
	p.QuadTo(l, b-rad*0.4142, l, b-rad)
	p.LineTo(l, t+rad)
	p.QuadTo(l, t+rad*0.4142, l+rad*0.2929, t+rad*0.2929)
	p.QuadTo(l+rad*0.4142, t, l+rad, t)
	p.Close()
	return p
}

func qlJS(s qlShape) string {
	p := qlPath(s)
	var b strings.Builder
	i := 0
	for _, c := range p.Cmds {
		switch c {
		case 'M':
			fmt.Fprintf(&b, "c.moveTo(%v,%v);", p.Pts[i], p.Pts[i+1])
			i += 2
		case 'L':
			fmt.Fprintf(&b, "c.lineTo(%v,%v);", p.Pts[i], p.Pts[i+1])
			i += 2
		case 'Q':
			fmt.Fprintf(&b, "c.quadraticCurveTo(%v,%v,%v,%v);", p.Pts[i], p.Pts[i+1], p.Pts[i+2], p.Pts[i+3])
			i += 4
		}
	}
	return b.String()
}

func TestQuadLabGen(t *testing.T) {
	if os.Getenv("QUADLAB_GEN") == "" {
		t.Skip()
	}
	var b strings.Builder
	w, h := qlCols*qlCell, (qlN/qlCols)*qlCell
	fmt.Fprintf(&b, "<!doctype html><html><body style=\"margin:0;background:#fff\"><canvas id=c width=%d height=%d style=\"display:block\"></canvas><script>const c=document.getElementById('c').getContext('2d',{willReadFrequently:true});c.fillStyle='#fff';c.fillRect(0,0,%d,%d);c.fillStyle='#000';\n", w, h, w, h)
	for _, s := range qlShapes() {
		fmt.Fprintf(&b, "c.beginPath();%s;c.closePath();c.fill();\n", qlJS(s))
	}
	b.WriteString("</script></body></html>\n")
	os.WriteFile("testdata/glyphlab/quads.html", []byte(b.String()), 0o644)
	t.Logf("viewport %d x %d", w, h)
}

func TestQuadLab(t *testing.T) {
	fh, err := os.Open("testdata/glyphlab/quads.png")
	if err != nil {
		t.Skip("no quads.png")
	}
	ref, _ := png.Decode(fh)
	fh.Close()
	labApplyEnv()
	tot, n8, n24 := 0, 0, 0
	ink := 0
	for i, s := range qlShapes() {
		p := qlPath(s)
		cell := IRect{(i % qlCols) * qlCell, (i / qlCols) * qlCell, (i%qlCols)*qlCell + qlCell, (i/qlCols)*qlCell + qlCell}
		m := fxFillPath(p, cell)
		se, s8, s24, si := 0, 0, 0, 0
		for y := cell.Y0; y < cell.Y1; y++ {
			for x := cell.X0; x < cell.X1; x++ {
				ours := 0
				if m != nil {
					ours = int(m.At(x, y))
				}
				r, _, _, _ := ref.At(x, y).RGBA()
				want := 255 - int(r>>8)
				if ours == 0 && want == 0 {
					continue
				}
				d := ours - want
				if d < 0 {
					d = -d
				}
				si++
				se += d
				if d > 8 {
					s8++
				}
				if d > 24 {
					s24++
				}
			}
		}
		tot += se
		n8 += s8
		n24 += s24
		ink += si
		if os.Getenv("QUADLAB_V") != "" {
			fmt.Printf("shape %2d ink %5d mean %.2f >8 %d >24 %d  %+v\n", i, si, float64(se)/float64(max(si, 1)), s8, s24, s)
		}
	}
	t.Logf("quadlab: ink %d mean %.3f  >8 %.2f%%  >24 %.2f%%", ink, float64(tot)/float64(ink), 100*float64(n8)/float64(ink), 100*float64(n24)/float64(ink))
	if float64(tot)/float64(ink) > 0.5 || float64(n24)/float64(ink) > 0.001 {
		t.Errorf("quad shapes drift from Chromium's canvas AA")
	}
}

// glyph outlines drawn as canvas paths (padded with a far-away sliver so the
// path is large enough for Skia to choose analytic AA)
type qgSpec struct {
	face labFace
	r    rune
	size int
	ox   int
	oy   int
	sub  int
}

func qgSpecs() []qgSpec {
	var out []qgSpec
	faces := []labFace{labFaces[5], labFaces[6], labFaces[0], labFaces[3]}
	sizes := []int{12, 16, 24, 48}
	k := 0
	for _, f := range faces {
		for _, sz := range sizes {
			for _, r := range []rune("oOsegQa8") {
				out = append(out, qgSpec{f, r, sz, (k % 5) * 200, (k / 5) * 110, 0})
				k++
			}
		}
	}
	return out
}

var qgNoSliver = os.Getenv("QG_NOSLIVER") != ""

func qgPath(s qgSpec) *Path {
	face := font.Lookup(s.face.family, font.Style{Weight: s.face.weight, Italic: s.face.italic})
	p := fxGlyphPath(Glyph{R: s.r, Face: face}, float64(s.size), float64(s.size), 0, false)
	p = p.Transform(Translate(float64(s.ox+10), float64(s.oy+70)))
	if qgNoSliver {
		return p
	}
	// sliver far to the right so that the bounds are large
	sx := float64(s.ox + 150)
	sy := float64(s.oy + 10)
	p.MoveTo(sx, sy)
	p.LineTo(sx+1, sy)
	p.LineTo(sx+1, sy+2)
	p.LineTo(sx, sy+2)
	p.Close()
	return p
}

func qgJS(p *Path) string {
	var b strings.Builder
	i := 0
	for _, c := range p.Cmds {
		switch c {
		case 'M':
			fmt.Fprintf(&b, "c.moveTo(%v,%v);", p.Pts[i], p.Pts[i+1])
			i += 2
		case 'L':
			fmt.Fprintf(&b, "c.lineTo(%v,%v);", p.Pts[i], p.Pts[i+1])
			i += 2
		case 'Q':
			fmt.Fprintf(&b, "c.quadraticCurveTo(%v,%v,%v,%v);", p.Pts[i], p.Pts[i+1], p.Pts[i+2], p.Pts[i+3])
			i += 4
		case 'C':
			fmt.Fprintf(&b, "c.bezierCurveTo(%v,%v,%v,%v,%v,%v);", p.Pts[i], p.Pts[i+1], p.Pts[i+2], p.Pts[i+3], p.Pts[i+4], p.Pts[i+5])
			i += 6
		case 'Z':
			b.WriteString("c.closePath();")
		}
	}
	return b.String()
}

func TestGlyphPathLabGen(t *testing.T) {
	if os.Getenv("QUADLAB_GEN") == "" {
		t.Skip()
	}
	specs := qgSpecs()
	w, h := 1000, (len(specs)/5+1)*110
	var b strings.Builder
	fmt.Fprintf(&b, "<!doctype html><html><body style=\"margin:0;background:#fff\"><canvas id=c width=%d height=%d style=\"display:block\"></canvas><script>const c=document.getElementById('c').getContext('2d',{willReadFrequently:true});c.fillStyle='#fff';c.fillRect(0,0,%d,%d);c.fillStyle='#000';\n", w, h, w, h)
	for _, s := range specs {
		fmt.Fprintf(&b, "c.beginPath();%sc.fill();\n", qgJS(qgPath(s)))
	}
	b.WriteString("</script></body></html>\n")
	name := "testdata/glyphlab/gpaths.html"
	if qgNoSliver {
		name = "testdata/glyphlab/gpaths_ns.html"
	}
	os.WriteFile(name, []byte(b.String()), 0o644)
	t.Logf("viewport %d x %d", w, h)
}

func TestGlyphPathLab(t *testing.T) {
	fh, err := os.Open(qgPNG())
	if err != nil {
		t.Skip()
	}
	ref, _ := png.Decode(fh)
	fh.Close()
	labApplyEnv()
	tot, n8, n24, ink := 0, 0, 0, 0
	for i, s := range qgSpecs() {
		p := qgPath(s)
		cell := IRect{s.ox, s.oy, s.ox + 200, s.oy + 110}
		m := fxFillPath(p, cell)
		se, s8, s24, si := 0, 0, 0, 0
		for y := cell.Y0; y < cell.Y1; y++ {
			for x := cell.X0; x < cell.X0+140; x++ {
				ours := 0
				if m != nil {
					ours = int(m.At(x, y))
				}
				r, _, _, _ := ref.At(x, y).RGBA()
				want := 255 - int(r>>8)
				if ours == 0 && want == 0 {
					continue
				}
				d := ours - want
				if d < 0 {
					d = -d
				}
				si++
				se += d
				if d > 8 {
					s8++
				}
				if d > 24 {
					s24++
				}
			}
		}
		tot += se
		n8 += s8
		n24 += s24
		ink += si
		if os.Getenv("QUADLAB_V") != "" {
			fmt.Printf("gpath %3d %s/%d/%c ink %5d mean %.2f >8 %d >24 %d\n", i, s.face.name, s.size, s.r, si, float64(se)/float64(max(si, 1)), s8, s24)
		}
	}
	t.Logf("glyph paths (canvas AAA): ink %d mean %.3f  >8 %.2f%%  >24 %.2f%%", ink, float64(tot)/float64(ink), 100*float64(n8)/float64(ink), 100*float64(n24)/float64(ink))
	if float64(tot)/float64(ink) > 1.2 || float64(n24)/float64(ink) > 0.001 {
		t.Errorf("glyph outlines drift from Chromium's canvas AA")
	}
}

func qgPNG() string {
	if qgNoSliver {
		return "testdata/glyphlab/gpaths_ns.png"
	}
	return "testdata/glyphlab/gpaths.png"
}

// cubic lab: closed shapes of one cubic and a chord
type clShape struct{ p [4][2]float64 }

func clShapes() []clShape {
	r := rand.New(rand.NewSource(11))
	var out []clShape
	for i := 0; i < 144; i++ {
		ox, oy := float64((i%qlCols)*qlCell), float64((i/qlCols)*qlCell)
		f := func(lo, hi float64) float64 { return float64(int((lo+r.Float64()*(hi-lo))*100)) / 100 }
		var s clShape
		s.p[0] = [2]float64{ox + f(8, 30), oy + f(8, 92)}
		s.p[3] = [2]float64{ox + f(60, 92), oy + f(8, 92)}
		if i%3 == 0 { // S-shaped / wild controls
			s.p[1] = [2]float64{ox + f(5, 95), oy + f(5, 95)}
			s.p[2] = [2]float64{ox + f(5, 95), oy + f(5, 95)}
		} else { // gentle: controls near the chord
			for k := 1; k <= 2; k++ {
				t := float64(k) / 3
				x := s.p[0][0] + (s.p[3][0]-s.p[0][0])*t
				y := s.p[0][1] + (s.p[3][1]-s.p[0][1])*t
				s.p[k] = [2]float64{float64(int((x+f(-12, 12))*100)) / 100, float64(int((y+f(-12, 12))*100)) / 100}
			}
		}
		out = append(out, s)
	}
	return out
}

func clPath(s clShape) *Path {
	p := NewPath()
	p.MoveTo(s.p[0][0], s.p[0][1])
	p.CubicTo(s.p[1][0], s.p[1][1], s.p[2][0], s.p[2][1], s.p[3][0], s.p[3][1])
	p.Close()
	return p
}

func TestCubicLabGen(t *testing.T) {
	if os.Getenv("QUADLAB_GEN") == "" {
		t.Skip()
	}
	w, h := qlCols*qlCell, (len(clShapes())/qlCols)*qlCell
	var b strings.Builder
	fmt.Fprintf(&b, "<!doctype html><html><body style=\"margin:0;background:#fff\"><canvas id=c width=%d height=%d style=\"display:block\"></canvas><script>const c=document.getElementById('c').getContext('2d',{willReadFrequently:true});c.fillStyle='#fff';c.fillRect(0,0,%d,%d);c.fillStyle='#000';\n", w, h, w, h)
	for _, s := range clShapes() {
		fmt.Fprintf(&b, "c.beginPath();%sc.fill();\n", qgJS(clPath(s)))
	}
	b.WriteString("</script></body></html>\n")
	os.WriteFile("testdata/glyphlab/cubics.html", []byte(b.String()), 0o644)
	t.Logf("viewport %d x %d", w, h)
}

func TestCubicLab(t *testing.T) {
	fh, err := os.Open("testdata/glyphlab/cubics.png")
	if err != nil {
		t.Skip()
	}
	ref, _ := png.Decode(fh)
	fh.Close()
	labApplyEnv()
	tot, n8, n24, ink := 0, 0, 0, 0
	for i, s := range clShapes() {
		cell := IRect{(i % qlCols) * qlCell, (i / qlCols) * qlCell, (i%qlCols)*qlCell + qlCell, (i/qlCols)*qlCell + qlCell}
		m := fxFillPath(clPath(s), cell)
		se, s8, s24, si := 0, 0, 0, 0
		for y := cell.Y0; y < cell.Y1; y++ {
			for x := cell.X0; x < cell.X1; x++ {
				ours := 0
				if m != nil {
					ours = int(m.At(x, y))
				}
				r, _, _, _ := ref.At(x, y).RGBA()
				want := 255 - int(r>>8)
				if ours == 0 && want == 0 {
					continue
				}
				d := ours - want
				if d < 0 {
					d = -d
				}
				si++
				se += d
				if d > 8 {
					s8++
				}
				if d > 24 {
					s24++
				}
			}
		}
		tot += se
		n8 += s8
		n24 += s24
		ink += si
		if os.Getenv("QUADLAB_V") != "" {
			fmt.Printf("cubic %3d ink %5d mean %.2f >8 %d >24 %d\n", i, si, float64(se)/float64(max(si, 1)), s8, s24)
		}
	}
	t.Logf("cubics: ink %d mean %.3f  >8 %.2f%%  >24 %.2f%%", ink, float64(tot)/float64(ink), 100*float64(n8)/float64(ink), 100*float64(n24)/float64(ink))
	if float64(tot)/float64(ink) > 1.0 || float64(n24)/float64(ink) > 0.002 {
		t.Errorf("cubic shapes drift from Chromium's canvas AA")
	}
}

// stroked glyph outlines: canvas stroke() against fxStrokePath + the analytic
// scan converter
func strokeParams(i int) (w float64, join LineJoin, js string) {
	ws := []float64{1.2, 2, 3.5}
	w = ws[i%3]
	switch (i / 3) % 3 {
	case 1:
		return w, JoinRound, "round"
	case 2:
		return w, JoinBevel, "bevel"
	}
	return w, JoinMiter, "miter"
}

func TestStrokeLabGen(t *testing.T) {
	if os.Getenv("QUADLAB_GEN") == "" {
		t.Skip()
	}
	specs := qgSpecs()
	w, h := 1000, (len(specs)/5+1)*110
	var b strings.Builder
	fmt.Fprintf(&b, "<!doctype html><html><body style=\"margin:0;background:#fff\"><canvas id=c width=%d height=%d style=\"display:block\"></canvas><script>const c=document.getElementById('c').getContext('2d',{willReadFrequently:true});c.fillStyle='#fff';c.fillRect(0,0,%d,%d);c.strokeStyle='#000';c.miterLimit=4;\n", w, h, w, h)
	qgNoSliver = true
	for i, s := range specs {
		lw, _, js := strokeParams(i)
		fmt.Fprintf(&b, "c.lineJoin='%s';c.lineWidth=%v;c.beginPath();%sc.stroke();\n", js, lw, qgJS(qgPath(s)))
	}
	b.WriteString("</script></body></html>\n")
	os.WriteFile("testdata/glyphlab/strokes.html", []byte(b.String()), 0o644)
	t.Logf("viewport %d x %d", w, h)
}

func TestStrokeLab(t *testing.T) {
	fh, err := os.Open("testdata/glyphlab/strokes.png")
	if err != nil {
		t.Skip()
	}
	ref, _ := png.Decode(fh)
	fh.Close()
	qgNoSliver = true
	tot, n8, n24, ink := 0, 0, 0, 0
	for i, s := range qgSpecs() {
		lw, join, _ := strokeParams(i)
		p := qgPath(s)
		cell := IRect{s.ox, s.oy, s.ox + 200, s.oy + 110}
		var m *Mask
		if os.Getenv("STROKELAB_LEGACY") != "" {
			polys := StrokePolys(p.Flatten(0.02), StrokeStyle{Width: lw, Join: join, MiterLimit: 4}, 0.02)
			m = RasterizePolys(polys, NonZero, cell)
		} else {
			m = fxFillPath(fxStrokePath(p, lw, join, CapButt, 4, 1), cell)
		}
		se, s8, s24, si := 0, 0, 0, 0
		for y := cell.Y0; y < cell.Y1; y++ {
			for x := cell.X0; x < cell.X0+140; x++ {
				ours := 0
				if m != nil {
					ours = int(m.At(x, y))
				}
				r, _, _, _ := ref.At(x, y).RGBA()
				want := 255 - int(r>>8)
				if ours == 0 && want == 0 {
					continue
				}
				d := ours - want
				if d < 0 {
					d = -d
				}
				si++
				se += d
				if d > 8 {
					s8++
				}
				if d > 24 {
					s24++
				}
			}
		}
		tot += se
		n8 += s8
		n24 += s24
		ink += si
		if os.Getenv("QUADLAB_V") != "" {
			fmt.Printf("stroke %3d %s/%d/%c w%v ink %5d mean %.2f >8 %d >24 %d\n", i, s.face.name, s.size, s.r, lw, si, float64(se)/float64(max(si, 1)), s8, s24)
		}
	}
	t.Logf("stroked glyph paths: ink %d mean %.3f  >8 %.2f%%  >24 %.2f%%", ink, float64(tot)/float64(ink), 100*float64(n8)/float64(ink), 100*float64(n24)/float64(ink))
	if os.Getenv("STROKELAB_LEGACY") == "" && (float64(tot)/float64(ink) > 2.5 || float64(n24)/float64(ink) > 0.02) {
		t.Errorf("stroked outlines drift from Chromium's canvas stroker")
	}
}
