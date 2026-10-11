package raster

import (
	"fmt"
	"image/png"
	"os"
	"strconv"
	"testing"
)

func TestZZRows2(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("CELL"))
	fh, _ := os.Open("testdata/glyphlab/cubics2.png")
	ref, _ := png.Decode(fh)
	cells, _, _ := cl2Layout()
	s := cl2Shapes()[n]
	c := cells[n]
	m := fxFillPath(cl2Path(s, c), c)
	ex := zzExact2(s, c)
	for y := c.Y0; y < c.Y1; y++ {
		var a, b, e float64
		for x := c.X0; x < c.X1; x++ {
			r, _, _, _ := ref.At(x, y).RGBA()
			a += float64(255-int(r>>8)) / 255
			b += float64(m.At(x, y)) / 255
			e += float64(ex.At(x, y)) / 255
		}
		if a > 0 || b > 0 {
			fmt.Printf("y%3d ref %7.3f ours %+.3f exact %+.3f\n", y-c.Y0, a, b-a, e-a)
		}
	}
}

func zzExact2(s cl2Shape, cell IRect) *Mask {
	r := newRasterizer(cell)
	defer r.release()
	ox, oy := float64(cell.X0), float64(cell.Y0)
	pt := func(t float64) (float64, float64) {
		u := 1 - t
		x := u*u*u*s.p[0][0] + 3*u*u*t*s.p[1][0] + 3*u*t*t*s.p[2][0] + t*t*t*s.p[3][0]
		y := u*u*u*s.p[0][1] + 3*u*u*t*s.p[1][1] + 3*u*t*t*s.p[2][1] + t*t*t*s.p[3][1]
		return x + ox, y + oy
	}
	px, py := pt(0)
	for i := 1; i <= 4000; i++ {
		x, y := pt(float64(i) / 4000)
		r.line(px, py, x, y)
		px, py = x, y
	}
	x0, y0 := pt(0)
	r.line(px, py, x0, y0)
	return fxMask(r)
}
