package raster

import (
	"math/rand"
	"testing"
)

// The pixel-copy fast path of ImageShader must equal the general sampler.
func TestImageShaderCopyPathExact(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for it := 0; it < 300; it++ {
		w, h := 1+rng.Intn(40), 1+rng.Intn(30)
		img := NewSurface(w, h)
		for i := 0; i < len(img.Pix); i += 4 {
			a := uint8(rng.Intn(256))
			img.Pix[i+3] = a
			for k := 0; k < 3; k++ {
				img.Pix[i+k] = uint8(rng.Intn(int(a) + 1))
			}
			if rng.Intn(20) == 0 { // out-of-range premult values must be clamped identically
				img.Pix[i] = 255
			}
		}
		tx, ty := float64(rng.Intn(60)-30), float64(rng.Intn(60)-30)
		modes := []FilterMode{FilterBilinear, FilterNearest}
		for _, repX := range []bool{false, true} {
			for _, repY := range []bool{false, true} {
				for _, f := range modes {
					sh := NewImageShader(img, Translate(tx, ty), repX, repY, f, 1)
					if rng.Intn(3) == 0 {
						sh.SetClamp(IRect{rng.Intn(3), rng.Intn(3), w - rng.Intn(3), h - rng.Intn(3)})
					}
					for y := -5; y < h+40; y += 3 {
						x0, n := rng.Intn(50)-20, 1+rng.Intn(80)
						a := make([]uint8, n*4)
						b := make([]uint8, n*4)
						noCopyFast = true
						sh.ShadeRow(x0, y, n, a)
						noCopyFast = false
						if !sh.shadeRowCopy(x0, y, n, b) {
							t.Fatal("fast path refused an aligned mapping")
						}
						for i := range a {
							if a[i] != b[i] {
								t.Fatalf("it=%d rep=%v,%v f=%v clamp=%v y=%d x0=%d: byte %d: general %d copy %d", it, repX, repY, f, sh.Clamp, y, x0, i, a[i], b[i])
							}
						}
					}
				}
			}
		}
	}
}
