package raster

import (
	"fmt"
	"image/png"
	"os"
	"strconv"
	"testing"
)

// exact coverage via fine flattening
func zzExact(s clShape, cell IRect) *Mask {
	r := newRasterizer(cell)
	defer r.release()
	pt := func(t float64) (float64, float64) {
		u := 1 - t
		x := u*u*u*s.p[0][0] + 3*u*u*t*s.p[1][0] + 3*u*t*t*s.p[2][0] + t*t*t*s.p[3][0]
		y := u*u*u*s.p[0][1] + 3*u*u*t*s.p[1][1] + 3*u*t*t*s.p[2][1] + t*t*t*s.p[3][1]
		return x, y
	}
	px, py := pt(0)
	for i := 1; i <= 2000; i++ {
		x, y := pt(float64(i) / 2000)
		r.line(px, py, x, y)
		px, py = x, y
	}
	r.line(px, py, s.p[0][0], s.p[0][1])
	return fxMask(r)
}

func TestZZExact(t *testing.T) {
	fh, _ := os.Open("testdata/glyphlab/cubics.png")
	ref, _ := png.Decode(fh)
	var tot [2]int
	var n8 [2]int
	ink := 0
	for i, s := range clShapes() {
		cell := IRect{(i % qlCols) * qlCell, (i / qlCols) * qlCell, (i%qlCols)*qlCell + qlCell, (i/qlCols)*qlCell + qlCell}
		ms := [2]*Mask{fxFillPath(clPath(s), cell), zzExact(s, cell)}
		for y := cell.Y0; y < cell.Y1; y++ {
			for x := cell.X0; x < cell.X1; x++ {
				r, _, _, _ := ref.At(x, y).RGBA()
				want := 255 - int(r>>8)
				if ms[0].At(x, y) == 0 && want == 0 {
					continue
				}
				ink++
				for k := 0; k < 2; k++ {
					d := int(ms[k].At(x, y)) - want
					if d < 0 {
						d = -d
					}
					tot[k] += d
					if d > 8 {
						n8[k]++
					}
				}
			}
		}
	}
	_ = strconv.Itoa
	fmt.Printf("ink %d skia-emu mean %.3f >8 %.2f%% ; exact mean %.3f >8 %.2f%%\n", ink, float64(tot[0])/float64(ink), 100*float64(n8[0])/float64(ink), float64(tot[1])/float64(ink), 100*float64(n8[1])/float64(ink))
}
