package audioproc

import (
	"math"
	"math/rand"
	"testing"
)

const frame = 960

// speechLike makes a signal with the structure that matters to the processors: bursts and pauses of random length
// (syllables, words, phrases) over coloured noise.
type speechLike struct {
	r     *rand.Rand
	lp    float64
	phase float64
	rate  float64 // average syllable rate (1/s)
	level float64
	gate  func(t float64) bool

	env, target float64
	left        int
}

func (s *speechLike) next(t float64) float64 {
	n := s.r.NormFloat64()
	s.lp = 0.85*s.lp + 0.15*n
	if s.left <= 0 { // a new segment every 40-400 ms: voiced or a pause
		s.left = int((0.04 + 0.36*s.r.Float64()) * rate)
		if s.r.Float64() < 0.6 {
			s.target = 0.4 + 0.6*s.r.Float64()
		} else {
			s.target = 0.02
		}
	}
	s.left--
	s.env += (s.target - s.env) * 0.002
	if s.gate != nil && !s.gate(t) {
		return 0
	}
	return s.level * 2.2 * s.lp * s.env
}

// room is a decaying random echo path of the given delay.
func room(seed int64, delay int, gain float64) []float64 {
	r := rand.New(rand.NewSource(seed))
	h := make([]float64, delay+6000)
	for i := 0; i < 6000; i++ {
		h[delay+i] = gain * r.NormFloat64() * math.Exp(-float64(i)/1500) * 0.2
	}
	h[delay] = gain
	return h
}

type sim struct {
	p      *Processor
	farSig []float64
	nearSp []float64
	h      []float64
	noise  float64
	rng    *rand.Rand
	out    []float64
	in     []float64
	echo   []float64
}

// run feeds the processor for seconds of audio. farGate / nearGate say when the far end and the near speaker talk.
func run(t *testing.T, p *Processor, seconds float64, farGate, nearGate func(t float64) bool, h []float64, farAhead int) (echoOnly, mixed *sim) {
	t.Helper()
	n := int(seconds*rate) / frame * frame
	far := &speechLike{r: rand.New(rand.NewSource(1)), rate: 3.1, level: 0.16, gate: farGate}
	near := &speechLike{r: rand.New(rand.NewSource(2)), rate: 2.3, level: 0.4, phase: 1, gate: nearGate}
	s := &sim{p: p, h: h, rng: rand.New(rand.NewSource(3)), noise: 0.0015}
	s.farSig = make([]float64, n+farAhead+len(h))
	for i := 0; i < len(s.farSig); i++ {
		s.farSig[i] = far.next(float64(i) / rate)
	}
	s.nearSp = make([]float64, n)
	for i := range s.nearSp {
		s.nearSp[i] = near.next(float64(i) / rate)
	}
	// the far end is played farAhead samples after it was handed to the processor
	stereo := make([]int16, frame*2)
	pcm := make([]int16, frame)
	echo := convolve(s.farSig[:n], h)
	s.echo = echo
	s.out = make([]float64, 0, n)
	s.in = make([]float64, 0, n)
	for f := 0; f*frame < n; f++ {
		// far frame for the playback buffer: its samples are those played farAhead later
		for i := 0; i < frame; i++ {
			v := toS16(s.farSig[f*frame+i+farAhead])
			stereo[2*i], stereo[2*i+1] = v, v
		}
		p.AnalyzeFarEnd(stereo, rate, 2)
		for i := 0; i < frame; i++ {
			v := echo[f*frame+i] + s.nearSp[f*frame+i] + s.noise*s.rng.NormFloat64()
			pcm[i] = toS16(v)
			s.in = append(s.in, float64(pcm[i])/32768)
		}
		p.ProcessNearEnd(pcm)
		for i := 0; i < frame; i++ {
			s.out = append(s.out, float64(pcm[i])/32768)
		}
	}
	return s, s
}

// convolve returns the first len(x) samples of x*h (overlap-add with the package's FFT).
func convolve(x, h []float64) []float64 {
	const blk = 32768
	size := 1
	for size < blk+len(h) {
		size <<= 1
	}
	f := newFFT(size)
	H := make([]complex128, size)
	for i, v := range h {
		H[i] = complex(v, 0)
	}
	f.transform(H, false)
	out := make([]float64, len(x)+len(h))
	buf := make([]complex128, size)
	for start := 0; start < len(x); start += blk {
		clear(buf)
		for i := 0; i < blk && start+i < len(x); i++ {
			buf[i] = complex(x[start+i], 0)
		}
		f.transform(buf, false)
		for i := range buf {
			buf[i] *= H[i]
		}
		f.transform(buf, true)
		for i := 0; i < size && start+i < len(out); i++ {
			out[start+i] += real(buf[i])
		}
	}
	return out[:len(x)]
}

func rms(x []float64) float64 { return math.Sqrt(energy(x) / math.Max(float64(len(x)), 1)) }

func db(r float64) float64 { return 20 * math.Log10(math.Max(r, 1e-9)) }

// TestEchoCancellation: far-end speech played in a room with a 70 ms echo path, nobody speaking near. After the filter
// converged the echo must be well below what the microphone heard.
func TestEchoCancellation(t *testing.T) {
	p := New()
	p.SetOptions(Options{EchoCancellation: true, NoiseSuppression: false})
	farOn := func(tt float64) bool { return true }
	s, _ := run(t, p, 40, farOn, func(float64) bool { return false }, room(7, 70*48+137, 0.5), 3000)
	// measure over the last 10 s
	lo := len(s.out) - 10*rate
	inR, outR := rms(s.in[lo:]), rms(s.out[lo:])
	erle := db(inR) - db(outR)
	t.Logf("echo only: microphone %.1f dBFS, after processing %.1f dBFS, ERLE %.1f dB", db(inR), db(outR), erle)
	if erle < 15 {
		t.Errorf("ERLE %.1f dB, want at least 15", erle)
	}
}

// TestEchoCancellationWithoutDelayPrior has the playback buffer far from the guess (echo comes 400 ms after the far
// frame was handed over).
func TestEchoCancellationFarOffset(t *testing.T) {
	p := New()
	p.SetOptions(Options{EchoCancellation: true})
	s, _ := run(t, p, 45, func(float64) bool { return true }, func(float64) bool { return false }, room(11, 400*48, 0.45), 0)
	lo := len(s.out) - 10*rate
	erle := db(rms(s.in[lo:])) - db(rms(s.out[lo:]))
	t.Logf("echo only, 400 ms path: ERLE %.1f dB", erle)
	if erle < 12 {
		t.Errorf("ERLE %.1f dB, want at least 12", erle)
	}
}

// TestDoubleTalk: the near speaker talks while the far end does too. The near speech must survive; the echo must
// still go down.
func TestDoubleTalk(t *testing.T) {
	p := New()
	p.SetOptions(Options{EchoCancellation: true, NoiseSuppression: true})
	nearAfter := func(tt float64) bool { return tt > 30 } // near speaker joins at 30 s
	s, _ := run(t, p, 50, func(float64) bool { return true }, nearAfter, room(5, 60*48, 0.5), 2000)
	lo := 32 * rate
	// the near speech in the output: find the delay of the processing chain by correlation
	ref := s.nearSp[lo:len(s.out)]
	best, bestC := 0, -2.0
	for lag := 0; lag < 3000; lag += 8 {
		var cv, a, b float64
		for i := 0; i+lag < len(ref) && i < 5*rate; i += 2 {
			x, y := ref[i], s.out[lo+i+lag]
			cv += x * y
			a += x * x
			b += y * y
		}
		if c := cv / math.Sqrt(a*b+1e-12); c > bestC {
			best, bestC = lag, c
		}
	}
	// regress the output on the near speech and look at what is left
	var num, den float64
	for i := 0; i+best < len(ref); i++ {
		num += ref[i] * s.out[lo+i+best]
		den += ref[i] * ref[i]
	}
	g := num / den
	var resid, tot float64
	for i := 0; i+best < len(ref); i++ {
		d := s.out[lo+i+best] - g*ref[i]
		resid += d * d
		tot += s.out[lo+i+best] * s.out[lo+i+best]
	}
	t.Logf("energies over the double-talk part: near speech %.1f dB, echo %.1f dB, microphone %.1f dB, output %.1f dB", db(rms(ref)), db(rms(s.echo[lo:len(s.out)])), db(rms(s.in[lo:])), db(rms(s.out[lo:])))
	t.Logf("double talk: output correlates %.2f with the near speech (delay %d samples, gain %.2f), unexplained share %.1f dB", bestC, best, g, 10*math.Log10(resid/tot))
	if bestC < 0.7 {
		t.Errorf("near speech correlation %.2f, want at least 0.7", bestC)
	}
	if g < 0.35 {
		t.Errorf("near speech was attenuated to gain %.2f", g)
	}
}

// TestNoiseSuppression: a constant noise floor with speech bursts; the pauses must get quieter, the bursts not.
func TestNoiseSuppression(t *testing.T) {
	p := New()
	p.SetOptions(Options{NoiseSuppression: true})
	rng := rand.New(rand.NewSource(9))
	sp := &speechLike{r: rand.New(rand.NewSource(4)), rate: 1.5, level: 0.2}
	n := 20 * rate
	in := make([]float64, 0, n)
	out := make([]float64, 0, n)
	pcm := make([]int16, frame)
	var clean []float64
	for f := 0; f*frame < n; f++ {
		for i := 0; i < frame; i++ {
			tt := float64(f*frame+i) / rate
			s := sp.next(tt)
			clean = append(clean, s)
			pcm[i] = toS16(s + 0.02*rng.NormFloat64()) // -34 dBFS rms noise
			in = append(in, float64(pcm[i])/32768)
		}
		p.ProcessNearEnd(pcm)
		for i := 0; i < frame; i++ {
			out = append(out, float64(pcm[i])/32768)
		}
	}
	// pauses and bursts by the clean signal's short-time level (a sample value near zero says nothing: waveforms cross zero)
	var pauseIn, pauseOut, burstIn, burstOut float64
	var np, nb int
	lo := 10 * rate
	const w = 480
	for i := lo; i < n-2000; i += w {
		lvl := rms(clean[i : i+w])
		in2, out2 := energy(in[i:i+w]), energy(out[i+768:i+768+w])
		if lvl < 0.004 && rms(clean[i-w:i]) < 0.004 && rms(clean[i+w:i+2*w]) < 0.004 {
			pauseIn += in2
			pauseOut += out2
			np += w
		} else if lvl > 0.06 {
			burstIn += in2
			burstOut += out2
			nb += w
		}
	}
	pause := 10 * math.Log10(pauseOut/pauseIn)
	burst := 10 * math.Log10(burstOut/burstIn)
	t.Logf("noise suppression: pauses %.1f dB, speech bursts %.1f dB (%d pause samples, %d burst samples)", pause, burst, np, nb)
	if np < 1000 || nb < 1000 {
		t.Fatalf("the test signal has too few pauses or bursts (%d, %d)", np, nb)
	}
	if pause > -8 {
		t.Errorf("pauses only %.1f dB quieter, want at least 8 dB", pause)
	}
	if burst < -4 {
		t.Errorf("speech bursts attenuated by %.1f dB, want less than 4 dB", -burst)
	}
}

func TestAGC(t *testing.T) {
	p := New()
	p.SetOptions(Options{AutoGain: true})
	sp := &speechLike{r: rand.New(rand.NewSource(8)), rate: 3, level: 0.01}
	pcm := make([]int16, frame)
	var outR []float64
	for f := 0; f < 1500; f++ {
		for i := 0; i < frame; i++ {
			pcm[i] = toS16(sp.next(float64(f*frame+i) / rate))
		}
		p.ProcessNearEnd(pcm)
		if f > 1200 {
			for _, v := range pcm {
				outR = append(outR, float64(v)/32768)
			}
		}
	}
	r := rms(outR)
	t.Logf("quiet speech (about -45 dBFS) after AGC: %.1f dBFS", db(r))
	if db(r) < -36 {
		t.Errorf("AGC left the speech at %.1f dBFS", db(r))
	}
}

func TestPassthroughWhenOff(t *testing.T) {
	p := New()
	p.SetOptions(Options{})
	pcm := make([]int16, frame)
	for i := range pcm {
		pcm[i] = int16(i * 7)
	}
	want := append([]int16(nil), pcm...)
	p.ProcessNearEnd(pcm)
	for i := range pcm {
		if pcm[i] != want[i] {
			t.Fatalf("sample %d changed", i)
		}
	}
}

func BenchmarkProcessFrame(b *testing.B) {
	p := New()
	pcm := make([]int16, frame)
	st := make([]int16, frame*2)
	rng := rand.New(rand.NewSource(1))
	for i := range pcm {
		pcm[i] = int16(rng.Intn(4000) - 2000)
	}
	for i := range st {
		st[i] = int16(rng.Intn(4000) - 2000)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p.AnalyzeFarEnd(st, rate, 2)
		p.ProcessNearEnd(pcm)
	}
}
