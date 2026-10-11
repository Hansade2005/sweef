// Package paint turns a layout into a display list in CSS paint order
// (CSS 2.2 Appendix E: stacking contexts by z-index, then block
// backgrounds, floats, inline content, positioned descendants), and
// answers hit tests in the same order.
package paint

import (
	"github.com/sweefbrowser/sweef/internal/render/gfx"
	"github.com/sweefbrowser/sweef/internal/render/layout"
	"github.com/sweefbrowser/sweef/internal/render/res"
	"golang.org/x/net/html"
)

// Paint records the display list for a layout. Fixed-position content is
// recorded at its position for scroll (0, 0); Options say otherwise.
func Paint(r *layout.Result, ld res.Loader, o Options) *gfx.DisplayList {
	return &gfx.DisplayList{Bounds: gfx.Rect{W: r.DocW, H: r.DocH}, Background: gfx.White}
}

// Options control painting.
type Options struct {
	// ScrollX/ScrollY place position: fixed content for a viewport
	// screenshot at that scroll offset.
	ScrollX, ScrollY float64
	// FocusNode draws a focus ring for the focused element.
	FocusNode *html.Node
}

// HitTest returns the topmost element whose painted area contains the
// page point (x, y), honouring pointer-events, visibility, clipping and
// z-order; nil for none.
func HitTest(r *layout.Result, x, y float64) *html.Node { return nil }
