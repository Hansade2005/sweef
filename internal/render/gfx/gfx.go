// Package gfx is the 2D graphics core shared by page painting and canvas 2D:
// colours, affine matrices, paths, paints (solid, gradients, patterns),
// stroking, anti-aliased path filling with clip masks and compositing, image
// drawing, glyph runs and blur. Page painting records a display list
// (displaylist.go) and replays it onto a Surface; canvas 2D draws onto a
// Surface directly.
//
// Surfaces hold premultiplied RGBA (image.RGBA), the format Go's image/draw
// and PNG encoding expect.
package gfx

import (
	"image"
	"math"
)

// Color is a straight-alpha colour with components in 0..1.
type Color struct{ R, G, B, A float64 }

var (
	Transparent = Color{}
	Black       = Color{0, 0, 0, 1}
	White       = Color{1, 1, 1, 1}
)

// RGBA8 builds a colour from 8-bit components and a 0..1 alpha.
func RGBA8(r, g, b uint8, a float64) Color {
	return Color{float64(r) / 255, float64(g) / 255, float64(b) / 255, a}
}

// IsTransparent reports whether the colour paints nothing.
func (c Color) IsTransparent() bool { return c.A <= 0 }

// WithAlpha multiplies the colour's alpha by a.
func (c Color) WithAlpha(a float64) Color { c.A *= a; return c }

// Premul returns premultiplied 16-bit components (image/color convention).
func (c Color) Premul() (r, g, b, a uint32) {
	al := clamp01(c.A)
	return uint32(clamp01(c.R)*al*65535 + 0.5), uint32(clamp01(c.G)*al*65535 + 0.5),
		uint32(clamp01(c.B)*al*65535 + 0.5), uint32(al*65535 + 0.5)
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// Point is a 2D point in float px.
type Point struct{ X, Y float64 }

// Rect is an axis-aligned rectangle in float px.
type Rect struct{ X, Y, W, H float64 }

func (r Rect) Empty() bool                   { return r.W <= 0 || r.H <= 0 }
func (r Rect) Right() float64                { return r.X + r.W }
func (r Rect) Bottom() float64               { return r.Y + r.H }
func (r Rect) Translate(dx, dy float64) Rect { return Rect{r.X + dx, r.Y + dy, r.W, r.H} }

// Intersect returns the overlap of r and o (empty when disjoint).
func (r Rect) Intersect(o Rect) Rect {
	x0, y0 := math.Max(r.X, o.X), math.Max(r.Y, o.Y)
	x1, y1 := math.Min(r.Right(), o.Right()), math.Min(r.Bottom(), o.Bottom())
	if x1 <= x0 || y1 <= y0 {
		return Rect{}
	}
	return Rect{x0, y0, x1 - x0, y1 - y0}
}

// Union returns the bounding box of r and o.
func (r Rect) Union(o Rect) Rect {
	if r.Empty() {
		return o
	}
	if o.Empty() {
		return r
	}
	x0, y0 := math.Min(r.X, o.X), math.Min(r.Y, o.Y)
	x1, y1 := math.Max(r.Right(), o.Right()), math.Max(r.Bottom(), o.Bottom())
	return Rect{x0, y0, x1 - x0, y1 - y0}
}

// Inset shrinks r by the given edge amounts (top, right, bottom, left).
func (r Rect) Inset(t, rt, b, l float64) Rect {
	return Rect{r.X + l, r.Y + t, math.Max(0, r.W-l-rt), math.Max(0, r.H-t-b)}
}

// Radii are per-corner elliptical radii: top-left, top-right, bottom-right,
// bottom-left, each {x, y}. The zero value is a square corner.
type Radii [4]Point

// IsZero reports square corners.
func (r Radii) IsZero() bool { return r == Radii{} }

// Matrix is a 2D affine transform [a b c d e f] mapping (x, y) to
// (a*x + c*y + e, b*x + d*y + f), the canvas/SVG convention.
type Matrix [6]float64

// Identity is the identity transform.
var Identity = Matrix{1, 0, 0, 1, 0, 0}

func Translate(x, y float64) Matrix { return Matrix{1, 0, 0, 1, x, y} }
func Scale(x, y float64) Matrix     { return Matrix{x, 0, 0, y, 0, 0} }
func Rotate(rad float64) Matrix {
	s, c := math.Sincos(rad)
	return Matrix{c, s, -s, c, 0, 0}
}

// Mul returns m * n: n is applied first, then m.
func (m Matrix) Mul(n Matrix) Matrix {
	return Matrix{
		m[0]*n[0] + m[2]*n[1], m[1]*n[0] + m[3]*n[1],
		m[0]*n[2] + m[2]*n[3], m[1]*n[2] + m[3]*n[3],
		m[0]*n[4] + m[2]*n[5] + m[4], m[1]*n[4] + m[3]*n[5] + m[5],
	}
}

// Apply maps a point.
func (m Matrix) Apply(p Point) Point {
	return Point{m[0]*p.X + m[2]*p.Y + m[4], m[1]*p.X + m[3]*p.Y + m[5]}
}

// Invert returns the inverse and whether m is invertible.
func (m Matrix) Invert() (Matrix, bool) {
	det := m[0]*m[3] - m[1]*m[2]
	if det == 0 || math.IsNaN(det) || math.IsInf(det, 0) {
		return Matrix{}, false
	}
	id := 1 / det
	return Matrix{
		m[3] * id, -m[1] * id, -m[2] * id, m[0] * id,
		(m[2]*m[5] - m[3]*m[4]) * id, (m[1]*m[4] - m[0]*m[5]) * id,
	}, true
}

// IsIdentity reports m == Identity.
func (m Matrix) IsIdentity() bool { return m == Identity }

// IsTranslate reports a pure translation.
func (m Matrix) IsTranslate() bool { return m[0] == 1 && m[1] == 0 && m[2] == 0 && m[3] == 1 }

// TransformRect returns the bounding box of r mapped through m.
func (m Matrix) TransformRect(r Rect) Rect {
	ps := [4]Point{m.Apply(Point{r.X, r.Y}), m.Apply(Point{r.Right(), r.Y}), m.Apply(Point{r.X, r.Bottom()}), m.Apply(Point{r.Right(), r.Bottom()})}
	x0, y0, x1, y1 := ps[0].X, ps[0].Y, ps[0].X, ps[0].Y
	for _, p := range ps[1:] {
		x0, y0 = math.Min(x0, p.X), math.Min(y0, p.Y)
		x1, y1 = math.Max(x1, p.X), math.Max(y1, p.Y)
	}
	return Rect{x0, y0, x1 - x0, y1 - y0}
}

// FillRule selects how path windings decide insideness.
type FillRule uint8

const (
	NonZero FillRule = iota
	EvenOdd
)

// CompositeOp is a Porter-Duff operator or a separable/non-separable blend
// mode (canvas globalCompositeOperation and CSS mix-blend-mode).
type CompositeOp uint8

const (
	SrcOver CompositeOp = iota
	SrcIn
	SrcOut
	SrcAtop
	DstOver
	DstIn
	DstOut
	DstAtop
	Xor
	Copy
	Lighter
	Multiply
	Screen
	Overlay
	Darken
	Lighten
	ColorDodge
	ColorBurn
	HardLight
	SoftLight
	Difference
	Exclusion
	Hue
	Saturation
	ColorBlend
	Luminosity
)

// LineCap and LineJoin are stroke end and corner styles.
type LineCap uint8

const (
	ButtCap LineCap = iota
	RoundCap
	SquareCap
)

type LineJoin uint8

const (
	MiterJoin LineJoin = iota
	RoundJoin
	BevelJoin
)

// StrokeStyle describes how a path is stroked.
type StrokeStyle struct {
	Width      float64
	Cap        LineCap
	Join       LineJoin
	MiterLimit float64 // 10 when zero
	Dash       []float64
	DashOffset float64
}

// PaintKind selects what a Paint fills with.
type PaintKind uint8

const (
	PaintSolid PaintKind = iota
	PaintLinear
	PaintRadial
	PaintConic
	PaintPattern
)

// GradientStop is one colour stop at offset 0..1.
type GradientStop struct {
	Offset float64
	Color  Color
}

// Repeat is how a pattern image tiles.
type Repeat uint8

const (
	RepeatBoth Repeat = iota
	RepeatX
	RepeatY
	NoRepeat
)

// Paint is a fill source. Geometry fields are in the user space of the
// draw call; Matrix (when non-zero) maps paint space to user space, which
// is how canvas patterns' setTransform and CSS background sizing are done.
type Paint struct {
	Kind  PaintKind
	Color Color
	// Linear: (X0,Y0)->(X1,Y1). Radial: circles (X0,Y0,R0)->(X1,Y1,R1).
	// Conic: centre (X0,Y0), start Angle (radians, 0 = +x axis).
	X0, Y0, R0, X1, Y1, R1 float64
	Angle                  float64
	Stops                  []GradientStop
	// RepeatingGradient makes the stop list repeat (CSS repeating-*).
	RepeatingGradient bool
	// Pattern image and tiling.
	Image  image.Image
	Repeat Repeat
	Matrix Matrix
	// Smoothing selects bilinear (true) or nearest sampling for patterns.
	Smoothing bool
}

// SolidPaint is a convenience for a solid colour paint.
func SolidPaint(c Color) Paint { return Paint{Kind: PaintSolid, Color: c} }

// Shadow is a canvas/CSS drop shadow applied to a draw.
type Shadow struct {
	Color   Color
	OffsetX float64
	OffsetY float64
	Blur    float64 // CSS blur radius (canvas shadowBlur / 2 = sigma)
}
