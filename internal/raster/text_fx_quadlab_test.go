package raster

import (
	"fmt"
	"image/png"
	"math"
	"math/rand"
	"os"
	"strings"
	"testing"
)

// Quad lab: closed shapes made of one quadratic curve and a chord, drawn by
// Chromium's canvas (Skia AAA) and by fxFillPath. Used to study the coverage
// walk. QUADLAB_GEN=1 writes testdata/glyphlab/quads.html.

const (
	qlCell = 100
	qlCols = 8
	qlN    = 192
)

type qlShape struct{ x0, y0, x1, y1, x2, y2 float64 }

func qlShapes() []qlShape {
	r := rand.New(rand.NewSource(7))
	var out []qlShape
	for i := 0; i < qlN; i++ {
		ox, oy := float64((i%qlCols)*qlCell), float64((i/qlCols)*qlCell)
		f := func(lo, hi float64) float64 { return float64(int((lo+r.Float64()*(hi-lo))*100)) / 100 }
		s := qlShape{ox + f(8, 30), oy + f(8, 30), ox + f(10, 90), oy + f(10, 90), ox + f(60, 92), oy + f(60, 92)}
		if i%3 == 1 { // flat-ended curve like glyph overshoots
			s.y1 = s.y2
		}
		if i%3 == 2 {
			s.x1 = s.x0
		}
		if i >= 96 { // shallow curves: control point close to the chord
			a := qlShape{ox + f(8, 40), oy + f(8, 90), 0, 0, ox + f(55, 92), oy + f(8, 90)}
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
		out = append(out, s)
	}
	return out
}

func TestQuadLabGen(t *testing.T) {
	if os.Getenv("QUADLAB_GEN") == "" {
		t.Skip()
	}
	var b strings.Builder
	w, h := qlCols*qlCell, (qlN/qlCols)*qlCell
	fmt.Fprintf(&b, "<!doctype html><html><body style=\"margin:0;background:#fff\"><canvas id=c width=%d height=%d style=\"display:block\"></canvas><script>const c=document.getElementById('c').getContext('2d',{willReadFrequently:true});c.fillStyle='#fff';c.fillRect(0,0,%d,%d);c.fillStyle='#000';\n", w, h, w, h)
	for _, s := range qlShapes() {
		fmt.Fprintf(&b, "c.beginPath();c.moveTo(%v,%v);c.quadraticCurveTo(%v,%v,%v,%v);c.closePath();c.fill();\n", s.x0, s.y0, s.x1, s.y1, s.x2, s.y2)
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
		p := NewPath()
		p.MoveTo(s.x0, s.y0)
		p.QuadTo(s.x1, s.y1, s.x2, s.y2)
		p.Close()
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
}
