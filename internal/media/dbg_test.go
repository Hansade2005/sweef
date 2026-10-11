package media

import (
	"fmt"
	"os"
	"testing"
)

// firstDiff reports where the first differing frame differs, in macroblock units.
func firstDiff(t *testing.T, path string, w, h int) {
	data, _ := os.ReadFile(path)
	ref, _ := ffmpegRawNative(t, path)
	frames := decodeRawNative(t, data)
	fs := len(frames[0])
	for i, f := range frames {
		r := ref[i*fs : (i+1)*fs]
		for j := range f {
			if f[j] != r[j] {
				plane := "Y"
				x, y := j%w, j/w
				if j >= w*h {
					plane = "C"
					k := j - w*h
					x, y = k%(w/2), k/(w/2)
					if y >= h/2 {
						y -= h / 2
						plane = "Cr"
					} else {
						plane = "Cb"
					}
				}
				fmt.Printf("frame %d first diff plane %s at (%d,%d) mb (%d,%d): ffmpeg %d native %d\n", i, plane, x, y, x/16, y/16, r[j], f[j])
				return
			}
		}
	}
	fmt.Println("no diff")
}

func TestDbgFirstDiff(t *testing.T) {
	f := os.Getenv("DBG_MP4")
	if f == "" {
		t.Skip()
	}
	var w, h int
	fmt.Sscanf(os.Getenv("DBG_SIZE"), "%dx%d", &w, &h)
	firstDiff(t, f, w, h)
}
