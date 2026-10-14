package audioproc

import "math"

const (
	nsN    = 512
	nsHop  = 256
	nsBins = nsN/2 + 1
)

// spectral is the second stage: a short-time spectral gain that removes stationary noise (noise PSD from minimum
// statistics) and the echo the adaptive filter left behind (weighted by the filter's own echo estimate).
type spectral struct {
	f       *fft
	win     []float64
	in      []float64
	echo    []float64
	olap    []float64
	work    []complex128
	e, y    []complex128
	tmp     []float64
	noise   []float64
	smooth  []float64
	minCur  []float64
	minHist [][]float64
	hops    int
	gPrev   []float64
	gamma   []float64
	echoPow []float64
	gmin    float64
}

func newSpectral() *spectral {
	s := &spectral{f: newFFT(nsN), win: make([]float64, nsN), in: make([]float64, nsN), echo: make([]float64, nsN), olap: make([]float64, nsN),
		work: make([]complex128, nsN), e: make([]complex128, nsBins), y: make([]complex128, nsBins), tmp: make([]float64, nsN),
		noise: make([]float64, nsBins), smooth: make([]float64, nsBins), minCur: make([]float64, nsBins), gPrev: make([]float64, nsBins),
		gamma: make([]float64, nsBins), echoPow: make([]float64, nsBins)}
	for i := range s.win {
		s.win[i] = math.Sin(math.Pi * (float64(i) + 0.5) / nsN)
	}
	for i := range s.noise {
		s.noise[i] = 1e-9
		s.minCur[i] = math.MaxFloat64
		s.gPrev[i] = 1
	}
	s.minHist = make([][]float64, 6)
	for i := range s.minHist {
		s.minHist[i] = make([]float64, nsBins)
		for k := range s.minHist[i] {
			s.minHist[i][k] = math.MaxFloat64
		}
	}
	return s
}

// hop consumes nsHop new samples of the signal and of the echo estimate and writes nsHop output samples.
// noiseOn enables the noise term; echoLevel (0..1) scales how hard the estimated echo is suppressed.
func (s *spectral) hop(x, y, out []float64, noiseOn bool, echoLevel float64) {
	copy(s.in, s.in[nsHop:])
	copy(s.in[nsHop:], x)
	copy(s.echo, s.echo[nsHop:])
	copy(s.echo[nsHop:], y)
	for i := 0; i < nsN; i++ {
		s.tmp[i] = s.in[i] * s.win[i]
	}
	s.f.forwardReal(s.tmp, s.e, s.work)
	for i := 0; i < nsN; i++ {
		s.tmp[i] = s.echo[i] * s.win[i]
	}
	s.f.forwardReal(s.tmp, s.y, s.work)
	// noise floor by minimum statistics: smoothed power, minimum over the last ~2 s, with a bias correction
	s.hops++
	for k := 0; k < nsBins; k++ {
		p := real(s.e[k])*real(s.e[k]) + imag(s.e[k])*imag(s.e[k])
		s.smooth[k] = 0.85*s.smooth[k] + 0.15*p
		if s.smooth[k] < s.minCur[k] {
			s.minCur[k] = s.smooth[k]
		}
		yp := real(s.y[k])*real(s.y[k]) + imag(s.y[k])*imag(s.y[k])
		s.echoPow[k] = 0.6*s.echoPow[k] + 0.4*yp
	}
	if s.hops%64 == 0 { // every ~340 ms start a new sub-window
		oldest := s.minHist[0]
		copy(s.minHist[0:], s.minHist[1:])
		s.minHist[len(s.minHist)-1], s.minCur = s.minCur, oldest
		for k := range s.minCur {
			s.minCur[k] = math.MaxFloat64
		}
	}
	for k := 0; k < nsBins; k++ {
		m := s.minCur[k]
		for _, h := range s.minHist {
			if h[k] < m {
				m = h[k]
			}
		}
		if m < math.MaxFloat64 {
			s.noise[k] = math.Max(1.5*m, 1e-10)
		}
	}
	gmin := 0.15
	if echoLevel > 0 {
		gmin = 0.03
	}
	if !noiseOn && echoLevel == 0 {
		gmin = 1
	}
	var gain [nsBins]float64
	for k := 0; k < nsBins; k++ {
		p := real(s.e[k])*real(s.e[k]) + imag(s.e[k])*imag(s.e[k])
		nt := 0.0
		if noiseOn {
			nt = s.noise[k]
		}
		nt += 0.8 * echoLevel * s.echoPow[k]
		if nt < 1e-12 {
			gain[k] = 1
			continue
		}
		gm := p / nt
		xi := 0.96*s.gPrev[k]*s.gPrev[k]*s.gamma[k] + 0.04*math.Max(gm-1, 0)
		if xi < 0.01 {
			xi = 0.01
		}
		g := xi / (1 + xi)
		s.gamma[k] = gm
		if g < gmin {
			g = gmin
		}
		gain[k] = g
	}
	// smooth over frequency and time against musical noise
	for k := 0; k < nsBins; k++ {
		a, b, c := gain[max(k-1, 0)], gain[k], gain[min(k+1, nsBins-1)]
		g := 0.25*a + 0.5*b + 0.25*c
		g = 0.4*s.gPrev[k] + 0.6*g
		if g > 1 {
			g = 1
		}
		s.gPrev[k] = g
		s.e[k] = complex(real(s.e[k])*g, imag(s.e[k])*g)
	}
	s.f.inverseReal(s.e, s.tmp, s.work)
	for i := 0; i < nsN; i++ {
		s.olap[i] += s.tmp[i] * s.win[i]
	}
	copy(out, s.olap[:nsHop])
	copy(s.olap, s.olap[nsHop:])
	clear(s.olap[nsHop:])
}
