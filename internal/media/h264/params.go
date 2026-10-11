package h264

// Sequence and picture parameter sets (7.3.2.1, 7.3.2.2).

// zigzag4 maps scan position to raster index inside a 4x4 block.
var zigzag4 = [16]uint8{0, 1, 4, 8, 5, 2, 3, 6, 9, 12, 13, 10, 7, 11, 14, 15}

// fieldScan4 is the field scan (Table 8-12 / Figure 8-9) of 4x4 blocks.
var fieldScan4 = [16]uint8{0, 4, 1, 8, 12, 5, 9, 13, 2, 6, 10, 14, 3, 7, 11, 15}

// zigzag8 maps scan position to raster index inside an 8x8 block.
var zigzag8 = [64]uint8{
	0, 1, 8, 16, 9, 2, 3, 10, 17, 24, 32, 25, 18, 11, 4, 5,
	12, 19, 26, 33, 40, 48, 41, 34, 27, 20, 13, 6, 7, 14, 21, 28,
	35, 42, 49, 56, 57, 50, 43, 36, 29, 22, 15, 23, 30, 37, 44, 51,
	58, 59, 52, 45, 38, 31, 39, 46, 53, 60, 61, 54, 47, 55, 62, 63,
}

// fieldScan8 is the 8x8 field scan (Table 8-13).
var fieldScan8 = [64]uint8{
	0, 8, 16, 1, 9, 24, 32, 17, 2, 25, 40, 48, 56, 33, 10, 3,
	18, 41, 49, 57, 26, 11, 4, 19, 34, 42, 50, 58, 27, 12, 5, 20,
	35, 43, 51, 59, 28, 13, 6, 21, 36, 44, 52, 60, 29, 14, 22, 37,
	45, 53, 61, 30, 7, 15, 38, 46, 54, 62, 23, 31, 39, 47, 55, 63,
}

// Default scaling lists in raster order (Tables 7-3 and 7-4).
var defaultScaling4 = [2][16]uint8{
	{6, 13, 20, 28, 13, 20, 28, 32, 20, 28, 32, 37, 28, 32, 37, 42},
	{10, 14, 20, 24, 14, 20, 24, 27, 20, 24, 27, 30, 24, 27, 30, 34},
}

var defaultScaling8 = [2][64]uint8{
	{6, 10, 13, 16, 18, 23, 25, 27,
		10, 11, 16, 18, 23, 25, 27, 29,
		13, 16, 18, 23, 25, 27, 29, 31,
		16, 18, 23, 25, 27, 29, 31, 33,
		18, 23, 25, 27, 29, 31, 33, 36,
		23, 25, 27, 29, 31, 33, 36, 38,
		25, 27, 29, 31, 33, 36, 38, 40,
		27, 29, 31, 33, 36, 38, 40, 42},
	{9, 13, 15, 17, 19, 21, 22, 24,
		13, 13, 17, 19, 21, 22, 24, 25,
		15, 17, 19, 21, 22, 24, 25, 27,
		17, 19, 21, 22, 24, 25, 27, 28,
		19, 21, 22, 24, 25, 27, 28, 30,
		21, 22, 24, 25, 27, 28, 30, 32,
		22, 24, 25, 27, 28, 30, 32, 33,
		24, 25, 27, 28, 30, 32, 33, 35},
}

// scalingMatrix holds resolved weight matrices in raster order. The 8x8 lists
// are ordered Intra Y, Inter Y, Intra Cb, Inter Cb, Intra Cr, Inter Cr.
type scalingMatrix struct {
	m4 [6][16]uint8
	m8 [6][64]uint8
}

func flatMatrix() scalingMatrix {
	var s scalingMatrix
	for i := range s.m4 {
		for j := range s.m4[i] {
			s.m4[i][j] = 16
		}
	}
	for i := range s.m8 {
		for j := range s.m8[i] {
			s.m8[i][j] = 16
		}
	}
	return s
}

// parseScalingList reads one list into raster order. It returns true when
// the useDefaultScalingMatrixFlag is set.
func parseScalingList(r *bitReader, dst []uint8, zz []uint8) (useDefault bool) {
	last, next := 8, 8
	for j := range dst {
		if next != 0 {
			delta := int(r.se())
			next = (last + delta + 256) % 256
			useDefault = j == 0 && next == 0
		}
		v := next
		if next == 0 {
			v = last
		}
		dst[zz[j]] = uint8(v)
		last = v
	}
	return
}

// parseScalingMatrix reads the lists of an SPS or PPS. n8 is the number of
// 8x8 lists transmitted (0, 2 or 6). fallbackSPS supplies the matrix used for
// the first list of each kind when it is not transmitted at picture level
// (rule B); nil selects the default lists (rule A).
func parseScalingMatrix(r *bitReader, n8 int, fallbackSPS *scalingMatrix) scalingMatrix {
	var m scalingMatrix
	n := 6 + n8
	for i := 0; i < n; i++ {
		present := r.flag()
		if i < 6 {
			dflt := defaultScaling4[i/3][:]
			fb := dflt
			if fallbackSPS != nil && (i == 0 || i == 3) {
				fb = fallbackSPS.m4[i][:]
			}
			if !present {
				if i == 0 || i == 3 {
					copy(m.m4[i][:], fb)
				} else {
					copy(m.m4[i][:], m.m4[i-1][:])
				}
				continue
			}
			if parseScalingList(r, m.m4[i][:], zigzag4[:]) {
				copy(m.m4[i][:], dflt)
			}
			continue
		}
		k := i - 6
		dflt := defaultScaling8[k&1][:]
		var fb []uint8
		switch {
		case k >= 2:
			fb = m.m8[k-2][:]
		case fallbackSPS != nil:
			fb = fallbackSPS.m8[k][:]
		default:
			fb = dflt
		}
		if !present {
			copy(m.m8[k][:], fb)
			continue
		}
		if parseScalingList(r, m.m8[k][:], zigzag8[:]) {
			copy(m.m8[k][:], dflt)
		}
	}
	// 8x8 lists that are not part of the syntax
	for k := n8; k < 6; k++ {
		switch {
		case k >= 2:
			m.m8[k] = m.m8[k-2]
		case fallbackSPS != nil:
			m.m8[k] = fallbackSPS.m8[k]
		default:
			m.m8[k] = defaultScaling8[k]
		}
	}
	return m
}

// SPS is a sequence parameter set.
type SPS struct {
	ID                 int
	Profile, Level     int
	ChromaFormat       int
	SeparatePlanes     bool
	BitDepthLuma       int
	BitDepthChroma     int
	Bypass             bool
	HasMatrix          bool
	Matrix             scalingMatrix
	Log2MaxFrameNum    int
	POCType            int
	Log2MaxPOCLsb      int
	DeltaPOCAlwaysZero bool
	OffsetNonRef       int32
	OffsetTopToBottom  int32
	OffsetForRefFrame  []int32
	MaxNumRefFrames    int
	GapsAllowed        bool
	MbW, MbH           int // MbH counts frame macroblock rows
	FrameMbsOnly       bool
	MBAFF              bool
	Direct8x8          bool
	CropL, CropR       int
	CropT, CropB       int
	ColorMatrix        int // VUI matrix_coefficients (2 = unspecified)
	FullRange          bool
	NumReorder         int
	raw                []byte
}

// ChromaArrayType is the chroma sampling that is actually coded in the
// macroblock layer (0 for monochrome and for separately coded planes).
func (s *SPS) ChromaArrayType() int {
	if s.SeparatePlanes {
		return 0
	}
	return s.ChromaFormat
}

func isHighProfile(p int) bool {
	switch p {
	case 100, 110, 122, 244, 44, 83, 86, 118, 128, 138, 139, 134, 135:
		return true
	}
	return false
}

func parseSPS(rbsp []byte) (*SPS, error) {
	r := newBitReader(rbsp)
	s := &SPS{ChromaFormat: 1, BitDepthLuma: 8, BitDepthChroma: 8, ColorMatrix: 2, Matrix: flatMatrix(), raw: append([]byte(nil), rbsp...)}
	s.Profile = int(r.u(8))
	r.u(8) // constraint flags
	s.Level = int(r.u(8))
	s.ID = int(r.ue())
	if s.ID > 31 {
		return nil, errBitstream
	}
	if isHighProfile(s.Profile) {
		s.ChromaFormat = int(r.ue())
		if s.ChromaFormat > 3 {
			return nil, errBitstream
		}
		if s.ChromaFormat == 3 {
			s.SeparatePlanes = r.flag()
		}
		s.BitDepthLuma = int(r.ue()) + 8
		s.BitDepthChroma = int(r.ue()) + 8
		if s.BitDepthLuma > 14 || s.BitDepthChroma > 14 {
			return nil, ErrUnsupported
		}
		s.Bypass = r.flag()
		if r.flag() {
			s.HasMatrix = true
			n8 := 2
			if s.ChromaFormat == 3 {
				n8 = 6
			}
			s.Matrix = parseScalingMatrix(r, n8, nil)
		}
	}
	s.Log2MaxFrameNum = int(r.ue()) + 4
	s.POCType = int(r.ue())
	switch s.POCType {
	case 0:
		s.Log2MaxPOCLsb = int(r.ue()) + 4
		if s.Log2MaxPOCLsb > 16 {
			return nil, errBitstream
		}
	case 1:
		s.DeltaPOCAlwaysZero = r.flag()
		s.OffsetNonRef = r.se()
		s.OffsetTopToBottom = r.se()
		n := int(r.ue())
		if n > 255 {
			return nil, errBitstream
		}
		for i := 0; i < n; i++ {
			s.OffsetForRefFrame = append(s.OffsetForRefFrame, r.se())
		}
	case 2:
	default:
		return nil, errBitstream
	}
	if s.Log2MaxFrameNum > 16 {
		return nil, errBitstream
	}
	s.MaxNumRefFrames = int(r.ue())
	s.GapsAllowed = r.flag()
	s.MbW = int(r.ue()) + 1
	mapH := int(r.ue()) + 1
	s.FrameMbsOnly = r.flag()
	s.MbH = mapH
	if !s.FrameMbsOnly {
		s.MBAFF = r.flag()
		s.MbH = mapH * 2
	}
	s.Direct8x8 = r.flag()
	if s.MbW > 1024 || s.MbH > 1024 || s.MbW*s.MbH > 1<<17 {
		return nil, ErrUnsupported
	}
	if r.flag() { // frame cropping
		cl, cr, ct, cb := int(r.ue()), int(r.ue()), int(r.ue()), int(r.ue())
		sw, sh := 1, 1
		switch s.ChromaArrayType() {
		case 1:
			sw, sh = 2, 2
		case 2:
			sw = 2
		}
		if !s.FrameMbsOnly {
			sh *= 2
		}
		s.CropL, s.CropR, s.CropT, s.CropB = cl*sw, cr*sw, ct*sh, cb*sh
		if s.CropL+s.CropR >= s.MbW*16 || s.CropT+s.CropB >= s.MbH*16 {
			s.CropL, s.CropR, s.CropT, s.CropB = 0, 0, 0, 0
		}
	}
	if r.flag() { // VUI
		parseVUI(r, s)
	}
	if r.overrun() {
		return nil, errBitstream
	}
	return s, nil
}

func parseVUI(r *bitReader, s *SPS) {
	if r.flag() { // aspect_ratio_info_present
		if r.u(8) == 255 {
			r.u(32)
		}
	}
	if r.flag() { // overscan
		r.u1()
	}
	if r.flag() { // video_signal_type
		r.u(3)
		s.FullRange = r.flag()
		if r.flag() {
			r.u(8)
			r.u(8)
			s.ColorMatrix = int(r.u(8))
		}
	}
	if r.flag() { // chroma loc
		r.ue()
		r.ue()
	}
	if r.flag() { // timing
		r.u(32)
		r.u(32)
		r.u1()
	}
	nal := r.flag()
	if nal {
		skipHRD(r)
	}
	vcl := r.flag()
	if vcl {
		skipHRD(r)
	}
	if nal || vcl {
		r.u1()
	}
	r.u1()        // pic_struct_present
	if r.flag() { // bitstream_restriction
		r.u1()
		r.ue()
		r.ue()
		r.ue()
		r.ue()
		s.NumReorder = int(r.ue())
		r.ue()
	}
}

func skipHRD(r *bitReader) {
	n := int(r.ue()) + 1
	r.u(4)
	r.u(4)
	for i := 0; i < n && i < 32; i++ {
		r.ue()
		r.ue()
		r.u1()
	}
	r.u(5)
	r.u(5)
	r.u(5)
	r.u(5)
}

// PPS is a picture parameter set.
type PPS struct {
	ID, SPSID         int
	CABAC             bool
	BottomPOCPresent  bool
	NumRefIdxDefault  [2]int
	WeightedPred      bool
	WeightedBipredIdc int
	InitQP            int
	InitQS            int
	ChromaQPOffset    [2]int
	DeblockControl    bool
	ConstrainedIntra  bool
	RedundantPicCnt   bool
	Transform8x8      bool
	Matrix            scalingMatrix
	sps               *SPS
	raw               []byte

	// slice groups (FMO)
	NumSliceGroups   int
	SliceGroupType   int
	RunLength        [8]int
	TopLeft, BotRght [8]int
	ChangeDirection  bool
	ChangeRate       int
	SliceGroupID     []uint8

	// dequantisation tables, [list][qp%6][raster index]
	ls4 [6][6][16]int32
	ls8 [6][6][64]int32
}

var normAdjust4 = [6][3]int32{{10, 16, 13}, {11, 18, 14}, {13, 20, 16}, {14, 23, 18}, {16, 25, 20}, {18, 29, 23}}
var normAdjust8 = [6][6]int32{
	{20, 18, 32, 19, 25, 24}, {22, 19, 35, 21, 28, 26}, {26, 23, 42, 24, 33, 31},
	{28, 25, 45, 26, 35, 33}, {32, 28, 51, 30, 40, 38}, {36, 32, 58, 34, 46, 43},
}

func (p *PPS) buildTables() {
	for l := 0; l < 6; l++ {
		for m := 0; m < 6; m++ {
			for k := 0; k < 16; k++ {
				i, j := k>>2, k&3
				var na int32
				switch {
				case i&1 == 0 && j&1 == 0:
					na = normAdjust4[m][0]
				case i&1 == 1 && j&1 == 1:
					na = normAdjust4[m][1]
				default:
					na = normAdjust4[m][2]
				}
				p.ls4[l][m][k] = int32(p.Matrix.m4[l][k]) * na
			}
		}
	}
	for l := 0; l < 6; l++ {
		for m := 0; m < 6; m++ {
			for k := 0; k < 64; k++ {
				i, j := k>>3, k&7
				var na int32
				switch {
				case i%4 == 0 && j%4 == 0:
					na = normAdjust8[m][0]
				case i%2 == 1 && j%2 == 1:
					na = normAdjust8[m][1]
				case i%4 == 2 && j%4 == 2:
					na = normAdjust8[m][2]
				case (i%4 == 0 && j%2 == 1) || (i%2 == 1 && j%4 == 0):
					na = normAdjust8[m][3]
				case (i%4 == 0 && j%4 == 2) || (i%4 == 2 && j%4 == 0):
					na = normAdjust8[m][4]
				default:
					na = normAdjust8[m][5]
				}
				p.ls8[l][m][k] = int32(p.Matrix.m8[l][k]) * na
			}
		}
	}
}

func parsePPS(rbsp []byte, spsList *[32]*SPS) (*PPS, error) {
	r := newBitReader(rbsp)
	p := &PPS{raw: append([]byte(nil), rbsp...)}
	p.ID = int(r.ue())
	p.SPSID = int(r.ue())
	if p.ID > 255 || p.SPSID > 31 {
		return nil, errBitstream
	}
	sps := spsList[p.SPSID]
	if sps == nil {
		return nil, errBitstream
	}
	p.sps = sps
	p.CABAC = r.flag()
	p.BottomPOCPresent = r.flag()
	p.NumSliceGroups = int(r.ue()) + 1
	if p.NumSliceGroups > 8 {
		return nil, errBitstream
	}
	if p.NumSliceGroups > 1 {
		p.SliceGroupType = int(r.ue())
		switch p.SliceGroupType {
		case 0:
			for i := 0; i < p.NumSliceGroups; i++ {
				p.RunLength[i] = int(r.ue()) + 1
			}
		case 2:
			for i := 0; i < p.NumSliceGroups-1; i++ {
				p.TopLeft[i] = int(r.ue())
				p.BotRght[i] = int(r.ue())
			}
		case 3, 4, 5:
			p.ChangeDirection = r.flag()
			p.ChangeRate = int(r.ue()) + 1
		case 6:
			n := int(r.ue()) + 1
			if n > sps.MbW*sps.MbH {
				return nil, errBitstream
			}
			bits := uint(0)
			for (1 << bits) < p.NumSliceGroups {
				bits++
			}
			p.SliceGroupID = make([]uint8, n)
			for i := range p.SliceGroupID {
				p.SliceGroupID[i] = uint8(r.u(bits))
			}
		case 1:
		default:
			return nil, errBitstream
		}
	}
	p.NumRefIdxDefault[0] = int(r.ue()) + 1
	p.NumRefIdxDefault[1] = int(r.ue()) + 1
	p.WeightedPred = r.flag()
	p.WeightedBipredIdc = int(r.u(2))
	p.InitQP = 26 + int(r.se())
	p.InitQS = 26 + int(r.se())
	p.ChromaQPOffset[0] = int(r.se())
	p.DeblockControl = r.flag()
	p.ConstrainedIntra = r.flag()
	p.RedundantPicCnt = r.flag()
	p.ChromaQPOffset[1] = p.ChromaQPOffset[0]
	p.Matrix = sps.Matrix
	if r.moreRBSPData() {
		p.Transform8x8 = r.flag()
		if r.flag() {
			n8 := 0
			if p.Transform8x8 {
				n8 = 2
				if sps.ChromaFormat == 3 {
					n8 = 6
				}
			}
			var fb *scalingMatrix
			if sps.HasMatrix {
				fb = &sps.Matrix
			}
			p.Matrix = parseScalingMatrix(r, n8, fb)
		}
		p.ChromaQPOffset[1] = int(r.se())
	}
	if r.overrun() || p.NumRefIdxDefault[0] > 32 || p.NumRefIdxDefault[1] > 32 {
		return nil, errBitstream
	}
	p.buildTables()
	return p, nil
}
