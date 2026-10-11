package av1

// seqHdr is the sequence header OBU (spec 5.5).
type seqHdr struct {
	profile      int
	stillPicture bool
	reducedStill bool

	timingInfoPresent       bool
	decoderModelInfoPresent bool
	equalPictureInterval    bool
	bufferDelayLenM1        int
	bufferRemovalTimeLenM1  int
	framePresTimeLenM1      int
	opCnt                   int
	opIdc                   [32]int
	decoderModelPresentOp   [32]bool

	frameWidthBitsM1  int
	frameHeightBitsM1 int
	maxFrameW         int
	maxFrameH         int

	frameIDNumbersPresent bool
	deltaFrameIDLenM2     int
	addFrameIDLenM1       int

	use128             bool
	enableFilterIntra  bool
	enableIntraEdge    bool
	enableInterintra   bool
	enableMasked       bool
	enableWarped       bool
	enableDualFilter   bool
	enableOrderHint    bool
	enableJntComp      bool
	enableRefFrameMvs  bool
	forceScreenContent int
	forceIntegerMv     int
	orderHintBits      int

	enableSuperres    bool
	enableCdef        bool
	enableRestoration bool
	bitDepth          int
	mono              bool
	numPlanes         int
	subX, subY        int
	colorPrimaries    int
	transferChar      int
	matrixCoeffs      int
	colorRange        int
	chromaSamplePos   int
	separateUVDeltaQ  bool
	filmGrainPresent  bool
	operatingPointIdc int
}

// filmGrain holds the film_grain_params syntax elements (spec 5.9.30).
type filmGrain struct {
	apply             bool
	seed              int
	update            bool
	numYPoints        int
	yValue, yScaling  [16]int
	chromaFromLuma    bool
	numCbPoints       int
	cbValue, cbScale  [16]int
	numCrPoints       int
	crValue, crScale  [16]int
	scalingShift      int // grain_scaling_minus_8 + 8
	arCoeffLag        int
	arCoeffsY         [24]int // minus 128
	arCoeffsCb        [25]int
	arCoeffsCr        [25]int
	arCoeffShift      int // ar_coeff_shift_minus_6 + 6
	grainScaleShift   int
	cbMult, cbLumaMul int
	cbOffset          int
	crMult, crLumaMul int
	crOffset          int
	overlap           bool
	clipRestricted    bool
}

// frameHdr is the uncompressed header of one frame (spec 5.9.2).
type frameHdr struct {
	showExisting       bool
	frameToShow        int
	frameType          int
	frameIsIntra       bool
	showFrame          bool
	showable           bool
	errorRes           bool
	disableCdfUpd      bool
	allowScreenCT      bool
	forceIntMv         bool
	currentFrameID     int
	sizeOverride       bool
	orderHint          int
	primaryRef         int
	refreshFlags       int
	allowIntrabc       bool
	refFrameIdx        [refsPerFrame]int
	allowHighPrecMV    bool
	interpFilter       int
	motionModeSw       bool
	useRefFrameMvs     bool
	disableFrameEndUpd bool
	orderHints         [totalRefsPerFrame]int
	refSignBias        [totalRefsPerFrame]bool

	frameWidth, frameHeight   int
	upscaledWidth             int
	renderWidth, renderHeight int
	useSuperres               bool
	superresDenom             int
	miCols, miRows            int

	// tile info
	tileCols, tileRows         int
	tileColsLog2, tileRowsLog2 int
	miColStarts                [maxTileCols + 1]int
	miRowStarts                [maxTileRows + 1]int
	ctxUpdateTileID            int
	tileSizeBytes              int

	// quantization
	baseQIdx                          int
	dqYDc, dqUDc, dqUAc, dqVDc, dqVAc int
	usingQmatrix                      bool
	qmY, qmU, qmV                     int
	segQMLevel                        [3][maxSegments]int
	deltaQPresent                     bool
	deltaQRes                         int
	deltaLfPresent                    bool
	deltaLfRes                        int
	deltaLfMulti                      bool
	codedLossless, allLossless        bool
	losslessArray                     [maxSegments]bool

	// segmentation
	segEnabled      bool
	segUpdateMap    bool
	segTemporalUpd  bool
	segUpdateData   bool
	featEnabled     [maxSegments][segLvlMax]bool
	featData        [maxSegments][segLvlMax]int
	segIDPreSkip    bool
	lastActiveSegID int

	// loop filter
	lfLevel       [4]int
	lfSharpness   int
	lfDeltaEnable bool
	lfRefDeltas   [totalRefsPerFrame]int
	lfModeDeltas  [2]int

	// cdef
	cdefDamping                              int
	cdefBits                                 int
	cdefYPri, cdefYSec, cdefUVPri, cdefUVSec [8]int

	// loop restoration
	lrType     [3]int
	usesLr     bool
	lrUnitSize [3]int

	txMode          int
	referenceSelect bool
	skipModePresent bool
	skipModeFrame   [2]int
	allowWarped     bool
	reducedTxSet    bool

	gmType       [totalRefsPerFrame]int
	gmParams     [totalRefsPerFrame][6]int
	prevGmParams [totalRefsPerFrame][6]int

	film filmGrain

	// header size in bytes (used for OBU_FRAME)
	hdrBits int
}

func (s *seqHdr) relDist(a, b int) int {
	if !s.enableOrderHint {
		return 0
	}
	diff := a - b
	m := 1 << uint(s.orderHintBits-1)
	diff = (diff & (m - 1)) - (diff & m)
	return diff
}

func (s *seqHdr) parse(b *bitReader) error {
	*s = seqHdr{}
	s.profile = b.f(3)
	s.stillPicture = b.flag()
	s.reducedStill = b.flag()
	if s.reducedStill {
		b.f(5) // seq_level_idx[0]
		s.opCnt = 1
	} else {
		s.timingInfoPresent = b.flag()
		if s.timingInfoPresent {
			b.f(32)
			b.f(32)
			s.equalPictureInterval = b.flag()
			if s.equalPictureInterval {
				b.uvlc()
			}
			s.decoderModelInfoPresent = b.flag()
			if s.decoderModelInfoPresent {
				s.bufferDelayLenM1 = b.f(5)
				b.f(32)
				s.bufferRemovalTimeLenM1 = b.f(5)
				s.framePresTimeLenM1 = b.f(5)
			}
		}
		initialDisplayDelayPresent := b.flag()
		s.opCnt = b.f(5) + 1
		for i := 0; i < s.opCnt; i++ {
			s.opIdc[i] = b.f(12)
			lvl := b.f(5)
			if lvl > 7 {
				b.f(1)
			}
			if s.decoderModelInfoPresent {
				s.decoderModelPresentOp[i] = b.flag()
				if s.decoderModelPresentOp[i] {
					n := s.bufferDelayLenM1 + 1
					b.f(n)
					b.f(n)
					b.f(1)
				}
			}
			if initialDisplayDelayPresent {
				if b.flag() {
					b.f(4)
				}
			}
		}
	}
	s.operatingPointIdc = s.opIdc[0]
	s.frameWidthBitsM1 = b.f(4)
	s.frameHeightBitsM1 = b.f(4)
	s.maxFrameW = b.f(s.frameWidthBitsM1+1) + 1
	s.maxFrameH = b.f(s.frameHeightBitsM1+1) + 1
	if !s.reducedStill {
		s.frameIDNumbersPresent = b.flag()
	}
	if s.frameIDNumbersPresent {
		s.deltaFrameIDLenM2 = b.f(4)
		s.addFrameIDLenM1 = b.f(3)
	}
	s.use128 = b.flag()
	s.enableFilterIntra = b.flag()
	s.enableIntraEdge = b.flag()
	if s.reducedStill {
		s.forceScreenContent = selectScreenCont
		s.forceIntegerMv = selectIntegerMv
	} else {
		s.enableInterintra = b.flag()
		s.enableMasked = b.flag()
		s.enableWarped = b.flag()
		s.enableDualFilter = b.flag()
		s.enableOrderHint = b.flag()
		if s.enableOrderHint {
			s.enableJntComp = b.flag()
			s.enableRefFrameMvs = b.flag()
		}
		if b.flag() { // seq_choose_screen_content_tools
			s.forceScreenContent = selectScreenCont
		} else {
			s.forceScreenContent = b.f(1)
		}
		if s.forceScreenContent > 0 {
			if b.flag() {
				s.forceIntegerMv = selectIntegerMv
			} else {
				s.forceIntegerMv = b.f(1)
			}
		} else {
			s.forceIntegerMv = selectIntegerMv
		}
		if s.enableOrderHint {
			s.orderHintBits = b.f(3) + 1
		}
	}
	s.enableSuperres = b.flag()
	s.enableCdef = b.flag()
	s.enableRestoration = b.flag()
	// color_config
	highBitdepth := b.flag()
	if s.profile == 2 && highBitdepth {
		if b.flag() {
			s.bitDepth = 12
		} else {
			s.bitDepth = 10
		}
	} else {
		if highBitdepth {
			s.bitDepth = 10
		} else {
			s.bitDepth = 8
		}
	}
	if s.profile == 1 {
		s.mono = false
	} else {
		s.mono = b.flag()
	}
	s.numPlanes = 3
	if s.mono {
		s.numPlanes = 1
	}
	s.colorPrimaries, s.transferChar, s.matrixCoeffs = 2, 2, 2
	if b.flag() {
		s.colorPrimaries = b.f(8)
		s.transferChar = b.f(8)
		s.matrixCoeffs = b.f(8)
	}
	if s.mono {
		s.colorRange = b.f(1)
		s.subX, s.subY = 1, 1
		s.separateUVDeltaQ = false
	} else {
		if s.colorPrimaries == 1 && s.transferChar == 13 && s.matrixCoeffs == 0 {
			s.colorRange = 1
			s.subX, s.subY = 0, 0
		} else {
			s.colorRange = b.f(1)
			switch s.profile {
			case 0:
				s.subX, s.subY = 1, 1
			case 1:
				s.subX, s.subY = 0, 0
			default:
				if s.bitDepth == 12 {
					s.subX = b.f(1)
					if s.subX != 0 {
						s.subY = b.f(1)
					}
				} else {
					s.subX, s.subY = 1, 0
				}
			}
			if s.subX != 0 && s.subY != 0 {
				s.chromaSamplePos = b.f(2)
			}
		}
		s.separateUVDeltaQ = b.flag()
	}
	s.filmGrainPresent = b.flag()
	if b.err {
		return errBitstream
	}
	return nil
}
