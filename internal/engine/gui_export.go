package engine

import (
	"image"
	"math"
	"time"

	"github.com/sweefbrowser/sweef/internal/raster"
	"github.com/sweefbrowser/sweef/internal/render"
)

// gui_export.go: additive hooks used by internal/app (the GUI layer). They
// only expose what paintPage already computes.

// Generation returns the document's mutation counter (0 without a document).
// It changes whenever the DOM or computed styles change, so a compositor can
// skip repainting an unchanged tab.
func (t *Tab) Generation() uint64 {
	t.mu.RLock()
	d := t.dom
	t.mu.RUnlock()
	if d == nil {
		return 0
	}
	return d.MutationGen()
}

// PaintOptions selects how PaintRGBAOpts rasterises.
type PaintOptions struct {
	Width, Height  int     // CSS px; Height <= 0 uses the tab viewport height
	DSF            float64 // device pixel ratio
	Full           bool    // paint the whole document height (capped at 16000 CSS px)
	Transparent    bool    // leave the canvas transparent (premultiplied alpha), for overlays
	KeepScrollbars bool    // keep the page's own scrollbars (default: removed; the GUI draws its own)
	// Incremental repaints only what changed since the previous incremental
	// paint of this tab (see incremental.go); the result is then the damaged
	// sub-rectangle of the surface unless the whole surface had to be repainted.
	Incremental bool
}

// PaintRGBA rasterises the page straight to an RGBA image (no PNG round
// trip) with an opaque white canvas and no page scrollbars. width/height are
// CSS pixels; the result is (width*dsf)x(height*dsf).
func (t *Tab) PaintRGBA(width, height int, dsf float64, full bool) (*image.RGBA, bool) {
	return t.PaintRGBAOpts(PaintOptions{Width: width, Height: height, DSF: dsf, Full: full})
}

// PaintRGBAOpts is PaintRGBA with options. ok is false when the render
// pipeline is not active for this document.
func (t *Tab) PaintRGBAOpts(o PaintOptions) (*image.RGBA, bool) {
	t.mu.RLock()
	doc := t.dom
	vh := t.vpH
	t.mu.RUnlock()
	if doc == nil || !renderEnabled() {
		return nil, false
	}
	if o.Width <= 0 {
		o.Width = 1280
	}
	if o.DSF <= 0 {
		o.DSF = 1
	}
	t0 := time.Now()
	l := doc.Layout(o.Width)
	tLayout := time.Since(t0)
	ext, _ := l.Ext.(*renderExt)
	if ext == nil {
		return nil, false
	}
	height := o.Height
	if o.Full {
		height = int(math.Ceil(ext.res.Height))
		if height < 1 {
			height = 1
		}
		if height > 16000 {
			height = 16000
		}
	}
	if height <= 0 {
		height = vh
		if height <= 0 {
			height = 720
		}
	}
	list := ext.res.List
	if !o.KeepScrollbars {
		kept := make([]render.Op, 0, len(list))
		for _, op := range list {
			switch op.(type) {
			case render.Scrollbar, render.ScrollCorner:
				// only the root scrollbars: element scrollbars are not top-left anchored
				if isRootScrollbar(op, ext.res.Viewport) {
					continue
				}
			}
			kept = append(kept, op)
		}
		list = kept
	}
	env := raster.PaintEnv{Bitmap: ext.env.Bitmap}
	if !o.Transparent {
		env.Background = raster.Color{R: 1, G: 1, B: 1, A: 1}
	}
	t1 := time.Now()
	var img *image.RGBA
	if o.Incremental {
		img = t.paintIncremental(list, o.Width, height, o.DSF, env)
	} else {
		img = raster.Paint(list, o.Width, height, o.DSF, env)
	}
	t.lastPaint.Store(&PaintInfo{Layout: tLayout, Raster: time.Since(t1), Incremental: o.Incremental, Rect: img.Rect})
	return img, true
}

// PaintInfo describes the last PaintRGBAOpts of a tab.
type PaintInfo struct {
	Layout, Raster time.Duration // style + layout + display list, then rasterisation
	Incremental    bool
	Rect           image.Rectangle // the part of the surface the returned image covers
}

// LastPaintInfo reports how the last PaintRGBAOpts went (nil before the first).
func (t *Tab) LastPaintInfo() *PaintInfo { return t.lastPaint.Load() }

func isRootScrollbar(op render.Op, vp render.Rect) bool {
	switch o := op.(type) {
	case render.Scrollbar:
		if o.Vertical {
			return o.Rect.Y == 0 && o.Rect.X+o.Rect.W >= vp.W-0.5
		}
		return o.Rect.X == 0 && o.Rect.Y+o.Rect.H >= vp.H-0.5
	case render.ScrollCorner:
		return o.Rect.X+o.Rect.W >= vp.W-0.5 && o.Rect.Y+o.Rect.H >= vp.H-0.5
	}
	return false
}
