package h264

import (
	"encoding/binary"
	"math/bits"
	"math/rand"
	"testing"
)

// cabacRef is the straightforward engine of the specification (a 9-bit offset register, one renormalisation
// bit at a time) that the table driven engine is tested against.
type cabacRef struct {
	data []byte
	pos  int
	rng  uint32
	off  uint32
	st   [1024]uint8
}

func (c *cabacRef) readBits(n uint) uint32 {
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

func (c *cabacRef) start(bitPos int) {
	c.pos = bitPos
	c.rng = 510
	c.off = c.readBits(9)
}

func (c *cabacRef) bin(ctx int) int {
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

func (c *cabacRef) bypass() int {
	c.off = c.off<<1 | c.readBits(1)
	if c.off >= c.rng {
		c.off -= c.rng
		return 1
	}
	return 0
}

func (c *cabacRef) terminate() int {
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

// The table driven engine decodes exactly what the reference engine decodes (bins, bypass bins, terminating
// bins, the stream position), for random data and random context use.
func TestCABACMatchesReference(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	for iter := 0; iter < 200; iter++ {
		data := make([]byte, 600+r.Intn(400))
		for i := range data[:len(data)-32] {
			switch iter % 3 {
			case 0:
				data[i] = byte(r.Intn(256))
			case 1:
				data[i] = byte(r.Intn(256)) & byte(r.Intn(256)) // mostly zeros: long MPS runs
			default:
				data[i] = byte(r.Intn(256)) | byte(r.Intn(256))
			}
		}
		var ref cabacRef
		var fast cabacDec
		ref.data, fast.data = data, data
		for i := range ref.st {
			ref.st[i] = uint8(r.Intn(126)) // any state and MPS
			fast.st[i] = ref.st[i]
		}
		start := r.Intn(16) * 8
		ref.start(start)
		fast.start(start)
		for n := 0; n < 3000; n++ {
			var a, b int
			switch k := r.Intn(10); {
			case k < 7:
				ctx := r.Intn(1024)
				a, b = ref.bin(ctx), fast.bin(ctx)
			case k < 9:
				a, b = ref.bypass(), fast.bypass()
			default:
				a, b = ref.terminate(), fast.terminate()
				if a == 1 { // a terminating bin of 1 ends a slice or precedes PCM samples
					if ref.pos != fast.bitPos() {
						t.Fatalf("iter %d step %d: position %d, reference %d", iter, n, fast.bitPos(), ref.pos)
					}
					start = (ref.pos + 7) &^ 7
					ref.start(start)
					fast.start(start)
				}
			}
			if a != b {
				t.Fatalf("iter %d step %d: bin %d, reference %d", iter, n, b, a)
			}
			if ref.pos != fast.bitPos() {
				t.Fatalf("iter %d step %d: position %d, reference %d", iter, n, fast.bitPos(), ref.pos)
			}
		}
		for i := range ref.st {
			if ref.st[i] != fast.st[i] {
				t.Fatalf("iter %d: context %d state %d, reference %d", iter, i, fast.st[i], ref.st[i])
			}
		}
	}
}
