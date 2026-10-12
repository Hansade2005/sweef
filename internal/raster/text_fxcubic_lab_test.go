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

// Cubic lab 2: closed shapes made of one cubic (plus the chord), drawn by
// Chromium's canvas and by fxFillPath, grouped by kind and size. Regenerate
// the page with CUBICLAB_GEN=1 go test -run CubicLab2Gen, capture it at the
// printed viewport into testdata/glyphlab/cubics2.png.

type cl2Shape struct {
	kind string
	cell int
	p    [4][2]float64
}

const cl2Cols = 6

func cl2Shapes() []cl2Shape {
	r := rand.New(rand.NewSource(7))
	var out []cl2Shape
	q := func(v float64) float64 { return math.Floor(v*100) / 100 }
	rnd := func(lo, hi float64) float64 { return q(lo + r.Float64()*(hi-lo)) }
	add := func(kind string, cell int, pts ...[2]float64) {
		var s cl2Shape
		s.kind, s.cell = kind, cell
		for i := range s.p {
			s.p[i] = [2]float64{q(pts[i][0]), q(pts[i][1])}
		}
		out = append(out, s)
	}
	for _, cell := range []int{64, 128, 256} {
		c := float64(cell)
		n := 24
		if cell == 256 {
			n = 12
		}
		for i := 0; i < n; i++ {
			// circular arc by one cubic, random radius, start angle, sweep
			rad := rnd(c*0.1, c*0.42)
			a0 := rnd(0, 6.28)
			sw := rnd(0.5, 1.57)
			k := 4.0 / 3 * math.Tan(sw/4)
			cx, cy := c/2+rnd(-2, 2), c/2+rnd(-2, 2)
			pt := func(a float64) (x, y, tx, ty float64) {
				return cx + rad*math.Cos(a), cy + rad*math.Sin(a), -math.Sin(a), math.Cos(a)
			}
			x0, y0, tx0, ty0 := pt(a0)
			x3, y3, tx1, ty1 := pt(a0 + sw)
			add(fmt.Sprintf("arc%d", cell), cell, [2]float64{x0, y0}, [2]float64{x0 + k*rad*tx0, y0 + k*rad*ty0}, [2]float64{x3 - k*rad*tx1, y3 - k*rad*ty1}, [2]float64{x3, y3})
		}
		for i := 0; i < n; i++ { // S curves
			p0 := [2]float64{rnd(c*0.1, c*0.25), rnd(c*0.2, c*0.8)}
			p3 := [2]float64{rnd(c*0.75, c*0.9), rnd(c*0.2, c*0.8)}
			add(fmt.Sprintf("S%d", cell), cell, p0, [2]float64{rnd(c*0.3, c*0.6), rnd(c*0.0, c*0.3)}, [2]float64{rnd(c*0.4, c*0.7), rnd(c*0.7, c*1.0)}, p3)
		}
		for i := 0; i < n; i++ { // loops
			p0 := [2]float64{rnd(c*0.3, c*0.5), rnd(c*0.1, c*0.3)}
			p3 := [2]float64{rnd(c*0.5, c*0.7), rnd(c*0.1, c*0.3)}
			add(fmt.Sprintf("loop%d", cell), cell, p0, [2]float64{rnd(c*0.7, c*1.0), rnd(c*0.7, c*1.0)}, [2]float64{rnd(c*0.0, c*0.3), rnd(c*0.7, c*1.0)}, p3)
		}
		for i := 0; i < n; i++ { // near-lines
			p0 := [2]float64{rnd(c*0.1, c*0.3), rnd(c*0.1, c*0.9)}
			p3 := [2]float64{rnd(c*0.7, c*0.9), rnd(c*0.1, c*0.9)}
			var m [2][2]float64
			for k := 0; k < 2; k++ {
				t := float64(k+1) / 3
				m[k] = [2]float64{p0[0] + (p3[0]-p0[0])*t + rnd(-1, 1), p0[1] + (p3[1]-p0[1])*t + rnd(-1, 1)}
			}
			add(fmt.Sprintf("line%d", cell), cell, p0, m[0], m[1], p3)
		}
		for i := 0; i < n/2; i++ { // cusps: controls swapped past each other
			p0 := [2]float64{rnd(c*0.15, c*0.3), rnd(c*0.6, c*0.9)}
			p3 := [2]float64{rnd(c*0.7, c*0.85), rnd(c*0.6, c*0.9)}
			top := rnd(c*0.05, c*0.3)
			mx := (p0[0] + p3[0]) / 2
			d := rnd(c*0.2, c*0.4)
			add(fmt.Sprintf("cusp%d", cell), cell, p0, [2]float64{mx + d, top}, [2]float64{mx - d, top}, p3)
		}
		for i := 0; i < n; i++ { // vertical-ish extrema (steep)
			p0 := [2]float64{rnd(c*0.3, c*0.7), rnd(c*0.05, c*0.2)}
			p3 := [2]float64{rnd(c*0.3, c*0.7), rnd(c*0.8, c*0.95)}
			add(fmt.Sprintf("steep%d", cell), cell, p0, [2]float64{rnd(c*0.0, c*1.0), rnd(c*0.2, c*0.5)}, [2]float64{rnd(c*0.0, c*1.0), rnd(c*0.5, c*0.8)}, p3)
		}
	}
	return out
}

// cl2Layout places the shapes in rows of cl2Cols; each cell group has its own size
func cl2Layout() (cells []IRect, w, h int) {
	shapes := cl2Shapes()
	x, y, rowH := 0, 0, 0
	maxW := 0
	for _, s := range shapes {
		if x+s.cell > cl2Cols*128 {
			x, y = 0, y+rowH
			rowH = 0
		}
		cells = append(cells, IRect{x, y, x + s.cell, y + s.cell})
		x += s.cell
		rowH = max(rowH, s.cell)
		maxW = max(maxW, x)
	}
	return cells, cl2Cols * 128, y + rowH
}

func cl2Path(s cl2Shape, c IRect) *Path {
	p := NewPath()
	ox, oy := float64(c.X0), float64(c.Y0)
	p.MoveTo(s.p[0][0]+ox, s.p[0][1]+oy)
	p.CubicTo(s.p[1][0]+ox, s.p[1][1]+oy, s.p[2][0]+ox, s.p[2][1]+oy, s.p[3][0]+ox, s.p[3][1]+oy)
	p.Close()
	return p
}

func TestCubicLab2Gen(t *testing.T) {
	if os.Getenv("CUBICLAB_GEN") == "" {
		t.Skip()
	}
	cells, w, h := cl2Layout()
	var b strings.Builder
	fmt.Fprintf(&b, "<!doctype html><html><body style=\"margin:0;background:#fff\"><canvas id=c width=%d height=%d style=\"display:block\"></canvas><script>const c=document.getElementById('c').getContext('2d',{willReadFrequently:true});c.fillStyle='#fff';c.fillRect(0,0,%d,%d);c.fillStyle='#000';\n", w, h, w, h)
	for i, s := range cl2Shapes() {
		fmt.Fprintf(&b, "c.beginPath();%sc.fill();\n", qgJS(cl2Path(s, cells[i])))
	}
	b.WriteString("</script></body></html>\n")
	os.WriteFile("testdata/glyphlab/cubics2.html", []byte(b.String()), 0o644)
	t.Logf("viewport %d x %d", w, h)
}

func TestCubicLab2(t *testing.T) {
	fh, err := os.Open("testdata/glyphlab/cubics2.png")
	if err != nil {
		t.Skip()
	}
	ref, _ := png.Decode(fh)
	fh.Close()
	cells, _, _ := cl2Layout()
	type agg struct{ ink, se, n8, n24 int }
	by := map[string]*agg{}
	var kinds []string
	var all agg
	for i, s := range cl2Shapes() {
		c := cells[i]
		m := fxFillPath(cl2Path(s, c), c)
		a := by[s.kind]
		if a == nil {
			a = &agg{}
			by[s.kind] = a
			kinds = append(kinds, s.kind)
		}
		for y := c.Y0; y < c.Y1; y++ {
			for x := c.X0; x < c.X1; x++ {
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
				for _, g := range []*agg{a, &all} {
					g.ink++
					g.se += d
					if d > 8 {
						g.n8++
					}
					if d > 24 {
						g.n24++
					}
				}
			}
		}
	}
	for _, k := range kinds {
		a := by[k]
		t.Logf("%-8s ink %6d mean %.3f >8 %5.2f%% >24 %5.2f%%", k, a.ink, float64(a.se)/float64(a.ink), 100*float64(a.n8)/float64(a.ink), 100*float64(a.n24)/float64(a.ink))
	}
	t.Logf("ALL      ink %6d mean %.3f >8 %5.2f%% >24 %5.2f%%", all.ink, float64(all.se)/float64(all.ink), 100*float64(all.n8)/float64(all.ink), 100*float64(all.n24)/float64(all.ink))
	if float64(all.se)/float64(all.ink) > 0.6 || float64(all.n8)/float64(all.ink) > 0.004 || float64(all.n24)/float64(all.ink) > 0.001 {
		t.Errorf("cubic shapes drift from Chromium's canvas AA")
	}
}

func TestPathIsConvex(t *testing.T) {
	cub := NewPath()
	cub.MoveTo(0, 0)
	cub.CubicTo(10, 20, 30, 20, 40, 0) // arch and chord: convex
	cub.Close()
	s := NewPath()
	s.MoveTo(0, 0)
	s.CubicTo(10, 20, 30, -20, 40, 0) // S curve: the control polygon turns both ways
	s.Close()
	two := NewPath()
	two.MoveTo(0, 0)
	two.LineTo(10, 0)
	two.LineTo(10, 10)
	two.Close()
	two.MoveTo(20, 0)
	two.LineTo(30, 0)
	two.LineTo(30, 10)
	two.Close()
	for _, c := range []struct {
		name string
		p    *Path
		want bool
	}{{"arch", cub, true}, {"S", s, false}, {"two contours", two, false}} {
		if got := pathIsConvex(c.p); got != c.want {
			t.Errorf("%s: convex %v, want %v", c.name, got, c.want)
		}
	}
}
