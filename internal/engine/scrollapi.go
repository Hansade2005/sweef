package engine

import (
	"math"

	"golang.org/x/net/html"
)

// scrollapi.go: the viewport's scroll position as page script sees it, and how script asks for it to change
// (window.scrollTo, scrollBy, scrollIntoView, anchor navigation).

// ScrollRequest is page script asking the window to scroll the viewport.
type ScrollRequest struct {
	X, Y   float64 // the document offset to scroll to, CSS px, already within the scrollable range
	Smooth bool    // behavior: smooth, or scroll-behavior: smooth on the root element
}

// SetScrollHook registers who moves the viewport when page script asks. With a hook set, script's request does
// not change the scroll position by itself: the window scrolls (animating when it is smooth), reports the new
// offset with NoteScrollY and fires the scroll events. Without one (headless) the position changes at once.
func (t *Tab) SetScrollHook(f func(ScrollRequest)) {
	if f == nil {
		t.scrollHook.Store(nil)
		return
	}
	t.scrollHook.Store(&f)
}

// NoteScrollY records the viewport's scroll offset (CSS px) so that window.scrollY and getBoundingClientRect
// are right at once. The window calls it for every frame of a scroll.
func (t *Tab) NoteScrollY(y int) {
	if y < 0 {
		y = 0
	}
	t.ds.scrollY.Store(int64(y))
}

// scrollWindow handles window.scrollTo from script. It reports whether the position changed synchronously.
func (t *Tab) scrollWindow(x, y float64, behavior string) bool {
	_ = x // the viewport scrolls vertically only
	t.mu.RLock()
	d := t.dom
	t.mu.RUnlock()
	if d == nil || math.IsNaN(y) || math.IsInf(y, 0) {
		return false
	}
	docH, smoothCSS := 0.0, false
	l := d.Layout(d.LayoutWidth())
	if ext, _ := l.Ext.(*renderExt); ext != nil {
		docH = ext.res.Height
		for c := d.Root.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.ElementNode && c.Data == "html" {
				if b := ext.res.Boxes[c]; b != nil && b.Style != nil {
					smoothCSS = b.Style.ScrollBehavior == "smooth"
				}
				break
			}
		}
	} else {
		docH = float64(l.Height)
	}
	_, vh, _, _, _ := t.Viewport()
	maxY := math.Max(0, docH-float64(vh))
	y = math.Round(math.Min(math.Max(y, 0), maxY))
	smooth := behavior == "smooth" || (behavior != "instant" && smoothCSS)
	if h := t.scrollHook.Load(); h != nil {
		if !smooth {
			t.NoteScrollY(int(y)) // script reads the new position at once; the window moves the view and fires the events
		}
		(*h)(ScrollRequest{X: 0, Y: y, Smooth: smooth})
		return false
	}
	changed := int64(y) != t.ds.scrollY.Load()
	t.SetScrollY(int(y))
	return changed
}
