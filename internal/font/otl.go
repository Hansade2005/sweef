package font

import (
	"encoding/binary"
	"sort"
	"sync"
)

// A small OpenType layout reader: just enough GPOS (pair kerning) and GSUB
// (standard ligatures) to shape Latin text the way HarfBuzz does for Skia.

type otTable []byte

func (t otTable) u16(o int) int {
	if o < 0 || o+2 > len(t) {
		return 0
	}
	return int(binary.BigEndian.Uint16(t[o:]))
}

func (t otTable) i16(o int) int { return int(int16(t.u16(o))) }

func (t otTable) u32(o int) int {
	if o < 0 || o+4 > len(t) {
		return 0
	}
	return int(binary.BigEndian.Uint32(t[o:]))
}

// findTable locates an sfnt table (collection index idx, or -1).
func findTable(data []byte, idx int, tag string) []byte {
	base := 0
	if len(data) >= 16 && string(data[:4]) == "ttcf" {
		if idx < 0 {
			idx = 0
		}
		n := int(binary.BigEndian.Uint32(data[8:]))
		if idx >= n || 12+4*idx+4 > len(data) {
			return nil
		}
		base = int(binary.BigEndian.Uint32(data[12+4*idx:]))
	}
	if base+12 > len(data) {
		return nil
	}
	n := int(binary.BigEndian.Uint16(data[base+4:]))
	for i := 0; i < n; i++ {
		o := base + 12 + 16*i
		if o+16 > len(data) {
			return nil
		}
		if string(data[o:o+4]) == tag {
			off := int(binary.BigEndian.Uint32(data[o+8:]))
			ln := int(binary.BigEndian.Uint32(data[o+12:]))
			if off < 0 || off+ln > len(data) {
				return nil
			}
			return data[off : off+ln]
		}
	}
	return nil
}

// otLayout holds the lookups selected for one table and feature set.
type otLayout struct {
	t       otTable
	lookups []int // lookup offsets (absolute within the table)
}

func newLayout(raw []byte, feats map[string]bool) *otLayout {
	if len(raw) < 10 {
		return nil
	}
	t := otTable(raw)
	scriptList, featureList, lookupList := t.u16(4), t.u16(6), t.u16(8)
	if scriptList == 0 || featureList == 0 || lookupList == 0 {
		return nil
	}
	// pick script: latn, else DFLT, else first
	nScripts := t.u16(scriptList)
	scriptOff := -1
	pick := func(tag string) int {
		for i := 0; i < nScripts; i++ {
			o := scriptList + 2 + 6*i
			if o+6 > len(t) {
				break
			}
			if string(t[o:o+4]) == tag {
				return scriptList + t.u16(o+4)
			}
		}
		return -1
	}
	for _, tag := range []string{"latn", "DFLT"} {
		if scriptOff = pick(tag); scriptOff >= 0 {
			break
		}
	}
	if scriptOff < 0 && nScripts > 0 {
		scriptOff = scriptList + t.u16(scriptList+2+4)
	}
	var featIdx []int
	if scriptOff >= 0 {
		if d := t.u16(scriptOff); d != 0 {
			ls := scriptOff + d
			n := t.u16(ls + 4)
			for i := 0; i < n; i++ {
				featIdx = append(featIdx, t.u16(ls+6+2*i))
			}
		}
	}
	nFeat := t.u16(featureList)
	seen := map[int]bool{}
	var lk []int
	for _, fi := range featIdx {
		if fi >= nFeat {
			continue
		}
		o := featureList + 2 + 6*fi
		if o+6 > len(t) || !feats[string(t[o:o+4])] {
			continue
		}
		fo := featureList + t.u16(o+4)
		n := t.u16(fo + 2)
		for i := 0; i < n; i++ {
			li := t.u16(fo + 4 + 2*i)
			if !seen[li] {
				seen[li] = true
				lk = append(lk, li)
			}
		}
	}
	sort.Ints(lk)
	nLookups := t.u16(lookupList)
	out := &otLayout{t: t}
	for _, li := range lk {
		if li >= nLookups {
			continue
		}
		out.lookups = append(out.lookups, lookupList+t.u16(lookupList+2+2*li))
	}
	if len(out.lookups) == 0 {
		return nil
	}
	return out
}

// subtables returns (type, absolute offset) of each subtable of a lookup,
// resolving extension lookups.
func (l *otLayout) subtables(lo int) (typ int, subs []int) {
	t := l.t
	lt := t.u16(lo)
	typ = lt
	n := t.u16(lo + 4)
	for i := 0; i < n; i++ {
		so := lo + t.u16(lo+6+2*i)
		if lt == 9 || lt == 7 { // extension
			if t.u16(so) != 1 {
				continue
			}
			typ = t.u16(so + 2)
			so += t.u32(so + 4)
		}
		subs = append(subs, so)
	}
	return
}

func (t otTable) coverage(o int, g int) int {
	switch t.u16(o) {
	case 1:
		n := t.u16(o + 2)
		i := sort.Search(n, func(i int) bool { return t.u16(o+4+2*i) >= g })
		if i < n && t.u16(o+4+2*i) == g {
			return i
		}
	case 2:
		n := t.u16(o + 2)
		i := sort.Search(n, func(i int) bool { return t.u16(o+4+6*i+2) >= g })
		if i < n {
			r := o + 4 + 6*i
			if t.u16(r) <= g {
				return t.u16(r+4) + g - t.u16(r)
			}
		}
	}
	return -1
}

func (t otTable) classOf(o int, g int) int {
	switch t.u16(o) {
	case 1:
		start, n := t.u16(o+2), t.u16(o+4)
		if g >= start && g < start+n {
			return t.u16(o + 6 + 2*(g-start))
		}
	case 2:
		n := t.u16(o + 2)
		i := sort.Search(n, func(i int) bool { return t.u16(o+4+6*i+2) >= g })
		if i < n {
			r := o + 4 + 6*i
			if t.u16(r) <= g {
				return t.u16(r + 4)
			}
		}
	}
	return 0
}

func vrSize(f int) int {
	n := 0
	for ; f != 0; f &= f - 1 {
		n++
	}
	return n * 2
}

// vrAdvance reads XAdvance from a value record at o with format f.
func (t otTable) vrAdvance(o, f int) int {
	if f&4 == 0 {
		return 0
	}
	skip := 0
	if f&1 != 0 {
		skip += 2
	}
	if f&2 != 0 {
		skip += 2
	}
	return t.i16(o + skip)
}

// pairAdjust returns the summed XAdvance adjustment of the first glyph for
// the pair (g1, g2) in font units.
func (l *otLayout) pairAdjust(g1, g2 int) int {
	t := l.t
	total := 0
	for _, lo := range l.lookups {
		typ, subs := l.subtables(lo)
		if typ != 2 {
			continue
		}
		for _, so := range subs {
			cov := so + t.u16(so+2)
			ci := t.coverage(cov, g1)
			if ci < 0 {
				continue
			}
			vf1, vf2 := t.u16(so+4), t.u16(so+6)
			s1, s2 := vrSize(vf1), vrSize(vf2)
			if t.u16(so) == 1 {
				if ci >= t.u16(so+8) {
					continue
				}
				ps := so + t.u16(so+10+2*ci)
				n := t.u16(ps)
				rec := 2 + s1 + s2
				i := sort.Search(n, func(i int) bool { return t.u16(ps+2+rec*i) >= g2 })
				if i < n && t.u16(ps+2+rec*i) == g2 {
					total += t.vrAdvance(ps+2+rec*i+2, vf1)
					break
				}
			} else {
				cd1, cd2 := so+t.u16(so+8), so+t.u16(so+10)
				c1n, c2n := t.u16(so+12), t.u16(so+14)
				c1, c2 := t.classOf(cd1, g1), t.classOf(cd2, g2)
				if c1 >= c1n || c2 >= c2n {
					continue
				}
				rec := s1 + s2
				o := so + 16 + (c1*c2n+c2)*rec
				total += t.vrAdvance(o, vf1)
				break
			}
		}
	}
	return total
}

// ligature finds the longest standard ligature starting at gs[0]; it returns
// the ligature glyph and the number of glyphs consumed (0 if none).
func (l *otLayout) ligature(gs []int) (int, int) {
	t := l.t
	for _, lo := range l.lookups {
		typ, subs := l.subtables(lo)
		if typ != 4 {
			continue
		}
		for _, so := range subs {
			ci := t.coverage(so+t.u16(so+2), gs[0])
			if ci < 0 || ci >= t.u16(so+4) {
				continue
			}
			ls := so + t.u16(so+6+2*ci)
			n := t.u16(ls)
			for i := 0; i < n; i++ {
				lg := ls + t.u16(ls+2+2*i)
				glyph, cc := t.u16(lg), t.u16(lg+2)
				if cc < 2 || cc > len(gs) {
					continue
				}
				ok := true
				for k := 1; k < cc; k++ {
					if t.u16(lg+4+2*(k-1)) != gs[k] {
						ok = false
						break
					}
				}
				if ok {
					return glyph, cc
				}
			}
		}
	}
	return 0, 0
}

type otCache struct {
	once        sync.Once
	kern        *otLayout
	liga        *otLayout
	hasGPOSKern bool
}

func (f *Face) loadOT() *otCache {
	f.ot.once.Do(func() {
		if g := findTable(f.data, f.idx, "GPOS"); g != nil {
			f.ot.kern = newLayout(g, map[string]bool{"kern": true})
		}
		if g := findTable(f.data, f.idx, "GSUB"); g != nil {
			f.ot.liga = newLayout(g, map[string]bool{"liga": true, "clig": true, "rlig": true})
		}
	})
	return &f.ot
}
