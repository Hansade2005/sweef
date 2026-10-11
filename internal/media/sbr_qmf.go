package media

import (
	"math"
	"math/cmplx"
	"sync"
)

// SBR QMF banks (ISO/IEC 14496-3 4.6.18.4): 32 band complex analysis and 64
// band (or 32 band, downsampled mode) synthesis. Scaling follows FFmpeg: the
// analysis works on 16 bit scaled samples, the synthesis returns to +-1.

const (
	sbrAnaScale = 2 * 32768.0
	sbrSynScale = 1.0 / (64 * 32768.0)
)

var (
	sbrQMFOnce sync.Once
	sbrWinUS   [640]float64 // c[n]
	sbrWinDS   [320]float64 // c[2n]
	// synthesis 32 (downsampled): exp(i pi/64 (k+1/2)(2n+sbrSyn32Off)) n<64, k<32
	sbrSyn32Cos, sbrSyn32Sin [64 * 32]float64
	// fast transform helpers
	sbrFFT32Tw, sbrFFT64Tw [32]complex128 // forward FFT twiddles exp(-2 pi i k/N)
	sbrDCT4Tw, sbrDCT4Post [32]complex128 // exp(-i pi (n+1/4)/64), exp(-i pi n/64)
	sbrAnaPre, sbrAnaPost  [64]complex128 // exp(-i pi n/64), exp(-i pi (k+1/2)/128)
)

// Modulation phases of the QMF banks (the half sample offsets reproduce the
// delay and the inter band phase relation of the reference decoder, which
// matter for the HF generation):
//
//	analysis      X[k] = sum_n u[n] exp(i pi/64  (k+1/2)(2n+sbrAnaOff))
//	synthesis 64  v[n] = Re sum_k X[k] exp(i pi/128 (k+1/2)(2n+sbrSynOff))
//	synthesis 32  v[n] = Re sum_k X[k] exp(i pi/64  (k+1/2)(2n+sbrSyn32Off))
const (
	sbrAnaOff   = -0.5
	sbrSynOff   = -255.0
	sbrSyn32Off = -127.5
)

func sbrQMFInit() {
	sbrQMFOnce.Do(func() {
		for i, v := range sbrQMFWindow {
			sbrWinUS[i] = float64(v)
		}
		for i := range sbrWinDS {
			sbrWinDS[i] = sbrWinUS[2*i]
		}
		for n := 0; n < 64; n++ {
			for k := 0; k < 32; k++ {
				a := math.Pi / 64 * (float64(k) + 0.5) * (float64(2*n) + sbrSyn32Off)
				sbrSyn32Cos[n*32+k] = math.Cos(a)
				sbrSyn32Sin[n*32+k] = math.Sin(a)
			}
		}
		for k := 0; k < 32; k++ {
			sbrFFT32Tw[k] = cmplx.Exp(complex(0, -2*math.Pi*float64(k)/32))
			sbrFFT64Tw[k] = cmplx.Exp(complex(0, -2*math.Pi*float64(k)/64))
			sbrDCT4Tw[k] = cmplx.Exp(complex(0, -math.Pi*float64(4*k+1)/256))
			sbrDCT4Post[k] = cmplx.Exp(complex(0, -math.Pi*float64(k)/64))
		}
		for n := 0; n < 64; n++ {
			sbrAnaPre[n] = cmplx.Exp(complex(0, -math.Pi*float64(n)/64))
			sbrAnaPost[n] = cmplx.Exp(complex(0, -math.Pi*(float64(n)+0.5)/128))
		}
	})
}

// sbrFFT runs an in place radix-2 forward FFT of n (32 or 64) points.
func sbrFFT(x []complex128, tw *[32]complex128) {
	n := len(x)
	// bit reversal
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
		half := l >> 1
		stride := n / l
		for i := 0; i < n; i += l {
			for k := 0; k < half; k++ {
				w := tw[k*stride]
				u := x[i+k]
				v := x[i+k+half] * w
				x[i+k] = u + v
				x[i+k+half] = u - v
			}
		}
	}
}

// sbrDCT4 computes the 64 point DCT-IV out[m] = sum_k x[k] cos(pi/64 (m+1/2)(k+1/2)).
func sbrDCT4(x *[64]float64, out *[64]float64) {
	var t [32]complex128
	for n := 0; n < 32; n++ {
		t[n] = complex(x[2*n], x[63-2*n]) * sbrDCT4Tw[n]
	}
	sbrFFT(t[:], &sbrFFT32Tw)
	for k := 0; k < 32; k++ {
		y := t[k] * sbrDCT4Post[k]
		out[2*k] = real(y)
		out[63-2*k] = -imag(y)
	}
}

// sbrAnalysis holds the analysis filterbank history of one channel.
type sbrAnalysis struct {
	x [320 + 1024 - 32]float64 // oldest sample first
}

// run analyses 1024 new input samples (+-1 scale) into 32 slots of 32 complex
// subband samples w[slot][band][re,im].
func (a *sbrAnalysis) run(in []float32, w *[32][32][2]float64) {
	sbrQMFInit()
	// x layout: [0,288) history, then the new samples
	copy(a.x[:288], a.x[1024:1024+288])
	for i := 0; i < 1024; i++ {
		a.x[288+i] = float64(in[i])
	}
	var z [64]complex128
	for l := 0; l < 32; l++ {
		xx := a.x[32*l : 32*l+320]
		for n := 0; n < 64; n++ {
			s := 0.0
			for j := 0; j < 5; j++ {
				idx := n + 64*j
				s += sbrWinDS[idx] * xx[319-idx]
			}
			z[n] = complex(s, 0) * sbrAnaPre[n]
		}
		sbrFFT(z[:], &sbrFFT64Tw)
		for k := 0; k < 32; k++ {
			// the transform above ran with a negative exponent on the conjugate
			// of the wanted sum: sum_n u[n] e^{+i pi n/64} e^{+2 pi i k n/64}
			y := cmplx.Conj(z[k]) * sbrAnaPost[k]
			w[l][k][0] = real(y) * sbrAnaScale
			w[l][k][1] = imag(y) * sbrAnaScale
		}
	}
}

// sbrSynthesis holds the synthesis filterbank state of one channel.
type sbrSynthesis struct {
	v [1280]float64
}

// run synthesises 32 slots of 64 bands x[slot][band][re,im] into 2048 samples
// (or, with down, 32 bands into 1024 samples).
func (s *sbrSynthesis) run(x *[38][64][2]float64, out []float32, down bool) {
	sbrQMFInit()
	if down {
		for l := 0; l < 32; l++ {
			copy(s.v[64:640], s.v[:576])
			for n := 0; n < 64; n++ {
				sum := 0.0
				c := sbrSyn32Cos[n*32 : n*32+32]
				sn := sbrSyn32Sin[n*32 : n*32+32]
				for k := 0; k < 32; k++ {
					sum += x[l][k][0]*c[k] - x[l][k][1]*sn[k]
				}
				s.v[n] = sum * sbrSynScale
			}
			for k := 0; k < 32; k++ {
				sum := 0.0
				for j := 0; j < 5; j++ {
					sum += s.v[128*j+k]*sbrWinDS[64*j+k] + s.v[128*j+96+k]*sbrWinDS[64*j+32+k]
				}
				out[l*32+k] = float32(sum)
			}
		}
		return
	}
	var ci, si, cc, ss [64]float64
	for l := 0; l < 32; l++ {
		copy(s.v[128:1280], s.v[:1152])
		// v[64+j] = S[j]+C[j], v[63-j] = C[j]-S[j] with DCT-IV / DST-IV of
		// (-1)^k Xi[k] and (-1)^k Xr[k]
		for k := 0; k < 64; k++ {
			sign := 1.0
			if k&1 == 1 {
				sign = -1
			}
			ci[k] = sign * x[l][k][1]
		}
		for m := 0; m < 64; m++ {
			// s_in[m] = -(-1)^m Xr[63-m]
			sign := 1.0
			if m&1 == 1 {
				sign = -1
			}
			si[m] = -sign * x[l][63-m][0]
		}
		sbrDCT4(&ci, &cc)
		sbrDCT4(&si, &ss)
		for j := 0; j < 64; j++ {
			sj := ss[j]
			if j&1 == 1 {
				sj = -sj
			}
			s.v[64+j] = (sj + cc[j]) * sbrSynScale
			s.v[63-j] = (cc[j] - sj) * sbrSynScale
		}
		for k := 0; k < 64; k++ {
			sum := 0.0
			for j := 0; j < 5; j++ {
				sum += s.v[256*j+k]*sbrWinUS[128*j+k] + s.v[256*j+192+k]*sbrWinUS[128*j+64+k]
			}
			out[l*64+k] = float32(sum)
		}
	}
}
