package raster

import (
	"fmt"
	"math"
	"testing"

	"github.com/sweefbrowser/sweef/internal/font"
)

func TestZZFrac(t *testing.T) {
	f := font.Lookup("Inter", font.Style{Weight: 400})
	fmt.Println(f.UnitsPerEm())
	frac, tot := 0, 0
	for _, r := range "oRgQeabcdefghijklmnopqrstuvwxyz" {
		for _, s := range glyphUnits(Glyph{Face: f, R: r}) {
			for _, v := range []float64{s.X0, s.Y0, s.X1, s.Y1, s.X2, s.Y2} {
				tot++
				if v != math.Round(v) {
					frac++
				}
			}
		}
	}
	fmt.Println("frac", frac, "of", tot)
}
