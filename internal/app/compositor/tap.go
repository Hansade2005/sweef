package compositor

import "image"

// tap.go: read access for screen and tab capture (getDisplayMedia). Nothing here runs unless a capture asks:
// the compositor does no extra work for the tap, a caller polls at its own frame rate and copies under the same
// mutex that guards composing.

// Gen counts the frames composed with real damage. A capture source compares it with the value it saw last and
// resends its previous frame when nothing changed instead of converting identical pixels again.
func (c *Compositor) Gen() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.frames
}

func reuse(dst *image.RGBA, r image.Rectangle) *image.RGBA {
	if dst == nil || dst.Rect.Dx() != r.Dx() || dst.Rect.Dy() != r.Dy() {
		return image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	}
	return dst
}

// SnapshotWindow copies the last composed frame (chrome, page and overlay: the whole browser window) into dst,
// allocating when dst is nil or the wrong size, and returns it with the generation it shows.
func (c *Compositor) SnapshotWindow(dst *image.RGBA) (*image.RGBA, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	dst = reuse(dst, c.frame.Bounds())
	copy(dst.Pix, c.frame.Pix)
	return dst, c.frames
}

// SnapshotPage copies what the viewport shows of the page texture, at the current scroll position, without
// the chrome and its popovers drawn over it. It is the "this tab" source: a permission prompt or the site-info
// popover never ends up in a shared tab.
func (c *Compositor) SnapshotPage(dst *image.RGBA) (*image.RGBA, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	vp := c.vp
	dst = reuse(dst, vp)
	w, h := vp.Dx(), vp.Dy()
	for y := 0; y < h; y++ {
		row := dst.Pix[y*dst.Stride : y*dst.Stride+4*w]
		py := y + c.scroll
		if c.page == nil || py >= c.pageH || py < 0 {
			fillRow(row, c.bg)
			continue
		}
		n := min(w, c.page.Bounds().Dx())
		so := c.page.PixOffset(0, py)
		copy(row[:4*n], c.page.Pix[so:so+4*n])
		if n < w {
			fillRow(row[4*n:], c.bg)
		}
	}
	return dst, c.frames
}
