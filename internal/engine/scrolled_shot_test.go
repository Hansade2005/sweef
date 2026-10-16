package engine

import (
	"bytes"
	"image/png"
	"testing"
)

// A viewport screenshot of a scrolled page shows the scrolled viewport; position:fixed content stays put.
func TestScreenshotFollowsScroll(t *testing.T) {
	e := New(Options{CacheDir: "off"})
	defer e.Close()
	tab, _ := e.NewTab("about:blank")
	tab.SetViewport(400, 300, 1, false, false)
	tab.SetDocument(`<body style="margin:0"><div style="position:fixed;top:0;left:0;width:100%;height:40px;background:#f00"></div>
<div style="height:1000px;background:#00f"></div><div style="height:1000px;background:#0f0"></div></body>`, "https://shot.test/")
	mustEval(t, tab, `window.scrollTo(0, 1200), 1`)
	im, err := png.Decode(bytes.NewReader(tab.PNGScreenshot(400, 300, 1)))
	if err != nil {
		t.Fatal(err)
	}
	r, g, b, _ := im.At(200, 10).RGBA()
	if r>>8 != 255 || g>>8 != 0 || b>>8 != 0 {
		t.Fatalf("fixed header moved: %v %v %v", r>>8, g>>8, b>>8)
	}
	r, g, b, _ = im.At(200, 150).RGBA()
	if g>>8 != 255 || r>>8 != 0 || b>>8 != 0 {
		t.Fatalf("not scrolled: %v %v %v", r>>8, g>>8, b>>8)
	}
}
