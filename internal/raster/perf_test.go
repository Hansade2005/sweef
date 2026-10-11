package raster

import (
	"strings"
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/font"
	"github.com/sweefbrowser/sweef/internal/render"
)

func typicalPage() []render.Op {
	face := font.Lookup("Liberation Sans", font.Style{Weight: 400})
	var ops []render.Op
	ops = append(ops, render.FillRect{Rect: render.Rect{W: 1280, H: 800}, Color: rc(255, 255, 255)})
	ops = append(ops, render.FillRect{Rect: render.Rect{W: 1280, H: 60}, Color: rc(33, 37, 41)})
	ops = append(ops, render.BoxShadow{Rect: render.Rect{X: 40, Y: 90, W: 600, H: 300}, Blur: 16, DY: 4, Color: render.Color{A: 60}})
	ops = append(ops, render.FillRect{Rect: render.Rect{X: 40, Y: 90, W: 600, H: 300}, Color: rc(255, 255, 255), Radii: rad(8)})
	ops = append(ops, render.Border{Rect: render.Rect{X: 40, Y: 90, W: 600, H: 300}, Widths: [4]float64{1, 1, 1, 1}, Colors: [4]render.Color{rc(200, 200, 200), rc(200, 200, 200), rc(200, 200, 200), rc(200, 200, 200)}, Styles: [4]string{"solid", "solid", "solid", "solid"}, Radii: rad(8)})
	line := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 3)
	for i := 0; i < 36; i++ {
		x := 60.0
		if i > 17 {
			x = 680
		}
		ops = append(ops, textRun(face, line, x, 120+float64(i%18)*20, 14, rc(30, 30, 30)))
	}
	return ops
}

func TestPaintSpeed(t *testing.T) {
	ops := typicalPage()
	Paint(ops, 1280, 800, 1, PaintEnv{}) // warm caches
	best := time.Hour
	for i := 0; i < 5; i++ {
		t0 := time.Now()
		Paint(ops, 1280, 800, 1, PaintEnv{Background: Color{1, 1, 1, 1}})
		if d := time.Since(t0); d < best {
			best = d
		}
	}
	t.Logf("1280x800 typical text page (36 lines x 135 chars, shadow, rrect, border): best %v", best)
	if best > 60*time.Millisecond {
		t.Errorf("paint too slow: %v", best)
	}
}
