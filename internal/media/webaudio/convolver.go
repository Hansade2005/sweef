package webaudio

import "math"

// convProc is a uniform-partitioned FFT convolver (block = render quantum).
type convProc struct {
	buf       *Buffer
	normalize bool
	rate      float64
	scale     float32
	plan      *fftPlan
	parts     [][][]complex128 // [irChannel][partition][2*quantum]
	fdl       [][][]complex128 // [inChannel][ring of partitions]
	prev      [][]float32      // previous input block per channel
	pos       int
	nChIR     int
}

const convN = 2 * quantum

func (p *convProc) setBuffer(b *Buffer) {
	p.buf = b
	p.rebuild()
}

func (p *convProc) rebuild() {
	p.parts, p.fdl, p.prev = nil, nil, nil
	p.pos = 0
	if p.buf == nil || p.buf.Frames() == 0 {
		return
	}
	if p.plan == nil {
		p.plan = newPlan(convN)
	}
	p.nChIR = len(p.buf.Data)
	scale := 1.0
	if p.normalize {
		var sum float64
		for _, ch := range p.buf.Data {
			for _, v := range ch {
				sum += float64(v) * float64(v)
			}
		}
		power := math.Sqrt(sum / float64(len(p.buf.Data)*p.buf.Frames()))
		if math.IsNaN(power) || math.IsInf(power, 0) || power < 0.000125 {
			power = 0.000125
		}
		scale = 1 / power
		scale *= math.Pow(10, -58*0.05)
		rate := p.rate
		if p.buf.Rate > 0 {
			rate = p.buf.Rate
		}
		if rate > 0 {
			scale *= 44100 / rate
		}
		if len(p.buf.Data) == 4 {
			scale *= 0.5
		}
	}
	p.scale = float32(scale)
	nParts := (p.buf.Frames() + quantum - 1) / quantum
	p.parts = make([][][]complex128, p.nChIR)
	for ch := 0; ch < p.nChIR; ch++ {
		p.parts[ch] = make([][]complex128, nParts)
		for k := 0; k < nParts; k++ {
			blk := make([]complex128, convN)
			for i := 0; i < quantum; i++ {
				j := k*quantum + i
				if j < p.buf.Frames() {
					blk[i] = complex(float64(p.buf.Data[ch][j])*scale, 0)
				}
			}
			p.plan.run(blk, false)
			p.parts[ch][k] = blk
		}
	}
}

func (p *convProc) process(n *Node, c *Context, start int64, frames int, in [][][]float32) {
	inCh := len(in[0])
	if p.parts == nil {
		n.setOut(0, inCh)
		return
	}
	nParts := len(p.parts[0])
	for len(p.fdl) < 2 {
		ring := make([][]complex128, nParts)
		for i := range ring {
			ring[i] = make([]complex128, convN)
		}
		p.fdl = append(p.fdl, ring)
		p.prev = append(p.prev, make([]float32, quantum))
	}
	// FFT of [prev, cur] per input channel (max 2 used)
	nIn := inCh
	if nIn > 2 {
		nIn = 2
	}
	spec := make([][]complex128, nIn)
	for ch := 0; ch < nIn; ch++ {
		blk := p.fdl[ch][p.pos%nParts]
		for i := 0; i < quantum; i++ {
			blk[i] = complex(float64(p.prev[ch][i]), 0)
			blk[quantum+i] = complex(float64(in[0][ch][i]), 0)
		}
		p.plan.run(blk, false)
		spec[ch] = blk
		copy(p.prev[ch], in[0][ch])
	}
	// output channel layout
	outCh := 1
	switch {
	case p.nChIR == 1:
		outCh = nIn
	case p.nChIR == 2:
		outCh = 2
	case p.nChIR == 4:
		outCh = 2
	}
	out := n.setOut(0, outCh)
	acc := make([][]complex128, outCh)
	for o := range acc {
		acc[o] = make([]complex128, convN)
	}
	irFor := func(o, i int) int { // IR channel that maps input i to output o
		switch p.nChIR {
		case 1:
			return 0
		case 2:
			if nIn == 1 {
				return o
			}
			if o == i {
				return o
			}
			return -1
		case 4:
			if nIn == 1 {
				return o
			}
			return o + 2*i
		}
		return -1
	}
	for o := 0; o < outCh; o++ {
		for i := 0; i < nIn; i++ {
			if p.nChIR == 1 && o != i {
				continue
			}
			ir := irFor(o, i)
			if ir < 0 {
				continue
			}
			if p.nChIR == 4 && nIn == 1 && i != 0 {
				continue
			}
			for k := 0; k < nParts; k++ {
				x := p.fdl[i][((p.pos-k)%nParts+nParts)%nParts]
				h := p.parts[ir][k]
				a := acc[o]
				for b := 0; b < convN; b++ {
					a[b] += x[b] * h[b]
				}
			}
		}
		p.plan.run(acc[o], true)
		for i := 0; i < quantum; i++ {
			out[o][i] = float32(real(acc[o][quantum+i]) / convN)
		}
	}
	p.pos++
}
