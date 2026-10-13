package font

import (
	"math"
	"strconv"
	"strings"
)

// variation.go: TrueType variable fonts (OpenType font variations).
//
// Supported: fvar axes, avar segment maps, gvar glyph variations (shared and
// private tuples, intermediate regions, sparse deltas with inferred deltas
// for untouched points, composite glyphs with component offset deltas) and
// HVAR advance deltas (or, without HVAR, the phantom point deltas of gvar).
// Not supported: CFF2 variable fonts and MVAR/VVAR/cvar (a CFF2 or gvar-less
// font draws its default instance); optical size (opsz) stays at the
// default because faces are instantiated per weight, not per font size.
//
// A WebFace instantiates the font once per requested weight: the weight sets
// the wght axis (clamped to the declared range and the axis), the stretch
// the wdth axis, italic the ital/slnt axes, and the font-variation-settings
// descriptor of the @font-face rule overrides any axis.

const (
	maxAxes       = 64
	maxTuples     = 4095
	maxCompDepth  = 8
	maxCompsTotal = 128
)

type vaxis struct {
	tag           string
	min, def, max float64
}

type variation struct {
	axes []vaxis
	avar [][][2]float64 // per axis: (from, to) normalised pairs
	gvar otTable
	glyf []byte
	loca []byte
	locL bool // long loca
	nGly int
	hmtx []byte
	nHM  int
	upm  int
	hvar *hvarTable

	sharedTuples []float64 // sharedTupleCount * axisCount
	nShared      int
	gvarLong     bool
	gvarArrOff   int
	gvarOffs     int // offset of the glyphVariationDataOffsets array
}

func parseVariation(f *Face) (vr *variation) {
	defer func() {
		if recover() != nil {
			vr = nil
		}
	}()
	fv := findTable(f.data, -1, "fvar")
	gv := findTable(f.data, -1, "gvar")
	glyf := findTable(f.data, -1, "glyf")
	loca := findTable(f.data, -1, "loca")
	head := findTable(f.data, -1, "head")
	hhea := findTable(f.data, -1, "hhea")
	hmtx := findTable(f.data, -1, "hmtx")
	maxp := findTable(f.data, -1, "maxp")
	if fv == nil || gv == nil || glyf == nil || loca == nil || len(head) < 54 || len(hhea) < 36 || len(maxp) < 6 {
		return nil
	}
	t := otTable(fv)
	axOff, nAx, axSize := t.u16(4), t.u16(8), t.u16(10)
	if nAx < 1 || nAx > maxAxes || axSize < 20 || axOff+nAx*axSize > len(fv) {
		return nil
	}
	v := &variation{gvar: gv, glyf: glyf, loca: loca, hmtx: hmtx, upm: f.upm}
	for i := 0; i < nAx; i++ {
		o := axOff + i*axSize
		ax := vaxis{tag: string(fv[o : o+4]),
			min: float64(int32(t.u32(o+4))) / 65536, def: float64(int32(t.u32(o+8))) / 65536, max: float64(int32(t.u32(o+12))) / 65536}
		if ax.min > ax.def || ax.def > ax.max {
			return nil
		}
		v.axes = append(v.axes, ax)
	}
	v.nGly = int(otTable(maxp).u16(4))
	v.locL = otTable(head).u16(50) == 1
	if len(loca) < (v.nGly+1)*map[bool]int{false: 2, true: 4}[v.locL] {
		return nil
	}
	v.nHM = otTable(hhea).u16(34)
	if v.nHM < 1 || v.nHM > v.nGly || len(hmtx) < 4*v.nHM {
		return nil
	}
	// gvar header
	g := otTable(gv)
	if g.u16(0) != 1 || g.u16(4) != nAx {
		return nil
	}
	v.nShared = g.u16(6)
	shOff := g.u32(8)
	if g.u16(12) != v.nGly {
		return nil
	}
	v.gvarLong = g.u16(14)&1 != 0
	v.gvarArrOff = g.u32(16)
	v.gvarOffs = 20
	need := 4 * (v.nGly + 1)
	if !v.gvarLong {
		need = 2 * (v.nGly + 1)
	}
	if v.gvarOffs+need > len(gv) || shOff < 0 || shOff+v.nShared*nAx*2 > len(gv) || v.gvarArrOff > len(gv) {
		return nil
	}
	for i := 0; i < v.nShared*nAx; i++ {
		v.sharedTuples = append(v.sharedTuples, f2dot14(g.i16(shOff+2*i)))
	}
	if av := findTable(f.data, -1, "avar"); len(av) >= 8 {
		a := otTable(av)
		if a.u16(0) == 1 && a.u16(6) == nAx {
			off := 8
			ok := true
			var maps [][][2]float64
			for i := 0; i < nAx && ok; i++ {
				n := a.u16(off)
				if n > 64 || off+2+4*n > len(av) {
					ok = false
					break
				}
				var m [][2]float64
				for k := 0; k < n; k++ {
					m = append(m, [2]float64{f2dot14(a.i16(off + 2 + 4*k)), f2dot14(a.i16(off + 4 + 4*k))})
				}
				maps = append(maps, m)
				off += 2 + 4*n
			}
			if ok {
				v.avar = maps
			}
		}
	}
	if hv := findTable(f.data, -1, "HVAR"); hv != nil {
		v.hvar = parseHVAR(hv, nAx)
	}
	return v
}

func f2dot14(v int) float64 { return float64(v) / 16384 }

func (v *variation) hasWght() bool { return v != nil && len(v.axes) > 0 }

// axisIndex returns the index of the axis with the tag, or -1.
func (v *variation) axisIndex(tag string) int {
	for i, a := range v.axes {
		if a.tag == tag {
			return i
		}
	}
	return -1
}

// coords normalises user axis values (nil entries mean the default) to -1..1.
func (v *variation) coords(user map[string]float64) []float64 {
	out := make([]float64, len(v.axes))
	for i, a := range v.axes {
		val, ok := user[a.tag]
		if !ok {
			continue
		}
		if val < a.min {
			val = a.min
		}
		if val > a.max {
			val = a.max
		}
		switch {
		case val < a.def:
			out[i] = -(a.def - val) / (a.def - a.min)
		case val > a.def:
			out[i] = (val - a.def) / (a.max - a.def)
		}
		if v.avar != nil && i < len(v.avar) {
			out[i] = avarMap(v.avar[i], out[i])
		}
	}
	return out
}

func avarMap(m [][2]float64, c float64) float64 {
	if len(m) < 2 {
		return c
	}
	if c <= m[0][0] {
		return m[0][1] + (c - m[0][0])
	}
	for i := 1; i < len(m); i++ {
		if c <= m[i][0] {
			a, b := m[i-1], m[i]
			if b[0] == a[0] {
				return b[1]
			}
			return a[1] + (c-a[0])*(b[1]-a[1])/(b[0]-a[0])
		}
	}
	last := m[len(m)-1]
	return last[1] + (c - last[0])
}

// userAxes is the axis assignment for a face drawn at the weight, with the
// descriptor's font-variation-settings on top.
func userAxes(d *FaceDesc, weight int) map[string]float64 {
	m := map[string]float64{"wght": float64(weight), "wdth": 100}
	if d.Style == StyleItalic {
		m["ital"] = 1
	} else if d.Style == StyleOblique {
		m["slnt"] = -14
	}
	for _, p := range strings.Split(d.Variations, ",") {
		f := strings.Fields(strings.TrimSpace(p))
		if len(f) != 2 {
			continue
		}
		tag := strings.Trim(f[0], `"'`)
		if len(tag) != 4 {
			continue
		}
		if x, err := strconv.ParseFloat(f[1], 64); err == nil {
			m[tag] = x
		}
	}
	return m
}

// instance returns the face drawn at weight w.
func (v *variation) instance(wf *WebFace, w int) *Face {
	base := wf.base
	coords := v.coords(userAxes(&wf.Desc, w))
	zero := true
	for _, c := range coords {
		if c != 0 {
			zero = false
		}
	}
	nf, err := newFace(base.Family, Style{Weight: w, Italic: base.Style.Italic}, wf.sfnt, false)
	if err != nil {
		return base
	}
	nf.ur = base.ur
	nf.vi = &instanceData{v: v, f: nf, coords: coords, zero: zero, adv: map[int]float64{}, outl: map[int][][]gpt{}}
	return nf
}

// ---- per instance data --------------------------------------------------------------

type gpt struct {
	x, y float64
	on   bool
}

type instanceData struct {
	v      *variation
	f      *Face
	coords []float64
	zero   bool
	adv    map[int]float64
	outl   map[int][][]gpt
}

// advance of glyph gid in font units.
func (d *instanceData) advance(gid int) (a float64) {
	if x, ok := d.adv[gid]; ok {
		return x
	}
	defer func() {
		if recover() != nil {
			a = d.defaultAdvance(gid)
		}
		d.adv[gid] = a
	}()
	a = d.defaultAdvance(gid)
	if d.zero || gid < 0 || gid >= d.v.nGly {
		return a
	}
	if d.v.hvar != nil {
		if delta, ok := d.v.hvar.advanceDelta(gid, d.coords); ok {
			return a + delta
		}
	}
	// phantom points of gvar
	g, err := d.v.rawGlyph(gid)
	if err != nil {
		return a
	}
	dx, _, err := d.v.glyphDeltas(gid, g, d.coords)
	if err != nil || len(dx) < len(g.pts)+2 {
		return a
	}
	n := len(g.pts)
	return a + dx[n+1] - dx[n]
}

func (d *instanceData) defaultAdvance(gid int) float64 {
	h := d.v.hmtx
	i := gid
	if i >= d.v.nHM {
		i = d.v.nHM - 1
	}
	if 4*i+2 > len(h) {
		return 0
	}
	return float64(uint16(h[4*i])<<8 | uint16(h[4*i+1]))
}

func (d *instanceData) lsb(gid int) float64 {
	h := d.v.hmtx
	var o int
	if gid < d.v.nHM {
		o = 4*gid + 2
	} else {
		o = 4*d.v.nHM + 2*(gid-d.v.nHM)
	}
	if o+2 > len(h) {
		return 0
	}
	return float64(int16(uint16(h[o])<<8 | uint16(h[o+1])))
}

// outline returns the glyph's outline at size px as segments (y down).
func (d *instanceData) outline(gid int, size float64) (out []Segment) {
	defer func() {
		if recover() != nil {
			out = nil
		}
	}()
	cs, ok := d.outl[gid]
	if !ok {
		var err error
		cs, err = d.v.glyphContours(d, gid, d.coords, 0)
		if err != nil {
			// a broken variation: the default instance still draws
			if segs, e2 := d.f.font.LoadGlyph(&d.f.buf, glyphIndex(gid), d.f.scale(size), nil); e2 == nil {
				return convertSegments(segs)
			}
			return nil
		}
		if len(d.outl) > 4096 {
			d.outl = map[int][][]gpt{}
		}
		d.outl[gid] = cs
	}
	k := size / float64(d.f.upm)
	q := func(v float64) float64 { return math.Round(v*64) / 64 }
	for _, c := range cs {
		out = appendContour(out, c, k, q)
	}
	return out
}

func appendContour(out []Segment, pts []gpt, k float64, q func(float64) float64) []Segment {
	n := len(pts)
	if n == 0 {
		return out
	}
	P := func(p gpt) (float64, float64) { return q(p.x * k), q(-p.y * k) }
	start := -1
	for i, p := range pts {
		if p.on {
			start = i
			break
		}
	}
	var seq []gpt
	var s gpt
	if start >= 0 {
		s = pts[start]
		seq = append(seq, pts[start+1:]...)
		seq = append(seq, pts[:start]...)
	} else {
		s = gpt{x: (pts[n-1].x + pts[0].x) / 2, y: (pts[n-1].y + pts[0].y) / 2, on: true}
		seq = append(seq, pts...)
	}
	seq = append(seq, s)
	sx, sy := P(s)
	out = append(out, Segment{Op: 'M', X0: sx, Y0: sy})
	var c gpt
	hasC := false
	for _, p := range seq {
		if p.on {
			x, y := P(p)
			if hasC {
				cx, cy := P(c)
				out = append(out, Segment{Op: 'Q', X0: cx, Y0: cy, X1: x, Y1: y})
			} else {
				out = append(out, Segment{Op: 'L', X0: x, Y0: y})
			}
			hasC = false
			continue
		}
		if hasC {
			m := gpt{x: (c.x + p.x) / 2, y: (c.y + p.y) / 2}
			cx, cy := P(c)
			mx, my := P(m)
			out = append(out, Segment{Op: 'Q', X0: cx, Y0: cy, X1: mx, Y1: my})
		}
		c, hasC = p, true
	}
	return out
}
