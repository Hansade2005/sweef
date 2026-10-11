// Copyright 2011 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file (LICENSE.x-image).

package vp8

import "math/bits"

// uniformProb represents a 50% probability that the next bit is 0.
const uniformProb = 128

// partition is the boolean entropy decoder of RFC 6386 section 7, written in
// the multi-byte-window style of libvpx. Reading past the end of the buffer
// yields zero bits (as libvpx does) instead of failing.
type partition struct {
	buf   []byte
	pos   int
	value uint64
	count int
	rng   uint32
}

func (p *partition) init(buf []byte) {
	p.buf = buf
	p.pos = 0
	p.value = 0
	p.count = -8
	p.rng = 255
	p.fill()
}

func (p *partition) fill() {
	shift := 48 - p.count
	for shift >= 0 {
		if p.pos >= len(p.buf) {
			p.count += 0x40000000
			return
		}
		p.value |= uint64(p.buf[p.pos]) << uint(shift)
		p.pos++
		p.count += 8
		shift -= 8
	}
}

// readBit returns the next bit, which is 0 with probability prob/256.
func (p *partition) readBit(prob uint8) bool {
	split := 1 + (((p.rng - 1) * uint32(prob)) >> 8)
	if p.count < 0 {
		p.fill()
	}
	big := uint64(split) << 56
	rng := split
	bit := false
	if p.value >= big {
		rng = p.rng - split
		p.value -= big
		bit = true
	}
	shift := uint(bits.LeadingZeros8(uint8(rng)))
	p.rng = rng << shift
	p.value <<= shift
	p.count -= int(shift)
	return bit
}

// readUint returns the next n-bit unsigned integer, most significant first.
func (p *partition) readUint(prob, n uint8) uint32 {
	var u uint32
	for n > 0 {
		n--
		if p.readBit(prob) {
			u |= 1 << n
		}
	}
	return u
}

// readInt returns the next n-bit magnitude followed by a sign bit.
func (p *partition) readInt(prob, n uint8) int32 {
	u := p.readUint(prob, n)
	if p.readBit(prob) {
		return -int32(u)
	}
	return int32(u)
}

// readOptionalInt reads a flag and, when set, a signed n-bit integer.
func (p *partition) readOptionalInt(prob, n uint8) int32 {
	if !p.readBit(prob) {
		return 0
	}
	return p.readInt(prob, n)
}

// readFlag reads a uniform-probability bit.
func (p *partition) readFlag() bool { return p.readBit(uniformProb) }
