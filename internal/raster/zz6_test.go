package raster

import (
	"fmt"
	"image/png"
	"os"
	"testing"
)

func TestZZShift(t *testing.T) {
	fh, _ := os.Open("testdata/glyphlab/cubics.png")
	ref, _ := png.Decode(fh)
	type agg struct{ ink, n8, n24, se int }
	by := map[int]*agg{}
	for i, s := range clShapes() {
		cell := IRect{(i % qlCols) * qlCell, (i / qlCols) * qlCell, (i%qlCols)*qlCell + qlCell, (i/qlCols)*qlCell + qlCell}
		var x, y [4]int
		for k := 0; k < 4; k++ {
			x[k] = fxToFDot6x4(float32(s.p[k][0] - float64(cell.X0)))
			y[k] = fxToFDot6x4(float32(s.p[k][1] - float64(cell.Y0)))
		}
		sh := fxDiffToShift(fxCubicDelta(x[0], x[1], x[2], x[3]), fxCubicDelta(y[0], y[1], y[2], y[3])) + 1
		a := by[sh]
		if a == nil {
			a = &agg{}
			by[sh] = a
		}
		m := fxFillPath(clPath(s), cell)
		for yy := cell.Y0; yy < cell.Y1; yy++ {
			for xx := cell.X0; xx < cell.X1; xx++ {
				r, _, _, _ := ref.At(xx, yy).RGBA()
				want := 255 - int(r>>8)
				if m.At(xx, yy) == 0 && want == 0 {
					continue
				}
				d := int(m.At(xx, yy)) - want
				if d < 0 {
					d = -d
				}
				a.ink++
				a.se += d
				if d > 8 {
					a.n8++
				}
			}
		}
	}
	for sh, a := range by {
		fmt.Printf("shift %d ink %d mean %.2f >8 %.2f%%\n", sh, a.ink, float64(a.se)/float64(a.ink), 100*float64(a.n8)/float64(a.ink))
	}
}
