package net

import (
	"sort"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// tabIndexOf is the element's tabindex for sequential focus navigation and
// whether it takes part at all (HTML "sequentially focusable").
func tabIndexOf(n *html.Node) (int, bool) {
	if n.Type != html.ElementNode {
		return 0, false
	}
	if v, ok := attrVal(n, "tabindex"); ok {
		if i, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			if i < 0 {
				return 0, false
			}
			if hasAttr(n, "disabled") && isFormControl(n) {
				return 0, false
			}
			return i, true
		}
	}
	switch n.Data {
	case "a", "area":
		return 0, hasAttr(n, "href")
	case "input":
		if strings.EqualFold(attr(n, "type"), "hidden") {
			return 0, false
		}
		return 0, !hasAttr(n, "disabled")
	case "button", "select", "textarea":
		return 0, !hasAttr(n, "disabled")
	case "iframe", "summary":
		return 0, true
	}
	if ce, ok := attrVal(n, "contenteditable"); ok && !strings.EqualFold(ce, "false") {
		return 0, true
	}
	return 0, false
}

func isFormControl(n *html.Node) bool {
	switch n.Data {
	case "input", "button", "select", "textarea", "fieldset":
		return true
	}
	return false
}

// NextTabStop returns the element Tab (or Shift+Tab when back) moves focus to
// from fromID (0: nothing focused): elements with a positive tabindex first in
// ascending order, then the others in tree order, skipping disabled, hidden
// and tabindex<0 ones. It wraps around; 0 when the page has no tab stop.
func (d *Doc) NextTabStop(fromID int64, back bool) int64 {
	type stop struct {
		n   *html.Node
		idx int
	}
	var stops []stop
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type != html.ElementNode {
				continue
			}
			switch c.Data {
			case "script", "style", "template", "head", "noscript":
				continue
			}
			if i, ok := tabIndexOf(c); ok {
				stops = append(stops, stop{c, i})
			}
			walk(c)
		}
	}
	walk(d.Root)
	sort.SliceStable(stops, func(a, b int) bool {
		ia, ib := stops[a].idx, stops[b].idx
		if (ia > 0) != (ib > 0) {
			return ia > 0
		}
		if ia > 0 {
			return ia < ib
		}
		return false
	})
	order := stops[:0]
	for _, s := range stops {
		if v, err := d.IsVisible(d.idOf(s.n)); err == nil && v {
			order = append(order, s)
		}
	}
	if len(order) == 0 {
		return 0
	}
	cur := -1
	if from := d.find(fromID); from != nil {
		for i, s := range order {
			if s.n == from {
				cur = i
				break
			}
		}
	}
	var next int
	switch {
	case cur < 0 && back:
		next = len(order) - 1
	case cur < 0:
		next = 0
	case back:
		next = (cur - 1 + len(order)) % len(order)
	default:
		next = (cur + 1) % len(order)
	}
	return d.idOf(order[next].n)
}

// FocusableAncestor is the element a pointer press on id focuses: id itself
// or its nearest ancestor that can take focus (form controls, links,
// tabindex, contenteditable); 0 when none can.
func (d *Doc) FocusableAncestor(id int64) int64 {
	for n := d.find(id); n != nil; n = n.Parent {
		if n.Type != html.ElementNode {
			continue
		}
		if _, ok := attrVal(n, "tabindex"); ok {
			return d.idOf(n)
		}
		if _, ok := tabIndexOf(n); ok {
			return d.idOf(n)
		}
	}
	return 0
}
