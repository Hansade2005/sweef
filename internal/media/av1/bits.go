package av1

import "errors"

var errBitstream = errors.New("av1: invalid bitstream")

// ErrUnsupported is returned for streams outside the supported feature set.
var ErrUnsupported = errors.New("av1: unsupported stream feature")

// bitReader reads MSB-first bit fields (spec section 4.10 / 8.1).
type bitReader struct {
	buf []byte
	pos int // bit position
	err bool
}

func (b *bitReader) bit() int {
	i := b.pos >> 3
	if i >= len(b.buf) {
		b.err = true
		b.pos++
		return 0
	}
	v := int(b.buf[i]>>(7-uint(b.pos&7))) & 1
	b.pos++
	return v
}

// f reads an n-bit unsigned value (n <= 32).
func (b *bitReader) f(n int) int {
	x := 0
	for i := 0; i < n; i++ {
		x = x<<1 | b.bit()
	}
	return x
}

func (b *bitReader) flag() bool { return b.bit() != 0 }

// su reads a signed value of n bits (including the sign bit).
func (b *bitReader) su(n int) int {
	v := b.f(n)
	signMask := 1 << uint(n-1)
	if v&signMask != 0 {
		v -= 2 * signMask
	}
	return v
}

// ns reads a non-symmetric unsigned value in 0..n-1.
func (b *bitReader) ns(n int) int {
	w := 0
	for x := n; x != 0; x >>= 1 {
		w++
	}
	m := (1 << uint(w)) - n
	v := b.f(w - 1)
	if v < m {
		return v
	}
	extra := b.bit()
	return (v << 1) - m + extra
}

func (b *bitReader) le(n int) int {
	t := 0
	for i := 0; i < n; i++ {
		t += b.f(8) << uint(8*i)
	}
	return t
}

func (b *bitReader) leb128() int {
	v := 0
	for i := 0; i < 8; i++ {
		x := b.f(8)
		v |= (x & 0x7f) << uint(i*7)
		if x&0x80 == 0 {
			break
		}
	}
	return v
}

func (b *bitReader) uvlc() int {
	lz := 0
	for {
		if b.err {
			return 0
		}
		if b.bit() != 0 {
			break
		}
		lz++
	}
	if lz >= 32 {
		return (1 << 32) - 1
	}
	return b.f(lz) + (1 << uint(lz)) - 1
}

func (b *bitReader) byteAlign() { b.pos = (b.pos + 7) &^ 7 }
