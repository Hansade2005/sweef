package raster

import (
	"fmt"
	"os"
	"strconv"
	"testing"
)

func TestZZDev2(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("CELL"))
	s := cl2Shapes()[n]
	c := cl2CellsOnly()[n]
	var fb fxBuilder
	fxPathEdges(&fb, cl2Path(s, c), float64(c.X0), float64(c.Y0), false)
	cp := s.p
	at := func(tt float64) (float64, float64) {
		u := 1 - tt
		x := u*u*u*cp[0][0] + 3*u*u*tt*cp[1][0] + 3*u*tt*tt*cp[2][0] + tt*tt*tt*cp[3][0]
		y := u*u*u*cp[0][1] + 3*u*u*tt*cp[1][1] + 3*u*tt*tt*cp[2][1] + tt*tt*tt*cp[3][1]
		return x, y
	}
	xAtY := func(y float64) float64 {
		lo, hi := 0.0, 1.0
		for k := 0; k < 60; k++ {
			m := (lo + hi) / 2
			_, ym := at(m)
			if (ym < y) == (cp[3][1] > cp[0][1]) {
				lo = m
			} else {
				hi = m
			}
		}
		x, _ := at(lo)
		return x
	}
	for _, e := range fb.edges {
		y0 := float64(e.y0) / 65536
		fmt.Printf("y %7.3f x %8.3f true %8.3f dev %+.4f slope %.3f\n", y0, float64(e.x)/65536, xAtY(y0), float64(e.x)/65536-xAtY(y0), float64(e.slope)/65536)
	}
}

func cl2CellsOnly() []IRect { c, _, _ := cl2Layout(); return c }
