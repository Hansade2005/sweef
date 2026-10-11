package h264

// Macroblock type tables (Tables 7-11, 7-13, 7-14, 7-17, 7-18).

const (
	pmL0 = 1
	pmL1 = 2
	pmBi = 3
)

type interMB struct {
	n      int // number of partitions
	w, h   int // partition size in 4x4 units
	pm     [2]uint8
	is8x8  bool
	direct bool
	ref0   bool // P_8x8ref0: reference indices are not transmitted
}

var pMBTypes = [5]interMB{
	{1, 4, 4, [2]uint8{pmL0, 0}, false, false, false},
	{2, 4, 2, [2]uint8{pmL0, pmL0}, false, false, false},
	{2, 2, 4, [2]uint8{pmL0, pmL0}, false, false, false},
	{4, 2, 2, [2]uint8{}, true, false, false},
	{4, 2, 2, [2]uint8{}, true, false, true}, // P_8x8ref0
}

var bMBTypes = [23]interMB{
	{4, 2, 2, [2]uint8{}, false, true, false}, // B_Direct_16x16
	{1, 4, 4, [2]uint8{pmL0, 0}, false, false, false},
	{1, 4, 4, [2]uint8{pmL1, 0}, false, false, false},
	{1, 4, 4, [2]uint8{pmBi, 0}, false, false, false},
	{2, 4, 2, [2]uint8{pmL0, pmL0}, false, false, false},
	{2, 2, 4, [2]uint8{pmL0, pmL0}, false, false, false},
	{2, 4, 2, [2]uint8{pmL1, pmL1}, false, false, false},
	{2, 2, 4, [2]uint8{pmL1, pmL1}, false, false, false},
	{2, 4, 2, [2]uint8{pmL0, pmL1}, false, false, false},
	{2, 2, 4, [2]uint8{pmL0, pmL1}, false, false, false},
	{2, 4, 2, [2]uint8{pmL1, pmL0}, false, false, false},
	{2, 2, 4, [2]uint8{pmL1, pmL0}, false, false, false},
	{2, 4, 2, [2]uint8{pmL0, pmBi}, false, false, false},
	{2, 2, 4, [2]uint8{pmL0, pmBi}, false, false, false},
	{2, 4, 2, [2]uint8{pmL1, pmBi}, false, false, false},
	{2, 2, 4, [2]uint8{pmL1, pmBi}, false, false, false},
	{2, 4, 2, [2]uint8{pmBi, pmL0}, false, false, false},
	{2, 2, 4, [2]uint8{pmBi, pmL0}, false, false, false},
	{2, 4, 2, [2]uint8{pmBi, pmL1}, false, false, false},
	{2, 2, 4, [2]uint8{pmBi, pmL1}, false, false, false},
	{2, 4, 2, [2]uint8{pmBi, pmBi}, false, false, false},
	{2, 2, 4, [2]uint8{pmBi, pmBi}, false, false, false},
	{4, 2, 2, [2]uint8{}, true, false, false}, // B_8x8
}

type subMB struct {
	n      int // number of sub-partitions
	w, h   int // sub-partition size in 4x4 units
	pm     uint8
	direct bool
}

var pSubTypes = [4]subMB{
	{1, 2, 2, pmL0, false},
	{2, 2, 1, pmL0, false},
	{2, 1, 2, pmL0, false},
	{4, 1, 1, pmL0, false},
}

var bSubTypes = [13]subMB{
	{4, 1, 1, 0, true},
	{1, 2, 2, pmL0, false},
	{1, 2, 2, pmL1, false},
	{1, 2, 2, pmBi, false},
	{2, 2, 1, pmL0, false},
	{2, 1, 2, pmL0, false},
	{2, 2, 1, pmL1, false},
	{2, 1, 2, pmL1, false},
	{2, 2, 1, pmBi, false},
	{2, 1, 2, pmBi, false},
	{4, 1, 1, pmL0, false},
	{4, 1, 1, pmL1, false},
	{4, 1, 1, pmBi, false},
}

// blkX/blkY give the 4x4 position of luma4x4BlkIdx (decoding order).
var blkX = [16]int{0, 1, 0, 1, 2, 3, 2, 3, 0, 1, 0, 1, 2, 3, 2, 3}
var blkY = [16]int{0, 0, 1, 1, 0, 0, 1, 1, 2, 2, 3, 3, 2, 2, 3, 3}

// posToBlk maps raster 4x4 position to luma4x4BlkIdx.
var posToBlk = [16]int{0, 1, 4, 5, 2, 3, 6, 7, 8, 9, 12, 13, 10, 11, 14, 15}

// chromaQPTab maps qPI to QPc (Table 8-15).
var chromaQPTab = [52]uint8{
	0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19,
	20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 29, 30, 31, 32, 32, 33, 34, 34, 35, 35,
	36, 36, 37, 37, 37, 38, 38, 38, 39, 39, 39, 39,
}

// chromaQPf maps qPI (possibly negative at high bit depth) to QPc.
func chromaQPf(q int) int {
	if q < 30 {
		return q
	}
	return int(chromaQPTab[q])
}

var golombToIntra4x4CBPGray = [16]uint8{15, 0, 7, 11, 13, 14, 3, 5, 10, 12, 1, 2, 4, 8, 6, 9}
var golombToInterCBPGray = [16]uint8{0, 1, 2, 4, 8, 3, 5, 10, 12, 15, 7, 11, 13, 14, 6, 9}
