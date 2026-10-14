package media

import (
	"encoding/binary"
	"image"
	"runtime"
	"sync"

	"github.com/sweefbrowser/sweef/internal/media/yuv"
)

// clipTab clamps (value >> 16) to 0..255 without a branch: the index is the shifted value plus clipOff, which covers
// every value the conversion can produce (about -230 to 490).
const clipOff = 512

var clipTab = func() (t [2048]uint8) {
	for i := range t {
		v := i - clipOff
		switch {
		case v < 0:
			t[i] = 0
		case v > 255:
			t[i] = 255
		default:
			t[i] = uint8(v)
		}
	}
	return
}()

// frameToRGBA converts a plain 8-bit 4:2:0 frame (the sample planes of a decoder, 16 bits per sample) straight to
// RGBA with the limited-range BT.601 (or BT.709) matrix. It is yuvToRGBA applied to f.ToYCbCr() with the
// narrowing copy and the second pass over the picture left out; two rows share the work on their chroma
// samples, and large pictures are converted in bands on several goroutines.
func frameToRGBA(f *yuv.Frame, bt709 bool) *image.RGBA {
	f.Wait()
	w, h := f.Width, f.Height
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	var cr2r, cb2g, cr2g, cb2b int32
	if bt709 {
		cr2r, cb2g, cr2g, cb2b = 117489, 13975, 34925, 138438
	} else {
		cr2r, cb2g, cr2g, cb2b = 104597, 25675, 53279, 132201
	}
	const yscale = 76309 // 255/219 in 16.16
	ys, cs := f.Stride[0], f.Stride[1]
	yp, cbp, crp := f.Planes[0], f.Planes[1], f.Planes[2]
	// band converts rows y0 (even) to y1
	band := func(y0, y1 int) {
		for y := y0; y < y1; y += 2 {
			two := y+1 < y1
			ya := yp[y*ys:][:w]
			cb := cbp[(y>>1)*cs:][:(w+1)>>1]
			cr := crp[(y>>1)*cs:][:(w+1)>>1]
			da := dst.Pix[y*dst.Stride:][:4*w]
			var yb []uint16
			var db []uint8
			if two {
				yb = yp[(y+1)*ys:][:w]
				db = dst.Pix[(y+1)*dst.Stride:][:4*w]
			}
			for x := 0; x < w; x += 2 {
				u := int32(cb[x>>1]) - 128
				v := int32(cr[x>>1]) - 128
				rc := cr2r*v + 32768
				gc := 32768 - cb2g*u - cr2g*v
				bc := cb2b*u + 32768
				y0 := (int32(ya[x]) - 16) * yscale
				binary.LittleEndian.PutUint32(da[4*x:], uint32(clipTab[(((y0+rc)>>16)+clipOff)&2047])|uint32(clipTab[(((y0+gc)>>16)+clipOff)&2047])<<8|uint32(clipTab[(((y0+bc)>>16)+clipOff)&2047])<<16|0xff000000)
				if x+1 < w {
					y1 := (int32(ya[x+1]) - 16) * yscale
					binary.LittleEndian.PutUint32(da[4*x+4:], uint32(clipTab[(((y1+rc)>>16)+clipOff)&2047])|uint32(clipTab[(((y1+gc)>>16)+clipOff)&2047])<<8|uint32(clipTab[(((y1+bc)>>16)+clipOff)&2047])<<16|0xff000000)
				}
				if two {
					y2 := (int32(yb[x]) - 16) * yscale
					binary.LittleEndian.PutUint32(db[4*x:], uint32(clipTab[(((y2+rc)>>16)+clipOff)&2047])|uint32(clipTab[(((y2+gc)>>16)+clipOff)&2047])<<8|uint32(clipTab[(((y2+bc)>>16)+clipOff)&2047])<<16|0xff000000)
					if x+1 < w {
						y3 := (int32(yb[x+1]) - 16) * yscale
						binary.LittleEndian.PutUint32(db[4*x+4:], uint32(clipTab[(((y3+rc)>>16)+clipOff)&2047])|uint32(clipTab[(((y3+gc)>>16)+clipOff)&2047])<<8|uint32(clipTab[(((y3+bc)>>16)+clipOff)&2047])<<16|0xff000000)
					}
				}
			}
		}
	}
	workers := min(runtime.GOMAXPROCS(0), 4)
	if w*h < 320*240 || workers < 2 {
		band(0, h)
		return dst
	}
	var wg sync.WaitGroup
	step := ((h+workers-1)/workers + 1) &^ 1 // even, so that no chroma row is shared by two bands
	for y := 0; y < h; y += step {
		y1 := min(y+step, h)
		wg.Add(1)
		go func(y0, y1 int) {
			defer wg.Done()
			band(y0, y1)
		}(y, y1)
	}
	wg.Wait()
	return dst
}
