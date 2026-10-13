package rtcmedia

import (
	"math"

	"github.com/sweefbrowser/sweef/internal/capture"
)

func sqrtRatio(a, b int) float64 { return math.Sqrt(float64(a) / float64(b)) }

// NewFrame allocates an I420 frame of the given size (width and height are rounded up to even).
func NewFrame(w, h int) *capture.Frame {
	w, h = (w+1)&^1, (h+1)&^1
	cw, ch := w/2, h/2
	return &capture.Frame{W: w, H: h, Y: make([]byte, w*h), U: make([]byte, cw*ch), V: make([]byte, cw*ch), YStride: w, UVStride: cw}
}

// FillBlack sets a frame to black (Y 16, U/V 128).
func FillBlack(f *capture.Frame) {
	for y := 0; y < f.H; y++ {
		row := f.Y[y*f.YStride : y*f.YStride+f.W]
		for i := range row {
			row[i] = 16
		}
	}
	for y := 0; y < (f.H+1)/2; y++ {
		u := f.U[y*f.UVStride : y*f.UVStride+(f.W+1)/2]
		v := f.V[y*f.UVStride : y*f.UVStride+(f.W+1)/2]
		for i := range u {
			u[i], v[i] = 128, 128
		}
	}
}

// Scaler converts frames to another size (bilinear, with a box-filter pre-step for large reductions).
type Scaler struct {
	bufs [2][]byte
	xs   []xTap
}

type xTap struct {
	i0, i1 int
	w1     int32 // weight of i1 in 1/256
}

func (s *Scaler) taps(dw, sw int) []xTap {
	if cap(s.xs) < dw {
		s.xs = make([]xTap, dw)
	}
	t := s.xs[:dw]
	for x := 0; x < dw; x++ {
		// pixel centres: sx = (x + 0.5) * sw / dw - 0.5, in 1/256
		p := ((2*x+1)*sw*256)/(2*dw) - 128
		if p < 0 {
			p = 0
		}
		i := p >> 8
		w := int32(p & 255)
		if i >= sw-1 {
			i, w = sw-1, 0
		}
		i1 := i + 1
		if i1 >= sw {
			i1 = sw - 1
		}
		t[x] = xTap{i, i1, w}
	}
	return t
}

func halve(dst []byte, ds int, src []byte, ss, sw, sh int) (int, int) {
	dw, dh := sw/2, sh/2
	for y := 0; y < dh; y++ {
		r0 := src[2*y*ss : 2*y*ss+sw]
		r1 := src[(2*y+1)*ss : (2*y+1)*ss+sw]
		o := dst[y*ds : y*ds+dw]
		for x := range o {
			o[x] = byte((int(r0[2*x]) + int(r0[2*x+1]) + int(r1[2*x]) + int(r1[2*x+1]) + 2) >> 2)
		}
	}
	return dw, dh
}

// scalePlane scales one plane.
func (s *Scaler) scalePlane(dst []byte, ds, dw, dh int, src []byte, ss, sw, sh int) {
	if sw == dw && sh == dh {
		for y := 0; y < dh; y++ {
			copy(dst[y*ds:y*ds+dw], src[y*ss:y*ss+sw])
		}
		return
	}
	// reduce by whole factors of two while the source is at least twice the target
	cur := 0
	for sw >= 2*dw && sh >= 2*dh && sw >= 4 && sh >= 4 {
		need := (sw / 2) * (sh / 2)
		if cap(s.bufs[cur]) < need {
			s.bufs[cur] = make([]byte, need)
		}
		out := s.bufs[cur][:need]
		nw, nh := halve(out, sw/2, src, ss, sw, sh)
		src, ss, sw, sh = out, nw, nw, nh
		cur ^= 1
	}
	taps := s.taps(dw, sw)
	for y := 0; y < dh; y++ {
		p := ((2*y+1)*sh*256)/(2*dh) - 128
		if p < 0 {
			p = 0
		}
		j := p >> 8
		wy := int32(p & 255)
		if j >= sh-1 {
			j, wy = sh-1, 0
		}
		j1 := j + 1
		if j1 >= sh {
			j1 = sh - 1
		}
		r0 := src[j*ss : j*ss+sw]
		r1 := src[j1*ss : j1*ss+sw]
		o := dst[y*ds : y*ds+dw]
		for x := range o {
			t := taps[x]
			a := int32(r0[t.i0])*(256-t.w1) + int32(r0[t.i1])*t.w1
			b := int32(r1[t.i0])*(256-t.w1) + int32(r1[t.i1])*t.w1
			o[x] = byte((a*(256-wy) + b*wy + 32768) >> 16)
		}
	}
}

// Scale writes src, resampled, into dst (both I420; dst's size decides).
func (s *Scaler) Scale(dst, src *capture.Frame) {
	s.scalePlane(dst.Y, dst.YStride, dst.W, dst.H, src.Y, src.YStride, src.W, src.H)
	scw, sch := (src.W+1)/2, (src.H+1)/2
	dcw, dch := (dst.W+1)/2, (dst.H+1)/2
	s.scalePlane(dst.U, dst.UVStride, dcw, dch, src.U, src.UVStride, scw, sch)
	s.scalePlane(dst.V, dst.UVStride, dcw, dch, src.V, src.UVStride, scw, sch)
}

// fitDims returns the even size with the aspect ratio of sw x sh that has at most maxPixels pixels
// (and never more than the source).
func fitDims(sw, sh, maxPixels int) (int, int) {
	w, h := sw, sh
	if maxPixels > 0 && w*h > maxPixels {
		f := sqrtRatio(maxPixels, w*h)
		w = int(float64(w) * f)
		h = int(float64(h) * f)
	}
	w, h = w&^1, h&^1
	if w < 16 {
		w = 16
	}
	if h < 16 {
		h = 16
	}
	return w, h
}
