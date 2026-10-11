// Package webaudio implements the Web Audio rendering graph in pure Go:
// nodes, AudioParam automation, channel mixing and the DSP kernels. The
// JS-visible API (AudioContext, nodes, params) lives in the engine prelude
// and drives a Context through string commands.
package webaudio

import (
	"math"
	"math/cmplx"
)

// fft computes an in-place radix-2 FFT (inverse when inv is true, unscaled).
func fft(x []complex128, inv bool) {
	n := len(x)
	for i, j := 1, 0; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j ^= bit
		if i < j {
			x[i], x[j] = x[j], x[i]
		}
	}
	for l := 2; l <= n; l <<= 1 {
		ang := -2 * math.Pi / float64(l)
		if inv {
			ang = -ang
		}
		wl := cmplx.Rect(1, ang)
		for i := 0; i < n; i += l {
			w := complex(1, 0)
			for k := 0; k < l/2; k++ {
				u, v := x[i+k], x[i+k+l/2]*w
				x[i+k], x[i+k+l/2] = u+v, u-v
				w *= wl
			}
		}
	}
}

// fftPlan caches twiddles for repeated transforms of one size.
type fftPlan struct {
	n   int
	tw  []complex128
	rev []int
}

func newPlan(n int) *fftPlan {
	p := &fftPlan{n: n, tw: make([]complex128, n/2), rev: make([]int, n)}
	for i := range p.tw {
		p.tw[i] = cmplx.Rect(1, -2*math.Pi*float64(i)/float64(n))
	}
	bits := 0
	for 1<<uint(bits) < n {
		bits++
	}
	for i := 0; i < n; i++ {
		r := 0
		for b := 0; b < bits; b++ {
			if i&(1<<uint(b)) != 0 {
				r |= 1 << uint(bits-1-b)
			}
		}
		p.rev[i] = r
	}
	return p
}

func (p *fftPlan) run(x []complex128, inv bool) {
	n := p.n
	for i := 0; i < n; i++ {
		if j := p.rev[i]; i < j {
			x[i], x[j] = x[j], x[i]
		}
	}
	for l := 2; l <= n; l <<= 1 {
		step := n / l
		for i := 0; i < n; i += l {
			for k := 0; k < l/2; k++ {
				w := p.tw[k*step]
				if inv {
					w = cmplx.Conj(w)
				}
				u, v := x[i+k], x[i+k+l/2]*w
				x[i+k], x[i+k+l/2] = u+v, u-v
			}
		}
	}
}
