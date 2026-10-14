package engine

import "testing"

func TestFrameBorderPaddingAndScale(t *testing.T) {
	child := newSite(t, map[string]string{"/": `<!doctype html><body style="margin:0;background:#f00">`})
	parent := newSite(t, map[string]string{"/": `<!doctype html><body style="margin:0;background:#fff">
<iframe src="` + child.URL + `/" style="position:absolute;left:10px;top:10px;box-sizing:content-box;width:50px;height:40px;border:5px solid #00f;padding:3px"></iframe>`})
	e := newEng(t)
	tab, err := e.NewTab(parent.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	// 1x: border box (10,10)-(76,66); border 5, padding 3, so the content starts at (18,18) and is 50x40
	img, _ := tab.PaintRGBA(120, 100, 1, false)
	if c := px(img, 12, 12); !isColor(c, 0, 0, 255) {
		t.Fatalf("border: %v", c)
	}
	if c := px(img, 16, 30); !isColor(c, 255, 255, 255) {
		t.Fatalf("padding shows the parent, not the frame: %v", c)
	}
	if c := px(img, 20, 20); !isColor(c, 255, 0, 0) {
		t.Fatalf("content: %v", c)
	}
	if c := px(img, 17, 17); isColor(c, 255, 0, 0) {
		t.Fatalf("the frame leaked into the padding: %v", c)
	}
	if c := px(img, 70, 40); !isColor(c, 255, 255, 255) {
		t.Fatalf("right of the content box (padding): %v", c)
	}
	// 2x: the frame is painted at the device scale, edges stay sharp
	img2, _ := tab.PaintRGBA(120, 100, 2, false)
	if img2.Bounds().Dx() != 240 {
		t.Fatalf("width %d", img2.Bounds().Dx())
	}
	if c := px(img2, 40, 40); !isColor(c, 255, 0, 0) {
		t.Fatalf("content at 2x: %v", c)
	}
	if c := px(img2, 35, 40); isColor(c, 255, 0, 0) {
		t.Fatalf("2x frame bled out of its box: %v", c)
	}
	if c := px(img2, 36, 40); !isColor(c, 255, 0, 0) {
		t.Fatalf("2x frame edge should be exact (content box starts at device x=36): %v", c)
	}
}
