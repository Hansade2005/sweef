package raster

import (
	"fmt"
	"testing"

	"github.com/sweefbrowser/sweef/internal/font"
)

func TestDbgFx(t *testing.T) {
	f := font.Lookup("DejaVu Sans", font.Style{Weight: 700})
	g := Glyph{R: 'O', Face: f}
	segs := glyphUnits(g)
	upm := f.UnitsPerEm()
	scaleX := ftDivFix(int(16*64), upm)
	conv := func(x, y float64) (float64, float64) {
		X := ftMulFix(int(x+0.5*sgn(x)), scaleX)
		Y := ftMulFix(int(y+0.5*sgn(y)), scaleX)
		return float64(X) / 64, float64(Y) / 64
	}
	for _, s := range segs {
		x0, y0 := conv(s.X0, s.Y0)
		x1, y1 := conv(s.X1, s.Y1)
		fmt.Printf("%c %.3f,%.3f  %.3f,%.3f\n", s.Op, x0, y0, x1, y1)
	}
}

func sgn(v float64) float64 {
	if v < 0 {
		return -1
	}
	return 1
}
