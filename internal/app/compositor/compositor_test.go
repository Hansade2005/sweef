package compositor

import (
	"image"
	"image/color"
	"testing"
)

func solid(w, h int, c color.RGBA) *image.RGBA {
	m := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(m.Pix); i += 4 {
		m.Pix[i], m.Pix[i+1], m.Pix[i+2], m.Pix[i+3] = c.R, c.G, c.B, c.A
	}
	return m
}

func stripes(w, h int) *image.RGBA { // page whose row y has colour (y%251, 0, 0)
	m := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			o := m.PixOffset(x, y)
			m.Pix[o], m.Pix[o+1], m.Pix[o+2], m.Pix[o+3] = uint8(y%251), 0, 0, 255
		}
	}
	return m
}

var (
	red   = color.RGBA{200, 0, 0, 255}
	blue  = color.RGBA{0, 0, 200, 255}
	green = color.RGBA{0, 200, 0, 255}
	white = color.RGBA{255, 255, 255, 255}
)

func at(c *Compositor, x, y int) color.RGBA { return c.Frame().RGBAAt(x, y) }

func TestChromeHolePage(t *testing.T) {
	c := New(200, 100, 1)
	c.SetViewport(image.Rect(0, 30, 200, 100))
	c.SetChrome(solid(200, 100, blue))
	c.SetPage(solid(200, 70, green))
	_, d := c.Compose()
	if d != image.Rect(0, 0, 200, 100) {
		t.Fatalf("first compose damage %v", d)
	}
	if at(c, 10, 10) != blue || at(c, 10, 40) != green || at(c, 199, 99) != green {
		t.Fatalf("layers wrong: %v %v %v", at(c, 10, 10), at(c, 10, 40), at(c, 199, 99))
	}
	// nothing changed -> no damage, no work
	if _, d := c.Compose(); !d.Empty() {
		t.Fatalf("idle compose damaged %v", d)
	}
	if c.Dirty() {
		t.Fatal("dirty with nothing to do")
	}
}

func TestChromeDamageIsTight(t *testing.T) {
	c := New(200, 100, 1)
	c.SetViewport(image.Rect(0, 30, 200, 100))
	c.SetChrome(solid(200, 100, blue))
	c.SetPage(solid(200, 70, green))
	c.Compose()
	// change a 10x6 patch of the chrome strip
	n := solid(200, 100, blue)
	for y := 5; y < 11; y++ {
		for x := 20; x < 30; x++ {
			n.SetRGBA(x, y, red)
		}
	}
	c.SetChrome(n)
	_, d := c.Compose()
	if d != image.Rect(20, 5, 30, 11) {
		t.Fatalf("damage %v, want the 10x6 patch", d)
	}
	if at(c, 25, 8) != red || at(c, 19, 8) != blue {
		t.Fatal("patch not composed")
	}
	// a chrome change hidden entirely behind the hole costs nothing
	n2 := solid(200, 100, blue)
	copy(n2.Pix, n.Pix)
	n2.SetRGBA(50, 60, red)
	c.SetChrome(n2)
	if c.Dirty() {
		t.Fatal("change inside the viewport hole must not damage")
	}
}

func TestPageScrollWindow(t *testing.T) {
	c := New(100, 100, 1)
	c.SetViewport(image.Rect(0, 20, 100, 100)) // 80px window
	c.SetChrome(solid(100, 100, blue))
	c.SetPage(stripes(100, 400))
	c.SetDocHeight(400)
	c.Compose()
	if at(c, 5, 20).R != 0 || at(c, 5, 25).R != 5 {
		t.Fatalf("top of page wrong: %v %v", at(c, 5, 20), at(c, 5, 25))
	}
	if !c.ScrollTo(100) {
		t.Fatal("ScrollTo did not move")
	}
	_, d := c.Compose()
	if d != image.Rect(0, 20, 100, 100) {
		t.Fatalf("scroll damage %v", d)
	}
	if at(c, 5, 20).R != 100 || at(c, 5, 99).R != 179 {
		t.Fatalf("scrolled content wrong: %v %v", at(c, 5, 20), at(c, 5, 99))
	}
	// clamp to document end
	c.ScrollTo(10_000)
	pos, max := c.Scroll()
	if pos != 320 || max != 320 {
		t.Fatalf("clamp: %d/%d", pos, max)
	}
	c.Compose()
	if at(c, 5, 99).R != uint8(399%251) {
		t.Fatalf("bottom row %v", at(c, 5, 99))
	}
	if c.ScrollTo(10_000) {
		t.Fatal("scroll at the end reported movement")
	}
	// the document is taller than the painted texture: the part not yet painted shows the background
	c.SetPage(stripes(100, 100))
	c.SetDocHeight(400)
	c.ScrollTo(200)
	c.Compose()
	if at(c, 5, 60) != white {
		t.Fatalf("unpainted area must show the background, got %v", at(c, 5, 60))
	}
}

func TestPageUpdateDamageOnlyVisible(t *testing.T) {
	c := New(100, 100, 1)
	c.SetViewport(image.Rect(0, 0, 100, 50))
	c.SetChrome(solid(100, 100, blue))
	p := stripes(100, 300)
	c.SetPage(p)
	c.SetDocHeight(300)
	c.ScrollTo(100)
	c.Compose()
	// change a row far below the window: no damage
	q := stripes(100, 300)
	q.SetRGBA(10, 250, red)
	c.SetPage(q)
	if c.Dirty() {
		t.Fatal("off-screen page change damaged the frame")
	}
	// change a visible row
	r := stripes(100, 300)
	r.SetRGBA(10, 120, red)
	c.SetPage(r)
	_, d := c.Compose()
	if d != image.Rect(10, 20, 11, 21) {
		t.Fatalf("damage %v", d)
	}
	if at(c, 10, 20) != red {
		t.Fatal("visible change not drawn")
	}
}

func TestOverlayBlend(t *testing.T) {
	c := New(100, 100, 1)
	c.SetViewport(image.Rect(0, 0, 100, 100))
	c.SetChrome(solid(100, 100, blue))
	c.SetPage(solid(100, 100, white))
	c.Compose()
	// premultiplied half-transparent red 20x20 popover at (10,10)
	ov := solid(20, 20, color.RGBA{128, 0, 0, 128})
	c.SetOverlay(ov, image.Pt(10, 10), 255)
	_, d := c.Compose()
	if d != image.Rect(10, 10, 30, 30) {
		t.Fatalf("overlay damage %v", d)
	}
	got := at(c, 15, 15)
	// src + dst*(1-a) = 128 + 255*127/255 = 255 (r), 0+127 (g,b)
	if got.R < 253 || got.G < 125 || got.G > 129 || got.B < 125 || got.B > 129 {
		t.Fatalf("blend over white = %v", got)
	}
	if at(c, 5, 5) != white {
		t.Fatal("overlay leaked outside its rect")
	}
	// global alpha (fade): at 50% the source contributes half
	c.SetOverlay(ov, image.Pt(10, 10), 128)
	c.Compose()
	faded := at(c, 15, 15)
	if faded.G <= got.G {
		t.Fatalf("a more transparent overlay must be lighter: %v vs %v", faded, got)
	}
	// opaque pixel replaces
	c.SetOverlay(solid(5, 5, green), image.Pt(60, 60), 255)
	c.Compose()
	if at(c, 62, 62) != green {
		t.Fatalf("opaque overlay pixel = %v", at(c, 62, 62))
	}
	// hiding restores what was underneath
	c.SetOverlay(nil, image.Point{}, 0)
	_, d = c.Compose()
	if d.Empty() || at(c, 15, 15) != white || at(c, 62, 62) != white {
		t.Fatalf("hide: damage %v pix %v", d, at(c, 15, 15))
	}
	// an overlay cropped from a larger surface (SubImage) lands at the right place
	big := image.NewRGBA(image.Rect(0, 0, 100, 100))
	for y := 40; y < 50; y++ {
		for x := 40; x < 50; x++ {
			big.SetRGBA(x, y, color.RGBA{0, 255, 0, 255})
		}
	}
	sub := big.SubImage(image.Rect(40, 40, 50, 50)).(*image.RGBA)
	c.SetOverlay(sub, image.Pt(40, 40), 255)
	c.Compose()
	if at(c, 45, 45) != (color.RGBA{0, 255, 0, 255}) || at(c, 39, 45) != white {
		t.Fatalf("sub-image overlay misplaced: %v %v", at(c, 45, 45), at(c, 39, 45))
	}
}

func TestResizeAndPartialChrome(t *testing.T) {
	c := New(100, 100, 1)
	c.SetViewport(image.Rect(0, 40, 100, 100))
	c.SetChrome(solid(100, 100, blue))
	c.Compose()
	c.Resize(200, 120, 2)
	w, h, s := c.Size()
	if w != 200 || h != 120 || s != 2 {
		t.Fatal("size")
	}
	if !c.Dirty() {
		t.Fatal("resize must damage everything")
	}
	// a partial chrome paint (only the strip above the hole) updates just that strip
	c.SetViewport(image.Rect(0, 80, 200, 120))
	c.SetChrome(solid(200, 120, blue))
	c.Compose()
	c.SetChrome(solid(200, 80, red))
	_, d := c.Compose()
	if d != image.Rect(0, 0, 200, 80) {
		t.Fatalf("partial paint damage %v", d)
	}
	if at(c, 10, 10) != red {
		t.Fatal("strip not updated")
	}
	// the hole region keeps the previous chrome pixels (not zeroed)
	if got := c.chrome.RGBAAt(10, 100); got != blue {
		t.Fatalf("partial paint clobbered the rest of the chrome buffer: %v", got)
	}
}

func TestScrollbarOverlay(t *testing.T) {
	c := New(100, 100, 1)
	c.SetViewport(image.Rect(0, 0, 100, 100))
	c.SetChrome(solid(100, 100, blue))
	c.SetPage(solid(100, 100, white))
	c.SetDocHeight(400)
	c.Compose()
	if at(c, 95, 5) != white {
		t.Fatal("scrollbar visible while hidden")
	}
	c.SetScrollbar(200)
	_, d := c.Compose()
	if d.Empty() || d.Dx() > 12 {
		t.Fatalf("scrollbar damage should be a thin strip, got %v", d)
	}
	if at(c, 95, 5) == white {
		t.Fatal("scrollbar thumb not drawn")
	}
	// the thumb covers a quarter of the track for a 4x document
	th := c.sbThumb()
	if th.Dy() != 25 {
		t.Fatalf("thumb height %d", th.Dy())
	}
	c.SetScrollbar(0)
	c.Compose()
	if at(c, 95, 5) != white {
		t.Fatal("scrollbar not removed")
	}
}

func TestScroller(t *testing.T) {
	s := NewScroller()
	s.SetMax(1000)
	if s.Animating() {
		t.Fatal("idle scroller animating")
	}
	s.Wheel(300)
	if !s.Animating() {
		t.Fatal("not animating after wheel")
	}
	prev := 0
	steps := 0
	for s.Animating() && steps < 1000 {
		p := s.Tick(1.0 / 60)
		if p < prev {
			t.Fatal("scroll moved backwards")
		}
		prev = p
		steps++
	}
	if prev != 300 || s.Animating() {
		t.Fatalf("ended at %d after %d ticks", prev, steps)
	}
	if steps < 8 || steps > 60 {
		t.Fatalf("ease took %d frames (~%dms)", steps, steps*16)
	}
	s.Wheel(5000)
	if s.Target != 1000 {
		t.Fatalf("target not clamped: %v", s.Target)
	}
	s.Wheel(-5000)
	if s.Target != 0 {
		t.Fatal("clamp low")
	}
	s.Smooth = false
	s.Wheel(50)
	if s.Pos != 50 || s.Animating() {
		t.Fatal("non-smooth must jump")
	}
	s.SetMax(20)
	if s.Pos != 20 || s.Target != 20 {
		t.Fatal("SetMax must clamp position")
	}
}

// Steady-state composition must not allocate.
func TestComposeAllocatesNothing(t *testing.T) {
	c := New(1280, 800, 1)
	c.SetViewport(image.Rect(0, 80, 1280, 800))
	chromeA, chromeB := solid(1280, 800, blue), solid(1280, 800, blue)
	chromeB.SetRGBA(100, 40, red)
	page := stripes(1280, 4000)
	c.SetChrome(solid(1280, 800, blue))
	c.SetPage(page)
	c.SetDocHeight(4000)
	ov := solid(300, 200, color.RGBA{30, 30, 30, 200})
	c.SetOverlay(ov, image.Pt(100, 90), 255)
	c.Compose()
	y := 0
	flip := false
	allocs := testing.AllocsPerRun(50, func() {
		y = (y + 37) % 3000
		c.ScrollTo(y)
		c.SetScrollbar(uint8(y % 200))
		// a chrome buffer swap with a tiny diff, as a caret blink would produce
		if flip = !flip; flip {
			c.chrome, chromeA = chromeA, c.chrome
		}
		c.Invalidate(image.Rect(90, 30, 110, 50))
		c.Compose()
	})
	if allocs != 0 {
		t.Fatalf("Compose path allocated %.1f times per frame", allocs)
	}
	_ = chromeB
}

func TestSetChromeDiffAllocatesNothing(t *testing.T) {
	c := New(1280, 800, 1)
	a, b := solid(1280, 800, blue), solid(1280, 800, blue)
	b.SetRGBA(640, 40, red)
	c.SetChrome(solid(1280, 800, blue))
	c.Compose()
	cur := a
	allocs := testing.AllocsPerRun(50, func() {
		next := b
		if cur == b {
			next = a
		}
		cur = next
		spare := c.SetChrome(next)
		_ = spare
		c.Compose()
	})
	if allocs != 0 {
		t.Fatalf("SetChrome+Compose allocated %.1f times", allocs)
	}
}

func BenchmarkComposeScroll(b *testing.B) {
	c := New(1280, 800, 1)
	c.SetViewport(image.Rect(0, 80, 1280, 800))
	c.SetChrome(solid(1280, 800, blue))
	c.SetPage(stripes(1280, 4000))
	c.SetDocHeight(4000)
	c.Compose()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.ScrollTo((i * 13) % 3000)
		c.Compose()
	}
}

func BenchmarkComposeChromeStrip(b *testing.B) {
	c := New(1280, 800, 1)
	c.SetViewport(image.Rect(0, 80, 1280, 800))
	a, d := solid(1280, 800, blue), solid(1280, 800, blue)
	d.SetRGBA(300, 40, red)
	c.SetChrome(solid(1280, 800, blue))
	c.SetPage(stripes(1280, 800))
	c.Compose()
	b.ReportAllocs()
	b.ResetTimer()
	cur := a
	for i := 0; i < b.N; i++ {
		if cur == a {
			cur = d
		} else {
			cur = a
		}
		c.SetChrome(cur)
		c.Compose()
	}
}

func BenchmarkDiffUnchanged4MB(b *testing.B) {
	x, y := solid(1280, 800, blue), solid(1280, 800, blue)
	b.SetBytes(int64(len(x.Pix)))
	for i := 0; i < b.N; i++ {
		diff(x, y, x.Bounds())
	}
}
