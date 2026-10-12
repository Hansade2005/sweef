package engine

import "image"

// gui_export.go: two additive hooks used by internal/app (the GUI layer).

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

// PaintRGBA rasterises the page straight to an RGBA image (no PNG round
// trip). width/height are CSS pixels; the result is (width*dsf)x(height*dsf).
// full paints the whole document height (capped) so the caller can scroll
// the result without asking the engine again. ok is false when the render
// pipeline is not active for this document.
func (t *Tab) PaintRGBA(width, height int, dsf float64, full bool) (*image.RGBA, bool) {
	if width <= 0 {
		width = 1280
	}
	if dsf <= 0 {
		dsf = 1
	}
	return t.paintPage(width, height, dsf, full)
}
