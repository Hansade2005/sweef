package vp8enc

// boolEncoder is the boolean entropy encoder of RFC 6386 section 7.3, in the
// libvpx formulation (a 32-bit low value, a bit counter and carry propagation
// into the bytes already written).
type boolEncoder struct {
	buf   []byte
	low   uint32
	rng   uint32
	count int // negative while a byte is incomplete; starts at -24
}

func (e *boolEncoder) init(buf []byte) {
	e.buf = buf[:0]
	e.low = 0
	e.rng = 255
	e.count = -24
}

// put writes one bit that is 0 with probability prob/256.
func (e *boolEncoder) put(prob uint8, bit bool) {
	split := 1 + (((e.rng - 1) * uint32(prob)) >> 8)
	rng := split
	low := e.low
	if bit {
		low += split
		rng = e.rng - split
	}
	shift := int(norm[rng])
	rng <<= uint(shift)
	count := e.count + shift
	if count >= 0 {
		offset := shift - count
		if (low<<uint(offset-1))&0x80000000 != 0 {
			e.carry()
		}
		e.buf = append(e.buf, byte(low>>uint(24-offset)))
		low <<= uint(offset)
		shift = count
		low &= 0xffffff
		count -= 8
	}
	low <<= uint(shift)
	e.low, e.rng, e.count = low, rng, count
}

// carry propagates a carry into the bytes already written.
func (e *boolEncoder) carry() {
	i := len(e.buf) - 1
	for i >= 0 && e.buf[i] == 0xff {
		e.buf[i] = 0
		i--
	}
	if i >= 0 {
		e.buf[i]++
	}
}

// flag writes a bit with probability one half.
func (e *boolEncoder) flag(bit bool) { e.put(128, bit) }

// literal writes the n low bits of v, most significant first, each with probability one half.
func (e *boolEncoder) literal(v uint32, n int) {
	for n--; n >= 0; n-- {
		e.put(128, (v>>uint(n))&1 != 0)
	}
}

// finish flushes the encoder (32 padding bits, as libvpx's vp8_stop_encode) and returns the bytes.
func (e *boolEncoder) finish() []byte {
	for i := 0; i < 32; i++ {
		e.put(128, false)
	}
	return e.buf
}

// norm[r] is the number of left shifts that bring r (1..255) to at least 128.
var norm = func() (t [256]uint8) {
	for r := 1; r < 256; r++ {
		n := 0
		for x := r; x < 128; x <<= 1 {
			n++
		}
		t[r] = uint8(n)
	}
	return
}()
