package raster

import (
	"fmt"
	"image/png"
	"os"
	"testing"
	"strconv"
)

func TestZZCell(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("CELL"))
	fh, _ := os.Open("testdata/glyphlab/cubics.png")
	ref, _ := png.Decode(fh)
	s := clShapes()[n]
	fmt.Println(s.p)
	i := n
	cell := IRect{(i % qlCols) * qlCell, (i / qlCols) * qlCell, (i%qlCols)*qlCell + qlCell, (i/qlCols)*qlCell + qlCell}
	m := fxFillPath(clPath(s), cell)
	for y := cell.Y0; y < cell.Y1; y++ {
		line := ""
		any := false
		for x := cell.X0; x < cell.X1; x++ {
			ours := 0
			if m != nil {
				ours = int(m.At(x, y))
			}
			r, _, _, _ := ref.At(x, y).RGBA()
			want := 255 - int(r>>8)
			d := ours - want
			if d > 8 || d < -8 {
				line += fmt.Sprintf(" %d:%d/%d", x-cell.X0, ours, want)
				any = true
			}
		}
		if any {
			fmt.Printf("y%d%s\n", y-cell.Y0, line)
		}
	}
}
