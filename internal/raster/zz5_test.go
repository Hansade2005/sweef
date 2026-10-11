package raster

import (
	"fmt"
	"os"
	"strconv"
	"testing"
)

func TestZZDev(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("CELL"))
	s := clShapes()[n]
	i := n
	ox, oy := float64((i%qlCols)*qlCell), float64((i/qlCols)*qlCell)
	var fb fxBuilder
	fxPathEdges(&fb, clPath(s), ox, oy, false)
	// true curve points
	var cp [4][2]float64
	for k := 0; k < 4; k++ {
		cp[k] = [2]float64{s.p[k][0] - ox, s.p[k][1] - oy}
	}
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
		if len(fb.edges) > 0 && e.wind == 1 && e.x/65536 == int(cp[0][0]) && false {
			continue
		}
		xe := e.x + fxMul(e.slope, e.y1-e.y0)
		y0, y1 := float64(e.y0)/65536, float64(e.y1)/65536
		fmt.Printf("w%2d y %7.3f..%7.3f  dev start %+.4f end %+.4f\n", e.wind, y0, y1, float64(e.x)/65536-xAtY(y0), float64(xe)/65536-xAtY(y1))
	}
}
