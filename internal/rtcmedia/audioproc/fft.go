// Package audioproc is the voice processing of a call: acoustic echo cancellation (a partitioned-block frequency
// domain adaptive filter with delay tracking and a residual echo suppressor), noise suppression (spectral gain with a
// minimum-statistics noise estimate) and automatic gain control. It processes 48 kHz mono in 20 ms frames, the unit
// the WebRTC audio sender works in, and takes the far end (what is about to be played) from the receive side.
//
// It is plain Go, small and deterministic; it is not a port of WebRTC's AEC3 and does not reach its quality. The
// measured behaviour (echo return loss enhancement, speech preservation during double talk, noise reduction) is
// reported by the tests and in docs/webrtc.md.
package audioproc

import (
	"math"
	"math/bits"
)

// fft is a radix-2 complex FFT of a fixed power-of-two size.
type fft struct {
	n   int
	tw  []complex128
	rev []int
}

func newFFT(n int) *fft {
	if n&(n-1) != 0 {
		panic("audioproc: FFT size must be a power of two")
	}
	f := &fft{n: n, tw: make([]complex128, n/2), rev: make([]int, n)}
	for i := range f.tw {
		a := -2 * math.Pi * float64(i) / float64(n)
		f.tw[i] = complex(math.Cos(a), math.Sin(a))
	}
	sh := 64 - bits.Len(uint(n-1))
	for i := range f.rev {
		f.rev[i] = int(bits.Reverse64(uint64(i)) >> uint(sh))
	}
	return f
}

// transform does the FFT in place; inverse scales by 1/n.
func (f *fft) transform(x []complex128, inverse bool) {
	n := f.n
	for i, j := range f.rev {
		if i < j {
			x[i], x[j] = x[j], x[i]
		}
	}
	for size := 2; size <= n; size <<= 1 {
		half := size >> 1
		step := n / size
		for start := 0; start < n; start += size {
			for k := 0; k < half; k++ {
				w := f.tw[k*step]
				if inverse {
					w = complex(real(w), -imag(w))
				}
				a, b := x[start+k], x[start+k+half]*w
				x[start+k], x[start+k+half] = a+b, a-b
			}
		}
	}
	if inverse {
		s := 1 / float64(n)
		for i := range x {
			x[i] = complex(real(x[i])*s, imag(x[i])*s)
		}
	}
}

// realFFT of n real samples into n/2+1 bins (using a complex transform of size n).
func (f *fft) forwardReal(in []float64, out []complex128, work []complex128) {
	for i := 0; i < f.n; i++ {
		work[i] = complex(in[i], 0)
	}
	f.transform(work, false)
	copy(out, work[:f.n/2+1])
}

// inverseReal rebuilds n real samples from n/2+1 bins.
func (f *fft) inverseReal(in []complex128, out []float64, work []complex128) {
	n := f.n
	copy(work, in[:n/2+1])
	for k := 1; k < n/2; k++ {
		c := in[k]
		work[n-k] = complex(real(c), -imag(c))
	}
	f.transform(work, true)
	for i := 0; i < n; i++ {
		out[i] = real(work[i])
	}
}
