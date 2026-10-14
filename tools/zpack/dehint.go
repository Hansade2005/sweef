package main

import (
	"encoding/binary"
	"fmt"
	"sort"
)

// dehint removes the TrueType hinting program from a TrueType font: the per-glyph instructions in 'glyf' (simple
// glyphs: the instruction bytes; composite glyphs: the instruction block and its WE_HAVE_INSTRUCTIONS flag) and the
// 'fpgm', 'prep' and 'cvt ' tables that only instructions use. Outlines, advances, the character map, names and
// metrics are untouched. Sweef never runs hinting (internal/font asks sfnt for HintingNone everywhere, and the
// rasteriser reads outlines only), and in the eight Go fonts the instructions are more than half of the glyph data.
// The result is a valid TrueType font with correct checksums; internal/font's TestGoFontArchive compares every
// glyph, advance, mapping and name with the original.
func dehint(font []byte) ([]byte, error) {
	if len(font) < 12 {
		return nil, fmt.Errorf("short font")
	}
	switch binary.BigEndian.Uint32(font) {
	case 0x00010000, 0x74727565: // sfnt version 1.0 or 'true'
	default:
		return nil, fmt.Errorf("not a TrueType font")
	}
	n := int(binary.BigEndian.Uint16(font[4:]))
	type tbl struct {
		tag  string
		data []byte
	}
	var tabs []tbl
	for i := 0; i < n; i++ {
		e := font[12+16*i:]
		off, ln := int(binary.BigEndian.Uint32(e[8:])), int(binary.BigEndian.Uint32(e[12:]))
		if off+ln > len(font) {
			return nil, fmt.Errorf("table %q outside the file", e[:4])
		}
		tabs = append(tabs, tbl{string(e[:4]), append([]byte(nil), font[off:off+ln]...)})
	}
	get := func(tag string) *tbl {
		for i := range tabs {
			if tabs[i].tag == tag {
				return &tabs[i]
			}
		}
		return nil
	}
	head, maxp, loca, glyf := get("head"), get("maxp"), get("loca"), get("glyf")
	if head == nil || maxp == nil || loca == nil || glyf == nil {
		return nil, fmt.Errorf("missing head, maxp, loca or glyf")
	}
	long := binary.BigEndian.Uint16(head.data[50:]) == 1
	ng := int(binary.BigEndian.Uint16(maxp.data[4:]))
	off := func(i int) int {
		if long {
			return int(binary.BigEndian.Uint32(loca.data[4*i:]))
		}
		return 2 * int(binary.BigEndian.Uint16(loca.data[2*i:]))
	}
	var newGlyf []byte
	newLoca := make([]int, ng+1)
	for g := 0; g < ng; g++ {
		newLoca[g] = len(newGlyf)
		a, b := off(g), off(g+1)
		if a > b || b > len(glyf.data) {
			return nil, fmt.Errorf("glyph %d: bad loca", g)
		}
		gd := glyf.data[a:b]
		if len(gd) == 0 {
			continue
		}
		out, err := stripGlyph(gd)
		if err != nil {
			return nil, fmt.Errorf("glyph %d: %w", g, err)
		}
		newGlyf = append(newGlyf, out...)
		for len(newGlyf)%4 != 0 {
			newGlyf = append(newGlyf, 0)
		}
	}
	newLoca[ng] = len(newGlyf)
	var lo []byte
	if long {
		lo = make([]byte, 4*(ng+1))
		for i, o := range newLoca {
			binary.BigEndian.PutUint32(lo[4*i:], uint32(o))
		}
	} else {
		if len(newGlyf) >= 1<<17 {
			return nil, fmt.Errorf("glyf too big for short loca")
		}
		lo = make([]byte, 2*(ng+1))
		for i, o := range newLoca {
			binary.BigEndian.PutUint16(lo[2*i:], uint16(o/2))
		}
	}
	glyf.data, loca.data = newGlyf, lo

	var keep []tbl
	for _, t := range tabs {
		switch t.tag {
		case "fpgm", "prep", "cvt ":
		default:
			keep = append(keep, t)
		}
	}
	sort.Slice(keep, func(i, j int) bool { return keep[i].tag < keep[j].tag })
	for i := range keep {
		if keep[i].tag == "head" {
			binary.BigEndian.PutUint32(keep[i].data[8:], 0) // checkSumAdjustment, filled in below
		}
	}

	// Assemble: header, directory, tables on 4-byte boundaries.
	nt := len(keep)
	hdr := make([]byte, 12+16*nt)
	copy(hdr, font[:4])
	binary.BigEndian.PutUint16(hdr[4:], uint16(nt))
	sr := 1
	es := 0
	for sr*2 <= nt {
		sr *= 2
		es++
	}
	binary.BigEndian.PutUint16(hdr[6:], uint16(sr*16))
	binary.BigEndian.PutUint16(hdr[8:], uint16(es))
	binary.BigEndian.PutUint16(hdr[10:], uint16(nt*16-sr*16))
	body := []byte{}
	headAt := -1
	for i, t := range keep {
		e := hdr[12+16*i:]
		copy(e, t.tag)
		binary.BigEndian.PutUint32(e[4:], checksum(t.data))
		binary.BigEndian.PutUint32(e[8:], uint32(len(hdr)+len(body)))
		binary.BigEndian.PutUint32(e[12:], uint32(len(t.data)))
		if t.tag == "head" {
			headAt = len(hdr) + len(body)
		}
		body = append(body, t.data...)
		for len(body)%4 != 0 {
			body = append(body, 0)
		}
	}
	out := append(hdr, body...)
	if headAt >= 0 {
		binary.BigEndian.PutUint32(out[headAt+8:], 0xB1B0AFBA-checksum(out))
	}
	return out, nil
}

func checksum(b []byte) uint32 {
	var s uint32
	for i := 0; i+4 <= len(b); i += 4 {
		s += binary.BigEndian.Uint32(b[i:])
	}
	if r := len(b) % 4; r != 0 {
		var t [4]byte
		copy(t[:], b[len(b)-r:])
		s += binary.BigEndian.Uint32(t[:])
	}
	return s
}

// stripGlyph returns a glyph description without instructions.
func stripGlyph(gd []byte) ([]byte, error) {
	if len(gd) < 10 {
		return nil, fmt.Errorf("short glyph")
	}
	nc := int16(binary.BigEndian.Uint16(gd))
	if nc >= 0 { // simple: header, endPts[nc], instructionLength, instructions, flags, coordinates
		p := 10 + 2*int(nc)
		if p+2 > len(gd) {
			return nil, fmt.Errorf("short simple glyph")
		}
		il := int(binary.BigEndian.Uint16(gd[p:]))
		if p+2+il > len(gd) {
			return nil, fmt.Errorf("instructions outside the glyph")
		}
		out := append([]byte(nil), gd[:p]...)
		out = append(out, 0, 0)
		return append(out, gd[p+2+il:]...), nil
	}
	// composite: components, then (if the last has WE_HAVE_INSTRUCTIONS) numInstr and the instructions
	out := append([]byte(nil), gd[:10]...)
	p := 10
	for {
		if p+4 > len(gd) {
			return nil, fmt.Errorf("short composite glyph")
		}
		flags := binary.BigEndian.Uint16(gd[p:])
		size := 4
		if flags&0x0001 != 0 { // ARG_1_AND_2_ARE_WORDS
			size += 4
		} else {
			size += 2
		}
		switch {
		case flags&0x0008 != 0: // WE_HAVE_A_SCALE
			size += 2
		case flags&0x0040 != 0: // WE_HAVE_AN_X_AND_Y_SCALE
			size += 4
		case flags&0x0080 != 0: // WE_HAVE_A_TWO_BY_TWO
			size += 8
		}
		if p+size > len(gd) {
			return nil, fmt.Errorf("short composite component")
		}
		comp := append([]byte(nil), gd[p:p+size]...)
		binary.BigEndian.PutUint16(comp, flags&^0x0100) // WE_HAVE_INSTRUCTIONS
		out = append(out, comp...)
		p += size
		if flags&0x0020 == 0 { // MORE_COMPONENTS
			return out, nil
		}
	}
}
