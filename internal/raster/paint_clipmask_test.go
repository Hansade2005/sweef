package raster

import (
	"fmt"
	"math"
	"strings"

	"github.com/sweefbrowser/sweef/internal/render"
)

// paintClipMaskCases covers clip-path shapes and mask layers against Chromium.
func paintClipMaskCases() []paintCase {
	grad := func(r render.Rect) render.Gradient {
		return render.Gradient{Rect: r, Kind: "linear", Angle: 90,
			Stops: []render.GradientStop{{Pos: 0, Color: rc(238, 51, 51)}, {Pos: 1, Color: rc(51, 51, 238)}},
			Tile:  &render.Tile{Origin: [2]float64{r.X, r.Y}, Size: [2]float64{r.W, r.H}, Step: [2]float64{r.W, r.H}}}
	}
	cell := func(i int) render.Rect {
		return render.Rect{X: float64(10 + (i%4)*72), Y: float64(10 + (i/4)*62), W: 66, H: 52}
	}
	bg := "background:linear-gradient(90deg,#e33,#33e);"
	div := func(i int, extra string) string {
		r := cell(i)
		return fmt.Sprintf(`<div style="position:absolute;left:%gpx;top:%gpx;width:%gpx;height:%gpx;%s%s"></div>`, r.X, r.Y, r.W, r.H, bg, extra)
	}
	var cases []paintCase

	// clip-path shapes
	var html strings.Builder
	var clips []func() []render.Op
	add := func(i int, css string, sub func(r render.Rect) render.ClipSub) {
		html.WriteString(div(i, "clip-path:"+css))
		clips = append(clips, func() []render.Op {
			r := cell(i)
			return []render.Op{render.PushClipPath{Subs: []render.ClipSub{sub(r)}}, grad(r), render.PopClipPath{}}
		})
	}
	add(0, "inset(6px 10px 8px 12px round 10px)", func(r render.Rect) render.ClipSub {
		return render.ClipSub{Kind: "rrect", Rect: render.Rect{X: r.X + 12, Y: r.Y + 6, W: r.W - 22, H: r.H - 14}, Radii: rad(10)}
	})
	add(1, "circle(22px at 40% 55%)", func(r render.Rect) render.ClipSub {
		return render.ClipSub{Kind: "ellipse", CX: r.X + 0.4*r.W, CY: r.Y + 0.55*r.H, RX: 22, RY: 22}
	})
	add(2, "ellipse(45% 30% at 50% 50%)", func(r render.Rect) render.ClipSub {
		return render.ClipSub{Kind: "ellipse", CX: r.X + 0.5*r.W, CY: r.Y + 0.5*r.H, RX: 0.45 * r.W, RY: 0.3 * r.H}
	})
	add(3, "polygon(50% 0, 100% 100%, 0 100%)", func(r render.Rect) render.ClipSub {
		return render.ClipSub{Kind: "polygon", Pts: [][2]float64{{r.X + r.W/2, r.Y}, {r.X + r.W, r.Y + r.H}, {r.X, r.Y + r.H}}}
	})
	add(4, "polygon(evenodd, 50% 0%, 79% 91%, 2% 35%, 98% 35%, 21% 91%)", func(r render.Rect) render.ClipSub {
		pts := [][2]float64{{50, 0}, {79, 91}, {2, 35}, {98, 35}, {21, 91}}
		cs := render.ClipSub{Kind: "polygon", EvenOdd: true}
		for _, q := range pts {
			cs.Pts = append(cs.Pts, [2]float64{r.X + q[0]/100*r.W, r.Y + q[1]/100*r.H})
		}
		return cs
	})
	add(5, "path('M 6 6 L 58 4 Q 66 30 44 46 C 30 54 12 44 6 6 Z')", func(r render.Rect) render.ClipSub {
		return render.ClipSub{Kind: "path", D: "M 6 6 L 58 4 Q 66 30 44 46 C 30 54 12 44 6 6 Z", M: [6]float64{1, 0, 0, 1, r.X, r.Y}, HasM: true}
	})
	add(6, "inset(0 round 50%)", func(r render.Rect) render.ClipSub {
		return render.ClipSub{Kind: "rrect", Rect: r, Radii: render.Radii{{r.W / 2, r.H / 2}, {r.W / 2, r.H / 2}, {r.W / 2, r.H / 2}, {r.W / 2, r.H / 2}}}
	})
	add(7, "inset(8px)", func(r render.Rect) render.ClipSub {
		return render.ClipSub{Kind: "rrect", Rect: render.Rect{X: r.X + 8, Y: r.Y + 8, W: r.W - 16, H: r.H - 16}}
	})
	cases = append(cases, paintCase{"clip_path", html.String(), func() []render.Op {
		var ops []render.Op
		for _, f := range clips {
			ops = append(ops, f()...)
		}
		return ops
	}})

	// masks
	var mh strings.Builder
	var mops []func() []render.Op
	stops := func(a, b render.Color) []render.GradientStop {
		return []render.GradientStop{{Pos: 0, Color: a}, {Pos: 1, Color: b}}
	}
	black, clear := rc(0, 0, 0), render.Color{}
	madd := func(i int, css string, layers func(r render.Rect) []render.MaskLayerOps) {
		mh.WriteString(div(i, css))
		mops = append(mops, func() []render.Op {
			r := cell(i)
			return []render.Op{render.PushMask{Bounds: r, Layers: layers(r)}, grad(r), render.PopMask{}}
		})
	}
	lin := func(r render.Rect, ang float64, st []render.GradientStop) render.Gradient {
		return render.Gradient{Rect: r, Kind: "linear", Angle: ang, Stops: st,
			Tile: &render.Tile{Origin: [2]float64{r.X, r.Y}, Size: [2]float64{r.W, r.H}, Step: [2]float64{r.W, r.H}}}
	}
	madd(0, "mask-image:linear-gradient(to right,#000,transparent)", func(r render.Rect) []render.MaskLayerOps {
		return []render.MaskLayerOps{{Ops: []render.Op{lin(r, 90, stops(black, clear))}, Mode: "alpha", Composite: "add"}}
	})
	madd(1, "mask-image:linear-gradient(45deg,#000 20%,transparent 80%)", func(r render.Rect) []render.MaskLayerOps {
		return []render.MaskLayerOps{{Ops: []render.Op{lin(r, 45, []render.GradientStop{{Pos: 0.2, Color: black}, {Pos: 0.8, Color: clear}})}, Mode: "alpha", Composite: "add"}}
	})
	madd(2, "mask-image:radial-gradient(circle at 50% 50%,#000 30%,transparent 70%)", func(r render.Rect) []render.MaskLayerOps {
		g := render.Gradient{Rect: r, Kind: "radial", CX: r.W / 2, CY: r.H / 2, RX: math2hyp(r), RY: math2hyp(r),
			Stops: []render.GradientStop{{Pos: 0.3, Color: black}, {Pos: 0.7, Color: clear}},
			Tile:  &render.Tile{Origin: [2]float64{r.X, r.Y}, Size: [2]float64{r.W, r.H}, Step: [2]float64{r.W, r.H}}}
		return []render.MaskLayerOps{{Ops: []render.Op{g}, Mode: "alpha", Composite: "add"}}
	})
	madd(3, "mask-image:linear-gradient(to right,#fff,#000);mask-mode:luminance", func(r render.Rect) []render.MaskLayerOps {
		return []render.MaskLayerOps{{Ops: []render.Op{lin(r, 90, stops(rc(255, 255, 255), black))}, Mode: "luminance", Composite: "add"}}
	})
	madd(4, "mask-image:repeating-linear-gradient(90deg,#000 0 8px,transparent 8px 16px)", func(r render.Rect) []render.MaskLayerOps {
		g := lin(r, 90, []render.GradientStop{{Pos: 0, Color: black}, {Pos: 0.5, Color: black}, {Pos: 0.5, Color: clear}, {Pos: 1, Color: clear}})
		g.Tile = &render.Tile{Origin: [2]float64{r.X, r.Y}, Size: [2]float64{16, r.H}, Step: [2]float64{16, r.H}, RepeatX: true}
		return []render.MaskLayerOps{{Ops: []render.Op{g}, Mode: "alpha", Composite: "add"}}
	})
	madd(5, "mask-image:linear-gradient(#000,#000),linear-gradient(90deg,#000 50%,transparent 50%);mask-composite:subtract", func(r render.Rect) []render.MaskLayerOps {
		half := lin(r, 90, []render.GradientStop{{Pos: 0, Color: black}, {Pos: 0.5, Color: black}, {Pos: 0.5, Color: clear}, {Pos: 1, Color: clear}})
		solid := lin(r, 90, stops(black, black))
		return []render.MaskLayerOps{{Ops: []render.Op{solid}, Mode: "alpha", Composite: "subtract"}, {Ops: []render.Op{half}, Mode: "alpha", Composite: "add"}}
	})
	madd(6, "mask-image:linear-gradient(#000,#000);mask-size:30px 30px;mask-repeat:no-repeat;mask-position:80% 20%", func(r render.Rect) []render.MaskLayerOps {
		x, y := r.X+0.8*(r.W-30), r.Y+0.2*(r.H-30)
		g := lin(r, 0, stops(black, black))
		g.Tile = &render.Tile{Origin: [2]float64{x, y}, Size: [2]float64{30, 30}, Step: [2]float64{30, 30}}
		return []render.MaskLayerOps{{Ops: []render.Op{g}, Mode: "alpha", Composite: "add"}}
	})
	madd(7, "mask-image:linear-gradient(#000,#000);mask-clip:content-box;padding:8px;box-sizing:border-box", func(r render.Rect) []render.MaskLayerOps {
		g := lin(render.Rect{X: r.X + 8, Y: r.Y + 8, W: r.W - 16, H: r.H - 16}, 0, stops(black, black))
		return []render.MaskLayerOps{{Ops: []render.Op{g}, Mode: "alpha", Composite: "add"}}
	})
	cases = append(cases, paintCase{"mask", mh.String(), func() []render.Op {
		var ops []render.Op
		for _, f := range mops {
			ops = append(ops, f()...)
		}
		return ops
	}})
	return cases
}

func math2hyp(r render.Rect) float64 {
	// farthest-corner circle radius from the centre
	return math.Sqrt(r.W*r.W/4 + r.H*r.H/4)
}
