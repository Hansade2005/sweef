package media

import (
	"math"
	"sync"
)

// eSBR QMF banks. The analysis bank has 32 (2:1), 24 (8:3) or 16 (4:1) bands,
// the synthesis bank always 64. Gains and phase offsets were calibrated
// against the output of libxaac's fixed point filterbanks, which follow the
// standard QMF definitions (ISO/IEC 14496-3 4.6.18.4) up to these constants.

type esbrAnaDef struct {
	m    int
	gain float64
	off  float64 // exponent offset: exp(j pi/(2M) (k+1/2) (2n+off))
	win  []float64
	cs   []complex128 // [k*2M+n]
}

var (
	esbrAnaOnce sync.Once
	esbrAna32   esbrAnaDef
	esbrAna24   esbrAnaDef
	esbrAna16   esbrAnaDef
)

func esbrAnaInit() {
	esbrAnaOnce.Do(func() {
		sbrQMFInit()
		build := func(d *esbrAnaDef, m int, gain, off float64, win []float64) {
			d.m, d.gain, d.off, d.win = m, gain, off, win
			d.cs = make([]complex128, m*2*m)
			for k := 0; k < m; k++ {
				for n := 0; n < 2*m; n++ {
					a := math.Pi / float64(2*m) * (float64(k) + 0.5) * (float64(2*n) + off)
					d.cs[k*2*m+n] = complex(math.Cos(a), math.Sin(a))
				}
			}
		}
		w32 := make([]float64, 320)
		w16 := make([]float64, 160)
		for i := range w32 {
			w32[i] = sbrWinUS[2*i]
		}
		for i := range w16 {
			w16[i] = sbrWinUS[4*i]
		}
		w24 := make([]float64, 240)
		for i := range w24 {
			w24[i] = float64(esbrQmfC24[i]) / 2147483648.0
		}
		build(&esbrAna32, 32, 2, -0.5, w32)
		build(&esbrAna24, 24, 8.0/3.0, -0.375, w24)
		build(&esbrAna16, 16, 4, -0.25, w16)
	})
}

// esbrAnalysis is the analysis filterbank state of one channel.
type esbrAnalysis struct {
	def *esbrAnaDef
	x   []float64 // newest sample first, 10*M entries
}

func newESBRAnalysis(m int) *esbrAnalysis {
	esbrAnaInit()
	a := &esbrAnalysis{}
	switch m {
	case 32:
		a.def = &esbrAna32
	case 24:
		a.def = &esbrAna24
	default:
		a.def = &esbrAna16
	}
	a.x = make([]float64, 10*a.def.m)
	return a
}

// slot filters M new samples (core samples scaled to 16 bit units) into M
// complex subband samples.
func (a *esbrAnalysis) slot(in []float64, re, im []float32) {
	d := a.def
	m := d.m
	copy(a.x[m:], a.x[:9*m])
	for i := 0; i < m; i++ {
		a.x[m-1-i] = in[i]
	}
	var u [64]float64
	for n := 0; n < 2*m; n++ {
		s := 0.0
		for j := 0; j < 5; j++ {
			idx := n + 2*m*j
			s += a.x[idx] * d.win[idx]
		}
		u[n] = s
	}
	for k := 0; k < m; k++ {
		var sr, si float64
		row := d.cs[k*2*m : (k+1)*2*m]
		for n := 0; n < 2*m; n++ {
			sr += u[n] * real(row[n])
			si += u[n] * imag(row[n])
		}
		re[k] = float32(sr * d.gain)
		im[k] = float32(si * d.gain)
	}
}

// esbrSynthesis is the 64 band synthesis bank (one channel).
type esbrSynthesis struct {
	s sbrSynthesis
}

// run synthesises n slots (a multiple of 32) of 64 bands into 64*n samples in
// 16 bit units: the spec synthesis with a gain of 1/64.
func (s *esbrSynthesis) run(re, im [][64]float32, n int, out []float64) {
	var x [38][64][2]float64
	var o [2048]float32
	for base := 0; base < n; base += 32 {
		for l := 0; l < 32; l++ {
			for k := 0; k < 64; k++ {
				x[l][k][0] = float64(re[base+l][k])
				x[l][k][1] = float64(im[base+l][k])
			}
		}
		s.s.run(&x, o[:], false)
		for i := 0; i < 2048; i++ {
			out[64*base+i] = float64(o[i]) * 32768.0
		}
	}
}
