package hevc

import "crypto/md5"

// Decoded picture hash SEI (D.3.19): lets a caller (and the conformance
// tests) check every reconstructed picture against the checksum the encoder
// computed, without needing a reference decoder.

const (
	hashMD5      = 0
	hashCRC      = 1
	hashChecksum = 2
)

type picHash struct {
	typ int
	md5 [3][16]byte
	val [3]uint32
}

// HashStats reports how many pictures carried a decoded picture hash SEI
// (checked) and how many of those did not match the reconstruction (bad).
// Checking is enabled with CheckHash.
func (d *Decoder) HashStats() (checked, bad int) { return d.hashChecked, d.hashBad }

// parseSEI looks for a decoded picture hash in a suffix SEI NAL unit (header
// removed, escaped payload).
func parseSEIHash(rbsp []byte) *picHash {
	p := 0
	for p+2 < len(rbsp) {
		typ := 0
		for p < len(rbsp) && rbsp[p] == 0xff {
			typ += 255
			p++
		}
		if p >= len(rbsp) {
			return nil
		}
		typ += int(rbsp[p])
		p++
		size := 0
		for p < len(rbsp) && rbsp[p] == 0xff {
			size += 255
			p++
		}
		if p >= len(rbsp) {
			return nil
		}
		size += int(rbsp[p])
		p++
		if p+size > len(rbsp) {
			return nil
		}
		pl := rbsp[p : p+size]
		p += size
		if typ != 132 || len(pl) < 1 {
			continue
		}
		h := &picHash{typ: int(pl[0])}
		q := 1
		for c := 0; c < 3; c++ {
			switch h.typ {
			case hashMD5:
				if q+16 > len(pl) {
					return h
				}
				copy(h.md5[c][:], pl[q:q+16])
				q += 16
			case hashCRC:
				if q+2 > len(pl) {
					return h
				}
				h.val[c] = uint32(pl[q])<<8 | uint32(pl[q+1])
				q += 2
			case hashChecksum:
				if q+4 > len(pl) {
					return h
				}
				h.val[c] = uint32(pl[q])<<24 | uint32(pl[q+1])<<16 | uint32(pl[q+2])<<8 | uint32(pl[q+3])
				q += 4
			}
		}
		return h
	}
	return nil
}

// verify compares the hash with the picture; it reports whether all
// components match.
func (h *picHash) verify(p *Picture) bool {
	s := p.sps
	nc := 3
	if s.chromaFormat == 0 {
		nc = 1
	}
	for c := 0; c < nc; c++ {
		w, hh, bd := p.w, p.h, s.bitDepthY
		if c > 0 {
			w, hh, bd = p.cw, p.ch, s.bitDepthC
		}
		pl, stride := p.planes[c], p.stride[c]
		switch h.typ {
		case hashMD5:
			m := md5.New()
			buf := make([]byte, 0, w*2)
			for y := 0; y < hh; y++ {
				buf = buf[:0]
				for _, v := range pl[y*stride : y*stride+w] {
					buf = append(buf, byte(v))
					if bd > 8 {
						buf = append(buf, byte(v>>8))
					}
				}
				m.Write(buf)
			}
			var sum [16]byte
			copy(sum[:], m.Sum(nil))
			if sum != h.md5[c] {
				return false
			}
		case hashCRC:
			crc := uint32(0xffff)
			feed := func(b byte) {
				for i := 7; i >= 0; i-- {
					msb := (crc >> 15) & 1
					crc = (((crc << 1) + uint32(b>>uint(i))&1) & 0xffff) ^ (msb * 0x1021)
				}
			}
			for y := 0; y < hh; y++ {
				for _, v := range pl[y*stride : y*stride+w] {
					feed(byte(v))
					if bd > 8 {
						feed(byte(v >> 8))
					}
				}
			}
			feed(0)
			feed(0)
			if crc != h.val[c] {
				return false
			}
		case hashChecksum:
			var sum uint32
			for y := 0; y < hh; y++ {
				for x, v := range pl[y*stride : y*stride+w] {
					mask := uint32((x & 0xff) ^ (y & 0xff) ^ (x >> 8) ^ (y >> 8))
					sum += (uint32(v) & 0xff) ^ mask
					if bd > 8 {
						sum += (uint32(v) >> 8) ^ mask
					}
				}
			}
			if sum != h.val[c] {
				return false
			}
		}
	}
	return true
}
