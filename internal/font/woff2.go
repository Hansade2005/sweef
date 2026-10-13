package font

import (
	"bytes"
	"encoding/binary"
	"io"

	"github.com/dsnet/compress/brotli"
)

// woff2.go: WOFF 2.0 (https://www.w3.org/TR/WOFF2/) to sfnt, in pure Go.
//
// The compressed stream is brotli (github.com/dsnet/compress/brotli, the
// decoder already used for HTTP content encoding) and is read with a hard cap
// of the size the directory announces. The glyf/loca and hmtx transforms are
// reversed following the specification; every read goes through a
// bounds-checked reader, every count is checked against the bytes that are
// actually left, and the size of everything produced is capped, so hostile
// input costs at most Limits.MaxSfnt of memory and never panics (a recover
// still guards the whole decode). Font collections (flavor 'ttcf') are not
// supported.

var woff2KnownTags = [63]string{
	"cmap", "head", "hhea", "hmtx", "maxp", "name", "OS/2", "post", "cvt ", "fpgm", "glyf", "loca", "prep",
	"CFF ", "VORG", "EBDT", "EBLC", "gasp", "hdmx", "kern", "LTSH", "PCLT", "VDMX", "vhea", "vmtx", "BASE",
	"GDEF", "GPOS", "GSUB", "EBSC", "JSTF", "MATH", "CBDT", "CBLC", "COLR", "CPAL", "SVG ", "sbix", "acnt",
	"avar", "bdat", "bloc", "bsln", "cvar", "fdsc", "feat", "fmtx", "fvar", "gvar", "hsty", "just", "lcar",
	"mort", "morx", "opbd", "prop", "trak", "Zapf", "Silf", "Glat", "Gloc", "Feat", "Sill",
}

// uintBase128 reads the variable length integer of the WOFF2 directory.
func (r *rd) uintBase128() uint32 {
	var acc uint32
	for i := 0; i < 5; i++ {
		b := r.u8()
		if r.err != nil {
			return 0
		}
		if i == 0 && b == 0x80 {
			r.err = bad("woff2: UIntBase128 with leading zero")
			return 0
		}
		if acc&0xFE000000 != 0 {
			r.err = bad("woff2: UIntBase128 overflow")
			return 0
		}
		acc = acc<<7 | uint32(b&0x7f)
		if b&0x80 == 0 {
			return acc
		}
	}
	r.err = bad("woff2: UIntBase128 too long")
	return 0
}

// u255 reads a 255UInt16.
func (r *rd) u255() int {
	b := r.u8()
	switch b {
	case 253:
		return r.u16()
	case 255:
		return 253 + r.u8()
	case 254:
		return 506 + r.u8()
	}
	return b
}

type w2table struct {
	tag       string
	flags     int
	transform bool
	origLen   int
	srcLen    int // bytes in the decompressed stream
	src       []byte
}

// DecodeWOFF2 converts a WOFF 2.0 file to a plain sfnt.
func DecodeWOFF2(data []byte, lim Limits) (out []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			out, err = nil, bad("woff2: internal error")
		}
	}()
	lim = lim.norm()
	r := &rd{b: data}
	if r.u32() != woff2Sig {
		return nil, bad("woff2: bad signature")
	}
	flavor := r.u32()
	length := int64(r.u32())
	numTables := r.u16()
	r.u16()   // reserved
	r.skip(4) // totalSfntSize
	compSize := int64(r.u32())
	r.skip(4) // major, minor
	metaOff, metaLen, _ := int64(r.u32()), int64(r.u32()), r.u32()
	privOff, privLen := int64(r.u32()), int64(r.u32())
	if r.err != nil {
		return nil, r.err
	}
	if length != int64(len(data)) {
		return nil, bad("woff2: header length does not match file size")
	}
	if numTables == 0 || numTables > lim.MaxTables {
		return nil, bad("woff2: bad table count")
	}
	if flavor == 0x74746366 {
		return nil, bad("woff2: font collections are not supported")
	}
	switch flavor {
	case 0x00010000, 0x4F54544F, 0x74727565:
	default:
		return nil, bad("woff2: unsupported flavor")
	}
	tabs := make([]w2table, numTables)
	seen := map[string]bool{}
	var streamLen, sfntLen int64
	for i := range tabs {
		t := &tabs[i]
		t.flags = r.u8()
		idx := t.flags & 0x3f
		if idx == 63 {
			t.tag = string(r.bytes(4))
		} else {
			t.tag = woff2KnownTags[idx]
		}
		ver := t.flags >> 6
		switch t.tag {
		case "glyf", "loca":
			t.transform = ver == 0
			if ver == 1 || ver == 2 {
				return nil, bad("woff2: unknown glyf/loca transform")
			}
		default:
			t.transform = ver != 0
			if ver > 1 || (ver == 1 && t.tag != "hmtx") {
				return nil, bad("woff2: unknown table transform")
			}
		}
		ol := r.uintBase128()
		if r.err != nil {
			return nil, r.err
		}
		t.origLen = int(ol)
		t.srcLen = t.origLen
		if t.transform {
			tl := r.uintBase128()
			if r.err != nil {
				return nil, r.err
			}
			t.srcLen = int(tl)
			if t.tag == "loca" && tl != 0 {
				return nil, bad("woff2: transformed loca must be empty")
			}
		}
		if seen[t.tag] {
			return nil, bad("woff2: duplicate table " + t.tag)
		}
		seen[t.tag] = true
		if int64(t.origLen) > int64(lim.MaxSfnt) || int64(t.srcLen) > int64(lim.MaxSfnt) {
			return nil, bad("woff2: table too large")
		}
		streamLen += int64(t.srcLen)
		sfntLen += int64(t.origLen)
		if streamLen > int64(lim.MaxSfnt) || sfntLen > int64(lim.MaxSfnt) {
			return nil, bad("woff2: font too large")
		}
	}
	if seen["glyf"] != seen["loca"] || (seen["glyf"] && tabsTransformed(tabs, "glyf") != tabsTransformed(tabs, "loca")) {
		return nil, bad("woff2: glyf and loca must both be present and both transformed or both not")
	}
	if compSize <= 0 || int64(r.off)+compSize > int64(len(data)) {
		return nil, bad("woff2: compressed stream outside file")
	}
	compEnd := int64(r.off) + compSize
	// optional blocks follow the compressed stream (4-byte aligned) and stay inside the file
	for _, blk := range [][2]int64{{metaOff, metaLen}, {privOff, privLen}} {
		if blk[1] == 0 {
			continue
		}
		if blk[0] < compEnd || blk[0]+blk[1] > length {
			return nil, bad("woff2: bad metadata block")
		}
	}
	// decompress with a cap of exactly the announced size
	stream, derr := brotliAll(data[r.off:compEnd], int(streamLen))
	if derr != nil {
		return nil, derr
	}
	if int64(len(stream)) != streamLen {
		return nil, bad("woff2: decompressed size differs from the directory")
	}
	pos := 0
	for i := range tabs {
		tabs[i].src = stream[pos : pos+tabs[i].srcLen]
		pos += tabs[i].srcLen
	}
	byTag := map[string]*w2table{}
	for i := range tabs {
		byTag[tabs[i].tag] = &tabs[i]
	}
	var tables []sfntTable
	var glyf, loca []byte
	var numGlyphs int
	var xMins []int16
	if g := byTag["glyf"]; g != nil {
		if g.transform {
			var gerr error
			glyf, loca, numGlyphs, _, xMins, gerr = reconstructGlyf(g.src, lim.MaxSfnt)
			if gerr != nil {
				return nil, gerr
			}
			if l := byTag["loca"]; l != nil && l.origLen != len(loca) {
				return nil, bad("woff2: loca size differs from the directory")
			}
		} else {
			glyf = g.src
			loca = byTag["loca"].src
		}
	}
	for i := range tabs {
		t := &tabs[i]
		switch {
		case t.tag == "glyf":
			tables = append(tables, sfntTable{"glyf", glyf})
		case t.tag == "loca":
			tables = append(tables, sfntTable{"loca", loca})
		case t.tag == "hmtx" && t.transform:
			hm, herr := reconstructHmtx(t.src, byTag, xMins, numGlyphs)
			if herr != nil {
				return nil, herr
			}
			tables = append(tables, sfntTable{"hmtx", hm})
		default:
			tables = append(tables, sfntTable{t.tag, append([]byte(nil), t.src...)})
		}
	}
	return buildSFNT(flavor, tables, lim.MaxSfnt)
}

func tabsTransformed(tabs []w2table, tag string) bool {
	for i := range tabs {
		if tabs[i].tag == tag {
			return tabs[i].transform
		}
	}
	return false
}

// brotliAll decompresses b, producing at most max+1 bytes (the caller compares
// the length with what the directory announced).
func brotliAll(b []byte, limit int) (out []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			out, err = nil, bad("woff2: brotli decoder failure")
		}
	}()
	br, e := brotli.NewReader(bytes.NewReader(b), nil)
	if e != nil {
		return nil, bad("woff2: bad brotli stream")
	}
	defer br.Close()
	out, e = io.ReadAll(io.LimitReader(br, int64(limit)+1))
	if e != nil {
		return nil, bad("woff2: bad brotli stream")
	}
	if len(out) > limit {
		return nil, bad("woff2: brotli stream is longer than announced")
	}
	return out, nil
}

// ---- glyf / loca transform -----------------------------------------------------

const (
	flagOnCurve  = 0x01
	flagXShort   = 0x02
	flagYShort   = 0x04
	flagRepeat   = 0x08
	flagXSame    = 0x10
	flagYSame    = 0x20
	flagOverlap  = 0x40
	compArgWords = 0x0001
	compScale    = 0x0008
	compMore     = 0x0020
	compXYScale  = 0x0040
	comp2x2      = 0x0080
	compInstr    = 0x0100
)

func withSign(flag, v int) int {
	if flag&1 != 0 {
		return v
	}
	return -v
}

// reconstructGlyf rebuilds glyf and loca from the transformed glyf stream.
// xMins are the xMin of each glyph (for the hmtx transform).
func reconstructGlyf(src []byte, limit int) (glyf, loca []byte, numGlyphs, indexFmt int, xMins []int16, err error) {
	h := &rd{b: src}
	h.u16() // reserved (version)
	opt := h.u16()
	numGlyphs = h.u16()
	indexFmt = h.u16()
	var sz [7]int
	for i := range sz {
		sz[i] = int(h.u32())
	}
	if h.err != nil {
		return nil, nil, 0, 0, nil, h.err
	}
	if indexFmt > 1 {
		return nil, nil, 0, 0, nil, bad("woff2: bad indexFormat")
	}
	sub := func(n int) *rd {
		if n < 0 || n > h.left() {
			h.fail()
			return &rd{err: h.err}
		}
		b := h.b[h.off : h.off+n]
		h.off += n
		return &rd{b: b}
	}
	nContourS, nPointsS, flagS, glyphS, compS, bboxS, instrS := sub(sz[0]), sub(sz[1]), sub(sz[2]), sub(sz[3]), sub(sz[4]), sub(sz[5]), sub(sz[6])
	if h.err != nil {
		return nil, nil, 0, 0, nil, h.err
	}
	var overlap []byte
	if opt&1 != 0 {
		overlap = h.bytes((numGlyphs + 7) / 8)
		if h.err != nil {
			return nil, nil, 0, 0, nil, h.err
		}
	}
	// bbox stream: bitmap padded to four bytes, then the boxes
	bbBits := bboxS.bytes(((numGlyphs + 31) >> 5) << 2)
	if bboxS.err != nil {
		return nil, nil, 0, 0, nil, bboxS.err
	}
	if nContourS.left() < 2*numGlyphs {
		return nil, nil, 0, 0, nil, bad("woff2: nContour stream too short")
	}
	offsets := make([]uint32, numGlyphs+1)
	xMins = make([]int16, numGlyphs)
	var g []byte // output
	put16 := func(v int) { g = append(g, byte(v>>8), byte(v)) }
	for i := 0; i < numGlyphs; i++ {
		offsets[i] = uint32(len(g))
		nc := nContourS.i16()
		hasBBox := bbBits[i>>3]&(0x80>>(uint(i)&7)) != 0
		switch {
		case nc == 0:
			if hasBBox {
				return nil, nil, 0, 0, nil, bad("woff2: empty glyph with a bounding box")
			}
			continue
		case nc > 0:
			// simple glyph
			endPts := make([]int, nc)
			total := 0
			for c := 0; c < nc; c++ {
				np := nPointsS.u255()
				if nPointsS.err != nil {
					return nil, nil, 0, 0, nil, nPointsS.err
				}
				total += np
				if total > flagS.left() {
					return nil, nil, 0, 0, nil, bad("woff2: more points than flags")
				}
				endPts[c] = total - 1
			}
			flags := flagS.bytes(total)
			if flagS.err != nil {
				return nil, nil, 0, 0, nil, flagS.err
			}
			xs, ys := make([]int, total), make([]int, total)
			x, y := 0, 0
			for p := 0; p < total; p++ {
				f := int(flags[p] & 0x7f)
				var dx, dy int
				switch {
				case f < 10:
					b := glyphS.u8()
					dy = withSign(f, ((f&14)<<7)+b)
				case f < 20:
					b := glyphS.u8()
					dx = withSign(f, (((f-10)&14)<<7)+b)
				case f < 84:
					b0 := f - 20
					b1 := glyphS.u8()
					dx = withSign(f, 1+(b0&0x30)+(b1>>4))
					dy = withSign(f>>1, 1+((b0&0x0c)<<2)+(b1&0x0f))
				case f < 120:
					b0 := f - 84
					b1, b2 := glyphS.u8(), glyphS.u8()
					dx = withSign(f, 1+((b0/12)<<8)+b1)
					dy = withSign(f>>1, 1+(((b0%12)>>2)<<8)+b2)
				case f < 124:
					b1, b2, b3 := glyphS.u8(), glyphS.u8(), glyphS.u8()
					dx = withSign(f, (b1<<4)+(b2>>4))
					dy = withSign(f>>1, ((b2&0x0f)<<8)+b3)
				default:
					b1, b2, b3, b4 := glyphS.u8(), glyphS.u8(), glyphS.u8(), glyphS.u8()
					dx = withSign(f, (b1<<8)+b2)
					dy = withSign(f>>1, (b3<<8)+b4)
				}
				x += dx
				y += dy
				xs[p], ys[p] = x, y
				if x < -32768 || x > 32767 || y < -32768 || y > 32767 {
					return nil, nil, 0, 0, nil, bad("woff2: coordinate out of range")
				}
			}
			if glyphS.err != nil {
				return nil, nil, 0, 0, nil, glyphS.err
			}
			il := glyphS.u255()
			if glyphS.err != nil {
				return nil, nil, 0, 0, nil, glyphS.err
			}
			instr := instrS.bytes(il)
			if instrS.err != nil {
				return nil, nil, 0, 0, nil, instrS.err
			}
			var bb [4]int
			if hasBBox {
				for k := range bb {
					bb[k] = bboxS.i16()
				}
				if bboxS.err != nil {
					return nil, nil, 0, 0, nil, bboxS.err
				}
			} else if total > 0 {
				bb = [4]int{xs[0], ys[0], xs[0], ys[0]}
				for p := 1; p < total; p++ {
					bb[0], bb[1] = min(bb[0], xs[p]), min(bb[1], ys[p])
					bb[2], bb[3] = max(bb[2], xs[p]), max(bb[3], ys[p])
				}
			}
			xMins[i] = int16(bb[0])
			put16(nc)
			for _, v := range bb {
				put16(v)
			}
			for _, e := range endPts {
				put16(e)
			}
			put16(il)
			g = append(g, instr...)
			ov := overlap != nil && overlap[i>>3]&(0x80>>(uint(i)&7)) != 0
			g = appendSimplePoints(g, flags, xs, ys, ov)
		case nc == -1:
			if !hasBBox {
				return nil, nil, 0, 0, nil, bad("woff2: composite glyph without a bounding box")
			}
			var bb [4]int
			for k := range bb {
				bb[k] = bboxS.i16()
			}
			if bboxS.err != nil {
				return nil, nil, 0, 0, nil, bboxS.err
			}
			xMins[i] = int16(bb[0])
			put16(0xffff)
			for _, v := range bb {
				put16(v)
			}
			haveInstr := false
			for {
				fl := compS.u16()
				gid := compS.u16()
				if compS.err != nil {
					return nil, nil, 0, 0, nil, compS.err
				}
				put16(fl)
				put16(gid)
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
				g = append(g, compS.bytes(n)...)
				if compS.err != nil {
					return nil, nil, 0, 0, nil, compS.err
				}
				if fl&compInstr != 0 {
					haveInstr = true
				}
				if fl&compMore == 0 {
					break
				}
				if len(g) > limit {
					return nil, nil, 0, 0, nil, bad("woff2: glyf too large")
				}
			}
			if haveInstr {
				il := glyphS.u255()
				if glyphS.err != nil {
					return nil, nil, 0, 0, nil, glyphS.err
				}
				instr := instrS.bytes(il)
				if instrS.err != nil {
					return nil, nil, 0, 0, nil, instrS.err
				}
				put16(il)
				g = append(g, instr...)
			}
		default:
			return nil, nil, 0, 0, nil, bad("woff2: bad number of contours")
		}
		for len(g)&3 != 0 {
			g = append(g, 0)
		}
		if len(g) > limit {
			return nil, nil, 0, 0, nil, bad("woff2: glyf too large")
		}
	}
	offsets[numGlyphs] = uint32(len(g))
	if indexFmt == 0 {
		if len(g) > 0x1fffe {
			return nil, nil, 0, 0, nil, bad("woff2: glyf too large for short loca")
		}
		loca = make([]byte, 2*(numGlyphs+1))
		for i, o := range offsets {
			binary.BigEndian.PutUint16(loca[2*i:], uint16(o/2))
		}
	} else {
		loca = make([]byte, 4*(numGlyphs+1))
		for i, o := range offsets {
			binary.BigEndian.PutUint32(loca[4*i:], o)
		}
	}
	return g, loca, numGlyphs, indexFmt, xMins, nil
}

// appendSimplePoints writes the flag and coordinate arrays of a simple glyph
// in the glyf encoding (smallest form, runs as repeats).
func appendSimplePoints(g []byte, flags []byte, xs, ys []int, overlap bool) []byte {
	n := len(xs)
	fl := make([]byte, n)
	var xb, yb []byte
	px, py := 0, 0
	for i := 0; i < n; i++ {
		var f byte
		if flags[i]&0x80 == 0 {
			f |= flagOnCurve
		}
		dx, dy := xs[i]-px, ys[i]-py
		px, py = xs[i], ys[i]
		switch {
		case dx == 0:
			f |= flagXSame
		case dx > -256 && dx < 256:
			f |= flagXShort
			if dx > 0 {
				f |= flagXSame
				xb = append(xb, byte(dx))
			} else {
				xb = append(xb, byte(-dx))
			}
		default:
			xb = append(xb, byte(dx>>8), byte(dx))
		}
		switch {
		case dy == 0:
			f |= flagYSame
		case dy > -256 && dy < 256:
			f |= flagYShort
			if dy > 0 {
				f |= flagYSame
				yb = append(yb, byte(dy))
			} else {
				yb = append(yb, byte(-dy))
			}
		default:
			yb = append(yb, byte(dy>>8), byte(dy))
		}
		fl[i] = f
	}
	if overlap && n > 0 {
		fl[0] |= flagOverlap
	}
	for i := 0; i < n; {
		j := i + 1
		for j < n && fl[j] == fl[i] && j-i < 256 {
			j++
		}
		if j-i >= 3 {
			g = append(g, fl[i]|flagRepeat, byte(j-i-1))
			i = j
			continue
		}
		g = append(g, fl[i])
		i++
	}
	g = append(g, xb...)
	return append(g, yb...)
}

// reconstructHmtx reverses the hmtx transform.
func reconstructHmtx(src []byte, by map[string]*w2table, xMins []int16, numGlyphs int) ([]byte, error) {
	hh := by["hhea"]
	mp := by["maxp"]
	if hh == nil || mp == nil || hh.transform || mp.transform || len(hh.src) < 36 || len(mp.src) < 6 {
		return nil, bad("woff2: transformed hmtx needs hhea and maxp")
	}
	nHM := int(binary.BigEndian.Uint16(hh.src[34:]))
	ng := int(binary.BigEndian.Uint16(mp.src[4:]))
	if numGlyphs != 0 && numGlyphs != ng {
		return nil, bad("woff2: glyph counts differ")
	}
	if nHM < 1 || nHM > ng || (xMins == nil && ng > 0) || len(xMins) != ng {
		return nil, bad("woff2: bad hmtx transform inputs")
	}
	r := &rd{b: src}
	fl := r.u8()
	if fl&^3 != 0 {
		return nil, bad("woff2: reserved hmtx flags")
	}
	adv := make([]int, nHM)
	for i := range adv {
		adv[i] = r.u16()
	}
	lsb := make([]int, ng)
	for i := 0; i < nHM; i++ {
		if fl&1 != 0 {
			lsb[i] = int(xMins[i])
		} else {
			lsb[i] = r.i16()
		}
	}
	for i := nHM; i < ng; i++ {
		if fl&2 != 0 {
			lsb[i] = int(xMins[i])
		} else {
			lsb[i] = r.i16()
		}
	}
	if r.err != nil {
		return nil, r.err
	}
	out := make([]byte, 0, 4*nHM+2*(ng-nHM))
	for i := 0; i < nHM; i++ {
		out = append(out, byte(adv[i]>>8), byte(adv[i]), byte(lsb[i]>>8), byte(lsb[i]))
	}
	for i := nHM; i < ng; i++ {
		out = append(out, byte(lsb[i]>>8), byte(lsb[i]))
	}
	return out, nil
}
