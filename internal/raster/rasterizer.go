package raster

import (
	"math"
	"sync"
)

// FillRule selects how winding numbers map to coverage.
type FillRule int

const (
	NonZero FillRule = iota
	EvenOdd
)

// Mask is an 8-bit coverage map over Bounds.
type Mask struct {
	B      IRect
	Stride int
	A      []uint8
}

func newMask(b IRect) *Mask {
	w := b.W()
	return &Mask{B: b, Stride: w, A: make([]uint8, w*b.H())}
}

// Row returns the coverage row for absolute y (nil outside).
func (m *Mask) Row(y int) []uint8 {
	if y < m.B.Y0 || y >= m.B.Y1 {
		return nil
	}
	o := (y - m.B.Y0) * m.Stride
	return m.A[o : o+m.Stride]
}

// At returns coverage at absolute pixel coordinates.
func (m *Mask) At(x, y int) uint8 {
	if x < m.B.X0 || x >= m.B.X1 || y < m.B.Y0 || y >= m.B.Y1 {
		return 0
	}
	return m.A[(y-m.B.Y0)*m.Stride+x-m.B.X0]
}

var accPool = sync.Pool{New: func() any { s := make([]float32, 0, 1<<16); return &s }}

// rasterizer accumulates signed area coverage for one bounding box.
type rasterizer struct {
	b      IRect
	w, h   int
	stride int
	acc    []float32
	pbuf   *[]float32
}

func newRasterizer(b IRect) *rasterizer {
	r := &rasterizer{b: b, w: b.W(), h: b.H()}
	r.stride = r.w + 2
	n := r.stride * r.h
	r.pbuf = accPool.Get().(*[]float32)
	if cap(*r.pbuf) < n {
		*r.pbuf = make([]float32, n)
	} else {
		*r.pbuf = (*r.pbuf)[:n]
		clear(*r.pbuf)
	}
	r.acc = *r.pbuf
	return r
}

func (r *rasterizer) release() { accPool.Put(r.pbuf) }

// line accumulates the signed area of an edge given in device coordinates.
func (r *rasterizer) line(x0, y0, x1, y1 float64) {
	x0 -= float64(r.b.X0)
	x1 -= float64(r.b.X0)
	y0 -= float64(r.b.Y0)
	y1 -= float64(r.b.Y0)
	if y0 == y1 {
		return
	}
	dir := float32(1)
	if y0 > y1 {
		x0, y0, x1, y1 = x1, y1, x0, y0
		dir = -1
	}
	if y1 <= 0 || y0 >= float64(r.h) {
		return
	}
	dxdy := (x1 - x0) / (y1 - y0)
	x := x0
	if y0 < 0 {
		x -= y0 * dxdy
		y0 = 0
	}
	yEnd := y1
	if yEnd > float64(r.h) {
		yEnd = float64(r.h)
	}
	fw := float64(r.w)
	for y := int(y0); y < r.h && float64(y) < yEnd; y++ {
		fy := float64(y)
		dy := math.Min(fy+1, yEnd) - math.Max(fy, y0)
		if dy <= 0 {
			continue
		}
		xn := x + dxdy*dy
		d := float32(dy) * dir
		xa, xb := x, xn
		if xa > xb {
			xa, xb = xb, xa
		}
		// clamp horizontally; edges outside left still contribute at column 0
		if xb <= 0 {
			r.acc[y*r.stride] += d
			x = xn
			continue
		}
		if xa >= fw {
			x = xn
			continue
		}
		if xa < 0 {
			xa = 0
		}
		if xb > fw {
			xb = fw
		}
		row := r.acc[y*r.stride : (y+1)*r.stride]
		x0f := math.Floor(xa)
		x0i := int(x0f)
		x1c := math.Ceil(xb)
		x1i := int(x1c)
		if x1i <= x0i+1 {
			xmf := float32(0.5*(xa+xb) - x0f)
			row[x0i] += d - d*xmf
			row[x0i+1] += d * xmf
		} else {
			s := float32(1 / (xb - xa))
			x0fr := float32(xa - x0f)
			a0 := 0.5 * s * (1 - x0fr) * (1 - x0fr)
			x1fr := float32(xb - x1c + 1)
			am := 0.5 * s * x1fr * x1fr
			row[x0i] += d * a0
			if x1i == x0i+2 {
				row[x0i+1] += d * (1 - a0 - am)
			} else {
				a1 := s * (1.5 - x0fr)
				row[x0i+1] += d * (a1 - a0)
				for xi := x0i + 2; xi < x1i-1; xi++ {
					row[xi] += d * s
				}
				a2 := a1 + float32(x1i-x0i-3)*s
				row[x1i-1] += d * (1 - a2 - am)
			}
			row[x1i] += d * am
		}
		x = xn
	}
}

func (r *rasterizer) mask(rule FillRule) *Mask {
	m := newMask(r.b)
	for y := 0; y < r.h; y++ {
		row := r.acc[y*r.stride : y*r.stride+r.w]
		out := m.A[y*m.Stride : y*m.Stride+r.w]
		var a float32
		if rule == NonZero {
			for x, d := range row {
				a += d
				v := a
				if v < 0 {
					v = -v
				}
				if v >= 1 {
					out[x] = 255
				} else {
					out[x] = uint8(v*255 + 0.5)
				}
			}
		} else {
			for x, d := range row {
				a += d
				v := a
				if v < 0 {
					v = -v
				}
				v -= 2 * float32(math.Floor(float64(v)/2))
				if v > 1 {
					v = 2 - v
				}
				out[x] = uint8(v*255 + 0.5)
			}
		}
	}
	return m
}

// polyBounds returns the float bounding box of subpaths.
func polyBounds(sp []Subpath) (x0, y0, x1, y1 float64, ok bool) {
	x0, y0, x1, y1 = math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, s := range sp {
		for _, p := range s.P {
			if math.IsNaN(p.X) || math.IsNaN(p.Y) {
				return 0, 0, 0, 0, false
			}
			x0, x1 = math.Min(x0, p.X), math.Max(x1, p.X)
			y0, y1 = math.Min(y0, p.Y), math.Max(y1, p.Y)
		}
	}
	return x0, y0, x1, y1, x0 <= x1
}

// RasterizePolys produces a coverage mask of the (implicitly closed)
// polylines, restricted to clip. Returns nil when nothing is covered.
func RasterizePolys(sp []Subpath, rule FillRule, clip IRect) *Mask {
	x0, y0, x1, y1, ok := polyBounds(sp)
	if !ok {
		return nil
	}
	const lim = 1 << 28
	b := IRect{int(math.Floor(math.Max(x0, -lim))), int(math.Floor(math.Max(y0, -lim))), int(math.Ceil(math.Min(x1, lim))), int(math.Ceil(math.Min(y1, lim)))}
	b = b.Intersect(clip)
	if b.Empty() {
		return nil
	}
	r := newRasterizer(b)
	defer r.release()
	for _, s := range sp {
		n := len(s.P)
		if n < 2 {
			continue
		}
		for i := 0; i < n-1; i++ {
			r.line(s.P[i].X, s.P[i].Y, s.P[i+1].X, s.P[i+1].Y)
		}
		r.line(s.P[n-1].X, s.P[n-1].Y, s.P[0].X, s.P[0].Y)
	}
	return r.mask(rule)
}

// FillMask rasterizes a path transformed by m.
func FillMask(p *Path, m Matrix, rule FillRule, clip IRect) *Mask {
	return RasterizePolys(flattenDevice(p, m), rule, clip)
}

// flattenDevice flattens p after transforming to device space.
func flattenDevice(p *Path, m Matrix) []Subpath {
	q := p
	if !m.IsIdentity() {
		q = p.Transform(m)
	}
	return q.Flatten(0.05)
}
