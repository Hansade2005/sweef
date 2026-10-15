package engine

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
	"time"
)

// encodePNGFast round-trips through image/png, opaque (written as RGB) and with transparency (RGBA).
func TestEncodePNGFastRoundTrip(t *testing.T) {
	for _, alpha := range []bool{false, true} {
		img := image.NewRGBA(image.Rect(0, 0, 1280, 800))
		for y := 0; y < 800; y++ {
			for x := 0; x < 1280; x++ {
				a := uint8(255)
				if alpha && (x+y)%7 == 0 {
					a = uint8(x)
				}
				img.SetRGBA(x, y, color.RGBA{uint8(x), uint8(y), uint8(x ^ y), a})
			}
		}
		if alpha { // premultiplied: keep the colour within alpha
			for i := 0; i < len(img.Pix); i += 4 {
				for k := 0; k < 3; k++ {
					if img.Pix[i+k] > img.Pix[i+3] {
						img.Pix[i+k] = img.Pix[i+3]
					}
				}
			}
		}
		var buf bytes.Buffer
		t0 := time.Now()
		if err := encodePNGFast(&buf, img); err != nil {
			t.Fatal(err)
		}
		d := time.Since(t0)
		n := buf.Len()
		got, err := png.Decode(&buf)
		if err != nil {
			t.Fatalf("alpha=%v: %v", alpha, err)
		}
		var ref bytes.Buffer
		_ = png.Encode(&ref, img)
		want, _ := png.Decode(&ref)
		for y := 0; y < 800; y += 37 {
			for x := 0; x < 1280; x += 13 {
				if c, w := color.NRGBAModel.Convert(got.At(x, y)), color.NRGBAModel.Convert(want.At(x, y)); c != w {
					t.Fatalf("alpha=%v: pixel %d,%d is %v, image/png gives %v", alpha, x, y, c, w)
				}
			}
		}
		t.Logf("alpha=%v: %d bytes in %v", alpha, n, d)
	}
}
