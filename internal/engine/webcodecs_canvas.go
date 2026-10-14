package engine

// webcodecs_canvas.go: the places where WebCodecs meets the canvas and the image decoders: a VideoFrame is a
// drawImage / createImageBitmap / createPattern source, a canvas, ImageBitmap or <video> is a VideoFrame source,
// and ImageDecoder turns encoded images into VideoFrames.

import (
	"bytes"
	"image"
	"image/draw"

	"github.com/sweefbrowser/sweef/internal/media"
	"github.com/sweefbrowser/sweef/internal/raster"
)

// wcSurface renders a frame's visible rectangle as a canvas surface.
func wcSurface(hd *wcHandle) *raster.Surface {
	if hd == nil || hd.d.audio || hd.vw <= 0 || hd.vh <= 0 {
		return nil
	}
	pix := hd.d.toRGBA(hd.vx, hd.vy, hd.vw, hd.vh)
	s := raster.NewSurface(hd.vw, hd.vh)
	s.SetNRGBA(pix, hd.vw, hd.vh, 0, 0)
	return s
}

// surfaceNRGBA returns the surface as straight-alpha RGBA rows (w*4 bytes).
func surfaceNRGBA(s *raster.Surface) (pix []byte, w, h int) {
	w, h = s.W(), s.H()
	pix = make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		src := s.Pix[y*s.Stride : y*s.Stride+w*4]
		dst := pix[y*w*4:]
		for x := 0; x < w; x++ {
			r, g, b, a := uint32(src[x*4]), uint32(src[x*4+1]), uint32(src[x*4+2]), uint32(src[x*4+3])
			if a != 0 && a != 255 {
				r, g, b = min(r*255/a, 255), min(g*255/a, 255), min(b*255/a, 255)
			}
			dst[x*4], dst[x*4+1], dst[x*4+2], dst[x*4+3] = byte(r), byte(g), byte(b), byte(a)
		}
	}
	return
}

// toFrame(kind, ref) creates a VideoFrame payload from whatever the canvas layer can read for that source (a 2D
// canvas, an ImageBitmap, a <canvas> or <video> element) and returns its description.
func (h *cvHost) toFrame(a []any) (any, error) {
	s := h.surfaceOf(cvS(a, 0), a, 1)
	if s == nil || s.W() == 0 || s.H() == 0 {
		return nil, nil
	}
	pix, w, hh := surfaceNRGBA(s)
	d := wcFromRGBA(pix, w, hh)
	hd := wcNewHandle(h.j.wcState(), d, 0, 0, w, hh)
	if hd == nil {
		return nil, hostErrOf(errQuota)
	}
	return jsonStr(hd.info()), nil
}

type wcQuotaErr struct{}

func (wcQuotaErr) Error() string { return "QuotaExceededError:too many open VideoFrames" }

var errQuota error = wcQuotaErr{}

// ---- ImageDecoder -----------------------------------------------------------------------------------------

var imageMIME = map[string]string{"png": "image/png", "jpeg": "image/jpeg", "gif": "image/gif", "webp": "image/webp", "bmp": "image/bmp"}

func nrgbaOf(img image.Image) *image.NRGBA {
	b := img.Bounds()
	n := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(n, n.Bounds(), img, b.Min, draw.Src)
	return n
}

// imageOpen decodes every frame of an encoded image and registers them as RGBA payloads. The result describes the
// tracks for the JS ImageDecoder: {type, animated, loops, frames: [{i: info, d: milliseconds}]}.
func (h *wcHost) imageOpen(src []byte, typ string) any {
	var imgs []image.Image
	var secs []float64
	mime := ""
	animated, loops := false, false
	if fr, sc, lp, ok := media.AnimatedImageFrames(src); ok {
		imgs, secs, loops, animated = fr, sc, lp, true
		mime = "image/gif"
		if len(src) > 8 && string(src[1:4]) == "PNG" {
			mime = "image/png"
		}
	} else {
		img, format, err := image.Decode(bytes.NewReader(src))
		if err != nil {
			return wcErr("EncodingError", "The image could not be decoded: %v", err)
		}
		imgs, secs, mime = []image.Image{img}, []float64{0}, imageMIME[format]
	}
	if mime == "" {
		mime = typ
	}
	type fr struct {
		I map[string]any `json:"i"`
		D float64        `json:"d"`
	}
	var frames []fr
	for k, im := range imgs {
		n := nrgbaOf(im)
		w, hh := n.Rect.Dx(), n.Rect.Dy()
		d := wcFromRGBA(n.Pix, w, hh)
		hd := wcNewHandle(h, d, 0, 0, w, hh)
		if hd == nil {
			for _, f := range frames {
				h.drop(f.I["h"].(int64))
			}
			return wcErr("QuotaExceededError", "the image is too large")
		}
		frames = append(frames, fr{I: hd.info(), D: secs[k] * 1000})
	}
	return jsonStr(map[string]any{"type": mime, "animated": animated, "loops": loops, "frames": frames})
}
