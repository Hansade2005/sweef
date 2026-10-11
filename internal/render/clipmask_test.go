package render

import (
	"math"
	"os"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func cmLayout(t *testing.T, src string) (*html.Node, *Result) {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	var sheets []Sheet
	collectSheets(doc, &sheets)
	return doc, Layout(doc, Options{Width: 1280, Height: 800, DPR: 1, Sheets: sheets})
}

func findID(n *html.Node, id string) *html.Node {
	if n.Type == html.ElementNode {
		for _, a := range n.Attr {
			if a.Key == "id" && a.Val == id {
				return n
			}
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if r := findID(c, id); r != nil {
			return r
		}
	}
	return nil
}

// TestMulticolFragments checks per-column block fragments (getClientRects)
// against rectangles measured in Chromium 1194 for testdata/fidelity/multicol4.html.
func TestMulticolFragments(t *testing.T) {
	src, err := os.ReadFile("../../testdata/fidelity/multicol4.html")
	if err != nil {
		t.Skip(err)
	}
	doc, res := cmLayout(t, string(src))
	var divs []*html.Node
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "div" {
			divs = append(divs, n)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	want := map[int][][4]float64{
		1: {{28, 32, 217.33, 146}, {269.33, 28, 217.33, 150}, {510.66, 28, 217.33, 68.78}},
		3: {{28, 222, 217.33, 146}, {269.33, 218, 217.33, 150}, {510.66, 218, 217.33, 48.19}},
		5: {{28, 412, 217.33, 146}, {269.33, 408, 217.33, 150}, {510.66, 408, 217.33, 45.19}},
	}
	for idx, rects := range want {
		b := res.Boxes[divs[idx]]
		if b == nil || len(b.Frags) != len(rects) {
			t.Errorf("div %d: %d fragments, want %d", idx, len(b.Frags), len(rects))
			continue
		}
		for i, r := range rects {
			f := b.Frags[i]
			if math.Abs(f.X-r[0]) > 0.7 || math.Abs(f.Y-r[1]) > 0.7 || math.Abs(f.W-r[2]) > 0.7 || math.Abs(f.H-r[3]) > 0.7 {
				t.Errorf("div %d fragment %d = %.2f %.2f %.2f %.2f, want %v", idx, i, f.X, f.Y, f.W, f.H, r)
			}
		}
	}
	// a block inside a single column keeps one rectangle
	if b := res.Boxes[divs[0]]; len(b.Frags) != 0 {
		t.Errorf("container reports %d fragments", len(b.Frags))
	}
}

func opsOf(res *Result) []string {
	var out []string
	for _, op := range res.List {
		switch o := op.(type) {
		case PushLayer:
			out = append(out, "PushLayer")
		case PopLayer:
			out = append(out, "PopLayer")
		case PushClipPath:
			out = append(out, "PushClipPath:"+o.Subs[0].Kind)
		case PopClipPath:
			out = append(out, "PopClipPath")
		case PushMask:
			out = append(out, "PushMask")
		case PopMask:
			out = append(out, "PopMask")
		}
	}
	return out
}

func TestClipMaskOps(t *testing.T) {
	_, res := cmLayout(t, `<body style="margin:0"><div style="width:100px;height:80px;background:red;opacity:.5;filter:blur(2px);clip-path:circle(40px);mask-image:linear-gradient(#000,transparent)"></div></body>`)
	got := strings.Join(opsOf(res), " ")
	want := "PushLayer PushClipPath:ellipse PushMask PushLayer PopLayer PopMask PopClipPath PopLayer"
	if got != want {
		t.Errorf("ops\n got %s\nwant %s", got, want)
	}
	// shape geometry: circle(40px) at the centre of the border box
	for _, op := range res.List {
		if o, ok := op.(PushClipPath); ok {
			s := o.Subs[0]
			if s.CX != 50 || s.CY != 40 || s.RX != 40 {
				t.Errorf("circle %v", s)
			}
		}
	}
	// mask layer content comes from the background machinery
	for _, op := range res.List {
		if o, ok := op.(PushMask); ok {
			if len(o.Layers) != 1 || len(o.Layers[0].Ops) == 0 || o.Layers[0].Mode != "alpha" {
				t.Errorf("mask layers %+v", o.Layers)
			}
			if o.Bounds.W != 100 || o.Bounds.H != 80 {
				t.Errorf("mask bounds %v", o.Bounds)
			}
		}
	}
	// a broken mask image masks everything, a missing clip reference nothing
	_, res = cmLayout(t, `<body><div style="width:10px;height:10px;background:red;mask-image:url(missing.png)"></div><div style="width:10px;height:10px;clip-path:url(#nope)"></div></body>`)
	if got := strings.Join(opsOf(res), " "); got != "PushMask PopMask" {
		t.Errorf("ops %s", got)
	}
}

func TestClipPathHitTest(t *testing.T) {
	doc, res := cmLayout(t, `<body style="margin:0"><div id=a style="width:100px;height:100px;clip-path:circle(50px)"></div><div id=b style="width:100px;height:100px;clip-path:polygon(0 0,100% 0,0 100%)"></div></body>`)
	a, b := findID(doc, "a"), findID(doc, "b")
	if res.Hit(50, 50) != a {
		t.Error("centre of circle")
	}
	if res.Hit(2, 2) == a {
		t.Error("corner outside the circle hit")
	}
	if res.Hit(20, 120) != b {
		t.Error("inside triangle")
	}
	if res.Hit(90, 190) == b {
		t.Error("outside triangle hit")
	}
}

func TestNonRenderedSVGBoxes(t *testing.T) {
	doc, res := cmLayout(t, `<body><svg width="100" height="100"><clipPath id="c"><rect id="r" width="50" height="50"/></clipPath><rect id="v" width="20" height="20"/></svg></body>`)
	if b := res.Boxes[findID(doc, "r")]; b == nil || b.Border.W != 0 || b.Border.H != 0 {
		t.Errorf("clipPath child box %+v", b)
	}
	if b := res.Boxes[findID(doc, "v")]; b == nil || b.Border.W != 20 {
		t.Errorf("rendered child box %+v", b)
	}
}
