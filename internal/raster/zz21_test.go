package raster

import (
	"fmt"
	"image/png"
	"os"
	"strconv"
	"testing"
)

func TestZZRefDiff(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("CELL"))
	fh, _ := os.Open(zzSP + "z3.png")
	refN, _ := png.Decode(fh)
	fh2, _ := os.Open("testdata/glyphlab/cubics2.png")
	refC, _ := png.Decode(fh2)
	cells, _, _ := cl2Layout()
	c := cells[n]
	s := cl2Shapes()[n]
	var fb fxBuilder
	fxPathEdges(&fb, cl2Path(s, c), float64(c.X0), float64(c.Y0), false)
	slopeAt := func(y float64, wind int) float64 {
		for _, e := range fb.edges {
			if e.wind == wind && float64(e.y0)/65536 <= y && y < float64(e.y1)/65536 {
				return float64(e.slope) / 65536
			}
		}
		return 99
	}
	for y := c.Y0; y < c.Y1; y++ {
		var a, b float64
		for x := c.X0; x < c.X1; x++ {
			if x-c.X0 < 8 && y-c.Y0 < 8 {
				continue
			}
			r, _, _, _ := refN.At(x, y).RGBA()
			a += float64(255-int(r>>8)) / 255
			r, _, _, _ = refC.At(x, y).RGBA()
			b += float64(255-int(r>>8)) / 255
		}
		if b > 0 {
			fmt.Printf("y%3d convex %7.3f nonconvex-convex %+.3f slopes %.2f %.2f\n", y-c.Y0, b, a-b, slopeAt(float64(y-c.Y0)+0.5, 1), slopeAt(float64(y-c.Y0)+0.5, -1))
		}
	}
}
