package font

import (
	"encoding/binary"
	"strings"
)

// sanitize.go: structural validation of a decoded web font, in the spirit of
// OTS (the sanitiser browsers run on every web font): a font whose tables
// contradict each other is refused instead of being rendered as garbage.
// Checked: the table directory, the required tables, head/hhea/maxp sizes and
// the relations between numGlyphs, hmtx and loca, loca monotonicity, glyph
// records inside glyf and (for simple glyphs) the flag and coordinate arrays.
// The cmap and the glyph outlines of CFF fonts are validated by parsing the
// font with golang.org/x/image/font/sfnt afterwards.

// FontKind says what container a font file was.
type FontKind string

const (
	KindTTF   FontKind = "truetype"
	KindOTF   FontKind = "opentype"
	KindWOFF  FontKind = "woff"
	KindWOFF2 FontKind = "woff2"
)

// DetectKind recognises the container from the first bytes.
func DetectKind(data []byte) (FontKind, bool) {
	if len(data) < 4 {
		return "", false
	}
	switch string(data[:4]) {
	case "wOFF":
		return KindWOFF, true
	case "wOF2":
		return KindWOFF2, true
	case "OTTO":
		return KindOTF, true
	case "true", "\x00\x01\x00\x00":
		return KindTTF, true
	}
	return "", false
}

// Decode turns a font file (WOFF2, WOFF, TrueType or OpenType; collections are
// not accepted) into a validated plain sfnt. It never panics.
func Decode(data []byte, lim Limits) (sfnt []byte, kind FontKind, err error) {
	defer func() {
		if r := recover(); r != nil {
			sfnt, err = nil, bad("internal error while reading the font")
		}
	}()
	lim = lim.norm()
	kind, ok := DetectKind(data)
	if !ok {
		return nil, "", bad("not a font file")
	}
	switch kind {
	case KindWOFF:
		sfnt, err = DecodeWOFF(data, lim)
	case KindWOFF2:
		sfnt, err = DecodeWOFF2(data, lim)
	default:
		if len(data) > lim.MaxSfnt {
			return nil, kind, bad("font too large")
		}
		sfnt = data
	}
	if err != nil {
		return nil, kind, err
	}
	// a plain sfnt may have unaligned tables or lie about its tables: rebuild it
	// from a checked directory
	sfnt, err = normalizeSFNT(sfnt, lim)
	if err != nil {
		return nil, kind, err
	}
	if err := validateSFNT(sfnt); err != nil {
		return nil, kind, err
	}
	return sfnt, kind, nil
}

// normalizeSFNT validates the table directory of an sfnt and returns an equivalent file
// with aligned, non-overlapping tables.
func normalizeSFNT(data []byte, lim Limits) ([]byte, error) {
	r := &rd{b: data}
	flavor := r.u32()
	n := r.u16()
	r.skip(6)
	if r.err != nil {
		return nil, r.err
	}
	if flavor != 0x00010000 && flavor != 0x4F54544F && flavor != 0x74727565 {
		return nil, bad("unsupported sfnt version")
	}
	if n == 0 || n > lim.MaxTables {
		return nil, bad("bad table count")
	}
	seen := map[string]bool{}
	var tabs []sfntTable
	var total int
	for i := 0; i < n; i++ {
		tag := string(r.bytes(4))
		r.skip(4)
		off, ln := int(r.u32()), int(r.u32())
		if r.err != nil {
			return nil, r.err
		}
		if seen[tag] {
			return nil, bad("duplicate table " + tag)
		}
		seen[tag] = true
		if off < 12+16*n || off > len(data) || ln < 0 || ln > len(data)-off {
			return nil, bad("table " + tag + " outside the file")
		}
		total += ln
		if total > lim.MaxSfnt {
			return nil, bad("font too large")
		}
		tabs = append(tabs, sfntTable{tag, data[off : off+ln]})
	}
	// tables must not overlap each other (OTS rejects overlap)
	type span struct{ a, b int }
	var spans []span
	for i := 0; i < n; i++ {
		off := int(binary.BigEndian.Uint32(data[12+16*i+8:]))
		ln := int(binary.BigEndian.Uint32(data[12+16*i+12:]))
		if ln > 0 {
			spans = append(spans, span{off, off + ln})
		}
	}
	for i := range spans {
		for j := i + 1; j < len(spans); j++ {
			if spans[i].a < spans[j].b && spans[j].a < spans[i].b {
				return nil, bad("overlapping tables")
			}
		}
	}
	return buildSFNT(flavor, tabs, lim.MaxSfnt)
}

func tableOf(data []byte, tag string) []byte {
	n := int(binary.BigEndian.Uint16(data[4:]))
	for i := 0; i < n; i++ {
		e := data[12+16*i:]
		if string(e[:4]) == tag {
			off, ln := int(binary.BigEndian.Uint32(e[8:])), int(binary.BigEndian.Uint32(e[12:]))
			return data[off : off+ln]
		}
	}
	return nil
}

func validateSFNT(data []byte) error {
	tb := func(tag string) []byte { return tableOf(data, tag) }
	head, hhea, maxp, hmtx, cmap := tb("head"), tb("hhea"), tb("maxp"), tb("hmtx"), tb("cmap")
	if head == nil || hhea == nil || maxp == nil || hmtx == nil || cmap == nil {
		return bad("missing required table")
	}
	glyf, loca, cff := tb("glyf"), tb("loca"), tb("CFF ")
	if cff == nil && (glyf == nil || loca == nil) {
		return bad("no glyph outlines")
	}
	if cff != nil && glyf != nil {
		return bad("both glyf and CFF tables")
	}
	if len(head) < 54 || binary.BigEndian.Uint32(head[12:]) != 0x5F0F3CF5 {
		return bad("bad head table")
	}
	upm := int(binary.BigEndian.Uint16(head[18:]))
	if upm < 16 || upm > 16384 {
		return bad("bad unitsPerEm")
	}
	locaFmt := int(int16(binary.BigEndian.Uint16(head[50:])))
	if locaFmt != 0 && locaFmt != 1 {
		return bad("bad indexToLocFormat")
	}
	if len(maxp) < 6 {
		return bad("bad maxp table")
	}
	numGlyphs := int(binary.BigEndian.Uint16(maxp[4:]))
	if numGlyphs < 1 {
		return bad("no glyphs")
	}
	if cff == nil && len(maxp) < 32 {
		return bad("bad maxp table")
	}
	if len(hhea) < 36 {
		return bad("bad hhea table")
	}
	nHM := int(binary.BigEndian.Uint16(hhea[34:]))
	if nHM < 1 || nHM > numGlyphs || len(hmtx) < 4*nHM+2*(numGlyphs-nHM) {
		return bad("hmtx does not match hhea and maxp")
	}
	if len(cmap) < 4 {
		return bad("bad cmap table")
	}
	if cff != nil {
		if len(cff) < 4 || cff[0] != 1 {
			return bad("bad CFF table")
		}
		return nil
	}
	// loca
	entry := 2
	if locaFmt == 1 {
		entry = 4
	}
	if len(loca) < entry*(numGlyphs+1) {
		return bad("loca too short")
	}
	locaAt := func(i int) int {
		if locaFmt == 0 {
			return 2 * int(binary.BigEndian.Uint16(loca[2*i:]))
		}
		return int(binary.BigEndian.Uint32(loca[4*i:]))
	}
	prev := 0
	for i := 0; i <= numGlyphs; i++ {
		o := locaAt(i)
		if o < prev || o > len(glyf) {
			return bad("loca offsets are not increasing or lie outside glyf")
		}
		prev = o
	}
	for i := 0; i < numGlyphs; i++ {
		a, b := locaAt(i), locaAt(i+1)
		if b-a == 0 {
			continue
		}
		if err := validateGlyph(glyf[a:b], numGlyphs); err != nil {
			return err
		}
	}
	return nil
}

// validateGlyph checks that a glyph record is self-consistent: it parses
// completely inside its loca interval.
func validateGlyph(g []byte, numGlyphs int) error {
	r := &rd{b: g}
	nc := r.i16()
	r.skip(8)
	if r.err != nil {
		return bad("glyph shorter than its header")
	}
	if nc >= 0 {
		ends := make([]int, nc)
		last := -1
		for i := range ends {
			ends[i] = r.u16()
			if ends[i] < last {
				return bad("glyph contour ends not increasing")
			}
			last = ends[i]
		}
		il := r.u16()
		r.skip(il)
		if r.err != nil {
			return bad("glyph instructions outside the glyph")
		}
		np := 0
		if nc > 0 {
			np = ends[nc-1] + 1
		}
		fl := make([]byte, 0, np)
		for len(fl) < np {
			f := byte(r.u8())
			fl = append(fl, f)
			if f&flagRepeat != 0 {
				rep := r.u8()
				for ; rep > 0 && len(fl) < np; rep-- {
					fl = append(fl, f)
				}
				if rep > 0 {
					return bad("glyph flag repeat past the last point")
				}
			}
			if r.err != nil {
				return bad("glyph flags outside the glyph")
			}
		}
		skipC := 0
		for _, f := range fl {
			switch {
			case f&flagXShort != 0:
				skipC++
			case f&flagXSame == 0:
				skipC += 2
			}
			switch {
			case f&flagYShort != 0:
				skipC++
			case f&flagYSame == 0:
				skipC += 2
			}
		}
		r.skip(skipC)
		if r.err != nil {
			return bad("glyph coordinates outside the glyph")
		}
		return nil
	}
	// composite
	for {
		fl := r.u16()
		gid := r.u16()
		if gid >= numGlyphs {
			return bad("composite refers to a missing glyph")
		}
		n := 2
		if fl&compArgWords != 0 {
			n = 4
		}
		switch {
		case fl&compScale != 0:
			n += 2
		case fl&compXYScale != 0:
			n += 4
		case fl&comp2x2 != 0:
			n += 8
		}
		r.skip(n)
		if r.err != nil {
			return bad("composite glyph truncated")
		}
		if fl&compMore == 0 {
			if fl&compInstr != 0 {
				il := r.u16()
				r.skip(il)
				if r.err != nil {
					return bad("composite instructions outside the glyph")
				}
			}
			return nil
		}
	}
}

// FamilyKey is the lower-case form family names are compared in.
func FamilyKey(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}
