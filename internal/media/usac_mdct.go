package media

import (
	"math"
	"math/cmplx"
	"sync"
)

// Mixed-radix complex FFT and the inverse MDCT / DCT-IV built on it, for the
// transform sizes USAC needs (768, 1024, 96, 128, and the TCX / FAC sizes).

type fftPlan struct {
	n       int
	factors []int
	tw      []complex128 // exp(-2*pi*i*k/n)
	scratch []complex128
}

func newFFTPlan(n int) *fftPlan {
	p := &fftPlan{n: n}
	m := n
	for _, f := range []int{4, 2, 3, 5, 7} {
		for m%f == 0 && m > 1 {
			p.factors = append(p.factors, f)
			m /= f
		}
	}
	for f := 11; m > 1; f += 2 {
		for m%f == 0 {
			p.factors = append(p.factors, f)
			m /= f
		}
	}
	p.tw = make([]complex128, n)
	for k := range p.tw {
		p.tw[k] = cmplx.Exp(complex(0, -2*math.Pi*float64(k)/float64(n)))
	}
	p.scratch = make([]complex128, n)
	return p
}

// fftOOP computes out[0:n] = DFT of in[0], in[stride], ..., in[(n-1)*stride].
func (p *fftPlan) fftOOP(out, in []complex128, n, stride, fi int) {
	if n == 1 {
		out[0] = in[0]
		return
	}
	f := p.factors[fi]
	m := n / f
	for r := 0; r < f; r++ {
		p.fftOOP(out[r*m:(r+1)*m], in[r*stride:], m, stride*f, fi+1)
	}
	// combine: X[k + q*m] = sum_r W_n^{r*(k+q*m)} * Y_r[k]
	step := p.n / n
	switch f {
	case 2:
		for k := 0; k < m; k++ {
			a := out[k]
			b := out[m+k] * p.tw[k*step]
			out[k] = a + b
			out[m+k] = a - b
		}
	case 4:
		for k := 0; k < m; k++ {
			a0 := out[k]
			a1 := out[m+k] * p.tw[k*step]
			a2 := out[2*m+k] * p.tw[2*k*step]
			a3 := out[3*m+k] * p.tw[3*k*step]
			s02, d02 := a0+a2, a0-a2
			s13, d13 := a1+a3, a1-a3
			// multiply d13 by -i
			jd := complex(imag(d13), -real(d13))
			out[k] = s02 + s13
			out[m+k] = d02 + jd
			out[2*m+k] = s02 - s13
			out[3*m+k] = d02 - jd
		}
	default:
		var tmp [16]complex128
		var t []complex128
		if f <= 16 {
			t = tmp[:f]
		} else {
			t = make([]complex128, f)
		}
		for k := 0; k < m; k++ {
			for r := 0; r < f; r++ {
				t[r] = out[r*m+k] * p.tw[(r*k*step)%p.n]
			}
			for q := 0; q < f; q++ {
				var s complex128
				for r := 0; r < f; r++ {
					s += t[r] * p.tw[((r*q*m*step)%p.n)]
				}
				out[q*m+k] = s
			}
		}
	}
}

func (p *fftPlan) forward(x []complex128) {
	if p.n <= 1 {
		return
	}
	buf := p.scratch
	p.fftOOP(buf, x, p.n, 1, 0)
	copy(x, buf)
}

// ---- inverse MDCT / DCT-IV ---------------------------------------------------

// usacMDCT computes, for a coefficient vector X of length m,
//
//	y[n] = sum_k X[k] cos(pi/m (n + 1/2 + m/2)(k + 1/2)), n < 2m
//
// (the same definition as imdctPlan.run) for any even m with m/2 a product of
// small primes.
type usacMDCT struct {
	m    int
	fft  *fftPlan
	tw   []complex128 // exp(-i*pi*(4n+1)/(4m))
	post []complex128 // exp(-i*pi*k/m)
	t    []complex128
	u    []float64
}

var (
	usacMdctMu    sync.Mutex
	usacMdctCache = map[int]*usacMDCT{}
)

// newUsacMDCT returns a private plan (scratch buffers are per plan).
func newUsacMDCT(m int) *usacMDCT {
	n := m / 2
	p := &usacMDCT{m: m, fft: newFFTPlan(n), tw: make([]complex128, n), post: make([]complex128, n)}
	for i := 0; i < n; i++ {
		p.tw[i] = cmplx.Exp(complex(0, -math.Pi*float64(4*i+1)/float64(4*m)))
		p.post[i] = cmplx.Exp(complex(0, -math.Pi*float64(i)/float64(m)))
	}
	p.t = make([]complex128, n)
	p.u = make([]float64, m)
	return p
}

// dct4 computes u[k'] = sum_k x[k] cos(pi/m (k+1/2)(k'+1/2)) into p.u.
func (p *usacMDCT) dct4(x []float64) []float64 {
	m := p.m
	n := m / 2
	t := p.t
	for i := 0; i < n; i++ {
		t[i] = complex(x[2*i], x[m-1-2*i]) * p.tw[i]
	}
	p.fft.forward(t)
	u := p.u
	for k := 0; k < n; k++ {
		c := t[k] * p.post[k]
		u[2*k] = real(c)
		u[m-1-2*k] = -imag(c)
	}
	return u
}

// run writes the 2m output samples of the inverse MDCT of x into y.
func (p *usacMDCT) run(x []float64, y []float64) {
	m := p.m
	u := p.dct4(x)
	h := m / 2
	for i := 0; i < 2*m; i++ {
		mm := i + h
		switch {
		case mm < m:
			y[i] = u[mm]
		case mm < 2*m:
			y[i] = -u[2*m-1-mm]
		default:
			y[i] = -u[mm-2*m]
		}
	}
}
