package gfx

import (
	"testing"

	"github.com/sweefbrowser/sweef/internal/render/font"
)

func TestReplaySmoke(t *testing.T) {
	var d DisplayList
	d.Background = White
	d.FillRect(Rect{10, 10, 50, 30}, Radii{{5, 5}, {5, 5}, {5, 5}, {5, 5}}, SolidPaint(Color{1, 0, 0, 1}), 0)
	f := font.Get(font.Style{Families: []string{"sans-serif"}, Size: 16})
	d.Text(&TextItem{Run: GlyphRun{Glyphs: f.Shape("Hi", 0, 0), Origin: Point{10, 70}}, Color: Black}, 0)
	s := d.Replay(100, 100, 0, 0, 1)
	if c := s.Img.RGBAAt(30, 25); c.R != 255 || c.G != 0 {
		t.Fatalf("rect pixel %v", c)
	}
	dark := 0
	for y := 55; y < 72; y++ {
		for x := 10; x < 30; x++ {
			if s.Img.RGBAAt(x, y).R < 128 {
				dark++
			}
		}
	}
	if dark < 10 {
		t.Fatalf("text not drawn (%d dark px)", dark)
	}
}
