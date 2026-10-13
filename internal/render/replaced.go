package render

import (
	"image"
	"math"
	"strconv"
	"strings"

	"github.com/sweefbrowser/sweef/internal/style"
)

func parseDim(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasSuffix(s, "%") {
		return 0, false
	}
	i := 0
	for i < len(s) && (s[i] >= '0' && s[i] <= '9' || s[i] == '.') {
		i++
	}
	if i == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(s[:i], 64)
	return v, err == nil
}

func (c *ctx) loadImage(r *replacedInfo) {
	if r.url == "" || c.env == nil {
		return
	}
	if im := c.env.Image(r.url); im != nil {
		r.img = im
		bnd := im.Bounds()
		r.iw, r.ih = float64(bnd.Dx()), float64(bnd.Dy())
		r.hasIW, r.hasIH = true, true
		if r.ih > 0 {
			r.ratio = r.iw / r.ih
		}
		return
	}
	if w, h, hasW, hasH, ratio, ok := c.svgImageIntrinsic(r.url); ok {
		r.iw, r.ih, r.hasIW, r.hasIH, r.ratio = w, h, hasW, hasH, ratio
	}
}

// setupReplaced fills b.rep for replaced elements and controls. It returns
// false when the element should be treated as ordinary content.
func (c *ctx) setupReplaced(b *lbox) bool {
	n := b.n
	r := &replacedInfo{kind: n.Data}
	switch n.Data {
	case "img":
		if attr(n, "data-sweef-lite") == "skipped" {
			setupLite(r, n, "skipped")
			break
		}
		src := attr(n, "src")
		if src == "" {
			if ss := attr(n, "srcset"); ss != "" {
				src = strings.Fields(strings.Split(ss, ",")[0])[0]
			}
		}
		r.url = c.resolveURL(src)
		c.loadImage(r)
		if r.img == nil {
			// attributes give an aspect ratio for the layout of not-yet-loaded images
			w, okw := parseDim(attr(n, "width"))
			h, okh := parseDim(attr(n, "height"))
			if okw && okh && h > 0 {
				r.ratio = w / h
			}
			if src == "" || true {
				alt := attr(n, "alt")
				if alt != "" && !okw && !okh && false {
					return false
				}
			}
		}
	case "canvas":
		r.iw, r.ih, r.hasIW, r.hasIH = 300, 150, true, true
		if w, ok := parseDim(attr(n, "width")); ok {
			r.iw = w
		}
		if h, ok := parseDim(attr(n, "height")); ok {
			r.ih = h
		}
		if c.env != nil {
			if w, h, ok := c.env.IntrinsicSize(n); ok {
				r.iw, r.ih = w, h
			}
			if bm := c.env.Bitmap(n); bm != nil {
				r.bitmap = true
				r.img = bm
			}
		}
		if r.ih > 0 {
			r.ratio = r.iw / r.ih
		}
	case "video":
		r.iw, r.ih, r.hasIW, r.hasIH = 300, 150, true, true
		if c.env != nil {
			if w, h, ok := c.env.IntrinsicSize(n); ok && w > 0 {
				r.iw, r.ih = w, h
				r.ratio = w / h
			}
			if bm := c.env.Bitmap(n); bm != nil {
				r.bitmap = true
				r.img = bm
			}
		}
	case "audio":
		r.iw, r.ih, r.hasIW, r.hasIH = 300, 54, true, true
	case "iframe", "embed", "object":
		r.iw, r.ih, r.hasIW, r.hasIH = 300, 150, true, true
		if n.Data == "iframe" && attr(n, "data-sweef-lite") == "frame" {
			setupLite(r, n, "frame")
		}
	case "svg":
		r.iw, r.ih, r.hasIW, r.hasIH = 300, 150, true, true
		w, okw := parseDim(attr(n, "width"))
		h, okh := parseDim(attr(n, "height"))
		if vb := strings.Fields(strings.ReplaceAll(attr(n, "viewBox"), ",", " ")); len(vb) == 4 {
			vw, _ := strconv.ParseFloat(vb[2], 64)
			vh, _ := strconv.ParseFloat(vb[3], 64)
			if vw > 0 && vh > 0 {
				r.ratio = vw / vh
				if !okw && !okh {
					r.hasIW, r.hasIH = false, false
				} else if okw && !okh {
					h, okh = w/r.ratio, true
				} else if okh && !okw {
					w, okw = h*r.ratio, true
				}
			}
		}
		if okw {
			r.iw = w
		}
		if okh {
			r.ih = h
		}
		if r.ratio == 0 && r.ih > 0 {
			r.ratio = r.iw / r.ih
		}
	case "input":
		t := strings.ToLower(attr(n, "type"))
		if t == "hidden" {
			return false
		}
		r.ctrl = t
		if r.ctrl == "" {
			r.ctrl = "text"
		}
		if t == "image" {
			r.kind = "img"
			r.ctrl = ""
			r.url = c.resolveURL(attr(n, "src"))
			c.loadImage(r)
		}
	case "select", "textarea", "progress", "meter":
		r.ctrl = n.Data
	default:
		return false
	}
	b.rep = r
	return true
}

// replacedIntrinsic gives the intrinsic content widths of replaced boxes.
func (c *ctx) replacedIntrinsic(b *lbox) (float64, float64) {
	if b.inner == iControl {
		ci := c.controlInfo(b)
		w := ci.cw
		if wv, ok := c.specW(b, b.st.Width, 0); ok && b.st.Width.K == style.LPx {
			w = wv
		}
		return w, w
	}
	st := b.st
	if wv, ok := c.specW(b, st.Width, 0); ok && st.Width.K == style.LPx {
		return wv, wv
	}
	w, _ := c.replacedUsedSize(b, 0, -1, false)
	return w, w
}

// replacedUsedSize computes the content-box size of a replaced element.
// cbw < 0 / forAbs: no containing width to fill.
func (c *ctx) replacedUsedSize(b *lbox, cbw, cbh float64, intrinsicOnly bool) (float64, float64) {
	st := b.st
	r := b.rep
	var iw, ih float64
	hasW, hasH := r.hasIW, r.hasIH
	iw, ih = r.iw, r.ih
	ratio := r.ratio
	if st.AspectRatio > 0 && (!st.AspectAuto || ratio == 0) {
		ratio = st.AspectRatio
	}
	c.edges(b, math.Max(cbw, 0))
	w, wOK := 0.0, false
	h, hOK := 0.0, false
	if !intrinsicOnly || true {
		if st.Width.K == style.LPx || (st.Width.K == style.LPct || st.Width.K == style.LCalc) && cbw > 0 {
			w, wOK = c.specW(b, st.Width, cbw)
		} else if st.Width.IsIntrinsic() {
			w, wOK = c.specW(b, st.Width, cbw)
		}
		if st.Height.K == style.LPx || (st.Height.K == style.LPct || st.Height.K == style.LCalc) && cbh >= 0 {
			h, hOK = c.specH(b, st.Height, cbh)
		}
	}
	switch {
	case wOK && hOK:
	case wOK:
		if ratio > 0 {
			h = w / ratio
			if st.BoxSizing == "border-box" {
				// ratio applies to the content box
			}
		} else if hasH {
			h = ih
		} else {
			h = 150
		}
	case hOK:
		if ratio > 0 {
			w = h * ratio
		} else if hasW {
			w = iw
		} else {
			w = 300
		}
	default:
		switch {
		case hasW && hasH:
			w, h = iw, ih
			if st.AspectRatio > 0 && !st.AspectAuto {
				// a preferred aspect ratio replaces the natural one: keep the width
				h = w / ratio
			}
		case hasW:
			w = iw
			if ratio > 0 {
				h = w / ratio
			} else {
				h = 150
			}
		case hasH:
			h = ih
			if ratio > 0 {
				w = h * ratio
			} else {
				w = 300
			}
		case ratio > 0:
			// fill the containing block width
			if cbw > 0 {
				w = cbw - b.mar[1] - b.mar[3] - b.hExtra()
				if w < 0 {
					w = 0
				}
				h = w / ratio
			} else {
				w, h = 300, 300/ratio
			}
		default:
			if r.kind == "img" {
				w, h = 0, 0
			} else {
				w, h = 300, 150
			}
		}
	}
	// min / max constraints, preserving the ratio when both were auto
	mxw, mnw := math.Inf(1), 0.0
	if st.MaxWidth.K != style.LNone && st.MaxWidth.K != style.LAuto {
		if v, ok := c.specW(b, st.MaxWidth, cbw); ok {
			mxw = v
		}
	}
	if v, ok := c.specW(b, st.MinWidth, cbw); ok {
		mnw = v
	}
	mxh, mnh := math.Inf(1), 0.0
	if st.MaxHeight.K != style.LNone && st.MaxHeight.K != style.LAuto {
		if v, ok := c.specH(b, st.MaxHeight, cbh); ok {
			mxh = v
		}
	}
	if v, ok := c.specH(b, st.MinHeight, cbh); ok {
		mnh = v
	}
	if ratio > 0 && !wOK && !hOK {
		// CSS 2.1 10.4 table, simplified
		cw, ch := w, h
		if cw > mxw {
			cw, ch = mxw, mxw/ratio
		}
		if ch > mxh {
			ch, cw = mxh, mxh*ratio
		}
		if cw < mnw {
			cw, ch = mnw, mnw/ratio
		}
		if ch < mnh {
			ch, cw = mnh, mnh*ratio
		}
		return cw, ch
	}
	if ratio > 0 && wOK && !hOK {
		if w > mxw {
			w = mxw
		}
		if w < mnw {
			w = mnw
		}
		h = w / ratio
		return w, math.Max(mnh, math.Min(mxh, h))
	}
	if ratio > 0 && hOK && !wOK {
		h = math.Max(mnh, math.Min(mxh, h))
		w = h * ratio
		return math.Max(mnw, math.Min(mxw, w)), h
	}
	return math.Max(mnw, math.Min(mxw, w)), math.Max(mnh, math.Min(mxh, h))
}

func (c *ctx) layoutReplaced(b *lbox, in layoutIn) {
	var w, h float64
	cbw := in.cbw
	if in.shrink && b.kind != kBlock {
		cbw = in.cbw
	}
	w, h = c.replacedUsedSize(b, cbw, in.cbh, false)
	if in.fixW >= 0 {
		w = math.Max(0, in.fixW-b.hExtra())
	}
	if in.fixH >= 0 {
		h = math.Max(0, in.fixH-b.vExtra())
	}
	b.w = w + b.hExtra()
	b.h = h + b.vExtra()
	b.contentH = h
	b.mtopOut, b.mbotOut = b.mar[0], b.mar[2]
	b.through = false
	b.hasBase = false
	if b.kind == kBlock && !b.floated && !b.oof {
		c.horizontalMargins(b, in, b.w)
	}
}

var _ image.Image
