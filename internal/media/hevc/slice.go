package hevc

const (
	sliceB = 0
	sliceP = 1
	sliceI = 2
)

// NAL unit types.
const (
	nalTrailN   = 0
	nalRaslN    = 8
	nalRaslR    = 9
	nalRadlN    = 6
	nalRadlR    = 7
	nalBlaWLP   = 16
	nalBlaNLP   = 18
	nalIdrWRadl = 19
	nalIdrNLP   = 20
	nalCra      = 21
	nalVPS      = 32
	nalSPS      = 33
	nalPPS      = 34
	nalAUD      = 35
	nalEOS      = 36
	nalEOB      = 37
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

	// derived per picture
	sliceAddrRs int // first CTB address (raster) of the slice containing this segment
	refPoc      [2][16]int32
	refLT       [2][16]bool
	refPic      [2][16]*Picture
	nalType     int
	temporalID  int
	noBackward  bool // NoBackwardPredFlag
	idx         int  // index in the picture's slice table
}

// parseSliceHeader parses the slice segment header. prev is the previous
// independent slice header of the same picture (for dependent segments).
func (d *Decoder) parseSliceHeader(r *bitReader, nalType, tid int, prev *sliceHeader) (*sliceHeader, error) {
	sh := &sliceHeader{nalType: nalType, temporalID: tid}
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
		if nalType != nalIdrWRadl && nalType != nalIdrNLP {
			sh.pocLsb = int(r.u(sps.log2MaxPocLsb))
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
		if sps.sao {
			sh.saoLuma = r.flag()
			if sps.chromaArrayType != 0 {
				sh.saoChroma = r.flag()
			}
		}
		// NumPicTotalCurr
		for i := 0; i < sh.rps.numNeg; i++ {
			if sh.rps.used[0][i] {
				sh.numPicTotalCurr++
			}
		}
		for i := 0; i < sh.rps.numPos; i++ {
			if sh.rps.used[1][i] {
				sh.numPicTotalCurr++
			}
		}
		for i := 0; i < sh.numLT; i++ {
			if sh.ltUsed[i] {
				sh.numPicTotalCurr++
			}
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
			}
		}
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
			lf[i] = r.flag()
		}
		if sps.chromaArrayType != 0 {
			for i := 0; i < n; i++ {
				cf[i] = r.flag()
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
