package raster

import (
	"fmt"
	"image/png"
	"os"
	"testing"
)

func TestZZHist(t *testing.T) {
	fh, _ := os.Open(zzSP + "z5.png")
	ref, _ := png.Decode(fh)
	for _, extra := range []bool{false, true} {
		h := map[int]int{}
		for _, c := range z5Cells() {
			if c.kind != "cubic" || c.extra != extra {
				continue
			}
			for y := 0; y < 256; y++ {
				for x := 0; x < 256; x++ {
					if x < 8 && y < 8 {
						continue
					}
					r, _, _, _ := ref.At(c.ox+x, c.oy+y).RGBA()
					h[255-int(r>>8)]++
				}
			}
		}
		n := 0
		for k, v := range h {
			if k > 0 && k < 255 && v > 0 {
				n++
			}
		}
		fmt.Println("extra", extra, "distinct partial alpha values", n)
	}
}
