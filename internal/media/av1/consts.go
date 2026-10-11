package av1

// Constants from section 3 of the AV1 specification.
const (
	refsPerFrame      = 7
	totalRefsPerFrame = 8
	numRefFrames      = 8
	maxSegments       = 8
	segLvlAltQ        = 0
	segLvlAltLfYV     = 1
	segLvlRefFrame    = 5
	segLvlSkip        = 6
	segLvlGlobalMv    = 7
	segLvlMax         = 8
	primaryRefNone    = 7
	maxLoopFilter     = 63
	frameLfCount      = 4
	warpedModelPrec   = 16
	maxTileCols       = 64
	maxTileRows       = 64
	maxTileWidth      = 4096
	maxTileArea       = 4096 * 2304
	superresNum       = 8
	superresDenomMin  = 9
	superresDenomBits = 3
	selectScreenCont  = 2
	selectIntegerMv   = 2
)

// OBU types.
const (
	obuSequenceHeader       = 1
	obuTemporalDelimiter    = 2
	obuFrameHeader          = 3
	obuTileGroup            = 4
	obuMetadata             = 5
	obuFrame                = 6
	obuRedundantFrameHeader = 7
	obuTileList             = 8
	obuPadding              = 15
)

// Frame types.
const (
	keyFrame       = 0
	interFrame     = 1
	intraOnlyFrame = 2
	switchFrame    = 3
)

// Reference frames.
const (
	refNone     = -1
	intraFrame  = 0
	lastFrame   = 1
	last2Frame  = 2
	last3Frame  = 3
	goldenFrame = 4
	bwdrefFrame = 5
	altref2Frm  = 6
	altrefFrame = 7
)

// Block sizes.
const (
	block4x4 = iota
	block4x8
	block8x4
	block8x8
	block8x16
	block16x8
	block16x16
	block16x32
	block32x16
	block32x32
	block32x64
	block64x32
	block64x64
	block64x128
	block128x64
	block128x128
	block4x16
	block16x4
	block8x32
	block32x8
	block16x64
	block64x16
	blockSizes
	blockInvalid = blockSizes
)

// Partition types.
const (
	partitionNone = iota
	partitionHorz
	partitionVert
	partitionSplit
	partitionHorzA
	partitionHorzB
	partitionVertA
	partitionVertB
	partitionHorz4
	partitionVert4
)

// Prediction modes.
const (
	dcPred = iota
	vPred
	hPred
	d45Pred
	d135Pred
	d113Pred
	d157Pred
	d203Pred
	d67Pred
	smoothPred
	smoothVPred
	smoothHPred
	paethPred
	uvCflPred
)

// Inter modes (YMode values, spec 6.10.22).
const (
	modeNearestMv      = 14
	modeNearMv         = 15
	modeGlobalMv       = 16
	modeNewMv          = 17
	modeNearestNearest = 18
	modeNearNear       = 19
	modeNearestNew     = 20
	modeNewNearest     = 21
	modeNearNew        = 22
	modeNewNear        = 23
	modeGlobalGlobal   = 24
	modeNewNew         = 25
)

// Transform sizes.
const (
	tx4x4 = iota
	tx8x8
	tx16x16
	tx32x32
	tx64x64
	tx4x8
	tx8x4
	tx8x16
	tx16x8
	tx16x32
	tx32x16
	tx32x64
	tx64x32
	tx4x16
	tx16x4
	tx8x32
	tx32x8
	tx16x64
	tx64x16
	txSizesAll
)

// Transform types.
const (
	dctDct = iota
	adstDct
	dctAdst
	adstAdst
	flipadstDct
	dctFlipadst
	flipadstFlipadst
	adstFlipadst
	flipadstAdst
	idtx
	vDct
	hDct
	vAdst
	hAdst
	vFlipadst
	hFlipadst
)

// Tx modes.
const (
	only4x4       = 0
	txModeLargest = 1
	txModeSelect  = 2
)

// Interpolation filters.
const (
	eighttap       = 0
	eighttapSmooth = 1
	eighttapSharp  = 2
	bilinear       = 3
	switchable     = 4
)

// Motion modes.
const (
	motionSimple    = 0
	motionObmc      = 1
	motionLocalWarp = 2
)

// Compound types.
const (
	compoundWedge    = 0
	compoundDiffwtd  = 1
	compoundAverage  = 2
	compoundIntra    = 3
	compoundDistance = 4
)

// Global motion types.
const (
	gmIdentity    = 0
	gmTranslation = 1
	gmRotzoom     = 2
	gmAffine      = 3
)

// Restoration types.
const (
	restoreNone       = 0
	restoreWiener     = 1
	restoreSgrproj    = 2
	restoreSwitchable = 3
)

func blockWidth(bs int) int  { return 4 * num4x4BlocksWide[bs] }
func blockHeight(bs int) int { return 4 * num4x4BlocksHigh[bs] }

func clip3(lo, hi, x int) int {
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func imin(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func imax(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func round2(x int, n uint) int {
	if n == 0 {
		return x
	}
	return (x + (1 << (n - 1))) >> n
}

func round2Signed(x int, n uint) int {
	if x >= 0 {
		return round2(x, n)
	}
	return -round2(-x, n)
}

func floorLog2(x int) int {
	s := 0
	for x > 1 {
		x >>= 1
		s++
	}
	return s
}

func ceilLog2(x int) int {
	if x < 2 {
		return 0
	}
	i := 1
	p := 2
	for p < x {
		i++
		p <<= 1
	}
	return i
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
