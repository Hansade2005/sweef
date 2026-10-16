package net

import (
	"sync"

	"golang.org/x/net/html"
)

// Option selectedness (HTML "selectedness" and "dirtiness" of <option>).
//
// The engine carries an option's selectedness in its `selected` attribute:
// the renderer, the snapshot, form submission and :checked all read it. In a
// browser, though, the two part ways once the selection has been changed by
// the user or by script (select.value, selectedIndex, option.selected): from
// then on the option is "dirty" and page scripts adding or removing the
// `selected` content attribute no longer move the selection. Pages rely on
// that: the-internet.herokuapp.com/dropdown's change handler strips every
// `selected` attribute and then reads select.value; with the attribute as the
// only state the value fell back to the first option and the pick was lost.
//
// So for a dirty option the attribute keeps meaning selectedness, and what a
// script wrote to `selected` is kept here instead and served back by
// getAttribute/hasAttribute/attributes.
type optShadow struct {
	has bool
	val string
}

type optSelState struct {
	mu    sync.Mutex
	dirty map[*html.Node]optShadow
}

func (d *Doc) optState() *optSelState {
	d.optOnce.Do(func() { d.optSel = &optSelState{dirty: map[*html.Node]optShadow{}} })
	return d.optSel
}

func attrVal(n *html.Node, k string) (string, bool) {
	for _, a := range n.Attr {
		if a.Key == k {
			return a.Val, true
		}
	}
	return "", false
}

// markOptionsDirty records the script-visible `selected` attribute of every
// option of sel that is not yet dirty; call it before the engine rewrites the
// attributes to reflect a new selection.
func (d *Doc) markOptionsDirty(sel *html.Node) {
	st := d.optState()
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, o := range optionsOf(sel) {
		if _, ok := st.dirty[o]; !ok {
			v, has := attrVal(o, "selected")
			st.dirty[o] = optShadow{has: has, val: v}
		}
	}
}

// ownerSelect is the <select> an option belongs to (parent, or grandparent
// through an <optgroup>), nil if none.
func ownerSelect(o *html.Node) *html.Node {
	p := o.Parent
	if p != nil && p.Type == html.ElementNode && p.Data == "optgroup" {
		p = p.Parent
	}
	if p != nil && p.Type == html.ElementNode && p.Data == "select" {
		return p
	}
	return nil
}

// scriptSetSelected handles a page script writing the `selected` attribute of
// an option. It reports whether it took care of the write.
func (d *Doc) scriptSetSelected(n *html.Node, val string, remove bool) bool {
	if n.Type != html.ElementNode || n.Data != "option" {
		return false
	}
	st := d.optState()
	st.mu.Lock()
	if _, ok := st.dirty[n]; ok {
		st.dirty[n] = optShadow{has: !remove, val: val}
		st.mu.Unlock()
		return true
	}
	st.mu.Unlock()
	if remove {
		return false
	}
	// A clean option gaining `selected` in a single select becomes the
	// selected one (the selectedness setting algorithm): the others lose
	// selectedness but keep their attribute as far as scripts can tell.
	sel := ownerSelect(n)
	if sel == nil || hasAttr(sel, "multiple") {
		return false
	}
	st.mu.Lock()
	for _, o := range optionsOf(sel) {
		if o == n {
			continue
		}
		if v, has := attrVal(o, "selected"); has {
			if _, ok := st.dirty[o]; !ok {
				st.dirty[o] = optShadow{has: true, val: v}
			}
			removeAttr(o, "selected")
		}
	}
	st.mu.Unlock()
	return false
}

// scriptSelectedAttr returns the script-visible `selected` attribute of a
// dirty option; ok is false when n is not one.
func (d *Doc) scriptSelectedAttr(n *html.Node) (sh optShadow, ok bool) {
	if n.Type != html.ElementNode || n.Data != "option" || d.optSel == nil {
		return optShadow{}, false
	}
	st := d.optSel
	st.mu.Lock()
	sh, ok = st.dirty[n]
	st.mu.Unlock()
	return sh, ok
}
