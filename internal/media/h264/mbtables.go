package h264

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
