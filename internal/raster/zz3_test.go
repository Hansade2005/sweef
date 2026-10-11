package raster

import (
	"fmt"
	"image/png"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestZZCol(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("CELL"))
	fh, _ := os.Open("testdata/glyphlab/cubics.png")
	ref, _ := png.Decode(fh)
	s := clShapes()[n]
	i := n
	cell := IRect{(i % qlCols) * qlCell, (i / qlCols) * qlCell, (i%qlCols)*qlCell + qlCell, (i/qlCols)*qlCell + qlCell}
	m := fxFillPath(clPath(s), cell)
	y0, _ := strconv.Atoi(os.Getenv("Y0"))
	y1, _ := strconv.Atoi(os.Getenv("Y1"))
	for _, cs := range strings.Split(os.Getenv("COLS"), ",") {
		c, _ := strconv.Atoi(cs)
		x := cell.X0 + c
		fmt.Printf("col %d:", c)
		for y := y0; y <= y1; y++ {
			yy := cell.Y0 + y
			r, _, _, _ := ref.At(x, yy).RGBA()
			fmt.Printf(" y%d %d/%d", y, m.At(x, yy), 255-int(r>>8))
		}
		fmt.Println()
	}
}
