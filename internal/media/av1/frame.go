package av1

type mvec struct{ row, col int16 }

// miInfo is the per-4x4 mode information of the current frame.
type miInfo struct {
	refFrame     [2]int8
	yMode        uint8
	uvMode       uint8
	miSize       uint8
	isInter      bool
	skip         bool
	skipMode     bool
	segID        uint8
	interTxSize  uint8
	paletteSize  [2]uint8
	compGroupIdx uint8
	compoundIdx  uint8
	interpFilter [2]uint8
	motionMode   uint8
	mv           [2]mvec
	deltaLF      [4]int8
}

const maxSB = 32 // 4x4 units in the largest superblock

// frameDec holds the state for decoding one frame (frame level, tile level
// and block level, spec sections 5.11 and 7).
type frameDec struct {
	d   *Decoder
	seq *seqHdr
	fh  *frameHdr

	bitDepth       int
	subX, subY     int
	numPlanes      int
	miCols, miRows int
	cur            *frameBuf
	mi             []miInfo
	txTypes        []uint8
	lfTxSize       [3][]uint8
	cdefIdx        []int8
	cdefStride     int
	palColors      [2][]uint16 // per mi, 8 entries each (allocated on demand)
	prevSegIDs     []uint8
	motionFieldMvs [totalRefsPerFrame][]mvec
	refBufs        [totalRefsPerFrame]*refFrame
	refScaleX      [totalRefsPerFrame]int
	refScaleY      [totalRefsPerFrame]int
	cdf            cdfCtx // frame CDFs (after header processing)
	savedCdf       *cdfCtx
	sbSize4        int
	sbMask         int

	// loop restoration units
	lrUnitRows, lrUnitCols [3]int
	lrType                 [3][]uint8
	lrWiener               [3][][2][3]int8
	lrSgrSet               [3][]uint8
	lrSgrXqd               [3][][2]int8

	// ---- tile state ----
	sym                  symDec
	largeScale           bool // tile decoding of the large scale tile process
	tcdf                 cdfCtx
	miRowStart, miRowEnd int
	miColStart, miColEnd int
	currentQIndex        int
	deltaLF              [4]int
	refSgrXqd            [3][2]int
	refLrWiener          [3][2][3]int
	readDeltas           bool
	aboveLevelCtx        [3][]uint8
	aboveDcCtx           [3][]uint8
	aboveSegPred         []uint8
	leftLevelCtx         [3][]uint8
	leftDcCtx            [3][]uint8
	leftSegPred          []uint8
	blockDecoded         [3][maxSB + 3][maxSB + 3]bool

	// ---- block state ----
	miRow, miCol            int
	miSize                  int
	bw4, bh4                int
	hasChroma               bool
	availU, availL          bool
	availUChroma            bool
	availLChroma            bool
	segmentID               int
	lossless                bool
	skip                    bool
	skipMode                bool
	isInter                 bool
	useIntrabc              bool
	yMode, uvMode           int
	angleDeltaY             int
	angleDeltaUV            int
	useFilterIntra          bool
	filterIntraMode         int
	cflAlphaU, cflAlphaV    int
	paletteSizeY            int
	paletteSizeUV           int
	paletteColorsY          [8]uint16
	paletteColorsU          [8]uint16
	paletteColorsV          [8]uint16
	colorMapY               [64 * 64]uint8
	colorMapUV              [64 * 64]uint8
	refFrame                [2]int
	mv                      [2]mvec
	predMv                  [2]mvec
	refMvIdx                int
	motionMode              int
	compoundType            int
	interintra              bool
	interintraMode          int
	wedgeInterintra         bool
	wedgeIndex              int
	wedgeSign               int
	maskType                int
	compGroupIdx            int
	compoundIdx             int
	interpFilter            [2]int
	txSize                  int
	leftRefFrame            [2]int
	aboveRefFrame           [2]int
	leftIntra, aboveIntra   bool
	leftSingle, aboveSingle bool
	maxLumaW, maxLumaH      int

	// MV prediction state
	numMvFound        int
	newMvCount        int
	refStackMv        [10][2]mvec
	weightStack       [10]int
	globalMvs         [2]mvec
	foundMatch        bool
	closeMatches      int
	totalMatches      int
	newMvContext      int
	refMvContext      int
	zeroMvContext     int
	drlCtxStack       [10]int
	numSamples        int
	numSamplesScanned int
	candList          [8][4]int

	// residual scratch
	quant       [1024]int32
	dequant     [1024]int32
	residual2   [64 * 64]int32
	nzRows      int
	planeTxType int

	// prediction scratch
	predBuf         [2][128 * 128]int32 // inter prediction intermediates (compound)
	maskBuf         [128 * 128]uint8
	maskStride      int
	obmcPred        [128 * 128]int32
	patch           [(128 + 7) * (128 + 7)]uint16
	interBuf        [(128 + 7) * 128]int32
	isInterIntra    bool
	localValid      bool
	localWarpParams [6]int
	edgeAbove       [16 + 288]int32
	edgeLeft        [16 + 288]int32
	rows            [128]uint16
	lrScratch       *lrScratch
}

type lrScratch struct{}
