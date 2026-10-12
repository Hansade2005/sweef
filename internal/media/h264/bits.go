// Package h264 is a pure-Go H.264/AVC decoder for 8-bit 4:2:0 progressive
// video: Baseline, Main and High profiles with CAVLC or CABAC, I/P/B slices,
// multiple reference frames, weighted prediction, 8x8 transform and the
// in-loop deblocking filter. Interlaced coding (field pictures, MBAFF),
// slice groups (FMO) and extended-profile data partitioning are supported;
// SP/SI slices report ErrUnsupported.
package h264

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/bits"
	"runtime"
)

// ErrUnsupported is returned for streams outside the supported subset.
var ErrUnsupported = errors.New("h264: unsupported stream feature")

var errBitstream = errors.New("h264: invalid bitstream")

// bitReader reads an RBSP (emulation prevention bytes already removed).
// The backing slice must have at least 8 bytes of slack after n bytes.
type bitReader struct {
	b   []byte
	pos int // bit position
	end int // bit length of the payload
	lst int // position of the rbsp_stop_one_bit
}

func newBitReader(rbsp []byte) *bitReader {
	b := make([]byte, len(rbsp)+16)
	copy(b, rbsp)
	r := &bitReader{b: b, end: len(rbsp) * 8}
	last := r.end - 1
	for last >= 0 && (b[last>>3]>>(7-uint(last&7)))&1 == 0 {
		last--
	}
	r.lst = last
	return r
}

func (r *bitReader) peek(n uint) uint32 {
	i := r.pos >> 3
	if i+8 > len(r.b) {
		return 0
	}
	v := binary.BigEndian.Uint64(r.b[i:])
	return uint32((v << uint(r.pos&7)) >> (64 - n))
}

func (r *bitReader) u(n uint) uint32 {
	if n == 0 {
		return 0
	}
	v := r.peek(n)
	r.pos += int(n)
	return v
}

func (r *bitReader) u1() uint32 {
	v := uint32(r.b[r.pos>>3]>>(7-uint(r.pos&7))) & 1
	r.pos++
	return v
}

func (r *bitReader) flag() bool { return r.u1() != 0 }

func (r *bitReader) ue() uint32 {
	w := r.peek(32)
	if w == 0 {
		r.pos += 32
		return 0xffffffff
	}
	lz := uint(bits.LeadingZeros32(w))
	if lz <= 15 {
		n := 2*lz + 1
		r.pos += int(n)
		return (w >> (32 - n)) - 1
	}
	r.pos += int(lz) + 1
	return (1<<lz - 1) + r.u(lz)
}

func (r *bitReader) se() int32 {
	k := r.ue()
	if k&1 != 0 {
		return int32((k + 1) >> 1)
	}
	return -int32(k >> 1)
}

// te reads a truncated Exp-Golomb value with the given maximum.
func (r *bitReader) te(max uint32) uint32 {
	if max > 1 {
		return r.ue()
	}
	return 1 - r.u1()
}

func (r *bitReader) skip(n int) { r.pos += n }

func (r *bitReader) overrun() bool { return r.pos > r.end }

func (r *bitReader) byteAlign() { r.pos = (r.pos + 7) &^ 7 }

func (r *bitReader) aligned() bool { return r.pos&7 == 0 }

// moreRBSPData reports whether data remains before the rbsp trailing bits.
func (r *bitReader) moreRBSPData() bool {
	return r.pos < r.lst
}

// unescape removes emulation prevention bytes (00 00 03 -> 00 00).
func unescape(nal []byte) []byte {
	out := make([]byte, 0, len(nal))
	zeros := 0
	for _, c := range nal {
		if zeros >= 2 && c == 3 {
			zeros = 0
			continue
		}
		out = append(out, c)
		if c == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	return out
}

// splitAnnexB splits a byte stream on 00 00 01 start codes.
func splitAnnexB(data []byte) [][]byte {
	var out [][]byte
	n := len(data)
	i := 0
	start := -1
	for i+2 < n {
		if data[i] == 0 && data[i+1] == 0 && data[i+2] == 1 {
			if start >= 0 {
				e := i
				for e > start && data[e-1] == 0 {
					e--
				}
				out = append(out, data[start:e])
			}
			start = i + 3
			i += 3
			continue
		}
		i++
	}
	if start >= 0 && start < n {
		e := n
		for e > start && data[e-1] == 0 {
			e--
		}
		out = append(out, data[start:e])
	}
	return out
}

// splitLengthPrefixed splits AVCC-style NAL units.
func splitLengthPrefixed(data []byte, lenSize int) [][]byte {
	var out [][]byte
	for len(data) >= lenSize {
		n := 0
		for i := 0; i < lenSize; i++ {
			n = n<<8 | int(data[i])
		}
		data = data[lenSize:]
		if n > len(data) {
			n = len(data)
		}
		if n > 0 {
			out = append(out, data[:n])
		}
		data = data[n:]
	}
	return out
}

// DebugCorrupt, when set, receives the source location of corrupt-data errors.
var DebugCorrupt func(loc string)

func corrupt() error {
	if DebugCorrupt != nil {
		_, file, line, _ := runtime.Caller(1)
		DebugCorrupt(fmt.Sprintf("%s:%d", file, line))
	}
	return errCorrupt
}
