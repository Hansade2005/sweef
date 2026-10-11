package raster

import (
	"math"

	"github.com/sweefbrowser/sweef/internal/render"
)

// CSS mask: PushMask / PopMask ops. The masked content is painted into an
// isolated layer; PopMask renders every mask layer into its own surface,
// combines them (mask-composite) into one alpha plane and multiplies the
// layer with it before compositing.

type maskJob struct {
	layers []render.MaskLayerOps
	ctm    Matrix
	bounds render.Rect
}

func (p *Painter) pushMask(o render.PushMask) {
	c := p.c
	c.Save()
	b := c.ClipBounds()
	if o.Bounds.W > 0 && o.Bounds.H > 0 {
		pa := NewPath()
		pa.Rect(o.Bounds.X, o.Bounds.Y, o.Bounds.W, o.Bounds.H)
		bb, _ := pa.Transform(c.Transform()).Bounds()
		b = bb.OutsetBounds().Intersect(b)
	}
	c.BeginLayer(b, nil)
	c.layerMeta = append(c.layerMeta, layerMeta{opacity: 1, mask: &maskJob{layers: o.Layers, ctm: c.Transform(), bounds: o.Bounds}})
}

func (p *Painter) popMask() {
	c := p.c
	n := len(c.layerMeta)
	if n == 0 || c.LayerDepth() == 0 {
		return
	}
	lm := c.layerMeta[n-1]
	c.layerMeta = c.layerMeta[:n-1]
	if lm.mask != nil {
		p.applyMask(c.surf, lm.mask)
	}
	c.EndLayer(1, SourceOver)
	c.Restore()
}

// applyMask multiplies the surface by the combined mask layers.
func (p *Painter) applyMask(layer *Surface, job *maskJob) {
	R := layer.R
	w, h := R.W(), R.H()
	if w <= 0 || h <= 0 {
		return
	}
	acc := make([]uint8, w*h)
	for i := len(job.layers) - 1; i >= 0; i-- {
		ml := job.layers[i]
		s := NewSurfaceRect(R)
		sub := &Painter{c: NewContextSurface(s), env: p.env, dpr: p.dpr}
		sub.c.SetTransform(job.ctm)
		sub.c.SetSmoothing(true, QualityLow)
		for _, op := range ml.Ops {
			sub.op(op)
		}
		for sub.c.LayerDepth() > 0 {
			sub.c.EndLayer(1, SourceOver)
		}
		lum := ml.Mode == "luminance"
		for y := 0; y < h; y++ {
			row := s.Pix[y*s.Stride : y*s.Stride+w*4]
			dst := acc[y*w : (y+1)*w]
			for x := 0; x < w; x++ {
				px := row[x*4 : x*4+4]
				var as uint32
				if lum {
					// luminance of the (premultiplied) colour = luminance * alpha
					as = (2126*uint32(px[0]) + 7152*uint32(px[1]) + 722*uint32(px[2]) + 5000) / 10000
					if as > 255 {
						as = 255
					}
				} else {
					as = uint32(px[3])
				}
				ad := uint32(dst[x])
				var r uint32
				comp := ml.Composite
				if i == len(job.layers)-1 {
					comp = "add" // the bottom layer has nothing to combine with
				}
				switch comp {
				case "subtract":
					r = mul255(as, 255-ad)
				case "intersect":
					r = mul255(as, ad)
				case "exclude":
					r = mul255(as, 255-ad) + mul255(ad, 255-as)
				default:
					r = as + mul255(ad, 255-as)
				}
				if r > 255 {
					r = 255
				}
				dst[x] = uint8(r)
			}
		}
	}
	for y := 0; y < h; y++ {
		row := layer.Pix[y*layer.Stride : y*layer.Stride+w*4]
		m := acc[y*w : (y+1)*w]
		for x := 0; x < w; x++ {
			a := uint32(m[x])
			if a == 255 {
				continue
			}
			px := row[x*4 : x*4+4]
			if a == 0 {
				px[0], px[1], px[2], px[3] = 0, 0, 0, 0
				continue
			}
			px[0] = uint8(mul255(uint32(px[0]), a))
			px[1] = uint8(mul255(uint32(px[1]), a))
			px[2] = uint8(mul255(uint32(px[2]), a))
			px[3] = uint8(mul255(uint32(px[3]), a))
		}
	}
	_ = math.Abs
}
