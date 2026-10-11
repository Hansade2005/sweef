package render

import (
	"math"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func layout3D(t *testing.T, src string) (*Result, *html.Node) {
	doc, _ := html.Parse(strings.NewReader(src))
	var sheets []Sheet
	collectSheets(doc, &sheets)
	return Layout(doc, Options{Width: 800, Height: 600, DPR: 1, Sheets: sheets}), doc
}

func byID(n *html.Node, id string) *html.Node {
	if n.Type == html.ElementNode {
		for _, a := range n.Attr {
			if a.Key == "id" && a.Val == id {
				return n
			}
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if r := byID(c, id); r != nil {
			return r
		}
	}
	return nil
}

// Chromium 1194 reference rectangles (getBoundingClientRect): the projection
// divides by w without clipping, so a box behind the camera mirrors.
func TestClientRect3DAgainstChromium(t *testing.T) {
	res, doc := layout3D(t, `<!doctype html><body style="margin:0">
<div style="perspective:200px;width:300px;height:200px"><div id=a style="width:100px;height:100px;transform:translateZ(250px)"></div></div>
<div style="perspective:200px;width:300px;height:200px"><div id=b style="width:100px;height:100px;transform:rotateY(80deg) translateZ(60px)"></div></div>
<div style="perspective:100px;width:300px;height:200px"><div id=d style="width:400px;height:100px;transform:rotateY(70deg);"></div></div>
</body>`)
	want := map[string][4]float64{
		"a": {350, 100, 400, 400},
		"b": {79.32351684570312, 157.48968505859375, 43.68634796142578, 142.51031494140625},
		"d": {170.92828369140625, 465.2703857421875, 20.193008422851562, 148.4454345703125},
	}
	for id, w := range want {
		r, ok := res.ClientRect(byID(doc, id))
		if !ok {
			t.Fatalf("%s: no rect", id)
		}
		got := [4]float64{r.X, r.Y, r.W, r.H}
		for i := range got {
			if math.Abs(got[i]-w[i]) > 0.01 {
				t.Errorf("%s: got %v want %v", id, got, w)
				break
			}
		}
	}
}

func TestPreserve3DChainAndFlatten(t *testing.T) {
	src := `<!doctype html><body style="margin:0">
<div style="perspective:600px;width:300px;height:300px"><div id=r style="transform-style:preserve-3d;transform:rotateY(45deg);width:200px;height:200px">
<div id=f style="width:200px;height:200px;transform:translateZ(100px)"></div></div></div>
<div style="perspective:600px;width:300px;height:300px"><div id=r2 style="transform-style:preserve-3d;overflow:hidden;transform:rotateY(45deg);width:200px;height:200px">
<div id=f2 style="width:200px;height:200px;transform:translateZ(100px)"></div></div></div></body>`
	res, doc := layout3D(t, src)
	a, _ := res.ClientRect(byID(doc, "f"))
	b, _ := res.ClientRect(byID(doc, "f2"))
	// overflow forces flattening: translateZ no longer accumulates with rotateY
	if math.Abs(a.X-b.X) < 1 && math.Abs(a.W-b.W) < 1 {
		t.Errorf("preserve-3d and flattened chains give the same rect %v %v", a, b)
	}
	if bx := res.Boxes[byID(doc, "r2")]; bx.Xf3 == nil || bx.Xf3.Preserve {
		t.Errorf("overflow:hidden must force flat")
	}
	if bx := res.Boxes[byID(doc, "r")]; bx.Xf3 == nil || !bx.Xf3.Preserve {
		t.Errorf("preserve-3d not recorded")
	}
}

func TestMat4(t *testing.T) {
	// rotateY(90) maps the x axis onto -z exactly
	m := rotate4(0, 1, 0, 90)
	x, y, z, w := m.apply(1, 0, 0)
	if x != 0 || y != 0 || z != -1 || w != 1 {
		t.Errorf("rotateY(90): %v %v %v %v", x, y, z, w)
	}
	// the back face of a layer rotated by 180 degrees is visible, 0 is not
	if !rotate4(0, 1, 0, 180).backFaceVisible() || rotate4(0, 1, 0, 0).backFaceVisible() || rotate4(0, 1, 0, 100).backFaceVisible() == false {
		t.Errorf("backFaceVisible")
	}
	inv, ok := perspective4(300).mul(rotate4(1, 1, 0, 33)).invert()
	if !ok {
		t.Fatal("not invertible")
	}
	id := perspective4(300).mul(rotate4(1, 1, 0, 33)).mul(inv)
	for i := 0; i < 4; i++ {
		for j := 0; j < 4; j++ {
			want := 0.0
			if i == j {
				want = 1
			}
			if math.Abs(id[i][j]-want) > 1e-9 {
				t.Fatalf("inverse: %v", id)
			}
		}
	}
}

// A cube's faces are emitted back to front: the front face (nearest) paints last.
func TestCubeDepthOrderAndHit(t *testing.T) {
	src := `<!doctype html><body style="margin:0"><style>
.s{perspective:600px;width:300px;height:300px;position:relative}
.c{position:absolute;left:50px;top:50px;width:200px;height:200px;transform-style:preserve-3d;transform:translateZ(-100px) rotateX(-25deg) rotateY(35deg)}
.f{position:absolute;width:200px;height:200px}
#front{background:#f00;transform:translateZ(100px)} #back{background:#0f0;transform:rotateY(180deg) translateZ(100px)}
#right{background:#00f;transform:rotateY(90deg) translateZ(100px)} #left{background:#ff0;transform:rotateY(-90deg) translateZ(100px)}
#top{background:#0ff;transform:rotateX(90deg) translateZ(100px)} #bottom{background:#f0f;transform:rotateX(-90deg) translateZ(100px)}
</style><div class=s><div class=c><div class=f id=front></div><div class=f id=back></div><div class=f id=right></div><div class=f id=left></div><div class=f id=top></div><div class=f id=bottom></div></div></div></body>`
	res, doc := layout3D(t, src)
	pos := map[string]int{}
	names := map[Color]string{{R: 255, A: 255}: "front", {G: 255, A: 255}: "back", {B: 255, A: 255}: "right", {R: 255, G: 255, A: 255}: "left", {G: 255, B: 255, A: 255}: "top", {R: 255, B: 255, A: 255}: "bottom"}
	for i, op := range res.List {
		if f, ok := op.(FillRect); ok {
			if n, ok := names[f.Color]; ok {
				pos[n] = i
			}
		}
	}
	if len(pos) != 6 {
		t.Fatalf("faces painted: %v", pos)
	}
	// rotateY(35deg) turns the left face to the viewer: front, left and top are nearer than back, right and bottom
	for _, pr := range [][2]string{{"back", "front"}, {"right", "left"}, {"bottom", "top"}, {"back", "left"}, {"back", "top"}, {"right", "front"}} {
		if pos[pr[0]] > pos[pr[1]] {
			t.Errorf("%s must paint before %s: %v", pr[0], pr[1], pos)
		}
	}
	// the cube is seen from the front-right-top: those faces are nearer than
	// the back, left and bottom faces
	fr, _ := res.ClientRect(byID(doc, "front"))
	if n := res.Hit(fr.X+fr.W/2, fr.Y+fr.H/2); n == nil || byIDAttr(n) != "front" {
		t.Errorf("hit at the front face centre: %v", n)
	}
	if n := res.Hit(5, 5); n == nil || byIDAttr(n) == "front" {
		t.Errorf("hit outside the cube: %v", n)
	}
}

func byIDAttr(n *html.Node) string {
	for _, a := range n.Attr {
		if a.Key == "id" {
			return a.Val
		}
	}
	return ""
}
