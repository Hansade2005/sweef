package net

import (
	"strings"

	"golang.org/x/net/html"
)

// NeedsScript reports whether the document has anything for a JavaScript
// engine to do: a <script> element of a JavaScript type, or an inline event
// handler attribute (onclick=, onload=, ...). Pages without either can be
// served without creating a VM.
func (d *Doc) NeedsScript() bool {
	found := false
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if found {
			return
		}
		if n.Type == html.ElementNode {
			if n.Data == "script" {
				t := strings.ToLower(strings.TrimSpace(attr(n, "type")))
				switch t {
				case "", "text/javascript", "application/javascript", "module", "text/ecmascript", "application/ecmascript", "application/x-javascript", "text/x-javascript", "text/jscript":
					found = true
					return
				}
			}
			for _, a := range n.Attr {
				if len(a.Key) > 2 && a.Key[0] == 'o' && a.Key[1] == 'n' {
					found = true
					return
				}
				if a.Key == "href" && strings.HasPrefix(strings.ToLower(strings.TrimSpace(a.Val)), "javascript:") {
					found = true
					return
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(d.Root)
	return found
}
