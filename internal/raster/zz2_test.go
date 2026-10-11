package raster

import (
	"fmt"
	"os"
	"strconv"
	"testing"
)

func TestZZEdges(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("CELL"))
	s := clShapes()[n]
	i := n
	ox, oy := float64((i%qlCols)*qlCell), float64((i/qlCols)*qlCell)
	var fb fxBuilder
	fxPathEdges(&fb, clPath(s), ox, oy, false)
	for _, e := range fb.edges {
		xe := e.x + fxMul(e.slope, e.y1-e.y0)
		fmt.Printf("w%2d x %8.3f y %8.3f -> x %8.3f y %8.3f\n", e.wind, float64(e.x)/65536, float64(e.y0)/65536, float64(xe)/65536, float64(e.y1)/65536)
	}
}
