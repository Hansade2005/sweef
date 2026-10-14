package render

import (
	"math"
	"testing"

	"golang.org/x/net/html"
)

// A position:fixed box inside an absolutely positioned one has the viewport as its containing block. It is laid out
// while the absolute box's own out-of-flow children are being laid out, and used to be dropped from that pass: it
// stayed 0x0 at the origin (YouTube's fixed masthead inside its absolutely positioned app shell).
func TestFixedInsideAbsoluteIsLaidOut(t *testing.T) {
	doc := parseHTML(t, `<body style="margin:0"><div style="position:absolute;top:0;left:0;width:100%"><div><div id="f" style="position:fixed;top:0;width:100%"><div style="display:block;width:100%;height:56px"></div></div></div></div></body>`)
	res := Layout(doc, Options{Width: 800, Height: 600})
	r, ok := res.ClientRect(findByID(doc, "f"))
	if !ok || math.Abs(r.W-800) > 0.01 || math.Abs(r.H-56) > 0.01 || r.X != 0 || r.Y != 0 {
		t.Fatalf("fixed box inside an absolute one: %v ok=%v, want (0,0,800,56)", r, ok)
	}
}

// The same box nested two absolute levels deep, with a second fixed sibling, still gets every box laid out.
func TestFixedInsideNestedAbsolute(t *testing.T) {
	doc := parseHTML(t, `<body style="margin:0"><div style="position:absolute;top:0;left:0;width:100%"><div style="position:absolute;top:10px;left:0;width:50%"><div id="a" style="position:fixed;top:0;left:0;width:100%;height:5px"></div><div id="b" style="position:fixed;bottom:0;right:0;width:25%;height:7px"></div></div></div></body>`)
	res := Layout(doc, Options{Width: 800, Height: 600})
	a, okA := res.ClientRect(findByID(doc, "a"))
	b, okB := res.ClientRect(findByID(doc, "b"))
	if !okA || !okB || math.Abs(a.W-800) > 0.01 || a.H != 5 || math.Abs(b.W-200) > 0.01 || b.H != 7 || math.Abs(b.Y-593) > 0.01 {
		t.Fatalf("a=%v ok=%v b=%v ok=%v", a, okA, b, okB)
	}
}

// SVG attribute names are case-sensitive: a script that adds a lowercase "viewbox" next to the real viewBox (as
// YouTube's icon component does after it has set the right viewBox) must not override it in the cascade.
func TestSVGLowercaseDuplicateDoesNotOverrideCamelCase(t *testing.T) {
	for _, order := range []string{"camel-first", "lower-first"} {
		doc := parseHTML(t, `<body><svg id="s" viewBox="0 0 93 20"></svg></body>`)
		svg := findByID(doc, "s")
		lower := html.Attribute{Key: "viewbox", Val: "0 0 24 24"}
		if order == "lower-first" {
			svg.Attr = append([]html.Attribute{lower}, svg.Attr...)
		} else {
			svg.Attr = append(svg.Attr, lower)
		}
		scene := buildSVGScene(svg, newSVGCascade(nil, nil, nil))
		if got := scene.Root.A["viewbox"]; got != "0 0 93 20" {
			t.Fatalf("%s: viewbox in the scene = %q, want the viewBox attribute's \"0 0 93 20\"", order, got)
		}
	}
}
