package webaudio

import (
	"math"
)

// PeriodicWave mirrors Chromium's band-limited wavetable design: 36 ranges
// (three per octave) of 4096-sample tables, normalised on the first range.
type PeriodicWave struct {
	rate     float64
	tables   [][]float32 // [range][4096]
	lowestF  float64
	size     int
	nRanges  int
	centsPer float64
}

const waveSize = 4096

// NewPeriodicWave builds the tables from Fourier coefficients (real = cos
// terms, imag = sin terms; index 0 ignored).
func NewPeriodicWave(rate float64, real, imag []float64, disableNorm bool) *PeriodicWave {
	w := &PeriodicWave{rate: rate, size: waveSize, nRanges: 36, centsPer: 400}
	w.lowestF = rate / 2 / float64(waveSize/2)
	n := len(real)
	if len(imag) > n {
		n = len(imag)
	}
	if n > waveSize/2 {
		n = waveSize / 2
	}
	norm := 1.0
	plan := newPlan(waveSize)
	for r := 0; r < w.nRanges; r++ {
		maxPartials := int(math.Pow(2, -float64(r)*w.centsPer/1200) * float64(waveSize/2))
		spec := make([]complex128, waveSize)
		for k := 1; k < n; k++ {
			if k > maxPartials {
				break
			}
			var re, im float64
			if k < len(real) {
				re = real[k]
			}
			if k < len(imag) {
				im = imag[k]
			}
			if k == waveSize/2 {
				continue
			}
			// x[t] = re*cos(kωt) + im*sin(kωt)  ->  spectrum bins k and N-k
			spec[k] = complex(re/2, -im/2)
			spec[waveSize-k] = complex(re/2, im/2)
		}
		plan.run(spec, true)
		t := make([]float32, waveSize)
		for i := range t {
			t[i] = float32(real64(spec[i]))
		}
		if r == 0 && !disableNorm {
			var mx float64
			for _, v := range t {
				if a := math.Abs(float64(v)); a > mx {
					mx = a
				}
			}
			if mx > 0 {
				norm = 1 / mx
			}
		}
		if !disableNorm {
			for i := range t {
				t[i] *= float32(norm)
			}
		}
		w.tables = append(w.tables, t)
	}
	return w
}

func real64(c complex128) float64 { return real(c) }

// basicWave returns Fourier coefficients for the standard oscillator types.
func basicCoefficients(kind string) (re, im []float64) {
	n := waveSize / 2
	re = make([]float64, n)
	im = make([]float64, n)
	switch kind {
	case "sine":
		im[1] = 1
	case "square":
		for k := 1; k < n; k++ {
			im[k] = (1 - math.Cos(math.Pi*float64(k))) * 2 / (float64(k) * math.Pi)
		}
	case "sawtooth":
		for k := 1; k < n; k++ {
			s := 1.0
			if k%2 == 0 {
				s = -1
			}
			im[k] = 2 / (float64(k) * math.Pi) * s
		}
	case "triangle":
		for k := 1; k < n; k++ {
			if k%2 == 0 {
				continue
			}
			s := 1.0
			if (k-1)/2%2 == 1 {
				s = -1
			}
			im[k] = 8 / (float64(k) * math.Pi * float64(k) * math.Pi) * s
		}
	}
	return
}

var basicWaves = map[string]*PeriodicWave{}

func basicWave(rate float64, kind string) *PeriodicWave {
	key := kind + string(rune(int(rate)))
	if w, ok := basicWaves[key]; ok {
		return w
	}
	re, im := basicCoefficients(kind)
	w := NewPeriodicWave(rate, re, im, false)
	basicWaves[key] = w
	return w
}

func (w *PeriodicWave) lookup(f float64) (lo, hi []float32, frac float32) {
	f = math.Abs(f)
	ratio := 0.5
	if f > 0 {
		ratio = f / w.lowestF
	}
	cents := math.Log2(ratio) * 1200
	pr := 1 + cents/w.centsPer
	if pr < 0 {
		pr = 0
	}
	if pr > float64(w.nRanges-1) {
		pr = float64(w.nRanges - 1)
	}
	i1 := int(pr)
	i2 := i1
	if i1 < w.nRanges-1 {
		i2 = i1 + 1
	}
	return w.tables[i1], w.tables[i2], float32(pr - float64(i1))
}

type oscProc struct {
	kind  string
	wave  *PeriodicWave
	phase float64 // virtual read index into the table
	init  bool
}

func (o *oscProc) process(n *Node, c *Context, start int64, frames int, in [][][]float32) {
	freq := n.Params["frequency"].compute(c, start, frames)
	det := n.Params["detune"].compute(c, start, frames)
	out := n.setOut(0, 1)
	from, to, ended := n.activeRange(c, start, frames)
	w := o.wave
	if o.kind != "custom" || w == nil {
		k := o.kind
		if k == "custom" {
			k = "sine"
		}
		w = basicWave(c.SampleRate, k)
	}
	rateScale := float64(waveSize) / c.SampleRate
	nyq := c.SampleRate / 2
	for i := from; i < to; i++ {
		f := float64(freq[i])
		if d := float64(det[i]); d != 0 {
			f *= math.Pow(2, d/1200)
		}
		if f > nyq {
			f = nyq
		} else if f < -nyq {
			f = -nyq
		}
		lo, hi, fr := w.lookup(f)
		ri := int(o.phase)
		ri2 := ri + 1
		if ri >= waveSize {
			ri -= waveSize
		}
		if ri2 >= waveSize {
			ri2 -= waveSize
		}
		pf := float32(o.phase - math.Floor(o.phase))
		s1 := lo[ri]*(1-pf) + lo[ri2]*pf
		s2 := hi[ri]*(1-pf) + hi[ri2]*pf
		out[0][i] = (1-fr)*s1 + fr*s2
		o.phase += f * rateScale
		for o.phase >= waveSize {
			o.phase -= waveSize
		}
		for o.phase < 0 {
			o.phase += waveSize
		}
	}
	if ended {
		n.markEnded()
	}
}

// ---- waves registry ------------------------------------------------------------------------

// SetWave registers a custom PeriodicWave under id.
func (c *Context) SetWave(id int, real, imag []float64, disableNorm bool) {
	if c.waves == nil {
		c.waves = map[int]*PeriodicWave{}
	}
	c.waves[id] = NewPeriodicWave(c.SampleRate, real, imag, disableNorm)
}

// ---- AudioBufferSourceNode ---------------------------------------------------------------------

type bufSrcProc struct {
	buf       *Buffer
	loop      bool
	loopStart float64
	loopEnd   float64
	offset    float64
	hasDur    bool
	dur       float64
	pos       float64
	playedSec float64
	begun     bool
	done      bool
}

func (b *bufSrcProc) process(n *Node, c *Context, start int64, frames int, in [][][]float32) {
	rate := n.Params["playbackRate"].compute(c, start, frames)
	det := n.Params["detune"].compute(c, start, frames)
	ch := 1
	if b.buf != nil && len(b.buf.Data) > 0 {
		ch = len(b.buf.Data)
	}
	out := n.setOut(0, ch)
	if b.buf == nil || b.done {
		if b.done {
			return
		}
		_, _, ended := n.activeRange(c, start, frames)
		if ended {
			n.markEnded()
			b.done = true
		}
		return
	}
	from, to, ended := n.activeRange(c, start, frames)
	nf := b.buf.Frames()
	if nf == 0 {
		if from < to {
			b.done = true
			n.markEnded()
		}
		return
	}
	bufRate := b.buf.Rate
	if bufRate == 0 {
		bufRate = c.SampleRate
	}
	ratio := bufRate / c.SampleRate
	if !b.begun && from < to {
		b.begun = true
		b.pos = b.offset * bufRate
		if b.pos < 0 {
			b.pos = 0
		}
	}
	// loop region in frames
	ls, le := 0.0, float64(nf)
	if b.loop {
		if b.loopStart >= 0 {
			ls = b.loopStart * bufRate
		}
		if b.loopEnd > 0 {
			le = b.loopEnd * bufRate
		}
		if !(ls >= 0 && le > ls && le <= float64(nf)) {
			ls, le = 0, float64(nf)
		}
	}
	maxPlay := math.Inf(1)
	if b.hasDur {
		maxPlay = b.dur * bufRate
	}
	for i := from; i < to; i++ {
		if b.loop {
			for b.pos >= le {
				b.pos -= le - ls
			}
			for b.pos < ls && float64(rate[0]) < 0 {
				b.pos += le - ls
			}
		}
		if b.pos >= float64(nf) || b.pos < 0 || b.playedSec >= maxPlay {
			b.done = true
			ended = true
			break
		}
		i0 := int(b.pos)
		f := float32(b.pos - float64(i0))
		for k := 0; k < ch; k++ {
			d := b.buf.Data[k]
			a := d[i0]
			// Chromium interpolates with the true next sample (even past a loop
			// end); at the very end of a non-looping buffer the last sample repeats.
			next := i0 + 1
			if next >= nf {
				if b.loop {
					next = int(ls)
				} else {
					next = nf - 1
				}
			}
			bb := d[next]
			out[k][i] = a + (bb-a)*f
		}
		r := float64(rate[i])
		if dd := float64(det[i]); dd != 0 {
			r *= math.Pow(2, dd/1200)
		}
		step := r * ratio
		b.pos += step
		b.playedSec += math.Abs(step)
	}
	if ended {
		b.done = true
		n.markEnded()
	}
}

// ---- media element clip source ---------------------------------------------------------------------

// clipProc plays a decoded clip at a position set by the host (used for
// MediaElementAudioSourceNode).
type clipProc struct {
	buf     *Buffer
	pos     float64
	playing bool
	rate    float64
	vol     float64
}

func (p *clipProc) process(n *Node, c *Context, start int64, frames int, in [][][]float32) {
	if p.buf == nil || len(p.buf.Data) == 0 {
		n.setOut(0, 1)
		return
	}
	out := n.setOut(0, len(p.buf.Data))
	if !p.playing {
		return
	}
	rate := p.rate
	if rate == 0 {
		rate = 1
	}
	vol := p.vol
	br := p.buf.Rate
	step := rate * br / c.SampleRate
	pos := p.pos * br
	nf := p.buf.Frames()
	for i := 0; i < frames; i++ {
		i0 := int(pos)
		if i0 < 0 || i0 >= nf {
			break
		}
		f := float32(pos - float64(i0))
		for k := range out {
			a := p.buf.Data[k][i0]
			var b float32
			if i0+1 < nf {
				b = p.buf.Data[k][i0+1]
			}
			out[k][i] = (a + (b-a)*f) * float32(vol)
		}
		pos += step
	}
	p.pos = pos / br
}
