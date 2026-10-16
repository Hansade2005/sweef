package render

import (
	"strings"
	"testing"

	"golang.org/x/net/html"
)

type scrollEnvStub struct {
	nilEnv
	offs map[*html.Node][2]float64
}

func (e scrollEnvStub) ScrollOffset(n *html.Node) (float64, float64) {
	o := e.offs[n]
	return o[0], o[1]
}

const scrollFixture = `<!doctype html><html><body style="margin:0">
<div id="sc" style="width:200px;height:100px;overflow:auto;position:relative">
<div id="a" style="height:60px;background:#f00">alpha</div>
<div id="b" style="height:60px;background:#0f0"><span id="sp">beta <b>bold</b></span></div>
<div id="c" style="height:60px;background:#00f">gamma</div>
<div id="inner" style="height:80px;overflow:auto"><div style="height:200px">inner content</div></div>
</div>
<div id="after" style="height:40px">after</div>
</body></html>`

func layoutWithScroll(t *testing.T, offs func(doc *html.Node) map[*html.Node][2]float64) (*html.Node, *Result) {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(scrollFixture))
	if err != nil {
		t.Fatal(err)
	}
	env := scrollEnvStub{offs: offs(doc)}
	return doc, Layout(doc, Options{Width: 800, Height: 600, DPR: 1, Env: env})
}

func TestScrollOffsetShiftsContentNotTheBox(t *testing.T) {
	var sc *html.Node
	doc, base := layoutWithScroll(t, func(d *html.Node) map[*html.Node][2]float64 { return nil })
	sc = findID(doc, "sc")
	baseB := base.Boxes[findID(doc, "b")].Border
	_, res := layoutWithScroll(t, func(d *html.Node) map[*html.Node][2]float64 {
		return map[*html.Node][2]float64{findID(d, "sc"): {0, 70}}
	})
	_ = sc
	// a different document instance: look the nodes up again by id
	doc2, res2 := layoutWithScroll(t, func(d *html.Node) map[*html.Node][2]float64 {
		return map[*html.Node][2]float64{findID(d, "sc"): {0, 70}}
	})
	_ = res
	scBox := res2.Boxes[findID(doc2, "sc")]
	if scBox.Border.Y != 0 || scBox.Border.H != 100 {
		t.Fatalf("the scroll container itself must not move: %+v", scBox.Border)
	}
	if scBox.ScrollTop != 70 {
		t.Fatalf("ScrollTop %v, want 70", scBox.ScrollTop)
	}
	// the container has 60*3+80 = 260px of content in a 100px box
	if scBox.ScrollH < 259 || scBox.ScrollH > 261 {
		t.Fatalf("ScrollH %v", scBox.ScrollH)
	}
	b := res2.Boxes[findID(doc2, "b")].Border
	if b.Y != baseB.Y-70 {
		t.Fatalf("child #b at y=%v, want %v (%v - 70)", b.Y, baseB.Y-70, baseB.Y)
	}
	// the display list moved the same way: the backgrounds of a, b, c
	var ys []float64
	for _, op := range res2.List {
		if f, ok := op.(FillRect); ok && f.Color.A == 255 && (f.Color.R == 255 || f.Color.G == 255 || f.Color.B == 255) && f.Rect.W >= 180 && f.Rect.W <= 200 {
			ys = append(ys, f.Rect.Y)
		}
	}
	if len(ys) != 3 || ys[0] != -70 || ys[1] != -10 || ys[2] != 50 {
		t.Fatalf("backgrounds at %v, want [-70 -10 50]", ys)
	}
	// text moved too, including text of an inline box
	var textYs []float64
	for _, op := range res2.List {
		if tr, ok := op.(TextRun); ok && len(tr.Glyphs) > 0 {
			textYs = append(textYs, tr.Y)
		}
	}
	if len(textYs) < 3 {
		t.Fatalf("expected text runs, got %d", len(textYs))
	}
	// hit testing follows the scrolled content: page y=30 inside the container is now in #b (rows 50..110 of content)
	if n := res2.Hit(20, 5); n == nil || (attrOfNode(n, "id") != "b" && attrOfNode(n.Parent, "id") != "b") {
		t.Fatalf("hit at (20,5) = %v, want #b or its text", describe(n))
	}
	// below the container the page is unaffected
	if n := res2.Hit(20, 120); attrOfNode(n, "id") != "after" {
		t.Fatalf("hit below the container = %v", describe(n))
	}
	// clipped content is not hittable outside the container
	if n := res2.Hit(20, 100); n != nil && attrOfNode(n, "id") == "inner" {
		t.Fatalf("content scrolled out of the container's box must not be hit outside it: %v", describe(n))
	}
}

func TestScrollOffsetNestedAndScrollbarThumb(t *testing.T) {
	doc, res := layoutWithScroll(t, func(d *html.Node) map[*html.Node][2]float64 {
		return map[*html.Node][2]float64{findID(d, "sc"): {0, 100}, findID(d, "inner"): {0, 30}}
	})
	in := res.Boxes[findID(doc, "inner")]
	if in.ScrollTop != 30 {
		t.Fatalf("inner ScrollTop %v", in.ScrollTop)
	}
	// inner sits at content y=180 minus the outer scroll 100 = 80 and keeps its own size
	if in.Border.Y != 80 || in.Border.H != 80 {
		t.Fatalf("inner border %+v", in.Border)
	}
	// both scrollbars were drawn and the thumbs sit below the track start
	var thumbs []float64
	for _, op := range res.List {
		if sb, ok := op.(Scrollbar); ok && sb.Vertical && sb.ThumbLen > 0 {
			thumbs = append(thumbs, sb.ThumbOff)
		}
	}
	if len(thumbs) < 2 {
		t.Fatalf("expected the thumbs of two vertical scrollbars, got %v", thumbs)
	}
	for _, off := range thumbs {
		if off <= 18 {
			t.Fatalf("thumb offset %v: a scrolled container's thumb must have left the top of the track", thumbs)
		}
	}
}

func TestNoScrollEnvNoChange(t *testing.T) {
	doc, err := html.Parse(strings.NewReader(scrollFixture))
	if err != nil {
		t.Fatal(err)
	}
	a := Layout(doc, Options{Width: 800, Height: 600, DPR: 1})
	b := Layout(doc, Options{Width: 800, Height: 600, DPR: 1, Env: scrollEnvStub{}})
	if d := DiffResults(a, b); d != "" {
		t.Fatalf("a ScrollEnv with no offsets changed the result: %s", d)
	}
}

func attrOfNode(n *html.Node, k string) string {
	if n == nil {
		return ""
	}
	for _, a := range n.Attr {
		if a.Key == k {
			return a.Val
		}
	}
	return ""
}

func describe(n *html.Node) string {
	if n == nil {
		return "<nil>"
	}
	return n.Data + "#" + attrOfNode(n, "id")
}
