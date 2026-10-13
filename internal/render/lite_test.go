package render

import (
	"image"
	"testing"
)

type hintEnv struct {
	nilEnv
	hints map[string]Rect
}

func (e *hintEnv) ImageHint(u string, r Rect) { e.hints[u] = r }

func TestLitePlaceholderKeepsDeclaredSize(t *testing.T) {
	doc := parseHTML(t, `<body style="margin:0"><p id="a" style="margin:0;height:20px">x</p>
<img id="i" src="/big.png" data-sweef-lite="skipped" width="320" height="200" alt="Hero">
<img id="j" src="/other.png" data-sweef-lite="skipped">
<img id="w" src="/w.png" data-sweef-lite="skipped" width="100">
<iframe id="f" src="http://ads.example/x" data-sweef-lite="frame"></iframe></body>`)
	res := Layout(doc, Options{Width: 800, Height: 600})
	box := func(id string) Rect {
		b := res.Boxes[findByID(doc, id)]
		if b == nil {
			t.Fatalf("no box for #%s", id)
		}
		return b.Border
	}
	if r := box("i"); r.W != 320 || r.H != 200 {
		t.Errorf("declared size: %+v", r)
	}
	if r := box("j"); r.W != 300 || r.H != 150 {
		t.Errorf("default size should be 300x150: %+v", r)
	}
	if r := box("w"); r.W != 100 || r.H != 50 {
		t.Errorf("width only: %+v", r)
	}
	if r := box("f"); r.W < 300 || r.H < 150 {
		t.Errorf("frame placeholder: %+v", r)
	}
	var draws int
	for _, op := range res.List {
		if d, ok := op.(SVGDraw); ok && d.Dst.W > 90 {
			draws++
		}
	}
	if draws != 4 {
		t.Errorf("each placeholder paints one labelled box, got %d draws", draws)
	}
}

func TestImageHinterReportsDeferredImages(t *testing.T) {
	doc := parseHTML(t, `<body style="margin:0"><div style="height:500px"></div><img src="/late.png" width="100" height="50">
<div style="background-image:url(/bg.png);height:80px"></div></body>`)
	env := &hintEnv{hints: map[string]Rect{}}
	Layout(doc, Options{Width: 800, Height: 600, Env: env, BaseURL: "http://h.test/"})
	r, ok := env.hints["http://h.test/late.png"]
	if !ok || r.Y != 500 || r.W != 100 || r.H != 50 {
		t.Fatalf("hint for the <img>: %+v %v (%v)", r, ok, env.hints)
	}
	if r, ok := env.hints["/bg.png"]; !ok || r.Y < 550 {
		t.Fatalf("hint for the CSS background: %+v %v (%v)", r, ok, env.hints)
	}
	_ = image.Rect
}
