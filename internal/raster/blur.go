package raster

import (
	"math"
	"runtime"
	"sync"
)

// parallelFor splits [0,n) into chunks across CPUs when the work is large.
func parallelFor(n, minPerChunk int, fn func(lo, hi int)) {
	w := runtime.GOMAXPROCS(0)
	if w <= 1 || n < 2*minPerChunk {
		fn(0, n)
		return
	}
	if w > n/minPerChunk {
		w = n / minPerChunk
	}
	if w < 2 {
		fn(0, n)
		return
	}
	var wg sync.WaitGroup
	chunk := (n + w - 1) / w
	for lo := 0; lo < n; lo += chunk {
		hi := min(lo+chunk, n)
		wg.Add(1)
		go func(lo, hi int) {
			defer wg.Done()
			fn(lo, hi)
		}(lo, hi)
	}
	wg.Wait()
}

// boxSizes returns the SVG feGaussianBlur box-blur window sizes for sigma.
func boxD(sigma float64) int {
	return int(math.Floor(sigma*3*math.Sqrt(2*math.Pi)/4 + 0.5))
}

type boxSpec struct{ lo, hi int }

func boxPasses(sigma float64) []boxSpec {
	d := boxD(sigma)
	if d < 1 {
		return nil
	}
	if d%2 == 1 {
		r := (d - 1) / 2
		return []boxSpec{{r, r}, {r, r}, {r, r}}
	}
	return []boxSpec{{d / 2, d/2 - 1}, {d/2 - 1, d / 2}, {d / 2, d / 2}}
}

// blurPlane blurs interleaved pixel data (ch channels, row stride in bytes)
// with independent sigma per axis. Out-of-range samples are transparent.
func blurPlane(pix []uint8, w, h, stride, ch int, sx, sy float64) {
	if w == 0 || h == 0 {
		return
	}
	if sx > 0 {
		blurAxis(pix, w, h, stride, ch, sx, true)
	}
	if sy > 0 {
		blurAxis(pix, w, h, stride, ch, sy, false)
	}
}

func gaussKernel(sigma float64) []float32 {
	r := int(math.Ceil(sigma * 3))
	k := make([]float32, 2*r+1)
	var sum float64
	for i := -r; i <= r; i++ {
		v := math.Exp(-float64(i*i) / (2 * sigma * sigma))
		k[i+r] = float32(v)
		sum += v
	}
	for i := range k {
		k[i] /= float32(sum)
	}
	return k
}

func blurAxis(pix []uint8, w, h, stride, ch int, sigma float64, horiz bool) {
	if sigma < 2 {
		k := gaussKernel(sigma)
		r := len(k) / 2
		if horiz {
			parallelFor(h, 32, func(lo, hi int) {
				line := make([]float32, w*ch)
				for y := lo; y < hi; y++ {
					row := pix[y*stride : y*stride+w*ch]
					for x := 0; x < w; x++ {
						for c := 0; c < ch; c++ {
							var s float32
							for i := -r; i <= r; i++ {
								xx := x + i
								if xx < 0 || xx >= w {
									continue
								}
								s += k[i+r] * float32(row[xx*ch+c])
							}
							line[x*ch+c] = s
						}
					}
					for i, v := range line {
						row[i] = uint8(v + 0.5)
					}
				}
			})
		} else {
			parallelFor(w, 32, func(lo, hi int) {
				col := make([]float32, h)
				for x := lo; x < hi; x++ {
					for c := 0; c < ch; c++ {
						for y := 0; y < h; y++ {
							var s float32
							for i := -r; i <= r; i++ {
								yy := y + i
								if yy < 0 || yy >= h {
									continue
								}
								s += k[i+r] * float32(pix[yy*stride+x*ch+c])
							}
							col[y] = s
						}
						for y := 0; y < h; y++ {
							pix[y*stride+x*ch+c] = uint8(col[y] + 0.5)
						}
					}
				}
			})
		}
		return
	}
	passes := boxPasses(sigma)
	if horiz {
		parallelFor(h, 32, func(lo, hi int) {
			tmp := make([]uint8, w*ch)
			for y := lo; y < hi; y++ {
				row := pix[y*stride : y*stride+w*ch]
				for _, p := range passes {
					boxRow(row, tmp, w, ch, p.lo, p.hi)
					copy(row, tmp)
				}
			}
		})
	} else {
		parallelFor(w, 16, func(lo, hi int) {
			tmp := make([]uint8, h)
			col := make([]uint8, h)
			for x := lo; x < hi; x++ {
				for c := 0; c < ch; c++ {
					for y := 0; y < h; y++ {
						col[y] = pix[y*stride+x*ch+c]
					}
					for _, p := range passes {
						boxRow(col, tmp, h, 1, p.lo, p.hi)
						copy(col, tmp)
					}
					for y := 0; y < h; y++ {
						pix[y*stride+x*ch+c] = col[y]
					}
				}
			}
		})
	}
}

// boxRow averages src over [i-lo, i+hi] into dst (ch interleaved channels).
func boxRow(src, dst []uint8, n, ch, lo, hi int) {
	d := lo + hi + 1
	for c := 0; c < ch; c++ {
		var sum int
		// window for i=0: [-lo, hi]
		for j := 0; j <= hi && j < n; j++ {
			sum += int(src[j*ch+c])
		}
		for i := 0; i < n; i++ {
			dst[i*ch+c] = uint8((sum + d/2) / d)
			// slide: add i+hi+1, remove i-lo
			if a := i + hi + 1; a < n {
				sum += int(src[a*ch+c])
			}
			if r := i - lo; r >= 0 {
				sum -= int(src[r*ch+c])
			}
		}
	}
}

// BlurMask returns a copy of m blurred by sigma and offset (dx,dy), expanded
// as needed and cropped to clip.
func BlurMask(m *Mask, sigmaX, sigmaY float64, dx, dy int, clip IRect) *Mask {
	padX := int(math.Ceil(sigmaX * 3))
	padY := int(math.Ceil(sigmaY * 3))
	b := IRect{m.B.X0 - padX + dx, m.B.Y0 - padY + dy, m.B.X1 + padX + dx, m.B.Y1 + padY + dy}
	// to blur correctly we need the whole expanded area; crop only the output
	full := newMask(b)
	for y := m.B.Y0; y < m.B.Y1; y++ {
		src := m.Row(y)
		o := (y+dy-b.Y0)*full.Stride + (m.B.X0 + dx - b.X0)
		copy(full.A[o:o+len(src)], src)
	}
	blurPlane(full.A, b.W(), b.H(), full.Stride, 1, sigmaX, sigmaY)
	c := b.Intersect(clip)
	if c.Empty() {
		return nil
	}
	if c == b {
		return full
	}
	out := newMask(c)
	for y := c.Y0; y < c.Y1; y++ {
		copy(out.Row(y), full.A[(y-b.Y0)*full.Stride+(c.X0-b.X0):][:c.W()])
	}
	return out
}
