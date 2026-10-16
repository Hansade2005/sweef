package net

import (
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// Playwright-style selector chaining: "A >> B" resolves B inside each match
// of A. Besides any selector the engine knows (css, text=, role=, ...), a
// step can be one of Playwright's pseudo-engines:
//
//	nth=N          the Nth match so far (0-based; negative counts from the end)
//	visible=true   only the matches that are (or with false, are not) visible
//	has-text=T     only the matches whose text contains T (case-insensitive)
//
// The SDKs build Locator.first/.last/.nth(i) and locator.locator(sub) this
// way, so they work against any surface that takes a selector.

// splitChain splits sel on top-level ">>" (outside quotes, brackets and
// parentheses). A selector without one comes back as a single part.
func splitChain(sel string) []string {
	if !strings.Contains(sel, ">>") {
		return []string{sel}
	}
	var parts []string
	depth, start := 0, 0
	var quote byte
	for i := 0; i < len(sel); i++ {
		c := sel[i]
		switch {
		case quote != 0:
			if c == '\\' {
				i++
			} else if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'' || c == '`':
			quote = c
		case c == '[' || c == '(':
			depth++
		case c == ']' || c == ')':
			if depth > 0 {
				depth--
			}
		case c == '>' && depth == 0 && i+1 < len(sel) && sel[i+1] == '>':
			parts = append(parts, strings.TrimSpace(sel[start:i]))
			i++
			start = i + 1
		}
	}
	parts = append(parts, strings.TrimSpace(sel[start:]))
	for _, p := range parts {
		if p == "" {
			return []string{sel} // "a >>" or ">> b": not a chain, let the parser report it
		}
	}
	return parts
}

func unquoteStep(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		return v[1 : len(v)-1]
	}
	return v
}

func (d *Doc) resolveChain(sel string, parts []string) ([]*Info, error) {
	cur, err := d.resolveEngineOne(parts[0])
	if err != nil {
		return nil, err
	}
	for _, p := range parts[1:] {
		if len(cur) == 0 {
			break
		}
		key, val, _ := strings.Cut(p, "=")
		switch strings.TrimSpace(key) {
		case "nth":
			i, err := strconv.Atoi(strings.TrimSpace(val))
			if err != nil {
				return nil, fmt.Errorf("bad selector step %q in %q", p, sel)
			}
			if i < 0 {
				i += len(cur)
			}
			if i < 0 || i >= len(cur) {
				cur = nil
			} else {
				cur = cur[i : i+1]
			}
			continue
		case "visible":
			want := strings.TrimSpace(val) != "false"
			kept := cur[:0:0]
			for _, in := range cur {
				if v, _ := d.IsVisible(in.BackendID); v == want {
					kept = append(kept, in)
				}
			}
			cur = kept
			continue
		case "has-text":
			want := strings.ToLower(unquoteStep(val))
			kept := cur[:0:0]
			for _, in := range cur {
				if strings.Contains(strings.ToLower(in.Text), want) {
					kept = append(kept, in)
				}
			}
			cur = kept
			continue
		}
		// a selector scoped to the matches so far: its matches that sit inside one of them
		scope := make(map[*html.Node]bool, len(cur))
		for _, in := range cur {
			if n := d.find(in.BackendID); n != nil {
				scope[n] = true
			}
		}
		next, _ := d.resolveEngineOne(p)
		kept := next[:0:0]
		for _, in := range next {
			n := d.find(in.BackendID)
			if n == nil {
				continue
			}
			for a := n.Parent; a != nil; a = a.Parent {
				if scope[a] {
					kept = append(kept, in)
					break
				}
			}
		}
		cur = kept
	}
	if len(cur) == 0 {
		return nil, fmt.Errorf("no node matches %q", sel)
	}
	return cur, nil
}
