package media

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"testing"

	"github.com/sweefbrowser/sweef/internal/media/h264"
)

func TestDbgSep(t *testing.T) {
	f := os.Getenv("DBG_ES")
	if f == "" {
		t.Skip()
	}
	es, _ := os.ReadFile(f)
	aus := h264.SplitAccessUnits(es)
	d := h264.NewDecoder()
	h264.DebugCorrupt = func(s string) { fmt.Println("corrupt at", s) }
	h264.DebugSlice = func(s string) { fmt.Println("SLICE", s) }
	n := 0
	for i, au := range aus {
		fr, err := d.DecodeFrame(au)
		if err != nil {
			fmt.Println("au", i, "err", err)
		}
		if fr == nil {
			continue
		}
		n++
		for p := 0; p < 3; p++ {
			mn, mx := 1<<20, 0
			for y := 0; y < fr.Height; y++ {
				for x := 0; x < fr.Width; x++ {
					v := int(fr.Planes[p][y*fr.Stride[p]+x])
					mn, mx = min(mn, v), max(mx, v)
				}
			}
			fmt.Println("frame", n, "plane", p, "min", mn, "max", mx, "depth", fr.Depth, "fmt", fr.Format, fr.Width, fr.Height)
		}
		if n == 1 || n == 3 {
			w, h := fr.Width, fr.Height
			img := image.NewGray(image.Rect(0, 0, w*3, h))
			for p := 0; p < 3; p++ {
				for y := 0; y < h; y++ {
					for x := 0; x < w; x++ {
						v := fr.Planes[p][y*fr.Stride[p]+x] >> uint(fr.Depth-8)
						img.SetGray(p*w+x, y, color.Gray{uint8(v)})
					}
				}
			}
			o, _ := os.Create(fmt.Sprintf("/tmp/claude-0/dbg_sep_%d.png", n))
			png.Encode(o, img)
			o.Close()
		}
		if n >= 3 {
			break
		}
	}
	fmt.Println("decoded", n)
}
