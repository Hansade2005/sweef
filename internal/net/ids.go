package net

import (
	"strings"

	"golang.org/x/net/html"
)

// idTable gives every node the Doc has ever touched a small, stable integer
// id. Ids start at 1 per document, are never reused, and survive any tree
// mutation — unlike the old path hash, which silently renumbered every node
// below an insertion. A navigation creates a fresh Doc and a fresh table, so
// refs from a previous page are stale by construction.
//
// Both elements and text nodes get ids: the JavaScript DOM bridge addresses
// text nodes too.
type idTable struct {
	next  int64
	ids   map[*html.Node]int64
	nodes map[int64]*html.Node

	// Snapshot refs are a second, denser numbering: a node gets a ref the
	// first time a snapshot lists it, keeps it for the life of the document,
	// and only listed nodes can be addressed as eN. Internal ids (scripts,
	// JS wrappers) therefore never show up in, or collide with, agent refs.
	nextRef int64
	refs    map[*html.Node]int64
	byRef   map[int64]*html.Node
}

func newIDTable() *idTable {
	return &idTable{ids: map[*html.Node]int64{}, nodes: map[int64]*html.Node{}, refs: map[*html.Node]int64{}, byRef: map[int64]*html.Node{}}
}

// refOf returns the node's snapshot ref, assigning the next one on first use.
func (d *Doc) refOf(n *html.Node) int64 {
	d.idMu.Lock()
	defer d.idMu.Unlock()
	if r, ok := d.tab.refs[n]; ok {
		return r
	}
	d.tab.nextRef++
	r := d.tab.nextRef
	d.tab.refs[n] = r
	d.tab.byRef[r] = n
	return r
}

// nodeByRef resolves a snapshot ref; nil when no snapshot of this document
// ever listed it.
func (d *Doc) nodeByRef(ref int64) *html.Node {
	d.idMu.RLock()
	defer d.idMu.RUnlock()
	return d.tab.byRef[ref]
}

// RefForBackend returns the snapshot ref for an internal id, assigning one
// when the node has not been listed yet (so nodes found through evaluate can
// be acted on). 0 for unknown ids and non-elements.
func (d *Doc) RefForBackend(id int64) int64 {
	n := d.find(id)
	if n == nil || n.Type != html.ElementNode {
		return 0
	}
	return d.refOf(n)
}

// idOf returns the node's id, assigning one on first touch.
func (d *Doc) idOf(n *html.Node) int64 {
	if n == nil {
		return 0
	}
	d.idMu.Lock()
	defer d.idMu.Unlock()
	if id, ok := d.tab.ids[n]; ok {
		return id
	}
	d.tab.next++
	id := d.tab.next
	d.tab.ids[n] = id
	d.tab.nodes[id] = n
	return id
}

// find resolves an id to its node in O(1). nil when the id was never issued
// by this document.
func (d *Doc) find(id int64) *html.Node {
	d.idMu.RLock()
	defer d.idMu.RUnlock()
	return d.tab.nodes[id]
}

// Attached reports whether n is still connected to the document root.
func (d *Doc) Attached(n *html.Node) bool {
	for x := n; x != nil; x = x.Parent {
		if x == d.Root {
			return true
		}
	}
	return false
}

// RefState classifies a snapshot ref for error messages: "attached",
// "detached" or "unknown".
func (d *Doc) RefState(ref int64) string {
	n := d.nodeByRef(ref)
	switch {
	case n == nil:
		return "unknown"
	case d.Attached(n):
		return "attached"
	default:
		return "detached"
	}
}

// HealRef finds the control that took over from the element of snapshot ref (hidden or removed since the
// snapshot): the one visible element with the same accessible name and a compatible role. Widgets that swap
// their element when used do this all the time: Wikipedia's search box is replaced by a combobox when it is
// clicked, and the agent's next "fill e3" otherwise failed on the hidden original. It returns the new ref
// (assigned now) and true only for an unambiguous match.
func (d *Doc) HealRef(ref int64) (int64, bool) {
	n := d.nodeByRef(ref)
	if n == nil || n.Type != html.ElementNode {
		return 0, false
	}
	if d.Attached(n) {
		if vis, _ := d.IsVisible(d.idOf(n)); vis {
			return 0, false // nothing to heal
		}
	}
	name := strings.ToLower(strings.TrimSpace(accessibleName(n)))
	if name == "" {
		return 0, false
	}
	role := computeRole(n)
	editable := func(r string) bool { return r == "textbox" || r == "searchbox" || r == "combobox" }
	var found *html.Node
	for _, c := range d.queryAll("*") {
		if c == n {
			continue
		}
		r := computeRole(c)
		if r != role && !(editable(role) && editable(r)) {
			continue
		}
		if strings.ToLower(strings.TrimSpace(accessibleName(c))) != name {
			continue
		}
		if vis, _ := d.IsVisible(d.idOf(c)); !vis {
			continue
		}
		if found != nil {
			return 0, false // ambiguous
		}
		found = c
	}
	if found == nil {
		return 0, false
	}
	return d.refOf(found), true
}
