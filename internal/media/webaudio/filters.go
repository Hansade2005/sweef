package webaudio

import (
	"math"
	"math/cmplx"
)

// biquadCoefs returns normalised (b0,b1,b2,a1,a2) for the given filter.
func biquadCoefs(kind string, freq, q, gainDb, sr float64) (b0, b1, b2, a1, a2 float64) {
	nyq := sr / 2
	nf := freq / nyq
	pass := func() (float64, float64, float64, float64, float64) { return 1, 0, 0, 0, 0 }
	zero := func() (float64, float64, float64, float64, float64) { return 0, 0, 0, 0, 0 }
	if math.IsNaN(nf) {
		return pass()
	}
	switch kind {
	case "lowpass":
		if nf >= 1 {
			return pass()
		}
		if nf <= 0 {
			return zero()
		}
		return lowpassC(nf, q)
	case "highpass":
		if nf >= 1 {
			return zero()
		}
		if nf <= 0 {
			return pass()
		}
		return highpassC(nf, q)
	case "bandpass":
		if nf <= 0 || nf >= 1 {
			return zero()
		}
		if q <= 0 {
			return pass()
		}
		w0 := math.Pi * nf
		alpha := math.Sin(w0) / (2 * q)
		k := math.Cos(w0)
		return norm(alpha, 0, -alpha, 1+alpha, -2*k, 1-alpha)
	case "notch":
		if nf <= 0 || nf >= 1 {
			return pass()
		}
		if q <= 0 {
			return zero()
		}
		w0 := math.Pi * nf
		alpha := math.Sin(w0) / (2 * q)
		k := math.Cos(w0)
		return norm(1, -2*k, 1, 1+alpha, -2*k, 1-alpha)
	case "allpass":
		if nf <= 0 || nf >= 1 {
			return pass()
		}
		if q <= 0 {
			return func() (float64, float64, float64, float64, float64) { return -1, 0, 0, 0, 0 }()
		}
		w0 := math.Pi * nf
		alpha := math.Sin(w0) / (2 * q)
		k := math.Cos(w0)
		return norm(1-alpha, -2*k, 1+alpha, 1+alpha, -2*k, 1-alpha)
	case "peaking":
		if nf <= 0 || nf >= 1 {
			return pass()
		}
		A := math.Pow(10, gainDb/40)
		if q <= 0 {
			return A * A, 0, 0, 0, 0
		}
		w0 := math.Pi * nf
		alpha := math.Sin(w0) / (2 * q)
		k := math.Cos(w0)
		return norm(1+alpha*A, -2*k, 1-alpha*A, 1+alpha/A, -2*k, 1-alpha/A)
	case "lowshelf":
		A := math.Pow(10, gainDb/40)
		if nf >= 1 {
			return A * A, 0, 0, 0, 0
		}
		if nf <= 0 {
			return pass()
		}
		w0 := math.Pi * nf
		S := 1.0
		alpha := math.Sin(w0) / 2 * math.Sqrt((A+1/A)*(1/S-1)+2)
		k := math.Cos(w0)
		k2 := 2 * math.Sqrt(A) * alpha
		return norm(A*((A+1)-(A-1)*k+k2), 2*A*((A-1)-(A+1)*k), A*((A+1)-(A-1)*k-k2), (A+1)+(A-1)*k+k2, -2*((A-1)+(A+1)*k), (A+1)+(A-1)*k-k2)
	case "highshelf":
		A := math.Pow(10, gainDb/40)
		if nf >= 1 {
			return pass()
		}
		if nf <= 0 {
			return A * A, 0, 0, 0, 0
		}
		w0 := math.Pi * nf
		S := 1.0
		alpha := math.Sin(w0) / 2 * math.Sqrt((A+1/A)*(1/S-1)+2)
		k := math.Cos(w0)
		k2 := 2 * math.Sqrt(A) * alpha
		return norm(A*((A+1)+(A-1)*k+k2), -2*A*((A-1)+(A+1)*k), A*((A+1)+(A-1)*k-k2), (A+1)-(A-1)*k+k2, 2*((A-1)-(A+1)*k), (A+1)-(A-1)*k-k2)
	}
	return pass()
}

func lowpassC(nf, qDb float64) (float64, float64, float64, float64, float64) {
	w0 := math.Pi * nf
	g := math.Pow(10, qDb/20)
	alpha := math.Sin(w0) / (2 * g)
	k := math.Cos(w0)
	return norm((1-k)/2, 1-k, (1-k)/2, 1+alpha, -2*k, 1-alpha)
}

func highpassC(nf, qDb float64) (float64, float64, float64, float64, float64) {
	w0 := math.Pi * nf
	g := math.Pow(10, qDb/20)
	alpha := math.Sin(w0) / (2 * g)
	k := math.Cos(w0)
	return norm((1+k)/2, -(1 + k), (1+k)/2, 1+alpha, -2*k, 1-alpha)
}

func norm(b0, b1, b2, a0, a1, a2 float64) (float64, float64, float64, float64, float64) {
	return b0 / a0, b1 / a0, b2 / a0, a1 / a0, a2 / a0
}

type biquadProc struct {
	kind           string
	x1, x2, y1, y2 [8]float64
}

func (b *biquadProc) coefsAt(n *Node, c *Context, freq, det, q, g float64) (float64, float64, float64, float64, float64) {
	f := freq * math.Pow(2, det/1200)
	nyq := c.SampleRate / 2
	if f > nyq {
		f = nyq
	}
	if f < 0 {
		f = 0
	}
	return biquadCoefs(b.kind, f, q, g, c.SampleRate)
}

func (b *biquadProc) process(n *Node, c *Context, start int64, frames int, in [][][]float32) {
	fp, dp, qp, gp := n.Params["frequency"], n.Params["detune"], n.Params["Q"], n.Params["gain"]
	freq := fp.compute(c, start, frames)
	det := dp.compute(c, start, frames)
	q := qp.compute(c, start, frames)
	g := gp.compute(c, start, frames)
	perFrame := fp.automated() || dp.automated() || qp.automated() || gp.automated() || len(fp.inputs)+len(dp.inputs)+len(qp.inputs)+len(gp.inputs) > 0
	out := n.setOut(0, len(in[0]))
	b0, b1, b2, a1, a2 := b.coefsAt(n, c, float64(freq[0]), float64(det[0]), float64(q[0]), float64(g[0]))
	for ch := range out {
		x1, x2, y1, y2 := b.x1[ch], b.x2[ch], b.y1[ch], b.y2[ch]
		src, dst := in[0][ch], out[ch]
		for i := 0; i < frames; i++ {
			if perFrame && i > 0 {
				b0, b1, b2, a1, a2 = b.coefsAt(n, c, float64(freq[i]), float64(det[i]), float64(q[i]), float64(g[i]))
			}
			x := float64(src[i])
			y := b0*x + b1*x1 + b2*x2 - a1*y1 - a2*y2
			x2, x1 = x1, x
			y2, y1 = y1, y
			dst[i] = float32(y)
		}
		b.x1[ch], b.x2[ch], b.y1[ch], b.y2[ch] = x1, x2, y1, y2
	}
}

// FrequencyResponse evaluates a biquad at the given frequencies (Hz). Values
// outside [0, nyquist] give NaN, as in the spec.
func (c *Context) FrequencyResponse(id int, freqs []float64) (mag, phase []float64) {
	n := c.byID[id]
	mag = make([]float64, len(freqs))
	phase = make([]float64, len(freqs))
	var b, a []float64
	switch p := n.proc.(type) {
	case *biquadProc:
		t := c.Time()
		f := n.Params["frequency"].ValueAt(t)
		d := n.Params["detune"].ValueAt(t)
		q := n.Params["Q"].ValueAt(t)
		g := n.Params["gain"].ValueAt(t)
		b0, b1, b2, a1, a2 := p.coefsAt(n, c, f, d, q, g)
		b = []float64{b0, b1, b2}
		a = []float64{1, a1, a2}
	case *iirProc:
		b, a = p.ff, p.fb
	default:
		return
	}
	nyq := c.SampleRate / 2
	for i, f := range freqs {
		if f < 0 || f > nyq || math.IsNaN(f) {
			mag[i], phase[i] = math.NaN(), math.NaN()
			continue
		}
		w := math.Pi * f / nyq
		z := cmplx.Rect(1, -w)
		var num, den complex128
		zz := complex(1, 0)
		for k := range b {
			num += complex(b[k], 0) * zz
			zz *= z
		}
		zz = complex(1, 0)
		for k := range a {
			den += complex(a[k], 0) * zz
			zz *= z
		}
		h := num / den
		mag[i] = cmplx.Abs(h)
		phase[i] = cmplx.Phase(h)
	}
	return
}

// ---- IIRFilterNode ------------------------------------------------------------------------------

type iirProc struct {
	ff, fb []float64
	x, y   [][]float64
}

func newIIRProc(ff, fb []float64) *iirProc {
	if len(fb) > 0 && fb[0] != 0 {
		a0 := fb[0]
		nf := make([]float64, len(ff))
		nb := make([]float64, len(fb))
		for i := range ff {
			nf[i] = ff[i] / a0
		}
		for i := range fb {
			nb[i] = fb[i] / a0
		}
		ff, fb = nf, nb
	}
	return &iirProc{ff: ff, fb: fb}
}

func (p *iirProc) process(n *Node, c *Context, start int64, frames int, in [][][]float32) {
	ch := len(in[0])
	for len(p.x) < ch {
		p.x = append(p.x, make([]float64, len(p.ff)))
		p.y = append(p.y, make([]float64, len(p.fb)))
	}
	out := n.setOut(0, ch)
	for k := 0; k < ch; k++ {
		x, y := p.x[k], p.y[k]
		for i := 0; i < frames; i++ {
			copy(x[1:], x[:len(x)-1])
			x[0] = float64(in[0][k][i])
			var s float64
			for j := range p.ff {
				s += p.ff[j] * x[j]
			}
			for j := 1; j < len(p.fb); j++ {
				s -= p.fb[j] * y[j-1]
			}
			copy(y[1:], y[:len(y)-1])
			if len(y) > 0 {
				y[0] = s
			}
			out[k][i] = float32(s)
		}
	}
}
