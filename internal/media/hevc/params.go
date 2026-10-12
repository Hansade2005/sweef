package hevc

// Parameter sets: SPS, PPS (VPS is not needed for decoding), scaling lists
// and short-term reference picture sets.

type stRPS struct {
	numNeg, numPos int
	delta          [2][16]int32 // [0] negative (S0), [1] positive (S1)
	used           [2][16]bool
}

func (r *stRPS) numDelta() int { return r.numNeg + r.numPos }

type scalingLists struct {
	list [4][6][64]uint8 // sizeId, matrixId, coefficients in diagonal scan order
	dc   [4][6]uint8     // sizeId 2 and 3
}

// scalingFactor holds the expanded matrices, raster order [y*n+x].
type scalingFactor struct {
	m [4][6][]uint8
}

type SPS struct {
	id                   int
	maxSubLayers         int
	chromaFormat         int
	chromaArrayType      int
	width, height        int
	cropL, cropR         int // in luma samples
	cropT, cropB         int
	bitDepthY, bitDepthC int
	log2MaxPocLsb        int
	maxDecPicBuf         [8]int
	maxNumReorder        [8]int
	maxLatencyPlus1      [8]int
	log2MinCb, log2Ctb   int
	log2MinTb, log2MaxTb int
	maxTHDepthInter      int
	maxTHDepthIntra      int
	scalingEnabled       bool
	scaling              *scalingLists
	amp                  bool
	sao                  bool
	pcm                  bool
	pcmBitDepthY         int
	pcmBitDepthC         int
	log2MinPcm           int
	log2MaxPcm           int
	pcmLoopFilterOff     bool
	stRPS                []stRPS
	ltPresent            bool
	ltPocLsb             []int
	ltUsed               []bool
	temporalMvp          bool
	strongIntra          bool

	// range extension
	tsRotation, tsContext, implicitRdpcm, explicitRdpcm bool
	extPrecision, intraSmoothingOff, highPrecOffsets    bool
	persistentRice, cabacBypassAlign                    bool

	fullRange bool
	matrix    int

	// derived
	subW, subH         uint // chroma shifts
	ctbSize            int
	wCtb, hCtb         int
	wMinCb, hMinCb     int
	qpBdOffY, qpBdOffC int
	wTb4, hTb4         int // size in 4x4 units
	minTbSzLog2        int
}

type PPS struct {
	id, spsID                    int
	dependentSlices              bool
	outputFlagPresent            bool
	numExtraSliceHdrBits         int
	signHiding                   bool
	cabacInitPresent             bool
	numRefIdxDefault             [2]int
	initQp                       int
	constrainedIntra             bool
	transformSkip                bool
	cuQpDelta                    bool
	diffCuQpDeltaDepth           int
	cbQpOffset, crQpOffset       int
	sliceChromaQpOffsets         bool
	weightedPred, weightedBi     bool
	transquantBypass             bool
	tiles                        bool
	wpp                          bool
	numTileCols, numTileRows     int
	uniformSpacing               bool
	colWidthMinus1               []int
	rowHeightMinus1              []int
	loopFilterAcrossTiles        bool
	loopFilterAcrossSlices       bool
	deblockControl               bool
	deblockOverride              bool
	deblockDisabled              bool
	betaOffsetDiv2, tcOffsetDiv2 int
	scaling                      *scalingLists
	listsModification            bool
	log2ParMrgLevel              int
	sliceHeaderExt               bool

	// range extension
	log2MaxTsSize             int
	crossComponentPred        bool
	chromaQpOffsetList        bool
	diffCuChromaQpOffsetDepth int
	chromaQpOffsetListLen     int
	cbQpOffsetList            [6]int
	crQpOffsetList            [6]int
	log2SaoOffsetScaleY       int
	log2SaoOffsetScaleC       int

	// derived for the active SPS (setup)
	setupFor       *SPS
	colBd, rowBd   []int // tile boundaries in CTBs
	rsToTs, tsToRs []int
	tileID         []int // by ts address
	tileRs         []int // by raster address
	minTbZs        []int32
	zsW            int
	factor         *scalingFactor
}

func parseProfileTierLevel(r *bitReader, profilePresent bool, maxSubLayersMinus1 int) {
	if profilePresent {
		r.skip(88)
	}
	r.skip(8) // general_level_idc
	var pp, lp [8]bool
	for i := 0; i < maxSubLayersMinus1; i++ {
		pp[i] = r.flag()
		lp[i] = r.flag()
	}
	if maxSubLayersMinus1 > 0 {
		for i := maxSubLayersMinus1; i < 8; i++ {
			r.skip(2)
		}
	}
	for i := 0; i < maxSubLayersMinus1; i++ {
		if pp[i] {
			r.skip(88)
		}
		if lp[i] {
			r.skip(8)
		}
	}
}

func parseSubLayerHRD(r *bitReader, cpbCnt int, subPic bool) {
	for i := 0; i < cpbCnt; i++ {
		r.ue()
		r.ue()
		if subPic {
			r.ue()
			r.ue()
		}
		r.u1()
	}
}

func parseHRD(r *bitReader, common bool, maxSub int) {
	nal, vcl, subPic := false, false, false
	if common {
		nal = r.flag()
		vcl = r.flag()
		if nal || vcl {
			subPic = r.flag()
			if subPic {
				r.skip(8 + 5 + 1 + 5)
			}
			r.skip(8)
			if subPic {
				r.skip(4)
			}
			r.skip(15)
		}
	}
	for i := 0; i <= maxSub; i++ {
		general := r.flag()
		within := true
		if !general {
			within = r.flag()
		}
		lowDelay := false
		if within {
			r.ue()
		} else {
			lowDelay = r.flag()
		}
		cpbCnt := 1
		if !lowDelay {
			cpbCnt = int(r.ue()) + 1
		}
		if cpbCnt > 32 {
			r.err = true
			return
		}
		if nal {
			parseSubLayerHRD(r, cpbCnt, subPic)
		}
		if vcl {
			parseSubLayerHRD(r, cpbCnt, subPic)
		}
	}
}

func parseVUI(r *bitReader, s *SPS) {
	if r.flag() { // aspect_ratio_info_present_flag
		if r.u(8) == 255 {
			r.skip(32)
		}
	}
	if r.flag() { // overscan_info_present_flag
		r.u1()
	}
	s.matrix = 2
	if r.flag() { // video_signal_type_present_flag
		r.skip(3)
		s.fullRange = r.flag()
		if r.flag() {
			r.skip(16)
			s.matrix = int(r.u(8))
		}
	}
	if r.flag() { // chroma_loc_info_present_flag
		r.ue()
		r.ue()
	}
	r.skip(3)     // neutral_chroma_indication, field_seq, frame_field_info_present
	if r.flag() { // default_display_window_flag
		r.ue()
		r.ue()
		r.ue()
		r.ue()
	}
	if r.flag() { // vui_timing_info_present_flag
		r.skip(64)
		if r.flag() {
			r.ue()
		}
		if r.flag() {
			parseHRD(r, true, s.maxSubLayers-1)
		}
	}
	if r.flag() { // bitstream_restriction_flag
		r.skip(3)
		r.ue()
		r.ue()
		r.ue()
		r.ue()
		r.ue()
	}
}

func ceilLog2(n int) int {
	l := 0
	for 1<<uint(l) < n {
		l++
	}
	return l
}

// parseSPS parses a sequence parameter set RBSP (after the NAL header).
func parseSPS(b []byte) (*SPS, error) {
	r := &bitReader{b: b}
	s := &SPS{matrix: 2}
	r.skip(4) // vps id
	s.maxSubLayers = int(r.u(3)) + 1
	r.u1()
	parseProfileTierLevel(r, true, s.maxSubLayers-1)
	s.id = int(r.ue())
	if s.id > 15 {
		return nil, errBitstream
	}
	s.chromaFormat = int(r.ue())
	if s.chromaFormat > 3 {
		return nil, errBitstream
	}
	if s.chromaFormat == 3 && r.flag() {
		return nil, ErrUnsupported // separate_colour_plane_flag
	}
	s.chromaArrayType = s.chromaFormat
	s.width = int(r.ue())
	s.height = int(r.ue())
	if s.width <= 0 || s.height <= 0 || s.width > 16384 || s.height > 16384 {
		return nil, errBitstream
	}
	subW, subH := 1, 1
	switch s.chromaFormat {
	case 1:
		s.subW, s.subH = 1, 1
		subW, subH = 2, 2
	case 2:
		s.subW, s.subH = 1, 0
		subW, subH = 2, 1
	}
	if r.flag() { // conformance_window_flag
		s.cropL = int(r.ue()) * subW
		s.cropR = int(r.ue()) * subW
		s.cropT = int(r.ue()) * subH
		s.cropB = int(r.ue()) * subH
	}
	s.bitDepthY = int(r.ue()) + 8
	s.bitDepthC = int(r.ue()) + 8
	if s.bitDepthY > 16 || s.bitDepthC > 16 {
		return nil, errBitstream
	}
	s.log2MaxPocLsb = int(r.ue()) + 4
	if s.log2MaxPocLsb > 16 {
		return nil, errBitstream
	}
	subLayerInfo := r.flag()
	start := s.maxSubLayers - 1
	if subLayerInfo {
		start = 0
	}
	for i := start; i < s.maxSubLayers; i++ {
		s.maxDecPicBuf[i] = int(r.ue()) + 1
		s.maxNumReorder[i] = int(r.ue())
		s.maxLatencyPlus1[i] = int(r.ue())
	}
	for i := 0; i < start; i++ {
		s.maxDecPicBuf[i] = s.maxDecPicBuf[start]
		s.maxNumReorder[i] = s.maxNumReorder[start]
		s.maxLatencyPlus1[i] = s.maxLatencyPlus1[start]
	}
	s.log2MinCb = int(r.ue()) + 3
	s.log2Ctb = s.log2MinCb + int(r.ue())
	s.log2MinTb = int(r.ue()) + 2
	s.log2MaxTb = s.log2MinTb + int(r.ue())
	if s.log2Ctb > 6 || s.log2Ctb < 4 || s.log2MaxTb > 5 || s.log2MinTb >= s.log2MinCb || s.log2MaxTb > s.log2Ctb {
		if s.log2Ctb < 4 && s.log2Ctb >= 3 {
			// 8x8 CTBs are not allowed by the specification.
		}
		return nil, errBitstream
	}
	s.maxTHDepthInter = int(r.ue())
	s.maxTHDepthIntra = int(r.ue())
	s.scalingEnabled = r.flag()
	if s.scalingEnabled {
		if r.flag() {
			sl, err := parseScalingListData(r, s.chromaFormat == 3)
			if err != nil {
				return nil, err
			}
			s.scaling = sl
		} else {
			s.scaling = defaultScalingLists()
		}
	}
	s.amp = r.flag()
	s.sao = r.flag()
	s.pcm = r.flag()
	if s.pcm {
		s.pcmBitDepthY = int(r.u(4)) + 1
		s.pcmBitDepthC = int(r.u(4)) + 1
		s.log2MinPcm = int(r.ue()) + 3
		s.log2MaxPcm = s.log2MinPcm + int(r.ue())
		s.pcmLoopFilterOff = r.flag()
		if s.pcmBitDepthY > s.bitDepthY || s.pcmBitDepthC > s.bitDepthC {
			return nil, errBitstream
		}
	}
	n := int(r.ue())
	if n > 64 {
		return nil, errBitstream
	}
	s.stRPS = make([]stRPS, n)
	for i := 0; i < n; i++ {
		if err := parseStRPS(r, s.stRPS, i, n, &s.stRPS[i]); err != nil {
			return nil, err
		}
	}
	s.ltPresent = r.flag()
	if s.ltPresent {
		k := int(r.ue())
		if k > 32 {
			return nil, errBitstream
		}
		for i := 0; i < k; i++ {
			s.ltPocLsb = append(s.ltPocLsb, int(r.u(s.log2MaxPocLsb)))
			s.ltUsed = append(s.ltUsed, r.flag())
		}
	}
	s.temporalMvp = r.flag()
	s.strongIntra = r.flag()
	if r.flag() { // vui
		parseVUI(r, s)
	}
	if r.flag() { // sps_extension_present_flag
		rangeExt := r.flag()
		multiExt := r.flag()
		ext3d := r.flag()
		scc := r.flag()
		r.skip(4)
		if rangeExt {
			s.tsRotation = r.flag()
			s.tsContext = r.flag()
			s.implicitRdpcm = r.flag()
			s.explicitRdpcm = r.flag()
			s.extPrecision = r.flag()
			s.intraSmoothingOff = r.flag()
			s.highPrecOffsets = r.flag()
			s.persistentRice = r.flag()
			s.cabacBypassAlign = r.flag()
		}
		if multiExt {
			r.u1()
		}
		if ext3d || scc {
			return nil, ErrUnsupported
		}
	}
	if r.err {
		return nil, errBitstream
	}
	if s.extPrecision || s.cabacBypassAlign {
		return nil, ErrUnsupported
	}
	s.ctbSize = 1 << uint(s.log2Ctb)
	s.wCtb = (s.width + s.ctbSize - 1) >> uint(s.log2Ctb)
	s.hCtb = (s.height + s.ctbSize - 1) >> uint(s.log2Ctb)
	mc := 1 << uint(s.log2MinCb)
	if s.width%mc != 0 || s.height%mc != 0 {
		return nil, errBitstream
	}
	s.wMinCb = s.width >> uint(s.log2MinCb)
	s.hMinCb = s.height >> uint(s.log2MinCb)
	s.qpBdOffY = 6 * (s.bitDepthY - 8)
	s.qpBdOffC = 6 * (s.bitDepthC - 8)
	s.wTb4 = (s.width + 3) >> 2
	s.hTb4 = (s.height + 3) >> 2
	if s.cropL+s.cropR >= s.width || s.cropT+s.cropB >= s.height {
		return nil, errBitstream
	}
	return s, nil
}

func parseStRPS(r *bitReader, all []stRPS, idx, num int, out *stRPS) error {
	inter := false
	if idx != 0 {
		inter = r.flag()
	}
	if inter {
		deltaIdx := 1
		if idx == num {
			deltaIdx = int(r.ue()) + 1
		}
		if deltaIdx > idx {
			return errBitstream
		}
		ref := &all[idx-deltaIdx]
		sign := r.u1()
		absd := int32(r.ue()) + 1
		deltaRps := absd
		if sign != 0 {
			deltaRps = -absd
		}
		nd := ref.numDelta()
		var usedFlag, useDelta [33]bool
		for j := 0; j <= nd; j++ {
			usedFlag[j] = r.flag()
			useDelta[j] = true
			if !usedFlag[j] {
				useDelta[j] = r.flag()
			}
		}
		i := 0
		for j := ref.numPos - 1; j >= 0; j-- {
			d := ref.delta[1][j] + deltaRps
			if d < 0 && useDelta[ref.numNeg+j] {
				if i >= 16 {
					return errBitstream
				}
				out.delta[0][i] = d
				out.used[0][i] = usedFlag[ref.numNeg+j]
				i++
			}
		}
		if deltaRps < 0 && useDelta[nd] {
			if i >= 16 {
				return errBitstream
			}
			out.delta[0][i] = deltaRps
			out.used[0][i] = usedFlag[nd]
			i++
		}
		for j := 0; j < ref.numNeg; j++ {
			d := ref.delta[0][j] + deltaRps
			if d < 0 && useDelta[j] {
				if i >= 16 {
					return errBitstream
				}
				out.delta[0][i] = d
				out.used[0][i] = usedFlag[j]
				i++
			}
		}
		out.numNeg = i
		i = 0
		for j := ref.numNeg - 1; j >= 0; j-- {
			d := ref.delta[0][j] + deltaRps
			if d > 0 && useDelta[j] {
				if i >= 16 {
					return errBitstream
				}
				out.delta[1][i] = d
				out.used[1][i] = usedFlag[j]
				i++
			}
		}
		if deltaRps > 0 && useDelta[nd] {
			if i >= 16 {
				return errBitstream
			}
			out.delta[1][i] = deltaRps
			out.used[1][i] = usedFlag[nd]
			i++
		}
		for j := 0; j < ref.numPos; j++ {
			d := ref.delta[1][j] + deltaRps
			if d > 0 && useDelta[ref.numNeg+j] {
				if i >= 16 {
					return errBitstream
				}
				out.delta[1][i] = d
				out.used[1][i] = usedFlag[ref.numNeg+j]
				i++
			}
		}
		out.numPos = i
		return nil
	}
	nn := int(r.ue())
	np := int(r.ue())
	if nn > 16 || np > 16 {
		return errBitstream
	}
	out.numNeg, out.numPos = nn, np
	var prev int32
	for i := 0; i < nn; i++ {
		prev -= int32(r.ue()) + 1
		out.delta[0][i] = prev
		out.used[0][i] = r.flag()
	}
	prev = 0
	for i := 0; i < np; i++ {
		prev += int32(r.ue()) + 1
		out.delta[1][i] = prev
		out.used[1][i] = r.flag()
	}
	return nil
}

// ---- scaling lists -----------------------------------------------------------

var defaultIntra8 = [64]uint8{
	16, 16, 16, 16, 17, 18, 21, 24,
	16, 16, 16, 16, 17, 19, 22, 25,
	16, 16, 17, 18, 20, 22, 25, 29,
	16, 16, 18, 21, 24, 27, 31, 36,
	17, 17, 20, 24, 30, 35, 41, 47,
	18, 19, 22, 27, 35, 44, 54, 65,
	21, 22, 25, 31, 41, 54, 70, 88,
	24, 25, 29, 36, 47, 65, 88, 115,
}

var defaultInter8 = [64]uint8{
	16, 16, 16, 16, 17, 18, 20, 24,
	16, 16, 16, 17, 18, 20, 24, 25,
	16, 16, 17, 18, 20, 24, 25, 28,
	16, 17, 18, 20, 24, 25, 28, 33,
	17, 18, 20, 24, 25, 28, 33, 41,
	18, 20, 24, 25, 28, 33, 41, 54,
	20, 24, 25, 28, 33, 41, 54, 71,
	24, 25, 28, 33, 41, 54, 71, 91,
}

func defaultList(sizeID, matrixID int) [64]uint8 {
	var l [64]uint8
	if sizeID == 0 {
		for i := 0; i < 16; i++ {
			l[i] = 16
		}
		return l
	}
	src := &defaultIntra8
	if matrixID >= 3 {
		src = &defaultInter8
	}
	for i := 0; i < 64; i++ {
		l[i] = src[scanDiag8[i][1]*8+scanDiag8[i][0]]
	}
	return l
}

func defaultScalingLists() *scalingLists {
	s := &scalingLists{}
	for sz := 0; sz < 4; sz++ {
		for m := 0; m < 6; m++ {
			s.list[sz][m] = defaultList(sz, m)
			s.dc[sz][m] = 16
		}
	}
	return s
}

func parseScalingListData(r *bitReader, chroma444 bool) (*scalingLists, error) {
	s := &scalingLists{}
	for sz := 0; sz < 4; sz++ {
		step := 1
		if sz == 3 {
			step = 3
		}
		for m := 0; m < 6; m += step {
			if !r.flag() { // scaling_list_pred_mode_flag
				delta := int(r.ue())
				if sz == 3 {
					delta *= 3
				}
				if delta > m {
					return nil, errBitstream
				}
				if delta == 0 {
					s.list[sz][m] = defaultList(sz, m)
					s.dc[sz][m] = 16
				} else {
					s.list[sz][m] = s.list[sz][m-delta]
					s.dc[sz][m] = s.dc[sz][m-delta]
				}
				continue
			}
			next := 8
			num := 64
			if sz == 0 {
				num = 16
			}
			if sz > 1 {
				next = int(r.se()) + 8
				s.dc[sz][m] = uint8(next)
			}
			for i := 0; i < num; i++ {
				next = (next + int(r.se()) + 256) & 255
				s.list[sz][m][i] = uint8(next)
			}
		}
	}
	if r.err {
		return nil, errBitstream
	}
	if chroma444 {
		// 32x32 chroma matrices are derived from the 16x16 lists.
		for _, m := range []int{1, 2, 4, 5} {
			s.list[3][m] = s.list[2][m]
			s.dc[3][m] = s.dc[2][m]
		}
	} else {
		for _, m := range []int{1, 2, 4, 5} {
			s.list[3][m] = s.list[2][m]
			s.dc[3][m] = s.dc[2][m]
		}
	}
	return s, nil
}

// expand builds the ScalingFactor matrices.
func (s *scalingLists) expand() *scalingFactor {
	f := &scalingFactor{}
	for sz := 0; sz < 4; sz++ {
		n := 4 << uint(sz)
		for m := 0; m < 6; m++ {
			t := make([]uint8, n*n)
			if sz == 0 {
				for i := 0; i < 16; i++ {
					t[int(scanDiag4[i][1])*4+int(scanDiag4[i][0])] = s.list[0][m][i]
				}
			} else {
				rep := n / 8
				for i := 0; i < 64; i++ {
					x0, y0 := int(scanDiag8[i][0])*rep, int(scanDiag8[i][1])*rep
					for k := 0; k < rep; k++ {
						for j := 0; j < rep; j++ {
							t[(y0+j)*n+x0+k] = s.list[sz][m][i]
						}
					}
				}
				if sz >= 2 {
					t[0] = s.dc[sz][m]
				}
			}
			f.m[sz][m] = t
		}
	}
	return f
}

// ---- PPS --------------------------------------------------------------------

func parsePPS(b []byte) (*PPS, error) {
	r := &bitReader{b: b}
	p := &PPS{loopFilterAcrossTiles: true}
	p.id = int(r.ue())
	p.spsID = int(r.ue())
	if p.id > 63 || p.spsID > 15 {
		return nil, errBitstream
	}
	p.dependentSlices = r.flag()
	p.outputFlagPresent = r.flag()
	p.numExtraSliceHdrBits = int(r.u(3))
	p.signHiding = r.flag()
	p.cabacInitPresent = r.flag()
	p.numRefIdxDefault[0] = int(r.ue()) + 1
	p.numRefIdxDefault[1] = int(r.ue()) + 1
	p.initQp = 26 + int(r.se())
	p.constrainedIntra = r.flag()
	p.transformSkip = r.flag()
	p.cuQpDelta = r.flag()
	if p.cuQpDelta {
		p.diffCuQpDeltaDepth = int(r.ue())
	}
	p.cbQpOffset = int(r.se())
	p.crQpOffset = int(r.se())
	p.sliceChromaQpOffsets = r.flag()
	p.weightedPred = r.flag()
	p.weightedBi = r.flag()
	p.transquantBypass = r.flag()
	p.tiles = r.flag()
	p.wpp = r.flag()
	p.numTileCols, p.numTileRows = 1, 1
	p.uniformSpacing = true
	if p.tiles {
		p.numTileCols = int(r.ue()) + 1
		p.numTileRows = int(r.ue()) + 1
		if p.numTileCols > 20 || p.numTileRows > 22 {
			return nil, errBitstream
		}
		p.uniformSpacing = r.flag()
		if !p.uniformSpacing {
			for i := 0; i < p.numTileCols-1; i++ {
				p.colWidthMinus1 = append(p.colWidthMinus1, int(r.ue()))
			}
			for i := 0; i < p.numTileRows-1; i++ {
				p.rowHeightMinus1 = append(p.rowHeightMinus1, int(r.ue()))
			}
		}
		p.loopFilterAcrossTiles = r.flag()
	}
	p.loopFilterAcrossSlices = r.flag()
	p.deblockControl = r.flag()
	if p.deblockControl {
		p.deblockOverride = r.flag()
		p.deblockDisabled = r.flag()
		if !p.deblockDisabled {
			p.betaOffsetDiv2 = int(r.se())
			p.tcOffsetDiv2 = int(r.se())
		}
	}
	if r.flag() { // pps_scaling_list_data_present_flag
		sl, err := parseScalingListData(r, true)
		if err != nil {
			return nil, err
		}
		p.scaling = sl
	}
	p.listsModification = r.flag()
	p.log2ParMrgLevel = int(r.ue()) + 2
	p.sliceHeaderExt = r.flag()
	p.log2MaxTsSize = 2
	if r.flag() { // pps_extension_present_flag
		rangeExt := r.flag()
		multiExt := r.flag()
		ext3d := r.flag()
		scc := r.flag()
		r.skip(4)
		if rangeExt {
			if p.transformSkip {
				p.log2MaxTsSize = int(r.ue()) + 2
			}
			p.crossComponentPred = r.flag()
			p.chromaQpOffsetList = r.flag()
			if p.chromaQpOffsetList {
				p.diffCuChromaQpOffsetDepth = int(r.ue())
				p.chromaQpOffsetListLen = int(r.ue()) + 1
				if p.chromaQpOffsetListLen > 6 {
					return nil, errBitstream
				}
				for i := 0; i < p.chromaQpOffsetListLen; i++ {
					p.cbQpOffsetList[i] = int(r.se())
					p.crQpOffsetList[i] = int(r.se())
				}
			}
			p.log2SaoOffsetScaleY = int(r.ue())
			p.log2SaoOffsetScaleC = int(r.ue())
		}
		_ = multiExt
		if ext3d || scc {
			return nil, ErrUnsupported
		}
	}
	if r.err || p.log2MaxTsSize > 5 {
		return nil, errBitstream
	}
	return p, nil
}

// setup derives the tile structure and scan conversion tables for sps.
func (p *PPS) setup(s *SPS) error {
	if p.setupFor == s {
		return nil
	}
	cols, rows := p.numTileCols, p.numTileRows
	if cols > s.wCtb || rows > s.hCtb {
		return errBitstream
	}
	colW := make([]int, cols)
	rowH := make([]int, rows)
	if p.uniformSpacing {
		for i := 0; i < cols; i++ {
			colW[i] = ((i+1)*s.wCtb)/cols - (i*s.wCtb)/cols
		}
		for i := 0; i < rows; i++ {
			rowH[i] = ((i+1)*s.hCtb)/rows - (i*s.hCtb)/rows
		}
	} else {
		sum := 0
		for i := 0; i < cols-1; i++ {
			colW[i] = p.colWidthMinus1[i] + 1
			sum += colW[i]
		}
		colW[cols-1] = s.wCtb - sum
		sum = 0
		for i := 0; i < rows-1; i++ {
			rowH[i] = p.rowHeightMinus1[i] + 1
			sum += rowH[i]
		}
		rowH[rows-1] = s.hCtb - sum
		if colW[cols-1] <= 0 || rowH[rows-1] <= 0 {
			return errBitstream
		}
	}
	p.colBd = make([]int, cols+1)
	p.rowBd = make([]int, rows+1)
	for i := 0; i < cols; i++ {
		p.colBd[i+1] = p.colBd[i] + colW[i]
	}
	for i := 0; i < rows; i++ {
		p.rowBd[i+1] = p.rowBd[i] + rowH[i]
	}
	n := s.wCtb * s.hCtb
	p.rsToTs = make([]int, n)
	p.tsToRs = make([]int, n)
	p.tileID = make([]int, n)
	for rs := 0; rs < n; rs++ {
		tbX, tbY := rs%s.wCtb, rs/s.wCtb
		tileX, tileY := 0, 0
		for i := 0; i < cols; i++ {
			if tbX >= p.colBd[i] {
				tileX = i
			}
		}
		for j := 0; j < rows; j++ {
			if tbY >= p.rowBd[j] {
				tileY = j
			}
		}
		v := 0
		for i := 0; i < tileX; i++ {
			v += rowH[tileY] * colW[i]
		}
		for j := 0; j < tileY; j++ {
			v += s.wCtb * rowH[j]
		}
		v += (tbY-p.rowBd[tileY])*colW[tileX] + tbX - p.colBd[tileX]
		p.rsToTs[rs] = v
		p.tsToRs[v] = rs
	}
	tid := 0
	for j := 0; j < rows; j++ {
		for i := 0; i < cols; i++ {
			for y := p.rowBd[j]; y < p.rowBd[j+1]; y++ {
				for x := p.colBd[i]; x < p.colBd[i+1]; x++ {
					p.tileID[p.rsToTs[y*s.wCtb+x]] = tid
				}
			}
			tid++
		}
	}
	p.tileRs = make([]int, n)
	for rs := 0; rs < n; rs++ {
		p.tileRs[rs] = p.tileID[p.rsToTs[rs]]
	}
	// MinTbAddrZs
	shift := uint(s.log2Ctb - s.log2MinTb)
	w := s.wCtb << shift
	h := s.hCtb << shift
	p.zsW = w
	p.minTbZs = make([]int32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			tbX := (x << uint(s.log2MinTb)) >> uint(s.log2Ctb)
			tbY := (y << uint(s.log2MinTb)) >> uint(s.log2Ctb)
			ctbRs := s.wCtb*tbY + tbX
			v := p.rsToTs[ctbRs] << (shift * 2)
			for i := uint(0); i < shift; i++ {
				m := 1 << i
				if m&x != 0 {
					v += m * m
				}
				if m&y != 0 {
					v += 2 * m * m
				}
			}
			p.minTbZs[y*w+x] = int32(v)
		}
	}
	// scaling factors
	if s.scalingEnabled {
		sl := s.scaling
		if p.scaling != nil {
			sl = p.scaling
		}
		p.factor = sl.expand()
	} else {
		p.factor = nil
	}
	p.setupFor = s
	return nil
}
