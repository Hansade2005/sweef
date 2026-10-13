package engine

import (
	"bytes"
	"fmt"
	"image"
	"math"
	"os"

	"github.com/sweefbrowser/sweef/internal/raster"
	"github.com/sweefbrowser/sweef/internal/render"
)

// incremental.go: PaintOptions.Incremental. The tab keeps a raster.Canvas with
// the pixels of its last incremental paint; a new paint repaints only the
// rectangle that the change of the display list can have touched (see
// raster/incremental.go) and returns that rectangle.

// paintIncremental brings the tab's canvas to list and returns the pixels the
// caller has to install: the whole surface when it had to be repainted fully
// (first paint, new size or scale, too much changed), otherwise only the
// damaged rectangle, as an image whose Rect is that rectangle in surface
// coordinates (an empty image when nothing changed). The result is a copy, so
// it stays valid while the canvas is repainted.
func (t *Tab) paintIncremental(list []render.Op, w, h int, dpr float64, env raster.PaintEnv) *image.RGBA {
	dw, dh := int(math.Round(float64(w)*dpr)), int(math.Round(float64(h)*dpr))
	t.incrMu.Lock()
	defer t.incrMu.Unlock()
	cv := t.incr
	if !cv.Matches(dw, dh, dpr, env) {
		cv = raster.NewCanvas(dw, dh, dpr, env)
		cv.Paint(list)
		t.incr = cv
		return copyRGBA(cv.Image(), cv.Image().Rect)
	}
	bands, full := cv.Update(list)
	if incrCheck {
		checkIncremental(cv, list, w, h, dpr, env)
	}
	if full {
		return copyRGBA(cv.Image(), cv.Image().Rect)
	}
	if len(bands) == 0 {
		return &image.RGBA{}
	}
	// the caller installs one rectangle: the hull of the repainted bands
	hull := image.Rect(bands[0].X0, bands[0].Y0, bands[0].X1, bands[0].Y1)
	for _, b := range bands[1:] {
		hull = hull.Union(image.Rect(b.X0, b.Y0, b.X1, b.Y1))
	}
	return copyRGBA(cv.Image(), hull)
}

// SWEEF_INCR_CHECK=1 compares every incremental paint with a full paint of the
// same display list (a development aid: it costs a full paint per frame) and
// reports and repairs a difference.
var incrCheck = os.Getenv("SWEEF_INCR_CHECK") != ""

func checkIncremental(cv *raster.Canvas, list []render.Op, w, h int, dpr float64, env raster.PaintEnv) {
	want := raster.Paint(list, w, h, dpr, env)
	if bytes.Equal(want.Pix, cv.Image().Pix) {
		return
	}
	fmt.Fprintf(os.Stderr, "sweef: incremental paint differs from a full paint (%dx%d at %gx); repainting fully\n", w, h, dpr)
	cv.Paint(list)
}

// copyRGBA copies the pixels of r (absolute coordinates) of src.
func copyRGBA(src *image.RGBA, r image.Rectangle) *image.RGBA {
	r = r.Intersect(src.Rect)
	if r.Empty() {
		return &image.RGBA{}
	}
	out := image.NewRGBA(r)
	n := 4 * r.Dx()
	for y := r.Min.Y; y < r.Max.Y; y++ {
		copy(out.Pix[(y-r.Min.Y)*out.Stride:][:n], src.Pix[src.PixOffset(r.Min.X, y):][:n])
	}
	return out
}
