package render

import (
	"golang.org/x/net/html"

	"github.com/sweefbrowser/sweef/internal/style"
)

// content-visibility: auto.
//
// Chromium skips the contents of an auto element while it is not relevant to
// the user. A skipped element is size-contained: with no contain-intrinsic-size
// its block size is 0 (plus padding and border) and its contents are neither
// painted nor take space. An element is relevant while its box intersects the
// viewport grown by 150% of the viewport size on every side (measured against
// Chromium 1194: with an 800x400 viewport and absolutely positioned sections,
// the ones whose top is below 1000px = 2.5 x 400 are skipped), while it
// contains focus or the selection, or while it is content-visibility:visible.
// The first layout starts with every auto subtree skipped, the ones that reach
// the grown viewport are opened and laid out again (positions only move away
// from the viewport when a subtree grows, and Chromium does not close a subtree
// it has opened), so a page of N auto sections shows the first few in full and
// the rest at their contain-intrinsic-size. Skipped contents are still laid out
// for geometry queries but take no space, like content-visibility: hidden.
//
// Not done: the `auto` keyword in contain-intrinsic-size remembering the last
// rendered size (a static load never rendered the element, so its fallback
// length is what Chromium uses too), reopening on scroll, find-in-page and
// anchor navigation.

type cvState struct {
	open  map[*html.Node]bool // auto elements opened so far
	cands []*html.Node        // skipped auto elements outside any skipped subtree
	fast  bool                // sizing rounds: skipped subtrees are not built
}

// cvDecide returns a skipped copy of st unless the element is already open or
// relevant. The style is copied: the engine shares Computed values between
// elements.
func (c *ctx) cvDecide(n *html.Node, st *style.Computed) *style.Computed {
	if c.cv == nil {
		return st
	}
	if c.cvDepth == 0 {
		if c.cv.open[n] || c.opts.CVRelevant != nil && c.opts.CVRelevant(n) {
			return st
		}
		c.cv.cands = append(c.cv.cands, n)
	}
	cp := *st
	cp.CVSkip = true
	c.styles[n] = &cp
	return &cp
}

// openRelevant opens the candidates that reach the grown viewport and reports
// whether any did.
func (cv *cvState) openRelevant(res *Result, o Options) bool {
	if len(cv.cands) == 0 {
		return false
	}
	w, h := o.Width, o.Height
	x0, y0 := o.ScrollX-1.5*w, o.ScrollY-1.5*h
	x1, y1 := o.ScrollX+2.5*w, o.ScrollY+2.5*h
	opened := false
	for _, n := range cv.cands {
		b := res.Boxes[n]
		if b == nil || b.Display == "none" {
			continue
		}
		r := b.Border
		if r.X < x1 && r.X+r.W > x0 && r.Y < y1 && r.Y+r.H > y0 {
			cv.open[n] = true
			opened = true
		}
	}
	return opened
}
