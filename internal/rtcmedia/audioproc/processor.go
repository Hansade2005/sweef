package audioproc

import (
	"math"
	"sync"
)

// Options say which parts of the processing run (MediaTrackConstraints echoCancellation, noiseSuppression,
// autoGainControl).
type Options struct {
	EchoCancellation bool
	NoiseSuppression bool
	AutoGain         bool
}

const (
	rate       = 48000
	farLen     = 1 << 17 // samples of far end history (2.7 s)
	envStep    = 384     // 8 ms
	envLen     = 512     // envelope buckets kept
	delayPrior = 4800    // samples the reference lags the far end by until the delay is measured (100 ms)
)

// Processor implements rtcmedia.AudioProcessor: AnalyzeFarEnd receives what is about to be played, ProcessNearEnd
// cleans one 20 ms frame (960 samples, mono) from the microphone in place. It is safe to call from the receive and
// send goroutines at once.
type Processor struct {
	mu   sync.Mutex
	opts Options

	far     [farLen]float64
	farHead int64 // samples written so far

	nearHead  int64 // samples consumed so far
	haveFar   bool
	delayInit bool
	delay     int64 // reference sample for near sample n is far[n-delay]

	in, out []float64 // FIFOs between the 20 ms frames and the 512-sample blocks
	hopIn   []float64
	aecOut  []float64
	echoEst []float64
	sOut    []float64
	nsIn    []float64
	nsEcho  []float64
	nsFifoI []float64
	nsFifoE []float64
	nsOutQ  []float64

	aec *fdaf
	ns  *spectral
	agc *agc
	est *delayEstimator

	// statistics (see Stats)
	echoIn, echoOut float64
}

// New creates a processor with everything on.
func New() *Processor {
	p := &Processor{opts: Options{EchoCancellation: true, NoiseSuppression: true, AutoGain: true}}
	p.init()
	return p
}

func (p *Processor) init() {
	p.aec, p.ns, p.agc = newFDAF(), newSpectral(), newAGC()
	p.est = newDelayEstimator()
	p.out = make([]float64, blockSize) // primes the output so a 960-sample frame is always available
	p.aecOut = make([]float64, blockSize)
	p.echoEst = make([]float64, blockSize)
	p.sOut = make([]float64, nsHop)
}

// SetOptions turns the processing stages on and off.
func (p *Processor) SetOptions(o Options) {
	p.mu.Lock()
	p.opts = o
	p.mu.Unlock()
}

// Options returns the current options.
func (p *Processor) Options() Options {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.opts
}

// AnalyzeFarEnd takes what is about to be played (interleaved 16-bit PCM, normally 20 ms of 48 kHz stereo).
func (p *Processor) AnalyzeFarEnd(pcm []int16, srate, channels int) {
	if channels < 1 || len(pcm) < channels || srate != rate {
		return
	}
	n := len(pcm) / channels
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := 0; i < n; i++ {
		var s float64
		for c := 0; c < channels; c++ {
			s += float64(pcm[i*channels+c])
		}
		v := s / float64(channels) / 32768
		p.far[p.farHead%farLen] = v
		p.farHead++
	}
	p.est.addFar(p.farHead-int64(n), p.far[:], n)
	p.haveFar = true
}

// ProcessNearEnd cleans one frame of the microphone signal (mono, 48 kHz) in place.
func (p *Processor) ProcessNearEnd(pcm []int16) {
	p.mu.Lock()
	defer p.mu.Unlock()
	o := p.opts
	if !o.EchoCancellation && !o.NoiseSuppression && !o.AutoGain {
		p.nearHead += int64(len(pcm))
		return
	}
	for _, v := range pcm {
		p.in = append(p.in, float64(v)/32768)
	}
	for len(p.in) >= blockSize {
		blk := p.in[:blockSize]
		p.block(blk, o)
		p.in = p.in[blockSize:]
	}
	if len(p.in) == 0 {
		p.in = nil
	}
	for i := range pcm {
		if i < len(p.out) {
			pcm[i] = toS16(p.out[i])
		}
	}
	if len(pcm) <= len(p.out) {
		p.out = p.out[len(pcm):]
	} else {
		p.out = p.out[:0]
	}
}

func toS16(v float64) int16 {
	v *= 32768
	if v > 32767 {
		return 32767
	}
	if v < -32768 {
		return -32768
	}
	return int16(v)
}

// block runs one 512-sample block through the stages and appends its output to the FIFO.
func (p *Processor) block(near []float64, o Options) {
	start := p.nearHead
	p.nearHead += blockSize
	p.est.addNear(start, near)
	ref := make([]float64, blockSize)
	haveRef := false
	if o.EchoCancellation && p.haveFar {
		if !p.delayInit {
			// until the first measurement: the reference lags the far end's head by a guess, fixed from here on so that
			// the reference runs on the near end's clock
			p.delay, p.delayInit = start-(p.farHead-delayPrior), true
		}
		// the delay is only measured while the filter does not explain the echo yet (a converged filter is left alone)
		if p.aec.converged < 0.5 {
			if d, changed, ok := p.est.update(start, p.farHead); ok {
				if changed || !p.est.applied {
					p.aec.reset()
					p.est.applied = true
				}
				p.delay = d
			}
		}
		from := start - p.delay
		if from >= 0 && from+blockSize <= p.farHead && from >= p.farHead-farLen {
			for i := 0; i < blockSize; i++ {
				ref[i] = p.far[(from+int64(i))%farLen]
			}
			haveRef = true
		}
	}
	res := near
	echoLevel := 0.0
	if haveRef {
		p.aec.process(ref, near, p.aecOut, p.echoEst)
		res = p.aecOut
		echoLevel = 1
		// What is left after the filter should be a small fraction of the echo estimate (the more converged the
		// filter, the smaller). Much more than that is not echo but the near speaker, or a filter that has not
		// converged yet: that is not to be suppressed in the name of echo.
		leak := 0.5 - 0.45*p.aec.converged
		if energy(res) > 3*leak*energy(p.echoEst)+1e-9 {
			echoLevel = 0
		}
		p.echoIn += energy(near)
		p.echoOut += energy(res)
	} else {
		clear(p.echoEst)
	}
	// second stage in hops of 256
	noiseOn := o.NoiseSuppression
	for h := 0; h < blockSize/nsHop; h++ {
		x := res[h*nsHop : (h+1)*nsHop]
		y := p.echoEst[h*nsHop : (h+1)*nsHop]
		if noiseOn || echoLevel > 0 {
			p.ns.hop(x, y, p.sOut, noiseOn, echoLevel)
			p.out = append(p.out, p.sOut...)
		} else {
			p.out = append(p.out, x...)
		}
	}
	if o.AutoGain {
		tail := p.out[len(p.out)-blockSize:]
		p.agc.process(tail)
	}
}

// Stats reports the echo return loss enhancement (dB) over the audio processed since the last call, or 0 when
// the echo canceller had nothing to do.
func (p *Processor) Stats() (erle float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.echoIn > 0 && p.echoOut > 0 {
		erle = 10 * math.Log10(p.echoIn/p.echoOut)
	}
	p.echoIn, p.echoOut = 0, 0
	return erle
}

// ---- delay estimation ---------------------------------------------------------------------------------------

// delayEstimator finds the delay between the far end and its echo in the near signal from the loudness envelopes
// of both (8 ms buckets), which is robust against the unknown phase and the room's filtering.
type delayEstimator struct {
	farEnv  [envLen]float64
	nearEnv [envLen]float64
	farB    int64 // buckets finished
	nearB   int64
	farAcc  float64
	nearAcc float64
	nearCnt int
	farCnt  int
	nextRun int64
	prev    int64 // candidate seen last time
	have    bool
	ready   bool
	applied bool
	cur     int64
}

func newDelayEstimator() *delayEstimator { return &delayEstimator{} }

func (d *delayEstimator) addFar(first int64, ring []float64, n int) {
	for i := 0; i < n; i++ {
		v := math.Abs(ring[(first+int64(i))%farLen])
		if v > d.farAcc {
			d.farAcc = v
		}
		d.farCnt++
		if d.farCnt == envStep {
			d.farEnv[d.farB%envLen] = d.farAcc
			d.farB++
			d.farAcc, d.farCnt = 0, 0
		}
	}
}

func (d *delayEstimator) addNear(first int64, x []float64) {
	for _, s := range x {
		v := math.Abs(s)
		if v > d.nearAcc {
			d.nearAcc = v
		}
		d.nearCnt++
		if d.nearCnt == envStep {
			d.nearEnv[d.nearB%envLen] = d.nearAcc
			d.nearB++
			d.nearAcc, d.nearCnt = 0, 0
		}
	}
}

// update runs about once a second. It returns the delay D (reference sample for near sample n is far[n-D]) when a
// confident measurement differs from the current one by more than 24 ms.
func (d *delayEstimator) update(nearPos, farHead int64) (delay int64, changed, ok bool) {
	if d.nearB < 125 || nearPos < d.nextRun {
		return d.cur, false, d.ready
	}
	d.nextRun = nearPos + rate // once per second
	// the near bucket index j covers near samples [j*envStep, (j+1)*envStep); far bucket index f covers far samples likewise.
	// candidate D (samples): near bucket j pairs with far bucket j - D/envStep.
	const win = 250 // near buckets compared (2 s)
	n0 := d.nearB - win
	if n0 < 0 {
		return d.cur, false, d.ready
	}
	// onsets: the positive change of the loudness from one bucket to the next. Echoes smear levels over tens of
	// milliseconds, but a speech onset stays an onset, so this correlates much more sharply than the levels do.
	on := func(env *[envLen]float64, j int64) float64 {
		d := env[j%envLen] - env[(j-1+envLen)%envLen]
		if d < 0 {
			return 0
		}
		return d
	}
	var nm float64
	for j := n0; j < d.nearB; j++ {
		nm += on(&d.nearEnv, j)
	}
	nm /= win
	var nv float64
	for j := n0; j < d.nearB; j++ {
		x := on(&d.nearEnv, j) - nm
		nv += x * x
	}
	if nv < 1e-8 {
		return d.cur, false, d.ready
	}
	// lag range in buckets, relative to the guess "far head" alignment
	center := (d.nearB*envStep - farHead + delayPrior) / envStep
	var lags []int64
	var cs []float64
	lo, hi := center-62, center+190 // -0.5 s .. +1.5 s around the prior
	for lag := lo; lag <= hi; lag++ {
		var fm float64
		valid := true
		for j := n0; j < d.nearB; j++ {
			f := j - lag
			if f < 0 || f >= d.farB || d.farB-f > envLen {
				valid = false
				break
			}
			fm += on(&d.farEnv, f)
		}
		if !valid {
			continue
		}
		fm /= win
		var fv, cv float64
		for j := n0; j < d.nearB; j++ {
			x := on(&d.farEnv, j-lag) - fm
			fv += x * x
			cv += x * (on(&d.nearEnv, j) - nm)
		}
		if fv < 1e-8 {
			continue
		}
		lags = append(lags, lag)
		cs = append(cs, cv/math.Sqrt(fv*nv))
	}
	best, bestC, second := int64(0), -1.0, -1.0
	for i, c := range cs {
		if c > bestC {
			best, bestC = lags[i], c
		}
	}
	for i, c := range cs {
		if abs64(lags[i]-best) > 3 && c > second {
			second = c
		}
	}
	if bestC < 0.3 || bestC < 1.25*math.Max(second, 0) {
		return d.cur, false, d.ready
	}
	// the reference must end after the echo's source (the filter models the past): aim a few buckets before the measured onset
	cand := (best - 4) * envStep
	if d.have && abs64(best-d.prev) <= 1 || !d.ready {
		if !d.ready || abs64(cand-d.cur) > 32*rate/1000 {
			changed = d.ready
			d.cur, d.ready = cand, true
			d.prev, d.have = best, true
			return d.cur, changed, true
		}
	}
	d.prev, d.have = best, true
	return d.cur, false, d.ready
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// ---- automatic gain control ---------------------------------------------------------------------------------------

type agc struct {
	gain  float64
	floor float64
}

func newAGC() *agc { return &agc{gain: 1, floor: 0.002} }

// process applies a slowly moving gain toward a target level while there is speech: louder than three times the
// noise floor, which is followed as a minimum that only creeps upward (a pause lets it fall at once).
func (a *agc) process(x []float64) {
	rms := math.Sqrt(energy(x) / float64(len(x)))
	if rms < a.floor {
		a.floor = math.Max(rms, 1e-5)
	} else {
		a.floor *= 1.0015 // about +15% per second
	}
	if rms > 3*a.floor {
		want := clampf(0.08/rms, 0.5, 8)
		if want < a.gain {
			a.gain += (want - a.gain) * 0.2 // loud: turn down quickly
		} else {
			a.gain += (want - a.gain) * 0.02 // quiet: turn up slowly
		}
	}
	for i := range x {
		v := x[i] * a.gain
		// soft limiter
		if v > 0.9 {
			v = 0.9 + 0.1*math.Tanh((v-0.9)/0.1)
		} else if v < -0.9 {
			v = -0.9 + 0.1*math.Tanh((v+0.9)/0.1)
		}
		x[i] = v
	}
}
