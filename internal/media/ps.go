package media

import (
	"errors"
	"math"
	"sync"
)

// Parametric stereo (ISO/IEC 14496-3 annex 8): bitstream parsing, hybrid
// analysis / synthesis filterbank (10/20 and 34 band modes), decorrelation,
// IID/ICC/IPD/OPD stereo processing. A mono SBR channel (QMF domain, 38 slots
// with the 6 slot look ahead of the hybrid filter) becomes a stereo pair.

const (
	psMaxEnv   = 5
	psMaxPar   = 34
	psSlots    = 32
	psMaxDelay = 14
	psAPLinks  = 3
	psAPDelay  = 5
)

var (
	psNrBands     = [2]int{71, 91}
	psNrAllpass   = [2]int{30, 50}
	psShortDelay  = [2]int{42, 62}
	psNrPar       = [2]int{20, 34}
	psDecayCutoff = [2]int{10, 32}
	psNumEnvTab   = [2][4]int{{0, 1, 2, 4}, {1, 2, 3, 4}}
	psNrIIDICC    = [6]int{10, 20, 34, 10, 20, 34}
	psNrIPDOPD    = [6]int{5, 11, 17, 5, 11, 17}
	psHuffOffset  = [10]int{30, 30, 14, 14, 7, 7, 0, 0, 0, 0}
)

// Huffman book indices (order of psHuff*).
const (
	psHIIDDf1 = iota
	psHIIDDt1
	psHIIDDf0
	psHIIDDt0
	psHICCDf
	psHICCDt
	psHIPDDf
	psHIPDDt
	psHOPDDf
	psHOPDDt
)

var (
	psTabOnce  sync.Once
	psF20_0_8  [][8][2]float64
	psF34_0_12 [][8][2]float64
	psF34_1_8  [][8][2]float64
	psF34_2_4  [][8][2]float64
	psQFract   [2][50][3][2]float64
	psPhiFract [2][50][2]float64
	psPdRe     [512]float64
	psPdIm     [512]float64
)

func psMakeFilters(proto *[7]float32, bands int) [][8][2]float64 {
	f := make([][8][2]float64, bands)
	for q := 0; q < bands; q++ {
		for n := 0; n < 7; n++ {
			theta := 2 * math.Pi * (float64(q) + 0.5) * float64(n-6) / float64(bands)
			f[q][n][0] = float64(proto[n]) * math.Cos(theta)
			f[q][n][1] = float64(proto[n]) * -math.Sin(theta)
		}
	}
	return f
}

func psInitTables() {
	psTabOnce.Do(func() {
		sbrInitHuff()
		psF20_0_8 = psMakeFilters(&psG0Q8, 8)
		psF34_0_12 = psMakeFilters(&psG0Q12, 12)
		psF34_1_8 = psMakeFilters(&psG1Q8, 8)
		psF34_2_4 = psMakeFilters(&psG2Q4, 4)
		links := [3]float64{float64(float32(0.43)), float64(float32(0.75)), float64(float32(0.347))}
		gain := float64(float32(0.39))
		for k := 0; k < 30; k++ {
			var fc float64
			if k < len(psFCenter20) {
				fc = float64(psFCenter20[k]) * 0.125
			} else {
				fc = float64(k) - 6.5
			}
			for m := 0; m < 3; m++ {
				th := -math.Pi * links[m] * fc
				psQFract[0][k][m] = [2]float64{math.Cos(th), math.Sin(th)}
			}
			th := -math.Pi * gain * fc
			psPhiFract[0][k] = [2]float64{math.Cos(th), math.Sin(th)}
		}
		for k := 0; k < 50; k++ {
			var fc float64
			if k < len(psFCenter34) {
				fc = float64(psFCenter34[k]) / 24.0
			} else {
				fc = float64(k) - 26.5
			}
			for m := 0; m < 3; m++ {
				th := -math.Pi * links[m] * fc
				psQFract[1][k][m] = [2]float64{math.Cos(th), math.Sin(th)}
			}
			th := -math.Pi * gain * fc
			psPhiFract[1][k] = [2]float64{math.Cos(th), math.Sin(th)}
		}
		for p0 := 0; p0 < 8; p0++ {
			for p1 := 0; p1 < 8; p1++ {
				for p2 := 0; p2 < 8; p2++ {
					re := 0.25*float64(psIpdopdCos[p0]) + 0.5*float64(psIpdopdCos[p1]) + float64(psIpdopdCos[p2])
					im := 0.25*float64(psIpdopdSin[p0]) + 0.5*float64(psIpdopdSin[p1]) + float64(psIpdopdSin[p2])
					mag := 1 / math.Hypot(im, re)
					psPdRe[p0*64+p1*8+p2] = re * mag
					psPdIm[p0*64+p1*8+p2] = im * mag
				}
			}
		}
	})
}

// psState is the parametric stereo decoder of one SCE.
type psState struct {
	start                                      bool
	enableIID, enableICC, enableExt, enableIPD bool
	iidQuant                                   int
	nrIID, nrICC, nrIPD                        int
	iccMode                                    int
	frameClass                                 int
	numEnv, numEnvOld                          int
	border                                     [psMaxEnv + 1]int
	iidPar, iccPar, ipdPar, opdPar             [psMaxEnv][psMaxPar]int8
	is34, is34Old                              bool

	inBuf               [5][44][2]float64
	delay               [91][psMaxDelay + psSlots][2]float64
	apDelay             [50][psAPLinks][psSlots + psAPDelay][2]float64
	peakDecayNrg        [34]float64
	powerSmooth         [34]float64
	peakDecayDiffSmooth [34]float64
	h                   [4][2][psMaxEnv + 1][psMaxPar]float64 // H11,H12,H21,H22 x (re,im)
	opdHist, ipdHist    [34]int
}

func newPS() *psState {
	psInitTables()
	return &psState{nrIID: 0, nrICC: 0, nrIPD: 0}
}

func (p *psState) zeroPar() {
	p.iidPar = [psMaxEnv][psMaxPar]int8{}
	p.iccPar = [psMaxEnv][psMaxPar]int8{}
	p.ipdPar = [psMaxEnv][psMaxPar]int8{}
	p.opdPar = [psMaxEnv][psMaxPar]int8{}
}

var errPS = errors.New("media: bad ps data")

func (p *psState) readPar(r *aacBits, par *[psMaxEnv][psMaxPar]int8, book, num, e, dt int, mask int, kind int) error {
	vlc := psVLCs[book]
	off := psHuffOffset[book]
	check := func(v int) bool {
		switch kind {
		case 0:
			lim := 7 + 8*p.iidQuant
			return v > lim || v < -lim
		case 1:
			return v < 0 || v > 7
		}
		return false
	}
	if dt != 0 {
		ePrev := e - 1
		if e == 0 {
			ePrev = p.numEnvOld - 1
		}
		if ePrev < 0 {
			ePrev = 0
		}
		for b := 0; b < num; b++ {
			v := int(par[ePrev][b]) + r.vlc(vlc) - off
			if mask != 0 {
				v &= mask
			}
			par[e][b] = int8(v)
			if check(v) || r.err {
				return errPS
			}
		}
	} else {
		v := 0
		for b := 0; b < num; b++ {
			v += r.vlc(vlc) - off
			if mask != 0 {
				v &= mask
			}
			par[e][b] = int8(v)
			if check(v) || r.err {
				return errPS
			}
		}
	}
	return nil
}

func (p *psState) readExtension(r *aacBits, id int) int {
	start := r.pos
	if id != 0 {
		return 0
	}
	p.enableIPD = r.bit()
	if p.enableIPD {
		for e := 0; e < p.numEnv; e++ {
			dt := r.bits(1)
			bk := psHIPDDf
			if dt != 0 {
				bk = psHIPDDt
			}
			p.readPar(r, &p.ipdPar, bk, p.nrIPD, e, dt, 7, 2)
			dt = r.bits(1)
			bk = psHOPDDf
			if dt != 0 {
				bk = psHOPDDt
			}
			p.readPar(r, &p.opdPar, bk, p.nrIPD, e, dt, 7, 2)
		}
	}
	r.bits(1) // reserved_ps
	return r.pos - start
}

// parse reads ps_data() of at most left bits.
func (p *psState) parse(r *aacBits, left int) error {
	start := r.pos
	fail := func() error {
		p.start = false
		p.zeroPar()
		return errPS
	}
	header := r.bit()
	if header {
		p.enableIID = r.bit()
		if p.enableIID {
			m := r.bits(3)
			if m > 5 {
				return fail()
			}
			p.nrIID = psNrIIDICC[m]
			p.iidQuant = 0
			if m > 2 {
				p.iidQuant = 1
			}
			p.nrIPD = psNrIPDOPD[m]
		}
		p.enableICC = r.bit()
		if p.enableICC {
			p.iccMode = r.bits(3)
			if p.iccMode > 5 {
				return fail()
			}
			p.nrICC = psNrIIDICC[p.iccMode]
		}
		p.enableExt = r.bit()
	}
	p.frameClass = r.bits(1)
	p.numEnvOld = p.numEnv
	p.numEnv = psNumEnvTab[p.frameClass][r.bits(2)]
	p.border[0] = -1
	if p.frameClass != 0 {
		for e := 1; e <= p.numEnv; e++ {
			p.border[e] = r.bits(5)
		}
	} else {
		l2 := [5]int{0, 0, 1, 1, 2}[p.numEnv]
		for e := 1; e <= p.numEnv; e++ {
			p.border[e] = (e * psSlots >> uint(l2)) - 1
		}
	}
	if p.enableIID {
		for e := 0; e < p.numEnv; e++ {
			dt := r.bits(1)
			bk := [4]int{psHIIDDf0, psHIIDDf1, psHIIDDt0, psHIIDDt1}[2*dt+p.iidQuant]
			if p.readPar(r, &p.iidPar, bk, p.nrIID, e, dt, 0, 0) != nil {
				return fail()
			}
		}
	} else {
		p.iidPar = [psMaxEnv][psMaxPar]int8{}
	}
	if p.enableICC {
		for e := 0; e < p.numEnv; e++ {
			dt := r.bits(1)
			bk := psHICCDf
			if dt != 0 {
				bk = psHICCDt
			}
			if p.readPar(r, &p.iccPar, bk, p.nrICC, e, dt, 0, 1) != nil {
				return fail()
			}
		}
	} else {
		p.iccPar = [psMaxEnv][psMaxPar]int8{}
	}
	if p.enableExt {
		cnt := r.bits(4)
		if cnt == 15 {
			cnt += r.bits(8)
		}
		cnt *= 8
		for cnt > 7 {
			id := r.bits(2)
			cnt -= 2 + p.readExtension(r, id)
		}
		if cnt < 0 {
			return fail()
		}
		r.skip(cnt)
	}
	// fix up the envelopes
	if p.numEnv == 0 || p.border[p.numEnv] < psSlots-1 {
		src := p.numEnvOld - 1
		if p.numEnv != 0 {
			src = p.numEnv - 1
		}
		if src >= 0 && src != p.numEnv {
			if p.enableIID {
				p.iidPar[p.numEnv] = p.iidPar[src]
			}
			if p.enableICC {
				p.iccPar[p.numEnv] = p.iccPar[src]
			}
			if p.enableIPD {
				p.ipdPar[p.numEnv] = p.ipdPar[src]
				p.opdPar[p.numEnv] = p.opdPar[src]
			}
		}
		if p.enableIID {
			for b := 0; b < p.nrIID; b++ {
				v := int(p.iidPar[p.numEnv][b])
				if v > 7+8*p.iidQuant || v < -(7+8*p.iidQuant) {
					return fail()
				}
			}
		}
		if p.enableICC {
			for b := 0; b < p.nrIID; b++ {
				if p.iccPar[p.numEnv][b] < 0 || p.iccPar[p.numEnv][b] > 7 {
					return fail()
				}
			}
		}
		p.numEnv++
		if p.numEnv > psMaxEnv {
			return fail()
		}
		p.border[p.numEnv] = psSlots - 1
	}
	p.is34Old = p.is34
	if p.enableIID || p.enableICC {
		p.is34 = (p.enableIID && p.nrIID == 34) || (p.enableICC && p.nrICC == 34)
	}
	if !p.enableIPD {
		p.ipdPar = [psMaxEnv][psMaxPar]int8{}
		p.opdPar = [psMaxEnv][psMaxPar]int8{}
	}
	if header {
		p.start = true
	}
	if r.pos-start > left || r.err {
		return fail()
	}
	return nil
}

// ---- parameter mapping ------------------------------------------------------

func mapIdx10_20(out, par *[psMaxPar]int8, full bool) {
	b := 9
	if !full {
		b = 4
		out[10] = 0
	}
	for ; b >= 0; b-- {
		out[2*b+1] = par[b]
		out[2*b] = par[b]
	}
}

func mapIdx34_20(out, par *[psMaxPar]int8, full bool) {
	out[0] = int8((2*int(par[0]) + int(par[1])) / 3)
	out[1] = int8((int(par[1]) + 2*int(par[2])) / 3)
	out[2] = int8((2*int(par[3]) + int(par[4])) / 3)
	out[3] = int8((int(par[4]) + 2*int(par[5])) / 3)
	out[4] = int8((int(par[6]) + int(par[7])) / 2)
	out[5] = int8((int(par[8]) + int(par[9])) / 2)
	out[6] = par[10]
	out[7] = par[11]
	out[8] = int8((int(par[12]) + int(par[13])) / 2)
	out[9] = int8((int(par[14]) + int(par[15])) / 2)
	out[10] = par[16]
	if full {
		out[11] = par[17]
		out[12] = par[18]
		out[13] = par[19]
		out[14] = int8((int(par[20]) + int(par[21])) / 2)
		out[15] = int8((int(par[22]) + int(par[23])) / 2)
		out[16] = int8((int(par[24]) + int(par[25])) / 2)
		out[17] = int8((int(par[26]) + int(par[27])) / 2)
		out[18] = int8((int(par[28]) + int(par[29]) + int(par[30]) + int(par[31])) / 4)
		out[19] = int8((int(par[32]) + int(par[33])) / 2)
	}
}

func mapIdx20_34(out, par *[psMaxPar]int8, full bool) {
	if full {
		out[33] = par[19]
	}
	out[32] = par[19]
	out[31] = par[18]
	out[30] = par[18]
	out[29] = par[18]
	out[28] = par[18]
	out[27] = par[17]
	out[26] = par[17]
	out[25] = par[16]
	out[24] = par[16]
	out[23] = par[15]
	out[22] = par[15]
	out[21] = par[14]
	out[20] = par[14]
	out[19] = par[13]
	out[18] = par[12]
	out[17] = par[11]
	out[16] = par[10]
	out[15] = par[9]
	out[14] = par[9]
	out[13] = par[8]
	out[12] = par[8]
	out[11] = par[7]
	out[10] = par[6]
	out[9] = par[5]
	out[8] = par[5]
	out[7] = par[4]
	out[6] = par[4]
	out[5] = int8((int(par[2]) + int(par[3])) / 2)
	out[4] = int8((int(par[1]) + int(par[2])) / 2)
	out[3] = par[2]
	out[2] = par[1]
	out[1] = int8((int(par[0]) + int(par[1])) / 2)
	out[0] = par[0]
}

func mapIdx10_34(out, par *[psMaxPar]int8, full bool) {
	if full {
		for i := 28; i <= 33; i++ {
			out[i] = par[9]
		}
		for i := 24; i <= 27; i++ {
			out[i] = par[8]
		}
		for i := 20; i <= 23; i++ {
			out[i] = par[7]
		}
		out[19] = par[6]
		out[18] = par[6]
		out[17] = par[5]
		out[16] = par[5]
	} else {
		out[16] = 0
	}
	for i := 12; i <= 15; i++ {
		out[i] = par[4]
	}
	out[11] = par[3]
	out[10] = par[3]
	for i := 6; i <= 9; i++ {
		out[i] = par[2]
	}
	out[5] = par[1]
	out[4] = par[1]
	out[3] = par[1]
	out[2] = par[0]
	out[1] = par[0]
	out[0] = par[0]
}

func mapVal34to20(par *[psMaxPar]float64) {
	par[0] = (2*par[0] + par[1]) * (1.0 / 3)
	par[1] = (par[1] + 2*par[2]) * (1.0 / 3)
	par[2] = (2*par[3] + par[4]) * (1.0 / 3)
	par[3] = (par[4] + 2*par[5]) * (1.0 / 3)
	par[4] = (par[6] + par[7]) * 0.5
	par[5] = (par[8] + par[9]) * 0.5
	par[6] = par[10]
	par[7] = par[11]
	par[8] = (par[12] + par[13]) * 0.5
	par[9] = (par[14] + par[15]) * 0.5
	par[10] = par[16]
	par[11] = par[17]
	par[12] = par[18]
	par[13] = par[19]
	par[14] = (par[20] + par[21]) * 0.5
	par[15] = (par[22] + par[23]) * 0.5
	par[16] = (par[24] + par[25]) * 0.5
	par[17] = (par[26] + par[27]) * 0.5
	par[18] = (par[28] + par[29] + par[30] + par[31]) * 0.25
	par[19] = (par[32] + par[33]) * 0.5
}

func mapVal20to34(par *[psMaxPar]float64) {
	par[33] = par[19]
	par[32] = par[19]
	par[31] = par[18]
	par[30] = par[18]
	par[29] = par[18]
	par[28] = par[18]
	par[27] = par[17]
	par[26] = par[17]
	par[25] = par[16]
	par[24] = par[16]
	par[23] = par[15]
	par[22] = par[15]
	par[21] = par[14]
	par[20] = par[14]
	par[19] = par[13]
	par[18] = par[12]
	par[17] = par[11]
	par[16] = par[10]
	par[15] = par[9]
	par[14] = par[9]
	par[13] = par[8]
	par[12] = par[8]
	par[11] = par[7]
	par[10] = par[6]
	par[9] = par[5]
	par[8] = par[5]
	par[7] = par[4]
	par[6] = par[4]
	par[5] = (par[3] + par[2]) * 0.5
	par[4] = (par[2] + par[1]) * 0.5
	par[3] = par[2]
	par[2] = par[1]
	par[1] = (par[1] + par[0]) * 0.5
}

// remap brings the per envelope parameters of numPar bands to the 20 or 34
// band resolution of the stereo processing.
func psRemap(dst *[psMaxEnv][psMaxPar]int8, src *[psMaxEnv][psMaxPar]int8, numPar, numEnv int, is34, full bool) *[psMaxEnv][psMaxPar]int8 {
	if is34 {
		switch numPar {
		case 20, 11:
			for e := 0; e < numEnv; e++ {
				mapIdx20_34(&dst[e], &src[e], full)
			}
		case 10, 5:
			for e := 0; e < numEnv; e++ {
				mapIdx10_34(&dst[e], &src[e], full)
			}
		default:
			return src
		}
		return dst
	}
	switch numPar {
	case 34, 17:
		for e := 0; e < numEnv; e++ {
			mapIdx34_20(&dst[e], &src[e], full)
		}
	case 10, 5:
		for e := 0; e < numEnv; e++ {
			mapIdx10_20(&dst[e], &src[e], full)
		}
	default:
		return src
	}
	return dst
}

// ---- hybrid filterbank ---------------------------------------------------------

// psHybridAnalysisN: out[i][n] = sum_j filter[i][j] * in[n+j] with the
// symmetric 13 tap structure; in has 44 samples, n < 32.
func psHybridN(in *[44][2]float64, out [][32][2]float64, filter [][8][2]float64) {
	for n := 0; n < 32; n++ {
		x := in[n : n+13]
		for i := range filter {
			f := &filter[i]
			sr := f[6][0] * x[6][0]
			si := f[6][0] * x[6][1]
			for j := 0; j < 6; j++ {
				a0r, a0i := x[j][0], x[j][1]
				a1r, a1i := x[12-j][0], x[12-j][1]
				sr += f[j][0]*(a0r+a1r) - f[j][1]*(a0i-a1i)
				si += f[j][0]*(a0i+a1i) + f[j][1]*(a0r-a1r)
			}
			out[i][n] = [2]float64{sr, si}
		}
	}
}

func psHybrid2(in *[44][2]float64, out [][32][2]float64, reverse int) {
	// g1_Q2: only the odd taps and the centre tap are non zero
	filt := [7]float64{0, float64(psG1Q2[1]), 0, float64(psG1Q2[3]), 0, float64(psG1Q2[5]), float64(psG1Q2[6])}
	for n := 0; n < 32; n++ {
		x := in[n : n+13]
		reIn := filt[6] * x[6][0]
		imIn := filt[6] * x[6][1]
		var reOp, imOp float64
		for j := 0; j < 6; j += 2 {
			reIn += filt[j] * (x[j][0] + x[12-j][0])
			reOp += filt[j+1] * (x[j+1][0] + x[11-j][0])
			imIn += filt[j] * (x[j][1] + x[12-j][1])
			imOp += filt[j+1] * (x[j+1][1] + x[11-j][1])
		}
		out[reverse][n] = [2]float64{reIn + reOp, imIn + imOp}
		out[1-reverse][n] = [2]float64{reIn - reOp, imIn - imOp}
	}
}

var psG1Q2 = [7]float32{0, 0.01899487526049, 0, -0.07293139167538, 0, 0.30596630545168, 0.5}

func (p *psState) hybridAnalysis(x *[38][64][2]float64, out *[91][32][2]float64, is34 bool) {
	for i := 0; i < 5; i++ {
		for j := 0; j < 38; j++ {
			p.inBuf[i][j+6] = x[j][i]
		}
	}
	if is34 {
		psHybridN(&p.inBuf[0], out[0:12], psF34_0_12)
		psHybridN(&p.inBuf[1], out[12:20], psF34_1_8)
		psHybridN(&p.inBuf[2], out[20:24], psF34_2_4)
		psHybridN(&p.inBuf[3], out[24:28], psF34_2_4)
		psHybridN(&p.inBuf[4], out[28:32], psF34_2_4)
		for i := 5; i < 64; i++ {
			for j := 0; j < 32; j++ {
				out[27+i][j] = x[j][i]
			}
		}
	} else {
		var t [8][32][2]float64
		psHybridN(&p.inBuf[0], t[:], psF20_0_8)
		for n := 0; n < 32; n++ {
			out[0][n] = t[6][n]
			out[1][n] = t[7][n]
			out[2][n] = t[0][n]
			out[3][n] = t[1][n]
			out[4][n] = [2]float64{t[2][n][0] + t[5][n][0], t[2][n][1] + t[5][n][1]}
			out[5][n] = [2]float64{t[3][n][0] + t[4][n][0], t[3][n][1] + t[4][n][1]}
		}
		psHybrid2(&p.inBuf[1], out[6:8], 1)
		psHybrid2(&p.inBuf[2], out[8:10], 0)
		for i := 3; i < 64; i++ {
			for j := 0; j < 32; j++ {
				out[7+i][j] = x[j][i]
			}
		}
	}
	for i := 0; i < 5; i++ {
		copy(p.inBuf[i][:6], p.inBuf[i][32:38])
	}
}

func psHybridSynthesis(out *[38][64][2]float64, in *[91][32][2]float64, is34 bool) {
	for n := 0; n < 32; n++ {
		for k := 0; k < 5; k++ {
			out[n][k] = [2]float64{}
		}
	}
	if is34 {
		for n := 0; n < 32; n++ {
			for i := 0; i < 12; i++ {
				out[n][0][0] += in[i][n][0]
				out[n][0][1] += in[i][n][1]
			}
			for i := 0; i < 8; i++ {
				out[n][1][0] += in[12+i][n][0]
				out[n][1][1] += in[12+i][n][1]
			}
			for i := 0; i < 4; i++ {
				out[n][2][0] += in[20+i][n][0]
				out[n][2][1] += in[20+i][n][1]
				out[n][3][0] += in[24+i][n][0]
				out[n][3][1] += in[24+i][n][1]
				out[n][4][0] += in[28+i][n][0]
				out[n][4][1] += in[28+i][n][1]
			}
			for i := 5; i < 64; i++ {
				out[n][i] = in[27+i][n]
			}
		}
		return
	}
	for n := 0; n < 32; n++ {
		for c := 0; c < 2; c++ {
			out[n][0][c] = in[0][n][c] + in[1][n][c] + in[2][n][c] + in[3][n][c] + in[4][n][c] + in[5][n][c]
			out[n][1][c] = in[6][n][c] + in[7][n][c]
			out[n][2][c] = in[8][n][c] + in[9][n][c]
		}
		for i := 3; i < 64; i++ {
			out[n][i] = in[7+i][n]
		}
	}
}

// ---- decorrelation and stereo processing ---------------------------------------

func (p *psState) decorrelation(out, s *[91][32][2]float64, is34 bool) {
	idx := 0
	if is34 {
		idx = 1
	}
	kToI := psKToI20[:]
	if is34 {
		kToI = psKToI34[:]
	}
	if is34 != p.is34Old {
		p.peakDecayNrg = [34]float64{}
		p.powerSmooth = [34]float64{}
		p.peakDecayDiffSmooth = [34]float64{}
		p.delay = [91][psMaxDelay + psSlots][2]float64{}
		p.apDelay = [50][psAPLinks][psSlots + psAPDelay][2]float64{}
	}
	var power [34][psSlots]float64
	var tgain [34][psSlots]float64
	for k := 0; k < psNrBands[idx]; k++ {
		i := kToI[k]
		for n := 0; n < psSlots; n++ {
			power[i][n] += s[k][n][0]*s[k][n][0] + s[k][n][1]*s[k][n][1]
		}
	}
	const (
		peakDecay       = float64(float32(0.76592833836465))
		transientImpact = 1.5
		aSmooth         = 0.25
	)
	for i := 0; i < psNrPar[idx]; i++ {
		for n := 0; n < psSlots; n++ {
			decayed := peakDecay * p.peakDecayNrg[i]
			if power[i][n] > decayed {
				p.peakDecayNrg[i] = power[i][n]
			} else {
				p.peakDecayNrg[i] = decayed
			}
			p.powerSmooth[i] += aSmooth * (power[i][n] - p.powerSmooth[i])
			p.peakDecayDiffSmooth[i] += aSmooth * (p.peakDecayNrg[i] - power[i][n] - p.peakDecayDiffSmooth[i])
			denom := transientImpact * p.peakDecayDiffSmooth[i]
			if denom > p.powerSmooth[i] {
				tgain[i][n] = p.powerSmooth[i] / denom
			} else {
				tgain[i][n] = 1
			}
		}
	}
	a := [3]float64{float64(float32(0.65143905753106)), float64(float32(0.56471812200776)), float64(float32(0.48954165955695))}
	k := 0
	for ; k < psNrAllpass[idx]; k++ {
		b := kToI[k]
		gDecay := 1 - 0.05*float64(k-psDecayCutoff[idx])
		if gDecay < 0 {
			gDecay = 0
		}
		if gDecay > 1 {
			gDecay = 1
		}
		dl := &p.delay[k]
		copy(dl[:psMaxDelay], dl[psSlots:psSlots+psMaxDelay])
		copy(dl[psMaxDelay:], s[k][:])
		for m := 0; m < psAPLinks; m++ {
			copy(p.apDelay[k][m][:psAPDelay], p.apDelay[k][m][psSlots:psSlots+psAPDelay])
		}
		var ag [3]float64
		for m := 0; m < 3; m++ {
			ag[m] = a[m] * gDecay
		}
		phi := psPhiFract[idx][k]
		for n := 0; n < psSlots; n++ {
			d := dl[psMaxDelay-2+n]
			inRe := d[0]*phi[0] - d[1]*phi[1]
			inIm := d[0]*phi[1] + d[1]*phi[0]
			for m := 0; m < psAPLinks; m++ {
				aRe := ag[m] * inRe
				aIm := ag[m] * inIm
				ld := p.apDelay[k][m][n+2-m]
				q := psQFract[idx][k][m]
				apdRe, apdIm := inRe, inIm
				inRe = ld[0]*q[0] - ld[1]*q[1] - aRe
				inIm = ld[0]*q[1] + ld[1]*q[0] - aIm
				p.apDelay[k][m][n+5] = [2]float64{apdRe + ag[m]*inRe, apdIm + ag[m]*inIm}
			}
			out[k][n] = [2]float64{tgain[b][n] * inRe, tgain[b][n] * inIm}
		}
	}
	for ; k < psShortDelay[idx]; k++ {
		i := kToI[k]
		dl := &p.delay[k]
		copy(dl[:psMaxDelay], dl[psSlots:psSlots+psMaxDelay])
		copy(dl[psMaxDelay:], s[k][:])
		for n := 0; n < psSlots; n++ {
			d := dl[psMaxDelay-14+n]
			out[k][n] = [2]float64{d[0] * tgain[i][n], d[1] * tgain[i][n]}
		}
	}
	for ; k < psNrBands[idx]; k++ {
		i := kToI[k]
		dl := &p.delay[k]
		copy(dl[:psMaxDelay], dl[psSlots:psSlots+psMaxDelay])
		copy(dl[psMaxDelay:], s[k][:])
		for n := 0; n < psSlots; n++ {
			d := dl[psMaxDelay-1+n]
			out[k][n] = [2]float64{d[0] * tgain[i][n], d[1] * tgain[i][n]}
		}
	}
}

func (p *psState) stereoProcessing(l, r *[91][32][2]float64, is34 bool) {
	idx := 0
	if is34 {
		idx = 1
	}
	kToI := psKToI20[:]
	if is34 {
		kToI = psKToI34[:]
	}
	var iidBuf, iccBuf, ipdBuf, opdBuf [psMaxEnv][psMaxPar]int8
	iid := psRemap(&iidBuf, &p.iidPar, p.nrIID, p.numEnv, is34, true)
	icc := psRemap(&iccBuf, &p.iccPar, p.nrICC, p.numEnv, is34, true)
	ipd, opd := &p.ipdPar, &p.opdPar
	if p.enableIPD {
		ipd = psRemap(&ipdBuf, &p.ipdPar, p.nrIPD, p.numEnv, is34, false)
		opd = psRemap(&opdBuf, &p.opdPar, p.nrIPD, p.numEnv, is34, false)
	}
	if is34 && !p.is34Old {
		for m := 0; m < 4; m++ {
			for c := 0; c < 2; c++ {
				mapVal20to34(&p.h[m][c][0])
			}
		}
	} else if !is34 && p.is34Old {
		for m := 0; m < 4; m++ {
			for c := 0; c < 2; c++ {
				mapVal34to20(&p.h[m][c][0])
			}
		}
	}
	for e := 0; e < p.numEnv; e++ {
		for b := 0; b < psNrPar[idx]; b++ {
			iidv := int(iid[e][b])
			c := float64(psIIDParDequant[iidv+7+23*p.iidQuant])
			c1 := math.Sqrt2 / math.Sqrt(1+c*c)
			c2 := c1 * c
			alpha := 0.5 * float64(psACosICCInvq[icc[e][b]])
			beta := alpha * (c1 - c2) * math.Sqrt2 / 2
			h11 := c2 * math.Cos(beta+alpha)
			h12 := c1 * math.Cos(beta-alpha)
			h21 := c2 * math.Sin(beta+alpha)
			h22 := c1 * math.Sin(beta-alpha)
			if b < p.nrIPD {
				opdIdx := p.opdHist[b]*8 + int(opd[e][b])
				ipdIdx := p.ipdHist[b]*8 + int(ipd[e][b])
				opdRe, opdIm := psPdRe[opdIdx&511], psPdIm[opdIdx&511]
				ipdRe, ipdIm := psPdRe[ipdIdx&511], psPdIm[ipdIdx&511]
				p.opdHist[b] = (p.opdHist[b]*8 + int(opd[e][b])) & 0x3f
				p.ipdHist[b] = (p.ipdHist[b]*8 + int(ipd[e][b])) & 0x3f
				adjRe := opdRe*ipdRe + opdIm*ipdIm
				adjIm := opdIm*ipdRe - opdRe*ipdIm
				p.h[0][1][e+1][b] = h11 * opdIm
				p.h[1][1][e+1][b] = h12 * adjIm
				p.h[2][1][e+1][b] = h21 * opdIm
				p.h[3][1][e+1][b] = h22 * adjIm
				h11 *= opdRe
				h12 *= adjRe
				h21 *= opdRe
				h22 *= adjRe
			} else {
				p.h[0][1][e+1][b], p.h[1][1][e+1][b], p.h[2][1][e+1][b], p.h[3][1][e+1][b] = 0, 0, 0, 0
			}
			p.h[0][0][e+1][b] = h11
			p.h[1][0][e+1][b] = h12
			p.h[2][0][e+1][b] = h21
			p.h[3][0][e+1][b] = h22
		}
	}
	for e := 0; e < p.numEnv; e++ {
		start := p.border[e]
		stop := p.border[e+1]
		width := 1.0 / float64(stop-start)
		for k := 0; k < psNrBands[idx]; k++ {
			b := int(kToI[k])
			var h, hs [4][2]float64
			for m := 0; m < 4; m++ {
				for c := 0; c < 2; c++ {
					h[m][c] = p.h[m][c][e][b]
					if !p.enableIPD && c == 1 {
						h[m][c] = 0
					}
					hs[m][c] = (p.h[m][c][e+1][b] - p.h[m][c][e][b]) * width
					if !p.enableIPD && c == 1 {
						hs[m][c] = 0
					}
				}
			}
			for n := start + 1; n <= stop; n++ {
				for m := 0; m < 4; m++ {
					h[m][0] += hs[m][0]
					h[m][1] += hs[m][1]
				}
				lr, li := l[k][n][0], l[k][n][1]
				rr, ri := r[k][n][0], r[k][n][1]
				// L = H11 s + H21 d, R = H12 s + H22 d (complex gains)
				l[k][n][0] = h[0][0]*lr - h[0][1]*li + h[2][0]*rr - h[2][1]*ri
				l[k][n][1] = h[0][0]*li + h[0][1]*lr + h[2][0]*ri + h[2][1]*rr
				r[k][n][0] = h[1][0]*lr - h[1][1]*li + h[3][0]*rr - h[3][1]*ri
				r[k][n][1] = h[1][0]*li + h[1][1]*lr + h[3][0]*ri + h[3][1]*rr
			}
		}
	}
	for m := 0; m < 4; m++ {
		for c := 0; c < 2; c++ {
			p.h[m][c][0] = p.h[m][c][p.numEnv]
		}
	}
}

// apply runs parametric stereo on the mono QMF signal x (38 slots) and writes
// the stereo pair to l and r.
func (p *psState) apply(x *[38][64][2]float64, l, r *[38][64][2]float64, top int) {
	is34 := p.is34
	idx := 0
	if is34 {
		idx = 1
	}
	top += psNrBands[idx] - 64
	if top < 0 {
		top = 0
	}
	for k := top; k < psNrBands[idx]; k++ {
		p.delay[k] = [psMaxDelay + psSlots][2]float64{}
	}
	if top < psNrAllpass[idx] {
		for k := top; k < psNrAllpass[idx]; k++ {
			p.apDelay[k] = [psAPLinks][psSlots + psAPDelay][2]float64{}
		}
	}
	var lb, rb [91][32][2]float64
	p.hybridAnalysis(x, &lb, is34)
	p.decorrelation(&rb, &lb, is34)
	p.stereoProcessing(&lb, &rb, is34)
	psHybridSynthesis(l, &lb, is34)
	psHybridSynthesis(r, &rb, is34)
}

func (s *sbrElem) applyPS() {
	c0, c1 := &s.data[0], &s.data[1]
	s.ps.apply(&c0.xOut, &c0.xOut, &c1.xOut, s.kx[1]+s.m[1])
}
