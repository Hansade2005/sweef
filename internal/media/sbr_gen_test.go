package media

import (
	"encoding/binary"
	"fmt"
	"math/rand"
)

// Test stream generator for HE-AAC: takes an AAC-LC stream and appends valid,
// pseudo random SBR (and PS) extension payloads to every access unit, then
// wraps the result in an MP4 with an explicit or implicit configuration. The
// payloads exercise every grid class, envelope / noise delta coding, coupling,
// harmonics and header changes; correctness is judged against ffmpeg.

type bitW struct {
	b []byte
	n int
}

func (w *bitW) put(v, n int) {
	for i := n - 1; i >= 0; i-- {
		if w.n&7 == 0 {
			w.b = append(w.b, 0)
		}
		if v>>uint(i)&1 != 0 {
			w.b[w.n>>3] |= 0x80 >> uint(w.n&7)
		}
		w.n++
	}
}

func (w *bitW) flag(b bool) {
	if b {
		w.put(1, 1)
	} else {
		w.put(0, 1)
	}
}

func (w *bitW) huff(lens []uint8, codes []uint32, sym int) {
	w.put(int(codes[sym]), int(lens[sym]))
}

func (w *bitW) append(o *bitW) {
	for i := 0; i < o.n; i++ {
		w.put(int(o.b[i>>3]>>(7-uint(i&7)))&1, 1)
	}
}

type genChan struct {
	class          int
	numEnv         int
	tEnv           [8]int
	freqRes        [8]int // [1..numEnv]
	absBord, nLead int
	nTrail         int
	varBord0       int
	rel            [8]int
	ptr            int
	numNoise       int
	dfEnv          [8]bool
	dfNoise        [3]bool
	invf           [5]int
	env            [8][48]int // values per envelope [1..numEnv]
	noise          [3][5]int
	harm           [48]bool
	harmFlag       bool
	prevEnv        [48]int
	prevRes        int
	prevNoise      [5]int
	overhang       int
}

// sbrGenCfg describes a stream scenario.
type sbrGenCfg struct {
	seed        int64
	cpe         bool
	coupling    int // 0 never, 1 always, 2 random per frame
	startFreq   int
	stopFreq    int
	xover       int
	freqScale   int
	alterScale  int
	noiseBands  int
	limBands    int
	limGains    int
	interpol    int
	smoothing   int
	ampRes      int
	extra1      bool
	extra2      bool
	headerEvery int
	switchAt    int // frame at which the spectrum parameters change (0 never)
	classes     []int
	crc         bool
	ps          *psGenCfg
	harmProb    int // percent chance a harmonic flag is set
	envLo, envHi int // envelope value range override
	fixEnv       int // FIXFIX envelope count override
	hiRes        bool
	noDf         bool
	loRes        bool
	invfConst    int // >= 0 forces all inverse filtering modes
	invfSet      bool
	envPattern   []int // per high resolution band envelope values (debugging)
	noiseVal    int // >= 0: constant noise floor start value (-1 random); needs tiny deltas
}

type sbrGen struct {
	cfg      sbrGenCfg
	rng      *rand.Rand
	coreRate int
	sh       *sbrElem
	ch       [2]genChan
	frame    int
	hdrBits  *bitW
	psg      *psGen
}

func newSBRGen(cfg sbrGenCfg, coreRate int) *sbrGen {
	g := &sbrGen{cfg: cfg, rng: rand.New(rand.NewSource(cfg.seed)), coreRate: coreRate}
	g.sh = newSBRElem(cfg.cpe, coreRate, false)
	g.sh.psOK = cfg.ps != nil
	if len(cfg.classes) == 0 {
		g.cfg.classes = []int{0, 1, 2, 3}
	}
	if cfg.ps != nil {
		g.psg = newPSGen(cfg.ps, g.rng)
	}
	return g
}

func (g *sbrGen) clampDelta(lav, mult, cur, lo, hi int) (sym, nv int) {
	var d int
	switch r := g.rng.Intn(100); {
	case r < 50:
		d = g.rng.Intn(3) - 1
	case r < 85:
		d = g.rng.Intn(9) - 4
	case r < 97:
		d = g.rng.Intn(2*lav+1) - lav
	default:
		d = lav
		if g.rng.Intn(2) == 0 {
			d = -lav
		}
	}
	if d > lav {
		d = lav
	}
	if d < -lav {
		d = -lav
	}
	for cur+mult*d > hi {
		d--
	}
	for cur+mult*d < lo {
		d++
	}
	if d > lav || d < -lav {
		d = 0
	}
	return d + lav, cur + mult*d
}

func (g *sbrGen) chooseGrid(c *genChan, first bool) {
	cl := g.cfg.classes
	for {
		class := cl[g.rng.Intn(len(cl))]
		if class == sbrFixFix && c.overhang != 0 {
			continue
		}
		c.class = class
		break
	}
	fr := func() int { return g.rng.Intn(2) }
	switch c.class {
	case sbrFixFix:
		c.numEnv = []int{1, 2, 4}[g.rng.Intn(3)]
		if g.cfg.fixEnv > 0 {
			c.numEnv = g.cfg.fixEnv
		}
		step := 16 / c.numEnv
		for i := 0; i <= c.numEnv; i++ {
			c.tEnv[i] = i * step
		}
		r := fr()
		if g.cfg.hiRes {
			r = 1
		}
		if g.cfg.loRes {
			r = 0
		}
		for i := 1; i <= c.numEnv; i++ {
			c.freqRes[i] = r
		}
		c.ptr = 0
		c.absBord = 0
	case sbrFixVar:
		c.absBord = g.rng.Intn(4)
		c.nTrail = g.rng.Intn(4)
		c.numEnv = c.nTrail + 1
		c.tEnv[0] = 0
		c.tEnv[c.numEnv] = 16 + c.absBord
		for i := 0; i < c.nTrail; i++ {
			c.rel[i] = g.rng.Intn(2)
			c.tEnv[c.numEnv-1-i] = c.tEnv[c.numEnv-i] - 2*c.rel[i] - 2
		}
		for i := 1; i <= c.numEnv; i++ {
			c.freqRes[i] = fr()
		}
		c.ptr = g.rng.Intn(sbrMin(c.numEnv+2, 1<<uint(sbrPtrBits(c.numEnv))))
	case sbrVarFix:
		c.varBord0 = c.overhang
		c.nLead = g.rng.Intn(4)
		c.numEnv = c.nLead + 1
		c.tEnv[0] = c.varBord0
		for i := 0; i < c.nLead; i++ {
			c.rel[i] = g.rng.Intn(2)
			c.tEnv[i+1] = c.tEnv[i] + 2*c.rel[i] + 2
		}
		c.tEnv[c.numEnv] = 16
		for i := 1; i <= c.numEnv; i++ {
			c.freqRes[i] = fr()
		}
		c.ptr = g.rng.Intn(sbrMin(c.numEnv+2, 1<<uint(sbrPtrBits(c.numEnv))))
		c.absBord = 0
	case sbrVarVar:
		c.varBord0 = c.overhang
		c.absBord = g.rng.Intn(4)
		c.nLead = g.rng.Intn(3)
		c.nTrail = g.rng.Intn(3)
		c.numEnv = c.nLead + c.nTrail + 1
		c.tEnv[0] = c.varBord0
		c.tEnv[c.numEnv] = 16 + c.absBord
		var rl, rt [4]int
		for i := 0; i < c.nLead; i++ {
			rl[i] = g.rng.Intn(2)
			c.tEnv[i+1] = c.tEnv[i] + 2*rl[i] + 2
		}
		for i := 0; i < c.nTrail; i++ {
			rt[i] = g.rng.Intn(2)
			c.tEnv[c.numEnv-1-i] = c.tEnv[c.numEnv-i] - 2*rt[i] - 2
		}
		for i := 0; i < 4; i++ {
			c.rel[i] = rl[i]
			c.rel[4+i%4] = rt[i]
		}
		c.rel = [8]int{rl[0], rl[1], rl[2], rl[3], rt[0], rt[1], rt[2], rt[3]}
		for i := 1; i <= c.numEnv; i++ {
			c.freqRes[i] = fr()
		}
		c.ptr = g.rng.Intn(sbrMin(c.numEnv+2, 1<<uint(sbrPtrBits(c.numEnv))))
	}
	c.numNoise = 1
	if c.numEnv > 1 {
		c.numNoise = 2
	}
	// next frame's leading border
	if c.class == sbrFixFix || c.class == sbrVarFix {
		c.overhang = 0
	} else {
		c.overhang = c.absBord
	}
}

func sbrMin(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (g *sbrGen) validGrid(c *genChan) bool {
	for i := 1; i <= c.numEnv; i++ {
		if c.tEnv[i-1] >= c.tEnv[i] {
			return false
		}
	}
	return true
}

func (g *sbrGen) writeGrid(w *bitW, c *genChan) {
	w.put(c.class, 2)
	switch c.class {
	case sbrFixFix:
		l := 0
		for 1<<uint(l) < c.numEnv {
			l++
		}
		w.put(l, 2)
		w.put(c.freqRes[1], 1)
	case sbrFixVar:
		w.put(c.absBord, 2)
		w.put(c.nTrail, 2)
		for i := 0; i < c.nTrail; i++ {
			w.put(c.rel[i], 2)
		}
		w.put(c.ptr, sbrPtrBits(c.numEnv))
		for i := 0; i < c.numEnv; i++ {
			w.put(c.freqRes[c.numEnv-i], 1)
		}
	case sbrVarFix:
		w.put(c.varBord0, 2)
		w.put(c.nLead, 2)
		for i := 0; i < c.nLead; i++ {
			w.put(c.rel[i], 2)
		}
		w.put(c.ptr, sbrPtrBits(c.numEnv))
		for i := 1; i <= c.numEnv; i++ {
			w.put(c.freqRes[i], 1)
		}
	case sbrVarVar:
		w.put(c.varBord0, 2)
		w.put(c.absBord, 2)
		w.put(c.nLead, 2)
		w.put(c.nTrail, 2)
		for i := 0; i < c.nLead; i++ {
			w.put(c.rel[i], 2)
		}
		for i := 0; i < c.nTrail; i++ {
			w.put(c.rel[4+i], 2)
		}
		w.put(c.ptr, sbrPtrBits(c.numEnv))
		for i := 1; i <= c.numEnv; i++ {
			w.put(c.freqRes[i], 1)
		}
	}
}

func (g *sbrGen) writeEnvelope(w *bitW, c *genChan, ch int, coupled, reset bool) {
	s := g.sh
	odd := s.n[1] & 1
	mult := 1
	var bits, tb, fb int
	ampRes := g.cfg.ampRes
	if c.class == sbrFixFix && c.numEnv == 1 {
		ampRes = 0
	}
	switch {
	case coupled && ch == 1 && ampRes != 0:
		mult, bits, tb, fb = 2, 5, hTEnvBal30, hFEnvBal30
	case coupled && ch == 1:
		mult, bits, tb, fb = 2, 6, hTEnvBal15, hFEnvBal15
	case ampRes != 0:
		bits, tb, fb = 6, hTEnv30, hFEnv30
	default:
		bits, tb, fb = 7, hTEnv15, hFEnv15
	}
	lo, hi := 8, 46
	if ampRes != 0 {
		lo, hi = 4, 23
	}
	if g.cfg.envHi > 0 {
		lo, hi = g.cfg.envLo, g.cfg.envHi
	}
	if coupled && ch == 1 {
		lo, hi = 0, 90
	}
	tl, fl := sbrHuffLav[tb], sbrHuffLav[fb]
	prev := c.prevEnv
	prevRes := c.prevRes
	for i := 0; i < c.numEnv; i++ {
		cur := c.freqRes[i+1]
		nb := s.n[cur]
		useDf := c.dfEnv[i]
		if useDf {
			for j := 0; j < nb; j++ {
				var base int
				switch {
				case cur == prevRes:
					base = prev[j]
				case cur != 0:
					base = prev[(j+odd)>>1]
				default:
					k := 0
					if j != 0 {
						k = 2*j - odd
					}
					base = prev[k]
				}
				sym, nv := g.clampDelta(tl, mult, base, lo, hi)
				w.huff(sbrHuffLen[tb], sbrHuffCode[tb], sym)
				c.env[i+1][j] = nv
			}
		} else {
			maxv := 1<<uint(bits) - 1
			var v int
			if coupled && ch == 1 {
				v = (maxv / 2) + g.rng.Intn(5) - 2
			} else {
				v = lo + (hi-lo)/4 + g.rng.Intn((hi-lo)/2+1)
			}
			if v > maxv {
				v = maxv
			}
			if len(g.cfg.envPattern) > 0 {
				v = g.cfg.envPattern[0]
			}
			w.put(v, bits)
			c.env[i+1][0] = mult * v
			for j := 1; j < nb; j++ {
				sym, nv := g.clampDelta(fl, mult, c.env[i+1][j-1], lo, hi)
				if len(g.cfg.envPattern) > 0 {
					nv = g.cfg.envPattern[j]
					sym = nv - c.env[i+1][j-1] + fl
				}
				w.huff(sbrHuffLen[fb], sbrHuffCode[fb], sym)
				c.env[i+1][j] = nv
			}
		}
		prev = c.env[i+1]
		prevRes = cur
	}
	c.prevEnv = c.env[c.numEnv]
	c.prevRes = c.freqRes[c.numEnv]
}

func (g *sbrGen) writeNoise(w *bitW, c *genChan, ch int, coupled bool) {
	s := g.sh
	mult := 1
	var tb, fb int
	if coupled && ch == 1 {
		mult = 2
		tb, fb = hTNoiseBal30, hFEnvBal30
	} else {
		tb, fb = hTNoise30, hFEnv30
	}
	tl, fl := sbrHuffLav[tb], sbrHuffLav[fb]
	prev := c.prevNoise
	for i := 0; i < c.numNoise; i++ {
		if c.dfNoise[i] {
			for j := 0; j < s.nQ; j++ {
				sym, nv := g.clampDelta(tl, mult, prev[j], 0, 30)
				if g.cfg.noiseVal >= 0 {
					sym, nv = tl, prev[j]
				}
				w.huff(sbrHuffLen[tb], sbrHuffCode[tb], sym)
				c.noise[i+1][j] = nv
			}
		} else {
			v := 8 + g.rng.Intn(10)
			if g.cfg.noiseVal >= 0 {
				v = g.cfg.noiseVal
			}
			if coupled && ch == 1 {
				v = 6 + g.rng.Intn(3)
			}
			w.put(v, 5)
			c.noise[i+1][0] = mult * v
			for j := 1; j < s.nQ; j++ {
				sym, nv := g.clampDelta(fl, mult, c.noise[i+1][j-1], 0, 30)
				if g.cfg.noiseVal >= 0 {
					sym, nv = fl, c.noise[i+1][j-1]
				}
				w.huff(sbrHuffLen[fb], sbrHuffCode[fb], sym)
				c.noise[i+1][j] = nv
			}
		}
		prev = c.noise[i+1]
	}
	c.prevNoise = c.noise[c.numNoise]
}

// frame produces the extension payload bits (after the extension_type nibble).
func (g *sbrGen) frameBits() *bitW {
	cfg := &g.cfg
	if cfg.switchAt > 0 && g.frame == cfg.switchAt {
		cfg.startFreq = (cfg.startFreq + 3) % 10
		cfg.stopFreq = 5 + (cfg.stopFreq+3)%6
		cfg.noiseBands = (cfg.noiseBands + 1) & 3
		cfg.limBands = (cfg.limBands + 1) & 3
	}
	for attempt := 0; ; attempt++ {
		w, ok := g.tryFrame(attempt)
		if ok {
			return w
		}
		if attempt > 200 {
			panic("sbr generator cannot produce a valid frame")
		}
	}
}

func (g *sbrGen) tryFrame(attempt int) (*bitW, bool) {
	cfg := &g.cfg
	// snapshot generator state so a failed attempt can be retried
	snapCh := g.ch
	snapRng := g.rng
	_ = snapRng
	w := &bitW{}
	if cfg.crc {
		w.put(0, 10)
	}
	sendHdr := g.frame == 0 || (cfg.headerEvery > 0 && g.frame%cfg.headerEvery == 0) || (cfg.switchAt > 0 && g.frame == cfg.switchAt)
	reset := false
	if sendHdr {
		hw := &bitW{}
		hw.put(cfg.ampRes, 1)
		hw.put(cfg.startFreq, 4)
		hw.put(cfg.stopFreq, 4)
		hw.put(cfg.xover, 3)
		hw.put(0, 2)
		hw.flag(cfg.extra1)
		hw.flag(cfg.extra2)
		fs, as, nb := 2, 1, 2
		if cfg.extra1 {
			hw.put(cfg.freqScale, 2)
			hw.put(cfg.alterScale, 1)
			hw.put(cfg.noiseBands, 2)
			fs, as, nb = cfg.freqScale, cfg.alterScale, cfg.noiseBands
		}
		if cfg.extra2 {
			hw.put(cfg.limBands, 2)
			hw.put(cfg.limGains, 2)
			hw.put(cfg.interpol, 1)
			hw.put(cfg.smoothing, 1)
		}
		old := g.sh.spec
		tmp := newSBRElem(cfg.cpe, g.coreRate, false)
		r := &aacBits{b: hw.b}
		tmp.readHeader(r)
		if err := tmp.resetTables(); err != nil {
			return nil, false
		}
		reset = tmp.spec != old
		_ = fs
		_ = as
		_ = nb
		w.put(1, 1)
		w.append(hw)
	} else {
		w.put(0, 1)
	}
	if g.frame == 0 && !sendHdr {
		return nil, false
	}
	// tables for sizing: derive from a scratch element that has seen all headers so far
	sz := g.sizing()
	if sz == nil {
		return nil, false
	}
	nch := 1
	if cfg.cpe {
		nch = 2
	}
	coupled := false
	if cfg.cpe {
		switch cfg.coupling {
		case 1:
			coupled = true
		case 2:
			coupled = g.rng.Intn(2) == 0
		}
	}
	// grids
	for ci := 0; ci < nch; ci++ {
		c := &g.ch[ci]
		if coupled && ci == 1 {
			continue
		}
		g.chooseGrid(c, g.frame == 0)
		if !g.validGrid(c) {
			g.ch = snapCh
			return nil, false
		}
	}
	if coupled {
		g.ch[1].class, g.ch[1].numEnv, g.ch[1].tEnv, g.ch[1].freqRes = g.ch[0].class, g.ch[0].numEnv, g.ch[0].tEnv, g.ch[0].freqRes
		g.ch[1].numNoise = g.ch[0].numNoise
		g.ch[1].overhang = g.ch[0].overhang
	}
	for ci := 0; ci < nch; ci++ {
		c := &g.ch[ci]
		for i := 0; i < c.numEnv; i++ {
			c.dfEnv[i] = g.rng.Intn(3) != 0 && !(reset || g.frame == 0)
			if g.cfg.noDf {
				c.dfEnv[i] = false
			}
		}
		for i := 0; i < c.numNoise; i++ {
			c.dfNoise[i] = g.rng.Intn(3) != 0 && !(reset || g.frame == 0) && !g.cfg.noDf
		}
		for i := 0; i < 5; i++ {
			c.invf[i] = g.rng.Intn(4)
			if g.cfg.invfSet {
				c.invf[i] = g.cfg.invfConst
			}
		}
		c.harmFlag = g.rng.Intn(100) < g.cfg.harmProb
		for i := 0; i < 48; i++ {
			c.harm[i] = g.rng.Intn(100) < 25
		}
	}
	g.sh.n = sz.n
	g.sh.nQ = sz.nQ
	if !cfg.cpe {
		c := &g.ch[0]
		w.put(0, 1) // bs_data_extra
		g.writeGrid(w, c)
		for i := 0; i < c.numEnv; i++ {
			w.flag(c.dfEnv[i])
		}
		for i := 0; i < c.numNoise; i++ {
			w.flag(c.dfNoise[i])
		}
		for i := 0; i < sz.nQ; i++ {
			w.put(c.invf[i], 2)
		}
		g.writeEnvelope(w, c, 0, false, reset)
		g.writeNoise(w, c, 0, false)
		w.flag(c.harmFlag)
		if c.harmFlag {
			for i := 0; i < sz.n[1]; i++ {
				w.flag(c.harm[i])
			}
		}
	} else {
		c0, c1 := &g.ch[0], &g.ch[1]
		w.put(0, 1)
		w.flag(coupled)
		if coupled {
			g.writeGrid(w, c0)
			for _, c := range []*genChan{c0, c1} {
				for i := 0; i < c.numEnv; i++ {
					w.flag(c.dfEnv[i])
				}
				for i := 0; i < c.numNoise; i++ {
					w.flag(c.dfNoise[i])
				}
			}
			for i := 0; i < sz.nQ; i++ {
				w.put(c0.invf[i], 2)
			}
			g.writeEnvelope(w, c0, 0, true, reset)
			g.writeNoise(w, c0, 0, true)
			g.writeEnvelope(w, c1, 1, true, reset)
			g.writeNoise(w, c1, 1, true)
		} else {
			g.writeGrid(w, c0)
			g.writeGrid(w, c1)
			for _, c := range []*genChan{c0, c1} {
				for i := 0; i < c.numEnv; i++ {
					w.flag(c.dfEnv[i])
				}
				for i := 0; i < c.numNoise; i++ {
					w.flag(c.dfNoise[i])
				}
			}
			for _, c := range []*genChan{c0, c1} {
				for i := 0; i < sz.nQ; i++ {
					w.put(c.invf[i], 2)
				}
			}
			g.writeEnvelope(w, c0, 0, false, reset)
			g.writeEnvelope(w, c1, 1, false, reset)
			g.writeNoise(w, c0, 0, false)
			g.writeNoise(w, c1, 1, false)
		}
		for _, c := range []*genChan{c0, c1} {
			w.flag(c.harmFlag)
			if c.harmFlag {
				for i := 0; i < sz.n[1]; i++ {
					w.flag(c.harm[i])
				}
			}
		}
	}
	// extension data (PS)
	if g.psg != nil && !cfg.cpe {
		pw := g.psg.frameBits()
		w.put(1, 1) // bs_extended_data
		nbytes := (pw.n + 2 + 7) / 8
		if nbytes >= 15 {
			w.put(15, 4)
			w.put(nbytes-15, 8)
		} else {
			w.put(nbytes, 4)
		}
		w.put(2, 2) // bs_extension_id PS
		w.append(pw)
		for i := pw.n + 2; i < nbytes*8; i++ {
			w.put(0, 1)
		}
	} else {
		w.put(0, 1)
	}
	// feed the shadow decoder so its state tracks the real one
	r := &aacBits{b: w.b}
	if err := g.sh.parse(r, w.n, cfg.crc); err != nil {
		g.ch = snapCh
		return nil, false
	}
	g.sh.reset = false
	g.sh.ready = false
	g.frame++
	return w, true
}

type sbrSizing struct {
	n  [2]int
	nQ int
}

// sizing returns the band counts valid for the header most recently written.
func (g *sbrGen) sizing() *sbrSizing {
	cfg := g.cfg
	tmp := newSBRElem(cfg.cpe, g.coreRate, false)
	hw := &bitW{}
	hw.put(cfg.ampRes, 1)
	hw.put(cfg.startFreq, 4)
	hw.put(cfg.stopFreq, 4)
	hw.put(cfg.xover, 3)
	hw.put(0, 2)
	hw.flag(cfg.extra1)
	hw.flag(cfg.extra2)
	if cfg.extra1 {
		hw.put(cfg.freqScale, 2)
		hw.put(cfg.alterScale, 1)
		hw.put(cfg.noiseBands, 2)
	}
	if cfg.extra2 {
		hw.put(cfg.limBands, 2)
		hw.put(cfg.limGains, 2)
		hw.put(cfg.interpol, 1)
		hw.put(cfg.smoothing, 1)
	}
	r := &aacBits{b: hw.b}
	tmp.readHeader(r)
	if tmp.resetTables() != nil {
		return nil
	}
	return &sbrSizing{n: tmp.n, nQ: tmp.nQ}
}

// ---- stream assembly -----------------------------------------------------------

// buildFill writes a fill element carrying an SBR extension payload.
func buildFill(payload *bitW, crc bool) *bitW {
	total := 4 + payload.n
	cnt := (total + 7) / 8
	w := &bitW{}
	w.put(6, 3)
	if cnt >= 15 {
		w.put(15, 4)
		w.put(cnt-14, 8)
	} else {
		w.put(cnt, 4)
	}
	if crc {
		w.put(0xE, 4)
	} else {
		w.put(0xD, 4)
	}
	w.append(payload)
	for i := total; i < cnt*8; i++ {
		w.put(0, 1)
	}
	return w
}

// injectFill splices a fill element in front of the END element of an access unit.
func injectFill(au []byte, endBit int, fill *bitW) []byte {
	w := &bitW{}
	for i := 0; i < endBit; i++ {
		w.put(int(au[i>>3]>>(7-uint(i&7)))&1, 1)
	}
	w.append(fill)
	w.put(7, 3)
	return w.b
}

// ascHE builds an AudioSpecificConfig. mode: "explicit-sbr" (AOT 5),
// "explicit-ps" (AOT 29), "bc-sbr" (AOT 2 with sync extension), "bc-ps",
// "implicit" (plain AAC-LC).
func ascHE(mode string, coreRate, outRate, ch int) []byte {
	sfi := func(r int) int {
		for i, v := range aacRates {
			if v == r {
				return i
			}
		}
		panic("rate")
	}
	w := &bitW{}
	switch mode {
	case "explicit-sbr", "explicit-ps":
		if mode == "explicit-sbr" {
			w.put(5, 5)
		} else {
			w.put(29, 5)
		}
		w.put(sfi(coreRate), 4)
		w.put(ch, 4)
		w.put(sfi(outRate), 4)
		w.put(2, 5)
		w.put(0, 3)
	case "bc-sbr", "bc-ps":
		w.put(2, 5)
		w.put(sfi(coreRate), 4)
		w.put(ch, 4)
		w.put(0, 3)
		w.put(0x2b7, 11)
		w.put(5, 5)
		w.put(1, 1)
		w.put(sfi(outRate), 4)
		if mode == "bc-ps" {
			w.put(0x548, 11)
			w.put(1, 1)
		}
	default:
		w.put(2, 5)
		w.put(sfi(coreRate), 4)
		w.put(ch, 4)
		w.put(0, 3)
	}
	return w.b
}

func be32b(v uint32) []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, v); return b }
func be16b(v uint16) []byte { b := make([]byte, 2); binary.BigEndian.PutUint16(b, v); return b }

func box(typ string, parts ...[]byte) []byte {
	n := 8
	for _, p := range parts {
		n += len(p)
	}
	b := be32b(uint32(n))
	b = append(b, typ...)
	for _, p := range parts {
		b = append(b, p...)
	}
	return b
}

func descr(tag byte, parts ...[]byte) []byte {
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	b := []byte{tag, byte(0x80 | n>>21&0x7f), byte(0x80 | n>>14&0x7f), byte(0x80 | n>>7&0x7f), byte(n & 0x7f)}
	for _, p := range parts {
		b = append(b, p...)
	}
	return b
}

// buildMP4 wraps raw AAC access units in a minimal MP4 file.
func buildMP4(asc []byte, sampleRate, channels int, packets [][]byte) []byte {
	var mdat []byte
	var sizes []byte
	for _, p := range packets {
		mdat = append(mdat, p...)
		sizes = append(sizes, be32b(uint32(len(p)))...)
	}
	ftyp := box("ftyp", []byte("M4A "), be32b(0), []byte("M4A mp42isom"))
	esds := box("esds", be32b(0), descr(3, be16b(1), []byte{0},
		descr(4, []byte{0x40, 0x15, 0, 0, 0}, be32b(0), be32b(0), descr(5, asc)),
		descr(6, []byte{2})))
	mp4a := box("mp4a", make([]byte, 6), be16b(1), make([]byte, 8), be16b(uint16(channels)), be16b(16), be32b(0), be32b(uint32(sampleRate)<<16), esds)
	stsd := box("stsd", be32b(0), be32b(1), mp4a)
	dur := uint32(len(packets) * 1024)
	stts := box("stts", be32b(0), be32b(1), be32b(uint32(len(packets))), be32b(1024))
	stsc := box("stsc", be32b(0), be32b(1), be32b(1), be32b(uint32(len(packets))), be32b(1))
	stsz := box("stsz", be32b(0), be32b(0), be32b(uint32(len(packets))), sizes)
	// moov size is fixed so the chunk offset can be computed
	build := func(off uint32) []byte {
		stco := box("stco", be32b(0), be32b(1), be32b(off))
		stbl := box("stbl", stsd, stts, stsc, stsz, stco)
		dinf := box("dinf", box("dref", be32b(0), be32b(1), box("url ", be32b(1))))
		minf := box("minf", box("smhd", be32b(0), be32b(0)), dinf, stbl)
		mdhd := box("mdhd", be32b(0), be32b(0), be32b(0), be32b(uint32(sampleRate)), be32b(dur), be16b(0x55c4), be16b(0))
		hdlr := box("hdlr", be32b(0), be32b(0), []byte("soun"), make([]byte, 12), []byte{0})
		mdia := box("mdia", mdhd, hdlr, minf)
		tkhd := box("tkhd", be32b(3), be32b(0), be32b(0), be32b(1), be32b(0), be32b(dur), make([]byte, 8), be16b(0), be16b(0), be16b(0x0100), be16b(0),
			be32b(0x10000), be32b(0), be32b(0), be32b(0), be32b(0x10000), be32b(0), be32b(0), be32b(0), be32b(0x40000000), be32b(0), be32b(0))
		trak := box("trak", tkhd, mdia)
		mvhd := box("mvhd", be32b(0), be32b(0), be32b(0), be32b(uint32(sampleRate)), be32b(dur), be32b(0x10000), be16b(0x0100), make([]byte, 10),
			be32b(0x10000), be32b(0), be32b(0), be32b(0), be32b(0x10000), be32b(0), be32b(0), be32b(0), be32b(0x40000000), make([]byte, 24), be32b(2))
		return box("moov", mvhd, trak)
	}
	moov := build(0)
	off := uint32(len(ftyp) + len(moov) + 8)
	moov = build(off)
	out := append([]byte{}, ftyp...)
	out = append(out, moov...)
	out = append(out, box("mdat", mdat)...)
	return out
}

func describe(c sbrGenCfg) string {
	return fmt.Sprintf("cpe=%v coupling=%d start=%d stop=%d xover=%d fs=%d as=%d nb=%d", c.cpe, c.coupling, c.startFreq, c.stopFreq, c.xover, c.freqScale, c.alterScale, c.noiseBands)
}

// ---- parametric stereo generator (filled in with the PS decoder) -------------

type psGenCfg struct{}
type psGen struct{}

func newPSGen(c *psGenCfg, r *rand.Rand) *psGen { return &psGen{} }
func (p *psGen) frameBits() *bitW               { return &bitW{} }
