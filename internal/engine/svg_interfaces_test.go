package engine

import "testing"

// Players' UI code tells SVG nodes apart with instanceof (Shaka's icons use SVGPathElement).
func TestSVGElementInterfaces(t *testing.T) {
	e := New(DefaultOptions())
	defer e.Close()
	tab, err := e.NewTab("about:blank")
	if err != nil {
		t.Fatal(err)
	}
	var r []bool
	evalJSON(t, tab, `(function () {
	  var NS = 'http://www.w3.org/2000/svg';
	  var path = document.createElementNS(NS, 'path'), svg = document.createElementNS(NS, 'svg');
	  var d = document.createElement('div');
	  d.innerHTML = '<svg><circle/><g><rect/></g></svg>';
	  return [path instanceof SVGPathElement, path instanceof SVGElement, !(path instanceof SVGCircleElement),
	    d.querySelector('circle') instanceof SVGCircleElement, d.querySelector('g') instanceof SVGGElement,
	    d.querySelector('rect') instanceof SVGRectElement, d.querySelector('svg') instanceof SVGSVGElement,
	    svg.appendChild(path) === path, Object.prototype.toString.call(path) === '[object SVGPathElement]'];
	})()`, &r)
	for i, ok := range r {
		if !ok {
			t.Errorf("check %d failed: %v", i, r)
		}
	}
	if len(r) != 9 {
		t.Fatalf("results: %v", r)
	}
}
