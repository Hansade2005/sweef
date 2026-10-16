package net

import "golang.org/x/net/html"

// MutationGen returns a counter that increases on every tree or style
// mutation. The GUI compositor compares it between frames to decide whether
// a tab needs repainting (damage tracking) without polling the DOM.
func (d *Doc) MutationGen() uint64 { return d.mutGen.Load() }

// ContentGen counts the tree mutations other than style attribute writes and event notes: a headless settle
// waits for it to stop moving, and a page that animates for ever (a carousel, a marquee, a framer-motion
// entrance loop) moves only MutationGen.
func (d *Doc) ContentGen() uint64 { return d.contentGen.Load() }

// TouchPaint marks that pixels drawn into the document from outside the tree
// changed (a new video frame) without touching layout: the cached layout stays
// valid, only PaintGen moves, and a repaint is needed.
func (d *Doc) TouchPaint() { d.paintGen.Add(1) }

// PaintGen counts TouchPaint calls.
func (d *Doc) PaintGen() uint64 { return d.paintGen.Load() }

// Touch counts as a tree mutation for layout caching and repaint detection
// (MutationGen) without changing the tree. The engine uses it when resources
// that were deferred become available (lazy images).
func (d *Doc) Touch() { d.invalidateStyles() }

// TouchLayout makes the next layout run again without dropping the computed styles: for state that layout reads
// from outside the tree and that changed (the scroll offsets of overflow containers).
func (d *Doc) TouchLayout() { d.mutGen.Add(1) }

// CachedLayout returns the layout computed last, whatever the state of the document is now, or nil when none has
// been computed or a layout is in progress. It never starts a layout, so a thread that must not wait (the GUI's
// event loop) can use it.
func (d *Doc) CachedLayout() *Layout {
	if !d.layoutMu.TryLock() {
		return nil
	}
	defer d.layoutMu.Unlock()
	return d.layoutCache
}

// SetAttrQuiet sets an attribute on n without notifying mutation listeners
// and without counting as a mutation for layout caching: it is for the
// engine's own markers (data-sweef-lite) written while the layout provider is
// running, which lays the document out again before returning. Callers outside
// the layout provider use SetAttrQuiet followed by Touch.
func (d *Doc) SetAttrQuiet(n *html.Node, name, val string) {
	if n != nil && n.Type == html.ElementNode {
		setAttr(n, name, val)
		d.dropStyleCache()
	}
}

// RemoveAttrQuiet is the removal counterpart of SetAttrQuiet.
func (d *Doc) RemoveAttrQuiet(n *html.Node, name string) {
	if n != nil {
		removeAttr(n, name)
		d.dropStyleCache()
	}
}

func (d *Doc) dropStyleCache() {
	d.cssMu.Lock()
	if d.css != nil {
		d.css.gen++
		d.css.cache = nil
	}
	d.cssMu.Unlock()
}
