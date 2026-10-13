package rtcmedia

import (
	"math"
	"sync"
)

// AudioSink consumes decoded PCM (interleaved signed 16-bit). internal/audioout adapts its output to this.
// Write must copy what it keeps and should not block for long.
type AudioSink interface {
	Write(pcm []int16, rate, channels int)
}

// NullSink discards audio.
type NullSink struct{}

func (NullSink) Write([]int16, int, int) {}

// RecordSink keeps the most recent audio in memory (tests, diagnostics).
type RecordSink struct {
	mu       sync.Mutex
	Max      int // maximum samples kept (interleaved); 0 means 10 seconds at 48 kHz stereo
	pcm      []int16
	rate, ch int
	total    int
}

func (r *RecordSink) Write(pcm []int16, rate, channels int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	max := r.Max
	if max == 0 {
		max = 48000 * 2 * 10
	}
	r.rate, r.ch = rate, channels
	r.total += len(pcm)
	r.pcm = append(r.pcm, pcm...)
	if len(r.pcm) > max {
		r.pcm = append(r.pcm[:0], r.pcm[len(r.pcm)-max:]...)
	}
}

// Snapshot returns a copy of the kept samples and their format.
func (r *RecordSink) Snapshot() (pcm []int16, rate, channels int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int16(nil), r.pcm...), r.rate, r.ch
}

// Total is the number of interleaved samples written so far.
func (r *RecordSink) Total() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.total
}

// AudioProcessor is the hook for acoustic echo cancellation and noise suppression. Sweef ships none
// (use headphones); an implementation receives the far-end signal (what is about to be played) and
// cleans the near-end signal (what the microphone captured) in place, 20 ms at a time at 48 kHz mono.
type AudioProcessor interface {
	AnalyzeFarEnd(pcm []int16, rate, channels int)
	ProcessNearEnd(pcm []int16)
}

// ---- resampling ---------------------------------------------------------------------------------

// Resampler converts mono float32 audio between sample rates with a windowed-sinc kernel, keeping its
// state across calls.
type Resampler struct {
	in, out int
	step    float64 // input samples per output sample
	pos     float64 // position of the next output sample in hist
	hist    []float32
	kernel  []float32 // phases x taps
	taps    int
	phases  int
	cutoff  float64
}

const resamplePhases = 256

// NewResampler returns a resampler from rate in to rate out.
func NewResampler(in, out int) *Resampler {
	r := &Resampler{in: in, out: out, step: float64(in) / float64(out), phases: resamplePhases}
	r.cutoff = math.Min(1, float64(out)/float64(in)) * 0.95
	half := 8
	if r.cutoff < 1 {
		half = int(math.Ceil(8 / r.cutoff))
	}
	r.taps = 2 * half
	r.kernel = make([]float32, r.phases*r.taps)
	for p := 0; p < r.phases; p++ {
		frac := float64(p) / float64(r.phases)
		var sum float64
		for t := 0; t < r.taps; t++ {
			x := float64(t-half+1) - frac // distance from the interpolation point, in input samples
			w := 0.5 + 0.5*math.Cos(math.Pi*x/float64(half))
			if math.Abs(x) >= float64(half) {
				w = 0
			}
			v := sinc(x*r.cutoff) * r.cutoff * w
			r.kernel[p*r.taps+t] = float32(v)
			sum += v
		}
		for t := 0; t < r.taps; t++ { // unity DC gain
			r.kernel[p*r.taps+t] /= float32(sum)
		}
	}
	r.hist = make([]float32, half-1)
	r.pos = float64(half - 1)
	return r
}

func sinc(x float64) float64 {
	if x == 0 {
		return 1
	}
	return math.Sin(math.Pi*x) / (math.Pi * x)
}

// Process appends the resampled input to dst and returns it.
func (r *Resampler) Process(dst []float32, src []float32) []float32 {
	if r.in == r.out {
		return append(dst, src...)
	}
	r.hist = append(r.hist, src...)
	half := r.taps / 2
	for {
		i := int(r.pos)
		if i+half >= len(r.hist) {
			break
		}
		frac := r.pos - float64(i)
		ph := int(frac * float64(r.phases))
		k := r.kernel[ph*r.taps : (ph+1)*r.taps]
		base := i - half + 1
		var acc float32
		for t, c := range k {
			acc += c * r.hist[base+t]
		}
		dst = append(dst, acc)
		r.pos += r.step
	}
	// drop consumed history, keeping what the next outputs need
	keep := int(r.pos) - half + 1
	if keep > 0 {
		r.hist = append(r.hist[:0], r.hist[keep:]...)
		r.pos -= float64(keep)
	}
	return dst
}

// ---- level helpers -----------------------------------------------------------------------------

// rmsLevel returns the RMS of the samples as a linear level in [0, 1].
func rmsLevel(pcm []int16) float64 {
	if len(pcm) == 0 {
		return 0
	}
	var s float64
	for _, v := range pcm {
		f := float64(v)
		s += f * f
	}
	return math.Sqrt(s/float64(len(pcm))) / 32768
}

// dBov converts a linear level to the audio level header value: 0 (full scale) to 127 (silence).
func dBov(level float64) int {
	if level <= 0.000001 {
		return 127
	}
	db := -20 * math.Log10(level)
	if db > 127 {
		return 127
	}
	if db < 0 {
		return 0
	}
	return int(db)
}
