package fonttest

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"sort"
)

// Table is one sfnt table.
type Table struct {
	Tag  string
	Data []byte
}

// ParseTables splits an sfnt into its tables in directory order.
func ParseTables(sfnt []byte) []Table {
	n := int(binary.BigEndian.Uint16(sfnt[4:]))
	var out []Table
	for i := 0; i < n; i++ {
		e := sfnt[12+16*i:]
		off, ln := int(binary.BigEndian.Uint32(e[8:])), int(binary.BigEndian.Uint32(e[12:]))
		out = append(out, Table{string(e[:4]), sfnt[off : off+ln]})
	}
	return out
}

func tabs(sfnt []byte) []Table {
	out := ParseTables(sfnt)
	sort.Slice(out, func(i, j int) bool { return out[i].Tag < out[j].Tag })
	return out
}

// WOFF encodes an sfnt as WOFF 1.0. Tables are zlib compressed when that makes
// them smaller and compress is set.
func WOFF(sfnt []byte, compress bool) []byte {
	ts := tabs(sfnt)
	n := len(ts)
	pos := 44 + 20*n
	var dir, body []byte
	total := 12 + 16*n
	for _, t := range ts {
		d := t.Data
		if compress {
			var b bytes.Buffer
			w, _ := zlib.NewWriterLevel(&b, 9)
			w.Write(t.Data)
			w.Close()
			if b.Len() < len(t.Data) {
				d = b.Bytes()
			}
		}
		dir = append(dir, t.Tag...)
		dir = append(dir, be32(uint32(pos))...)
		dir = append(dir, be32(uint32(len(d)))...)
		dir = append(dir, be32(uint32(len(t.Data)))...)
		dir = append(dir, be32(Checksum(t.Data))...)
		body = append(body, pad4(append([]byte(nil), d...))...)
		pos += (len(d) + 3) &^ 3
		total += (len(t.Data) + 3) &^ 3
	}
	flavor := binary.BigEndian.Uint32(sfnt)
	h := cat(be32(0x774F4646), be32(flavor), be32(uint32(pos)), be16(n), be16(0), be32(uint32(total)), be16(1), be16(0),
		be32(0), be32(0), be32(0), be32(0), be32(0))
	return cat(h, dir, body)
}

var knownTags = [63]string{
	"cmap", "head", "hhea", "hmtx", "maxp", "name", "OS/2", "post", "cvt ", "fpgm", "glyf", "loca", "prep",
	"CFF ", "VORG", "EBDT", "EBLC", "gasp", "hdmx", "kern", "LTSH", "PCLT", "VDMX", "vhea", "vmtx", "BASE",
	"GDEF", "GPOS", "GSUB", "EBSC", "JSTF", "MATH", "CBDT", "CBLC", "COLR", "CPAL", "SVG ", "sbix", "acnt",
	"avar", "bdat", "bloc", "bsln", "cvar", "fdsc", "feat", "fmtx", "fvar", "gvar", "hsty", "just", "lcar",
	"mort", "morx", "opbd", "prop", "trak", "Zapf", "Silf", "Glat", "Gloc", "Feat", "Sill",
}

func base128(v uint32) []byte {
	if v == 0 {
		return []byte{0}
	}
	var b []byte
	for ; v > 0; v >>= 7 {
		b = append([]byte{byte(v & 0x7f)}, b...)
	}
	for i := 0; i < len(b)-1; i++ {
		b[i] |= 0x80
	}
	return b
}

func u255(v int) []byte {
	switch {
	case v < 253:
		return []byte{byte(v)}
	case v < 506:
		return []byte{255, byte(v - 253)}
	case v < 762:
		return []byte{254, byte(v - 506)}
	}
	return []byte{253, byte(v >> 8), byte(v)}
}

// Options for WOFF2.
type Options struct {
	// Transform applies the glyf/loca transform (and the hmtx transform when
	// the font allows it); otherwise the null transform is used.
	Transform bool
	// Brotli compresses the stream; nil uses uncompressed meta-blocks.
	Brotli func([]byte) []byte
	// Overlap sets the OVERLAP_SIMPLE bit of every simple glyph (option flag 1).
	Overlap bool
}

// WOFF2 encodes an sfnt as WOFF 2.0.
func WOFF2(sfnt []byte, o Options) []byte {
	ts := tabs(sfnt)
	by := map[string][]byte{}
	for _, t := range ts {
		by[t.Tag] = t.Data
	}
	var dir, stream []byte
	var glyfT, locaT, hmtxT []byte
	haveGlyf := by["glyf"] != nil && by["loca"] != nil && by["head"] != nil && by["maxp"] != nil
	var xMins []int
	if o.Transform && haveGlyf {
		glyfT, xMins = TransformGlyf(by["glyf"], by["loca"], by["head"], by["maxp"], o.Overlap)
		locaT = []byte{}
		if hm := by["hmtx"]; hm != nil && by["hhea"] != nil {
			hmtxT = transformHmtx(hm, by["hhea"], by["maxp"], xMins)
		}
	}
	for _, t := range ts {
		idx := 63
		for i, k := range knownTags {
			if k == t.Tag {
				idx = i
			}
		}
		fl := idx
		src := t.Data
		transformed := false
		switch t.Tag {
		case "glyf":
			if o.Transform && haveGlyf {
				src, transformed = glyfT, true
			} else {
				fl |= 3 << 6
			}
		case "loca":
			if o.Transform && haveGlyf {
				src, transformed = locaT, true
			} else {
				fl |= 3 << 6
			}
		case "hmtx":
			if hmtxT != nil {
				src, transformed, fl = hmtxT, true, fl|1<<6
			}
		}
		dir = append(dir, byte(fl))
		if idx == 63 {
			dir = append(dir, t.Tag...)
		}
		dir = append(dir, base128(uint32(len(t.Data)))...)
		if transformed {
			dir = append(dir, base128(uint32(len(src)))...)
		}
		stream = append(stream, src...)
	}
	var comp []byte
	if o.Brotli != nil {
		comp = o.Brotli(stream)
	} else {
		comp = StoredBrotli(stream)
	}
	flavor := binary.BigEndian.Uint32(sfnt)
	body := pad4(append(append([]byte(nil), dir...), comp...))
	length := 48 + len(body)
	h := cat(be32(0x774F4632), be32(flavor), be32(uint32(length)), be16(len(ts)), be16(0), be32(uint32(len(sfnt))), be32(uint32(len(comp))),
		be16(1), be16(0), be32(0), be32(0), be32(0), be32(0), be32(0))
	return cat(h, body)
}

// StoredBrotli wraps b in a valid brotli stream made of uncompressed meta-blocks.
func StoredBrotli(b []byte) []byte {
	var out []byte
	var acc uint64
	var nbits uint
	put := func(v uint64, n uint) {
		acc |= v << nbits
		nbits += n
		for nbits >= 8 {
			out = append(out, byte(acc))
			acc >>= 8
			nbits -= 8
		}
	}
	flush := func() {
		if nbits > 0 {
			out = append(out, byte(acc))
			acc, nbits = 0, 0
		}
	}
	put(0, 1) // WBITS = 16
	for len(b) > 0 {
		n := len(b)
		if n > 65536 {
			n = 65536
		}
		put(0, 1)            // ISLAST
		put(0, 2)            // MNIBBLES = 4
		put(uint64(n-1), 16) // MLEN - 1
		put(1, 1)            // ISUNCOMPRESSED
		flush()
		out = append(out, b[:n]...)
		b = b[n:]
	}
	put(3, 2) // ISLAST, ISLASTEMPTY
	flush()
	return out
}

func triplet(dx, dy int, on bool) (flag byte, data []byte) {
	abs := func(v int) int {
		if v < 0 {
			return -v
		}
		return v
	}
	ax, ay := abs(dx), abs(dy)
	sx, sy := 0, 0
	if dx > 0 {
		sx = 1
	}
	if dy > 0 {
		sy = 2
	}
	switch {
	case dx == 0 && ay < 1280:
		flag = byte(((ay & 0xf00) >> 7) + sy/2)
		data = []byte{byte(ay)}
	case dy == 0 && ax < 1280:
		flag = byte(10 + ((ax & 0xf00) >> 7) + sx)
		data = []byte{byte(ax)}
	case ax < 65 && ay < 65:
		flag = byte(20 + ((ax - 1) & 0x30) + (((ay - 1) & 0x30) >> 2) + sx + sy)
		data = []byte{byte((((ax - 1) & 0xf) << 4) | ((ay - 1) & 0xf))}
	case ax < 769 && ay < 769:
		flag = byte(84 + 12*((ax-1)>>8) + (((ay - 1) >> 8) << 2) + sx + sy)
		data = []byte{byte(ax - 1), byte(ay - 1)}
	case ax < 4096 && ay < 4096:
		flag = byte(120 + sx + sy)
		data = []byte{byte(ax >> 4), byte(((ax & 0xf) << 4) | (ay >> 8)), byte(ay)}
	default:
		flag = byte(124 + sx + sy)
		data = []byte{byte(ax >> 8), byte(ax), byte(ay >> 8), byte(ay)}
	}
	if !on {
		flag |= 0x80
	}
	return
}

// TransformGlyf produces the WOFF2 transformed glyf table for glyf/loca and
// the xMin of every glyph.
func TransformGlyf(glyf, loca, head, maxp []byte, overlap bool) (out []byte, xMins []int) {
	numGlyphs := int(binary.BigEndian.Uint16(maxp[4:]))
	locaFmt := int(binary.BigEndian.Uint16(head[50:]))
	off := func(i int) int {
		if locaFmt == 0 {
			return 2 * int(binary.BigEndian.Uint16(loca[2*i:]))
		}
		return int(binary.BigEndian.Uint32(loca[4*i:]))
	}
	var nContour, nPoints, flags, glyphS, comp, bbox, instr []byte
	bboxBits := make([]byte, ((numGlyphs+31)>>5)<<2)
	ovl := make([]byte, (numGlyphs+7)/8)
	xMins = make([]int, numGlyphs)
	for i := 0; i < numGlyphs; i++ {
		g := glyf[off(i):off(i+1)]
		if len(g) == 0 {
			nContour = append(nContour, 0, 0)
			continue
		}
		nc := int(int16(binary.BigEndian.Uint16(g)))
		bb := [4]int{int(int16(binary.BigEndian.Uint16(g[2:]))), int(int16(binary.BigEndian.Uint16(g[4:]))), int(int16(binary.BigEndian.Uint16(g[6:]))), int(int16(binary.BigEndian.Uint16(g[8:])))}
		xMins[i] = bb[0]
		nContour = append(nContour, be16(nc&0xffff)...)
		if nc < 0 {
			p := 10
			haveInstr := false
			for {
				fl := int(binary.BigEndian.Uint16(g[p:]))
				n := 4
				if fl&1 != 0 {
					n += 4
				} else {
					n += 2
				}
				switch {
				case fl&0x8 != 0:
					n += 2
				case fl&0x40 != 0:
					n += 4
				case fl&0x80 != 0:
					n += 8
				}
				comp = append(comp, g[p:p+n]...)
				p += n
				if fl&0x100 != 0 {
					haveInstr = true
				}
				if fl&0x20 == 0 {
					break
				}
			}
			if haveInstr {
				il := int(binary.BigEndian.Uint16(g[p:]))
				glyphS = append(glyphS, u255(il)...)
				instr = append(instr, g[p+2:p+2+il]...)
			}
			bboxBits[i>>3] |= 0x80 >> (uint(i) & 7)
			for _, v := range bb {
				bbox = append(bbox, be16(v&0xffff)...)
			}
			continue
		}
		p := 10
		ends := make([]int, nc)
		for c := range ends {
			ends[c] = int(binary.BigEndian.Uint16(g[p:]))
			p += 2
		}
		il := int(binary.BigEndian.Uint16(g[p:]))
		p += 2
		ins := g[p : p+il]
		p += il
		np := ends[nc-1] + 1
		fl := make([]byte, 0, np)
		for len(fl) < np {
			f := g[p]
			p++
			fl = append(fl, f)
			if f&0x08 != 0 {
				r := int(g[p])
				p++
				for ; r > 0; r-- {
					fl = append(fl, f)
				}
			}
		}
		xs, ys := make([]int, np), make([]int, np)
		v := 0
		for k := 0; k < np; k++ {
			switch {
			case fl[k]&0x02 != 0:
				d := int(g[p])
				p++
				if fl[k]&0x10 == 0 {
					d = -d
				}
				v += d
			case fl[k]&0x10 == 0:
				v += int(int16(binary.BigEndian.Uint16(g[p:])))
				p += 2
			}
			xs[k] = v
		}
		v = 0
		for k := 0; k < np; k++ {
			switch {
			case fl[k]&0x04 != 0:
				d := int(g[p])
				p++
				if fl[k]&0x20 == 0 {
					d = -d
				}
				v += d
			case fl[k]&0x20 == 0:
				v += int(int16(binary.BigEndian.Uint16(g[p:])))
				p += 2
			}
			ys[k] = v
		}
		prev := -1
		for _, e := range ends {
			nPoints = append(nPoints, u255(e-prev)...)
			prev = e
		}
		px, py := 0, 0
		cb := [4]int{xs[0], ys[0], xs[0], ys[0]}
		for k := 0; k < np; k++ {
			f, d := triplet(xs[k]-px, ys[k]-py, fl[k]&1 != 0)
			px, py = xs[k], ys[k]
			flags = append(flags, f)
			glyphS = append(glyphS, d...)
			cb = [4]int{min(cb[0], px), min(cb[1], py), max(cb[2], px), max(cb[3], py)}
		}
		glyphS = append(glyphS, u255(il)...)
		instr = append(instr, ins...)
		if cb != bb {
			bboxBits[i>>3] |= 0x80 >> (uint(i) & 7)
			for _, v := range bb {
				bbox = append(bbox, be16(v&0xffff)...)
			}
		}
		if overlap {
			ovl[i>>3] |= 0x80 >> (uint(i) & 7)
		}
	}
	opt := 0
	if overlap {
		opt = 1
	}
	out = cat(be16(0), be16(opt), be16(numGlyphs), be16(locaFmt),
		be32(uint32(len(nContour))), be32(uint32(len(nPoints))), be32(uint32(len(flags))), be32(uint32(len(glyphS))),
		be32(uint32(len(comp))), be32(uint32(len(bboxBits)+len(bbox))), be32(uint32(len(instr))),
		nContour, nPoints, flags, glyphS, comp, bboxBits, bbox, instr)
	if overlap {
		out = append(out, ovl...)
	}
	return out, xMins
}

func transformHmtx(hmtx, hhea, maxp []byte, xMins []int) []byte {
	nHM := int(binary.BigEndian.Uint16(hhea[34:]))
	ng := int(binary.BigEndian.Uint16(maxp[4:]))
	lsbAt := func(i int) int {
		if i < nHM {
			return int(int16(binary.BigEndian.Uint16(hmtx[4*i+2:])))
		}
		return int(int16(binary.BigEndian.Uint16(hmtx[4*nHM+2*(i-nHM):])))
	}
	prop, mono := true, true
	for i := 0; i < ng; i++ {
		if lsbAt(i) != xMins[i] {
			if i < nHM {
				prop = false
			} else {
				mono = false
			}
		}
	}
	fl := 0
	if prop {
		fl |= 1
	}
	if mono {
		fl |= 2
	}
	out := []byte{byte(fl)}
	for i := 0; i < nHM; i++ {
		out = append(out, hmtx[4*i], hmtx[4*i+1])
	}
	if !prop {
		for i := 0; i < nHM; i++ {
			out = append(out, hmtx[4*i+2], hmtx[4*i+3])
		}
	}
	if !mono {
		for i := nHM; i < ng; i++ {
			out = append(out, hmtx[4*nHM+2*(i-nHM)], hmtx[4*nHM+2*(i-nHM)+1])
		}
	}
	return out
}
