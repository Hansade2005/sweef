package net

import "golang.org/x/net/html"

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
}

func newIDTable() *idTable {
	return &idTable{ids: map[*html.Node]int64{}, nodes: map[int64]*html.Node{}}
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

// RefState classifies an id for error messages: "attached", "detached" or
// "unknown".
func (d *Doc) RefState(id int64) string {
	n := d.find(id)
	switch {
	case n == nil:
		return "unknown"
	case d.Attached(n):
		return "attached"
	default:
		return "detached"
	}
}
