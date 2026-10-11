package raster

import (
	"fmt"
	"image/png"
	"os"
	"testing"
)

func TestZZ3Per(t *testing.T) {
	fh, _ := os.Open(zzSP + "z3.png")
	ref, _ := png.Decode(fh)
	fh2, _ := os.Open("testdata/glyphlab/cubics2.png")
	ref0, _ := png.Decode(fh2)
	cells, _, _ := cl2Layout()
	for i, s := range cl2Shapes() {
		if s.kind != os.Getenv("KIND") {
			continue
		}
		c := cells[i]
		m := fxFillPath(zz3Path(s, c, true), c)
		m0 := fxFillPath(cl2Path(s, c), c)
		ink, n8, n80 := 0, 0, 0
		for y := c.Y0; y < c.Y1; y++ {
			for x := c.X0; x < c.X1; x++ {
				if x-c.X0 < 8 && y-c.Y0 < 8 {
					continue
				}
				r, _, _, _ := ref.At(x, y).RGBA()
				w := 255 - int(r>>8)
				r0, _, _, _ := ref0.At(x, y).RGBA()
				w0 := 255 - int(r0>>8)
				if m.At(x, y) == 0 && w == 0 {
					continue
				}
				ink++
				if d := int(m.At(x, y)) - w; d > 8 || d < -8 {
					n8++
				}
				if d := int(m0.At(x, y)) - w0; d > 8 || d < -8 {
					n80++
				}
			}
		}
		fmt.Printf("cell %d ink %d >8 with %d without %d %v\n", i, ink, n8, n80, s.p)
	}
}
