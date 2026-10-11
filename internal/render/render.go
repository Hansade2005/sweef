package render

import (
	"image"
	"math"
	"sync"

	fnet "github.com/sweefbrowser/sweef/internal/net"
	"github.com/sweefbrowser/sweef/internal/render/gfx"
	"github.com/sweefbrowser/sweef/internal/render/layout"
	"github.com/sweefbrowser/sweef/internal/render/paint"
	"github.com/sweefbrowser/sweef/internal/render/res"
	"github.com/sweefbrowser/sweef/internal/render/style"
	"golang.org/x/net/html"
)

// The pipeline: style.Resolver -> layout.Layout -> paint.Paint ->
// gfx.DisplayList.Replay. Install registers it as the document layout
// engine so element boxes, elementFromPoint and screenshots share one
// layout.

var (
	loaderMu sync.RWMutex
	loaders  = map[*fnet.Doc]res.Loader{}
)

// SetLoader attaches the image loader for a document (the engine does this
// when it installs a document); nil removes it.
func SetLoader(d *fnet.Doc, l res.Loader) {
	loaderMu.Lock()
	if l == nil {
		delete(loaders, d)
	} else {
		loaders[d] = l
	}
	loaderMu.Unlock()
}

func loaderFor(d *fnet.Doc) res.Loader {
	loaderMu.RLock()
	l := loaders[d]
	loaderMu.RUnlock()
	if l == nil {
		return res.NoLoader{}
	}
	return l
}

// Install makes the render pipeline the layout engine for all documents.
func Install() {
	fnet.LayoutEngine = func(d *fnet.Doc, w, h int) *fnet.Layout {
		r := LayoutDoc(d, float64(w), float64(h))
		out := &fnet.Layout{Width: w, Height: int(math.Ceil(r.DocH)), Boxes: map[*html.Node][4]int{}, Engine: r}
		for n := range r.Fragments {
			if n.Type != html.ElementNode {
				continue
			}
			x, y, bw, bh, ok := r.BorderBox(n)
			if !ok {
				continue
			}
			out.Boxes[n] = [4]int{int(math.Round(x)), int(math.Round(y)), int(math.Round(bw)), int(math.Round(bh))}
		}
		out.Hit = func(x, y float64) *html.Node { return paint.HitTest(r, x, y) }
		return out
	}
}

// LayoutDoc runs style and layout for d at a viewport size.
func LayoutDoc(d *fnet.Doc, vw, vh float64) *layout.Result {
	sr := style.NewResolver(d, vw, vh)
	return layout.Layout(d, sr, loaderFor(d), vw, vh)
}

// Options for Render.
type Options struct {
	Width, Height int     // viewport in CSS px
	DPR           float64 // device pixel ratio (1 when 0)
	FullPage      bool    // capture the whole document height
	ScrollX       float64
	ScrollY       float64
	// Clip, when non-empty, crops the capture to a page rectangle.
	Clip gfx.Rect
}

// Prepare runs layout and paint without rasterising, so every image the
// frame needs has been requested from the loader.
func Prepare(d *fnet.Doc, o Options) {
	if o.Width <= 0 {
		o.Width = 1280
	}
	if o.Height <= 0 {
		o.Height = 720
	}
	var r *layout.Result
	if l := d.Layout(o.Width); l != nil {
		r, _ = l.Engine.(*layout.Result)
	}
	if r == nil {
		r = LayoutDoc(d, float64(o.Width), float64(o.Height))
	}
	applyScroll(d, r)
	paint.Paint(r, loaderFor(d), paint.Options{ScrollX: o.ScrollX, ScrollY: o.ScrollY})
}

// Render lays out and paints d, returning premultiplied RGBA pixels.
func Render(d *fnet.Doc, o Options) *image.RGBA {
	if o.Width <= 0 {
		o.Width = 1280
	}
	if o.Height <= 0 {
		o.Height = 720
	}
	if o.DPR <= 0 {
		o.DPR = 1
	}
	var r *layout.Result
	if l := d.Layout(o.Width); l != nil {
		r, _ = l.Engine.(*layout.Result)
	}
	if r == nil {
		r = LayoutDoc(d, float64(o.Width), float64(o.Height))
	}
	applyScroll(d, r)
	dl := paint.Paint(r, loaderFor(d), paint.Options{ScrollX: o.ScrollX, ScrollY: o.ScrollY})
	x, y, w, h := o.ScrollX, o.ScrollY, float64(o.Width), float64(o.Height)
	if o.FullPage {
		x, y = 0, 0
		w, h = math.Max(w, r.DocW), math.Max(h, r.DocH)
	}
	if !o.Clip.Empty() {
		x, y, w, h = o.Clip.X, o.Clip.Y, o.Clip.W, o.Clip.H
	}
	pw, ph := int(math.Ceil(w*o.DPR)), int(math.Ceil(h*o.DPR))
	return dl.Replay(pw, ph, x, y, o.DPR).Img
}

// applyScroll copies element scroll positions (scrollTop/scrollLeft set by
// script) onto the scroll containers' boxes for painting.
func applyScroll(d *fnet.Doc, r *layout.Result) {
	for n, fs := range r.Fragments {
		x, y := d.ScrollOffset(n)
		for _, f := range fs {
			f.ScrollX, f.ScrollY = x, y
		}
	}
}
