package raster

import (
	"encoding/base64"
	"image"
	"net/url"
	"strings"
	"sync"

	"github.com/sweefbrowser/sweef/internal/render"
)

// <image> (and <feImage>) pictures: raster images and nested SVG documents,
// from data: URIs or resolved through the Env at layout time (SVGNode.Img /
// SVGNode.Sub).

type svgPic struct {
	im     image.Image
	doc    *render.SVGDoc
	iw, ih float64
}

var svgDataDocs sync.Map // href -> *render.SVGDoc (or nil)

// dataSVGDoc parses a data: URI holding an SVG image.
func dataSVGDoc(href string) *render.SVGDoc {
	if v, ok := svgDataDocs.Load(href); ok {
		d, _ := v.(*render.SVGDoc)
		return d
	}
	var doc *render.SVGDoc
	if strings.HasPrefix(href, "data:") {
		if i := strings.IndexByte(href, ','); i > 0 {
			meta, body := href[5:i], href[i+1:]
			if strings.Contains(meta, "image/svg+xml") {
				var markup string
				ok := false
				if strings.Contains(meta, ";base64") {
					if b, err := base64.StdEncoding.DecodeString(strings.Map(func(r rune) rune {
						if r == ' ' || r == '\n' || r == '\r' {
							return -1
						}
						return r
					}, body)); err == nil {
						markup, ok = string(b), true
					}
				} else if s, err := url.PathUnescape(body); err == nil {
					markup, ok = s, true
				}
				if ok {
					doc = render.ParseSVG(markup)
				}
			}
		}
	}
	svgDataDocs.Store(href, doc)
	return doc
}

func picOfDoc(d *render.SVGDoc) *svgPic {
	w, h := d.IntrinsicSize()
	return &svgPic{doc: d, iw: w, ih: h}
}

// picture returns the picture an <image>/<feImage> node refers to.
func (r *svgR) picture(n *svgNode) *svgPic {
	if n.Img != nil {
		b := n.Img.Bounds()
		return &svgPic{im: n.Img, iw: float64(b.Dx()), ih: float64(b.Dy())}
	}
	if n.Sub != nil {
		return picOfDoc(n.Sub)
	}
	href := strings.TrimSpace(n.A["href"])
	if href == "" {
		return nil
	}
	if d := dataSVGDoc(href); d != nil {
		return picOfDoc(d)
	}
	if im := decodeDataImage(href); im != nil {
		b := im.Bounds()
		return &svgPic{im: im, iw: float64(b.Dx()), ih: float64(b.Dy())}
	}
	return nil
}

// drawPic draws pic into the user-space rectangle (x, y, w, h) honouring
// preserveAspectRatio; the context transform is the user space.
func (r *svgR) drawPic(pic *svgPic, x, y, w, h float64, par string, rendering string) {
	if pic == nil || w <= 0 || h <= 0 || pic.iw <= 0 || pic.ih <= 0 {
		return
	}
	c := r.c
	c.Save()
	defer c.Restore()
	c.ClipRect(x, y, w, h)
	if pic.doc != nil && strings.TrimSpace(pic.doc.Root.A["viewbox"]) != "" {
		// an image document with a viewBox gets the whole rectangle as its
		// viewport and applies its own viewBox / preserveAspectRatio (the
		// element's preserveAspectRatio does not re-fit it)
		svgDrawOn(c, render.SVGDraw{Dst: render.Rect{X: x, Y: y, W: w, H: h}, Doc: pic.doc, Color: render.Color{A: 255}, FontSize: 16, Family: "serif", Weight: 400})
		return
	}
	vm, _ := viewBoxMatrix([]float64{0, 0, pic.iw, pic.ih}, par, w, h)
	c.Concat(Translate(x, y).Mul(vm))
	if pic.doc != nil {
		svgDrawOn(c, render.SVGDraw{Dst: render.Rect{W: pic.iw, H: pic.ih}, Doc: pic.doc, Color: render.Color{A: 255}, FontSize: 16, Family: "serif", Weight: 400})
		return
	}
	pix := rendering == "pixelated" || rendering == "crisp-edges"
	c.SetSmoothing(!pix, QualityLow)
	c.DrawImage(FromImage(pic.im), 0, 0, pic.iw, pic.ih, 0, 0, pic.iw, pic.ih)
}

func (r *svgR) drawImage(n *svgNode, st sStyle, m Matrix) {
	if !st.visible {
		return
	}
	pic := r.picture(n)
	if pic == nil {
		return
	}
	x, y := r.attrLen(n, "x", 'x', &st, 0), r.attrLen(n, "y", 'y', &st, 0)
	w, wok := r.optLen(n, "width", 'x', &st)
	h, hok := r.optLen(n, "height", 'y', &st)
	switch {
	case !wok && !hok:
		w, h = pic.iw, pic.ih
	case !wok:
		w = h * pic.iw / pic.ih
	case !hok:
		h = w * pic.ih / pic.iw
	}
	r.c.Save()
	defer r.c.Restore()
	r.c.SetTransform(m)
	r.drawPic(pic, x, y, w, h, n.A["preserveaspectratio"], st.imageRendering)
}
