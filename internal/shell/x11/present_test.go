//go:build linux

package x11

import (
	"image"
	"image/color"
	"testing"
)

func fill(img *image.RGBA, r image.Rectangle, c color.RGBA) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			img.SetRGBA(x, y, c)
		}
	}
}

func testPresent(t *testing.T, noSHM bool) {
	disableSHM.Store(noSHM)
	defer disableSHM.Store(false)
	w := openTest(t, shellCfg(800, 600))
	if noSHM && w.UsesSHM() {
		t.Fatal("SHM not disabled")
	}
	if !noSHM && !w.UsesSHM() {
		t.Log("MIT-SHM unavailable; testing core PutImage only")
	}
	t.Logf("SHM=%v", w.UsesSHM())
	tc := testConn(t)

	frame := gradient(800, 600)
	w.Present(frame, image.Rectangle{})
	w.Sync()
	got := grab(t, tc, w.win, image.Rect(0, 0, 800, 600))
	if x, y, ok := samePixels(frame, got); !ok {
		t.Fatalf("full-frame pixel mismatch at %d,%d", x, y)
	}

	// Damage: only the rect changes on screen even if the rest of the frame
	// buffer was modified (proves damage is honoured).
	red := color.RGBA{255, 0, 0, 255}
	fill(frame, image.Rect(0, 0, 800, 600), color.RGBA{0, 255, 0, 255}) // not damaged
	dmg := image.Rect(100, 50, 301, 151)
	fill(frame, dmg, red)
	w.Present(frame, dmg)
	w.Sync()
	got = grab(t, tc, w.win, image.Rect(0, 0, 800, 600))
	for _, p := range []struct {
		x, y int
		want color.RGBA
	}{
		{100, 50, red}, {300, 150, red}, {200, 100, red},
		{99, 50, gradient(800, 600).RGBAAt(99, 50)}, {301, 100, gradient(800, 600).RGBAAt(301, 100)},
		{200, 151, gradient(800, 600).RGBAAt(200, 151)}, {0, 0, gradient(800, 600).RGBAAt(0, 0)},
	} {
		g := got.RGBAAt(p.x, p.y)
		if g.R != p.want.R || g.G != p.want.G || g.B != p.want.B {
			t.Fatalf("pixel %d,%d = %v want %v", p.x, p.y, g, p.want)
		}
	}
	if e := w.LastError(); e != nil {
		t.Fatal(e)
	}
}

func TestPresentSHM(t *testing.T)      { testPresent(t, false) }
func TestPresentPutImage(t *testing.T) { testPresent(t, true) }

func TestPresentLargeStrips(t *testing.T) {
	// Larger than one X request: exercises multi-strip core PutImage.
	disableSHM.Store(true)
	defer disableSHM.Store(false)
	w := openTest(t, shellCfg(1900, 1000))
	tc := testConn(t)
	frame := gradient(1900, 1000)
	w.Present(frame, image.Rectangle{})
	w.Sync()
	got := grab(t, tc, w.win, image.Rect(0, 0, 1900, 1000))
	if x, y, ok := samePixels(frame, got); !ok {
		t.Fatalf("mismatch at %d,%d", x, y)
	}
}

func TestPresentSubImageOffsetAndClip(t *testing.T) {
	w := openTest(t, shellCfg(400, 300))
	tc := testConn(t)
	// A frame larger than the window must be clipped, not rejected.
	frame := gradient(500, 400)
	w.Present(frame, image.Rect(-10, -10, 1000, 1000))
	w.Sync()
	if e := w.LastError(); e != nil {
		t.Fatal(e)
	}
	got := grab(t, tc, w.win, image.Rect(0, 0, 400, 300))
	want := frame.SubImage(image.Rect(0, 0, 400, 300)).(*image.RGBA)
	if x, y, ok := samePixels(want, got); !ok {
		t.Fatalf("mismatch at %d,%d", x, y)
	}
}

func TestResizeKeepsPicture(t *testing.T) {
	w := openTest(t, shellCfg(300, 200))
	tc := testConn(t)
	frame := gradient(300, 200)
	w.Present(frame, image.Rectangle{})
	w.Sync()
	tc.configureWindow(w.win, 4|8, 500, 400)
	tc.sync()
	e := next(t, w, func(e shell0Event) bool { return e.Kind == evResize && e.W == 500 })
	if e.H != 400 || e.Scale != 1 {
		t.Fatalf("resize event %+v", e)
	}
	big := gradient(500, 400)
	w.Present(big, image.Rectangle{})
	w.Sync()
	got := grab(t, tc, w.win, image.Rect(0, 0, 500, 400))
	if x, y, ok := samePixels(big, got); !ok {
		t.Fatalf("mismatch at %d,%d", x, y)
	}
}

func TestExposeRepaintsFromShadow(t *testing.T) {
	w := openTest(t, shellCfg(320, 240))
	tc := testConn(t)
	frame := gradient(320, 240)
	w.Present(frame, image.Rectangle{})
	w.Sync()
	// Another client scribbles over the window, then an unmap/map cycle
	// generates Expose; the shell must restore its picture.
	gc := tc.allocID()
	g := request(opCreateGC, 0, 16)
	le.PutUint32(g[4:], gc)
	le.PutUint32(g[8:], w.win)
	tc.send(g, false)
	row := make([]byte, 320*4)
	for i := range row {
		row[i] = 0x55
	}
	for y := 0; y < 240; y++ {
		b := make([]byte, 24+len(row))
		b[0], b[1] = opPutImage, 2
		le.PutUint16(b[2:], uint16(len(b)/4))
		le.PutUint32(b[4:], w.win)
		le.PutUint32(b[8:], gc)
		le.PutUint16(b[12:], 320)
		le.PutUint16(b[14:], 1)
		le.PutUint16(b[18:], uint16(y))
		b[21] = 24
		copy(b[24:], row)
		tc.send(b, false)
	}
	tc.sync()
	mid := grab(t, tc, w.win, image.Rect(0, 0, 320, 240))
	if _, _, ok := samePixels(frame, mid); ok {
		t.Fatal("scribble did not take effect")
	}
	unmap := request(10, 0, 8)
	le.PutUint32(unmap[4:], w.win)
	tc.send(unmap, false)
	tc.mapWindow(w.win)
	tc.sync()
	deadline := 50
	for ; deadline > 0; deadline-- {
		w.Sync()
		got := grab(t, tc, w.win, image.Rect(0, 0, 320, 240))
		if _, _, ok := samePixels(frame, got); ok {
			return
		}
		sleepMs(20)
	}
	t.Fatal("Expose did not restore the window contents")
}
