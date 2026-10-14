package engine

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"image/png"

	"github.com/sweefbrowser/sweef/internal/media"
)

func mediaExtract(data []byte) (*media.VideoSamples, error) { return media.ExtractVideoSamples(data) }

// mediaTestPNG is a 2x2 PNG whose first pixel is opaque red.
func mediaTestPNG() []byte {
	img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	img.SetNRGBA(0, 0, color.NRGBA{255, 0, 0, 255})
	img.SetNRGBA(1, 0, color.NRGBA{0, 255, 0, 255})
	img.SetNRGBA(0, 1, color.NRGBA{0, 0, 255, 255})
	img.SetNRGBA(1, 1, color.NRGBA{255, 255, 255, 255})
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

// mediaTestGIF is a two-frame animated GIF, frames of 100 ms and 200 ms.
func mediaTestGIF() []byte {
	pal := color.Palette{color.RGBA{255, 0, 0, 255}, color.RGBA{0, 0, 255, 255}}
	f0 := image.NewPaletted(image.Rect(0, 0, 4, 4), pal)
	f1 := image.NewPaletted(image.Rect(0, 0, 4, 4), pal)
	for i := range f1.Pix {
		f1.Pix[i] = 1
	}
	var b bytes.Buffer
	_ = gif.EncodeAll(&b, &gif.GIF{Image: []*image.Paletted{f0, f1}, Delay: []int{10, 20}})
	return b.Bytes()
}
