package media

import (
	"math"
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
	// analysis: exp(i pi/64 (k+1/2)(2n-1)) for k<32, n<64
	sbrAnaCos, sbrAnaSin [32 * 64]float64
	// synthesis 64: exp(i pi/128 (k+1/2)(2n-255)) for n<128, k<64
	sbrSynCos, sbrSynSin [128 * 64]float64
	// synthesis 32 (downsampled): exp(i pi/64 (k+1/2)(2n-127)) n<64, k<32
	sbrSyn32Cos, sbrSyn32Sin [64 * 32]float64
)

var sbrAnaOff, sbrSynOff, sbrSyn32Off, sbrDsShift = -0.5, -255.0, -127.5, 0

func sbrQMFInit() {
	sbrQMFOnce.Do(func() {
		for i, v := range sbrQMFWindow {
			sbrWinUS[i] = float64(v)
		}
		for i := range sbrWinDS {
			sbrWinDS[i] = sbrWinUS[2*i+sbrDsShift]
		}
		for k := 0; k < 32; k++ {
			for n := 0; n < 64; n++ {
				a := math.Pi / 64 * (float64(k) + 0.5) * (float64(2*n) + sbrAnaOff)
				sbrAnaCos[k*64+n] = math.Cos(a)
				sbrAnaSin[k*64+n] = math.Sin(a)
			}
		}
		for n := 0; n < 128; n++ {
			for k := 0; k < 64; k++ {
				a := math.Pi / 128 * (float64(k) + 0.5) * (float64(2*n) + sbrSynOff)
				sbrSynCos[n*64+k] = math.Cos(a)
				sbrSynSin[n*64+k] = math.Sin(a)
			}
		}
		for n := 0; n < 64; n++ {
			for k := 0; k < 32; k++ {
				a := math.Pi / 64 * (float64(k) + 0.5) * (float64(2*n) + sbrSyn32Off)
				sbrSyn32Cos[n*32+k] = math.Cos(a)
				sbrSyn32Sin[n*32+k] = math.Sin(a)
			}
		}
	})
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
	var u [64]float64
	for l := 0; l < 32; l++ {
		xx := a.x[32*l : 32*l+320]
		for n := 0; n < 64; n++ {
			s := 0.0
			for j := 0; j < 5; j++ {
				idx := n + 64*j
				s += sbrWinDS[idx] * xx[319-idx]
			}
			u[n] = s
		}
		for k := 0; k < 32; k++ {
			re, im := 0.0, 0.0
			c := sbrAnaCos[k*64 : k*64+64]
			s := sbrAnaSin[k*64 : k*64+64]
			for n := 0; n < 64; n++ {
				re += u[n] * c[n]
				im += u[n] * s[n]
			}
			w[l][k][0] = re * sbrAnaScale
			w[l][k][1] = im * sbrAnaScale
		}
	}
}

// sbrSynthesis holds the synthesis filterbank state of one channel.
type sbrSynthesis struct {
	v [1280]float64
}

// run synthesises 32 slots of 64 bands x[slot][band][re,im] into 2048 samples
// (or, with down, 32 bands into 1024 samples).
func (s *sbrSynthesis) run(x *[32][64][2]float64, out []float32, down bool) {
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
	for l := 0; l < 32; l++ {
		copy(s.v[128:1280], s.v[:1152])
		for n := 0; n < 128; n++ {
			sum := 0.0
			c := sbrSynCos[n*64 : n*64+64]
			sn := sbrSynSin[n*64 : n*64+64]
			for k := 0; k < 64; k++ {
				sum += x[l][k][0]*c[k] - x[l][k][1]*sn[k]
			}
			s.v[n] = sum * sbrSynScale
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
