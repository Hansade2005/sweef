package mediarec

import "math"

// Resampler converts interleaved float32 PCM from one sample rate to another, in a stream: windowed-sinc
// interpolation (a Hann-windowed lowpass at the lower of the two Nyquist rates, 8 zero crossings wide), with the
// weights normalised per output sample so a constant input stays constant. It keeps only the few samples of
// history it needs.
type Resampler struct {
	ch       int
	step     float64   // input samples per output sample
	cut      float64   // lowpass cutoff relative to the input Nyquist (1 when upsampling)
	half     int       // kernel half width in input samples
	in       []float32 // pending input, interleaved; in[0] is input sample index base
	base     int64
	pos      float64 // input position of the next output sample
	identity bool
}

const (
	rsZeroCross = 8
	rsRes       = 256
)

var rsTable = func() []float32 {
	t := make([]float32, rsZeroCross*rsRes+2)
	for i := range t {
		x := float64(i) / rsRes
		s := 1.0
		if x != 0 {
			s = math.Sin(math.Pi*x) / (math.Pi * x)
		}
		w := 0.5 + 0.5*math.Cos(math.Pi*x/rsZeroCross)
		if x >= rsZeroCross {
			w = 0
		}
		t[i] = float32(s * w)
	}
	return t
}()

// NewResampler returns a resampler for ch (1 or 2) interleaved channels.
func NewResampler(inRate, outRate, ch int) *Resampler {
	r := &Resampler{ch: ch, step: float64(inRate) / float64(outRate), cut: 1, identity: inRate == outRate}
	if outRate < inRate {
		r.cut = float64(outRate) / float64(inRate)
	}
	r.half = int(math.Ceil(rsZeroCross/r.cut)) + 1
	// start with half a kernel of silence in front so the first output sample is centred on input sample 0
	r.in = make([]float32, r.half*ch)
	r.base = -int64(r.half)
	return r
}

func (r *Resampler) kernel(d float64) float32 { // d in input samples
	x := math.Abs(d) * r.cut * rsRes
	i := int(x)
	if i >= rsZeroCross*rsRes {
		return 0
	}
	f := float32(x - float64(i))
	return rsTable[i]*(1-f) + rsTable[i+1]*f
}

// Process appends in (interleaved) and returns the output samples that are now complete.
func (r *Resampler) Process(in []float32) []float32 {
	if r.identity {
		return append([]float32(nil), in...)
	}
	r.in = append(r.in, in...)
	ch := r.ch
	have := r.base + int64(len(r.in)/ch) // one past the last input index
	out := make([]float32, 0, int(float64(len(in)/ch)/r.step+2)*ch)
	for {
		i0 := int64(math.Floor(r.pos))
		if i0+int64(r.half) >= have {
			break
		}
		var sum float32
		var acc [2]float32
		for i := i0 - int64(r.half) + 1; i <= i0+int64(r.half); i++ {
			w := r.kernel(float64(i) - r.pos)
			if w == 0 {
				continue
			}
			o := int(i-r.base) * ch
			if o < 0 {
				continue
			}
			sum += w
			for c := 0; c < ch; c++ {
				acc[c] += w * r.in[o+c]
			}
		}
		if sum == 0 {
			sum = 1
		}
		for c := 0; c < ch; c++ {
			out = append(out, acc[c]/sum)
		}
		r.pos += r.step
	}
	// drop history that no later output sample needs
	keep := int64(math.Floor(r.pos)) - int64(r.half)
	if drop := keep - r.base; drop > 0 {
		r.in = append(r.in[:0], r.in[int(drop)*ch:]...)
		r.base = keep
	}
	return out
}
