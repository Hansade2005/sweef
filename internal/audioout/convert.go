package audioout

// converter turns interleaved PCM of any channel count and sample rate into
// the device format (stereo, DeviceRate). Rate conversion is a 4-point
// Catmull-Rom interpolator with state carried across calls, so a stream can be
// written in arbitrary chunks without clicks. The ratio can change at any time
// (variable-speed playback).
type converter struct {
	inCh  int
	step  float64   // input frames consumed per output frame
	x     []float32 // stereo working buffer: 3 history frames, then new input
	p     float64   // read position in x, in frames
	fresh bool      // no input seen yet: history is primed from the first frame
}

func newConverter(inRate float64, inCh int) *converter {
	c := &converter{inCh: inCh, fresh: true}
	c.setRate(inRate)
	return c
}

func (c *converter) setRate(inRate float64) {
	if inRate <= 0 {
		inRate = DeviceRate
	}
	c.step = inRate / DeviceRate
}

func (c *converter) reset() {
	c.x = c.x[:0]
	c.p = 0
	c.fresh = true
}

// toStereo mixes interleaved frames of ch channels down (or up) to stereo.
func toStereo(in []float32, ch int, out []float32) []float32 {
	n := len(in) / ch
	if cap(out) < n*2 {
		out = make([]float32, n*2)
	}
	out = out[:n*2]
	switch ch {
	case 1:
		for i := 0; i < n; i++ {
			out[2*i], out[2*i+1] = in[i], in[i]
		}
	case 2:
		copy(out, in[:n*2])
	case 3:
		for i := 0; i < n; i++ {
			f := in[3*i : 3*i+3]
			out[2*i] = (f[0] + 0.7071*f[2]) * 0.5858
			out[2*i+1] = (f[1] + 0.7071*f[2]) * 0.5858
		}
	case 4:
		for i := 0; i < n; i++ {
			f := in[4*i : 4*i+4]
			out[2*i] = (f[0] + 0.7071*f[2]) * 0.5858
			out[2*i+1] = (f[1] + 0.7071*f[3]) * 0.5858
		}
	case 6: // L R C LFE Ls Rs (the order Web Audio and AAC/Opus use)
		for i := 0; i < n; i++ {
			f := in[6*i : 6*i+6]
			out[2*i] = (f[0] + 0.7071*f[2] + 0.7071*f[4]) * 0.4142
			out[2*i+1] = (f[1] + 0.7071*f[2] + 0.7071*f[5]) * 0.4142
		}
	default:
		for i := 0; i < n; i++ {
			out[2*i], out[2*i+1] = in[ch*i], in[ch*i+1]
		}
	}
	return out
}

func catmull(p0, p1, p2, p3, t float32) float32 {
	return p1 + 0.5*t*(p2-p0+t*(2*p0-5*p1+4*p2-p3+t*(3*(p1-p2)+p3-p0)))
}

// convert appends the device-format frames for in (interleaved, c.inCh
// channels) to dst. scratch is reused between calls to avoid allocation.
func (c *converter) convert(in []float32, dst []float32, scratch *[]float32) []float32 {
	if len(in) < c.inCh {
		return dst
	}
	if c.inCh > 0 && len(in)%c.inCh != 0 {
		in = in[:len(in)/c.inCh*c.inCh]
	}
	st := toStereo(in, c.inCh, *scratch)
	*scratch = st
	if c.step == 1 {
		return append(dst, st...)
	}
	if c.fresh {
		c.fresh = false
		c.x = append(c.x[:0], st[0], st[1], st[0], st[1], st[0], st[1])
		c.p = 3
	}
	c.x = append(c.x, st...)
	nf := len(c.x) / 2
	for c.p < float64(nf-2) {
		i1 := int(c.p)
		t := float32(c.p - float64(i1))
		a, b, cc, d := 2*(i1-1), 2*i1, 2*(i1+1), 2*(i1+2)
		dst = append(dst,
			catmull(c.x[a], c.x[b], c.x[cc], c.x[d], t),
			catmull(c.x[a+1], c.x[b+1], c.x[cc+1], c.x[d+1], t))
		c.p += c.step
	}
	// keep the last three frames as history
	keep := nf - 3
	if keep > 0 {
		copy(c.x, c.x[keep*2:])
		c.x = c.x[:6]
		c.p -= float64(keep)
	}
	return dst
}
