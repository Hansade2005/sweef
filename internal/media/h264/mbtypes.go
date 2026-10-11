package h264

// Macroblock type flags (the same bit layout FFmpeg uses; several predicates
// below test several bits at once).
const (
	mbIntra4x4   = 1 << 0
	mbIntra16x16 = 1 << 1
	mbIntraPCM   = 1 << 2
	mb16x16      = 1 << 3
	mb16x8       = 1 << 4
	mb8x16       = 1 << 5
	mb8x8        = 1 << 6
	mbInterlaced = 1 << 7
	mbDirect2    = 1 << 8
	mbRef0       = 1 << 9 // P_8x8ref0
	mbSkip       = 1 << 11
	mbP0L0       = 1 << 12
	mbP1L0       = 1 << 13
	mbP0L1       = 1 << 14
	mbP1L1       = 1 << 15
	mbL0         = mbP0L0 | mbP1L0
	mbL1         = mbP0L1 | mbP1L1
	mbL0L1       = mbL0 | mbL1
	mb8x8DCT     = 0x01000000

	mbInter = mb16x16 | mb16x8 | mb8x16 | mb8x8
)

func isIntraT(t uint32) bool { return t&7 != 0 }
func isInterT(t uint32) bool { return t&mbInter != 0 }
func isDir(t uint32, part, list int) bool {
	return t&(mbP0L0<<uint(part+2*list)) != 0
}
func usesList(t uint32, list int) bool { return t&((mbP0L0|mbP1L0)<<uint(2*list)) != 0 }

type iMbInfo struct {
	typ      uint32
	predMode int8 // Intra16x16 prediction mode in FFmpeg's numbering (unused; the spec numbering is derived from mb_type)
	cbp      int8
}

var iMbTypeInfo = [26]iMbInfo{
	{mbIntra4x4, -1, -1},
	{mbIntra16x16, 2, 0},
	{mbIntra16x16, 1, 0},
	{mbIntra16x16, 0, 0},
	{mbIntra16x16, 3, 0},
	{mbIntra16x16, 2, 16},
	{mbIntra16x16, 1, 16},
	{mbIntra16x16, 0, 16},
	{mbIntra16x16, 3, 16},
	{mbIntra16x16, 2, 32},
	{mbIntra16x16, 1, 32},
	{mbIntra16x16, 0, 32},
	{mbIntra16x16, 3, 32},
	{mbIntra16x16, 2, 15 + 0},
	{mbIntra16x16, 1, 15 + 0},
	{mbIntra16x16, 0, 15 + 0},
	{mbIntra16x16, 3, 15 + 0},
	{mbIntra16x16, 2, 15 + 16},
	{mbIntra16x16, 1, 15 + 16},
	{mbIntra16x16, 0, 15 + 16},
	{mbIntra16x16, 3, 15 + 16},
	{mbIntra16x16, 2, 15 + 32},
	{mbIntra16x16, 1, 15 + 32},
	{mbIntra16x16, 0, 15 + 32},
	{mbIntra16x16, 3, 15 + 32},
	{mbIntraPCM, -1, -1},
}

type pMbInfo struct {
	typ   uint32
	parts int
}

var pMbTypeInfo = [5]pMbInfo{
	{mb16x16 | mbP0L0, 1},
	{mb16x8 | mbP0L0 | mbP1L0, 2},
	{mb8x16 | mbP0L0 | mbP1L0, 2},
	{mb8x8 | mbP0L0 | mbP1L0, 4},
	{mb8x8 | mbP0L0 | mbP1L0 | mbRef0, 4},
}

var pSubMbTypeInfo = [4]pMbInfo{
	{mb16x16 | mbP0L0, 1},
	{mb16x8 | mbP0L0, 2},
	{mb8x16 | mbP0L0, 2},
	{mb8x8 | mbP0L0, 4},
}

var bMbTypeInfo = [23]pMbInfo{
	{mbDirect2 | mbL0L1, 1},
	{mb16x16 | mbP0L0, 1},
	{mb16x16 | mbP0L1, 1},
	{mb16x16 | mbP0L0 | mbP0L1, 1},
	{mb16x8 | mbP0L0 | mbP1L0, 2},
	{mb8x16 | mbP0L0 | mbP1L0, 2},
	{mb16x8 | mbP0L1 | mbP1L1, 2},
	{mb8x16 | mbP0L1 | mbP1L1, 2},
	{mb16x8 | mbP0L0 | mbP1L1, 2},
	{mb8x16 | mbP0L0 | mbP1L1, 2},
	{mb16x8 | mbP0L1 | mbP1L0, 2},
	{mb8x16 | mbP0L1 | mbP1L0, 2},
	{mb16x8 | mbP0L0 | mbP1L0 | mbP1L1, 2},
	{mb8x16 | mbP0L0 | mbP1L0 | mbP1L1, 2},
	{mb16x8 | mbP0L1 | mbP1L0 | mbP1L1, 2},
	{mb8x16 | mbP0L1 | mbP1L0 | mbP1L1, 2},
	{mb16x8 | mbP0L0 | mbP0L1 | mbP1L0, 2},
	{mb8x16 | mbP0L0 | mbP0L1 | mbP1L0, 2},
	{mb16x8 | mbP0L0 | mbP0L1 | mbP1L1, 2},
	{mb8x16 | mbP0L0 | mbP0L1 | mbP1L1, 2},
	{mb16x8 | mbP0L0 | mbP0L1 | mbP1L0 | mbP1L1, 2},
	{mb8x16 | mbP0L0 | mbP0L1 | mbP1L0 | mbP1L1, 2},
	{mb8x8 | mbP0L0 | mbP0L1 | mbP1L0 | mbP1L1, 4},
}

var bSubMbTypeInfo = [13]pMbInfo{
	{mbDirect2, 1},
	{mb16x16 | mbP0L0, 1},
	{mb16x16 | mbP0L1, 1},
	{mb16x16 | mbP0L0 | mbP0L1, 1},
	{mb16x8 | mbP0L0 | mbP1L0, 2},
	{mb8x16 | mbP0L0 | mbP1L0, 2},
	{mb16x8 | mbP0L1 | mbP1L1, 2},
	{mb8x16 | mbP0L1 | mbP1L1, 2},
	{mb16x8 | mbP0L0 | mbP0L1 | mbP1L0 | mbP1L1, 2},
	{mb8x16 | mbP0L0 | mbP0L1 | mbP1L0 | mbP1L1, 2},
	{mb8x8 | mbP0L0 | mbP1L0, 4},
	{mb8x8 | mbP0L1 | mbP1L1, 4},
	{mb8x8 | mbP0L0 | mbP0L1 | mbP1L0 | mbP1L1, 4},
}

// Cache layout: every cache is 5 rows of 8 entries. The block (x, y) of the
// current macroblock sits at c0 + x + 8*y; the left neighbours are in column
// 3 and the top neighbours in row 0.
const c0 = 4 + 1*8

// scan8 maps a luma4x4BlkIdx to its cache index.
var scan8 = [16]int{
	4 + 1*8, 5 + 1*8, 4 + 2*8, 5 + 2*8,
	6 + 1*8, 7 + 1*8, 6 + 2*8, 7 + 2*8,
	4 + 3*8, 5 + 3*8, 4 + 4*8, 5 + 4*8,
	6 + 3*8, 7 + 3*8, 6 + 4*8, 7 + 4*8,
}

const (
	refNotUsed   = -1 // list not used by the partition
	refNotAvail  = -2 // partition outside the slice / picture
	maxSlicesTab = 32
)
