package raster

import (
	"fmt"
	"image/png"
	"os"
	"strconv"
	"testing"
)

func TestZZ2Cells(t *testing.T) {
	fh, _ := os.Open("testdata/glyphlab/cubics2.png")
	ref, _ := png.Decode(fh)
	cells, _, _ := cl2Layout()
	want := os.Getenv("KIND")
	for i, s := range cl2Shapes() {
		if s.kind != want {
			continue
		}
		c := cells[i]
		m := fxFillPath(cl2Path(s, c), c)
		ink, n8, n24 := 0, 0, 0
		for y := c.Y0; y < c.Y1; y++ {
			for x := c.X0; x < c.X1; x++ {
				r, _, _, _ := ref.At(x, y).RGBA()
				w := 255 - int(r>>8)
				o := int(m.At(x, y))
				if o == 0 && w == 0 {
					continue
				}
				d := o - w
				if d < 0 {
					d = -d
				}
				ink++
				if d > 8 {
					n8++
				}
				if d > 24 {
					n24++
				}
			}
		}
		fmt.Printf("cell %d ink %d >8 %d >24 %d  %v\n", i, ink, n8, n24, s.p)
	}
	_ = strconv.Itoa
}
