package raster

import (
	"fmt"
	"image/png"
	"os"
	"strings"
	"testing"
)

func zz3Path(s cl2Shape, c IRect, extra bool) *Path {
	p := cl2Path(s, c)
	if extra {
		x, y := float64(c.X0), float64(c.Y0)
		p.MoveTo(x+1, y+1)
		p.LineTo(x+4, y+1)
		p.LineTo(x+1, y+4)
		p.Close()
	}
	return p
}

func TestZZ3Gen(t *testing.T) {
	if os.Getenv("Z3GEN") == "" {
		t.Skip()
	}
	cells, w, h := cl2Layout()
	var b strings.Builder
	fmt.Fprintf(&b, "<!doctype html><html><body style=\"margin:0;background:#fff\"><canvas id=c width=%d height=%d style=\"display:block\"></canvas><script>const c=document.getElementById('c').getContext('2d',{willReadFrequently:true});c.fillStyle='#fff';c.fillRect(0,0,%d,%d);c.fillStyle='#000';\n", w, h, w, h)
	for i, s := range cl2Shapes() {
		fmt.Fprintf(&b, "c.beginPath();%sc.fill();\n", qgJS(zz3Path(s, cells[i], true)))
	}
	b.WriteString("</script></body></html>\n")
	os.WriteFile("/tmp/claude-0/-home-user-sweef/88880b6c-b9c0-5b8b-838a-110015e2aca6/scratchpad/z3.html", []byte(b.String()), 0o644)
}

func TestZZ3(t *testing.T) {
	fh, err := os.Open("/tmp/claude-0/-home-user-sweef/88880b6c-b9c0-5b8b-838a-110015e2aca6/scratchpad/z3.png")
	if err != nil {
		t.Skip()
	}
	ref, _ := png.Decode(fh)
	cells, _, _ := cl2Layout()
	type agg struct{ ink, n8 int }
	by := map[string]*agg{}
	for i, s := range cl2Shapes() {
		c := cells[i]
		m := fxFillPath(zz3Path(s, c, true), c)
		a := by[s.kind]
		if a == nil {
			a = &agg{}
			by[s.kind] = a
		}
		for y := c.Y0; y < c.Y1; y++ {
			for x := c.X0; x < c.X1; x++ {
				r, _, _, _ := ref.At(x, y).RGBA()
				w := 255 - int(r>>8)
				o := int(m.At(x, y))
				if (x-c.X0 < 8 && y-c.Y0 < 8) || (o == 0 && w == 0) {
					continue
				}
				a.ink++
				if d := o - w; d > 8 || d < -8 {
					a.n8++
				}
			}
		}
	}
	for k, a := range by {
		fmt.Printf("%s ink %d >8 %.2f%%\n", k, a.ink, 100*float64(a.n8)/float64(a.ink))
	}
}
