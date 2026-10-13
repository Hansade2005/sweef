package font

import (
	"bytes"
	"compress/zlib"
	"io"
)

// woff.go: WOFF 1.0 (https://www.w3.org/TR/WOFF/) to sfnt.
//
// Every length and offset of the file is checked before it is used, each
// table is inflated with a hard cap equal to its declared original length
// (and the sum of the lengths is capped), and the declared length must come
// out exactly. The directory checksums are not trusted or compared: the
// output sfnt gets freshly computed ones (real fonts exist whose stored
// checksums are wrong and browsers render them; OTS does not check them).

const (
	woffSig  = 0x774F4646 // 'wOFF'
	woff2Sig = 0x774F4632 // 'wOF2'
)

// DecodeWOFF converts a WOFF 1.0 file to a plain sfnt.
func DecodeWOFF(data []byte, lim Limits) (sfnt []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			sfnt, err = nil, bad("woff: internal error")
		}
	}()
	lim = lim.norm()
	r := &rd{b: data}
	if r.u32() != woffSig {
		return nil, bad("woff: bad signature")
	}
	flavor := r.u32()
	length := int(r.u32())
	numTables := r.u16()
	r.u16() // reserved
	total := int64(r.u32())
	r.skip(4) // major, minor
	metaOff, metaLen, metaOrig := int64(r.u32()), int64(r.u32()), int64(r.u32())
	privOff, privLen := int64(r.u32()), int64(r.u32())
	if r.err != nil {
		return nil, r.err
	}
	if length != len(data) {
		return nil, bad("woff: header length does not match file size")
	}
	if numTables == 0 || numTables > lim.MaxTables {
		return nil, bad("woff: bad table count")
	}
	if total > int64(lim.MaxSfnt) {
		return nil, bad("woff: font too large")
	}
	_ = metaOrig
	switch flavor {
	case 0x00010000, 0x4F54544F, 0x74727565:
	default:
		return nil, bad("woff: unsupported flavor")
	}
	dirEnd := 44 + 20*numTables
	if dirEnd > len(data) {
		return nil, bad("woff: truncated table directory")
	}
	type ent struct {
		tag             string
		off, comp, orig int
	}
	ents := make([]ent, numTables)
	var sum int64
	prevEnd := dirEnd
	seen := map[string]bool{}
	for i := range ents {
		e := &ents[i]
		e.tag = string(r.bytes(4))
		e.off, e.comp, e.orig = int(r.u32()), int(r.u32()), int(r.u32())
		r.skip(4) // original checksum
		if r.err != nil {
			return nil, r.err
		}
		if seen[e.tag] {
			return nil, bad("woff: duplicate table " + e.tag)
		}
		seen[e.tag] = true
		if i > 0 && e.tag <= ents[i-1].tag {
			return nil, bad("woff: table directory not sorted")
		}
		if e.off < 0 || e.comp < 0 || e.orig < 0 || e.off&3 != 0 {
			return nil, bad("woff: bad table offset")
		}
		if e.off < prevEnd && e.comp > 0 {
			return nil, bad("woff: overlapping tables")
		}
		if int64(e.off)+int64(e.comp) > int64(length) {
			return nil, bad("woff: table outside file")
		}
		if e.comp > e.orig {
			return nil, bad("woff: compressed length larger than original")
		}
		if e.comp > 0 {
			prevEnd = e.off + e.comp
		}
		sum += int64(e.orig)
		if sum > int64(lim.MaxSfnt) {
			return nil, bad("woff: font too large")
		}
	}
	// the optional metadata and private blocks must lie after the tables, inside the file
	for _, blk := range [][2]int64{{metaOff, metaLen}, {privOff, privLen}} {
		if blk[1] == 0 {
			continue
		}
		if blk[0] < int64(prevEnd) || blk[0]+blk[1] > int64(length) {
			return nil, bad("woff: bad metadata block")
		}
	}
	tables := make([]sfntTable, 0, numTables)
	for _, e := range ents {
		raw := data[e.off : e.off+e.comp]
		var tab []byte
		if e.comp == e.orig {
			tab = append([]byte(nil), raw...)
		} else {
			zr, zerr := zlib.NewReader(bytes.NewReader(raw))
			if zerr != nil {
				return nil, bad("woff: bad zlib stream in " + e.tag)
			}
			tab = make([]byte, e.orig)
			if _, rerr := io.ReadFull(zr, tab); rerr != nil {
				return nil, bad("woff: table " + e.tag + " inflates to less than its declared length")
			}
			var extra [1]byte
			if n, _ := zr.Read(extra[:]); n > 0 {
				return nil, bad("woff: table " + e.tag + " inflates to more than its declared length")
			}
		}
		tables = append(tables, sfntTable{tag: e.tag, data: tab})
	}
	return buildSFNT(flavor, tables, lim.MaxSfnt)
}
