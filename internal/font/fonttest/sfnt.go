// Package fonttest builds small fonts for tests: a TrueType writer (so no
// licensed font binary has to be committed), WOFF and WOFF2 encoders and a
// few ready-made fonts whose glyphs are plain boxes with distinct widths and
// heights, which makes rendered text easy to recognise and compare.
//
// Nothing outside tests imports this package, so it adds nothing to the
// binaries.
package fonttest

import (
	"encoding/binary"
	"sort"
	"unicode/utf16"
)

// Pt is one outline point; Off marks an off-curve (quadratic control) point.
type Pt struct {
	X, Y int
	Off  bool
}

// Component places another glyph inside a composite.
type Component struct {
	GID    int
	DX, DY int
}

// Glyph is one glyph: simple (Contours) or composite (Comps).
type Glyph struct {
	Contours [][]Pt
	Comps    []Component
	Adv      int
	Instr    []byte // TrueType instructions (opaque bytes) of a simple glyph
}

// Lig is a GSUB ligature: the glyph sequence Comps (at least two) becomes To.
type Lig struct {
	Comps []int
	To    int
}

// Font describes a TrueType font.
type Font struct {
	Family, Style string
	UPM           int
	Ascent        int // hhea / typo ascender
	Descent       int // positive distance below the baseline
	LineGap       int
	XHeight, Cap  int
	Weight        int
	Italic        bool
	Mono          bool
	Glyphs        []Glyph // index 0 is .notdef
	Cmap          map[rune]int
	Ligatures     []Lig
	// Kern pairs as GPOS-less legacy 'kern' table entries (left gid, right gid, value in font units).
	Kern map[[2]int]int
	// ShortLoca selects the short loca format when the glyph data allows it.
	ShortLoca bool
	// ExtraTables are added verbatim (e.g. fvar and gvar of a variable font).
	ExtraTables map[string][]byte
}

// Rect is a closed box contour (counter-clockwise, all points on curve).
func Rect(x0, y0, x1, y1 int) []Pt {
	return []Pt{{X: x0, Y: y0}, {X: x1, Y: y0}, {X: x1, Y: y1}, {X: x0, Y: y1}}
}

func be16(v int) []byte { return []byte{byte(v >> 8), byte(v)} }
func be32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

func cat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func pad4(b []byte) []byte {
	for len(b)&3 != 0 {
		b = append(b, 0)
	}
	return b
}

// glyphBytes encodes a glyph in the glyf format; bbox is returned too.
func glyphBytes(g Glyph) (data []byte, bbox [4]int) {
	if len(g.Contours) == 0 && len(g.Comps) == 0 {
		return nil, bbox
	}
	first := true
	grow := func(x, y int) {
		if first {
			bbox = [4]int{x, y, x, y}
			first = false
			return
		}
		bbox[0], bbox[1] = min(bbox[0], x), min(bbox[1], y)
		bbox[2], bbox[3] = max(bbox[2], x), max(bbox[3], y)
	}
	if len(g.Comps) > 0 {
		var out []byte
		for _, c := range g.Comps {
			grow(c.DX, c.DY)
		}
		out = cat(be16(-1&0xffff), be16(bbox[0]), be16(bbox[1]), be16(bbox[2]), be16(bbox[3]))
		for i, c := range g.Comps {
			fl := 0x0001 | 0x0002 // words, args are xy values
			if i < len(g.Comps)-1 {
				fl |= 0x0020
			}
			out = append(out, cat(be16(fl), be16(c.GID), be16(c.DX), be16(c.DY))...)
		}
		return out, bbox
	}
	var ends []int
	var pts []Pt
	for _, c := range g.Contours {
		pts = append(pts, c...)
		ends = append(ends, len(pts)-1)
	}
	for _, p := range pts {
		grow(p.X, p.Y)
	}
	out := cat(be16(len(g.Contours)), be16(bbox[0]), be16(bbox[1]), be16(bbox[2]), be16(bbox[3]))
	for _, e := range ends {
		out = append(out, be16(e)...)
	}
	out = append(out, be16(len(g.Instr))...)
	out = append(out, g.Instr...)
	var xs, ys []byte
	px, py := 0, 0
	for _, p := range pts {
		f := 0
		if !p.Off {
			f |= 1
		}
		out = append(out, byte(f)) // coordinates as 16-bit deltas: no short or same flags
		xs = append(xs, be16(p.X-px)...)
		ys = append(ys, be16(p.Y-py)...)
		px, py = p.X, p.Y
	}
	return append(append(out, xs...), ys...), bbox
}

// SFNT serialises the font.
func (f *Font) SFNT() []byte {
	n := len(f.Glyphs)
	var glyf []byte
	offs := make([]int, n+1)
	boxes := make([][4]int, n)
	var gb [4]int
	firstBox := true
	for i, g := range f.Glyphs {
		offs[i] = len(glyf)
		d, bb := glyphBytes(g)
		boxes[i] = bb
		if len(d) > 0 {
			if firstBox {
				gb, firstBox = bb, false
			} else {
				gb[0], gb[1] = min(gb[0], bb[0]), min(gb[1], bb[1])
				gb[2], gb[3] = max(gb[2], bb[2]), max(gb[3], bb[3])
			}
		}
		glyf = append(glyf, pad4(d)...)
	}
	offs[n] = len(glyf)
	short := f.ShortLoca && len(glyf) < 0x20000
	var loca []byte
	for _, o := range offs {
		if short {
			loca = append(loca, be16(o/2)...)
		} else {
			loca = append(loca, be32(uint32(o))...)
		}
	}
	locaFmt := 1
	if short {
		locaFmt = 0
	}
	upm := f.UPM
	if upm == 0 {
		upm = 1000
	}
	mac := 0
	if f.Weight >= 600 {
		mac |= 1
	}
	if f.Italic {
		mac |= 2
	}
	head := cat(be32(0x00010000), be32(0), be32(0), be32(0x5F0F3CF5), be16(0x000B), be16(upm),
		make([]byte, 16), be16(gb[0]), be16(gb[1]), be16(gb[2]), be16(gb[3]), be16(mac), be16(8), be16(2), be16(locaFmt), be16(0))
	maxAdv := 0
	for _, g := range f.Glyphs {
		maxAdv = max(maxAdv, g.Adv)
	}
	hhea := cat(be32(0x00010000), be16(f.Ascent), be16(-f.Descent), be16(f.LineGap), be16(maxAdv), be16(0), be16(0), be16(maxAdv),
		be16(1), be16(0), be16(0), make([]byte, 8), be16(0), be16(n))
	maxPts, maxCont := 0, 0
	for _, g := range f.Glyphs {
		p := 0
		for _, c := range g.Contours {
			p += len(c)
		}
		maxPts, maxCont = max(maxPts, p), max(maxCont, len(g.Contours))
	}
	maxp := cat(be32(0x00010000), be16(n), be16(maxPts), be16(maxCont), be16(0), be16(0), be16(2), be16(0), be16(0), be16(0), be16(0), be16(0), be16(0), be16(4), be16(1))
	var hmtx []byte
	for i, g := range f.Glyphs {
		hmtx = append(hmtx, cat(be16(g.Adv), be16(boxes[i][0]))...)
	}
	asc := f.Ascent
	os2 := cat(be16(4), be16(f.UPM/2), be16(weight(f)), be16(5), be16(0), make([]byte, 20), be16(0), be16(0), make([]byte, 10), make([]byte, 16),
		[]byte("TEST"), be16(fsSel(f)), be16(0x20), be16(0xFFFF), be16(asc), be16(-f.Descent), be16(f.LineGap), be16(asc), be16(f.Descent),
		be32(1), be32(0), be16(f.XHeight), be16(f.Cap), be16(0), be16(0x20), be16(2))
	fixed := 0
	if f.Mono {
		fixed = 1
	}
	post := cat(be32(0x00030000), be32(0), be16(-100), be16(50), be32(uint32(fixed)), make([]byte, 16))
	tables := map[string][]byte{
		"head": head, "hhea": hhea, "maxp": maxp, "hmtx": hmtx, "OS/2": os2, "post": post,
		"cmap": f.cmap(), "name": f.name(), "glyf": glyf, "loca": loca,
	}
	if len(f.Ligatures) > 0 {
		tables["GSUB"] = f.gsub()
	}
	if len(f.Kern) > 0 {
		tables["kern"] = f.kern()
	}
	for k, v := range f.ExtraTables {
		tables[k] = v
	}
	return Assemble(0x00010000, tables)
}

func weight(f *Font) int {
	if f.Weight == 0 {
		return 400
	}
	return f.Weight
}

func fsSel(f *Font) int {
	v := 0x40
	if f.Italic {
		v = 0x01
	}
	if f.Weight >= 600 {
		v |= 0x20
	}
	return v
}

// Assemble builds an sfnt from raw tables (4-byte aligned, correct checksums).
func Assemble(flavor uint32, tables map[string][]byte) []byte {
	tags := make([]string, 0, len(tables))
	for t := range tables {
		tags = append(tags, t)
	}
	sort.Strings(tags)
	n := len(tags)
	out := make([]byte, 12+16*n)
	binary.BigEndian.PutUint32(out, flavor)
	binary.BigEndian.PutUint16(out[4:], uint16(n))
	sr, es := 1, 0
	for sr*2 <= n {
		sr *= 2
		es++
	}
	binary.BigEndian.PutUint16(out[6:], uint16(sr*16))
	binary.BigEndian.PutUint16(out[8:], uint16(es))
	binary.BigEndian.PutUint16(out[10:], uint16(n*16-sr*16))
	headAt := -1
	for i, t := range tags {
		d := tables[t]
		e := out[12+16*i:]
		copy(e, t)
		binary.BigEndian.PutUint32(e[4:], Checksum(d))
		binary.BigEndian.PutUint32(e[8:], uint32(len(out)))
		binary.BigEndian.PutUint32(e[12:], uint32(len(d)))
		if t == "head" {
			headAt = len(out)
		}
		out = append(out, pad4(append([]byte(nil), d...))...)
	}
	if headAt >= 0 {
		binary.BigEndian.PutUint32(out[headAt+8:], 0)
		binary.BigEndian.PutUint32(out[headAt+8:], 0xB1B0AFBA-Checksum(out))
	}
	return out
}

// Checksum is the sfnt table checksum.
func Checksum(b []byte) uint32 {
	var s uint32
	for i := 0; i < len(b); i += 4 {
		var w [4]byte
		copy(w[:], b[i:])
		s += binary.BigEndian.Uint32(w[:])
	}
	return s
}

func (f *Font) cmap() []byte {
	var runes []int
	for r := range f.Cmap {
		if r < 0x10000 {
			runes = append(runes, int(r))
		}
	}
	sort.Ints(runes)
	type seg struct{ start, end, delta int }
	var segs []seg
	for _, r := range runes {
		g := f.Cmap[rune(r)]
		d := (g - r) & 0xffff
		if k := len(segs) - 1; k >= 0 && segs[k].end+1 == r && segs[k].delta == d {
			segs[k].end = r
			continue
		}
		segs = append(segs, seg{r, r, d})
	}
	segs = append(segs, seg{0xFFFF, 0xFFFF, 1})
	sc := len(segs)
	var ends, starts, deltas, ranges []byte
	for _, s := range segs {
		ends = append(ends, be16(s.end)...)
		starts = append(starts, be16(s.start)...)
		deltas = append(deltas, be16(s.delta)...)
		ranges = append(ranges, 0, 0)
	}
	sr, es := 1, 0
	for sr*2 <= sc {
		sr *= 2
		es++
	}
	body := cat(be16(sc*2), be16(sr*2), be16(es), be16(sc*2-sr*2), ends, be16(0), starts, deltas, ranges)
	sub := cat(be16(4), be16(6+len(body)), be16(0), body)
	return cat(be16(0), be16(1), be16(3), be16(1), be32(12), sub)
}

func (f *Font) name() []byte {
	strs := []struct {
		id int
		s  string
	}{{1, f.Family}, {2, f.Style}, {4, f.Family + " " + f.Style}, {6, f.Family + "-" + f.Style}}
	var recs, pool []byte
	for _, s := range strs {
		u := utf16.Encode([]rune(s.s))
		var b []byte
		for _, c := range u {
			b = append(b, be16(int(c))...)
		}
		recs = append(recs, cat(be16(3), be16(1), be16(0x409), be16(s.id), be16(len(b)), be16(len(pool)))...)
		pool = append(pool, b...)
	}
	return cat(be16(0), be16(len(strs)), be16(6+12*len(strs)), recs, pool)
}

// gsub encodes the ligatures as one 'liga' lookup (type 4) under DFLT and latn.
func (f *Font) gsub() []byte {
	byFirst := map[int][]Lig{}
	var firsts []int
	for _, l := range f.Ligatures {
		if _, ok := byFirst[l.Comps[0]]; !ok {
			firsts = append(firsts, l.Comps[0])
		}
		byFirst[l.Comps[0]] = append(byFirst[l.Comps[0]], l)
	}
	sort.Ints(firsts)
	// longest ligatures first, as shaping engines expect
	var sets [][]byte
	for _, fg := range firsts {
		ls := byFirst[fg]
		sort.SliceStable(ls, func(i, j int) bool { return len(ls[i].Comps) > len(ls[j].Comps) })
		var ligTabs [][]byte
		for _, l := range ls {
			t := cat(be16(l.To), be16(len(l.Comps)))
			for _, c := range l.Comps[1:] {
				t = append(t, be16(c)...)
			}
			ligTabs = append(ligTabs, t)
		}
		set := be16(len(ligTabs))
		off := 2 + 2*len(ligTabs)
		for _, t := range ligTabs {
			set = append(set, be16(off)...)
			off += len(t)
		}
		for _, t := range ligTabs {
			set = append(set, t...)
		}
		sets = append(sets, set)
	}
	cov := cat(be16(1), be16(len(firsts)))
	for _, g := range firsts {
		cov = append(cov, be16(g)...)
	}
	sub := cat(be16(1), be16(6+2*len(sets)), be16(len(sets)))
	covOff := 6 + 2*len(sets)
	setOff := covOff + len(cov)
	for _, s := range sets {
		sub = append(sub, be16(setOff)...)
		setOff += len(s)
	}
	// header: version, coverage offset (relative to subtable start)
	sub = cat(be16(1), be16(covOff), be16(len(sets)), sub[6:6+2*len(sets)], cov)
	for _, s := range sets {
		sub = append(sub, s...)
	}
	lookup := cat(be16(4), be16(0), be16(1), be16(8), sub)
	lookupList := cat(be16(1), be16(4), lookup)
	feature := cat(be16(0), be16(1), be16(0))
	featureList := cat(be16(1), []byte("liga"), be16(8), feature)
	langSys := cat(be16(0), be16(0xFFFF), be16(1), be16(0))
	script := cat(be16(4), be16(0), langSys)
	scriptList := cat(be16(2), []byte("DFLT"), be16(14), []byte("latn"), be16(14+len(script)), script, script)
	hdr := 10
	sl := hdr
	fl := sl + len(scriptList)
	ll := fl + len(featureList)
	return cat(be32(0x00010000), be16(sl), be16(fl), be16(ll), scriptList, featureList, lookupList)
}

func (f *Font) kern() []byte {
	type kp struct{ l, r, v int }
	var ps []kp
	for k, v := range f.Kern {
		ps = append(ps, kp{k[0], k[1], v})
	}
	sort.Slice(ps, func(i, j int) bool {
		if ps[i].l != ps[j].l {
			return ps[i].l < ps[j].l
		}
		return ps[i].r < ps[j].r
	})
	sub := cat(be16(0), be16(14+6*len(ps)), be16(1), be16(len(ps)), be16(0), be16(0), be16(0))
	for _, p := range ps {
		sub = append(sub, cat(be16(p.l), be16(p.r), be16(p.v))...)
	}
	return cat(be16(0), be16(1), sub)
}
