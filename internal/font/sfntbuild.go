package font

import (
	"encoding/binary"
	"errors"
	"sort"
)

// sfntbuild.go: helpers shared by the web font decoders (WOFF, WOFF2) and the
// sanitiser: a bounds-checked big-endian reader that never panics, table
// checksums and the writer that lays decoded tables out as a plain sfnt.

// ErrBadFont is wrapped by every error the decoders and the sanitiser return
// for input that is not a usable font. Callers fall back to another face.
var ErrBadFont = errors.New("font: invalid or unsupported font data")

type fontError string

func (e fontError) Error() string { return "font: " + string(e) }
func (e fontError) Unwrap() error { return ErrBadFont }

func bad(msg string) error { return fontError(msg) }

// Limits applied while decoding untrusted font files.
type Limits struct {
	// MaxSfnt caps the size of the decoded sfnt (and so of any decompressed
	// table stream: a zip bomb is stopped here).
	MaxSfnt int
	// MaxTables caps the number of tables a file may declare.
	MaxTables int
}

// DefaultLimits are what the browser uses: real web fonts are far smaller
// (a full CJK font is about 16 MB; Google Fonts subsets are 10-100 KB).
var DefaultLimits = Limits{MaxSfnt: 32 << 20, MaxTables: 128}

func (l Limits) norm() Limits {
	if l.MaxSfnt <= 0 {
		l.MaxSfnt = DefaultLimits.MaxSfnt
	}
	if l.MaxTables <= 0 {
		l.MaxTables = DefaultLimits.MaxTables
	}
	return l
}

// rd is a bounds-checked big-endian reader. After the first out-of-range
// read every call returns zero and err stays set, so decoders can read a
// whole structure and check err once.
type rd struct {
	b   []byte
	off int
	err error
}

func (r *rd) fail() {
	if r.err == nil {
		r.err = bad("truncated data")
	}
}

func (r *rd) left() int { return len(r.b) - r.off }

func (r *rd) skip(n int) {
	if n < 0 || n > r.left() {
		r.fail()
		r.off = len(r.b)
		return
	}
	r.off += n
}

func (r *rd) u8() int {
	if r.err != nil || r.off >= len(r.b) {
		r.fail()
		return 0
	}
	v := r.b[r.off]
	r.off++
	return int(v)
}

func (r *rd) u16() int {
	if r.err != nil || r.left() < 2 {
		r.fail()
		return 0
	}
	v := binary.BigEndian.Uint16(r.b[r.off:])
	r.off += 2
	return int(v)
}

func (r *rd) i16() int { return int(int16(r.u16())) }

func (r *rd) u32() uint32 {
	if r.err != nil || r.left() < 4 {
		r.fail()
		return 0
	}
	v := binary.BigEndian.Uint32(r.b[r.off:])
	r.off += 4
	return v
}

func (r *rd) bytes(n int) []byte {
	if r.err != nil || n < 0 || n > r.left() {
		r.fail()
		return nil
	}
	v := r.b[r.off : r.off+n]
	r.off += n
	return v
}

// tableChecksum is the sfnt table checksum: the sum of the big-endian uint32
// words of the table, zero padded to a multiple of four bytes.
func tableChecksum(b []byte) uint32 {
	var sum uint32
	n := len(b) &^ 3
	for i := 0; i < n; i += 4 {
		sum += binary.BigEndian.Uint32(b[i:])
	}
	if n < len(b) {
		var w [4]byte
		copy(w[:], b[n:])
		sum += binary.BigEndian.Uint32(w[:])
	}
	return sum
}

type sfntTable struct {
	tag  string
	data []byte
}

// buildSFNT lays tables out as a standalone sfnt: directory sorted by tag,
// tables 4-byte aligned (x/image/font/sfnt requires alignment), checksums
// recomputed and head.checkSumAdjustment fixed up. It returns an error when
// the result would exceed limit bytes.
func buildSFNT(flavor uint32, tables []sfntTable, limit int) ([]byte, error) {
	sort.Slice(tables, func(i, j int) bool { return tables[i].tag < tables[j].tag })
	n := len(tables)
	size := 12 + 16*n
	for _, t := range tables {
		if len(t.data) > limit {
			return nil, bad("decoded font too large")
		}
		size += (len(t.data) + 3) &^ 3
		if size > limit || size < 0 {
			return nil, bad("decoded font too large")
		}
	}
	out := make([]byte, size)
	binary.BigEndian.PutUint32(out[0:], flavor)
	binary.BigEndian.PutUint16(out[4:], uint16(n))
	sr, es := 1, 0
	for sr*2 <= n {
		sr *= 2
		es++
	}
	binary.BigEndian.PutUint16(out[6:], uint16(sr*16))
	binary.BigEndian.PutUint16(out[8:], uint16(es))
	binary.BigEndian.PutUint16(out[10:], uint16(n*16-sr*16))
	pos := 12 + 16*n
	headOff := -1
	for i, t := range tables {
		e := out[12+16*i:]
		copy(e, t.tag)
		copy(out[pos:], t.data)
		cs := tableChecksum(t.data)
		if t.tag == "head" && len(t.data) >= 12 {
			headOff = pos
			// the checksum of head is computed with checkSumAdjustment zero
			cs = tableChecksum(append(append([]byte(nil), t.data[:8]...), append([]byte{0, 0, 0, 0}, t.data[12:]...)...))
		}
		binary.BigEndian.PutUint32(e[4:], cs)
		binary.BigEndian.PutUint32(e[8:], uint32(pos))
		binary.BigEndian.PutUint32(e[12:], uint32(len(t.data)))
		pos += (len(t.data) + 3) &^ 3
	}
	if headOff >= 0 {
		binary.BigEndian.PutUint32(out[headOff+8:], 0)
		binary.BigEndian.PutUint32(out[headOff+8:], 0xB1B0AFBA-tableChecksum(out))
	}
	return out, nil
}
