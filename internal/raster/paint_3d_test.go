package raster

import (
	"fmt"
	"math"

	"github.com/sweefbrowser/sweef/internal/render"
)

type m4 [4][4]float64

func id4() m4 { return m4{{1, 0, 0, 0}, {0, 1, 0, 0}, {0, 0, 1, 0}, {0, 0, 0, 1}} }

func (a m4) mul(b m4) (o m4) {
	for i := 0; i < 4; i++ {
		for j := 0; j < 4; j++ {
			for k := 0; k < 4; k++ {
				o[i][j] += a[i][k] * b[k][j]
			}
		}
	}
	return
}

func tr4(x, y, z float64) m4 { m := id4(); m[0][3], m[1][3], m[2][3] = x, y, z; return m }
func persp4(d float64) m4    { m := id4(); m[3][2] = -1 / d; return m }
func rotY4(deg float64) m4 {
	a := deg * math.Pi / 180
	m := id4()
	m[0][0], m[0][2], m[2][0], m[2][2] = math.Cos(a), math.Sin(a), -math.Sin(a), math.Cos(a)
	return m
}
func rotX4(deg float64) m4 {
	a := deg * math.Pi / 180
	m := id4()
	m[1][1], m[1][2], m[2][1], m[2][2] = math.Cos(a), -math.Sin(a), math.Sin(a), math.Cos(a)
	return m
}
func sc4(x, y, z float64) m4 { m := id4(); m[0][0], m[1][1], m[2][2] = x, y, z; return m }
func flat(m m4) (o [16]float64) {
	for i := 0; i < 4; i++ {
		for j := 0; j < 4; j++ {
			o[i*4+j] = m[i][j]
		}
	}
	return
}

// paint3DCases covers projective layers (render.PushTransform3D): the layer is
// rasterized in its own plane and drawn through the homography with bilinear
// sampling and an anti-aliased outline.
func paint3DCases() []paintCase {
	// a 100x70 card at (x, y): fill, border, a child rect; the css is what
	// Chromium renders as the reference
	card := func(x, y float64, m m4, lat bool, clip [][2]float64) []render.Op {
		b := render.Rect{X: x, Y: y, W: 100, H: 70}
		return []render.Op{
			render.PushTransform3D{M: flat(m), Bounds: b, Lattice: lat, LatX: x, LatY: y, Clip: clip},
			render.FillRect{Rect: b, Color: rc(255, 204, 187)},
			render.Border{Rect: b, Widths: [4]float64{3, 3, 3, 3}, Styles: [4]string{"solid", "solid", "solid", "solid"},
				Colors: [4]render.Color{rc(170, 51, 51), rc(170, 51, 51), rc(170, 51, 51), rc(170, 51, 51)}},
			render.FillRect{Rect: render.Rect{X: x + 12, Y: y + 14, W: 40, H: 22}, Color: rc(102, 170, 102)},
			render.PopTransform3D{},
		}
	}
	about := func(cx, cy float64, parts ...m4) m4 {
		m := tr4(cx, cy, 0)
		for _, p := range parts {
			m = m.mul(p)
		}
		return m.mul(tr4(-cx, -cy, 0))
	}
	div := func(x, y float64, css string) string {
		return fmt.Sprintf(`<div style="position:absolute;left:%gpx;top:%gpx;width:100px;height:70px;box-sizing:border-box;background:#fcb;border:3px solid #a33;%s"><i style="position:absolute;left:9px;top:11px;width:40px;height:22px;background:#6a6"></i></div>`, x, y, css)
	}
	return []paintCase{
		{name: "p3d_roty_persp", html: div(100, 60, "transform:perspective(250px) rotateY(40deg)"),
			ops: func() []render.Op { return card(100, 60, about(150, 95, persp4(250), rotY4(40)), true, nil) }},
		{name: "p3d_rotx", html: div(100, 60, "transform:rotateX(50deg)"),
			ops: func() []render.Op { return card(100, 60, about(150, 95, rotX4(50)), true, nil) }},
		{name: "p3d_tz_persp", html: `<div style="position:absolute;left:0;top:0;width:300px;height:200px;perspective:200px;perspective-origin:50% 50%">` + div(100, 60, "transform:translateZ(60px)") + `</div>`,
			ops: func() []render.Op {
				return card(100, 60, tr4(150, 100, 0).mul(persp4(200)).mul(tr4(-150, -100, 0)).mul(tr4(0, 0, 60)), true, nil)
			}},
		{name: "p3d_scale3d", html: div(100, 60, "transform:scale3d(1.5,1.2,2) rotateY(0.001deg)"),
			ops: func() []render.Op { return card(100, 60, about(150, 95, sc4(1.5, 1.2, 2)), true, nil) }},
		{name: "p3d_split", html: div(60, 60, "transform:perspective(300px) rotateY(30deg)") + div(160, 60, "transform:perspective(300px) rotateY(-30deg)"),
			ops: func() []render.Op {
				a := card(60, 60, about(110, 95, persp4(300), rotY4(30)), true, nil)
				// a layer drawn as two halves (the two pieces of a layer a plane cuts)
				cut := func(x0, x1 float64) [][2]float64 {
					return [][2]float64{{x0, 40}, {x1, 40}, {x1, 150}, {x0, 150}}
				}
				m := about(210, 95, persp4(300), rotY4(-30))
				b := card(160, 60, m, true, cut(100, 210))
				c := card(160, 60, m, true, cut(210, 300))
				return append(append(a, b...), c...)
			}},
	}
}
