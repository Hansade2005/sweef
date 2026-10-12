package hevc

const (
	sliceB = 0
	sliceP = 1
	sliceI = 2
)

// NAL unit types.
const (
	nalTrailN    = 0
	nalRaslN     = 8
	nalRaslR     = 9
	nalRadlN     = 6
	nalRadlR     = 7
	nalBlaWLP    = 16
	nalBlaNLP    = 18
	nalIdrWRadl  = 19
	nalIdrNLP    = 20
	nalCra       = 21
	nalVPS       = 32
	nalSPS       = 33
	nalPPS       = 34
	nalAUD       = 35
	nalEOS       = 36
	nalEOB       = 37
	nalSuffixSEI = 40
)

type predWeights struct {
	lumaLog2Denom   int
	chromaLog2Denom int
	lumaW           [2][16]int
	lumaO           [2][16]int
	chromaW         [2][16][2]int
	chromaO         [2][16][2]int
}

type sliceHeader struct {
	firstSlice    bool
	noOutputPrior bool
	ppsID         int
	dependent     bool
	segAddr       int
	sliceType     int
	picOutput     bool
	pocLsb        int
	rps           stRPS
	// long-term pictures
	numLT                   int
	ltPocLsb                [33]int
	ltUsed                  [33]bool
	ltMsbPresent            [33]bool
	ltMsbCycle              [33]int
	temporalMvp             bool
	saoLuma                 bool
	saoChroma               bool
	numRefIdx               [2]int
	listMod                 [2]bool
	listEntry               [2][16]int
	mvdL1Zero               bool
	cabacInit               bool
	colFromL0               bool
	colRefIdx               int
	pw                      *predWeights
	maxMergeCand            int
	qpDelta                 int
	sliceQp                 int
	cbQpOffset              int
	crQpOffset              int
	cuChromaQpOffsetEnabled bool
	deblockDisabled         bool
	betaOffsetDiv2          int
	tcOffsetDiv2            int
	loopFilterAcross        bool
	dataOffset              int // byte offset of slice data in the unescaped NAL payload
	numPicTotalCurr         int
	entry                   []int

	// screen content coding
	useIntegerMv             bool
	currPicRef               bool   // pps_curr_pic_ref_enabled_flag
	actDelta                 [3]int // slice_act_{y,cb,cr}_qp_offset
	nStBefore, nStAfter, nLt int    // pictures of the RPS used by the current picture

	// multilayer
	layer           int
	numActiveRefLay int
	refLayerIdc     [maxLayers]int // inter_layer_pred_layer_idc
	numIL0, numIL1  int
	pocResetIdc     int

	// derived per picture
	sliceAddrRs int // first CTB address (raster) of the slice containing this segment
	refPoc      [2][16]int32
	refLT       [2][16]bool
	refPic      [2][16]*Picture
	refLayer    [2][16]uint8
	nalType     int
	temporalID  int
	noBackward  bool // NoBackwardPredFlag
	idx         int  // index in the picture's slice table
}

// parseSliceHeader parses the slice segment header. prev is the previous
// independent slice header of the same picture (for dependent segments).
func (d *Decoder) parseSliceHeader(r *bitReader, nalType, tid int, prev *sliceHeader, layer int) (*sliceHeader, error) {
	sh := &sliceHeader{nalType: nalType, temporalID: tid, layer: layer}
	sh.firstSlice = r.flag()
	if nalType >= nalBlaWLP && nalType <= 23 {
		sh.noOutputPrior = r.flag()
	}
	sh.ppsID = int(r.ue())
	if sh.ppsID > 63 || d.pps[sh.ppsID] == nil {
		return nil, errBitstream
	}
	pps := d.pps[sh.ppsID]
	sps := d.sps[pps.spsID]
	if sps == nil {
		return nil, errBitstream
	}
	if !sh.firstSlice {
		if pps.dependentSlices {
			sh.dependent = r.flag()
		}
		sh.segAddr = int(r.u(ceilLog2(sps.wCtb * sps.hCtb)))
		if sh.segAddr >= sps.wCtb*sps.hCtb {
			return nil, errBitstream
		}
	}
	if sh.dependent {
		if prev == nil {
			return nil, errBitstream
		}
		// copy everything from the previous independent header
		cp := *prev
		cp.firstSlice = sh.firstSlice
		cp.noOutputPrior = sh.noOutputPrior
		cp.ppsID = sh.ppsID
		cp.dependent = true
		cp.segAddr = sh.segAddr
		sh = &cp
	} else {
		sh.sliceAddrRs = sh.segAddr
		r.skip(pps.numExtraSliceHdrBits)
		sh.sliceType = int(r.ue())
		if sh.sliceType > 2 {
			return nil, errBitstream
		}
		sh.picOutput = true
		if pps.outputFlagPresent {
			sh.picOutput = r.flag()
		}
		idr := nalType == nalIdrWRadl || nalType == nalIdrNLP
		var vps *VPS
		li := 0
		if layer > 0 {
			vps = d.vps[sps.vpsID]
			if vps == nil || !vps.hasExt || vps.layerIdx[layer] < 0 {
				return nil, ErrUnsupported
			}
			li = vps.layerIdx[layer]
		}
		if !idr || (layer > 0 && !vps.pocLsbNotPres[li]) {
			sh.pocLsb = int(r.u(sps.log2MaxPocLsb))
		}
		if !idr {
			if !r.flag() { // short_term_ref_pic_set_sps_flag
				if err := parseStRPS(r, sps.stRPS, len(sps.stRPS), len(sps.stRPS), &sh.rps); err != nil {
					return nil, err
				}
			} else {
				if len(sps.stRPS) == 0 {
					return nil, errBitstream
				}
				idx := 0
				if len(sps.stRPS) > 1 {
					idx = int(r.u(ceilLog2(len(sps.stRPS))))
				}
				if idx >= len(sps.stRPS) {
					return nil, errBitstream
				}
				sh.rps = sps.stRPS[idx]
			}
			if sps.ltPresent {
				numSps := 0
				if len(sps.ltPocLsb) > 0 {
					numSps = int(r.ue())
				}
				numPics := int(r.ue())
				if numSps > len(sps.ltPocLsb) || numSps+numPics > 32 {
					return nil, errBitstream
				}
				sh.numLT = numSps + numPics
				for i := 0; i < sh.numLT; i++ {
					if i < numSps {
						idx := 0
						if len(sps.ltPocLsb) > 1 {
							idx = int(r.u(ceilLog2(len(sps.ltPocLsb))))
						}
						if idx >= len(sps.ltPocLsb) {
							return nil, errBitstream
						}
						sh.ltPocLsb[i] = sps.ltPocLsb[idx]
						sh.ltUsed[i] = sps.ltUsed[idx]
					} else {
						sh.ltPocLsb[i] = int(r.u(sps.log2MaxPocLsb))
						sh.ltUsed[i] = r.flag()
					}
					sh.ltMsbPresent[i] = r.flag()
					if sh.ltMsbPresent[i] {
						c := int(r.ue())
						if i != 0 && i != numSps {
							c += sh.ltMsbCycle[i-1]
						}
						sh.ltMsbCycle[i] = c
					} else if i != 0 && i != numSps {
						sh.ltMsbCycle[i] = sh.ltMsbCycle[i-1]
					}
				}
			}
			if sps.temporalMvp {
				sh.temporalMvp = r.flag()
			}
		}
		if layer > 0 {
			if err := d.parseInterLayer(r, sh, vps, li); err != nil {
				return nil, err
			}
		}
		if sps.sao {
			sh.saoLuma = r.flag()
			if sps.chromaArrayType != 0 {
				sh.saoChroma = r.flag()
			}
		}
		// NumPicTotalCurr
		sh.numPicTotalCurr = sh.numActiveRefLay
		for i := 0; i < sh.rps.numNeg; i++ {
			if sh.rps.used[0][i] {
				sh.nStBefore++
			}
		}
		for i := 0; i < sh.rps.numPos; i++ {
			if sh.rps.used[1][i] {
				sh.nStAfter++
			}
		}
		for i := 0; i < sh.numLT; i++ {
			if sh.ltUsed[i] {
				sh.nLt++
			}
		}
		sh.numPicTotalCurr += sh.nStBefore + sh.nStAfter + sh.nLt
		if pps.currPicRef {
			sh.currPicRef = true
			sh.numPicTotalCurr++
		}
		if sh.sliceType != sliceI {
			sh.numRefIdx[0] = pps.numRefIdxDefault[0]
			if sh.sliceType == sliceB {
				sh.numRefIdx[1] = pps.numRefIdxDefault[1]
			}
			if r.flag() {
				sh.numRefIdx[0] = int(r.ue()) + 1
				if sh.sliceType == sliceB {
					sh.numRefIdx[1] = int(r.ue()) + 1
				}
			}
			if sh.numRefIdx[0] > 15 || sh.numRefIdx[1] > 15 {
				return nil, errBitstream
			}
			if pps.listsModification && sh.numPicTotalCurr > 1 {
				bits := ceilLog2(sh.numPicTotalCurr)
				for l := 0; l < 2; l++ {
					if l == 1 && sh.sliceType != sliceB {
						break
					}
					sh.listMod[l] = r.flag()
					if sh.listMod[l] {
						for i := 0; i < sh.numRefIdx[l]; i++ {
							sh.listEntry[l][i] = int(r.u(bits))
						}
					}
				}
			}
			if sh.sliceType == sliceB {
				sh.mvdL1Zero = r.flag()
			}
			if pps.cabacInitPresent {
				sh.cabacInit = r.flag()
			}
			if sh.temporalMvp {
				sh.colFromL0 = true
				if sh.sliceType == sliceB {
					sh.colFromL0 = r.flag()
				}
				if (sh.colFromL0 && sh.numRefIdx[0] > 1) || (!sh.colFromL0 && sh.numRefIdx[1] > 1) {
					sh.colRefIdx = int(r.ue())
					if sh.colRefIdx >= 16 {
						return nil, errBitstream
					}
				}
			}
			if (pps.weightedPred && sh.sliceType == sliceP) || (pps.weightedBi && sh.sliceType == sliceB) {
				if err := parsePredWeights(r, sh, sps); err != nil {
					return nil, err
				}
			}
			sh.maxMergeCand = 5 - int(r.ue())
			if sh.maxMergeCand < 1 || sh.maxMergeCand > 5 {
				return nil, errBitstream
			}
			switch sps.mvResControl {
			case 1:
				sh.useIntegerMv = true
			case 2:
				sh.useIntegerMv = r.flag()
			}
		}
		sh.qpDelta = int(r.se())
		sh.sliceQp = pps.initQp + sh.qpDelta
		if sh.sliceQp < -sps.qpBdOffY || sh.sliceQp > 51 {
			return nil, errBitstream
		}
		if pps.sliceChromaQpOffsets {
			sh.cbQpOffset = int(r.se())
			sh.crQpOffset = int(r.se())
		}
		if pps.sliceActOffsets {
			for k := 0; k < 3; k++ {
				sh.actDelta[k] = int(r.se())
			}
		}
		if pps.chromaQpOffsetList {
			sh.cuChromaQpOffsetEnabled = r.flag()
		}
		override := false
		if pps.deblockOverride {
			override = r.flag()
		}
		sh.deblockDisabled = pps.deblockDisabled
		sh.betaOffsetDiv2 = pps.betaOffsetDiv2
		sh.tcOffsetDiv2 = pps.tcOffsetDiv2
		if override {
			sh.deblockDisabled = r.flag()
			if !sh.deblockDisabled {
				sh.betaOffsetDiv2 = int(r.se())
				sh.tcOffsetDiv2 = int(r.se())
			} else {
				// ffmpeg keeps the offsets of the previous slice here, and
				// they leak into the filtering of neighbouring CTBs.
				sh.betaOffsetDiv2, sh.tcOffsetDiv2 = d.stickBeta, d.stickTc
			}
		}
		d.stickBeta, d.stickTc = sh.betaOffsetDiv2, sh.tcOffsetDiv2
		sh.loopFilterAcross = pps.loopFilterAcrossSlices
		if pps.loopFilterAcrossSlices && (sh.saoLuma || sh.saoChroma || !sh.deblockDisabled) {
			sh.loopFilterAcross = r.flag()
		}
	}
	if pps.tiles || pps.wpp {
		n := int(r.ue())
		if n > 0 {
			if n > sps.wCtb*sps.hCtb {
				return nil, errBitstream
			}
			l := int(r.ue()) + 1
			if l > 32 {
				return nil, errBitstream
			}
			for i := 0; i < n; i++ {
				sh.entry = append(sh.entry, int(r.u(l))+1)
				if r.err {
					return nil, errBitstream
				}
			}
		}
	}
	if pps.sliceHeaderExt {
		n := int(r.ue())
		r.skip(8 * n)
	}
	// byte_alignment()
	r.u1()
	r.alignByte()
	if r.err {
		return nil, errBitstream
	}
	sh.dataOffset = r.pos >> 3
	return sh, nil
}

func parsePredWeights(r *bitReader, sh *sliceHeader, sps *SPS) error {
	pw := &predWeights{}
	pw.lumaLog2Denom = int(r.ue())
	if pw.lumaLog2Denom > 7 {
		return errBitstream
	}
	pw.chromaLog2Denom = pw.lumaLog2Denom
	if sps.chromaArrayType != 0 {
		pw.chromaLog2Denom += int(r.se())
		if pw.chromaLog2Denom < 0 || pw.chromaLog2Denom > 7 {
			return errBitstream
		}
	}
	halfC := 1 << 7
	if sps.highPrecOffsets {
		halfC = 1 << uint(sps.bitDepthC-1)
	}
	nl := 1
	if sh.sliceType == sliceB {
		nl = 2
	}
	for l := 0; l < nl; l++ {
		n := sh.numRefIdx[l]
		var lf, cf [16]bool
		for i := 0; i < n; i++ {
			if !sh.currPicEntry(l, i) {
				lf[i] = r.flag()
			}
		}
		if sps.chromaArrayType != 0 {
			for i := 0; i < n; i++ {
				if !sh.currPicEntry(l, i) {
					cf[i] = r.flag()
				}
			}
		}
		for i := 0; i < n; i++ {
			pw.lumaW[l][i] = 1 << uint(pw.lumaLog2Denom)
			pw.chromaW[l][i] = [2]int{1 << uint(pw.chromaLog2Denom), 1 << uint(pw.chromaLog2Denom)}
			if lf[i] {
				pw.lumaW[l][i] += int(r.se())
				pw.lumaO[l][i] = int(r.se())
			}
			if cf[i] {
				for j := 0; j < 2; j++ {
					dw := int(r.se())
					do := int(r.se())
					w := (1 << uint(pw.chromaLog2Denom)) + dw
					pw.chromaW[l][i][j] = w
					o := halfC + do - ((halfC * w) >> uint(pw.chromaLog2Denom))
					if o < -halfC {
						o = -halfC
					} else if o > halfC-1 {
						o = halfC - 1
					}
					pw.chromaO[l][i][j] = o
				}
			}
		}
	}
	sh.pw = pw
	return nil
}

// parseInterLayer parses the inter-layer prediction syntax of the slice
// header of an enhancement layer picture (F.7.3.6.1) and derives the active
// reference layers.
func (d *Decoder) parseInterLayer(r *bitReader, sh *sliceHeader, vps *VPS, li int) error {
	nd := vps.numDirectRef[li]
	// RefLayerPicFlag / RefLayerPicIdc
	var refIdc [maxLayers]int
	numRef := 0
	for i := 0; i < nd; i++ {
		rl := vps.directRefIdx[li][i]
		tid := sh.temporalID
		if vps.subLayersMax[rl] >= tid && (tid == 0 || vps.maxTidIlRef[rl][li] > tid) {
			refIdc[numRef] = i
			numRef++
		}
	}
	interLayerPred := false
	numMinus1 := 0
	explicit := false
	if !vps.allRefActive && nd > 0 {
		interLayerPred = r.flag()
		if interLayerPred && nd > 1 {
			if !vps.maxOneActive {
				numMinus1 = int(r.u(ceilLog2u(nd)))
			}
			active := numActive(vps, numRef, interLayerPred, nd, numMinus1)
			if active != nd {
				explicit = true
				for i := 0; i < active; i++ {
					sh.refLayerIdc[i] = int(r.u(ceilLog2u(nd)))
					if sh.refLayerIdc[i] >= nd {
						return errBitstream
					}
				}
			}
		}
	}
	sh.numActiveRefLay = numActive(vps, numRef, interLayerPred, nd, numMinus1)
	if sh.numActiveRefLay > maxLayers {
		return errBitstream
	}
	if !explicit {
		for i := 0; i < sh.numActiveRefLay; i++ {
			sh.refLayerIdc[i] = refIdc[i]
		}
	}
	// split into the two inter-layer sets by view order
	cur := vps.viewID[li]
	zero := vps.viewID[0]
	for i := 0; i < sh.numActiveRefLay; i++ {
		ref := vps.viewID[vps.directRefIdx[li][sh.refLayerIdc[i]]]
		if (cur <= zero && cur <= ref) || (cur >= zero && cur >= ref) {
			sh.numIL0++
		} else {
			sh.numIL1++
		}
	}
	return nil
}

func numActive(vps *VPS, numRef int, enabled bool, nd, numMinus1 int) int {
	switch {
	case numRef == 0:
		return 0
	case vps.allRefActive:
		return numRef
	case !enabled:
		return 0
	case vps.maxOneActive || nd == 1:
		return 1
	}
	return numMinus1 + 1
}

// currPicEntry reports whether entry i of reference picture list l is the
// current picture (pps_curr_pic_ref_enabled_flag): the current picture ends
// each repetition of the temporary lists, and RefPicList0 ends with it when
// the list is not modified and the temporary list is longer (8.3.4).
func (sh *sliceHeader) currPicEntry(l, i int) bool {
	total := sh.numPicTotalCurr
	if !sh.currPicRef || total == 0 {
		return false
	}
	if sh.listMod[l] {
		return sh.listEntry[l][i]%total == total-1
	}
	if l == 0 && total > sh.numRefIdx[0] && i == sh.numRefIdx[0]-1 {
		return true
	}
	return i%total == total-1
}
