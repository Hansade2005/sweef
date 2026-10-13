package compositor

import (
	"image"
	"image/color"
	"testing"
)

func TestSnapshotPageAndWindow(t *testing.T) {
	c := New(200, 150, 1)
	c.SetViewport(image.Rect(0, 50, 200, 150))
	page := image.NewRGBA(image.Rect(0, 0, 200, 400))
	for y := 0; y < 400; y++ {
		for x := 0; x < 200; x++ {
			page.SetRGBA(x, y, color.RGBA{uint8(y / 2), 10, 20, 255})
		}
	}
	c.SetPage(page)
	chrome := image.NewRGBA(image.Rect(0, 0, 200, 150))
	for y := 0; y < 50; y++ { // an opaque toolbar above the viewport and a popover blob inside it
		for x := 0; x < 200; x++ {
			chrome.SetRGBA(x, y, color.RGBA{0, 0, 255, 255})
		}
	}
	for y := 60; y < 80; y++ {
		for x := 0; x < 40; x++ {
			chrome.SetRGBA(x, y, color.RGBA{255, 255, 0, 255})
		}
	}
	c.SetChrome(chrome)
	c.Compose()
	g0 := c.Gen()

	pv, g := c.SnapshotPage(nil)
	if pv.Bounds().Dx() != 200 || pv.Bounds().Dy() != 100 || g != g0 {
		t.Fatalf("page snapshot %v gen %d/%d", pv.Bounds(), g, g0)
	}
	if got := pv.RGBAAt(5, 0); got.R != 0 || got.G != 10 {
		t.Fatalf("top row is %v, want page row 0", got)
	}
	if got := pv.RGBAAt(5, 15); got.B != 20 || got.R != 7 {
		t.Fatalf("a popover blob must not appear in a tab share: %v", got)
	}
	c.ScrollTo(100)
	c.Compose()
	pv, g2 := c.SnapshotPage(pv)
	if got := pv.RGBAAt(0, 0); got.R != 50 {
		t.Fatalf("scrolled snapshot starts with %v, want page row 100", got)
	}
	if g2 == g0 {
		t.Fatal("generation did not advance after scrolling")
	}

	w, gw := c.SnapshotWindow(nil)
	if w.Bounds().Dx() != 200 || w.Bounds().Dy() != 150 || gw != g2 {
		t.Fatalf("window snapshot %v", w.Bounds())
	}
	if got := w.RGBAAt(5, 5); got.B != 255 {
		t.Fatalf("window snapshot lacks the toolbar: %v", got)
	}
	if got := w.RGBAAt(5, 65); got.R != 255 || got.G != 255 {
		t.Fatalf("window snapshot lacks the popover: %v", got)
	}
	// nothing changed: the generation holds so captures can skip conversion
	c.Compose()
	if c.Gen() != g2 {
		t.Fatal("generation moved without damage")
	}
	// a page shorter than the viewport pads with the background
	c.SetPage(image.NewRGBA(image.Rect(0, 0, 100, 30)))
	pv, _ = c.SnapshotPage(pv)
	if got := pv.RGBAAt(150, 80); got != (color.RGBA{255, 255, 255, 255}) {
		t.Fatalf("padding %v", got)
	}
}
