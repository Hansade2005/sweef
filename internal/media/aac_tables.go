package media

import "sync"

// Huffman decode tables built from the raw FFmpeg-derived code tables in
// aac_tables_gen.go. The generated books are in rodata order; the mapping to
// ISO/IEC 14496-3 spectral codebook numbers was established by maximum code
// length (book 1: 11, 2: 9, 3: 16, 4: 12, 5: 13, 6: 11, 7: 12, 8: 10, 9: 15,
// 10: 12) and verified by decoding ffmpeg-encoded streams.

type huffTab struct {
	maxLen int
	tab    []uint32 // len<<16 | symbol (0 = invalid)
}

func buildHuff(lens []uint8, codes func(i int) uint32) *huffTab {
	m := 0
	for _, l := range lens {
		if int(l) > m {
			m = int(l)
		}
	}
	h := &huffTab{maxLen: m, tab: make([]uint32, 1<<uint(m))}
	for i, l := range lens {
		base := codes(i) << uint(m-int(l))
		n := uint32(1) << uint(m-int(l))
		v := uint32(l)<<16 | uint32(i)
		for k := uint32(0); k < n; k++ {
			h.tab[base+k] = v
		}
	}
	return h
}

func u16codes(c []uint16) func(int) uint32 { return func(i int) uint32 { return uint32(c[i]) } }

var (
	aacHuffOnce sync.Once
	aacSpecHuff [12]*huffTab
	aacSFHuff   *huffTab
)

func aacInitHuff() {
	aacHuffOnce.Do(func() {
		aacSpecHuff[1] = buildHuff(aacRaw_r81_5_len[:], u16codes(aacRaw_r81_5_code[:]))
		aacSpecHuff[2] = buildHuff(aacRaw_r81_4_len[:], u16codes(aacRaw_r81_4_code[:]))
		aacSpecHuff[3] = buildHuff(aacRaw_r81_3_len[:], u16codes(aacRaw_r81_3_code[:]))
		aacSpecHuff[4] = buildHuff(aacRaw_r81_2_len[:], u16codes(aacRaw_r81_2_code[:]))
		aacSpecHuff[5] = buildHuff(aacRaw_r81_1_len[:], u16codes(aacRaw_r81_1_code[:]))
		aacSpecHuff[6] = buildHuff(aacRaw_r81_0_len[:], u16codes(aacRaw_r81_0_code[:]))
		aacSpecHuff[7] = buildHuff(aacRaw_r64_1_len[:], u16codes(aacRaw_r64_1_code[:]))
		aacSpecHuff[8] = buildHuff(aacRaw_r64_0_len[:], u16codes(aacRaw_r64_0_code[:]))
		aacSpecHuff[9] = buildHuff(aacRaw_r169_1_len[:], u16codes(aacRaw_r169_1_code[:]))
		aacSpecHuff[10] = buildHuff(aacRaw_r169_0_len[:], u16codes(aacRaw_r169_0_code[:]))
		aacSpecHuff[11] = buildHuff(aacRaw_r289_0_len[:], u16codes(aacRaw_r289_0_code[:]))
		aacSFHuff = buildHuff(aacRawSF_len[:], func(i int) uint32 { return aacRawSF_code[i] })
	})
}

// Scalefactor band offsets (ISO/IEC 14496-3 tables 4.84-4.110), indexed by
// sampling_frequency_index 0..12.
var aacSwbLong = map[int][]int{
	96: {0, 4, 8, 12, 16, 20, 24, 28, 32, 36, 40, 44, 48, 52, 56, 64, 72, 80, 88, 96, 108, 120, 132, 144, 156, 172, 188, 212, 240, 276, 320, 384, 448, 512, 576, 640, 704, 768, 832, 896, 960, 1024},
	64: {0, 4, 8, 12, 16, 20, 24, 28, 32, 36, 40, 44, 48, 52, 56, 64, 72, 80, 88, 100, 112, 124, 140, 156, 172, 192, 216, 240, 268, 304, 344, 384, 424, 464, 504, 544, 584, 624, 664, 704, 744, 784, 824, 864, 904, 944, 984, 1024},
	48: {0, 4, 8, 12, 16, 20, 24, 28, 32, 36, 40, 48, 56, 64, 72, 80, 88, 96, 108, 120, 132, 144, 160, 176, 196, 216, 240, 264, 292, 320, 352, 384, 416, 448, 480, 512, 544, 576, 608, 640, 672, 704, 736, 768, 800, 832, 864, 896, 928, 1024},
	32: {0, 4, 8, 12, 16, 20, 24, 28, 32, 36, 40, 48, 56, 64, 72, 80, 88, 96, 108, 120, 132, 144, 160, 176, 196, 216, 240, 264, 292, 320, 352, 384, 416, 448, 480, 512, 544, 576, 608, 640, 672, 704, 736, 768, 800, 832, 864, 896, 928, 960, 992, 1024},
	24: {0, 4, 8, 12, 16, 20, 24, 28, 32, 36, 40, 44, 52, 60, 68, 76, 84, 92, 100, 108, 116, 124, 136, 148, 160, 172, 188, 204, 220, 240, 260, 284, 308, 336, 364, 396, 432, 468, 508, 552, 600, 652, 704, 768, 832, 896, 960, 1024},
	16: {0, 8, 16, 24, 32, 40, 48, 56, 64, 72, 80, 88, 100, 112, 124, 136, 148, 160, 172, 184, 196, 212, 228, 244, 260, 280, 300, 320, 344, 368, 396, 424, 456, 492, 532, 572, 616, 664, 716, 772, 832, 896, 960, 1024},
	8:  {0, 12, 24, 36, 48, 60, 72, 84, 96, 108, 120, 132, 144, 156, 172, 188, 204, 220, 236, 252, 268, 288, 308, 328, 348, 372, 396, 420, 448, 476, 508, 544, 580, 620, 664, 712, 764, 820, 880, 944, 1024},
}

var aacSwbShort = map[int][]int{
	96: {0, 4, 8, 12, 16, 20, 24, 32, 40, 48, 64, 92, 128},
	64: {0, 4, 8, 12, 16, 20, 24, 32, 40, 48, 64, 92, 128},
	48: {0, 4, 8, 12, 16, 20, 28, 36, 44, 56, 68, 80, 96, 112, 128},
	32: {0, 4, 8, 12, 16, 20, 28, 36, 44, 56, 68, 80, 96, 112, 128},
	24: {0, 4, 8, 12, 16, 20, 24, 28, 36, 44, 52, 64, 76, 92, 108, 128},
	16: {0, 4, 8, 12, 16, 20, 24, 28, 32, 40, 48, 60, 72, 88, 108, 128},
	8:  {0, 4, 8, 12, 16, 20, 24, 28, 36, 44, 52, 60, 72, 88, 108, 128},
}

// swb table class per sampling_frequency_index
var aacSwbClass = [13]int{96, 96, 64, 48, 48, 32, 24, 24, 16, 16, 16, 8, 8}

var aacRates = [13]int{96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050, 16000, 12000, 11025, 8000, 7350}

var aacTnsMaxBandsLong = [13]int{31, 31, 34, 40, 42, 51, 46, 46, 42, 42, 42, 39, 39}
var aacTnsMaxBandsShort = [13]int{9, 9, 10, 14, 14, 14, 14, 14, 14, 14, 14, 14, 14}
