package raster

import (
	"fmt"
	"image/png"
	"os"
	"strings"
	"testing"

	"github.com/sweefbrowser/sweef/internal/font"
)

func TestDbgFx(t *testing.T) {
	spec := os.Getenv("DBG_SPEC")
	if spec == "" {
		t.Skip()
	}
	fh, _ := os.Open("testdata/glyphlab/ref.png")
	ref, _ := png.Decode(fh)
	fh.Close()
	p := strings.Split(spec, "/")
	var fi, si, gi int
	for i, f := range labFaces {
		if f.name == p[0] {
			fi = i
		}
	}
	var sz int
	fmt.Sscan(p[1], &sz)
	for i, s := range labSizes {
		if s == sz {
			si = i
		}
	}
	for i, g := range labGlyphs {
		if string(g) == p[2] {
			gi = i
		}
	}
	f := labFaces[fi]
	face := font.Lookup(f.family, font.Style{Weight: f.weight, Italic: f.italic})
	st := labCompare(ref, f, fi, si, gi, face)
	top := labRowTop(fi, si)
	x0 := 8 + gi*labPitch - 4
	lut := lutForLum(0)
	type qi struct{ shift, y0, y2, wind, qy int }
	infos := map[int]qi{}
	fxQInfo = func(idx, shift, y0, y2, wind, qy int) { infos[idx] = qi{shift, y0, y2, wind, qy} }
	defer func() { fxQInfo = nil; fxQuadOverride = 0 }()
	errFor := func(ov int) int {
		fxQuadOverride = ov
		fxQuadIdx = 0
		m := skiaGlyph(Glyph{R: labGlyphs[gi], Face: face}, float64(sz), float64(sz), 0, f.italic && !face.Style.Italic)
		sum := 0
		for y := m.B.Y0; y < m.B.Y1; y++ {
			for x := m.B.X0; x < m.B.X1; x++ {
				ours := 255 - int(lut[m.At(x, y)])
				r := labGray(ref, x0+4+st.dx+x, top+st.dy+y)
				d := ours - r
				if d < 0 {
					d = -d
				}
				sum += d
			}
		}
		return sum
	}
	if os.Getenv("DBG_TRACE") != "" {
		fxVTrace = func(idx, count int, nx, ny, sx, sy float64) {
			fmt.Printf("  q%d count %d new(%.3f,%.3f) snapped(%.3f,%.3f)\n", idx, count, nx, ny, sx, sy)
		}
		defer func() { fxVTrace = nil }()
		errFor(0)
		fmt.Println("--- override")
		var ov int
		fmt.Sscan(os.Getenv("DBG_TRACE"), &ov)
		errFor(ov)
		return
	}
	if v := os.Getenv("DBG_FINE"); v != "" {
		var a, b int
		fmt.Sscan(v, &a, &b)
		fxShiftOv = func(i int) int {
			if i == a || i == b {
				return 1
			}
			return 0
		}
		defer func() { fxShiftOv = nil }()
		fmt.Println("with fine", errFor(0))
		fxQuadIdx = 0
		m := skiaGlyph(Glyph{R: labGlyphs[gi], Face: face}, float64(sz), float64(sz), 0, f.italic && !face.Style.Italic)
		for y := m.B.Y0; y < m.B.Y1; y++ {
			for x := m.B.X0; x < m.B.X1; x++ {
				d := 255 - int(lut[m.At(x, y)]) - labGray(ref, x0+4+st.dx+x, top+st.dy+y)
				if d == 0 {
					fmt.Print("   .")
				} else {
					fmt.Printf("%4d", d)
				}
			}
			fmt.Println()
		}
	}
	if v := os.Getenv("DBG_EXACT"); v != "" {
		for i := 1; i <= 40; i++ {
			fxExactSet = map[int]bool{i: true}
			e := errFor(0)
			fmt.Println("exact quad", i, e)
		}
		fxExactSet = map[int]bool{}
	}
	if v := os.Getenv("DBG_MID"); v != "" {
		for _, y := range []float64{11.5, 11.75, 12.0, 12.25} {
			fxMidOv = map[[2]int]float64{{12, 1}: y, {13, 1}: y}
			fmt.Println("mid", y, errFor(0))
		}
		fxMidOv = map[[2]int]float64{}
	}
	if os.Getenv("DBG_FIT") != "" {
		// vertex trace to learn true/snapped values
		type vv struct{ ny, sy float64 }
		vt := map[[2]int]vv{}
		fxVTrace = func(idx, count int, nx, ny, sx, sy float64) { vt[[2]int{idx, count}] = vv{ny, sy} }
		errFor(0)
		fxVTrace = nil
		cur := errFor(0)
		for pass := 0; pass < 3; pass++ {
			for key, v := range vt {
				if key[1] == 0 {
					continue
				}
				best, bestY := cur, v.sy
				for _, y := range []float64{v.sy - 0.5, v.sy - 0.25, v.sy + 0.25, v.sy + 0.5, v.sy + 0.75, v.sy + 1} {
					old, had := fxMidOv[key]
					fxMidOv[key] = y
					e := errFor(0)
					if e < best {
						best, bestY = e, y
					}
					if had {
						fxMidOv[key] = old
					} else {
						delete(fxMidOv, key)
					}
				}
				if bestY != v.sy {
					fxMidOv[key] = bestY
					cur = best
				}
			}
		}
		fmt.Println("fit err", cur)
		for key, v := range vt {
			if y, ok := fxMidOv[key]; ok {
				fmt.Printf("quad %d count %d: trueY %.3f snapped %.3f fitted %.3f\n", key[0], key[1], v.ny, v.sy, y)
			}
		}
		return
	}
	base := errFor(0)
	n := fxQuadIdx
	fmt.Printf("%s base err %d quads %d\n", spec, base, n)
	for i := 1; i <= n; i++ {
		e := errFor(i)
		if e != base {
			q := infos[i]
			fmt.Printf("quad %d: err %d (%+d) shift %d y %.3f..%.3f wind %d snapoff %.3f\n", i, e, e-base, q.shift, float64(q.y0)/256, float64(q.y2)/256, q.wind, float64(q.qy>>2)/65536*4-float64(q.y0)/256*1)
		}
	}
}

func TestDbgRows(t *testing.T) {
	spec := os.Getenv("DBG_SPEC")
	if spec == "" {
		t.Skip()
	}
	fh, _ := os.Open("testdata/glyphlab/ref.png")
	ref, _ := png.Decode(fh)
	fh.Close()
	p := strings.Split(spec, "/")
	var fi, si, gi int
	for i, f := range labFaces {
		if f.name == p[0] {
			fi = i
		}
	}
	var sz int
	fmt.Sscan(p[1], &sz)
	for i, s := range labSizes {
		if s == sz {
			si = i
		}
	}
	for i, g := range labGlyphs {
		if string(g) == p[2] {
			gi = i
		}
	}
	f := labFaces[fi]
	face := font.Lookup(f.family, font.Style{Weight: f.weight, Italic: f.italic})
	st := labCompare(ref, f, fi, si, gi, face)
	top := labRowTop(fi, si)
	x0 := 8 + gi*labPitch - 4
	m1 := skiaGlyph(Glyph{R: labGlyphs[gi], Face: face}, float64(sz), float64(sz), 0, false)
	textFxMode = 2
	m2 := skiaGlyph(Glyph{R: labGlyphs[gi], Face: face}, float64(sz), float64(sz), 0, false)
	textFxMode = 0
	lut := lutForLum(0)
	inv := func(v int) int { // undo the preblend roughly
		for i := 0; i < 256; i++ {
			if int(lut[i]) >= v {
				return i
			}
		}
		return 255
	}
	fmt.Println("row  ref(raw)  snapped  exact   [coverage sums, pre-LUT for ref via inverse]")
	for y := m1.B.Y0; y < m1.B.Y1; y++ {
		sr, s1, s2 := 0, 0, 0
		for x := m1.B.X0 - 1; x < m1.B.X1+1; x++ {
			sr += inv(255 - labGray(ref, x0+4+st.dx+x, top+st.dy+y))
			s1 += int(m1.At(x, y))
			s2 += int(m2.At(x, y))
		}
		fmt.Printf("%3d %6d %6d %6d\n", y, sr, s1, s2)
	}
}

func TestDbgWalk(t *testing.T) {
	spec := os.Getenv("DBG_SPEC")
	if spec == "" {
		t.Skip()
	}
	p := strings.Split(spec, "/")
	var fi, gi int
	for i, f := range labFaces {
		if f.name == p[0] {
			fi = i
		}
	}
	var sz int
	fmt.Sscan(p[1], &sz)
	for i, g := range labGlyphs {
		if string(g) == p[2] {
			gi = i
		}
	}
	f := labFaces[fi]
	face := font.Lookup(f.family, font.Style{Weight: f.weight, Italic: f.italic})
	fxWalkTrace = func(ya, yb, ul, ur, ll, lr int) {
		if float64(yb)/65536 > float64(os.Getenv("DBG_Y0")[0]-'0')*0+0 && ll >= lr-4 {
			fmt.Printf("y %.3f..%.3f ul %.4f ur %.4f ll %.4f lr %.4f  cross=%v (%d)\n", float64(ya)/65536, float64(yb)/65536, float64(ul)/65536, float64(ur)/65536, float64(ll)/65536, float64(lr)/65536, ll > lr, ll-lr)
		}
	}
	defer func() { fxWalkTrace = nil }()
	skiaGlyph(Glyph{R: labGlyphs[gi], Face: face}, float64(sz), float64(sz), 0, false)
}
