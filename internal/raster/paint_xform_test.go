package raster

import (
	"fmt"
	"math"

	"github.com/sweefbrowser/sweef/internal/render"
)

// paintXformCases covers the paint-offset snapping of transformed elements
// (render.PushTransform.Lattice): the box origin rounds to a whole pixel and
// the content snaps on the box's own pixel grid.
func paintXformCases() []paintCase {
	type box struct {
		x, y   float64
		css    string
		m      [6]float64 // transform about the (rounded) origin; identity = translation-only
		drop   bool
		rx, ry float64
	}
	// frame returns the ops the painter emits for a 30x16 box with a 20.3x9.7
	// child at (5.4, 5.6), origin 0 0.
	frame := func(b box, matrix func(ox, oy float64) [6]float64) []render.Op {
		var ops []render.Op
		lx, ly := 0.0, 0.0
		M := matrix(b.x, b.y)
		if b.drop {
			lx, ly = b.x, b.y
			rx, ry := math.Round(b.x), math.Round(b.y)
			// T(A') * m * T(-A) with m = linear part of M about the origin
			m := matrix(0, 0)
			M = [6]float64{m[0], m[1], m[2], m[3], rx - (m[0]*b.x + m[2]*b.y) + m[4], ry - (m[1]*b.x + m[3]*b.y) + m[5]}
		}
		ops = append(ops, render.PushTransform{M: M, Lattice: true, LatX: lx, LatY: ly},
			render.FillRect{Rect: render.Rect{X: b.x, Y: b.y, W: 30, H: 16}, Color: rc(255, 204, 187)},
			render.Border{Rect: render.Rect{X: b.x, Y: b.y, W: 30, H: 16}, Widths: [4]float64{2, 2, 2, 2},
				Styles: [4]string{"solid", "solid", "solid", "solid"},
				Colors: [4]render.Color{rc(51, 102, 204), rc(51, 102, 204), rc(51, 102, 204), rc(51, 102, 204)}},
			render.FillRect{Rect: render.Rect{X: b.x + 5.4, Y: b.y + 5.6, W: 20.3, H: 9.7}, Color: rc(102, 170, 102)},
			render.PopTransform{})
		return ops
	}
	boxes := []box{
		{x: 10.3, y: 10.3, css: "transform:scale(2);transform-origin:0 0", drop: true},
		{x: 100.5, y: 10.5, css: "transform:scale(1.5);transform-origin:0 0", drop: true},
		{x: 160.7, y: 20.7, css: "transform:translate(0.4px,0.4px)"},
		{x: 210.3, y: 20.3, css: "will-change:transform", drop: true},
		{x: 250.3, y: 20.3, css: "transform:skewX(20deg);transform-origin:0 0", drop: true},
	}
	mats := []func(ox, oy float64) [6]float64{
		func(ox, oy float64) [6]float64 { return [6]float64{2, 0, 0, 2, 0, 0} },
		func(ox, oy float64) [6]float64 { return [6]float64{1.5, 0, 0, 1.5, 0, 0} },
		func(ox, oy float64) [6]float64 { return [6]float64{1, 0, 0, 1, 0.4, 0.4} },
		func(ox, oy float64) [6]float64 { return [6]float64{1, 0, 0, 1, 0, 0} },
		func(ox, oy float64) [6]float64 { return [6]float64{1, 0, math.Tan(20 * math.Pi / 180), 1, 0, 0} },
	}
	var html string
	for _, b := range boxes {
		html += fmt.Sprintf(`<div style="position:absolute;left:%gpx;top:%gpx;width:30px;height:16px;box-sizing:border-box;background:#fcb;border:2px solid #36c;%s"><i style="position:absolute;left:3.4px;top:3.6px;width:20.3px;height:9.7px;background:#6a6"></i></div>`, b.x, b.y, b.css)
	}
	return []paintCase{{name: "xform_lattice", html: html, ops: func() []render.Op {
		var ops []render.Op
		for i, b := range boxes {
			ops = append(ops, frame(b, mats[i])...)
		}
		return ops
	}}}
}
