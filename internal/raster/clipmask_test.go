package raster

import (
	"testing"

	"github.com/sweefbrowser/sweef/internal/render"
)

func solid(r render.Rect, c render.Color) render.FillRect { return render.FillRect{Rect: r, Color: c} }

func TestClipPathAndMaskPixels(t *testing.T) {
	red := render.Color{R: 255, A: 255}
	black := render.Color{A: 255}
	box := render.Rect{X: 10, Y: 10, W: 40, H: 40}
	for _, dpr := range []float64{1, 2} {
		// clip-path: circle
		list := []render.Op{
			render.PushClipPath{Subs: []render.ClipSub{{Kind: "ellipse", CX: 30, CY: 30, RX: 15, RY: 15}}},
			solid(box, red),
			render.PopClipPath{},
		}
		img := Paint(list, 60, 60, dpr, PaintEnv{})
		at := func(x, y float64) uint8 { return img.Pix[int(y*dpr)*img.Stride+int(x*dpr)*4+3] }
		if at(30, 30) != 255 || at(12, 12) != 0 || at(30, 52) != 0 {
			t.Errorf("dpr %v clip: centre=%d corner=%d below=%d", dpr, at(30, 30), at(12, 12), at(30, 52))
		}
		// union of two shapes, even-odd polygon hole
		list = []render.Op{
			render.PushClipPath{Subs: []render.ClipSub{
				{Kind: "rrect", Rect: render.Rect{X: 10, Y: 10, W: 10, H: 10}},
				{Kind: "rrect", Rect: render.Rect{X: 40, Y: 40, W: 10, H: 10}},
			}},
			solid(box, red),
			render.PopClipPath{},
		}
		img = Paint(list, 60, 60, dpr, PaintEnv{})
		if at(15, 15) != 255 || at(45, 45) != 255 || at(30, 30) != 0 {
			t.Errorf("dpr %v union: %d %d %d", dpr, at(15, 15), at(45, 45), at(30, 30))
		}
		// mask: left half opaque, luminance of white-to-black ramp
		ramp := render.Gradient{Rect: box, Kind: "linear", Angle: 90,
			Stops: []render.GradientStop{{Pos: 0, Color: render.Color{R: 255, G: 255, B: 255, A: 255}}, {Pos: 1, Color: black}},
			Tile:  &render.Tile{Origin: [2]float64{10, 10}, Size: [2]float64{40, 40}, Step: [2]float64{40, 40}}}
		list = []render.Op{
			render.PushMask{Bounds: box, Layers: []render.MaskLayerOps{{Ops: []render.Op{ramp}, Mode: "luminance", Composite: "add"}}},
			solid(box, red),
			render.PopMask{},
		}
		img = Paint(list, 60, 60, dpr, PaintEnv{})
		if l, r := at(12, 30), at(47, 30); l < 220 || r > 40 || at(5, 30) != 0 {
			t.Errorf("dpr %v mask: left=%d right=%d outside=%d", dpr, l, r, at(5, 30))
		}
		// composite subtract: solid minus the left half
		half := render.Gradient{Rect: box, Kind: "linear", Angle: 90,
			Stops: []render.GradientStop{{Pos: 0, Color: black}, {Pos: 0.5, Color: black}, {Pos: 0.5}, {Pos: 1}},
			Tile:  &render.Tile{Origin: [2]float64{10, 10}, Size: [2]float64{40, 40}, Step: [2]float64{40, 40}}}
		full := render.Gradient{Rect: box, Kind: "linear", Angle: 90,
			Stops: []render.GradientStop{{Pos: 0, Color: black}, {Pos: 1, Color: black}},
			Tile:  &render.Tile{Origin: [2]float64{10, 10}, Size: [2]float64{40, 40}, Step: [2]float64{40, 40}}}
		list = []render.Op{
			render.PushMask{Bounds: box, Layers: []render.MaskLayerOps{
				{Ops: []render.Op{full}, Mode: "alpha", Composite: "subtract"},
				{Ops: []render.Op{half}, Mode: "alpha", Composite: "add"},
			}},
			solid(box, red),
			render.PopMask{},
		}
		img = Paint(list, 60, 60, dpr, PaintEnv{})
		if at(15, 30) != 0 || at(45, 30) != 255 {
			t.Errorf("dpr %v subtract: left=%d right=%d", dpr, at(15, 30), at(45, 30))
		}
	}
}
