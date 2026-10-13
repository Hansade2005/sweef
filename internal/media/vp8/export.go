package vp8

import "image"

// This file exports read-only views of the decoder's tables and the loop
// filter so that the encoder (internal/media/vp8enc) shares them instead of
// carrying a second copy that could drift from what the decoder expects.

// CoeffProbs is the token probability table indexed [plane][band][context][node].
type CoeffProbs = [4][8][3][11]uint8

// DefaultCoeffProbs returns the default token probabilities (RFC 6386 13.5).
func DefaultCoeffProbs() CoeffProbs { return defaultTokenProb }

// CoeffUpdateProbs returns the probabilities that gate token probability
// updates in the frame header (RFC 6386 13.4).
func CoeffUpdateProbs() CoeffProbs { return tokenProbUpdateProb }

// DefaultMVProbs returns the default motion vector probabilities.
func DefaultMVProbs() [2][19]uint8 { return defaultMVProb }

// MVUpdateProbs returns the probabilities that gate motion vector probability updates.
func MVUpdateProbs() [2][19]uint8 { return mvUpdateProb }

// ModeContexts returns the inter mode probabilities indexed by neighbour count.
func ModeContexts() [6][4]uint8 { return modeContexts }

// DefaultIntraModeProbs returns the inter-frame luma and chroma intra mode probabilities.
func DefaultIntraModeProbs() ([4]uint8, [3]uint8) { return defaultYModeProb, defaultUVModeProb }

// QuantTables returns the DC and AC dequantisation tables indexed by quantiser index.
func QuantTables() (dc, ac [128]uint16) { return dequantTableDC, dequantTableAC }

// SixTap returns the sub-pixel interpolation filters indexed by the eighth-pel fraction.
func SixTap() [8][6]int32 { return sixtap }

// FilterParams describes the loop filter of one frame for FilterFrame.
type FilterParams struct {
	KeyFrame bool
	Simple   bool
	Sharp    int
	MBW, MBH int
	// Level holds the filter level of each macroblock (row-major, MBW*MBH); Inner marks the
	// macroblocks whose inner (4x4) edges are filtered (those with coded coefficients).
	Level []uint8
	Inner []bool
}

// LoopFilter is a prepared loop filter for one frame that can filter macroblock by macroblock,
// so that an encoder can run it as a wavefront on several goroutines: a macroblock may be filtered
// once its left, top and top-right neighbours are done.
type LoopFilter struct {
	d   *Decoder
	img *image.YCbCr
}

// NewLoopFilter prepares the filter of img (planes starting at the top-left pixel of the macroblock grid).
func NewLoopFilter(img *image.YCbCr, p FilterParams) *LoopFilter {
	d := &Decoder{mbw: p.MBW, mbh: p.MBH, miStride: p.MBW + 1}
	d.mbs = make([]mbInfo, d.miStride*(p.MBH+1))
	d.hdr.KeyFrame = p.KeyFrame
	d.lf.simple = p.Simple
	d.lf.sharp = p.Sharp
	for y := 0; y < p.MBH; y++ {
		for x := 0; x < p.MBW; x++ {
			mi := &d.mbs[(y+1)*d.miStride+x+1]
			mi.lfLevel = p.Level[y*p.MBW+x]
			mi.lfInner = p.Inner[y*p.MBW+x]
		}
	}
	return &LoopFilter{d: d, img: img}
}

// MB filters one macroblock.
func (f *LoopFilter) MB(mbx, mby int) { f.d.filterMB(f.img, mbx, mby) }

// FilterFrame applies the VP8 loop filter in place, exactly as the decoder does.
func FilterFrame(img *image.YCbCr, p FilterParams) {
	NewLoopFilter(img, p).d.frameFilter(img)
}
