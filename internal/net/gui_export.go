package net

import "golang.org/x/net/html"

// MutationGen returns a counter that increases on every tree or style
// mutation. The GUI compositor compares it between frames to decide whether
// a tab needs repainting (damage tracking) without polling the DOM.
func (d *Doc) MutationGen() uint64 { return d.mutGen.Load() }

// Touch counts as a tree mutation for layout caching and repaint detection
// (MutationGen) without changing the tree. The engine uses it when resources
// that were deferred become available (lazy images).
func (d *Doc) Touch() { d.invalidateStyles() }

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
