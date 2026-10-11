package webaudio

import "math"

type analyserProc struct {
	fftSize   int
	smoothing float64
	minDb     float64
	maxDb     float64
	ring      []float32
	w         int
	mag       []float64
	lastT     float64
	plan      *fftPlan
	haveData  bool
}

const analyserRing = 32768

func newAnalyserProc() *analyserProc {
	a := &analyserProc{fftSize: 2048, smoothing: 0.8, minDb: -100, maxDb: -30, ring: make([]float32, analyserRing), lastT: -1}
	a.mag = make([]float64, 1024)
	return a
}

func (a *analyserProc) setFFT(n int) {
	a.fftSize = n
	a.mag = make([]float64, n/2)
	a.plan = nil
	a.lastT = -1
}

func (a *analyserProc) process(n *Node, c *Context, start int64, frames int, in [][][]float32) {
	out := n.setOut(0, len(in[0]))
	for ch := range out {
		copy(out[ch], in[0][ch])
	}
	m := mixTo(in[0], 1, "speakers")[0]
	for i := 0; i < frames; i++ {
		a.ring[(a.w+i)%analyserRing] = m[i]
	}
	a.w = (a.w + frames) % analyserRing
	a.haveData = true
}

func (a *analyserProc) last(count int) []float32 {
	out := make([]float32, count)
	for i := 0; i < count; i++ {
		out[i] = a.ring[((a.w-count+i)%analyserRing+analyserRing)%analyserRing]
	}
	return out
}

func (a *analyserProc) analyse(c *Context) {
	t := c.Time()
	if t <= a.lastT {
		return
	}
	a.lastT = t
	N := a.fftSize
	if a.plan == nil {
		a.plan = newPlan(N)
	}
	data := a.last(N)
	alpha := 0.16
	a0 := (1 - alpha) / 2
	buf := make([]complex128, N)
	for i := 0; i < N; i++ {
		w := a0 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(N)) + alpha/2*math.Cos(4*math.Pi*float64(i)/float64(N))
		buf[i] = complex(float64(data[i])*w, 0)
	}
	a.plan.run(buf, false)
	s := a.smoothing
	for i := 0; i < N/2; i++ {
		m := math.Hypot(real(buf[i]), imag(buf[i])) / float64(N)
		if math.IsNaN(m) {
			m = 0
		}
		a.mag[i] = s*a.mag[i] + (1-s)*m
	}
}

// AnalyserFloatTime returns the latest fftSize samples.
func (c *Context) AnalyserFloatTime(id int) []float32 {
	a, ok := c.byID[id].proc.(*analyserProc)
	if !ok {
		return nil
	}
	return a.last(a.fftSize)
}

// AnalyserFloatFreq returns dB magnitudes.
func (c *Context) AnalyserFloatFreq(id int) []float32 {
	a, ok := c.byID[id].proc.(*analyserProc)
	if !ok {
		return nil
	}
	a.analyse(c)
	out := make([]float32, len(a.mag))
	for i, m := range a.mag {
		if m <= 0 {
			out[i] = float32(math.Inf(-1))
		} else {
			out[i] = float32(20 * math.Log10(m))
		}
	}
	return out
}

// AnalyserByteFreq scales dB magnitudes into 0..255.
func (c *Context) AnalyserByteFreq(id int) []uint8 {
	a, ok := c.byID[id].proc.(*analyserProc)
	if !ok {
		return nil
	}
	db := c.AnalyserFloatFreq(id)
	out := make([]uint8, len(db))
	rng := a.maxDb - a.minDb
	for i, d := range db {
		v := 255 / rng * (float64(d) - a.minDb)
		if math.IsNaN(v) || v < 0 {
			v = 0
		}
		if v > 255 {
			v = 255
		}
		out[i] = uint8(v)
	}
	return out
}

// AnalyserByteTime maps samples to 0..255 around 128.
func (c *Context) AnalyserByteTime(id int) []uint8 {
	a, ok := c.byID[id].proc.(*analyserProc)
	if !ok {
		return nil
	}
	f := a.last(a.fftSize)
	out := make([]uint8, len(f))
	for i, x := range f {
		v := math.Floor(128 * (1 + float64(x)))
		if v < 0 {
			v = 0
		}
		if v > 255 {
			v = 255
		}
		out[i] = uint8(v)
	}
	return out
}
