package h264

// Slice header (7.3.3) and reference picture marking syntax.

const (
	sliceP  = 0
	sliceB  = 1
	sliceI  = 2
	sliceSP = 3
	sliceSI = 4
)

type mmco struct {
	op   int
	a, b int
}

type refMod struct {
	idc int
	val int
}

type predWeight struct {
	lumaW, lumaO     [32]int
	chromaW, chromaO [32][2]int
	lumaFlag         [32]bool
	chromaFlag       [32]bool
}

type sliceHeader struct {
	nalType, nalRefIdc int
	idr                bool
	firstMB            int
	sliceType          int // 0..4
	pps                *PPS
	sps                *SPS
	frameNum           int
	fieldPic, bottom   bool
	idrPicID           int
	pocLsb             int
	deltaPOCBottom     int
	deltaPOC           [2]int
	redundantPicCnt    int
	structure          int
	sliceGroupCycle    int
	colourPlane        int
	directSpatial      bool
	numRef             [2]int
	refMods            [2][]refMod
	logWDLuma          int
	logWDChroma        int
	pw                 [2]predWeight
	hasPW              bool
	noOutputPrior      bool
	longTermRef        bool
	adaptiveMarking    bool
	mmcos              []mmco
	cabacInitIdc       int
	qp                 int // SliceQPY
	disableDeblock     int
	alphaOff, betaOff  int
}

func (h *sliceHeader) isB() bool { return h.sliceType == sliceB }
func (h *sliceHeader) isI() bool { return h.sliceType == sliceI || h.sliceType == sliceSI }

func (d *Decoder) parseSliceHeader(r *bitReader, nalType, nalRefIdc int) (*sliceHeader, error) {
	h := &sliceHeader{nalType: nalType, nalRefIdc: nalRefIdc, idr: nalType == 5}
	h.firstMB = int(r.ue())
	st := int(r.ue())
	if st > 9 {
		return nil, errBitstream
	}
	h.sliceType = st % 5
	ppsID := int(r.ue())
	if ppsID > 255 || d.pps[ppsID] == nil {
		return nil, errBitstream
	}
	h.pps = d.pps[ppsID]
	h.sps = h.pps.sps
	if d.sps[h.sps.ID] != nil && d.sps[h.sps.ID] != h.sps && d.sps[h.sps.ID].MbW == h.sps.MbW {
		h.sps = d.sps[h.sps.ID]
	}
	sps := h.sps
	if sps.ChromaFormat != 0 && sps.BitDepthLuma != sps.BitDepthChroma {
		return nil, ErrUnsupported
	}
	if sps.SeparatePlanes {
		h.colourPlane = int(r.u(2))
		if h.colourPlane > 2 {
			return nil, errBitstream
		}
	}
	h.frameNum = int(r.u(uint(sps.Log2MaxFrameNum)))
	h.structure = picFrame
	if !sps.FrameMbsOnly {
		h.fieldPic = r.flag()
		if h.fieldPic {
			h.bottom = r.flag()
			h.structure = picTop + btoi(h.bottom)
		}
	}
	if h.idr {
		h.idrPicID = int(r.ue())
	}
	switch sps.POCType {
	case 0:
		h.pocLsb = int(r.u(uint(sps.Log2MaxPOCLsb)))
		if h.pps.BottomPOCPresent && !h.fieldPic {
			h.deltaPOCBottom = int(r.se())
		}
	case 1:
		if !sps.DeltaPOCAlwaysZero {
			h.deltaPOC[0] = int(r.se())
			if h.pps.BottomPOCPresent && !h.fieldPic {
				h.deltaPOC[1] = int(r.se())
			}
		}
	}
	if h.pps.RedundantPicCnt {
		h.redundantPicCnt = int(r.ue())
	}
	if h.isB() {
		h.directSpatial = r.flag()
	}
	h.numRef = h.pps.NumRefIdxDefault
	if h.sliceType == sliceP || h.sliceType == sliceSP || h.isB() {
		if r.flag() {
			h.numRef[0] = int(r.ue()) + 1
			if h.isB() {
				h.numRef[1] = int(r.ue()) + 1
			}
		}
		if h.numRef[0] > 32 || h.numRef[1] > 32 {
			return nil, errBitstream
		}
	}
	if !h.isB() {
		h.numRef[1] = 0
	}
	if h.isI() {
		h.numRef = [2]int{}
	}
	// ref_pic_list_modification
	if !h.isI() {
		for l := 0; l < 1+btoi(h.isB()); l++ {
			if r.flag() {
				for n := 0; ; n++ {
					idc := int(r.ue())
					if idc == 3 {
						break
					}
					if idc > 5 || n > 66 {
						return nil, errBitstream
					}
					h.refMods[l] = append(h.refMods[l], refMod{idc, int(r.ue())})
				}
			}
		}
	}
	if (h.pps.WeightedPred && (h.sliceType == sliceP || h.sliceType == sliceSP)) || (h.pps.WeightedBipredIdc == 1 && h.isB()) {
		h.hasPW = true
		h.logWDLuma = int(r.ue())
		if sps.ChromaArrayType() != 0 {
			h.logWDChroma = int(r.ue())
		}
		for l := 0; l < 1+btoi(h.isB()); l++ {
			pw := &h.pw[l]
			for i := 0; i < h.numRef[l]; i++ {
				pw.lumaW[i] = 1 << uint(h.logWDLuma)
				if r.flag() {
					pw.lumaFlag[i] = true
					pw.lumaW[i] = int(r.se())
					pw.lumaO[i] = int(r.se())
				}
				for c := 0; c < 2; c++ {
					pw.chromaW[i][c] = 1 << uint(h.logWDChroma)
				}
				if sps.ChromaArrayType() != 0 && r.flag() {
					pw.chromaFlag[i] = true
					for c := 0; c < 2; c++ {
						pw.chromaW[i][c] = int(r.se())
						pw.chromaO[i][c] = int(r.se())
					}
				}
			}
		}
	}
	if nalRefIdc != 0 {
		maxPicNum := 1 << uint(sps.Log2MaxFrameNum)
		curPicNum := h.frameNum
		if h.fieldPic {
			maxPicNum <<= 1
			curPicNum = 2*h.frameNum + 1
		}
		if h.idr {
			h.noOutputPrior = r.flag()
			h.longTermRef = r.flag()
			h.adaptiveMarking = true
			if h.longTermRef {
				h.mmcos = append(h.mmcos, mmco{op: 6, a: 0})
			}
		} else if r.flag() {
			h.adaptiveMarking = true
			for n := 0; ; n++ {
				op := int(r.ue())
				if op == 0 {
					break
				}
				if op > 6 || n > 66 {
					return nil, errBitstream
				}
				m := mmco{op: op}
				switch op {
				case 1:
					m.a = (curPicNum - int(r.ue()) - 1) & (maxPicNum - 1)
				case 2:
					m.a = int(r.ue())
				case 3:
					m.a = (curPicNum - int(r.ue()) - 1) & (maxPicNum - 1)
					m.b = int(r.ue())
				case 4, 6:
					m.a = int(r.ue())
				}
				if op == 2 && m.a >= 32 || op == 3 && m.b >= 16 || op == 6 && m.a >= 16 || op == 4 && m.a > 16 {
					return nil, errBitstream
				}
				h.mmcos = append(h.mmcos, m)
			}
		}
	}
	if h.pps.CABAC && !h.isI() {
		h.cabacInitIdc = int(r.ue())
		if h.cabacInitIdc > 2 {
			return nil, errBitstream
		}
	}
	h.qp = h.pps.InitQP + int(r.se())
	if h.sliceType == sliceSP || h.sliceType == sliceSI {
		return nil, ErrUnsupported
	}
	if h.qp < -6*(sps.BitDepthLuma-8) || h.qp > 51 {
		return nil, errBitstream
	}
	if h.pps.DeblockControl {
		h.disableDeblock = int(r.ue())
		if h.disableDeblock != 1 {
			h.alphaOff = int(r.se()) * 2
			h.betaOff = int(r.se()) * 2
		}
	}
	if h.pps.NumSliceGroups > 1 && h.pps.SliceGroupType >= 3 && h.pps.SliceGroupType <= 5 {
		units := sps.MbW * (sps.MbH >> uint(btoi(!sps.FrameMbsOnly)))
		n := (units + h.pps.ChangeRate - 1) / h.pps.ChangeRate
		bits := uint(0)
		for (1 << bits) < n+1 {
			bits++
		}
		h.sliceGroupCycle = int(r.u(bits))
	}
	if r.overrun() {
		return nil, errBitstream
	}
	return h, nil
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}
