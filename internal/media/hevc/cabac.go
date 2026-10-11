package hevc

// Context index layout (follows the order of the initialisation tables).
const (
	ctxSaoMerge       = 0
	ctxSaoType        = 1
	ctxSplitCu        = 2 // 3
	ctxTqBypass       = 5
	ctxSkip           = 6 // 3
	ctxCuQpDelta      = 9 // 3 (two used)
	ctxPredMode       = 12
	ctxPartMode       = 13 // 4
	ctxPrevIntra      = 17
	ctxChromaPred     = 18
	ctxMergeFlag      = 20
	ctxMergeIdx       = 21
	ctxInterPredIdc   = 22 // 5
	ctxRefIdx         = 27 // 2
	ctxMvdGt0         = 31
	ctxMvdGt1         = 32
	ctxMvp            = 35
	ctxNoResidual     = 36
	ctxSplitTransform = 37 // 3
	ctxCbfLuma        = 40 // 2
	ctxCbfChroma      = 42 // 5
	ctxTransformSkip  = 47 // luma, chroma
	ctxRdpcm          = 49 // luma, chroma
	ctxRdpcmDir       = 51 // luma, chroma
	ctxLastX          = 53 // 18
	ctxLastY          = 71 // 18
	ctxCsbf           = 89 // 4
	ctxSig            = 93 // 44
	ctxGt1            = 137
	ctxGt2            = 161
	ctxLog2ResScale   = 167 // 8
	ctxResScaleSign   = 175 // 2
	ctxCuChromaQpFlag = 177
	ctxCuChromaQpIdx  = 178
	numCtx            = 179
)

var transIdxLps = [64]uint8{
	0, 0, 1, 2, 2, 4, 4, 5, 6, 7, 8, 9, 9, 11, 11, 12,
	13, 13, 15, 15, 16, 16, 18, 18, 19, 19, 21, 21, 22, 22, 23, 24,
	24, 25, 26, 26, 27, 27, 28, 29, 29, 30, 30, 30, 31, 32, 32, 33,
	33, 33, 34, 34, 35, 35, 35, 36, 36, 36, 37, 37, 37, 38, 38, 63,
}

// renormShift[lps>>3] is the renormalisation shift after an LPS.
var renormShift = [32]uint8{6, 5, 4, 4, 3, 3, 3, 3, 2, 2, 2, 2, 2, 2, 2, 2, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1}

// cabac is the arithmetic decoding engine. val holds the 9 bit offset
// scaled by 2^7 plus up to 7 look-ahead bits.
type cabac struct {
	data []byte
	pos  int // next byte to load
	rng  uint32
	val  uint32
	need int // -8..-1: look-ahead bits left is -need-1
	st   [numCtx]uint8
}

func (c *cabac) next() uint32 {
	if c.pos < len(c.data) {
		b := c.data[c.pos]
		c.pos++
		return uint32(b)
	}
	c.pos++
	return 0
}

// start initialises the engine at byte offset pos.
func (c *cabac) start(data []byte, pos int) {
	c.data = data
	c.pos = pos
	c.rng = 510
	c.val = c.next() << 8
	c.val |= c.next()
	c.need = -8
}

func (c *cabac) initContexts(initType, qp int) {
	if qp < 0 {
		qp = 0
	} else if qp > 51 {
		qp = 51
	}
	for i := 0; i < numCtx; i++ {
		v := int(cabacInit[initType][i])
		m := (v>>4)*5 - 45
		n := (v&15)<<3 - 16
		pre := ((m * qp) >> 4) + n
		if pre < 1 {
			pre = 1
		} else if pre > 126 {
			pre = 126
		}
		if pre <= 63 {
			c.st[i] = uint8((63 - pre) << 1)
		} else {
			c.st[i] = uint8((pre-64)<<1 | 1)
		}
	}
}

func (c *cabac) bin(ctx int) int {
	s := c.st[ctx]
	state := s >> 1
	mps := int(s & 1)
	lps := uint32(rangeTabLps[state][(c.rng>>6)&3])
	c.rng -= lps
	scaled := c.rng << 7
	if c.val < scaled {
		// MPS
		if state < 62 {
			c.st[ctx] = s + 2
		}
		if scaled < 256<<7 {
			c.rng = scaled >> 6
			c.val <<= 1
			c.need++
			if c.need == 0 {
				c.need = -8
				c.val |= c.next()
			}
		}
		return mps
	}
	// LPS
	n := uint(renormShift[lps>>3])
	c.val = (c.val - scaled) << n
	c.rng = lps << n
	bit := mps ^ 1
	if state == 0 {
		c.st[ctx] = uint8(bit)
	} else {
		c.st[ctx] = transIdxLps[state]<<1 | uint8(mps)
	}
	c.need += int(n)
	if c.need >= 0 {
		c.val |= c.next() << uint(c.need)
		c.need -= 8
	}
	return bit
}

func (c *cabac) bypass() int {
	c.val <<= 1
	c.need++
	if c.need >= 0 {
		c.need = -8
		c.val |= c.next()
	}
	scaled := c.rng << 7
	if c.val >= scaled {
		c.val -= scaled
		return 1
	}
	return 0
}

func (c *cabac) bypassBits(n int) int {
	v := 0
	for ; n > 0; n-- {
		v = v<<1 | c.bypass()
	}
	return v
}

func (c *cabac) terminate() int {
	c.rng -= 2
	scaled := c.rng << 7
	if c.val >= scaled {
		return 1
	}
	if scaled < 256<<7 {
		c.rng = scaled >> 6
		c.val <<= 1
		c.need++
		if c.need == 0 {
			c.need = -8
			c.val |= c.next()
		}
	}
	return 0
}

// bytePos returns the byte offset at which data following a terminating bin
// equal to 1 (pcm samples, the next substream) begins. The last bit consumed
// by the arithmetic decoder is the final bit of the flush sequence, so the
// following data starts at the first byte that has not been (partly) read.
func (c *cabac) bytePos() int { return c.pos }
