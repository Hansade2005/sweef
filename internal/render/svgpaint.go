package render

import (
	"math"
	"strings"

	"github.com/sweefbrowser/sweef/internal/style"
)

func (p *painter) svgDraw(b *lbox, dst Rect, doc *SVGDoc) SVGDraw {
	st := b.st
	return SVGDraw{Dst: dst, Doc: doc, Color: st.Color, FontSize: st.FontSize, Family: strings.Join(st.FontFamily, ","),
		Weight: st.FontWeight, Italic: st.FontStyle != 0}
}

// paintInlineSVG emits the scene of an inline <svg> element.
func (p *painter) paintInlineSVG(b *lbox, cr Rect, rad Radii) {
	doc := p.inlineSVGDoc(b.n, b.st)
	if doc == nil {
		return
	}
	ov := doc.Root.A["overflow"] // UA sheet: svg:not(:root) { overflow: hidden }
	clip := ov != "visible" && ov != "auto"
	if clip {
		p.emit(PushClip{Rect: cr, Radii: rad})
	}
	p.emit(p.svgDraw(b, cr, doc))
	p.paintForeign(b)
	if clip {
		p.emit(PopClip{})
	}
}

// paintSVGImg draws <img src=*.svg> (data: URIs or Env SVGSourcer) when the
// bitmap loader had nothing for it.
func (p *painter) paintSVGImg(b *lbox, r *replacedInfo, cr Rect, rad Radii) {
	doc := p.c.svgImageDocFor(r.url)
	if doc == nil {
		return
	}
	iw, ih := svgImageSize(doc)
	dst, _ := fitImage(b.st.ObjectFit, b.st.ObjectPos, cr, iw, ih)
	if b.st.ObjectFit == "" || b.st.ObjectFit == "fill" {
		dst = cr
	} else {
		// whole image rect (unclipped) so the scene maps onto it; clip to the box
		dst = fitDst(b.st.ObjectFit, b.st.ObjectPos, cr, iw, ih)
	}
	doc = p.c.svgImageDocSized(r.url, dst.W, dst.H)
	p.emit(PushClip{Rect: cr, Radii: rad})
	p.emit(SVGDraw{Dst: dst, Doc: doc, Color: b.st.Color, FontSize: 16, Family: "serif", Weight: 400})
	p.emit(PopClip{})
}

func fitDst(fit string, pos [2]style.Length, box Rect, iw, ih float64) Rect {
	var dw, dh float64
	switch fit {
	case "contain":
		s := minf(box.W/iw, box.H/ih)
		dw, dh = iw*s, ih*s
	case "cover":
		s := maxf(box.W/iw, box.H/ih)
		dw, dh = iw*s, ih*s
	case "none":
		dw, dh = iw, ih
	case "scale-down":
		s := minf(1, minf(box.W/iw, box.H/ih))
		dw, dh = iw*s, ih*s
	default:
		return box
	}
	return Rect{box.X + pos[0].Resolve(box.W-dw), box.Y + pos[1].Resolve(box.H-dh), dw, dh}
}

// paintScrollbars emits the classic scrollbars reserved by layout for b.
func (p *painter) paintScrollbars(b *lbox) {
	if b.sbW <= 0 && b.sbH <= 0 {
		return
	}
	st := b.st
	if st.Visibility != "visible" && st.Visibility != "" {
		return
	}
	dark := p.c.opts.ColorScheme == "dark" && (st.ColorScheme == "dark" || strings.Contains(st.ColorScheme, "dark"))
	x0, y0 := b.ax+b.bor[3], b.ay+b.bor[0]
	pw, ph := b.w-b.bor[1]-b.bor[3], b.h-b.bor[0]-b.bor[2]
	clientW, clientH := pw-b.sbW, ph-b.sbH
	totalW, totalH := b.scrollR-b.ax-b.bor[3], b.scrollB-b.ay-b.bor[0]
	thumb := func(barLen, visible, total, padLen float64) (float64, float64) {
		inner := barLen - 36
		if inner <= 0 || total <= padLen+0.5 {
			return 0, 0
		}
		l := inner * visible / total
		if l < 17 {
			l = 17
		}
		if l > inner {
			l = inner
		}
		return 18, l
	}
	if b.sbW > 0 {
		r := Rect{x0 + pw - b.sbW, y0, b.sbW, ph - b.sbH}
		off, ln := thumb(r.H, clientH, totalH, ph)
		p.emit(Scrollbar{Rect: r, Vertical: true, ThumbOff: off, ThumbLen: ln, Dark: dark})
	}
	if b.sbH > 0 {
		r := Rect{x0, y0 + ph - b.sbH, pw - b.sbW, b.sbH}
		off, ln := thumb(r.W, clientW, totalW, pw)
		p.emit(Scrollbar{Rect: r, Vertical: false, ThumbOff: off, ThumbLen: ln, Dark: dark})
	}
	if b.sbW > 0 && b.sbH > 0 {
		p.emit(ScrollCorner{Rect: Rect{x0 + pw - b.sbW, y0 + ph - b.sbH, b.sbW, b.sbH}, Dark: dark})
	}
}

// paintViewportScrollbars draws the page scrollbars (scroll offset 0) that
// layout reserved at the right / bottom of the viewport.
func (p *painter) paintViewportScrollbars() {
	c := p.c
	if c.sbViewport <= 0 && c.sbViewportH <= 0 {
		return
	}
	dark := c.opts.ColorScheme == "dark"
	vw, vh := c.vw, c.vh
	thumb := func(barLen, visible, total float64) (float64, float64) {
		inner := barLen - 36
		if inner <= 0 || total <= visible+0.5 {
			return 0, 0
		}
		l := inner * visible / total
		if l < 17 {
			l = 17
		}
		if l > inner {
			l = inner
		}
		return 18, l
	}
	if c.sbViewport > 0 {
		r := Rect{vw - c.sbViewport, 0, c.sbViewport, vh - c.sbViewportH}
		off, ln := thumb(r.H, vh-c.sbViewportH, c.res.Height)
		p.emit(Scrollbar{Rect: r, Vertical: true, ThumbOff: off, ThumbLen: ln, Dark: dark})
	}
	if c.sbViewportH > 0 {
		r := Rect{0, vh - c.sbViewportH, vw - c.sbViewport, c.sbViewportH}
		off, ln := thumb(r.W, vw-c.sbViewport, c.res.Width)
		p.emit(Scrollbar{Rect: r, Vertical: false, ThumbOff: off, ThumbLen: ln, Dark: dark})
	}
	if c.sbViewport > 0 && c.sbViewportH > 0 {
		p.emit(ScrollCorner{Rect: Rect{vw - c.sbViewport, vh - c.sbViewportH, c.sbViewport, c.sbViewportH}, Dark: dark})
	}
}

// emitSVGTiles paints a tiled SVG background image: one SVGDraw per tile,
// clipped to the layer's clip box.
func (p *painter) emitSVGTiles(b *lbox, doc *SVGDoc, clip Rect, rad Radii, t *Tile) {
	if doc != nil && doc.Base != "" {
		if sd := p.c.svgImageDocSized(doc.Base, t.Size[0], t.Size[1]); sd != nil {
			doc = sd
		}
	}
	p.emit(PushClip{Rect: clip, Radii: rad})
	starts := func(origin, step, size, lo, hi float64, repeat bool) []float64 {
		if !repeat {
			return []float64{origin}
		}
		if step <= 0 {
			return []float64{origin}
		}
		k := math.Floor((lo - origin) / step)
		var out []float64
		for x := origin + k*step; x < hi && len(out) < 2000; x += step {
			if x+size > lo {
				out = append(out, x)
			}
		}
		return out
	}
	xs := starts(t.Origin[0], t.Step[0], t.Size[0], clip.X, clip.X+clip.W, t.RepeatX)
	ys := starts(t.Origin[1], t.Step[1], t.Size[1], clip.Y, clip.Y+clip.H, t.RepeatY)
	n := 0
	for _, y := range ys {
		for _, x := range xs {
			if n++; n > 1500 {
				break
			}
			p.emit(SVGDraw{Dst: Rect{x, y, t.Size[0], t.Size[1]}, Doc: doc, Color: b.st.Color, FontSize: 16, Family: "serif", Weight: 400})
		}
	}
	p.emit(PopClip{})
}
