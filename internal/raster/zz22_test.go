package raster

import (
	"fmt"
	"image/png"
	"os"
	"strings"
	"testing"
)

type z5cell struct {
	kind   string
	extra  bool
	s      cl2Shape
	ox, oy int
}

func z5Cells() []z5cell {
	var arcs []cl2Shape
	for _, s := range cl2Shapes() {
		if s.kind == "arc256" && len(arcs) < 12 {
			arcs = append(arcs, s)
		}
	}
	var out []z5cell
	k := 0
	for _, a := range arcs {
		for _, kind := range []string{"cubic", "quad", "poly8", "poly32"} {
			for _, ex := range []bool{false, true} {
				out = append(out, z5cell{kind, ex, a, (k % 4) * 256, (k / 4) * 256})
				k++
			}
		}
	}
	return out
}

func z5Path(c z5cell) *Path {
	p := NewPath()
	ox, oy := float64(c.ox), float64(c.oy)
	P := c.s.p
	at := func(t float64) (float64, float64) {
		u := 1 - t
		x := u*u*u*P[0][0] + 3*u*u*t*P[1][0] + 3*u*t*t*P[2][0] + t*t*t*P[3][0]
		y := u*u*u*P[0][1] + 3*u*u*t*P[1][1] + 3*u*t*t*P[2][1] + t*t*t*P[3][1]
		return float64(int((x+ox)*100)) / 100, float64(int((y+oy)*100)) / 100
	}
	switch c.kind {
	case "cubic":
		p.MoveTo(P[0][0]+ox, P[0][1]+oy)
		p.CubicTo(P[1][0]+ox, P[1][1]+oy, P[2][0]+ox, P[2][1]+oy, P[3][0]+ox, P[3][1]+oy)
	case "quad":
		// control point: cubic's midpoint-matching quad: q = (3*(p1+p2) - (p0+p3))/4
		qx := (3*(P[1][0]+P[2][0]) - (P[0][0] + P[3][0])) / 4
		qy := (3*(P[1][1]+P[2][1]) - (P[0][1] + P[3][1])) / 4
		p.MoveTo(P[0][0]+ox, P[0][1]+oy)
		p.QuadTo(qx+ox, qy+oy, P[3][0]+ox, P[3][1]+oy)
	default:
		n := 8
		if c.kind == "poly32" {
			n = 32
		}
		x, y := at(0)
		p.MoveTo(x, y)
		for i := 1; i <= n; i++ {
			x, y = at(float64(i) / float64(n))
			p.LineTo(x, y)
		}
	}
	p.Close()
	if c.extra {
		p.MoveTo(ox+1, oy+1)
		p.LineTo(ox+4, oy+1)
		p.LineTo(ox+1, oy+4)
		p.Close()
	}
	return p
}

func TestZZ5Gen(t *testing.T) {
	if os.Getenv("Z5GEN") == "" {
		t.Skip()
	}
	cs := z5Cells()
	w, h := 1024, (len(cs)/4+1)*256
	var b strings.Builder
	fmt.Fprintf(&b, "<!doctype html><html><body style=\"margin:0;background:#fff\"><canvas id=c width=%d height=%d style=\"display:block\"></canvas><script>const c=document.getElementById('c').getContext('2d',{willReadFrequently:true});c.fillStyle='#fff';c.fillRect(0,0,%d,%d);c.fillStyle='#000';\n", w, h, w, h)
	for _, s := range cs {
		fmt.Fprintf(&b, "c.beginPath();%sc.fill();\n", qgJS(z5Path(s)))
	}
	b.WriteString("</script></body></html>\n")
	os.WriteFile(zzSP+"z5.html", []byte(b.String()), 0o644)
	fmt.Println("viewport", w, h)
}

func TestZZ5(t *testing.T) {
	fh, err := os.Open(zzSP + "z5.png")
	if err != nil {
		t.Skip()
	}
	ref, _ := png.Decode(fh)
	cs := z5Cells()
	type agg struct{ ink, se, n8 int }
	ours := map[string]*agg{}
	cmp := map[string]*agg{} // convex vs nonconvex Chromium
	for i, c := range cs {
		cell := IRect{c.ox, c.oy, c.ox + 256, c.oy + 256}
		m := fxFillPath(z5Path(c), cell)
		key := c.kind
		if c.extra {
			key += "+tri"
		}
		a := ours[key]
		if a == nil {
			a = &agg{}
			ours[key] = a
		}
		var other IRect
		if c.extra {
			other = IRect{cs[i-1].ox, cs[i-1].oy, cs[i-1].ox + 256, cs[i-1].oy + 256}
		}
		b := cmp[c.kind]
		if b == nil {
			b = &agg{}
			cmp[c.kind] = b
		}
		for y := 0; y < 256; y++ {
			for x := 0; x < 256; x++ {
				if x < 8 && y < 8 {
					continue
				}
				r, _, _, _ := ref.At(cell.X0+x, cell.Y0+y).RGBA()
				w := 255 - int(r>>8)
				o := int(m.At(cell.X0+x, cell.Y0+y))
				if o == 0 && w == 0 {
					continue
				}
				a.ink++
				d := o - w
				if d < 0 {
					d = -d
				}
				a.se += d
				if d > 8 {
					a.n8++
				}
				if c.extra {
					r2, _, _, _ := ref.At(other.X0+x, other.Y0+y).RGBA()
					d2 := w - (255 - int(r2>>8))
					if d2 < 0 {
						d2 = -d2
					}
					b.ink++
					b.se += d2
					if d2 > 8 {
						b.n8++
					}
				}
			}
		}
	}
	for k, a := range ours {
		fmt.Printf("ours %-10s ink %6d mean %.3f >8 %.2f%%\n", k, a.ink, float64(a.se)/float64(a.ink), 100*float64(a.n8)/float64(a.ink))
	}
	for k, a := range cmp {
		fmt.Printf("chromium convex-vs-nonconvex %-8s ink %6d mean %.3f >8 %.2f%%\n", k, a.ink, float64(a.se)/float64(max(a.ink, 1)), 100*float64(a.n8)/float64(max(a.ink, 1)))
	}
}
