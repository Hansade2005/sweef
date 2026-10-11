package raster

import (
	"fmt"
	"image/png"
	"os"
	"strconv"
	"testing"
)

func zzPoly(s cl2Shape, cell IRect, n int) *Mask {
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
	for i := 1; i <= n; i++ {
		x, y := pt(float64(i) / float64(n))
		r.line(px, py, x, y)
		px, py = x, y
	}
	x0, y0 := pt(0)
	r.line(px, py, x0, y0)
	return fxMask(r)
}

func TestZZPoly(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("CELL"))
	fh, _ := os.Open("testdata/glyphlab/cubics2.png")
	ref, _ := png.Decode(fh)
	cells, _, _ := cl2Layout()
	s := cl2Shapes()[n]
	c := cells[n]
	for _, N := range []int{2, 3, 4, 6, 8, 12, 16, 24, 32, 64, 4000} {
		m := zzPoly(s, c, N)
		var se float64
		n8 := 0
		for y := c.Y0; y < c.Y1; y++ {
			for x := c.X0; x < c.X1; x++ {
				r, _, _, _ := ref.At(x, y).RGBA()
				d := float64(int(m.At(x, y)) - (255 - int(r>>8)))
				if d < 0 {
					d = -d
				}
				se += d
				if d > 8 {
					n8++
				}
			}
		}
		fmt.Printf("N=%d err %.1f n8 %d\n", N, se, n8)
	}
}
