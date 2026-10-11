package raster

import (
	"fmt"
	"image/png"
	"os"
	"strconv"
	"testing"
)

func TestZZ3Cell(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("CELL"))
	fh, _ := os.Open(zzSP + "z3.png")
	ref, _ := png.Decode(fh)
	cells, _, _ := cl2Layout()
	s := cl2Shapes()[n]
	c := cells[n]
	fmt.Println(s.kind, s.p)
	m := fxFillPath(zz3Path(s, c, true), c)
	var fb fxBuilder
	fxPathEdges(&fb, zz3Path(s, c, true), float64(c.X0), float64(c.Y0), false)
	for _, e := range fb.edges {
		xe := e.x + fxMul(e.slope, e.y1-e.y0)
		fmt.Printf("w%2d x %8.3f y %8.3f -> x %8.3f y %8.3f\n", e.wind, float64(e.x)/65536, float64(e.y0)/65536, float64(xe)/65536, float64(e.y1)/65536)
	}
	for y := c.Y0; y < c.Y1; y++ {
		line := ""
		var sa, sb float64
		for x := c.X0; x < c.X1; x++ {
			r, _, _, _ := ref.At(x, y).RGBA()
			w := 255 - int(r>>8)
			o := int(m.At(x, y))
			sa += float64(w) / 255
			sb += float64(o) / 255
			if d := o - w; d > 8 || d < -8 {
				line += fmt.Sprintf(" %d:%d/%d", x-c.X0, o, w)
			}
		}
		if sa > 0 {
			fmt.Printf("y%d rowsum diff %+.3f%s\n", y-c.Y0, sb-sa, line)
		}
	}
}
