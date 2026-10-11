package h264

import (
	"encoding/binary"
	"math/bits"
)

// CABAC arithmetic decoding engine (9.3.1.2, 9.3.3.2).

var transIdxLPS = [64]uint8{
	0, 0, 1, 2, 2, 4, 4, 5, 6, 7, 8, 9, 9, 11, 11, 12,
	13, 13, 15, 15, 16, 16, 18, 18, 19, 19, 21, 21, 22, 22, 23, 24,
	24, 25, 26, 26, 27, 27, 28, 29, 29, 30, 30, 30, 31, 32, 32, 33,
	33, 33, 34, 34, 35, 35, 35, 36, 36, 36, 37, 37, 37, 38, 38, 63,
}

type cabacDec struct {
	data []byte // padded with >= 16 zero bytes
	pos  int    // bit position of the next unread bit
	rng  uint32
	off  uint32
	st   [1024]uint8 // pStateIdx<<1 | valMPS
}

func (c *cabacDec) readBits(n uint) uint32 {
	i := c.pos >> 3
	if i+8 > len(c.data) {
		c.pos += int(n)
		return 0
	}
	v := binary.BigEndian.Uint64(c.data[i:])
	r := uint32((v << uint(c.pos&7)) >> (64 - n))
	c.pos += int(n)
	return r
}

// start initialises the engine at a byte-aligned bit position.
func (c *cabacDec) start(bitPos int) {
	c.pos = bitPos
	c.rng = 510
	c.off = c.readBits(9)
}

func (c *cabacDec) initContexts(sliceQP, initIdc int, intra bool) {
	qp := sliceQP
	if qp < 0 {
		qp = 0
	} else if qp > 51 {
		qp = 51
	}
	for i := 0; i < 1024; i++ {
		var m, n int
		if intra {
			m, n = int(cabacInitI[i][0]), int(cabacInitI[i][1])
		} else {
			m, n = int(cabacInitPB[initIdc][i][0]), int(cabacInitPB[initIdc][i][1])
		}
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

func (c *cabacDec) bin(ctx int) int {
	s := c.st[ctx]
	state := s >> 1
	mps := s & 1
	lps := uint32(rangeTabLPS[state][(c.rng>>6)&3])
	c.rng -= lps
	var b int
	if c.off >= c.rng {
		b = int(mps ^ 1)
		c.off -= c.rng
		c.rng = lps
		if state == 0 {
			mps ^= 1
		}
		state = transIdxLPS[state]
	} else {
		b = int(mps)
		if state < 62 {
			state++
		}
	}
	c.st[ctx] = state<<1 | mps
	if c.rng < 256 {
		n := uint(bits.LeadingZeros32(c.rng) - 23)
		c.rng <<= n
		c.off = c.off<<n | c.readBits(n)
	}
	return b
}

func (c *cabacDec) bypass() int {
	c.off = c.off<<1 | c.readBits(1)
	if c.off >= c.rng {
		c.off -= c.rng
		return 1
	}
	return 0
}

func (c *cabacDec) terminate() int {
	c.rng -= 2
	if c.off >= c.rng {
		return 1
	}
	if c.rng < 256 {
		n := uint(bits.LeadingZeros32(c.rng) - 23)
		c.rng <<= n
		c.off = c.off<<n | c.readBits(n)
	}
	return 0
}
