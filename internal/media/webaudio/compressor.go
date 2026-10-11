package webaudio

import "math"

// compProc is a look-ahead feed-forward compressor modelled on Chromium's
// DynamicsCompressor: 6 ms pre-delay, soft knee, adaptive release and makeup
// gain compensation. It is an approximation, not a bit-exact port.
type compProc struct {
	sr        float64
	delay     [][]float32
	dw        int
	dsize     int
	env       float64 // detector envelope (linear gain reduction state)
	gain      float64
	reduction float64
	maxG      float64
}

func newCompProc(sr float64) *compProc {
	d := int(0.006 * sr)
	if d < 1 {
		d = 1
	}
	return &compProc{sr: sr, dsize: d, gain: 1, env: 1}
}

// saturate applies the static curve (knee + ratio) to a linear input level.
func compCurve(x, thresholdDb, kneeDb, ratio float64) float64 {
	// returns the output level in linear units for input level x (linear)
	if x <= 0 {
		return 0
	}
	xdb := 20 * math.Log10(x)
	var ydb float64
	lo := thresholdDb - kneeDb/2
	hi := thresholdDb + kneeDb/2
	slope := 1 / ratio
	switch {
	case xdb < lo:
		ydb = xdb
	case xdb > hi || kneeDb == 0:
		ydb = thresholdDb + (xdb-thresholdDb)*slope
	default:
		d := xdb - lo
		ydb = xdb + (slope-1)*d*d/(2*kneeDb)
	}
	return math.Pow(10, ydb/20)
}

func (p *compProc) process(n *Node, c *Context, start int64, frames int, in [][][]float32) {
	th := n.Params["threshold"].compute(c, start, 1)[0]
	kn := n.Params["knee"].compute(c, start, 1)[0]
	ra := n.Params["ratio"].compute(c, start, 1)[0]
	at := n.Params["attack"].compute(c, start, 1)[0]
	re := n.Params["release"].compute(c, start, 1)[0]
	ch := len(in[0])
	for len(p.delay) < ch {
		p.delay = append(p.delay, make([]float32, p.dsize))
	}
	out := n.setOut(0, ch)
	// makeup gain like Chromium: inverse of the full-range gain, ^0.6
	full := compCurve(1, float64(th), float64(kn), float64(ra))
	makeup := math.Pow(1/full, 0.6)
	atk := math.Exp(-1 / (math.Max(float64(at), 1e-4) * p.sr))
	rel := math.Exp(-1 / (math.Max(float64(re), 1e-3) * p.sr))
	minRed := 1.0
	for i := 0; i < frames; i++ {
		// detector: max abs over channels
		var peak float64
		for k := 0; k < ch; k++ {
			if a := math.Abs(float64(in[0][k][i])); a > peak {
				peak = a
			}
		}
		target := 1.0
		if peak > 1e-9 {
			target = compCurve(peak, float64(th), float64(kn), float64(ra)) / peak
		}
		if target < p.gain {
			p.gain = atk*p.gain + (1-atk)*target
			if p.gain < target {
				p.gain = target
			}
		} else {
			p.gain = rel*p.gain + (1-rel)*target
			if p.gain > 1 {
				p.gain = 1
			}
		}
		if p.gain < minRed {
			minRed = p.gain
		}
		for k := 0; k < ch; k++ {
			d := p.delay[k][(p.dw+i)%p.dsize]
			p.delay[k][(p.dw+i)%p.dsize] = in[0][k][i]
			out[k][i] = float32(float64(d) * p.gain * makeup)
		}
	}
	p.dw = (p.dw + frames) % p.dsize
	p.reduction = 20 * math.Log10(minRed)
}

// CompressorReduction returns the current gain reduction in dB.
func (c *Context) CompressorReduction(id int) float64 {
	if p, ok := c.byID[id].proc.(*compProc); ok {
		return p.reduction
	}
	return 0
}
