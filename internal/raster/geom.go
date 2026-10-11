// Package raster is a pure-Go software 2D rasterizer: anti-aliased path
// filling and stroking, gradients, patterns, compositing, blur, filters, text
// and a display-list painter (Paint) for internal/render. It is also the
// engine behind the HTML canvas 2D context.
package raster

import "math"

// Pt is a 2D point.
type Pt struct{ X, Y float64 }

// Matrix is an affine transform [a b c d e f]: x' = a*x + c*y + e, y' = b*x + d*y + f.
type Matrix [6]float64

// Identity is the identity transform.
var Identity = Matrix{1, 0, 0, 1, 0, 0}

// Mul returns m*n: the transform that applies n first, then m.
func (m Matrix) Mul(n Matrix) Matrix {
	return Matrix{
		m[0]*n[0] + m[2]*n[1],
		m[1]*n[0] + m[3]*n[1],
		m[0]*n[2] + m[2]*n[3],
		m[1]*n[2] + m[3]*n[3],
		m[0]*n[4] + m[2]*n[5] + m[4],
		m[1]*n[4] + m[3]*n[5] + m[5],
	}
}

// Apply transforms a point.
func (m Matrix) Apply(x, y float64) (float64, float64) {
	return m[0]*x + m[2]*y + m[4], m[1]*x + m[3]*y + m[5]
}

// ApplyPt transforms a point.
func (m Matrix) ApplyPt(p Pt) Pt {
	x, y := m.Apply(p.X, p.Y)
	return Pt{x, y}
}

// Det is the determinant of the linear part.
func (m Matrix) Det() float64 { return m[0]*m[3] - m[1]*m[2] }

// Invert returns the inverse; ok is false for singular matrices.
func (m Matrix) Invert() (Matrix, bool) {
	d := m.Det()
	if d == 0 || math.IsNaN(d) || math.IsInf(d, 0) {
		return Identity, false
	}
	id := 1 / d
	return Matrix{
		m[3] * id, -m[1] * id,
		-m[2] * id, m[0] * id,
		(m[2]*m[5] - m[3]*m[4]) * id,
		(m[1]*m[4] - m[0]*m[5]) * id,
	}, true
}

// Translate returns a translation matrix.
func Translate(x, y float64) Matrix { return Matrix{1, 0, 0, 1, x, y} }

// Scale returns a scale matrix.
func Scale(x, y float64) Matrix { return Matrix{x, 0, 0, y, 0, 0} }

// Rotate returns a rotation matrix (radians, clockwise in y-down space).
func Rotate(a float64) Matrix {
	s, c := math.Sincos(a)
	return Matrix{c, s, -s, c, 0, 0}
}

// IsAxisAligned reports whether m maps axis-aligned rectangles to
// axis-aligned rectangles (no rotation/skew other than 90 degree turns).
func (m Matrix) IsAxisAligned() bool {
	return (m[1] == 0 && m[2] == 0) || (m[0] == 0 && m[3] == 0)
}

// IsIdentity reports whether m is the identity.
func (m Matrix) IsIdentity() bool { return m == Identity }

// MaxScale is the largest singular value, an upper bound on stretching.
func (m Matrix) MaxScale() float64 {
	a, b, c, d := m[0], m[1], m[2], m[3]
	s1 := a*a + b*b + c*c + d*d
	det := a*d - b*c
	disc := math.Sqrt(math.Max(0, s1*s1-4*det*det))
	return math.Sqrt((s1 + disc) / 2)
}

// Rect is a float rectangle (x, y, w, h).
type Rect struct{ X, Y, W, H float64 }

func (r Rect) Right() float64  { return r.X + r.W }
func (r Rect) Bottom() float64 { return r.Y + r.H }

// IRect is an integer pixel rectangle [X0,X1) x [Y0,Y1).
type IRect struct{ X0, Y0, X1, Y1 int }

func (r IRect) Empty() bool { return r.X1 <= r.X0 || r.Y1 <= r.Y0 }
func (r IRect) W() int      { return r.X1 - r.X0 }
func (r IRect) H() int      { return r.Y1 - r.Y0 }

// Intersect returns the overlap of two rects.
func (r IRect) Intersect(o IRect) IRect {
	if o.X0 > r.X0 {
		r.X0 = o.X0
	}
	if o.Y0 > r.Y0 {
		r.Y0 = o.Y0
	}
	if o.X1 < r.X1 {
		r.X1 = o.X1
	}
	if o.Y1 < r.Y1 {
		r.Y1 = o.Y1
	}
	if r.Empty() {
		return IRect{}
	}
	return r
}

// Union returns the bounding rect of both.
func (r IRect) Union(o IRect) IRect {
	if r.Empty() {
		return o
	}
	if o.Empty() {
		return r
	}
	return IRect{min(r.X0, o.X0), min(r.Y0, o.Y0), max(r.X1, o.X1), max(r.Y1, o.Y1)}
}

// OutsetBounds returns the integer rect that fully contains r.
func (r Rect) OutsetBounds() IRect {
	return IRect{int(math.Floor(r.X)), int(math.Floor(r.Y)), int(math.Ceil(r.X + r.W)), int(math.Ceil(r.Y + r.H))}
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

func clampU8(v float64) uint8 {
	if v <= 0 {
		return 0
	}
	if v >= 255 {
		return 255
	}
	return uint8(v + 0.5)
}
