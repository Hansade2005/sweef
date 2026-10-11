package render

import (
	"image"
	"math"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func parseHTML(t *testing.T, s string) *html.Node {
	t.Helper()
	d, err := html.Parse(strings.NewReader(s))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func findByID(n *html.Node, id string) *html.Node {
	if n.Type == html.ElementNode && attr(n, "id") == id {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if r := findByID(c, id); r != nil {
			return r
		}
	}
	return nil
}

func TestForeignObjectGeometryAndHit(t *testing.T) {
	doc := parseHTML(t, `<body style="margin:0"><svg width="300" height="200" viewBox="0 0 150 100" style="display:block">
<g transform="translate(10 5)"><foreignObject id="fo" x="5" y="5" width="80" height="40">
<div id="d" xmlns="http://www.w3.org/1999/xhtml" style="width:100%;height:100%;background:#fc0"><button id="b" style="width:60px;height:20px">go</button></div>
</foreignObject></g></svg>`)
	res := Layout(doc, Options{Width: 800, Height: 600})
	// user (15,10) maps to device (30,20) at scale 2
	fo, ok := res.ClientRect(findByID(doc, "fo"))
	if !ok || math.Abs(fo.X-30) > 0.01 || math.Abs(fo.Y-20) > 0.01 || math.Abs(fo.W-160) > 0.01 || math.Abs(fo.H-80) > 0.01 {
		t.Fatalf("foreignObject rect %v ok=%v", fo, ok)
	}
	d, _ := res.ClientRect(findByID(doc, "d"))
	if d != fo {
		t.Fatalf("div fills the foreignObject: %v vs %v", d, fo)
	}
	b, ok := res.ClientRect(findByID(doc, "b"))
	if !ok || math.Abs(b.W-120) > 0.01 || math.Abs(b.H-40) > 0.01 {
		t.Fatalf("button rect %v", b)
	}
	if n := res.Hit(b.X+5, b.Y+5); n == nil || attr(n, "id") != "b" {
		t.Fatalf("hit on the button returned %v", n)
	}
	if n := res.Hit(b.X+b.W+20, b.Y+5); n == nil || attr(n, "id") != "d" {
		t.Fatalf("hit beside the button returned %v", n)
	}
}

type svgEnv struct {
	nilEnv
	files map[string]string
}

func (e svgEnv) SVGSource(u string) (string, bool) {
	s, ok := e.files[u]
	return s, ok
}
func (e svgEnv) Image(string) image.Image { return nil }

func TestSVGImageMediaQueries(t *testing.T) {
	const img = `<svg xmlns="http://www.w3.org/2000/svg" width="120" height="60"><style>
rect{fill:#3a7}
@media (prefers-color-scheme:dark){rect{fill:#206}}
@media (max-width:100px){circle{fill:#c33}}
</style><rect width="120" height="60"/><circle r="5"/></svg>`
	env := svgEnv{files: map[string]string{"http://x/a.svg": img}}
	fill := func(scheme string, w string) (string, string) {
		doc := parseHTML(t, `<body style="margin:0"><img src="a.svg" width="`+w+`" height="40"></body>`)
		res := Layout(doc, Options{Width: 800, Height: 600, ColorScheme: scheme, BaseURL: "http://x/", Env: env})
		for _, op := range res.List {
			if d, ok := op.(SVGDraw); ok {
				var rect, circ string
				for _, k := range d.Doc.Root.Kids {
					switch k.Tag {
					case "rect":
						rect = k.A["fill"]
					case "circle":
						circ = k.A["fill"]
					}
				}
				return rect, circ
			}
		}
		t.Fatal("no SVGDraw emitted")
		return "", ""
	}
	if r, c := fill("light", "120"); r != "#3a7" || c != "" {
		t.Errorf("light/120: rect %q circle %q", r, c)
	}
	if r, _ := fill("dark", "120"); r != "#206" {
		t.Errorf("dark: rect %q", r)
	}
	if _, c := fill("light", "90"); c != "#c33" {
		t.Errorf("90px wide image should match max-width:100px, circle %q", c)
	}
}

func TestExternalUseAndImageResolveThroughEnv(t *testing.T) {
	env := svgEnv{files: map[string]string{
		"http://x/sprite.svg": `<svg xmlns="http://www.w3.org/2000/svg"><g id="s"><rect width="10" height="10"/></g></svg>`,
	}}
	doc := parseHTML(t, `<body style="margin:0"><svg width="100" height="100"><use id="u" href="sprite.svg#s" x="20" y="30"/></svg></body>`)
	res := Layout(doc, Options{Width: 800, Height: 600, BaseURL: "http://x/p.html", Env: env})
	r, ok := res.ClientRect(findByID(doc, "u"))
	if !ok || r.X != 20 || r.Y != 30 || r.W != 10 || r.H != 10 {
		t.Fatalf("external use rect %v ok=%v", r, ok)
	}
}

func TestCrossSVGReferences(t *testing.T) {
	doc := parseHTML(t, `<body style="margin:0"><svg width="0" height="0"><symbol id="sy" viewBox="0 0 10 10"><rect width="10" height="10"/></symbol></svg>
<svg width="100" height="100"><use id="u" href="#sy" width="40" height="40"/></svg></body>`)
	res := Layout(doc, Options{Width: 800, Height: 600})
	r, ok := res.ClientRect(findByID(doc, "u"))
	if !ok || r.W != 40 || r.H != 40 {
		t.Fatalf("use of a symbol from another inline svg: %v ok=%v", r, ok)
	}
}

func TestSVGTextPathGeometry(t *testing.T) {
	doc := parseHTML(t, `<body style="margin:0"><svg width="300" height="100"><path id="p" d="M20 50 L220 50 L220 150" fill="none"/>
<text id="t"><textPath id="tp" href="#p" startOffset="10">ABC</textPath></text>
<text id="rot" x="10" y="90" rotate="90">AB</text></svg></body>`)
	res := Layout(doc, Options{Width: 800, Height: 600})
	r, ok := res.ClientRect(findByID(doc, "tp"))
	if !ok || r.X < 29 || r.X > 31 || r.W < 30 {
		t.Fatalf("textPath rect %v ok=%v", r, ok)
	}
	rr, _ := res.ClientRect(findByID(doc, "rot"))
	// rotate="90" turns each glyph cell on its origin: the pen still advances along x
	if rr.H > 15 || rr.W < 25 {
		t.Fatalf("rotated glyph cells: %v", rr)
	}
}

type imgEnv struct {
	nilEnv
	im image.Image
}

func (e imgEnv) Image(u string) image.Image {
	if u == "http://x/pic.png" {
		return e.im
	}
	return nil
}

func TestSVGImageHrefResolvedThroughEnv(t *testing.T) {
	im := image.NewRGBA(image.Rect(0, 0, 4, 2))
	doc := parseHTML(t, `<body><svg width="100" height="100"><image id="i" href="pic.png" width="40" height="20"/></svg></body>`)
	res := Layout(doc, Options{Width: 800, Height: 600, BaseURL: "http://x/p.html", Env: imgEnv{im: im}})
	for _, op := range res.List {
		if d, ok := op.(SVGDraw); ok {
			for _, k := range d.Doc.Root.Kids {
				if k.Tag == "image" {
					if k.Img == nil {
						t.Fatal("image href was not resolved through Env.Image")
					}
					return
				}
			}
		}
	}
	t.Fatal("no image node in the scene")
}

// position:fixed inside a foreignObject is placed against the foreignObject,
// like inside any transformed box.
func TestForeignObjectFixedContainingBlock(t *testing.T) {
	doc := parseHTML(t, `<body style="margin:0"><svg width="300" height="200" style="display:block"><g transform="translate(20 10)">
<foreignObject id="fo" x="30" y="40" width="150" height="100"><div xmlns="http://www.w3.org/1999/xhtml" style="height:100%"><span id="f" style="position:fixed;left:10px;top:12px;width:20px;height:8px;display:block"></span></div></foreignObject></g></svg></body>`)
	res := Layout(doc, Options{Width: 800, Height: 600})
	r, ok := res.ClientRect(findByID(doc, "f"))
	if !ok || math.Abs(r.X-60) > 0.01 || math.Abs(r.Y-62) > 0.01 || r.W != 20 || r.H != 8 {
		t.Fatalf("fixed box in foreignObject: %v ok=%v, want (60,62,20,8)", r, ok)
	}
}

// Ancestor clip-paths (url(#clipPath) with units and transforms, CSS shapes)
// clip hit testing of foreignObject content; masks do not.
func TestForeignObjectHitClipPath(t *testing.T) {
	doc := parseHTML(t, `<body style="margin:0"><svg width="400" height="300" style="display:block">
<defs><clipPath id="c"><circle cx="60" cy="60" r="40"/></clipPath><clipPath id="o" clipPathUnits="objectBoundingBox"><circle cx=".5" cy=".5" r=".3"/></clipPath>
<mask id="m"><rect x="0" y="0" width="30" height="300" fill="#fff"/></mask></defs>
<g clip-path="url(#c)"><foreignObject x="10" y="10" width="100" height="100"><div id="d1" xmlns="http://www.w3.org/1999/xhtml" style="width:100%;height:100%"></div></foreignObject></g>
<g clip-path="url(#o)" transform="translate(200 120) rotate(30)"><foreignObject x="0" y="0" width="100" height="100"><div id="d2" xmlns="http://www.w3.org/1999/xhtml" style="width:100%;height:100%"></div></foreignObject></g>
<g style="clip-path:inset(0 50px 0 0)" transform="translate(20 150)"><foreignObject x="0" y="0" width="100" height="60"><div id="d3" xmlns="http://www.w3.org/1999/xhtml" style="width:100%;height:100%"></div></foreignObject></g>
<foreignObject x="290" y="10" width="100" height="100" mask="url(#m)"><div id="d4" xmlns="http://www.w3.org/1999/xhtml" style="width:100%;height:100%"></div></foreignObject>
</svg></body>`)
	res := Layout(doc, Options{Width: 800, Height: 600})
	hit := func(x, y float64) string {
		if n := res.Hit(x, y); n != nil {
			if id := attr(n, "id"); id != "" {
				return id
			}
			return n.Data
		}
		return "null"
	}
	// Chromium 1194: elementFromPoint on the same page
	for _, c := range []struct {
		x, y float64
		want string
	}{
		{60, 60, "d1"}, {15, 15, "svg"}, {90, 90, "svg"}, {100, 100, "svg"},
		{40, 175, "d3"}, {80, 190, "svg"}, {100, 190, "svg"},
		{200, 230, "svg"}, {218, 188, "d2"}, // rotated circle of the bounding box
		{300, 20, "d4"}, {380, 100, "d4"}, // a mask does not clip hit testing
	} {
		if got := hit(c.x, c.y); got != c.want {
			t.Errorf("hit(%g,%g) = %s, want %s", c.x, c.y, got, c.want)
		}
	}
}
