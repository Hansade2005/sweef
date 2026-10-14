package raster

import (
	"bytes"
	"image"
	"math/rand"
	"testing"
)

func randOpaque(r *rand.Rand, w, h int) *image.RGBA {
	m := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(m.Pix); i += 4 {
		m.Pix[i], m.Pix[i+1], m.Pix[i+2], m.Pix[i+3] = byte(r.Intn(256)), byte(r.Intn(256)), byte(r.Intn(256)), 255
	}
	return m
}

// The video fast path gives exactly the pixels of the general image shader: for up and down scaling, odd
// sizes, boxes at any position, clipped boxes, source rectangles that are a part of the image, a device
// scale, and the cases it must refuse (fractional boxes, rotation, alpha).
func TestDrawImageFastMatchesGeneral(t *testing.T) {
	r := rand.New(rand.NewSource(3))
	for iter := 0; iter < 600; iter++ {
		iw, ih := 1+r.Intn(150), 1+r.Intn(100)
		sf := FromRGBAOpaque(randOpaque(r, iw, ih))
		if !sf.Opaque {
			t.Fatal("not opaque")
		}
		cw, ch := 20+r.Intn(200), 20+r.Intn(150)
		dx, dy := float64(r.Intn(40)-10), float64(r.Intn(40)-10)
		dw, dh := float64(1+r.Intn(220)), float64(1+r.Intn(160))
		sx, sy, sw, sh := 0.0, 0.0, float64(iw), float64(ih)
		if r.Intn(4) == 0 && iw > 4 && ih > 4 {
			sx, sy = float64(r.Intn(iw/2)), float64(r.Intn(ih/2))
			sw, sh = float64(1+r.Intn(iw-int(sx))), float64(1+r.Intn(ih-int(sy)))
		}
		if r.Intn(5) == 0 { // pixel for pixel
			dw, dh = sw, sh
		}
		variant := iter % 8
		clipX, clipW := float64(r.Intn(30)), float64(cw/2)
		scale := 1 + float64(r.Intn(2))
		draw := func(c *Context) {
			c.Save()
			defer c.Restore()
			c.SetSmoothing(true, QualityLow)
			switch variant {
			case 1:
				c.ClipRect(clipX, 5, clipW, float64(ch)/2)
			case 2:
				c.Scale(scale, scale) // a device scale
			case 3:
				c.SetGlobalAlpha(0.5) // refused
			case 4:
				c.Translate(0.5, 0) // a fractional box: refused
			case 5:
				c.Rotate(0.1) // refused
			case 6:
				c.SetSmoothing(false, QualityLow) // nearest: refused
			}
			c.DrawImage(sf, sx, sy, sw, sh, dx, dy, dw, dh)
		}
		bg := randOpaque(r, cw, ch)
		a, b := NewContext(cw, ch), NewContext(cw, ch)
		copy(a.Surface().Pix, bg.Pix)
		copy(b.Surface().Pix, bg.Pix)
		noImageFast = true
		draw(a)
		noImageFast = false
		draw(b)
		pa, pb := a.Surface().Pix, b.Surface().Pix
		if !bytes.Equal(pa, pb) {
			for i := range pa {
				if pa[i] != pb[i] {
					t.Fatalf("iter %d variant %d: image %dx%d src (%g,%g,%g,%g) -> (%g,%g,%g,%g) in %dx%d: first difference at byte %d (pixel %d,%d): general %d fast %d",
						iter, variant, iw, ih, sx, sy, sw, sh, dx, dy, dw, dh, cw, ch, i, (i/4)%cw, i/4/cw, pa[i], pb[i])
				}
			}
		}
	}
}
