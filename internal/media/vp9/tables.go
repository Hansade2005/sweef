package vp9

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
	blockInvalid = 14
)

// Prediction modes.
const (
	dcPred = iota
	vPred
	hPred
	d45Pred
	d135Pred
	d117Pred
	d153Pred
	d207Pred
	d63Pred
	tmPred
	nearestMv
	nearMv
	zeroMv
	newMv
)

// Reference frames.
const (
	intraFrame  = 0
	lastFrame   = 1
	goldenFrame = 2
	altrefFrame = 3
)

// Transform types and sizes.
const (
	dctDct = iota
	adstDct
	dctAdst
	adstAdst
)

const (
	tx4x4 = iota
	tx8x8
	tx16x16
	tx32x32
)

const (
	only4x4 = iota
	allow8x8
	allow16x16
	allow32x32
	txModeSelect
)

const (
	partNone = iota
	partHorz
	partVert
	partSplit
)

const (
	keyFrame = 0
	// interpolation filters
	eightTap       = 0
	eightTapSmooth = 1
	eightTapSharp  = 2
	bilinear       = 3
	switchable     = 4
	// reference modes
	singleReference = 0
	compoundRef     = 1
	referenceSelect = 2
)

// segmentation features
const (
	segLvlAltQ = iota
	segLvlAltL
	segLvlRefFrame
	segLvlSkip
)

var bWidthLog2 = [13]uint8{0, 0, 1, 1, 1, 2, 2, 2, 3, 3, 3, 4, 4}
var bHeightLog2 = [13]uint8{0, 1, 0, 1, 2, 1, 2, 3, 2, 3, 4, 3, 4}
var num4x4Wide = [13]uint8{1, 1, 2, 2, 2, 4, 4, 4, 8, 8, 8, 16, 16}
var num4x4High = [13]uint8{1, 2, 1, 2, 4, 2, 4, 8, 4, 8, 16, 8, 16}
var miWidthLog2 = [13]uint8{0, 0, 0, 0, 0, 1, 1, 1, 2, 2, 2, 3, 3}
var num8x8Wide = [13]uint8{1, 1, 1, 1, 1, 2, 2, 2, 4, 4, 4, 8, 8}
var num8x8High = [13]uint8{1, 1, 1, 1, 2, 1, 2, 4, 2, 4, 8, 4, 8}
var sizeGroup = [13]uint8{0, 0, 0, 1, 1, 1, 2, 2, 2, 3, 3, 3, 3}
var maxTxSize = [13]uint8{0, 0, 0, 1, 1, 1, 2, 2, 2, 3, 3, 3, 3}
var txModeBiggest = [5]uint8{0, 1, 2, 3, 3}

var subsizeLookup = [4][13]uint8{
	{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12},
	{blockInvalid, blockInvalid, blockInvalid, block8x4, blockInvalid, blockInvalid, block16x8, blockInvalid, blockInvalid, block32x16, blockInvalid, blockInvalid, block64x32},
	{blockInvalid, blockInvalid, blockInvalid, block4x8, blockInvalid, blockInvalid, block8x16, blockInvalid, blockInvalid, block16x32, blockInvalid, blockInvalid, block32x64},
	{blockInvalid, blockInvalid, blockInvalid, block4x4, blockInvalid, blockInvalid, block8x8, blockInvalid, blockInvalid, block16x16, blockInvalid, blockInvalid, block32x32},
}

// ssSizeLookup[size][subx][suby]
var ssSizeLookup = [13][2][2]uint8{
	{{block4x4, blockInvalid}, {blockInvalid, blockInvalid}},
	{{block4x8, block4x4}, {blockInvalid, blockInvalid}},
	{{block8x4, blockInvalid}, {block4x4, blockInvalid}},
	{{block8x8, block8x4}, {block4x8, block4x4}},
	{{block8x16, block8x8}, {blockInvalid, block4x8}},
	{{block16x8, blockInvalid}, {block8x8, block8x4}},
	{{block16x16, block16x8}, {block8x16, block8x8}},
	{{block16x32, block16x16}, {blockInvalid, block8x16}},
	{{block32x16, blockInvalid}, {block16x16, block16x8}},
	{{block32x32, block32x16}, {block16x32, block16x16}},
	{{block32x64, block32x32}, {blockInvalid, block16x32}},
	{{block64x32, blockInvalid}, {block32x32, block32x16}},
	{{block64x64, block64x32}, {block32x64, block32x32}},
}

var mode2txfm = [14]uint8{dctDct, adstDct, dctAdst, dctDct, adstAdst, adstDct, dctAdst, dctAdst, adstDct, adstAdst, dctDct, dctDct, dctDct, dctDct}

var coefband4x4 = [16]uint8{0, 1, 1, 2, 2, 2, 3, 3, 3, 3, 4, 4, 4, 5, 5, 5}
var energyClass = [12]uint8{0, 1, 2, 3, 3, 4, 4, 5, 5, 5, 5, 5}

var mode2counter = [14]uint8{9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 0, 0, 3, 1}
var counterToContext = [19]uint8{2, 3, 4, 1, 3, 9, 0, 9, 9, 5, 5, 9, 5, 9, 9, 9, 9, 9, 6}

var idxNColumnToSubblock = [4][2]uint8{{1, 2}, {1, 3}, {3, 2}, {3, 3}}

// Trees (spec 9.3.1); leaves are stored negated.
var (
	partitionTree     = []int8{-partNone, 2, -partHorz, 4, -partVert, -partSplit}
	intraModeTree     = []int8{-dcPred, 2, -tmPred, 4, -vPred, 6, 8, 12, -hPred, 10, -d135Pred, -d117Pred, -d45Pred, 14, -d63Pred, 16, -d153Pred, -d207Pred}
	segmentTree       = []int8{2, 4, 6, 8, 10, 12, 0, -1, -2, -3, -4, -5, -6, -7}
	txSize32Tree      = []int8{-tx4x4, 2, -tx8x8, 4, -tx16x16, -tx32x32}
	txSize16Tree      = []int8{-tx4x4, 2, -tx8x8, -tx16x16}
	txSize8Tree       = []int8{-tx4x4, -tx8x8}
	interModeTree     = []int8{-(zeroMv - nearestMv), 2, -(nearestMv - nearestMv), 4, -(nearMv - nearestMv), -(newMv - nearestMv)}
	interpFilterTree  = []int8{-eightTap, 2, -eightTapSmooth, -eightTapSharp}
	mvJointTree       = []int8{0, 2, -1, 4, -2, -3}
	mvClassTree       = []int8{0, 2, -1, 4, 6, 8, -2, -3, 10, 12, -4, -5, -6, 14, 16, 18, -7, -8, -9, -10}
	mvFrTree          = []int8{0, 2, -1, 4, -2, -3}
	tokenTree         = []int8{0, 2, -1, 4, 6, 10, -2, 8, -3, -4, 12, 14, -5, -6, 16, 18, -7, -8, -9, -10}
	smallTokenTree    = []int8{0, 0, 0, 4, -1, -2}
	binaryTree        = []int8{0, -1}
	colsPartitionTree = []int8{-partHorz, -partSplit}
	rowsPartitionTree = []int8{-partVert, -partSplit}
)

var extraBits = [11][3]int{
	{0, 0, 0}, {0, 0, 1}, {0, 0, 2}, {0, 0, 3}, {0, 0, 4},
	{1, 1, 5}, {2, 2, 7}, {3, 3, 11}, {4, 4, 19}, {5, 5, 35}, {6, 14, 67},
}

var catProbs = [7][14]uint8{
	{0},
	{159},
	{165, 145},
	{173, 148, 140},
	{176, 155, 140, 135},
	{180, 157, 141, 134, 130},
	{254, 254, 254, 252, 249, 243, 230, 196, 177, 153, 140, 133, 130, 129},
}

var literalToType = [4]uint8{eightTapSmooth, eightTap, eightTapSharp, bilinear}

var segFeatureBits = [4]int{8, 6, 2, 0}
var segFeatureSigned = [4]bool{true, true, false, false}
