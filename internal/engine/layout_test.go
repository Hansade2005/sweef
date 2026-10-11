package engine

import (
	"bytes"
	"encoding/json"
	"image/png"
	"strings"
	"testing"
)

const layoutHTML = `<!doctype html><html><head><title>L</title></head><body>
<h1>Title</h1>
<p>Some intro text that is long enough to wrap when the viewport is narrow, so the layout has more than one line.</p>
<ul><li>One</li><li>Two</li></ul>
<form><input id="email" placeholder="Email"><button id="go" type="button">Go</button></form>
<a id="more" href="#more">More</a>
</body></html>`

func TestLayoutGeometryIsConsistent(t *testing.T) {
	e := New(DefaultOptions())
	defer e.Close()
	tab, _ := e.NewTab("about:blank")
	tab.SetViewport(420, 600, 1, false, false)
	tab.SetDocument(layoutHTML, "https://layout.test/")
	boxOf := func(sel string) [4]int {
		b, err := tab.Locator(sel).BoundingBox()
		if err != nil {
			t.Fatalf("%s: %v", sel, err)
		}
		if b[2] <= 0 || b[3] <= 0 {
			t.Fatalf("%s: empty box %v", sel, b)
		}
		return b
	}
	h1, p, btn, email, more := boxOf("h1"), boxOf("p"), boxOf("#go"), boxOf("#email"), boxOf("#more")
	if !(h1[1] < p[1] && p[1] < email[1] && email[1] <= more[1]) {
		t.Fatalf("boxes not in document order: h1=%v p=%v email=%v more=%v", h1, p, email, more)
	}
	if p[3] < 2*h1[3]/3 {
		t.Fatalf("paragraph should wrap at 420px: %v", p)
	}
	if btn[0] <= email[0] {
		t.Fatalf("button should follow the input on the same line: email=%v btn=%v", email, btn)
	}
	// a click at the centre of the button's box lands on the button
	id, err := tab.hitTest(btn[0]+btn[2]/2, btn[1]+btn[3]/2)
	want, _ := tab.Locator("#go").BackendID()
	if err != nil || id != want {
		t.Fatalf("hit test at button centre: got %d want %d (%v)", id, want, err)
	}
	// getBoundingClientRect in page JS reports the same box
	r := mustEval(t, tab, `JSON.stringify((r => [r.x, r.y, r.width, r.height])(document.getElementById('go').getBoundingClientRect()))`)
	var rect []float64
	if err := json.Unmarshal([]byte(strings.Trim(strings.ReplaceAll(r, `\"`, `"`), `"`)), &rect); err != nil || len(rect) != 4 {
		t.Fatalf("getBoundingClientRect %s: %v", r, err)
	}
	for i, v := range []int{btn[0], btn[1], btn[2], btn[3]} {
		if d := rect[i] - float64(v); d > 1 || d < -1 {
			t.Fatalf("getBoundingClientRect %s vs box %v", r, btn)
		}
	}
	// the screenshot paints something non-white inside the button's box
	img, err := png.Decode(bytes.NewReader(tab.PNGScreenshot(420, 600, 1)))
	if err != nil {
		t.Fatal(err)
	}
	r0, g0, b0, _ := img.At(btn[0]+2, btn[1]+2).RGBA()
	if r0 == 0xffff && g0 == 0xffff && b0 == 0xffff {
		t.Fatal("screenshot has nothing at the button's box")
	}
	full, _ := png.Decode(bytes.NewReader(tab.PNGScreenshotFull(420, 1)))
	if full.Bounds().Dy() > 600 || full.Bounds().Dy() < more[1] {
		t.Fatalf("full-page height %d should cover content to y=%d", full.Bounds().Dy(), more[1])
	}
}
