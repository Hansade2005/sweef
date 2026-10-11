// Package vp9 is a pure-Go VP9 video decoder following the VP9 Bitstream &
// Decoding Process Specification v0.6: profile 0 (8-bit 4:2:0) with all intra
// and inter tools (compound prediction, scaled references, segmentation,
// tiles, loop filter, backward probability adaptation).
package vp9

import "math/bits"

// boolDec is the VP9 boolean entropy decoder (spec section 9.2).
type boolDec struct {
	buf   []byte
	pos   int
	value uint64
	count int
	rng   uint32
}

func (b *boolDec) init(buf []byte) bool {
	b.buf = buf
	b.pos = 0
	b.value = 0
	b.count = -8
	b.rng = 255
	b.fill()
	return b.read(128) == 0 // marker bit
}

func (b *boolDec) fill() {
	shift := 48 - b.count
	for shift >= 0 {
		if b.pos >= len(b.buf) {
			b.count += 0x40000000
			return
		}
		b.value |= uint64(b.buf[b.pos]) << uint(shift)
		b.pos++
		b.count += 8
		shift -= 8
	}
}

func (b *boolDec) read(prob uint8) int {
	split := 1 + (((b.rng - 1) * uint32(prob)) >> 8)
	if b.count < 0 {
		b.fill()
	}
	big := uint64(split) << 56
	rng := split
	bit := 0
	if b.value >= big {
		rng = b.rng - split
		b.value -= big
		bit = 1
	}
	shift := uint(bits.LeadingZeros8(uint8(rng)))
	b.rng = rng << shift
	b.value <<= shift
	b.count -= int(shift)
	return bit
}

func (b *boolDec) literal(n int) int {
	x := 0
	for i := 0; i < n; i++ {
		x = x<<1 | b.read(128)
	}
	return x
}

// tree decodes a value with the given tree and a probability lookup.
func (b *boolDec) tree(t []int8, probs []uint8) int {
	n := 0
	for {
		n = int(t[n+b.read(probs[n>>1])])
		if n <= 0 {
			return -n
		}
	}
}

// bitReader reads the uncompressed header.
type bitReader struct {
	b   []byte
	pos int
}

func (r *bitReader) u(n int) int {
	x := 0
	for i := 0; i < n; i++ {
		var bit int
		if r.pos>>3 < len(r.b) {
			bit = int(r.b[r.pos>>3]>>(7-uint(r.pos&7))) & 1
		}
		r.pos++
		x = x<<1 | bit
	}
	return x
}

func (r *bitReader) flag() bool { return r.u(1) != 0 }

// s reads an n-bit magnitude followed by a sign bit.
func (r *bitReader) s(n int) int {
	v := r.u(n)
	if r.flag() {
		return -v
	}
	return v
}

func (r *bitReader) overrun() bool { return r.pos > len(r.b)*8 }
