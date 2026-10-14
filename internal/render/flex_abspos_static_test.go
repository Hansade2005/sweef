package render

import "testing"

// An absolutely positioned child of a flex container with auto insets sits where it would
// as the sole flex item: justify-content on the main axis, align-self/align-items on the
// cross axis (CSS Flexbox 4.1). Expected positions are Chromium 141's, relative to each
// container (viewport 400 wide, so the content box is 400 px).
func TestFlexAbsposStaticPositionAligned(t *testing.T) {
	doc := parseHTML(t, `<!doctype html><body style="margin:0"><style>.f{display:flex;position:relative}.a{position:absolute;width:20px;height:20px}</style>
<div class=f style="align-items:center;height:60px"><span class=a id=a1></span><i style="display:block;width:10px;height:10px"></i></div>
<div class=f style="justify-content:flex-end;height:30px"><span class=a id=a2></span></div>
<div class=f style="flex-direction:column;align-items:center;height:30px"><span class=a id=a3></span></div>
<div class=f style="flex-direction:row-reverse;height:30px"><span class=a id=a4></span></div>
<div class=f style="height:30px"><span class=a id=a5></span></div></body>`)
	var sheets []Sheet
	collectSheets(doc, &sheets)
	res := Layout(doc, Options{Width: 400, Height: 300, Sheets: sheets})
	want := map[string][2]float64{"a1": {0, 20}, "a2": {380, 60}, "a3": {190, 90}, "a4": {380, 120}, "a5": {0, 150}}
	for id, w := range want {
		b := res.Boxes[findID(doc, id)]
		if b == nil || b.Border.X != w[0] || b.Border.Y != w[1] {
			t.Errorf("%s: want (%v,%v), got %+v", id, w[0], w[1], b)
		}
	}
}

// Quirks mode: body fills the viewport minus its used margins. A first child's top margin
// that collapses through body moves body down, so the fill must shrink by the same amount,
// or the page overflows by the difference and grows a scrollbar it does not need.
func TestQuirksBodyFillUsesCollapsedTopMargin(t *testing.T) {
	for _, c := range []struct {
		src  string
		y, h float64
	}{
		{`<div style="margin-top:10px">x</div>`, 10, 702},
		{`<body style="margin:0"><div style="margin-top:10px">x</div>`, 10, 710},
		{`<div>x</div>`, 8, 704},
	} {
		doc := parseHTML(t, c.src)
		res := Layout(doc, Options{Width: 1280, Height: 720})
		var body *Box
		for n, b := range res.Boxes {
			if n.Data == "body" {
				body = b
			}
		}
		if body == nil || body.Border.Y != c.y || body.Border.H != c.h || res.Height != 720 {
			t.Errorf("%s: body %+v, doc height %v; want y %v h %v, doc height 720", c.src, body, res.Height, c.y, c.h)
		}
	}
}
