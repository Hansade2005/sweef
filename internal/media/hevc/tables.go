package hevc

// Scan orders: scanOrder[scanIdx][log2BlockSize] lists (x, y) pairs.
// scanIdx 0 = up-right diagonal, 1 = horizontal, 2 = vertical.
var (
	scanOrder [3][4][][2]uint8
	scanPos   [3][4][]uint8 // inverse: scanPos[idx][log2][y<<log2|x] = position
	scanDiag4 [16][2]uint8
	scanDiag8 [64][2]uint8
)

func init() {
	for l := 0; l < 4; l++ {
		n := 1 << uint(l)
		// diagonal
		d := make([][2]uint8, 0, n*n)
		x, y := 0, 0
		for len(d) < n*n {
			for y >= 0 {
				if x < n && y < n {
					d = append(d, [2]uint8{uint8(x), uint8(y)})
				}
				y--
				x++
			}
			y = x
			x = 0
		}
		h := make([][2]uint8, 0, n*n)
		v := make([][2]uint8, 0, n*n)
		for i := 0; i < n; i++ {
			for j := 0; j < n; j++ {
				h = append(h, [2]uint8{uint8(j), uint8(i)})
				v = append(v, [2]uint8{uint8(i), uint8(j)})
			}
		}
		scanOrder[0][l], scanOrder[1][l], scanOrder[2][l] = d, h, v
		for s := 0; s < 3; s++ {
			inv := make([]uint8, n*n)
			for i, p := range scanOrder[s][l] {
				inv[int(p[1])<<uint(l)|int(p[0])] = uint8(i)
			}
			scanPos[s][l] = inv
		}
	}
	copy(scanDiag4[:], scanOrder[0][2])
	copy(scanDiag8[:], scanOrder[0][3])

	// DCT matrix
	for k := 0; k < 32; k++ {
		for n := 0; n < 32; n++ {
			a := ((2*n + 1) * k) % 128
			if a > 64 {
				a = 128 - a
			}
			var v int
			if a > 32 {
				v = -dctT[64-a]
			} else {
				v = dctT[a]
			}
			dctMat[k][n] = int16(v)
		}
	}
}

var dctT = [33]int{64, 90, 90, 90, 89, 88, 87, 85, 83, 82, 80, 78, 75, 73, 70, 67, 64, 61, 57, 54, 50, 46, 43, 38, 36, 31, 25, 22, 18, 13, 9, 4, 0}

// dctMat[k][n]: coefficient of basis function k at sample n (32 point).
var dctMat [32][32]int16

var dstMat = [4][4]int16{
	{29, 55, 74, 84},
	{74, 74, 0, -74},
	{84, -29, -74, 55},
	{55, -84, 74, -29},
}

var ctxIdxMap4 = [16]uint8{0, 1, 4, 5, 2, 3, 4, 5, 6, 6, 8, 8, 7, 7, 8, 8}

var levelScale = [6]int64{40, 45, 51, 57, 64, 72}

// chroma QP mapping for ChromaArrayType 1 (qPi >= 30).
var qpCTable = [14]int{29, 30, 31, 32, 33, 33, 34, 34, 35, 35, 36, 36, 37, 37}

var intraPredAngle = [35]int{
	0, 0, 32, 26, 21, 17, 13, 9, 5, 2, 0, -2, -5, -9, -13, -17, -21, -26,
	-32, -26, -21, -17, -13, -9, -5, -2, 0, 2, 5, 9, 13, 17, 21, 26, 32,
}

// invAngle for modes 11..25.
var invAngle = [35]int{
	11: -4096, 12: -1638, 13: -910, 14: -630, 15: -482, 16: -390, 17: -315, 18: -256,
	19: -315, 20: -390, 21: -482, 22: -630, 23: -910, 24: -1638, 25: -4096,
}

// 4:2:2 chroma intra mode mapping (Table 8-3).
var mode422 = [35]uint8{0, 1, 2, 2, 2, 2, 3, 5, 7, 8, 10, 11, 13, 15, 16, 18, 19, 20, 21, 22, 23, 23, 24, 24, 25, 25, 26, 27, 27, 28, 28, 29, 29, 30, 31}

var lumaFilter = [4][8]int32{
	{0, 0, 0, 64, 0, 0, 0, 0},
	{-1, 4, -10, 58, 17, -5, 1, 0},
	{-1, 4, -11, 40, 40, -11, 4, -1},
	{0, 1, -5, 17, 58, -10, 4, -1},
}

var chromaFilter = [8][4]int32{
	{0, 64, 0, 0},
	{-2, 58, 10, -2},
	{-4, 54, 16, -2},
	{-6, 46, 28, -4},
	{-4, 36, 36, -4},
	{-4, 28, 46, -6},
	{-2, 16, 54, -4},
	{-2, 10, 58, -2},
}

// deblocking tables
var tcTable = [54]uint8{
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 1, 1, 1, 1, 1, 1, 1, 2, 2, 2,
	2, 3, 3, 3, 3, 4, 4, 4, 5, 5, 6, 6, 7, 8, 9, 10, 11, 13, 14, 16, 18, 20, 22, 24,
}

var betaTable = [52]uint8{
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 20, 22, 24,
	26, 28, 30, 32, 34, 36, 38, 40, 42, 44, 46, 48, 50, 52, 54, 56, 58, 60, 62, 64,
}
