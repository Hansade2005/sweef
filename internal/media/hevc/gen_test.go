package hevc

// A random bitstream generator used to cross-check the decoder against
// ffmpeg on tools x265 never emits (tiles, PCM, range extension tools, long
// term references, dependent slices ...).
//
// Parameter sets and slice headers are written by hand; the slice data is
// produced by running the decoder itself with a binSource that picks every
// bin at random and arithmetic-encodes it. The resulting stream is therefore
// syntactically valid by construction, and ffmpeg's decoding of it is the
// reference for the decoder's own decoding of the same bytes.

import (
	"bytes"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// ---- bit writer -----------------------------------------------------------

type bitW struct {
	b []byte
	n int // bits written
}

func (w *bitW) bit(v int) {
	if w.n&7 == 0 {
		w.b = append(w.b, 0)
	}
	if v != 0 {
		w.b[w.n>>3] |= 0x80 >> uint(w.n&7)
	}
	w.n++
}

func (w *bitW) u(v uint64, n int) {
	for i := n - 1; i >= 0; i-- {
		w.bit(int(v>>uint(i)) & 1)
	}
}

func (w *bitW) flag(b bool) {
	if b {
		w.bit(1)
	} else {
		w.bit(0)
	}
}

func (w *bitW) ue(v uint32) {
	x := uint64(v) + 1
	l := 0
	for t := x; t > 1; t >>= 1 {
		l++
	}
	w.u(0, l)
	w.u(x, l+1)
}

func (w *bitW) se(v int32) {
	if v > 0 {
		w.ue(uint32(2*v - 1))
	} else {
		w.ue(uint32(-2 * v))
	}
}

func (w *bitW) alignZero() {
	for w.n&7 != 0 {
		w.bit(0)
	}
}

func (w *bitW) trailing() {
	w.bit(1)
	w.alignZero()
}

func nalWrap(typ, tid int, rbsp []byte) []byte {
	out := []byte{0, 0, 0, 1, byte(typ << 1), byte(tid + 1)}
	z := 0
	for _, c := range rbsp {
		if z >= 2 && c <= 3 {
			out = append(out, 3)
			z = 0
		}
		out = append(out, c)
		if c == 0 {
			z++
		} else {
			z = 0
		}
	}
	return out
}

// ---- arithmetic encoder / bin source ----------------------------------------

type genSource struct {
	rng *rand.Rand
	w   *bitW

	low, rg     int
	outstanding int
	first       bool

	ctbsLeft int
	ends     []int // byte offsets (in w.b) of substream ends
	run      int
	pPCM     int
	pcmUsed  int
}

func newGenSource(seed int64) *genSource {
	return &genSource{rng: rand.New(rand.NewSource(seed)), w: &bitW{}}
}

func (g *genSource) putBit(b int) {
	if g.first {
		g.first = false
	} else {
		g.w.bit(b)
	}
	for ; g.outstanding > 0; g.outstanding-- {
		g.w.bit(1 - b)
	}
}

func (g *genSource) renorm() {
	for g.rg < 256 {
		switch {
		case g.low < 256:
			g.putBit(0)
		case g.low >= 512:
			g.low -= 512
			g.putBit(1)
		default:
			g.low -= 256
			g.outstanding++
		}
		g.rg <<= 1
		g.low <<= 1
	}
}

func (g *genSource) start() {
	g.low, g.rg, g.outstanding, g.first = 0, 510, 0, true
}

func (g *genSource) prob(ctx int) int {
	switch {
	case ctx >= ctxSplitCu && ctx < ctxSplitCu+3:
		return 55
	case ctx >= ctxSkip && ctx < ctxSkip+3:
		return 30
	case ctx == ctxPredMode:
		return 35
	case ctx == ctxCuQpDelta:
		return 25
	case ctx == ctxCuQpDelta+1:
		return 12
	case ctx == ctxTqBypass:
		return 25
	case ctx >= ctxMvdGt0 && ctx <= ctxMvdGt1:
		return 35
	case ctx >= ctxRefIdx && ctx < ctxRefIdx+2:
		return 40
	case ctx >= ctxLastX && ctx < ctxLastY+18:
		return 40
	case ctx >= ctxSig && ctx < ctxSig+44:
		return 35
	case ctx >= ctxGt1 && ctx < ctxGt2+6:
		return 30
	case ctx == ctxSaoMerge:
		return 30
	case ctx == ctxCuChromaQpFlag:
		return 40
	}
	return 50
}

func (g *genSource) bin(ctx int, st *uint8) int {
	b := 0
	if g.rng.Intn(100) < g.prob(ctx) {
		b = 1
	}
	state := *st >> 1
	mps := int(*st & 1)
	lps := int(rangeTabLps[state][(g.rg>>6)&3])
	g.rg -= lps
	if b != mps {
		g.low += g.rg
		g.rg = lps
		if state == 0 {
			mps = 1 - mps
		}
		state = transIdxLps[state]
	} else if state < 62 {
		state++
	}
	*st = state<<1 | uint8(mps)
	g.renorm()
	return b
}

func (g *genSource) bypass() int {
	b := 0
	if g.run < 5 && g.rng.Intn(2) == 1 {
		b = 1
		g.run++
	} else {
		g.run = 0
	}
	g.low <<= 1
	if b == 1 {
		g.low += g.rg
	}
	switch {
	case g.low >= 1024:
		g.putBit(1)
		g.low -= 1024
	case g.low < 512:
		g.putBit(0)
	default:
		g.low -= 512
		g.outstanding++
	}
	return b
}

func (g *genSource) flush() {
	g.rg = 2
	g.renorm()
	g.putBit((g.low >> 9) & 1)
	g.w.u(uint64((g.low>>7)&3)|1, 2)
	g.w.alignZero()
}

func (g *genSource) terminate(kind int) int {
	v := 0
	switch kind {
	case termEndOfSlice:
		g.ctbsLeft--
		if g.ctbsLeft == 0 {
			v = 1
		}
	case termEndOfSubset:
		v = 1
	case termPCM:
		if g.rng.Intn(100) < g.pPCM {
			v = 1
			g.pcmUsed++
		}
	}
	g.rg -= 2
	if v == 1 {
		g.low += g.rg
		g.flush()
		if kind != termPCM {
			g.ends = append(g.ends, len(g.w.b))
		}
	} else {
		g.renorm()
	}
	return v
}

func (g *genSource) pcmBits(n int) uint32 {
	v := uint32(g.rng.Intn(1 << uint(n)))
	g.w.u(uint64(v), n)
	return v
}

// ---- stream configuration ---------------------------------------------------

type gcfg struct {
	w, h                   int
	chroma                 int
	bd                     int
	log2Ctb, log2MinCb     int
	log2MinTb, log2MaxTb   int
	depthInter, depthIntra int
	amp, sao               bool
	pcm                    bool
	pcmBd                  [2]int
	pcmMin, pcmMax         int
	pcmLF                  bool
	scaling                int // 0 off, 1 default, 2 sps lists, 3 pps lists
	temporalMvp, strong    bool
	ltPresent              bool
	tsRot, tsCtx           bool
	implRdpcm, explRdpcm   bool
	intraSmoothOff         bool
	highPrec, persRice     bool
	tilesC, tilesR         int
	tilesUniform           bool
	loopAcrossTiles        bool
	wpp                    bool
	depSlices              bool
	signHide               bool
	cabacInitPresent       bool
	initQp                 int
	constrained            bool
	tskip                  bool
	log2MaxTs              int
	cuQpDelta              bool
	qpDepth                int
	cbOff, crOff           int
	sliceChromaOff         bool
	wp, wbi                bool
	tqBypass               bool
	loopAcrossSlices       bool
	dbControl, dbOverride  bool
	dbDis                  bool
	dbBeta, dbTc           int
	listsMod               bool
	parMrg                 int
	cross                  bool
	cqpList                [][2]int
	saoScale               [2]int
	outputFlag             bool
	extraBits              int
	sliceHdrExt            bool
	frames                 int
	sliceMode              int // 0 one slice, 1 several slices, 2 slices + dependent segments
	pPCM                   int
}

type segPlan struct {
	startTs, count int
	dependent      bool
	entries        int
}

type rpsEnt struct {
	poc  int
	used bool
}

type refPic struct {
	poc int
	lt  bool
}

type picSpec struct {
	poc, nalType int
	idr          bool
	neg, pos     []rpsEnt
	lt           []rpsEnt
	numCurr      int
	ptype        int
	tmvp         bool
	colFromL0    bool
	colRef       int
	numRef       [2]int
	override     bool
	listModFlag  [2]bool
	listEntry    [2][16]int
	picOutput    bool
}

type sliceSpec struct {
	sliceType         int
	saoL, saoC        bool
	mvdL1Zero         bool
	cabacInit         bool
	mergeSub          int
	qpDelta           int
	cb, cr            int
	cuChromaQp        bool
	dbOverride, dbDis bool
	beta, tc          int
	lfAcross          bool
	extra             int
}

const log2PocLsb = 8

func writePTL(w *bitW, c gcfg) {
	prof := 1
	if c.bd > 8 {
		prof = 2
	}
	if c.chroma != 1 || c.bd > 10 || c.tsRot || c.tsCtx || c.implRdpcm || c.explRdpcm || c.cross || c.persRice || c.highPrec || c.intraSmoothOff || c.log2MaxTs > 2 || len(c.cqpList) > 0 || c.saoScale != [2]int{} {
		prof = 4
	}
	w.u(0, 2)
	w.u(0, 1)
	w.u(uint64(prof), 5)
	w.u(1<<uint(31-prof), 32)
	w.u(0, 4)
	w.u(0, 32)
	w.u(0, 11)
	w.u(0, 1)
	w.u(120, 8)
}

func writeScalingList(w *bitW, rng *rand.Rand) {
	for sz := 0; sz < 4; sz++ {
		step := 1
		if sz == 3 {
			step = 3
		}
		for m := 0; m < 6; m += step {
			pred := rng.Intn(3) != 0
			w.flag(pred)
			if !pred {
				maxd := m
				if sz == 3 {
					maxd = m / 3
				}
				w.ue(uint32(rng.Intn(maxd + 1)))
				continue
			}
			num := 64
			if sz == 0 {
				num = 16
			}
			next := 8
			if sz > 1 {
				dc := 8 + rng.Intn(60)
				w.se(int32(dc - 8))
				next = dc
			}
			for i := 0; i < num; i++ {
				target := 8 + rng.Intn(70)
				delta := target - next
				if delta > 127 {
					delta -= 256
				} else if delta < -128 {
					delta += 256
				}
				w.se(int32(delta))
				next = target
			}
		}
	}
}

func writePredWeights(w *bitW, rng *rand.Rand, c gcfg, st int, wcount [2]int) {
	ld := rng.Intn(8)
	w.ue(uint32(ld))
	if c.chroma != 0 {
		cd := rng.Intn(8)
		w.se(int32(cd - ld))
	}
	nl := 1
	if st == sliceB {
		nl = 2
	}
	half := 128
	if c.highPrec {
		half = 1 << uint(c.bd-1)
	}
	for l := 0; l < nl; l++ {
		var lf, cf [16]bool
		for i := 0; i < wcount[l]; i++ {
			lf[i] = rng.Intn(2) == 0
			w.flag(lf[i])
		}
		if c.chroma != 0 {
			for i := 0; i < wcount[l]; i++ {
				cf[i] = rng.Intn(2) == 0
				w.flag(cf[i])
			}
		}
		for i := 0; i < wcount[l]; i++ {
			if lf[i] {
				w.se(int32(rng.Intn(60) - 30))
				w.se(int32(rng.Intn(2*half) - half))
			}
			if cf[i] {
				for j := 0; j < 2; j++ {
					w.se(int32(rng.Intn(60) - 30))
					w.se(int32(rng.Intn(4*half) - 2*half))
				}
			}
		}
	}
}

// planSegments splits the picture into slice segments along units that keep
// the tile / wavefront constraints.
func planSegments(rng *rand.Rand, c gcfg, pps *PPS, sps *SPS) []segPlan {
	nCtb := sps.wCtb * sps.hCtb
	// unit boundaries in tile scan order
	var units [][2]int // start, count
	switch {
	case pps.tiles:
		start := 0
		for ts := 1; ts <= nCtb; ts++ {
			if ts == nCtb || pps.tileID[ts] != pps.tileID[ts-1] {
				units = append(units, [2]int{start, ts - start})
				start = ts
			}
		}
	case pps.wpp:
		for r := 0; r < sps.hCtb; r++ {
			units = append(units, [2]int{r * sps.wCtb, sps.wCtb})
		}
	default:
		for i := 0; i < nCtb; i++ {
			units = append(units, [2]int{i, 1})
		}
	}
	var segs []segPlan
	i := 0
	for i < len(units) {
		n := len(units) - i
		if c.sliceMode != 0 {
			n = 1 + rng.Intn(min(len(units)-i, 4))
		}
		cnt := 0
		for _, u := range units[i : i+n] {
			cnt += u[1]
		}
		sg := segPlan{startTs: units[i][0], count: cnt}
		if len(segs) > 0 && c.sliceMode == 2 && c.depSlices && rng.Intn(2) == 0 {
			sg.dependent = true
		}
		// substream boundaries strictly inside the segment
		for ts := sg.startTs + 1; ts < sg.startTs+cnt; ts++ {
			rs := pps.tsToRs[ts]
			x := rs % sps.wCtb
			nt := pps.tiles && pps.tileID[ts] != pps.tileID[ts-1]
			nr := false
			if pps.wpp {
				cs := 0
				for k := 0; k < pps.numTileCols; k++ {
					if x >= pps.colBd[k] {
						cs = pps.colBd[k]
					}
				}
				nr = x == cs
			}
			if nt || nr {
				sg.entries++
			}
		}
		segs = append(segs, sg)
		i += n
	}
	return segs
}

func (c gcfg) pixFmt() string {
	suffix := ""
	switch c.bd {
	case 10:
		suffix = "10le"
	case 12:
		suffix = "12le"
	}
	switch c.chroma {
	case 0:
		return "gray" + suffix
	case 1:
		return "yuv420p" + suffix
	case 2:
		return "yuv422p" + suffix
	}
	return "yuv444p" + suffix
}

// genStream builds an Annex B stream and reports statistics of the generator.
func genStream(t testing.TB, c gcfg, seed int64) ([]byte, map[string]int) {
	rng := rand.New(rand.NewSource(seed * 7919))
	var out bytes.Buffer
	d := NewDecoder()
	feed := func(nal []byte) {
		out.Write(nal)
		if err := d.handleNAL(nal[4:]); err != nil {
			genFatal("parameter set: %v", err)
		}
	}
	ctb := 1 << uint(c.log2Ctb)
	wCtb := (c.w + ctb - 1) / ctb
	hCtb := (c.h + ctb - 1) / ctb

	// VPS
	{
		w := &bitW{}
		w.u(0, 4)
		w.u(3, 2)
		w.u(0, 6)
		w.u(0, 3)
		w.flag(true)
		w.u(0xffff, 16)
		writePTL(w, c)
		w.flag(false)
		w.ue(7)
		w.ue(4)
		w.ue(0)
		w.u(0, 6)
		w.ue(0)
		w.flag(false)
		w.flag(false)
		w.trailing()
		out.Write(nalWrap(nalVPS, 0, w.b))
	}
	// SPS
	{
		w := &bitW{}
		w.u(0, 4)
		w.u(0, 3)
		w.flag(true)
		writePTL(w, c)
		w.ue(0)
		w.ue(uint32(c.chroma))
		if c.chroma == 3 {
			w.flag(false)
		}
		w.ue(uint32(c.w))
		w.ue(uint32(c.h))
		w.flag(false)
		w.ue(uint32(c.bd - 8))
		w.ue(uint32(c.bd - 8))
		w.ue(log2PocLsb - 4)
		w.flag(false)
		w.ue(7)
		w.ue(4)
		w.ue(0)
		w.ue(uint32(c.log2MinCb - 3))
		w.ue(uint32(c.log2Ctb - c.log2MinCb))
		w.ue(uint32(c.log2MinTb - 2))
		w.ue(uint32(c.log2MaxTb - c.log2MinTb))
		w.ue(uint32(c.depthInter))
		w.ue(uint32(c.depthIntra))
		w.flag(c.scaling != 0)
		if c.scaling != 0 {
			w.flag(c.scaling == 2)
			if c.scaling == 2 {
				writeScalingList(w, rng)
			}
		}
		w.flag(c.amp)
		w.flag(c.sao)
		w.flag(c.pcm)
		if c.pcm {
			w.u(uint64(c.pcmBd[0]-1), 4)
			w.u(uint64(c.pcmBd[1]-1), 4)
			w.ue(uint32(c.pcmMin - 3))
			w.ue(uint32(c.pcmMax - c.pcmMin))
			w.flag(c.pcmLF)
		}
		w.ue(0)
		w.flag(c.ltPresent)
		if c.ltPresent {
			w.ue(0)
		}
		w.flag(c.temporalMvp)
		w.flag(c.strong)
		w.flag(false)
		ext := c.tsRot || c.tsCtx || c.implRdpcm || c.explRdpcm || c.intraSmoothOff || c.highPrec || c.persRice
		w.flag(ext)
		if ext {
			w.flag(true)
			w.u(0, 7)
			w.flag(c.tsRot)
			w.flag(c.tsCtx)
			w.flag(c.implRdpcm)
			w.flag(c.explRdpcm)
			w.flag(false)
			w.flag(c.intraSmoothOff)
			w.flag(c.highPrec)
			w.flag(c.persRice)
			w.flag(false)
		}
		w.trailing()
		feed(nalWrap(nalSPS, 0, w.b))
	}
	// PPS
	tilesOn := c.tilesC > 1 || c.tilesR > 1
	var numRefDefault [2]int
	{
		w := &bitW{}
		w.ue(0)
		w.ue(0)
		w.flag(c.depSlices)
		w.flag(c.outputFlag)
		w.u(uint64(c.extraBits), 3)
		w.flag(c.signHide)
		w.flag(c.cabacInitPresent)
		numRefDefault = [2]int{1 + rng.Intn(3), 1 + rng.Intn(3)}
		w.ue(uint32(numRefDefault[0] - 1))
		w.ue(uint32(numRefDefault[1] - 1))
		w.se(int32(c.initQp - 26))
		w.flag(c.constrained)
		w.flag(c.tskip)
		w.flag(c.cuQpDelta)
		if c.cuQpDelta {
			w.ue(uint32(c.qpDepth))
		}
		w.se(int32(c.cbOff))
		w.se(int32(c.crOff))
		w.flag(c.sliceChromaOff)
		w.flag(c.wp)
		w.flag(c.wbi)
		w.flag(c.tqBypass)
		w.flag(tilesOn)
		w.flag(c.wpp)
		if tilesOn {
			w.ue(uint32(c.tilesC - 1))
			w.ue(uint32(c.tilesR - 1))
			w.flag(c.tilesUniform)
			if !c.tilesUniform {
				rem := wCtb
				for i := 0; i < c.tilesC-1; i++ {
					v := 1 + rng.Intn(rem-(c.tilesC-1-i))
					rem -= v
					w.ue(uint32(v - 1))
				}
				rem = hCtb
				for i := 0; i < c.tilesR-1; i++ {
					v := 1 + rng.Intn(rem-(c.tilesR-1-i))
					rem -= v
					w.ue(uint32(v - 1))
				}
			}
			w.flag(c.loopAcrossTiles)
		}
		w.flag(c.loopAcrossSlices)
		w.flag(c.dbControl)
		if c.dbControl {
			w.flag(c.dbOverride)
			w.flag(c.dbDis)
			if !c.dbDis {
				w.se(int32(c.dbBeta))
				w.se(int32(c.dbTc))
			}
		}
		w.flag(c.scaling == 3)
		if c.scaling == 3 {
			writeScalingList(w, rng)
		}
		w.flag(c.listsMod)
		w.ue(uint32(c.parMrg - 2))
		w.flag(c.sliceHdrExt)
		rext := c.log2MaxTs > 2 || c.cross || len(c.cqpList) > 0 || c.saoScale != [2]int{}
		w.flag(rext)
		if rext {
			w.flag(true)
			w.u(0, 7)
			if c.tskip {
				w.ue(uint32(c.log2MaxTs - 2))
			}
			w.flag(c.cross)
			w.flag(len(c.cqpList) > 0)
			if len(c.cqpList) > 0 {
				w.ue(uint32(rng.Intn(c.log2Ctb - c.log2MinCb + 1)))
				w.ue(uint32(len(c.cqpList) - 1))
				for _, e := range c.cqpList {
					w.se(int32(e[0]))
					w.se(int32(e[1]))
				}
			}
			w.ue(uint32(c.saoScale[0]))
			w.ue(uint32(c.saoScale[1]))
		}
		w.trailing()
		feed(nalWrap(nalPPS, 0, w.b))
	}
	sps := d.sps[0]
	pps := d.pps[0]
	if err := pps.setup(sps); err != nil {
		genFatal("%v", err)
	}
	stats := map[string]int{}

	// coding order of POCs: mini-GOPs of 1..3 pictures, last one first
	order := []int{0}
	for base := 0; len(order) < c.frames; {
		g := 1 + rng.Intn(3)
		pocs := make([]int, 0, g)
		for i := 1; i <= g; i++ {
			pocs = append(pocs, base+i)
		}
		first := pocs[len(pocs)-1]
		rest := append([]int(nil), pocs[:len(pocs)-1]...)
		rng.Shuffle(len(rest), func(i, j int) { rest[i], rest[j] = rest[j], rest[i] })
		order = append(order, first)
		order = append(order, rest...)
		base += g
	}
	order = order[:c.frames]

	var held []refPic
	for pi, poc := range order {
		ps := &picSpec{poc: poc, idr: pi == 0, nalType: 1, picOutput: true}
		if ps.idr {
			ps.nalType = nalIdrWRadl
			held = nil
		}
		var keep []refPic
		for _, r := range held {
			if rng.Intn(4) != 0 || len(held) <= 1 {
				keep = append(keep, r)
			}
		}
		if len(keep) > 4 {
			keep = keep[len(keep)-4:]
		}
		for i := range keep {
			r := &keep[i]
			if c.ltPresent && !r.lt && rng.Intn(4) == 0 {
				r.lt = true
			}
			e := rpsEnt{r.poc, rng.Intn(4) != 0}
			switch {
			case r.lt:
				ps.lt = append(ps.lt, e)
			case r.poc < poc:
				ps.neg = append(ps.neg, e)
			default:
				ps.pos = append(ps.pos, e)
			}
		}
		sort.Slice(ps.neg, func(i, j int) bool { return ps.neg[i].poc > ps.neg[j].poc })
		sort.Slice(ps.pos, func(i, j int) bool { return ps.pos[i].poc < ps.pos[j].poc })
		sort.Slice(ps.lt, func(i, j int) bool { return ps.lt[i].poc > ps.lt[j].poc })
		for _, l := range [][]rpsEnt{ps.neg, ps.pos, ps.lt} {
			for _, e := range l {
				if e.used {
					ps.numCurr++
				}
			}
		}
		ps.ptype = sliceI
		if !ps.idr && ps.numCurr > 0 {
			ps.ptype = sliceP
			if rng.Intn(2) == 0 {
				ps.ptype = sliceB
			}
		}
		ps.numRef = [2]int{1 + rng.Intn(3), 1 + rng.Intn(3)}
		ps.override = rng.Intn(2) == 0
		ps.tmvp = c.temporalMvp && rng.Intn(4) != 0
		ps.colFromL0 = rng.Intn(2) == 0
		ps.colRef = rng.Intn(3)
		for l := 0; l < 2; l++ {
			ps.listModFlag[l] = c.listsMod && ps.numCurr > 1 && rng.Intn(2) == 0
			for i := range ps.listEntry[l] {
				ps.listEntry[l][i] = rng.Intn(max(ps.numCurr, 1))
			}
		}
		if c.outputFlag && rng.Intn(4) == 0 {
			ps.picOutput = false
		}
		segs := planSegments(rng, c, pps, sps)
		var lastIndep *sliceSpec
		var lastWcount [2]int
		for si, sg := range segs {
			g := newGenSource(seed*131 + int64(pi*17+si))
			g.pPCM = c.pPCM
			g.ctbsLeft = sg.count
			ss := &sliceSpec{}
			if !sg.dependent {
				ss.sliceType = ps.ptype
				if ps.ptype != sliceI && rng.Intn(5) == 0 {
					ss.sliceType = sliceI
				}
				ss.saoL = c.sao && rng.Intn(4) != 0
				ss.saoC = c.sao && c.chroma != 0 && rng.Intn(4) != 0
				ss.mvdL1Zero = rng.Intn(3) == 0
				ss.cabacInit = c.cabacInitPresent && rng.Intn(2) == 0
				ss.mergeSub = rng.Intn(5)
				ss.qpDelta = rng.Intn(15) - 7
				if c.initQp+ss.qpDelta < -6*(c.bd-8) || c.initQp+ss.qpDelta > 51 {
					ss.qpDelta = 0
				}
				ss.cb, ss.cr = rng.Intn(7)-3, rng.Intn(7)-3
				ss.cuChromaQp = rng.Intn(3) != 0
				ss.dbOverride = c.dbControl && c.dbOverride && rng.Intn(2) == 0
				ss.dbDis = rng.Intn(4) == 0
				ss.beta, ss.tc = rng.Intn(13)-6, rng.Intn(13)-6
				ss.lfAcross = rng.Intn(2) == 0
				lastIndep = ss
			} else {
				ss = lastIndep
			}
			w := &bitW{}
			w.flag(si == 0)
			if ps.nalType >= nalBlaWLP && ps.nalType <= 23 {
				w.flag(false)
			}
			w.ue(0)
			if si != 0 {
				if c.depSlices {
					w.flag(sg.dependent)
				}
				w.u(uint64(pps.tsToRs[sg.startTs]), ceilLog2(sps.wCtb*sps.hCtb))
			}
			if !sg.dependent {
				for i := 0; i < c.extraBits; i++ {
					w.bit(rng.Intn(2))
				}
				w.ue(uint32(ss.sliceType))
				if c.outputFlag {
					w.flag(ps.picOutput)
				}
				if !ps.idr {
					w.u(uint64(ps.poc&(1<<log2PocLsb-1)), log2PocLsb)
					w.flag(false)
					w.ue(uint32(len(ps.neg)))
					w.ue(uint32(len(ps.pos)))
					prev := ps.poc
					for _, e := range ps.neg {
						w.ue(uint32(prev - e.poc - 1))
						w.flag(e.used)
						prev = e.poc
					}
					prev = ps.poc
					for _, e := range ps.pos {
						w.ue(uint32(e.poc - prev - 1))
						w.flag(e.used)
						prev = e.poc
					}
					if c.ltPresent {
						w.ue(uint32(len(ps.lt)))
						acc := 0
						for i, e := range ps.lt {
							w.u(uint64(e.poc&(1<<log2PocLsb-1)), log2PocLsb)
							w.flag(e.used)
							msb := rng.Intn(2) == 0
							w.flag(msb)
							if msb {
								m := (ps.poc >> log2PocLsb) - (e.poc >> log2PocLsb)
								w.ue(uint32(m - acc))
								acc = m
							}
							_ = i
						}
					}
					if c.temporalMvp {
						w.flag(ps.tmvp)
					}
				}
				if c.sao {
					w.flag(ss.saoL)
					if c.chroma != 0 {
						w.flag(ss.saoC)
					}
				}
				var wcount [2]int
				if ss.sliceType != sliceI {
					w.flag(ps.override)
					wcount = numRefDefault
					if ps.override {
						wcount = ps.numRef
						w.ue(uint32(ps.numRef[0] - 1))
						if ss.sliceType == sliceB {
							w.ue(uint32(ps.numRef[1] - 1))
						}
					}
					if ss.sliceType != sliceB {
						wcount[1] = 0
					}
					if c.listsMod && ps.numCurr > 1 {
						bits := ceilLog2(ps.numCurr)
						for l := 0; l < 2; l++ {
							if l == 1 && ss.sliceType != sliceB {
								break
							}
							w.flag(ps.listModFlag[l])
							if ps.listModFlag[l] {
								for i := 0; i < wcount[l]; i++ {
									w.u(uint64(ps.listEntry[l][i]), bits)
								}
							}
						}
					}
					if ss.sliceType == sliceB {
						w.flag(ss.mvdL1Zero)
					}
					if c.cabacInitPresent {
						w.flag(ss.cabacInit)
					}
					if ps.tmvp && c.temporalMvp {
						cf := true
						if ss.sliceType == sliceB {
							cf = ps.colFromL0
							w.flag(cf)
						}
						n := wcount[0]
						if !cf {
							n = wcount[1]
						}
						if n > 1 {
							w.ue(uint32(ps.colRef % n))
						}
					}
					if (c.wp && ss.sliceType == sliceP) || (c.wbi && ss.sliceType == sliceB) {
						writePredWeights(w, rng, c, ss.sliceType, wcount)
					}
					w.ue(uint32(ss.mergeSub))
				}
				lastWcount = wcount
				w.se(int32(ss.qpDelta))
				if c.sliceChromaOff {
					w.se(int32(ss.cb))
					w.se(int32(ss.cr))
				}
				if len(c.cqpList) > 0 {
					w.flag(ss.cuChromaQp)
				}
				if c.dbControl && c.dbOverride {
					w.flag(ss.dbOverride)
				}
				dis := c.dbControl && c.dbDis
				if ss.dbOverride {
					dis = ss.dbDis
					w.flag(dis)
					if !dis {
						w.se(int32(ss.beta))
						w.se(int32(ss.tc))
					}
				}
				if c.loopAcrossSlices && (ss.saoL || ss.saoC || !dis) {
					w.flag(ss.lfAcross)
				}
			}
			entryPos := -1
			if tilesOn || c.wpp {
				w.ue(uint32(sg.entries))
				if sg.entries > 0 {
					w.ue(31)
					entryPos = w.n
					for i := 0; i < sg.entries; i++ {
						w.u(0, 32)
					}
				}
			}
			if c.sliceHdrExt {
				n := rng.Intn(3)
				w.ue(uint32(n))
				for i := 0; i < n; i++ {
					w.u(uint64(rng.Intn(256)), 8)
				}
			}
			w.trailing()
			hdr := w.b
			_ = lastWcount

			// run the decoder in generator mode on this slice segment
			d.cab.gen = g
			rbsp := append(append([]byte(nil), hdr...), make([]byte, 16)...)
			r := &bitReader{b: rbsp}
			sh, err := d.parseSliceHeader(r, ps.nalType, 0, d.lastIndep)
			if err != nil {
				genFatal("pic %d seg %d: slice header: %v", pi, si, err)
			}
			if sh.dataOffset != len(hdr) {
				genFatal("pic %d seg %d: header length %d parsed %d", pi, si, len(hdr), sh.dataOffset)
			}
			if sh.firstSlice {
				d.finishPicture()
				if err := d.startPicture(sh); err != nil {
					genFatal("pic %d: startPicture: %v", pi, err)
				}
			}
			if !sh.dependent {
				d.lastIndep = sh
				if err := d.setupRefLists(sh); err != nil {
					genFatal("pic %d: ref lists: %v", pi, err)
				}
			}
			sh.idx = len(d.slices)
			d.slices = append(d.slices, sh)
			g.start()
			if err := d.decodeSliceData(sh, rbsp); err != nil {
				genFatal("pic %d seg %d: generate: %v", pi, si, err)
			}
			d.cab.gen = nil
			if g.ctbsLeft != 0 {
				genFatal("pic %d seg %d: %d CTBs left", pi, si, g.ctbsLeft)
			}
			stats["pcm"] += g.pcmUsed
			data := g.w.b
			// patch entry point offsets with the substream sizes
			if entryPos >= 0 && len(g.ends) == sg.entries+1 {
				prev := 0
				hw := &bitW{b: append([]byte(nil), hdr...), n: len(hdr) * 8}
				for i := 0; i < sg.entries; i++ {
					sz := g.ends[i] - prev
					prev = g.ends[i]
					putBits(hw.b, entryPos+32*i, 32, uint64(sz-1))
				}
				hdr = hw.b
			} else if entryPos >= 0 {
				genFatal("pic %d seg %d: %d substreams, expected %d", pi, si, len(g.ends), sg.entries+1)
			}
			feedNAL := nalWrap(ps.nalType, 0, append(append([]byte(nil), hdr...), data...))
			out.Write(feedNAL)
			stats["slices"]++
			if sg.dependent {
				stats["dependent"]++
			}
		}
		// update the reference list for the next pictures
		held = append(keep, refPic{poc: poc})
	}
	d.finishPicture()
	return out.Bytes(), stats
}

type genPanic string

func genFatal(f string, a ...any) { panic(genPanic(fmt.Sprintf(f, a...))) }

func putBits(b []byte, pos, n int, v uint64) {
	for i := 0; i < n; i++ {
		bit := byte(v>>uint(n-1-i)) & 1
		p := pos + i
		b[p>>3] = b[p>>3]&^(0x80>>uint(p&7)) | bit<<(7-uint(p&7))
	}
}

// randomCfg draws a random but valid configuration.
func randomCfg(rng *rand.Rand) gcfg {
	var c gcfg
	c.chroma = rng.Intn(4)
	c.bd = []int{8, 8, 10, 12}[rng.Intn(4)]
	c.log2Ctb = 4 + rng.Intn(3)
	c.log2MinCb = 3 + rng.Intn(c.log2Ctb-2)
	c.log2MinTb = 2 + rng.Intn(c.log2MinCb-2)
	c.log2MaxTb = c.log2MinTb + rng.Intn(min(5, c.log2Ctb)-c.log2MinTb+1)
	c.depthInter = rng.Intn(c.log2Ctb - c.log2MinTb + 1)
	c.depthIntra = rng.Intn(c.log2Ctb - c.log2MinTb + 1)
	minCb := 1 << uint(c.log2MinCb)
	c.w = minCb * (3 + rng.Intn(14))
	c.h = minCb * (2 + rng.Intn(10))
	for c.w > 200 {
		c.w -= minCb
	}
	for c.h > 130 {
		c.h -= minCb
	}
	c.amp = rng.Intn(2) == 0
	c.sao = rng.Intn(3) != 0
	c.pcm = rng.Intn(3) == 0 && c.chroma != 0 // ffmpeg miscounts the PCM size of monochrome streams
	if c.pcm {
		c.pcmBd = [2]int{1 + rng.Intn(c.bd), 1 + rng.Intn(c.bd)}
		lo := min(c.log2MinCb, 5)
		hi := min(c.log2Ctb, 5)
		c.pcmMin = lo + rng.Intn(hi-lo+1)
		c.pcmMax = c.pcmMin + rng.Intn(hi-c.pcmMin+1)
		c.pcmLF = rng.Intn(2) == 0
		c.pPCM = 20 + rng.Intn(60)
	}
	c.scaling = rng.Intn(4)
	c.temporalMvp = rng.Intn(3) != 0
	c.strong = rng.Intn(2) == 0
	c.ltPresent = rng.Intn(3) == 0
	if rng.Intn(2) == 0 {
		c.tsRot = rng.Intn(2) == 0
		c.tsCtx = rng.Intn(2) == 0
		c.implRdpcm = rng.Intn(2) == 0
		c.explRdpcm = rng.Intn(2) == 0
		c.intraSmoothOff = rng.Intn(3) == 0
		c.highPrec = rng.Intn(2) == 0
		if c.highPrec {
			c.wp, c.wbi = false, false
		}
		c.persRice = rng.Intn(2) == 0
	}
	wCtb := (c.w + (1 << uint(c.log2Ctb)) - 1) >> uint(c.log2Ctb)
	hCtb := (c.h + (1 << uint(c.log2Ctb)) - 1) >> uint(c.log2Ctb)
	c.tilesC, c.tilesR = 1, 1
	if rng.Intn(2) == 0 {
		c.tilesC = 1 + rng.Intn(min(wCtb, 3))
		c.tilesR = 1 + rng.Intn(min(hCtb, 3))
	}
	c.tilesUniform = rng.Intn(2) == 0
	c.loopAcrossTiles = rng.Intn(2) == 0
	c.wpp = rng.Intn(3) == 0 && c.tilesC == 1 && c.tilesR == 1 // ffmpeg does not support tiles together with wavefronts
	c.depSlices = rng.Intn(2) == 0
	c.signHide = rng.Intn(2) == 0
	c.cabacInitPresent = rng.Intn(2) == 0
	c.initQp = 20 + rng.Intn(20) - 6*(c.bd-8)/2
	c.constrained = rng.Intn(4) == 0 && c.log2MinCb == 3 // ffmpeg reads outside its motion field with larger blocks
	c.tskip = rng.Intn(2) == 0
	c.log2MaxTs = 2
	if c.tskip && rng.Intn(2) == 0 {
		c.log2MaxTs = 2 + rng.Intn(c.log2MaxTb-1)
	}
	c.cuQpDelta = rng.Intn(2) == 0
	if c.cuQpDelta {
		c.qpDepth = rng.Intn(c.log2Ctb - c.log2MinCb + 1)
	}
	c.cbOff, c.crOff = rng.Intn(9)-4, rng.Intn(9)-4
	c.sliceChromaOff = rng.Intn(2) == 0
	if !c.highPrec {
		c.wp = rng.Intn(3) == 0
		c.wbi = rng.Intn(3) == 0
	}
	c.tqBypass = rng.Intn(3) == 0
	c.loopAcrossSlices = rng.Intn(2) == 0
	c.dbControl = rng.Intn(2) == 0
	if c.dbControl {
		c.dbOverride = rng.Intn(2) == 0
		c.dbDis = rng.Intn(4) == 0
		c.dbBeta, c.dbTc = rng.Intn(13)-6, rng.Intn(13)-6
	}
	c.listsMod = rng.Intn(2) == 0
	c.parMrg = 2 + rng.Intn(c.log2Ctb-1)
	if c.chroma == 3 {
		c.cross = rng.Intn(2) == 0
	}
	if c.chroma != 0 && rng.Intn(2) == 0 {
		n := 6
		for i := 0; i < n; i++ {
			c.cqpList = append(c.cqpList, [2]int{rng.Intn(13) - 6, rng.Intn(13) - 6})
		}
	}
	if c.bd > 10 {
		c.saoScale = [2]int{rng.Intn(c.bd - 9), rng.Intn(c.bd - 9)}
	}
	c.outputFlag = rng.Intn(4) == 0
	c.extraBits = rng.Intn(3)
	if rng.Intn(4) != 0 {
		c.extraBits = 0
	}
	c.sliceHdrExt = rng.Intn(4) == 0
	c.frames = 3 + rng.Intn(5)
	c.sliceMode = rng.Intn(3)
	return c
}

func writeAnnexB(t testing.TB, name string, data []byte) string {
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func ffmpegRaw1(t testing.TB, path, pixFmt string) ([]byte, error) {
	args := []string{"-v", "error", "-nostdin", "-threads", "1", "-i", path, "-fps_mode", "passthrough", "-f", "rawvideo", "-pix_fmt", pixFmt, "-"}
	cmd := exec.Command("ffmpeg", args...)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	var bad []string
	for _, l := range bytes.Split(se.Bytes(), []byte("\n")) {
		ls := string(bytes.TrimSpace(l))
		if ls == "" || strings.Contains(ls, "not yet tested") || strings.Contains(ls, "not tested yet") || strings.Contains(ls, "Last message repeated") {
			continue
		}
		bad = append(bad, ls)
	}
	if len(bad) > 0 {
		return so.Bytes(), fmt.Errorf("ffmpeg: %s", strings.Join(bad, "; "))
	}
	return so.Bytes(), err
}

// checkGenerated decodes the stream with ffmpeg and with this package.
func checkGenerated(t *testing.T, c gcfg, seed int64) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			if g, ok := r.(genPanic); ok {
				t.Fatalf("generator: %s", string(g))
			}
			panic(r)
		}
	}()
	data, stats := genStream(t, c, seed)
	path := writeAnnexB(t, "gen.hevc", data)
	ref, ferr := ffmpegRaw1(t, path, c.pixFmt())
	frames, derr := decodeAnnexB(t, data)
	if derr != nil {
		t.Errorf("native decode error: %v", derr)
	}
	var got []byte
	for _, f := range frames {
		got = append(got, frameBytes(f)...)
	}
	if ferr != nil {
		t.Errorf("ffmpeg: %v (frames %d native, %d ref bytes)", ferr, len(frames), len(ref))
	}
	if !bytes.Equal(ref, got) {
		fsz := 0
		if len(frames) > 0 {
			fsz = len(frameBytes(frames[0]))
		}
		msg := fmt.Sprintf("output differs: ffmpeg %d bytes, native %d bytes (frame size %d)", len(ref), len(got), fsz)
		n := min(len(ref), len(got))
		for i := 0; i < n; i++ {
			if ref[i] != got[i] {
				msg += fmt.Sprintf("; first diff at byte %d (frame %d)", i, i/max(fsz, 1))
				break
			}
		}
		keep := os.Getenv("HEVC_KEEP")
		if keep != "" {
			_ = os.WriteFile(keep, data, 0o644)
			msg += " (stream saved to " + keep + ")"
		}
		t.Errorf("%s; cfg %+v stats %v", msg, c, stats)
	}
}

func TestGenerated(t *testing.T) {
	if !haveFFmpeg() {
		t.Skip("no ffmpeg")
	}
	n := 40
	if testing.Short() {
		n = 8
	}
	start := int64(0)
	if v := os.Getenv("HEVC_SEED"); v != "" {
		fmt.Sscan(v, &start)
		n = 1
	}
	for seed := start; seed < start+int64(n); seed++ {
		seed := seed
		t.Run(fmt.Sprint("seed", seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			checkGenerated(t, randomCfg(rng), seed)
		})
	}
}

func baseCfg() gcfg {
	return gcfg{w: 128, h: 96, chroma: 1, bd: 8, log2Ctb: 5, log2MinCb: 3, log2MinTb: 2, log2MaxTb: 5,
		depthInter: 2, depthIntra: 2, tilesC: 1, tilesR: 1, initQp: 30, log2MaxTs: 2, parMrg: 2,
		tilesUniform: true, loopAcrossTiles: true, frames: 5, temporalMvp: true, strong: true}
}

func TestGenFeatures(t *testing.T) {
	if !haveFFmpeg() {
		t.Skip("no ffmpeg")
	}
	feats := []struct {
		name string
		f    func(c *gcfg)
	}{
		{"base", func(c *gcfg) {}},
		{"intra-only", func(c *gcfg) { c.frames = 1 }},
		{"c400", func(c *gcfg) { c.chroma = 0 }},
		{"c422", func(c *gcfg) { c.chroma = 2 }},
		{"c444", func(c *gcfg) { c.chroma = 3 }},
		{"bd10", func(c *gcfg) { c.bd = 10 }},
		{"bd12", func(c *gcfg) { c.bd = 12 }},
		{"ctb16", func(c *gcfg) { c.log2Ctb = 4; c.log2MaxTb = 4 }},
		{"ctb64", func(c *gcfg) { c.log2Ctb = 6 }},
		{"mincb16", func(c *gcfg) { c.log2MinCb = 4 }},
		{"amp", func(c *gcfg) { c.amp = true }},
		{"sao", func(c *gcfg) { c.sao = true }},
		{"signhide", func(c *gcfg) { c.signHide = true }},
		{"tskip", func(c *gcfg) { c.tskip = true }},
		{"tskip-ts32", func(c *gcfg) { c.tskip = true; c.log2MaxTs = 5 }},
		{"cuqpdelta", func(c *gcfg) { c.cuQpDelta = true; c.qpDepth = 1 }},
		{"cbcroff", func(c *gcfg) { c.cbOff = 3; c.crOff = -4; c.sliceChromaOff = true }},
		{"constrained", func(c *gcfg) { c.constrained = true }},
		{"wp", func(c *gcfg) { c.wp = true; c.wbi = true }},
		{"tqbypass", func(c *gcfg) { c.tqBypass = true }},
		{"deblock-ctl", func(c *gcfg) { c.dbControl = true; c.dbOverride = true; c.dbBeta = 2; c.dbTc = -2 }},
		{"loop-slices", func(c *gcfg) { c.loopAcrossSlices = true; c.sliceMode = 1 }},
		{"cqp-422-dep", func(c *gcfg) { c.chroma = 2; c.bd = 12; c.cqpList = [][2]int{{6, 1}, {-5, -6}, {1, -1}, {-2, 2}, {2, 4}, {-6, 0}}; c.cuQpDelta = true; c.sliceMode = 2; c.depSlices = true; c.frames = 6 }},
		{"cqp-422-i-delta-nodb", func(c *gcfg) { c.chroma = 2; c.cqpList = [][2]int{{6, 1}, {-5, -6}, {1, -1}, {-2, 2}, {2, 4}, {-6, 0}}; c.cuQpDelta = true; c.frames = 1; c.dbControl = true; c.dbDis = true }},
		{"cqp-422-d1", func(c *gcfg) { c.chroma = 2; c.cqpList = [][2]int{{6, 1}}; c.cuQpDelta = true; c.frames = 1 }},
		{"cqp-420-d6", func(c *gcfg) { c.chroma = 1; c.cqpList = [][2]int{{6, 1}, {-5, -6}, {1, -1}, {-2, 2}, {2, 4}, {-6, 0}}; c.cuQpDelta = true; c.frames = 1 }},
		{"cqp-444-d6", func(c *gcfg) { c.chroma = 3; c.cqpList = [][2]int{{6, 1}, {-5, -6}, {1, -1}, {-2, 2}, {2, 4}, {-6, 0}}; c.cuQpDelta = true; c.frames = 1 }},
		{"d-422", func(c *gcfg) { c.chroma = 2; c.cuQpDelta = true; c.frames = 1 }},
		{"m27", func(c *gcfg) { c.log2Ctb = 6; c.log2MinCb = 5; c.log2MaxTb = 3; c.depthInter = 1; c.depthIntra = 1; c.temporalMvp = false; c.depSlices = true; c.initQp = 40; c.dbControl = true; c.dbOverride = true; c.dbBeta = -6; c.dbTc = 2; c.cqpList = [][2]int{{6, 1}, {-5, -6}, {1, -1}, {-2, 2}, {2, 4}, {-6, 0}}; c.frames = 6; c.sliceMode = 2 }},
		{"m27-nocqp", func(c *gcfg) { c.log2Ctb = 6; c.log2MinCb = 5; c.log2MaxTb = 3; c.depthInter = 1; c.depthIntra = 1; c.temporalMvp = false; c.depSlices = true; c.initQp = 40; c.dbControl = true; c.dbOverride = true; c.dbBeta = -6; c.dbTc = 2; c.frames = 6; c.sliceMode = 2 }},
		{"m27-nodb", func(c *gcfg) { c.log2Ctb = 6; c.log2MinCb = 5; c.log2MaxTb = 3; c.depthInter = 1; c.depthIntra = 1; c.temporalMvp = false; c.depSlices = true; c.initQp = 40; c.cqpList = [][2]int{{6, 1}, {-5, -6}, {1, -1}, {-2, 2}, {2, 4}, {-6, 0}}; c.frames = 6; c.sliceMode = 2 }},
		{"v27a", func(c *gcfg) { c.log2Ctb = 6; c.log2MinCb = 5; c.log2MaxTb = 3; c.depthInter = 1; c.depthIntra = 1; c.frames = 1; }},
		{"v27b", func(c *gcfg) { c.log2Ctb = 6; c.log2MinCb = 5; c.log2MaxTb = 3; c.depthInter = 1; c.depthIntra = 1; c.frames = 1; c.dbControl = true; c.dbOverride = true; c.dbBeta = -6; c.dbTc = 2; }},
		{"v27c", func(c *gcfg) { c.log2Ctb = 6; c.log2MinCb = 5; c.log2MaxTb = 3; c.depthInter = 1; c.depthIntra = 1; c.frames = 1; c.depSlices = true; c.sliceMode = 2; }},
		{"v27d", func(c *gcfg) { c.log2Ctb = 6; c.log2MinCb = 5; c.log2MaxTb = 3; c.depthInter = 1; c.depthIntra = 1; c.frames = 1; c.initQp = 40; }},
		{"v27e", func(c *gcfg) { c.log2Ctb = 6; c.log2MinCb = 5; c.log2MaxTb = 3; c.depthInter = 1; c.depthIntra = 1; c.frames = 1; c.dbControl = true; c.dbOverride = true; c.dbBeta = -6; c.dbTc = 2; c.sliceMode = 1 }},
		{"w1", func(c *gcfg) { c.dbControl = true; c.dbOverride = true; c.dbBeta = -6; c.dbTc = 2; c.sliceMode = 1; c.frames = 1; }},
		{"w2", func(c *gcfg) { c.log2Ctb = 6; c.dbControl = true; c.dbOverride = true; c.dbBeta = -6; c.dbTc = 2; c.sliceMode = 1; c.frames = 1; }},
		{"w3", func(c *gcfg) { c.log2Ctb = 6; c.log2MaxTb=3; c.dbControl = true; c.dbOverride = true; c.dbBeta = -6; c.dbTc = 2; c.sliceMode = 1; c.frames = 1; }},
		{"w4", func(c *gcfg) { c.log2Ctb = 6; c.log2MinCb=5; c.dbControl = true; c.dbOverride = true; c.dbBeta = -6; c.dbTc = 2; c.sliceMode = 1; c.frames = 1; }},
		{"cqp-422-nodelta", func(c *gcfg) { c.chroma = 2; c.cqpList = [][2]int{{6, 1}, {-5, -6}, {1, -1}, {-2, 2}, {2, 4}, {-6, 0}}; c.frames = 1 }},
		{"cqp-422-1", func(c *gcfg) { c.chroma = 2; c.cqpList = [][2]int{{6, 1}}; c.frames = 1 }},
		{"cqp-422-i-delta", func(c *gcfg) { c.chroma = 2; c.cqpList = [][2]int{{6, 1}, {-5, -6}, {1, -1}, {-2, 2}, {2, 4}, {-6, 0}}; c.cuQpDelta = true; c.frames = 1 }},
		{"cqp-422-p-nodelta", func(c *gcfg) { c.chroma = 2; c.cqpList = [][2]int{{6, 1}, {-5, -6}, {1, -1}, {-2, 2}, {2, 4}, {-6, 0}}; c.frames = 4 }},
		{"cqp-422", func(c *gcfg) { c.chroma = 2; c.cqpList = [][2]int{{6, 1}, {-5, -6}, {1, -1}, {-2, 2}, {2, 4}, {-6, 0}}; c.cuQpDelta = true }},
		{"cqp-dep", func(c *gcfg) { c.cqpList = [][2]int{{6, 1}, {-5, -6}, {1, -1}, {-2, 2}, {2, 4}, {-6, 0}}; c.cuQpDelta = true; c.sliceMode = 2; c.depSlices = true; c.frames = 6 }},
		{"slices-sao", func(c *gcfg) { c.sao = true; c.loopAcrossSlices = true; c.sliceMode = 1 }},
		{"slices-sao-noloop", func(c *gcfg) { c.sao = true; c.sliceMode = 1 }},
		{"slices", func(c *gcfg) { c.sliceMode = 1 }},
		{"dep-slices", func(c *gcfg) { c.sliceMode = 2; c.depSlices = true }},
		{"wpp", func(c *gcfg) { c.wpp = true }},
		{"wpp-slices", func(c *gcfg) { c.wpp = true; c.sliceMode = 2; c.depSlices = true }},
		{"tiles", func(c *gcfg) { c.tilesC = 2; c.tilesR = 2 }},
		{"tiles-nonuni", func(c *gcfg) { c.tilesC = 3; c.tilesR = 2; c.tilesUniform = false }},
		{"tiles-noloop", func(c *gcfg) { c.tilesC = 2; c.tilesR = 2; c.loopAcrossTiles = false; c.sao = true }},
		{"tiles-slices", func(c *gcfg) { c.tilesC = 2; c.tilesR = 2; c.sliceMode = 2; c.depSlices = true }},
		{"pcm", func(c *gcfg) { c.pcm = true; c.pcmBd = [2]int{7, 6}; c.pcmMin = 3; c.pcmMax = 5; c.pPCM = 30 }},
		{"pcm-lf", func(c *gcfg) {
			c.pcm = true
			c.pcmBd = [2]int{8, 8}
			c.pcmMin = 3
			c.pcmMax = 4
			c.pcmLF = true
			c.pPCM = 40
			c.sao = true
		}},
		{"scaling-default", func(c *gcfg) { c.scaling = 1 }},
		{"scaling-sps", func(c *gcfg) { c.scaling = 2 }},
		{"scaling-pps", func(c *gcfg) { c.scaling = 3 }},
		{"lt", func(c *gcfg) { c.ltPresent = true; c.frames = 8 }},
		{"listsmod", func(c *gcfg) { c.listsMod = true; c.frames = 8 }},
		{"parmrg", func(c *gcfg) { c.parMrg = 4 }},
		{"outputflag", func(c *gcfg) { c.outputFlag = true; c.frames = 8 }},
		{"extrabits", func(c *gcfg) { c.extraBits = 2; c.sliceHdrExt = true }},
		{"cabacinit", func(c *gcfg) { c.cabacInitPresent = true }},
		{"tsrot", func(c *gcfg) { c.tsRot = true; c.tskip = true; c.tqBypass = true }},
		{"tsctx", func(c *gcfg) { c.tsCtx = true; c.tskip = true; c.tqBypass = true }},
		{"implrdpcm", func(c *gcfg) { c.implRdpcm = true; c.tskip = true; c.tqBypass = true }},
		{"explrdpcm", func(c *gcfg) { c.explRdpcm = true; c.tskip = true; c.tqBypass = true }},
		{"intrasmoothoff", func(c *gcfg) { c.intraSmoothOff = true }},
		{"highprec", func(c *gcfg) { c.highPrec = true; c.bd = 10 }}, // ffmpeg ignores high_precision_offsets, so no weighted prediction here
		{"persrice", func(c *gcfg) { c.persRice = true }},
		{"cross", func(c *gcfg) { c.chroma = 3; c.cross = true }},
		{"cqplist", func(c *gcfg) {
			c.cqpList = [][2]int{{2, -3}, {-4, 5}, {1, 1}, {0, 2}, {-1, 3}, {6, -6}}
			c.cuQpDelta = true
		}}, // ffmpeg reads cu_chroma_qp_offset_idx with cMax 5, hence six entries
		{"saoscale", func(c *gcfg) { c.bd = 12; c.sao = true; c.saoScale = [2]int{1, 2} }},
	}
	only := os.Getenv("HEVC_FEATURE")
	for _, f := range feats {
		if only != "" && only != f.name {
			continue
		}
		f := f
		t.Run(f.name, func(t *testing.T) {
			for seed := int64(1); seed <= 4; seed++ {
				c := baseCfg()
				f.f(&c)
				checkGenerated(t, c, seed)
			}
		})
	}
}

// genFails reports whether the stream of cfg differs between ffmpeg and the decoder.
func genFails(t *testing.T, c gcfg, seed int64) (fail bool) {
	defer func() {
		if recover() != nil {
			fail = true
		}
	}()
	data, _ := genStream(t, c, seed)
	path := writeAnnexB(t, "gen.hevc", data)
	ref, _ := ffmpegRaw1(t, path, c.pixFmt())
	frames, _ := decodeAnnexB(t, data)
	var got []byte
	for _, f := range frames {
		got = append(got, frameBytes(f)...)
	}
	return !bytes.Equal(ref, got)
}

// TestGenMin shrinks the configuration of HEVC_SEED to the features that matter.
func TestGenMin(t *testing.T) {
	v := os.Getenv("HEVC_MIN")
	if v == "" {
		t.Skip()
	}
	var seed int64
	fmt.Sscan(v, &seed)
	c := randomCfg(rand.New(rand.NewSource(seed)))
	b := baseCfg()
	muts := []func(c *gcfg){
		func(c *gcfg) { c.chroma, c.bd = 1, 8 },
		func(c *gcfg) { c.bd = 8 },
		func(c *gcfg) { c.chroma = 1 },
		func(c *gcfg) { c.amp, c.sao = false, false },
		func(c *gcfg) { c.sao = false },
		func(c *gcfg) { c.pcm, c.pPCM = false, 0 },
		func(c *gcfg) { c.scaling = 0 },
		func(c *gcfg) { c.temporalMvp = false },
		func(c *gcfg) { c.ltPresent = false },
		func(c *gcfg) { c.tsRot, c.tsCtx, c.implRdpcm, c.explRdpcm = false, false, false, false },
		func(c *gcfg) { c.intraSmoothOff, c.highPrec, c.persRice = false, false, false },
		func(c *gcfg) { c.tilesC, c.tilesR = 1, 1 },
		func(c *gcfg) { c.wpp = false },
		func(c *gcfg) { c.depSlices, c.sliceMode = false, 0 },
		func(c *gcfg) { c.sliceMode = 0 },
		func(c *gcfg) { c.signHide = false },
		func(c *gcfg) { c.cabacInitPresent = false },
		func(c *gcfg) { c.constrained = false },
		func(c *gcfg) { c.tskip, c.log2MaxTs = false, 2 },
		func(c *gcfg) { c.log2MaxTs = 2 },
		func(c *gcfg) { c.cuQpDelta = false },
		func(c *gcfg) { c.cbOff, c.crOff, c.sliceChromaOff = 0, 0, false },
		func(c *gcfg) { c.wp, c.wbi = false, false },
		func(c *gcfg) { c.tqBypass = false },
		func(c *gcfg) { c.loopAcrossSlices = false },
		func(c *gcfg) { c.dbControl, c.dbOverride, c.dbDis = false, false, false },
		func(c *gcfg) { c.listsMod = false },
		func(c *gcfg) { c.parMrg = 2 },
		func(c *gcfg) { c.cross = false },
		func(c *gcfg) { c.cqpList = nil },
		func(c *gcfg) { c.saoScale = [2]int{} },
		func(c *gcfg) { c.outputFlag = false },
		func(c *gcfg) { c.extraBits, c.sliceHdrExt = 0, false },
		func(c *gcfg) { c.frames = 1 },
		func(c *gcfg) { c.frames = 2 },
		func(c *gcfg) { c.strong = false },
		func(c *gcfg) { c.loopAcrossTiles = true },
		func(c *gcfg) { c.initQp = 30 },
		func(c *gcfg) { c.initQp = 40 },
		func(c *gcfg) { c.strong = true },
		func(c *gcfg) { c.depthInter, c.depthIntra = 1, 1 },
		func(c *gcfg) { c.depthInter = 1 },
		func(c *gcfg) { c.w, c.h = 64, 64 },
		func(c *gcfg) { c.w, c.h = 128, 96 },
		func(c *gcfg) { c.log2MinTb = 2 },
		func(c *gcfg) { c.log2MaxTb, c.log2MinTb = min(c.log2MaxTb, 4), 2 },
	}
	_ = b
	if !genFails(t, c, seed) {
		t.Fatal("seed does not fail")
	}
	for round := 0; round < 2; round++ {
		for _, m := range muts {
			c2 := c
			m(&c2)
			if c2.pcm == false {
				c2.pPCM = 0
			}
			if c2.bd < 11 {
				c2.saoScale = [2]int{}
			}
			if c2.chroma == 0 {
				c2.cqpList = nil
				c2.cross = false
			}
			if c2.chroma != 3 {
				c2.cross = false
			}
			if c2.pcm && (c2.pcmBd[0] > c2.bd || c2.pcmBd[1] > c2.bd) {
				continue
			}
			if genFails(t, c2, seed) {
				c = c2
			}
		}
	}
	bv, cv := reflect.ValueOf(b), reflect.ValueOf(c)
	var diff []string
	for i := 0; i < bv.NumField(); i++ {
		if fmt.Sprint(bv.Field(i)) != fmt.Sprint(cv.Field(i)) {
			diff = append(diff, fmt.Sprintf("%s=%v", bv.Type().Field(i).Name, cv.Field(i)))
		}
	}
	t.Logf("minimal cfg differs from base in: %v", diff)
}

func TestGenOne(t *testing.T) {
	v := os.Getenv("HEVC_ONE")
	if v == "" {
		t.Skip()
	}
	c := baseCfg()
	fmt.Sscanf(v, "%d,%d,%d,%d,%d", &c.log2Ctb, &c.log2MinCb, &c.log2MinTb, &c.log2MaxTb, &c.frames)
	c.depthInter, c.depthIntra = 1, 1
	c.constrained = os.Getenv("HEVC_CIP") != ""
	c.temporalMvp = os.Getenv("HEVC_NOTMVP") == ""
	if q := os.Getenv("HEVC_QP"); q != "" {
		fmt.Sscan(q, &c.initQp)
	}
	for s := int64(1); s <= 6; s++ {
		checkGenerated(t, c, s)
	}
}
