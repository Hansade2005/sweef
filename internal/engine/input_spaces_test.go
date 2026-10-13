package engine

import (
	"image"
	"testing"
)

// inkWidth is the horizontal extent of the dark pixels of img inside r.
func inkWidth(img *image.RGBA, r image.Rectangle) (minX, maxX int) {
	minX, maxX = 1<<30, -1
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			c := img.RGBAAt(x, y)
			if int(c.R)+int(c.G)+int(c.B) < 3*140 {
				if x < minX {
					minX = x
				}
				if x > maxX {
					maxX = x
				}
			}
		}
	}
	return
}

// A text input draws a space as one space: its value "the quick brown fox" is as wide as the same text in a
// white-space:pre span (the omnibox once showed "a  the  quick" because each typed space was inserted twice, so
// the input, which keeps spaces, looked gapped while the dropdown row, which collapses them, looked right), and
// a second space is visibly wider.
func TestTextInputSpacesAreOneSpaceWide(t *testing.T) {
	e := New(Options{ViewportWidth: 700, ViewportHeight: 200, CacheDir: "off", SettleMs: 50})
	defer e.Close()
	tab, err := e.NewTab("about:blank")
	if err != nil {
		t.Fatal(err)
	}
	const font = "font:16px sans-serif;margin:0;padding:0;border:0;letter-spacing:0;word-spacing:0"
	tab.SetDocument(`<!doctype html><html><body style="margin:0;background:#fff;color:#000">
<div style="height:40px"><input id="one" value="the quick brown fox" style="`+font+`;width:600px;height:30px"></div>
<div style="height:40px"><input id="two" style="`+font+`;width:600px;height:30px"></div>
<div style="height:40px"><span id="ref" style="`+font+`;white-space:pre">the quick brown fox</span></div>
<div style="height:40px"><input id="dbl" value="the  quick  brown  fox" style="`+font+`;width:600px;height:30px"></div>
</body></html>`, "http://x/")
	if _, err := tab.Evaluate(`(function(){var u=document.getElementById('two');u.focus();u.value='the quick';var e=['t','h','e',' ','q','u','i','c','k',' ','b','r','o','w','n',' ','f','o','x'];u.value='';e.forEach(function(k){__swDispatchKey(__swIdOf(u),k,{})})})()`); err != nil {
		t.Fatal(err)
	}
	img, ok := tab.PaintRGBA(700, 200, 1, false)
	if !ok {
		t.Fatal("no paint")
	}
	row := func(i int) image.Rectangle { return image.Rect(0, i*40, 700, i*40+40) }
	w := func(i int) int {
		lo, hi := inkWidth(img, row(i))
		if hi < 0 {
			t.Fatalf("row %d drew no text", i)
		}
		return hi - lo
	}
	one, typed, ref, dbl := w(0), w(1), w(2), w(3)
	if d := one - ref; d < -1 || d > 1 {
		t.Errorf("input value is %dpx wide, the same text in a pre span %dpx", one, ref)
	}
	if d := typed - ref; d < -1 || d > 1 {
		t.Errorf("typed input (one key event per character) is %dpx wide, want %dpx: spaces were inserted twice or widened", typed, ref)
	}
	if dbl < ref+10 {
		t.Errorf("a value with doubled spaces (%dpx) must be visibly wider than single spaces (%dpx)", dbl, ref)
	}
}
