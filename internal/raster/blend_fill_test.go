package raster

import (
	"bytes"
	"math/rand"
	"testing"
)

// Filling a row with an opaque colour gives what blending that colour over anything gives.
func TestFillRowSolidMatchesBlend(t *testing.T) {
	r := rand.New(rand.NewSource(9))
	for iter := 0; iter < 200; iter++ {
		n := 1 + r.Intn(300)
		c := [4]uint8{uint8(r.Intn(256)), uint8(r.Intn(256)), uint8(r.Intn(256)), 255}
		a := make([]uint8, n*4)
		r.Read(a)
		b := append([]uint8(nil), a...)
		src := make([]uint8, n*4)
		for i := 0; i < n; i++ {
			copy(src[i*4:], c[:])
		}
		blendRow(a, src, nil, SourceOver, n)
		fillRowSolid(b, c)
		if !bytes.Equal(a, b) {
			t.Fatalf("width %d: fill differs from blend", n)
		}
	}
}

// A solid opaque rectangle drawn through the context (the page background) gives the pixels it always gave.
func TestOpaqueSolidFillThroughContext(t *testing.T) {
	r := rand.New(rand.NewSource(10))
	for iter := 0; iter < 50; iter++ {
		w, h := 5+r.Intn(200), 5+r.Intn(100)
		bg := randOpaque(r, w, h)
		c := NewContext(w, h)
		copy(c.Surface().Pix, bg.Pix)
		col := Color{R: float64(r.Intn(256)) / 255, G: float64(r.Intn(256)) / 255, B: float64(r.Intn(256)) / 255, A: 1}
		c.SetFill(Brush{Color: col})
		c.FillRect(float64(r.Intn(10)), float64(r.Intn(10)), float64(w-r.Intn(10)), float64(h-r.Intn(10)))
		// every touched pixel is the colour, the untouched ones are the background
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				p := c.Surface().Pix[(y*w+x)*4 : (y*w+x)*4+4]
				o := bg.Pix[(y*w+x)*4 : (y*w+x)*4+4]
				if p[3] != 255 {
					t.Fatalf("pixel %d,%d lost its alpha", x, y)
				}
				same := bytes.Equal(p, o)
				isCol := int(p[0]) == int(col.R*255+0.5) && int(p[1]) == int(col.G*255+0.5) && int(p[2]) == int(col.B*255+0.5)
				if !same && !isCol {
					t.Fatalf("pixel %d,%d is neither the background nor the fill colour: %v", x, y, p)
				}
			}
		}
	}
}
