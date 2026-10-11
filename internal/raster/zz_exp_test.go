package raster

import (
	"fmt"
	"image/png"
	"math"
	"os"
	"testing"
)

var CX, CY = 620, 20
var SC = 2.0
func TestTurbExp(t *testing.T) {
	f, _ := os.Open("/tmp/sc/p6.png")
	img, _ := png.Decode(f)
	g := newTurbGen(1)
	best := math.Inf(1)
	for _, off := range [][2]float64{{1,1},{0.5,0.5},{0.25,0.25},{0,0},{2,2},{0.75,0.75}} {
		for _, lin := range []bool{true} {
			var err float64
			n := 0
			for y := 5; y < 105; y++ {
				for x := 5; x < 145; x++ {
					var c [4]float64
					for ch := 0; ch < 4; ch++ {
						s := g.turbulence(ch, float64(x)/SC+off[0], float64(y)/SC+off[1], 0.04, 0.04, 3, true, false, 0, 0, 0, 0)
						v := (s*255 + 255) / 2
						c[ch] = math.Max(0, math.Min(1, v/255))
					}
					a := c[3]
					var rgb [3]float64
					for k := 0; k < 3; k++ {
						v := c[k] * a
						if lin {
							// premul linear -> srgb: unpremul convert premul
							u := c[k]
							v = linearToSRGB(u) * a
						}
						rgb[k] = (v + (1 - a)) * 255
					}
					r, gg, b, _ := img.At(CX+x, CY+y).RGBA()
					err += math.Abs(rgb[0]-float64(r>>8)) + math.Abs(rgb[1]-float64(gg>>8)) + math.Abs(rgb[2]-float64(b>>8))
					n += 3
				}
			}
			e := err / float64(n)
			fmt.Println(off, lin, e)
			if e < best {
				best = e
			}
		}
	}
}
