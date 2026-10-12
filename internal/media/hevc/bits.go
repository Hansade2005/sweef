// Package hevc is a pure-Go HEVC / H.265 video decoder (ITU-T H.265):
// Main, Main 10, Main Still Picture and the range extensions (4:0:0, 4:2:2,
// 4:4:4, up to 16 bit samples) without extended precision processing, SCC or
// multilayer tools.
package hevc

import "errors"

var (
	errBitstream = errors.New("hevc: invalid bitstream")
	// ErrUnsupported reports a stream that uses tools this decoder lacks.
	ErrUnsupported = errors.New("hevc: unsupported feature")
)

// bitReader reads an RBSP (emulation prevention bytes already removed).
type bitReader struct {
	b   []byte
	pos int // bit position
	err bool
}

func (r *bitReader) bitsLeft() int { return len(r.b)*8 - r.pos }

func (r *bitReader) u1() uint32 {
	i := r.pos >> 3
	if i >= len(r.b) {
		r.err = true
		r.pos++
		return 0
	}
	v := uint32(r.b[i]>>(7-uint(r.pos&7))) & 1
	r.pos++
	return v
}

func (r *bitReader) flag() bool { return r.u1() != 0 }

func (r *bitReader) u(n int) uint32 {
	var v uint32
	for n > 0 {
		i := r.pos >> 3
		if i >= len(r.b) {
			r.err = true
			r.pos += n
			return v << uint(n)
		}
		avail := 8 - r.pos&7
		take := avail
		if take > n {
			take = n
		}
		bits := (uint32(r.b[i]) >> uint(avail-take)) & (1<<uint(take) - 1)
		v = v<<uint(take) | bits
		r.pos += take
		n -= take
	}
	return v
}

func (r *bitReader) skip(n int) { r.pos += n }

func (r *bitReader) ue() uint32 {
	z := 0
	for r.u1() == 0 {
		z++
		if z > 32 || r.err {
			r.err = true
			return 0
		}
	}
	if z == 0 {
		return 0
	}
	if z == 32 {
		return 0xffffffff
	}
	return (1<<uint(z) - 1) + r.u(z)
}

func (r *bitReader) se() int32 {
	k := r.ue()
	if k&1 != 0 {
		return int32((k + 1) >> 1)
	}
	return -int32(k >> 1)
}

func (r *bitReader) alignByte() { r.pos = (r.pos + 7) &^ 7 }

// unescape removes emulation prevention bytes.
func unescape(b []byte) []byte {
	out := make([]byte, 0, len(b))
	z := 0
	for _, c := range b {
		if z >= 2 && c == 3 {
			z = 0
			continue
		}
		out = append(out, c)
		if c == 0 {
			z++
		} else {
			z = 0
		}
	}
	return out
}

// splitAnnexB splits a byte stream into NAL units (without start codes).
func splitAnnexB(d []byte) [][]byte {
	var out [][]byte
	n := len(d)
	i := 0
	start := -1
	for i+2 < n {
		if d[i+2] > 1 {
			i += 3
			continue
		}
		if d[i] == 0 && d[i+1] == 0 && d[i+2] == 1 {
			if start >= 0 {
				e := i
				for e > start && d[e-1] == 0 {
					e--
				}
				out = append(out, d[start:e])
			}
			start = i + 3
			i += 3
			continue
		}
		i++
	}
	if start >= 0 {
		e := n
		for e > start && d[e-1] == 0 {
			e--
		}
		out = append(out, d[start:e])
	}
	return out
}

func splitLengthPrefixed(d []byte, ls int) [][]byte {
	var out [][]byte
	for len(d) >= ls {
		n := 0
		for i := 0; i < ls; i++ {
			n = n<<8 | int(d[i])
		}
		d = d[ls:]
		if n > len(d) {
			n = len(d)
		}
		if n > 0 {
			out = append(out, d[:n])
		}
		d = d[n:]
	}
	return out
}
