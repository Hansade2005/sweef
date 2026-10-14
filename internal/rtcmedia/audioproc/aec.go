package audioproc

import "math"

const (
	blockSize = 512  // samples per adaptive-filter block (10.7 ms at 48 kHz)
	fftSize   = 1024 // overlap-save transform
	bins      = fftSize/2 + 1
	partCount = 24 // partitions: the filter spans partCount*blockSize = 256 ms
)

// fdaf is a partitioned-block frequency-domain adaptive filter (the multi-delay filter): the echo path is modelled
// as partCount blocks of blockSize taps, each adapted with a normalised update; one partition per block also gets
// the gradient constraint, which keeps the update cost low.
type fdaf struct {
	f     *fft
	hist  [][]complex128 // far spectra, hist[(newest-p) mod partCount] is p blocks old
	w     [][]complex128 // per-partition weights
	newst int
	pow   []float64 // smoothed far power per bin, summed over all partitions
	prevX []float64 // previous far block (the first half of the overlap-save window)

	work  []complex128
	buf   []float64
	spec  []complex128
	espec []complex128
	yspec []complex128

	// adaptation control
	farPeak   float64 // recent far-end peak
	hang      int     // blocks left to keep adaptation frozen after double talk was suspected
	eAvg      float64 // smoothed error energy
	dAvg      float64 // smoothed near energy
	yAvg      float64 // smoothed echo estimate energy
	converged float64 // 0..1: how well the filter explains the near signal
}

func newFDAF() *fdaf {
	a := &fdaf{f: newFFT(fftSize), prevX: make([]float64, blockSize), pow: make([]float64, bins),
		work: make([]complex128, fftSize), buf: make([]float64, fftSize), spec: make([]complex128, bins), espec: make([]complex128, bins), yspec: make([]complex128, bins)}
	a.hist = make([][]complex128, partCount)
	a.w = make([][]complex128, partCount)
	for i := range a.hist {
		a.hist[i] = make([]complex128, bins)
		a.w[i] = make([]complex128, bins)
	}
	return a
}

func (a *fdaf) reset() {
	for i := range a.w {
		clear(a.w[i])
		clear(a.hist[i])
	}
	clear(a.pow)
	clear(a.prevX)
	a.converged, a.eAvg, a.dAvg, a.yAvg, a.hang = 0, 0, 0, 0, 0
}

func energy(x []float64) float64 {
	var s float64
	for _, v := range x {
		s += v * v
	}
	return s
}

func peak(x []float64) float64 {
	var p float64
	for _, v := range x {
		if v < 0 {
			v = -v
		}
		if v > p {
			p = v
		}
	}
	return p
}

// process takes a far-end block (the reference, aligned to this near block) and the near block, and produces the
// echo-cancelled block and the echo estimate. All three slices have blockSize samples.
func (a *fdaf) process(far, near, out, echo []float64) {
	B := blockSize
	// far spectrum of [previous block, this block]
	copy(a.buf[:B], a.prevX)
	copy(a.buf[B:], far)
	copy(a.prevX, far)
	a.newst = (a.newst + 1) % partCount
	a.f.forwardReal(a.buf, a.hist[a.newst], a.work)
	X := a.hist[a.newst]
	for k := 0; k < bins; k++ {
		var s float64
		// total power over every partition: normalises the update for the whole filter length
		_ = s
		a.pow[k] = 0.9*a.pow[k] + 0.1*(real(X[k])*real(X[k])+imag(X[k])*imag(X[k]))
	}
	// echo estimate
	clear(a.yspec)
	for p := 0; p < partCount; p++ {
		H := a.hist[(a.newst-p+partCount)%partCount]
		W := a.w[p]
		for k := 0; k < bins; k++ {
			a.yspec[k] += W[k] * H[k]
		}
	}
	a.f.inverseReal(a.yspec, a.buf, a.work)
	copy(echo, a.buf[B:])
	for i := 0; i < B; i++ {
		out[i] = near[i] - echo[i]
	}
	// controls
	ex, ed, ee, ey := energy(far), energy(near), energy(out), energy(echo)
	fp := peak(far)
	if fp > a.farPeak {
		a.farPeak = fp
	} else {
		a.farPeak = 0.995*a.farPeak + 0.005*fp
	}
	a.dAvg = 0.9*a.dAvg + 0.1*ed
	a.eAvg = 0.9*a.eAvg + 0.1*ee
	a.yAvg = 0.9*a.yAvg + 0.1*ey
	// Geigel double-talk detector: a near signal louder than the far end could have echoed means someone is talking
	if peak(near) > 1.0*a.farPeak && a.farPeak > 0 {
		a.hang = 4
	} else if a.hang > 0 {
		a.hang--
	}
	farActive := ex/float64(B) > 1e-7 // about -70 dBFS rms
	if !farActive || a.hang > 0 {
		if ed > 0 {
			a.converged = 0.95*a.converged + 0.05*clampf(1-ee/(ed+1e-12), 0, 1)
		}
		return
	}
	if ed > 1e-12 {
		a.converged = 0.95*a.converged + 0.05*clampf(1-ee/ed, 0, 1)
	}
	// a filter that makes things worse is thrown away
	if ee > 4*ed+1e-9 && a.eAvg > 3*a.dAvg {
		a.reset()
		copy(out, near)
		return
	}
	mu := 0.9
	if a.converged > 0.6 {
		mu = 0.4
		// a converged filter leaves little: an error much larger than a small share of the echo estimate means the
		// near speaker is talking (or the room changed); adapt very slowly, so that speech does not pull the filter
		// away but a changed room is still followed
		if a.converged > 0.95 && ee > 0.2*ey && ee > 1e-7*float64(B) {
			mu = 0.04
		}
	}
	// error spectrum of [zeros, e]
	clear(a.buf[:B])
	copy(a.buf[B:], out)
	a.f.forwardReal(a.buf, a.espec, a.work)
	// update: W_p += mu * conj(X_p) * E / (P + eps)
	for p := 0; p < partCount; p++ {
		H := a.hist[(a.newst-p+partCount)%partCount]
		W := a.w[p]
		for k := 0; k < bins; k++ {
			d := a.pow[k]*float64(partCount) + 1e-3
			g := complex(mu/d, 0)
			W[k] += g * complex(real(H[k]), -imag(H[k])) * a.espec[k]
		}
	}
	// gradient constraint: the time-domain filter of a partition keeps only its first block (this is what makes the
	// filter causal and of the right length); every partition is constrained on every block
	for p := 0; p < partCount; p++ {
		a.f.inverseReal(a.w[p], a.buf, a.work)
		clear(a.buf[B:])
		a.f.forwardReal(a.buf, a.w[p], a.work)
	}
}

func clampf(v, lo, hi float64) float64 {
	return math.Max(lo, math.Min(hi, v))
}
