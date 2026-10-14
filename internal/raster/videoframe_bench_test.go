package raster

import (
	"image"
	"testing"
)

// BenchmarkDrawVideoFrame is the cost of putting one decoded 1080p frame on the screen: the general path
// copies the RGBA frame into a surface (FromImage) and draws it scaled with the image shader; the video path
// (the Fast variants) views the opaque frame as a surface without a copy and blits it.
func benchVideoFrame(b *testing.B, dw, dh int, fast bool) {
	frame := image.NewRGBA(image.Rect(0, 0, 1920, 1080))
	for i := range frame.Pix {
		frame.Pix[i] = byte(i*7 + i>>9)
		if i%4 == 3 {
			frame.Pix[i] = 255
		}
	}
	c := NewContext(dw, dh)
	noImageFast = !fast
	defer func() { noImageFast = false }()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var s *Surface
		if fast {
			s = FromRGBAOpaque(frame)
		} else {
			s = FromImage(frame)
		}
		c.Save()
		c.SetSmoothing(true, QualityLow)
		c.DrawImage(s, 0, 0, 1920, 1080, 0, 0, float64(dw), float64(dh))
		c.Restore()
	}
}

func BenchmarkDrawVideoFrame1080To1080(b *testing.B)     { benchVideoFrame(b, 1920, 1080, false) }
func BenchmarkDrawVideoFrame1080To720(b *testing.B)      { benchVideoFrame(b, 1280, 720, false) }
func BenchmarkDrawVideoFrame1080To360(b *testing.B)      { benchVideoFrame(b, 640, 360, false) }
func BenchmarkDrawVideoFrameFast1080To1080(b *testing.B) { benchVideoFrame(b, 1920, 1080, true) }
func BenchmarkDrawVideoFrameFast1080To720(b *testing.B)  { benchVideoFrame(b, 1280, 720, true) }
func BenchmarkDrawVideoFrameFast1080To360(b *testing.B)  { benchVideoFrame(b, 640, 360, true) }
