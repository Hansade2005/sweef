package raster

import (
	"encoding/binary"
	"sync"

	"github.com/sweefbrowser/sweef/internal/font"
)

// FreeType hands Skia the outline of a TrueType glyph as 26.6 integer
// points: every point is scaled on its own (FT_MulFix), skewed for synthetic
// italic, and only then are the on-curve points implied between two
// consecutive off-curve points formed as integer midpoints
// (FT_Outline_Decompose). The font package only exposes the decomposed
// segments with the implied points already rounded in font units, and cannot
// tell an explicit on-curve point that happens to sit at the midpoint from an
// implied one, so the raw 'glyf' data is read here.

type glyfPt struct {
	x, y int
	on   bool
}

type glyfFont struct {
	glyf, loca []byte
	long       bool
	n          int
}

var (
	gfMu    sync.Mutex
	gfCache = map[*font.Face]*glyfFont{}
)

// faceBytes returns the sfnt bytes a face reads from.
func faceBytes(f *font.Face) []byte { return f.Data() }

func glyfFor(f *font.Face) *glyfFont {
	if f.Varied() {
		// The raw glyf table holds the default instance only; a variable font drawn at another
		// weight or optical size must use the font's own outlines, which apply the gvar deltas.
		// Without this every variable web font painted at its default weight (Fraunces: Black).
		return nil
	}
	gfMu.Lock()
	defer gfMu.Unlock()
	if g, ok := gfCache[f]; ok {
		return g
	}
	g := parseGlyf(faceBytes(f))
	gfCache[f] = g
	return g
}

func parseGlyf(d []byte) *glyfFont {
	if len(d) < 12 {
		return nil
	}
	nt := int(binary.BigEndian.Uint16(d[4:]))
	tab := map[string][]byte{}
	for i := 0; i < nt; i++ {
		o := 12 + 16*i
		if o+16 > len(d) {
			return nil
		}
		tag := string(d[o : o+4])
		off, ln := int(binary.BigEndian.Uint32(d[o+8:])), int(binary.BigEndian.Uint32(d[o+12:]))
		if off < 0 || ln < 0 || off+ln > len(d) {
			return nil
		}
		tab[tag] = d[off : off+ln]
	}
	head, maxp, loca, glyf := tab["head"], tab["maxp"], tab["loca"], tab["glyf"]
	if len(head) < 54 || len(maxp) < 6 || loca == nil || glyf == nil {
		return nil
	}
	return &glyfFont{glyf: glyf, loca: loca, long: binary.BigEndian.Uint16(head[50:]) != 0, n: int(binary.BigEndian.Uint16(maxp[4:]))}
}

func (g *glyfFont) data(gid int) []byte {
	if gid < 0 || gid >= g.n {
		return nil
	}
	var a, b int
	if g.long {
		if (gid+2)*4 > len(g.loca) {
			return nil
		}
		a, b = int(binary.BigEndian.Uint32(g.loca[gid*4:])), int(binary.BigEndian.Uint32(g.loca[gid*4+4:]))
	} else {
		if (gid+2)*2 > len(g.loca) {
			return nil
		}
		a, b = int(binary.BigEndian.Uint16(g.loca[gid*2:]))*2, int(binary.BigEndian.Uint16(g.loca[gid*2+2:]))*2
	}
	if a >= b || b > len(g.glyf) {
		return nil
	}
	return g.glyf[a:b]
}

// contours returns the glyph's contours scaled to 26.6 (y up), every point on
// its own as FreeType does, following
// composite glyphs whose components are placed by offsets. ok is false for
// anything else (point matching, malformed data).
func (g *glyfFont) contours(gid, depth, sx, sy int) ([][]glyfPt, bool) {
	if depth > 6 {
		return nil, false
	}
	d := g.data(gid)
	if d == nil {
		return nil, true // empty glyph
	}
	if len(d) < 10 {
		return nil, false
	}
	nc := int(int16(binary.BigEndian.Uint16(d)))
	p := 10
	if nc >= 0 {
		return g.simple(d, nc, p, sx, sy)
	}
	// composite
	var out [][]glyfPt
	for {
		if p+4 > len(d) {
			return nil, false
		}
		flags := int(binary.BigEndian.Uint16(d[p:]))
		cg := int(binary.BigEndian.Uint16(d[p+2:]))
		p += 4
		var a1, a2 int
		if flags&1 != 0 {
			if p+4 > len(d) {
				return nil, false
			}
			a1, a2 = int(int16(binary.BigEndian.Uint16(d[p:]))), int(int16(binary.BigEndian.Uint16(d[p+2:])))
			p += 4
		} else {
			if p+2 > len(d) {
				return nil, false
			}
			a1, a2 = int(int8(d[p])), int(int8(d[p+1]))
			p += 2
		}
		if flags&2 == 0 {
			return nil, false // point matching
		}
		xx, xy, yx, yy := 0x10000, 0, 0, 0x10000
		f14 := func(o int) int { return int(int16(binary.BigEndian.Uint16(d[o:]))) << 2 }
		switch {
		case flags&0x8 != 0:
			if p+2 > len(d) {
				return nil, false
			}
			xx = f14(p)
			yy = xx
			p += 2
		case flags&0x40 != 0:
			if p+4 > len(d) {
				return nil, false
			}
			xx, yy = f14(p), f14(p+2)
			p += 4
		case flags&0x80 != 0:
			if p+8 > len(d) {
				return nil, false
			}
			xx, xy, yx, yy = f14(p), f14(p+2), f14(p+4), f14(p+6)
			p += 8
		}
		if flags&0x800 != 0 && (xx != 0x10000 || yy != 0x10000 || xy != 0 || yx != 0) {
			return nil, false // scaled component offsets: not handled
		}
		comp, ok := g.contours(cg, depth+1, sx, sy)
		if !ok {
			return nil, false
		}
		ox, oy := ftMulFix(a1, sx), ftMulFix(a2, sy)
		for _, c := range comp {
			nc := make([]glyfPt, len(c))
			for i, pt := range c {
				x, y := pt.x, pt.y
				if xx != 0x10000 || yy != 0x10000 || xy != 0 || yx != 0 {
					x, y = ftMulFix(pt.x, xx)+ftMulFix(pt.y, xy), ftMulFix(pt.x, yx)+ftMulFix(pt.y, yy)
				}
				nc[i] = glyfPt{x + ox, y + oy, pt.on}
			}
			out = append(out, nc)
		}
		if flags&0x20 == 0 {
			break
		}
	}
	return out, true
}

func (g *glyfFont) simple(d []byte, nc, p, sx, sy int) ([][]glyfPt, bool) {
	if nc == 0 {
		return nil, true
	}
	if p+2*nc+2 > len(d) {
		return nil, false
	}
	ends := make([]int, nc)
	for i := range ends {
		ends[i] = int(binary.BigEndian.Uint16(d[p+2*i:]))
	}
	p += 2 * nc
	n := ends[nc-1] + 1
	il := int(binary.BigEndian.Uint16(d[p:]))
	p += 2 + il
	flags := make([]byte, 0, n)
	for len(flags) < n {
		if p >= len(d) {
			return nil, false
		}
		f := d[p]
		p++
		flags = append(flags, f)
		if f&8 != 0 {
			if p >= len(d) {
				return nil, false
			}
			for r := int(d[p]); r > 0 && len(flags) < n; r-- {
				flags = append(flags, f)
			}
			p++
		}
	}
	xs, ys := make([]int, n), make([]int, n)
	read := func(out []int, short, same byte) bool {
		v := 0
		for i, f := range flags {
			switch {
			case f&short != 0:
				if p >= len(d) {
					return false
				}
				dv := int(d[p])
				p++
				if f&same == 0 {
					dv = -dv
				}
				v += dv
			case f&same == 0:
				if p+2 > len(d) {
					return false
				}
				v += int(int16(binary.BigEndian.Uint16(d[p:])))
				p += 2
			}
			out[i] = v
		}
		return true
	}
	if !read(xs, 2, 16) || !read(ys, 4, 32) {
		return nil, false
	}
	var out [][]glyfPt
	start := 0
	for _, e := range ends {
		if e < start || e >= n {
			return nil, false
		}
		c := make([]glyfPt, 0, e-start+1)
		for i := start; i <= e; i++ {
			c = append(c, glyfPt{ftMulFix(xs[i], sx), ftMulFix(ys[i], sy), flags[i]&1 != 0})
		}
		out = append(out, c)
		start = e + 1
	}
	return out, true
}

// fxGlyphPathTT is fxGlyphPath for TrueType outlines; ok is false when the
// glyph cannot be read this way.
func fxGlyphPathTT(f *font.Face, gid int, scaleX, scaleY, skewFix, sub int) (*Path, bool) {
	return fxGlyphPathTTM(f, gid, scaleX, scaleY, skewFix, fxMat{}, sub*16, 0)
}

// fxMat is the 16.16 matrix FreeType applies to a scaled outline
// (FT_Set_Transform), in FreeType's y-up space. The zero value is identity.
type fxMat struct{ xx, xy, yx, yy int }

func (m fxMat) identity() bool { return m == fxMat{} }

// fxGlyphPathTTM is fxGlyphPathTT with an additional FT_Outline_Transform
// matrix; sub and subY are the subpixel offsets in 26.6 units (device down for subY).
func fxGlyphPathTTM(f *font.Face, gid int, scaleX, scaleY, skewFix int, m fxMat, sub, subY int) (*Path, bool) {
	cs, ok := fxContoursTT(f, gid, scaleX, scaleY, skewFix, m)
	if !ok {
		return nil, false
	}
	p := NewPath()
	for _, c := range cs {
		fxDecompose(p, c, sub, subY)
	}
	return p, true
}

// fxContoursTT returns the contours of a TrueType glyph as FreeType's loader
// leaves them: scaled to 26.6, skewed, transformed by m (y up).
func fxContoursTT(f *font.Face, gid int, scaleX, scaleY, skewFix int, m fxMat) ([][]glyfPt, bool) {
	g := glyfFor(f)
	if g == nil {
		return nil, false
	}
	cs, ok := g.contours(gid, 0, scaleX, scaleY)
	if !ok || len(cs) == 0 {
		return nil, false
	}
	for _, c := range cs {
		if skewFix != 0 {
			// the skew is defined for y down: x' = x - 0.25*y(down) = x + 0.25*y(up)
			for i := range c {
				c[i].x += ftMulFix(c[i].y, -skewFix)
			}
		}
		if !m.identity() {
			for i := range c {
				x, y := c[i].x, c[i].y
				c[i].x = ftMulFix(x, m.xx) + ftMulFix(y, m.xy)
				c[i].y = ftMulFix(x, m.yx) + ftMulFix(y, m.yy)
			}
		}
	}
	return cs, true
}

// fxDecompose mirrors FT_Outline_Decompose for one contour of conic points.
func fxDecompose(p *Path, pts []glyfPt, sub, subY int) {
	n := len(pts)
	if n == 0 {
		return
	}
	out := func(x, y int) (float64, float64) { return float64(x+sub) / 64, -float64(y-subY) / 64 }
	mid := func(a, b glyfPt) glyfPt { return glyfPt{(a.x + b.x) / 2, (a.y + b.y) / 2, true} }
	first, last := 0, n-1
	start := pts[first]
	limit := last
	idx := first
	if !pts[first].on {
		if pts[last].on {
			start = pts[last]
			limit--
		} else {
			start = mid(pts[first], pts[last])
		}
		idx-- // the loop below starts by advancing onto the first point
	}
	x, y := out(start.x, start.y)
	p.MoveTo(x, y)
	for idx < limit {
		idx++
		cur := pts[idx]
		if cur.on {
			x, y := out(cur.x, cur.y)
			p.LineTo(x, y)
			continue
		}
		ctrl := cur
		for {
			if idx < limit {
				idx++
				nxt := pts[idx]
				if nxt.on {
					cx, cy := out(ctrl.x, ctrl.y)
					ex, ey := out(nxt.x, nxt.y)
					p.QuadTo(cx, cy, ex, ey)
					break
				}
				m := mid(ctrl, nxt)
				cx, cy := out(ctrl.x, ctrl.y)
				ex, ey := out(m.x, m.y)
				p.QuadTo(cx, cy, ex, ey)
				ctrl = nxt
				continue
			}
			cx, cy := out(ctrl.x, ctrl.y)
			ex, ey := out(start.x, start.y)
			p.QuadTo(cx, cy, ex, ey)
			break
		}
	}
	p.Close()
}
