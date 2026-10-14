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

// The engine keeps the offset register scaled: low holds the 9-bit offset of the specification followed by sh
// bits that were read ahead, so a renormalisation is a change of sh and the stream is read 32 bits at a time
// when fewer than 8 bits are left. The context states are stored as pStateIdx<<1 | valMPS and every
// transition (range lookup, next state after an MPS or an LPS) is one table lookup on that value.
type cabacDec struct {
	data []byte // padded with >= 16 zero bytes
	bp   int    // index of the next byte to read ahead
	sh   int    // bits read ahead in low
	rng  uint32
	low  uint64
	st   [1024]uint8 // pStateIdx<<1 | valMPS
}

var (
	cabacLPS     [128][4]uint8 // range of the LPS by state and the two range bits
	cabacNextMPS [128]uint8
	cabacNextLPS [128]uint8
)

func init() {
	for s := 0; s < 128; s++ {
		state, mps := s>>1, s&1
		cabacLPS[s] = rangeTabLPS[state]
		n := state
		if n < 62 {
			n++
		}
		cabacNextMPS[s] = uint8(n<<1 | mps)
		m := mps
		if state == 0 {
			m ^= 1
		}
		cabacNextLPS[s] = uint8(int(transIdxLPS[state])<<1 | m)
	}
}

// bitPos is the position in the stream of the next bit the specification's 9-bit register would read.
func (c *cabacDec) bitPos() int { return c.bp*8 - c.sh }

// refill reads 32 bits ahead (zeros past the end of the data).
func (c *cabacDec) refill() {
	i := c.bp
	var v uint32
	if i+4 <= len(c.data) {
		v = binary.BigEndian.Uint32(c.data[i:])
	} else {
		for k := 0; k < 4; k++ {
			v <<= 8
			if i+k < len(c.data) {
				v |= uint32(c.data[i+k])
			}
		}
	}
	c.bp += 4
	c.low = c.low<<32 | uint64(v)
	c.sh += 32
}

// start initialises the engine at a byte-aligned bit position.
func (c *cabacDec) start(bitPos int) {
	c.bp = bitPos >> 3
	c.rng = 510
	c.low, c.sh = 0, -9 // the first refill brings the 9 bits of the offset register and 23 more
	c.refill()
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
	if c.sh < 8 {
		c.refill()
	}
	s := c.st[ctx&1023]
	lps := uint32(cabacLPS[s&127][(c.rng>>6)&3])
	rng := c.rng - lps
	thr := uint64(rng) << uint(c.sh)
	var b int
	if c.low >= thr {
		c.low -= thr
		b = int(s&1) ^ 1
		c.st[ctx&1023] = cabacNextLPS[s&127]
		rng = lps
	} else {
		b = int(s & 1)
		c.st[ctx&1023] = cabacNextMPS[s&127]
	}
	if rng < 256 {
		n := bits.LeadingZeros32(rng) - 23
		rng <<= uint(n)
		c.sh -= n
	}
	c.rng = rng
	return b
}

func (c *cabacDec) bypass() int {
	if c.sh < 8 {
		c.refill()
	}
	c.sh--
	thr := uint64(c.rng) << uint(c.sh)
	if c.low >= thr {
		c.low -= thr
		return 1
	}
	return 0
}

func (c *cabacDec) terminate() int {
	if c.sh < 8 {
		c.refill()
	}
	c.rng -= 2
	if c.low >= uint64(c.rng)<<uint(c.sh) {
		return 1
	}
	if c.rng < 256 {
		n := bits.LeadingZeros32(c.rng) - 23
		c.rng <<= uint(n)
		c.sh -= n
	}
	return 0
}
