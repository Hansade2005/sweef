package raster

import (
	"fmt"
	"image/png"
	"os"
	"strings"
	"testing"
)

const zzSP = "/tmp/claude-0/-home-user-sweef/88880b6c-b9c0-5b8b-838a-110015e2aca6/scratchpad/"

func zz4Path(s qlShape, extra bool) *Path {
	p := qlPath(s)
	if extra {
		x, y := float64(int(s.x0/qlCell)*qlCell), float64(int(s.y0/qlCell)*qlCell)
		p.MoveTo(x+1, y+1)
		p.LineTo(x+4, y+1)
		p.LineTo(x+1, y+4)
		p.Close()
	}
	return p
}

func TestZZ4Gen(t *testing.T) {
	if os.Getenv("Z4GEN") == "" {
		t.Skip()
	}
	var b strings.Builder
	w, h := qlCols*qlCell, (qlN/qlCols)*qlCell
	fmt.Fprintf(&b, "<!doctype html><html><body style=\"margin:0;background:#fff\"><canvas id=c width=%d height=%d style=\"display:block\"></canvas><script>const c=document.getElementById('c').getContext('2d',{willReadFrequently:true});c.fillStyle='#fff';c.fillRect(0,0,%d,%d);c.fillStyle='#000';\n", w, h, w, h)
	for _, s := range qlShapes() {
		fmt.Fprintf(&b, "c.beginPath();%sc.fill();\n", qgJS(zz4Path(s, true)))
	}
	b.WriteString("</script></body></html>\n")
	os.WriteFile(zzSP+"z4.html", []byte(b.String()), 0o644)
}

func TestZZ4(t *testing.T) {
	fh, err := os.Open(zzSP + "z4.png")
	if err != nil {
		t.Skip()
	}
	ref, _ := png.Decode(fh)
	ink, n8 := 0, 0
	for i, s := range qlShapes() {
		cell := IRect{(i % qlCols) * qlCell, (i / qlCols) * qlCell, (i%qlCols)*qlCell + qlCell, (i/qlCols)*qlCell + qlCell}
		m := fxFillPath(zz4Path(s, true), cell)
		for y := cell.Y0; y < cell.Y1; y++ {
			for x := cell.X0; x < cell.X1; x++ {
				r, _, _, _ := ref.At(x, y).RGBA()
				w := 255 - int(r>>8)
				o := int(m.At(x, y))
				if o == 0 && w == 0 {
					continue
				}
				ink++
				if d := o - w; d > 8 || d < -8 {
					n8++
				}
			}
		}
	}
	fmt.Printf("quads+triangle: ink %d >8 %.2f%%\n", ink, 100*float64(n8)/float64(ink))
}
