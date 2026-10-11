package raster

import (
	"fmt"
	"image/png"
	"os"
	"strconv"
	"testing"
)

func TestZZ5Rows(t *testing.T) {
	k, _ := strconv.Atoi(os.Getenv("K"))
	fh, _ := os.Open(zzSP + "z5.png")
	ref, _ := png.Decode(fh)
	cs := z5Cells()
	var cc, cn z5cell
	n := 0
	for _, c := range cs {
		if c.kind == "cubic" && !c.extra {
			if n == k {
				cc = c
			}
		}
		if c.kind == "cubic" && c.extra {
			if n == k {
				cn = c
			}
			n++
		}
	}
	fmt.Println(cc.s.p)
	var fb fxBuilder
	fxPathEdges(&fb, z5Path(cc), float64(cc.ox), float64(cc.oy), false)
	for _, e := range fb.edges {
		xe := e.x + fxMul(e.slope, e.y1-e.y0)
		fmt.Printf("w%2d x %8.3f y %8.3f -> x %8.3f y %8.3f\n", e.wind, float64(e.x)/65536, float64(e.y0)/65536, float64(xe)/65536, float64(e.y1)/65536)
	}
	m := fxFillPath(z5Path(cc), IRect{cc.ox, cc.oy, cc.ox + 256, cc.oy + 256})
	for y := 0; y < 256; y++ {
		var a, b, o float64
		for x := 0; x < 256; x++ {
			if x < 8 && y < 8 {
				continue
			}
			r, _, _, _ := ref.At(cc.ox+x, cc.oy+y).RGBA()
			a += float64(255-int(r>>8)) / 255
			r, _, _, _ = ref.At(cn.ox+x, cn.oy+y).RGBA()
			b += float64(255-int(r>>8)) / 255
			o += float64(m.At(cc.ox+x, cc.oy+y)) / 255
		}
		if a > 0 {
			fmt.Printf("y%3d convex %7.3f  nonconvex %+.3f ours %+.3f\n", y, a, b-a, o-a)
		}
	}
}
