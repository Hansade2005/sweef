package engine

import "testing"

// Players filter a manifest's childNodes with instanceof Element (Shaka's findChildren): XML nodes must pass
// the DOM interface checks, and HTML nodes must keep failing the XML-only ones.
func TestXMLNodesAreInstancesOfTheDOMInterfaces(t *testing.T) {
	e := New(DefaultOptions())
	defer e.Close()
	tab, err := e.NewTab("about:blank")
	if err != nil {
		t.Fatal(err)
	}
	var r []bool
	evalJSON(t, tab, `(function () {
	  var d = new DOMParser().parseFromString('<a><!--c-->t<b/></a>', 'text/xml'), a = d.documentElement;
	  var html = document.createElement('div');
	  return [a instanceof Element, a instanceof Node, !(a instanceof HTMLElement), a.firstChild instanceof Comment, a.childNodes[1] instanceof Text,
	    a.childNodes[1] instanceof Node, !(a.childNodes[1] instanceof Element), d instanceof Document, d instanceof Node, html instanceof Element,
	    html instanceof Node, !(html instanceof Comment), Array.prototype.filter.call(a.childNodes, function (c) { return c instanceof Element; }).length === 1];
	})()`, &r)
	for i, ok := range r {
		if !ok {
			t.Errorf("check %d failed: %v", i, r)
		}
	}
	if len(r) != 13 {
		t.Fatalf("results: %v", r)
	}
}
