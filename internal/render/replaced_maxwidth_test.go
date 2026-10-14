package render

import (
	"image"
	"testing"
)

// An image with max-width: 100% (Tailwind's base reset puts it on every img) inside a
// shrink-to-fit box used to measure its intrinsic width against a containing width of 0,
// so the percentage clamped it to 0 px. Expected values are Chromium 141's.
func TestReplacedPercentMaxWidthShrinkToFit(t *testing.T) {
	sq := imgEnv{im: image.NewRGBA(image.Rect(0, 0, 320, 320))}
	doc := parseHTML(t, `<body style="margin:0"><style>img{display:block;max-width:100%;height:48px;width:auto}</style>
<span style="display:inline-flex"><img id=a src="pic.png"></span><br>
<span style="display:inline-block"><img id=b src="pic.png"></span><br>
<div style="float:left"><img id=c src="pic.png"></div><br style="clear:both">
<div style="display:flex"><div><img id=d src="pic.png"></div></div>
<div style="width:max-content"><img id=e src="pic.png"></div></body>`)
	var sheets []Sheet
	collectSheets(doc, &sheets)
	res := Layout(doc, Options{Width: 1280, Height: 800, BaseURL: "http://x/p.html", Env: sq, Sheets: sheets})
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		b := res.Boxes[findID(doc, id)]
		if b == nil || b.Border.W != 48 || b.Border.H != 48 {
			t.Errorf("%s: want 48x48, got %+v", id, b)
		}
	}

	// The percentage still clamps against a real width, and a flex item holding such an
	// image may shrink below the image's natural width (its min-content contribution is 0).
	wide := imgEnv{im: image.NewRGBA(image.Rect(0, 0, 1000, 500))}
	doc = parseHTML(t, `<body style="margin:0"><style>img{max-width:100%;height:auto;display:block}</style>
<div style="width:300px"><img id=a src="pic.png"></div>
<span style="display:inline-block;width:200px"><img id=b src="pic.png"></span>
<div style="display:flex;width:400px"><div><img id=c src="pic.png"></div></div></body>`)
	sheets = nil
	collectSheets(doc, &sheets)
	res = Layout(doc, Options{Width: 1280, Height: 800, BaseURL: "http://x/p.html", Env: wide, Sheets: sheets})
	for id, want := range map[string][2]float64{"a": {300, 150}, "b": {200, 100}, "c": {400, 200}} {
		b := res.Boxes[findID(doc, id)]
		if b == nil || b.Border.W != want[0] || b.Border.H != want[1] {
			t.Errorf("%s: want %vx%v, got %+v", id, want[0], want[1], b)
		}
	}
}
