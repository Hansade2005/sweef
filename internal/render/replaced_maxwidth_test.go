package render

import (
	"image"
	"testing"
)

// A replaced element with max-width:100% inside a shrink-to-fit box contributes
// its intrinsic width (the percentage resolves against the box it sizes), so the
// box is the image's size, not 0.
func TestReplacedPercentMaxWidthShrinkToFit(t *testing.T) {
	im := image.NewRGBA(image.Rect(0, 0, 48, 48))
	doc := parseHTML(t, `<body style="margin:0"><div id="f" style="float:left"><img id="i" src="pic.png" style="max-width:100%;height:auto"></div></body>`)
	res := Layout(doc, Options{Width: 800, Height: 600, BaseURL: "http://x/p.html", Env: imgEnv{im: im}})
	r, ok := res.ClientRect(findByID(doc, "f"))
	if !ok || r.W < 1 {
		t.Fatalf("float box collapsed: %v ok=%v", r, ok)
	}
	t.Logf("float %v", r)
}
