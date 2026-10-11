package raster

import (
	"fmt"
	"image/png"
	"os"
	"strconv"
	"testing"
)

func TestZZRows(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("CELL"))
	fh, _ := os.Open("testdata/glyphlab/cubics.png")
	ref, _ := png.Decode(fh)
	s := clShapes()[n]
	i := n
	cell := IRect{(i % qlCols) * qlCell, (i / qlCols) * qlCell, (i%qlCols)*qlCell + qlCell, (i/qlCols)*qlCell + qlCell}
	m := fxFillPath(clPath(s), cell)
	ex := zzExact(s, cell)
	for y := 0; y < 100; y++ {
		var a, b, c float64
		for x := 0; x < 100; x++ {
			r, _, _, _ := ref.At(cell.X0+x, cell.Y0+y).RGBA()
			a += float64(255-int(r>>8)) / 255
			b += float64(m.At(cell.X0+x, cell.Y0+y)) / 255
			c += float64(ex.At(cell.X0+x, cell.Y0+y)) / 255
		}
		if a > 0 || b > 0 {
			fmt.Printf("y%3d ref %7.3f ours %7.3f (%+.3f) exact %7.3f (%+.3f)\n", y, a, b, b-a, c, c-a)
		}
	}
}
