package font

import "encoding/binary"

// extractSFNT returns a standalone single-font sfnt for font idx of data. A
// TrueType Collection (and some single fonts) have table offsets that
// golang.org/x/image/font/sfnt rejects (it insists on 4-byte alignment), so
// the tables are copied into a fresh file with aligned offsets. idx < 0 means
// a single font file. ok is false when data is not parseable that way.
func extractSFNT(data []byte, idx int) (out []byte, ok bool) {
	base := 0
	if len(data) >= 16 && string(data[:4]) == "ttcf" {
		if idx < 0 {
			idx = 0
		}
		n := int(binary.BigEndian.Uint32(data[8:]))
		if idx >= n || 12+4*idx+4 > len(data) {
			return nil, false
		}
		base = int(binary.BigEndian.Uint32(data[12+4*idx:]))
	}
	if base < 0 || base+12 > len(data) {
		return nil, false
	}
	nt := int(binary.BigEndian.Uint16(data[base+4:]))
	if base+12+16*nt > len(data) {
		return nil, false
	}
	hdr := 12 + 16*nt
	size := hdr
	for i := 0; i < nt; i++ {
		e := data[base+12+16*i:]
		off := int(binary.BigEndian.Uint32(e[8:]))
		ln := int(binary.BigEndian.Uint32(e[12:]))
		if off < 0 || ln < 0 || off+ln > len(data) {
			return nil, false
		}
		size += (ln + 3) &^ 3
	}
	out = make([]byte, size)
	copy(out, data[base:base+hdr])
	pos := hdr
	for i := 0; i < nt; i++ {
		e := data[base+12+16*i:]
		off := int(binary.BigEndian.Uint32(e[8:]))
		ln := int(binary.BigEndian.Uint32(e[12:]))
		copy(out[pos:], data[off:off+ln])
		binary.BigEndian.PutUint32(out[12+16*i+8:], uint32(pos))
		pos += (ln + 3) &^ 3
	}
	return out, true
}
