package webaudio

import (
	"fmt"
	"math"
)

func optF(o map[string]any, k string, d float64) float64 {
	switch v := o[k].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case bool:
		if v {
			return 1
		}
		return 0
	}
	return d
}

func optS(o map[string]any, k, d string) string {
	if v, ok := o[k].(string); ok {
		return v
	}
	return d
}

// AddNode creates a node of the given type (lower-case names such as
// "gain", "oscillator", "biquad"). It returns the node id.
func (c *Context) AddNode(typ string, o map[string]any) (int, error) {
	if o == nil {
		o = map[string]any{}
	}
	nyq := c.SampleRate / 2
	const big = 3.4028234663852886e38
	var n *Node
	switch typ {
	case "destination":
		n = c.newNode(typ, 1, 1)
		n.ChCount = int(optF(o, "channels", 2))
		n.ChMode = "explicit"
		n.proc = passProc{}
	case "gain":
		n = c.newNode(typ, 1, 1)
		n.addParam("gain", 1, -big, big, true)
		n.proc = gainProc{}
	case "constantsource":
		n = c.newNode(typ, 0, 1)
		n.addParam("offset", 1, -big, big, true)
		n.proc = &constProc{}
	case "oscillator":
		n = c.newNode(typ, 0, 1)
		n.addParam("frequency", 440, -nyq, nyq, true)
		n.addParam("detune", 0, -153600, 153600, true)
		n.proc = &oscProc{kind: "sine"}
	case "buffersource":
		n = c.newNode(typ, 0, 1)
		n.addParam("playbackRate", 1, -big, big, false)
		n.addParam("detune", 0, -big, big, false)
		n.proc = &bufSrcProc{}
	case "stereopanner":
		n = c.newNode(typ, 1, 1)
		n.ChCount, n.ChMode = 2, "clamped-max"
		n.addParam("pan", 0, -1, 1, true)
		n.proc = panStereoProc{}
	case "biquad":
		n = c.newNode(typ, 1, 1)
		n.addParam("frequency", 350, 0, nyq, true)
		n.addParam("detune", 0, -153600, 153600, true)
		n.addParam("Q", 1, -big, big, true)
		n.addParam("gain", 0, -big, 1541, true)
		n.proc = &biquadProc{kind: "lowpass"}
	case "iir":
		n = c.newNode(typ, 1, 1)
		ff, fb := floats(o["feedforward"]), floats(o["feedback"])
		n.proc = newIIRProc(ff, fb)
	case "delay":
		maxD := optF(o, "maxDelayTime", 1)
		n = c.newNode(typ, 1, 1)
		n.addParam("delayTime", 0, 0, maxD, true)
		n.proc = &delayProc{max: maxD}
	case "waveshaper":
		n = c.newNode(typ, 1, 1)
		n.proc = &shaperProc{over: "none"}
	case "convolver":
		n = c.newNode(typ, 1, 1)
		n.ChCount, n.ChMode = 2, "clamped-max"
		n.proc = &convProc{normalize: true}
	case "compressor":
		n = c.newNode(typ, 1, 1)
		n.ChCount, n.ChMode = 2, "clamped-max"
		n.addParam("threshold", -24, -100, 0, false)
		n.addParam("knee", 30, 0, 40, false)
		n.addParam("ratio", 12, 1, 20, false)
		n.addParam("attack", 0.003, 0, 1, false)
		n.addParam("release", 0.25, 0, 1, false)
		n.proc = newCompProc(c.SampleRate)
	case "analyser":
		n = c.newNode(typ, 1, 1)
		n.proc = newAnalyserProc()
	case "channelsplitter":
		outs := int(optF(o, "outputs", 6))
		n = c.newNode(typ, 1, outs)
		n.ChCount, n.ChMode, n.ChInterp = outs, "explicit", "discrete"
		n.proc = splitProc{}
	case "channelmerger":
		ins := int(optF(o, "inputs", 6))
		n = c.newNode(typ, ins, 1)
		n.ChCount, n.ChMode = 1, "explicit"
		n.proc = mergeProc{}
	case "panner":
		n = c.newNode(typ, 1, 1)
		n.ChCount, n.ChMode = 2, "clamped-max"
		for _, a := range []string{"positionX", "positionY", "positionZ"} {
			n.addParam(a, 0, -big, big, true)
		}
		n.addParam("orientationX", 1, -big, big, true)
		n.addParam("orientationY", 0, -big, big, true)
		n.addParam("orientationZ", 0, -big, big, true)
		n.proc = &pannerProc{distModel: "inverse", ref: 1, max: 10000, rolloff: 1, innerAngle: 360, outerAngle: 360}
	case "mediaelement":
		n = c.newNode(typ, 0, 1)
		n.proc = &clipProc{}
	case "mediastreamsource":
		n = c.newNode(typ, 0, 1)
		n.proc = silenceProc{}
	case "mediastreamdest":
		n = c.newNode(typ, 1, 1)
		n.ChCount, n.ChMode = 2, "explicit"
		n.proc = passProc{}
	case "external":
		nin := int(optF(o, "inputs", 1))
		nout := int(optF(o, "outputs", 1))
		n = c.newNode(typ, nin, nout)
		n.external = true
		n.ChMode = "max"
		if nout == 0 {
			n.nOut = 0
			n.outBufs = nil
		}
		n.proc = nopProc{}
		c.dirty = true
	default:
		return 0, fmt.Errorf("unknown node type %q", typ)
	}
	for k, v := range o {
		_ = k
		_ = v
	}
	return n.ID, nil
}

func floats(v any) []float64 {
	arr, _ := v.([]any)
	out := make([]float64, len(arr))
	for i, x := range arr {
		switch f := x.(type) {
		case float64:
			out[i] = f
		case int:
			out[i] = float64(f)
		}
	}
	return out
}

// Set assigns a non-AudioParam attribute (type, loop, curve, ...).
func (c *Context) Set(id int, key string, v any) error {
	n := c.byID[id]
	if n == nil {
		return fmt.Errorf("bad node")
	}
	switch key {
	case "channelCount":
		n.ChCount = int(optF(map[string]any{"v": v}, "v", 2))
		return nil
	case "channelCountMode":
		n.ChMode, _ = v.(string)
		return nil
	case "channelInterpretation":
		n.ChInterp, _ = v.(string)
		return nil
	}
	switch p := n.proc.(type) {
	case *oscProc:
		switch key {
		case "type":
			p.kind, _ = v.(string)
		case "wave":
			p.wave = c.waves[int(optF(map[string]any{"v": v}, "v", 0))]
		}
	case *bufSrcProc:
		f := optF(map[string]any{"v": v}, "v", 0)
		switch key {
		case "buffer":
			p.buf = c.Buffers[int(f)]
		case "loop":
			p.loop = f != 0
		case "loopStart":
			p.loopStart = f
		case "loopEnd":
			p.loopEnd = f
		}
	case *biquadProc:
		if key == "type" {
			p.kind, _ = v.(string)
		}
	case *shaperProc:
		switch key {
		case "curve":
			if v == nil {
				p.curve = nil
			} else {
				p.curve = toF32(v)
			}
		case "oversample":
			p.over, _ = v.(string)
		}
	case *convProc:
		switch key {
		case "buffer":
			if v == nil {
				p.setBuffer(nil)
			} else {
				p.setBuffer(c.Buffers[int(optF(map[string]any{"v": v}, "v", 0))])
			}
			p.rate = c.SampleRate
		case "normalize":
			p.normalize = optF(map[string]any{"v": v}, "v", 1) != 0
			p.rebuild()
		}
	case *delayProc:
	case *analyserProc:
		f := optF(map[string]any{"v": v}, "v", 0)
		switch key {
		case "fftSize":
			p.setFFT(int(f))
		case "smoothing":
			p.smoothing = f
		case "minDecibels":
			p.minDb = f
		case "maxDecibels":
			p.maxDb = f
		}
	case *pannerProc:
		if s, ok := v.(string); ok {
			switch key {
			case "distanceModel":
				p.distModel = s
			case "panningModel":
				p.hrtf = s == "HRTF"
			}
		} else {
			f := optF(map[string]any{"v": v}, "v", 0)
			switch key {
			case "refDistance":
				p.ref = f
			case "maxDistance":
				p.max = f
			case "rolloffFactor":
				p.rolloff = f
			case "coneInnerAngle":
				p.innerAngle = f
			case "coneOuterAngle":
				p.outerAngle = f
			case "coneOuterGain":
				p.outerGain = f
			}
		}
	case *clipProc:
		f := optF(map[string]any{"v": v}, "v", 0)
		switch key {
		case "clip":
			p.buf = c.Buffers[int(f)]
		case "pos":
			p.pos = f
		case "playing":
			p.playing = f != 0
		case "rate":
			p.rate = f
		case "volume":
			p.vol = f
		}
	case *compProc:
	}
	return nil
}

func toF32(v any) []float32 {
	arr, _ := v.([]any)
	out := make([]float32, len(arr))
	for i, x := range arr {
		switch f := x.(type) {
		case float64:
			out[i] = float32(f)
		case int:
			out[i] = float32(f)
		}
	}
	return out
}

// Start/Stop schedule a source node.
func (c *Context) Start(id int, when, offset, duration float64, hasDur bool) error {
	n := c.byID[id]
	if n == nil {
		return fmt.Errorf("bad node")
	}
	if n.started {
		return fmt.Errorf("InvalidStateError")
	}
	n.started = true
	n.startT = when
	n.stopT = math.Inf(1)
	if p, ok := n.proc.(*bufSrcProc); ok {
		p.offset = offset
		p.hasDur = hasDur
		p.dur = duration
	}
	return nil
}

func (c *Context) Stop(id int, when float64) error {
	n := c.byID[id]
	if n == nil {
		return fmt.Errorf("bad node")
	}
	if !n.started {
		return fmt.Errorf("InvalidStateError")
	}
	n.stopT = when
	return nil
}

// activeRange gives the [from,to) frames of the quantum where a scheduled
// source produces sound; ended is set once the stop time has passed.
func (n *Node) activeRange(c *Context, start int64, frames int) (from, to int, ended bool) {
	if !n.started {
		return 0, 0, false
	}
	sf := int64(math.Ceil(n.startT*c.SampleRate - 1e-6))
	if sf < 0 {
		sf = 0
	}
	if start+int64(frames) <= sf {
		return 0, 0, false
	}
	from = 0
	if sf > start {
		from = int(sf - start)
	}
	to = frames
	if !math.IsInf(n.stopT, 1) {
		ef := int64(math.Ceil(n.stopT*c.SampleRate - 1e-6))
		if ef <= start {
			return 0, 0, true
		}
		if ef < start+int64(frames) {
			to = int(ef - start)
			ended = true
		}
	}
	return
}

func (n *Node) markEnded() {
	if n.endedFlag {
		return
	}
	n.endedFlag = true
	n.ctx.Ended = append(n.ctx.Ended, n.ID)
}

// ---- simple processors --------------------------------------------------------

type nopProc struct{}

func (nopProc) process(n *Node, c *Context, start int64, frames int, in [][][]float32) {}

type silenceProc struct{}

func (silenceProc) process(n *Node, c *Context, start int64, frames int, in [][][]float32) {
	n.setOut(0, 1)
}

type passProc struct{}

func (passProc) process(n *Node, c *Context, start int64, frames int, in [][][]float32) {
	out := n.setOut(0, len(in[0]))
	for ch := range out {
		copy(out[ch], in[0][ch])
	}
}

type gainProc struct{}

func (gainProc) process(n *Node, c *Context, start int64, frames int, in [][][]float32) {
	g := n.Params["gain"].compute(c, start, frames)
	out := n.setOut(0, len(in[0]))
	for ch := range out {
		src := in[0][ch]
		for i := 0; i < frames; i++ {
			out[ch][i] = src[i] * g[i]
		}
	}
}

type constProc struct{}

func (constProc) process(n *Node, c *Context, start int64, frames int, in [][][]float32) {
	off := n.Params["offset"].compute(c, start, frames)
	out := n.setOut(0, 1)
	from, to, ended := n.activeRange(c, start, frames)
	for i := from; i < to; i++ {
		out[0][i] = off[i]
	}
	if ended {
		n.markEnded()
	}
}

type splitProc struct{}

func (splitProc) process(n *Node, c *Context, start int64, frames int, in [][][]float32) {
	for o := 0; o < n.nOut; o++ {
		out := n.setOut(o, 1)
		if o < len(in[0]) {
			copy(out[0], in[0][o])
		}
	}
}

type mergeProc struct{}

func (mergeProc) process(n *Node, c *Context, start int64, frames int, in [][][]float32) {
	out := n.setOut(0, n.nIn)
	for i := 0; i < n.nIn; i++ {
		if len(in[i]) > 0 {
			// each input is mixed down to mono
			m := in[i][0]
			copy(out[i], m)
		}
	}
}

type panStereoProc struct{}

func (panStereoProc) process(n *Node, c *Context, start int64, frames int, in [][][]float32) {
	pan := n.Params["pan"].compute(c, start, frames)
	out := n.setOut(0, 2)
	src := in[0]
	for i := 0; i < frames; i++ {
		p := float64(pan[i])
		if p < -1 {
			p = -1
		} else if p > 1 {
			p = 1
		}
		if len(src) == 1 {
			x := (p + 1) / 2
			out[0][i] = src[0][i] * float32(math.Cos(x*math.Pi/2))
			out[1][i] = src[0][i] * float32(math.Sin(x*math.Pi/2))
		} else {
			l, r := src[0][i], src[1][i]
			if p <= 0 {
				x := p + 1
				out[0][i] = l + r*float32(math.Cos(x*math.Pi/2))
				out[1][i] = r * float32(math.Sin(x*math.Pi/2))
			} else {
				x := p
				out[0][i] = l * float32(math.Cos(x*math.Pi/2))
				out[1][i] = r + l*float32(math.Sin(x*math.Pi/2))
			}
		}
	}
}

// ---- delay -------------------------------------------------------------------------

type delayProc struct {
	max  float64
	ring [][]float32
	w    int
	size int
}

func (d *delayProc) ensure(c *Context, ch int) {
	if d.size == 0 {
		d.size = int(math.Ceil(d.max*c.SampleRate)) + quantum + 2
	}
	for len(d.ring) < ch {
		d.ring = append(d.ring, make([]float32, d.size))
	}
}

func (d *delayProc) process(n *Node, c *Context, start int64, frames int, in [][][]float32) {
	ch := len(in[0])
	if n.cyclic {
		ch = 2
		if len(d.ring) > 0 {
			ch = len(d.ring)
		}
	}
	d.ensure(c, ch)
	dt := n.Params["delayTime"].compute(c, start, frames)
	out := n.setOut(0, len(d.ring))
	w0 := d.w
	for i := 0; i < frames; i++ {
		dly := float64(dt[i]) * c.SampleRate
		if n.cyclic && dly < quantum {
			dly = quantum
		}
		if dly > float64(d.size-quantum-1) {
			dly = float64(d.size - quantum - 1)
		}
		for k := 0; k < len(d.ring); k++ {
			if !n.cyclic && k < len(in[0]) {
				d.ring[k][(w0+i)%d.size] = in[0][k][i]
			}
			pos := float64(w0+i) - dly
			for pos < 0 {
				pos += float64(d.size)
			}
			i0 := int(math.Floor(pos))
			f := float32(pos - float64(i0))
			a := d.ring[k][i0%d.size]
			b := d.ring[k][(i0+1)%d.size]
			out[k][i] = a + (b-a)*f
		}
	}
	if !n.cyclic {
		d.w = (w0 + frames) % d.size
	}
}

// post writes the input of a feedback delay after the rest of the graph ran.
func (d *delayProc) post(n *Node, c *Context, start int64, in [][][]float32) {
	d.ensure(c, len(in[0]))
	for k := 0; k < len(d.ring) && k < len(in[0]); k++ {
		for i := 0; i < quantum; i++ {
			d.ring[k][(d.w+i)%d.size] = in[0][k][i]
		}
	}
	d.w = (d.w + quantum) % d.size
}

// ---- wave shaper ------------------------------------------------------------------------

type shaperProc struct {
	curve []float32
	over  string
	up    [][]float32
	st    [][]float64
}

func shape(curve []float32, x float32) float32 {
	if len(curve) == 0 {
		return x
	}
	N := float32(len(curve))
	v := (N - 1) / 2 * (x + 1)
	if v <= 0 {
		return curve[0]
	}
	if v >= N-1 {
		return curve[len(curve)-1]
	}
	k := int(v)
	f := v - float32(k)
	return (1-f)*curve[k] + f*curve[k+1]
}

func (s *shaperProc) process(n *Node, c *Context, start int64, frames int, in [][][]float32) {
	out := n.setOut(0, len(in[0]))
	if len(s.curve) == 0 {
		for ch := range out {
			copy(out[ch], in[0][ch])
		}
		return
	}
	factor := 1
	switch s.over {
	case "2x":
		factor = 2
	case "4x":
		factor = 4
	}
	if factor == 1 {
		for ch := range out {
			for i := 0; i < frames; i++ {
				out[ch][i] = shape(s.curve, in[0][ch][i])
			}
		}
		return
	}
	for len(s.st) < len(out) {
		s.st = append(s.st, nil)
	}
	for ch := range out {
		out[ch] = out[ch][:frames]
		os := overshape(s, ch, in[0][ch][:frames], factor)
		copy(out[ch], os)
	}
}

// overshape applies the curve at factor× the sample rate using a windowed
// sinc interpolator/decimator (zero-latency approximation of Chromium's).
func overshape(s *shaperProc, ch int, x []float32, f int) []float32 {
	const taps = 16
	// history of the previous taps samples for continuity
	if s.st[ch] == nil {
		s.st[ch] = make([]float64, taps*2)
	}
	hist := s.st[ch]
	ext := make([]float64, len(hist)+len(x))
	copy(ext, hist)
	for i, v := range x {
		ext[len(hist)+i] = float64(v)
	}
	kern := func(t float64) float64 { // windowed sinc, t in input samples
		if t == 0 {
			return 1
		}
		if math.Abs(t) >= taps {
			return 0
		}
		return math.Sin(math.Pi*t) / (math.Pi * t) * (0.5 + 0.5*math.Cos(math.Pi*t/taps))
	}
	up := make([]float64, len(x)*f)
	for i := range up {
		pos := float64(i) / float64(f)
		base := int(math.Floor(pos)) + len(hist)
		var sum float64
		for k := base - taps + 1; k <= base+taps; k++ {
			if k < 0 || k >= len(ext) {
				continue
			}
			sum += ext[k] * kern(float64(base-len(hist))+pos-math.Floor(pos)-float64(k-len(hist)))
		}
		up[i] = float64(shape(s.curve, float32(sum)))
	}
	// decimate with a lowpass at the original Nyquist
	out := make([]float32, len(x))
	tapsUp := taps * f
	for i := range out {
		center := i * f
		var sum float64
		for k := center - tapsUp + 1; k < center+tapsUp; k++ {
			if k < 0 || k >= len(up) {
				continue
			}
			t := float64(k-center) / float64(f)
			sum += up[k] * kern(t)
		}
		out[i] = float32(sum / float64(f))
	}
	copy(hist, ext[len(ext)-len(hist):])
	return out
}
