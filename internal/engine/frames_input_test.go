package engine

import "testing"

// ---- input -------------------------------------------------------------------------------

const clickPage = `<!doctype html><body style="margin:0">
<button id="b" style="position:absolute;left:10px;top:10px;width:80px;height:30px">go</button>
<input id="i" style="position:absolute;left:10px;top:60px;width:120px;height:24px">
<script>
window.clicks = 0; window.keys = '';
document.getElementById('b').addEventListener('click', function () { window.clicks++; });
document.getElementById('i').addEventListener('input', function (e) { window.keys = e.target.value; });
</script></body>`

func firstFrame(t *testing.T, tab *Tab) *Tab {
	t.Helper()
	fr := tab.Frames()
	if len(fr) == 0 {
		t.Fatal("no frames")
	}
	return fr[0].Tab
}

func TestClickReachesCrossOriginFrame(t *testing.T) {
	child := newSite(t, map[string]string{"/": clickPage})
	parent := newSite(t, map[string]string{"/": `<!doctype html><body style="margin:0">
<h1 style="margin:0;height:50px">top</h1>
<iframe src="` + child.URL + `/" style="border:0;width:200px;height:120px;display:block;margin-left:40px"></iframe></body>`})
	e := newEng(t)
	tab, err := e.NewTab(parent.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	c := firstFrame(t, tab)
	if got := evalStr(t, c, `String(window.clicks)`); got != "0" {
		t.Fatalf("clicks before = %s", got)
	}
	// the button is at (10,10)-(90,40) inside the frame, which sits at (40,50) of the page
	if err := tab.MouseClick(40+50, 50+25); err != nil {
		t.Fatal(err)
	}
	if got := evalStr(t, c, `String(window.clicks)`); got != "1" {
		t.Fatalf("click did not reach the frame's handler: clicks = %s", got)
	}
	// a click outside the frame does not
	if err := tab.MouseClick(5, 20); err != nil {
		t.Fatal(err)
	}
	if got := evalStr(t, c, `String(window.clicks)`); got != "1" {
		t.Fatalf("click outside the frame reached it: %s", got)
	}
}

func TestKeyboardIntoFrameInput(t *testing.T) {
	child := newSite(t, map[string]string{"/": clickPage})
	parent := newSite(t, map[string]string{"/": `<!doctype html><body style="margin:0">
<input id="top" style="position:absolute;left:0;top:0;width:100px;height:20px">
<iframe src="` + child.URL + `/" style="border:0;position:absolute;left:0;top:40px;width:200px;height:120px"></iframe></body>
<script>window.topKeys=''; document.getElementById('top').addEventListener('input', function(e){ window.topKeys = e.target.value; });</script>`})
	e := newEng(t)
	tab, err := e.NewTab(parent.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	c := firstFrame(t, tab)
	// click into the frame's input (frame at (0,40); input at (10,60) in the frame)
	if err := tab.MouseClick(20, 40+70); err != nil {
		t.Fatal(err)
	}
	if tab.FocusedTab() != c {
		t.Fatal("the frame did not take focus")
	}
	for _, k := range []string{"h", "i", "!"} {
		if err := tab.Keyboard(k); err != nil {
			t.Fatal(err)
		}
	}
	if got := evalStr(t, c, `window.keys`); got != "hi!" {
		t.Fatalf("frame input = %q", got)
	}
	if got := evalStr(t, tab, `window.topKeys`); got != "" {
		t.Fatalf("keys leaked into the top document: %q", got)
	}
	// clicking the top input moves focus back
	if err := tab.MouseClick(20, 10); err != nil {
		t.Fatal(err)
	}
	if err := tab.Keyboard("x"); err != nil {
		t.Fatal(err)
	}
	if got := evalStr(t, tab, `window.topKeys`); got != "x" {
		t.Fatalf("top input = %q", got)
	}
	if got := evalStr(t, c, `window.keys`); got != "hi!" {
		t.Fatalf("frame input changed to %q", got)
	}
}

func TestNestedFrames(t *testing.T) {
	leaf := newSite(t, map[string]string{"/": clickPage})
	mid := newSite(t, map[string]string{"/": `<!doctype html><body style="margin:0;background:#00f">
<iframe src="` + leaf.URL + `/" style="border:0;position:absolute;left:30px;top:20px;width:150px;height:90px;background:#fff"></iframe></body>`})
	top := newSite(t, map[string]string{"/": `<!doctype html><body style="margin:0">
<iframe src="` + mid.URL + `/" style="border:0;position:absolute;left:50px;top:40px;width:250px;height:150px"></iframe></body>`})
	e := newEng(t)
	tab, err := e.NewTab(top.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	img, _ := tab.PaintRGBA(400, 300, 1, false)
	if c := px(img, 60, 50); !isColor(c, 0, 0, 255) {
		t.Fatalf("middle frame background: %v", c)
	}
	if c := px(img, 50+30+75, 40+20+80); !isColor(c, 255, 255, 255) {
		t.Fatalf("nested frame background: %v", c)
	}
	mc := firstFrame(t, tab)
	lc := firstFrame(t, mc)
	if lc == mc || lc.Top() != tab {
		t.Fatal("nested frame is not wired to the top")
	}
	// the leaf's button is at (10,10) inside the leaf, which sits at (80,60) of the page
	if err := tab.MouseClick(80+50, 60+25); err != nil {
		t.Fatal(err)
	}
	if got := evalStr(t, lc, `String(window.clicks)`); got != "1" {
		t.Fatalf("click did not reach the nested frame: %s", got)
	}
}
