package raster

import (
	"fmt"
	"image"
	"math"
	"os"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/sweefbrowser/sweef/internal/font"
	"github.com/sweefbrowser/sweef/internal/render"
)

// incremental.go: repainting only what changed between two display lists.
//
// A Canvas keeps the pixels of the last paint together with an analysis of the
// display list that produced them (ListInfo: a content+context key and a
// device-space bounding box for every op). Update compares the new list with
// it, derives the rectangle whose pixels can differ and repaints just that
// rectangle: it is cleared and every op that reaches it is executed again, in
// order, under a clip. Because the repaint runs the very same painter code on
// the very same ops, the pixels equal those of a full paint (the golden tests
// in internal/engine compare them byte for byte).
//
// What makes that sound:
//   - an op whose key (content + enclosing pushes) is unchanged paints the same
//     pixels over the same backdrop; if the backdrop changed, that is inside the
//     damaged rectangle, which is repainted entirely, bottom to top;
//   - compositing is per pixel, except for the ops marked "wide" (spatial
//     filters, SVG scenes, blurred text shadows): when one touches the damage,
//     the damage grows to the op's whole bounds;
//   - any op the analysis does not understand (live bitmaps, backdrop filters,
//     3D layers, clip paths) disables the incremental path for that list.

// ListInfo is the analysis of one display list.
type ListInfo struct {
	OK     bool
	Key    []uint64     // content + context key per op
	Bounds []IRect      // device bounds (a group's bounds for push ops)
	Pair   []int32      // push -> matching pop index (and pop -> push), else -1
	Wide   []bool       // damage touching this op / group must grow to its bounds
	Desc   []string     // op type names (SWEEF_NARROW_DEBUG)
	Exact  []bool       // painting the op under a horizontal clip gives the same pixels as unclipped (see narrow)
	Corner []cornerSpan // like Exact, unless the clip edge cuts the curved ends of the op (see cornerZone)
	Pre    []uint64     // Pre[i] hashes Key[:i]: equal prefixes paint equal intermediate surfaces
	Surf   IRect
}

type groupRec struct {
	idx    int
	acc    IRect
	limit  IRect
	hasLim bool
	wide   bool
	reach  int    // filter reach in device px (wide layers)
	ctx    uint64 // context outside the group
	ctm    Matrix // transform outside the group
	inner  IRect  // lower bound of the clip outside the group (see innerDev)
}

// innerDev is a rectangle guaranteed to lie inside the device image of r under
// m (empty when m is not axis aligned, where the image is not a rectangle).
func innerDev(m Matrix, r render.Rect, surf IRect, layer bool) IRect {
	if m[1] != 0 || m[2] != 0 {
		return IRect{}
	}
	x0, y0 := m.Apply(r.X, r.Y)
	x1, y1 := m.Apply(r.X+r.W, r.Y+r.H)
	if x0 > x1 {
		x0, x1 = x1, x0
	}
	if y0 > y1 {
		y0, y1 = y1, y0
	}
	const lim = 1e7
	if x0 != x0 || y0 != y0 || x1 != x1 || y1 != y1 || x0 < -lim || y0 < -lim || x1 > lim || y1 > lim {
		return IRect{}
	}
	if layer {
		// layers use their bounds as they are, rounded outwards (no snapping)
		return IRect{int(math.Floor(x0)), int(math.Floor(y0)), int(math.Ceil(x1)), int(math.Ceil(y1))}.Intersect(surf)
	}
	// clips are snapped to the pixel grid first: stay one pixel inside
	return IRect{int(math.Ceil(x0)) + 1, int(math.Ceil(y0)) + 1, int(math.Floor(x1)) - 1, int(math.Floor(y1)) - 1}.Intersect(surf)
}

const damagePad = 2 // device px: snapping and antialiasing slack around every op

func mix(a, b uint64) uint64 {
	v := (a ^ b) * 0x9E3779B97F4A7C15
	v ^= v >> 29
	return v * 0xBF58476D1CE4E5B9
}

func (r IRect) outset(n int) IRect { return IRect{r.X0 - n, r.Y0 - n, r.X1 + n, r.Y1 + n} }

func (r IRect) union(o IRect) IRect {
	if r.Empty() {
		return o
	}
	if o.Empty() {
		return r
	}
	return IRect{min(r.X0, o.X0), min(r.Y0, o.Y0), max(r.X1, o.X1), max(r.Y1, o.Y1)}
}

// devRect maps a local CSS rect through m to a conservative device rect.
func devRect(m Matrix, r render.Rect, surf IRect) IRect {
	if r.W != r.W || r.H != r.H || r.X != r.X || r.Y != r.Y {
		return surf
	}
	xs := [4]float64{r.X, r.X + r.W, r.X, r.X + r.W}
	ys := [4]float64{r.Y, r.Y, r.Y + r.H, r.Y + r.H}
	x0, y0, x1, y1 := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for i := 0; i < 4; i++ {
		x, y := m.Apply(xs[i], ys[i])
		x0, x1 = math.Min(x0, x), math.Max(x1, x)
		y0, y1 = math.Min(y0, y), math.Max(y1, y)
	}
	const lim = 1e7
	if x0 < -lim || y0 < -lim || x1 > lim || y1 > lim || x0 != x0 || y0 != y0 {
		return surf
	}
	return IRect{int(math.Floor(x0)), int(math.Floor(y0)), int(math.Ceil(x1)), int(math.Ceil(y1))}.outset(damagePad).Intersect(surf)
}

func filterReach(fs []render.Filter) (px float64, ok bool) {
	for _, f := range fs {
		switch f.Name {
		case "blur":
			if len(f.Args) > 0 {
				px += 3*math.Abs(f.Args[0]) + 2
			}
		case "drop-shadow":
			if len(f.Args) >= 2 {
				px += math.Abs(f.Args[0]) + math.Abs(f.Args[1]) + 2
			}
			if len(f.Args) >= 3 {
				px += 3 * math.Abs(f.Args[2])
			}
		case "brightness", "contrast", "grayscale", "hue-rotate", "invert", "opacity", "saturate", "sepia":
		default:
			return 0, false // url(#svg-filter) and anything unknown
		}
	}
	return px, true
}

// textBounds is a generous box around everything a text run can paint: the
// advance box, taller than the fonts' ascent and descent by a quarter em (tall
// glyphs, accents, decorations), and wider by an em either side (italic skew,
// bearings).
func textBounds(o render.TextRun) render.Rect {
	asc, desc := o.Size*0.95, o.Size*0.3
	var last *font.Face
	for _, g := range o.Glyphs {
		if g.Face == nil || g.Face == last {
			continue
		}
		last = g.Face
		m := g.Face.Metrics(o.Size)
		asc, desc = math.Max(asc, m.Ascent), math.Max(desc, m.Descent)
	}
	padY := 0.3*o.Size + 2 + o.Stroke
	padX := o.Size + o.Stroke
	for _, s := range o.Shadows {
		e := math.Abs(s.DX) + math.Abs(s.DY) + 1.5*s.Blur + 2
		padX, padY = math.Max(padX, o.Size+e), math.Max(padY, 0.3*o.Size+2+e)
	}
	top, bot := o.Y-asc-padY, o.Y+desc+padY+math.Abs(o.UnderOffset)
	return render.Rect{X: o.X - padX, Y: top, W: o.Width + 2*padX, H: bot - top}
}

func shadowBounds(o render.BoxShadow) render.Rect {
	if o.Inset {
		return o.Rect
	}
	e := 1.5*o.Blur + 3
	return render.Rect{X: o.Rect.X - o.Spread + o.DX - e, Y: o.Rect.Y - o.Spread + o.DY - e, W: o.Rect.W + 2*o.Spread + 2*e, H: o.Rect.H + 2*o.Spread + 2*e}
}

// shadowWholeSurface reports whether boxShadow computes the coverage of o for
// the whole surface (rather than for the current clip): its mask must not be
// larger than shadowMaskMax, a bound taken here on the shadow's device box.
func shadowWholeSurface(o render.BoxShadow, m Matrix) bool {
	sigma := math.Max(0, o.Blur) / 2 * m.MaxScale()
	pad := int(math.Ceil(sigma*3)) + 1
	r := o.Rect
	if !o.Inset {
		r = render.Rect{X: r.X + o.DX - o.Spread, Y: r.Y + o.DY - o.Spread, W: r.W + 2*o.Spread, H: r.H + 2*o.Spread}
	}
	if r.W <= 0 || r.H <= 0 {
		return true // nothing is drawn
	}
	w, h := r.W*m.MaxScale()+float64(2*pad+4), r.H*m.MaxScale()+float64(2*pad+4)
	return w*h <= shadowMaskMax/2
}

// cornerSpan marks the device columns where the outline of a rounded
// rectangle is curved: (Lo0, Lo1) at its left end and (Hi0, Hi1) at its right
// end. Between them the outline has only horizontal and vertical edges, so a
// vertical clip edge there cuts the shape exactly like a plain rectangle.
// The zero value means "no such guarantee".
type cornerSpan struct{ Lo0, Lo1, Hi0, Hi1 int }

func (c cornerSpan) ok() bool { return c.Hi1 > c.Lo0 }

// cornerZone is the cornerSpan of a rounded rectangle (zero when it has square
// corners, where Exact says it all, or the transform is not axis aligned).
func cornerZone(r render.Rect, rad render.Radii, m Matrix, surf IRect) cornerSpan {
	if rad.IsZero() || m[1] != 0 || m[2] != 0 || !hasRect(r) {
		return cornerSpan{}
	}
	mx := 0.0
	for _, c := range rad {
		mx = math.Max(mx, math.Max(c[0], c[1]))
	}
	b := devRect(m, r, IRect{-1 << 30, -1 << 30, 1 << 30, 1 << 30})
	z := int(math.Ceil(mx*m.MaxScale())) + damagePad + 1
	if b.Empty() || z < 0 || z > 1<<20 || b.X1-b.X0 > 1<<20 {
		return cornerSpan{}
	}
	return cornerSpan{b.X0, b.X0 + z + damagePad, b.X1 - z - damagePad, b.X1}
}

func hasRect(r render.Rect) bool { return r.W > 0 && r.H > 0 }

// Analyze builds the ListInfo of list for a w x h device-pixel surface.
func Analyze(list []render.Op, w, h int, dpr float64) *ListInfo {
	n := len(list)
	in := &ListInfo{Key: make([]uint64, n), Bounds: make([]IRect, n), Pair: make([]int32, n), Wide: make([]bool, n), Exact: make([]bool, n), Corner: make([]cornerSpan, n), Desc: descs(list), Pre: make([]uint64, n+1), Surf: IRect{0, 0, w, h}}
	ctm := Scale(dpr, dpr)
	var ctx uint64
	var stack []groupRec
	add := func(b IRect) {
		if k := len(stack); k > 0 {
			stack[k-1].acc = stack[k-1].acc.union(b)
		}
	}
	inner := in.Surf
	// push opens a group. layer groups (opacity, filters, masks) allocate an
	// offscreen surface; when their bounds miss the clip, the painter makes a
	// 1x1 one at the parent's origin, which leaks what is drawn into it. That
	// pixel depends on the parent's extent, which a partial repaint changes, so
	// such lists are not repainted incrementally.
	push := func(i int, sig uint64, limit render.Rect, lim bool, wide bool, outsetPx float64, isLayer bool) bool {
		g := groupRec{idx: i, wide: wide, ctx: ctx, ctm: ctm, inner: inner}
		if lim && hasRect(limit) {
			inner = inner.Intersect(innerDev(ctm, limit, in.Surf, isLayer))
		} else if !isLayer {
			inner = IRect{}
		}
		if isLayer && inner.Empty() {
			return false
		}
		if wide {
			g.reach = int(math.Ceil(outsetPx * ctm.MaxScale()))
		}
		if lim && hasRect(limit) {
			g.limit = devRect(ctm, limit, in.Surf).outset(g.reach)
			g.hasLim = true
		}
		in.Key[i] = mix(sig, ctx)
		ctx = mix(ctx, sig)
		stack = append(stack, g)
		return true
	}
	for i, op := range list {
		in.Pair[i] = -1
		sig, ok := render.OpSig(op)
		if !ok {
			return &ListInfo{OK: false}
		}
		leaf := func(r render.Rect, wide bool) {
			in.Key[i] = mix(sig, ctx)
			b := devRect(ctm, r, in.Surf)
			in.Bounds[i] = b
			in.Wide[i] = wide
			add(b)
		}
		switch o := op.(type) {
		case render.FillRect:
			leaf(o.Rect, false)
			// a plain rectangle has only vertical and horizontal edges: the scan
			// converter treats them the same with and without a horizontal clip
			in.Exact[i] = o.Radii.IsZero() && ctm[1] == 0 && ctm[2] == 0
			in.Corner[i] = cornerZone(o.Rect, o.Radii, ctm, in.Surf)
		case render.Border:
			leaf(o.Rect, false)
		case render.TextRun:
			leaf(textBounds(o), len(o.Shadows) > 0)
		case render.Image:
			leaf(o.Dst, false)
		case render.Gradient:
			leaf(o.Rect, false)
			in.Exact[i] = o.Radii.IsZero() && ctm[1] == 0 && ctm[2] == 0
			in.Corner[i] = cornerZone(o.Rect, o.Radii, ctm, in.Surf)
		case render.BoxShadow:
			leaf(shadowBounds(o), false)
			// without blur and corners it is built from rectangles only (a
			// window-sized 1px inset frame is the usual case)
			in.Exact[i] = o.Radii.IsZero() && o.Blur == 0 && ctm[1] == 0 && ctm[2] == 0
			// otherwise it is a blend of a coverage mask that boxShadow computes
			// over the whole surface (so it does not depend on the clip), as long
			// as the mask is small enough to be computed that way
			if !in.Exact[i] && ctm[1] == 0 && ctm[2] == 0 {
				in.Exact[i] = shadowWholeSurface(o, ctm)
			}
		case render.Scrollbar:
			leaf(o.Rect, false)
		case render.ScrollCorner:
			leaf(o.Rect, false)
		case render.SVGDraw:
			leaf(o.Dst, true)
		case render.PushClip:
			zc := cornerZone(o.Rect, o.Radii, ctm, in.Surf)
			push(i, sig, o.Rect, true, false, 0, false)
			in.Exact[i] = o.Radii.IsZero() && ctm[1] == 0 && ctm[2] == 0
			in.Corner[i] = zc
		case render.PushLayer:
			px, fok := filterReach(o.Filter)
			if !fok {
				return &ListInfo{OK: false}
			}
			if !push(i, sig, o.Bounds, true, len(o.Filter) > 0, px, true) {
				return &ListInfo{OK: false}
			}
		case render.PushMask:
			if !push(i, sig, o.Bounds, true, false, 0, true) {
				return &ListInfo{OK: false}
			}
		case render.PushTransform:
			g := groupRec{idx: i, ctx: ctx, ctm: ctm, inner: inner}
			in.Key[i] = mix(sig, ctx)
			ctx = mix(ctx, sig)
			ctm = ctm.Mul(Matrix(o.M))
			if ctm[1] != 0 || ctm[2] != 0 {
				inner = IRect{} // rotated or skewed: the clip is no rectangle any more
			}
			stack = append(stack, g)
		case render.PopClip, render.PopLayer, render.PopMask, render.PopTransform:
			k := len(stack)
			if k == 0 {
				return &ListInfo{OK: false}
			}
			g := stack[k-1]
			stack = stack[:k-1]
			in.Key[i] = mix(sig, ctx)
			ctx, ctm, inner = g.ctx, g.ctm, g.inner
			b := g.acc
			if g.wide {
				b = b.outset(g.reach)
			}
			if g.hasLim {
				b = b.Intersect(g.limit)
			}
			in.Bounds[g.idx], in.Bounds[i] = b, b
			in.Exact[i] = in.Exact[g.idx]
			in.Corner[i] = in.Corner[g.idx]
			in.Wide[g.idx] = g.wide
			in.Pair[g.idx], in.Pair[i] = int32(i), int32(g.idx)
			add(b)
		default:
			return &ListInfo{OK: false}
		}
	}
	if len(stack) != 0 {
		return &ListInfo{OK: false}
	}
	for i, k := range in.Key {
		in.Pre[i+1] = mix(in.Pre[i], k)
	}
	in.OK = true
	return in
}

// ---- diff ---------------------------------------------------------------------------

// lcsMatch returns for a and b which elements are matched by a longest common
// subsequence found with Myers' greedy algorithm. ok is false when the lists
// differ in more than maxD elements.
func lcsMatch(a, b []uint64, maxD int) (ma, mb []bool, ok bool) {
	ma, mb = make([]bool, len(a)), make([]bool, len(b))
	lo := 0
	for lo < len(a) && lo < len(b) && a[lo] == b[lo] {
		ma[lo], mb[lo] = true, true
		lo++
	}
	ea, eb := len(a), len(b)
	for ea > lo && eb > lo && a[ea-1] == b[eb-1] {
		ea--
		eb--
		ma[ea], mb[eb] = true, true
	}
	A, B := a[lo:ea], b[lo:eb]
	n, m := len(A), len(B)
	if n == 0 || m == 0 {
		return ma, mb, true
	}
	if n+m < maxD {
		maxD = n + m
	}
	off := maxD + 1
	v := make([]int, 2*off+2)
	var trace [][]int
	found := -1
	for d := 0; d <= maxD && found < 0; d++ {
		snap := make([]int, 2*d+1)
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[off+k-1] < v[off+k+1]) {
				x = v[off+k+1]
			} else {
				x = v[off+k-1] + 1
			}
			y := x - k
			for x < n && y < m && A[x] == B[y] {
				x++
				y++
			}
			v[off+k] = x
			if x >= n && y >= m {
				found = d
			}
		}
		for k := -d; k <= d; k++ {
			if (k+d)%2 == 0 {
				snap[k+d] = v[off+k]
			}
		}
		trace = append(trace, snap)
		if found >= 0 {
			break
		}
	}
	if found < 0 {
		return nil, nil, false
	}
	// backtrack: every diagonal step is a matched pair
	x, y := n, m
	for d := found; d > 0; d-- {
		prev := trace[d-1] // round d-1, index k+(d-1)
		k := x - y
		var pk, mx, my int
		px := 0
		if k == -d || (k != d && prev[k-1+d-1] < prev[k+1+d-1]) {
			pk = k + 1
			px = prev[pk+d-1]
			mx, my = px, px-pk+1 // a down move
		} else {
			pk = k - 1
			px = prev[pk+d-1]
			mx, my = px+1, px-pk // a right move
		}
		for x > mx && y > my {
			x--
			y--
			ma[lo+x], mb[lo+y] = true, true
		}
		x, y = px, px-pk
	}
	for x > 0 && y > 0 {
		x--
		y--
		ma[lo+x], mb[lo+y] = true, true
	}
	return ma, mb, true
}

// Damage returns the bands of device rows (full surface width) whose pixels can
// differ between a paint of the old list and one of the new list. ok is false
// when the lists are too different (or not comparable) for an incremental
// repaint to pay.
//
// The damage is whole rows on purpose: the scan converter treats a shape that
// crosses the clip's left or right edge slightly differently from one that
// does not (the part outside is folded into the edge column), and a full paint
// shows that only at the real clip edges. Repainting complete rows keeps the
// horizontal clip identical to a full paint's; clipping vertically is exact.
func Damage(old, cur *ListInfo) (bands []Band, ok bool) {
	if old == nil || cur == nil || !old.OK || !cur.OK || old.Surf != cur.Surf {
		return nil, false
	}
	ma, mb, ok := lcsMatch(old.Key, cur.Key, 700)
	if !ok {
		return nil, false
	}
	if dbgNarrow {
		for i, m := range ma {
			if !m {
				println("changed old", i, old.Desc[i][:min(len(old.Desc[i]), 120)], old.Bounds[i].X0, old.Bounds[i].X1)
			}
		}
		for i, m := range mb {
			if !m {
				println("changed new", i, cur.Desc[i][:min(len(cur.Desc[i]), 120)], cur.Bounds[i].X0, cur.Bounds[i].X1)
			}
		}
	}
	for i, m := range ma {
		if !m {
			bands = append(bands, Band{old.Bounds[i].Y0, old.Bounds[i].Y1, old.Bounds[i].X0, old.Bounds[i].X1})
		}
	}
	for i, m := range mb {
		if !m {
			bands = append(bands, Band{cur.Bounds[i].Y0, cur.Bounds[i].Y1, cur.Bounds[i].X0, cur.Bounds[i].X1})
		}
	}
	bands = mergeBands(bands)
	if len(bands) == 0 {
		return nil, true
	}
	// non-local ops that touch the damage are repainted entirely
	for grew := true; grew; {
		grew = false
		for _, in := range []*ListInfo{cur, old} {
			for i, w := range in.Wide {
				b := in.Bounds[i]
				if !w || b.Empty() {
					continue
				}
				for _, bd := range bands {
					if b.Y0 < bd.Y1 && bd.Y0 < b.Y1 && (b.Y0 < bd.Y0 || b.Y1 > bd.Y1) {
						bands = mergeBands(append(bands, Band{b.Y0, b.Y1, b.X0, b.X1}))
						grew = true
						break
					}
				}
			}
		}
	}
	y0, y1 := cur.Surf.Y0, cur.Surf.Y1
	out := bands[:0]
	for _, b := range bands {
		b.Y0, b.Y1 = max(b.Y0, y0), min(b.Y1, y1)
		if b.Y0 < b.Y1 {
			out = append(out, b)
		}
	}
	return out, true
}

// Band is a range of device rows.
// Band is a range of device rows, and the columns [X0,X1) of them that were
// repainted (X1 == 0: the whole width, for bands made by Damage).
type Band struct{ Y0, Y1, X0, X1 int }

// mergeBands sorts bands and merges those that overlap or nearly touch.
func mergeBands(bs []Band) []Band {
	const gap = 8
	out := bs[:0:0]
	for _, b := range bs {
		if b.Y1 <= b.Y0 {
			continue
		}
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Y0 < out[j].Y0 })
	m := out[:0]
	for _, b := range out {
		if n := len(m); n > 0 && b.Y0 <= m[n-1].Y1+gap {
			m[n-1].Y1 = max(m[n-1].Y1, b.Y1)
			m[n-1].X0, m[n-1].X1 = min(m[n-1].X0, b.X0), max(m[n-1].X1, b.X1)
		} else {
			m = append(m, b)
		}
	}
	return m
}

// ---- canvas -------------------------------------------------------------------------

// Canvas holds the pixels of the last paint and the analysis of its list.
//
// It can also hold a snapshot of the surface as it was after the first snapN
// ops of the list (the part that rarely changes: backgrounds, the toolbar
// beneath the field being typed in). While the first snapN ops are unchanged,
// a repaint starts from the snapshot's rows instead of redrawing everything
// under the changed ops.
type Canvas struct {
	Surf *Surface
	DPR  float64
	Env  PaintEnv
	Info *ListInfo

	snap    *Surface
	snapN   int
	snapPre uint64
	lcps    []int // common-prefix lengths of the last incremental updates

	// a snapshot is rendered in the background after an update has returned, so
	// it never delays a frame; the next Update waits for it
	building sync.WaitGroup
	built    *snapBuilt
}

type snapBuilt struct {
	surf *Surface
	n    int
	pre  uint64
}

// settle waits for a background snapshot and adopts it.
func (cv *Canvas) settle() {
	cv.building.Wait()
	if b := cv.built; b != nil {
		cv.snap, cv.snapN, cv.snapPre = b.surf, b.n, b.pre
		cv.built = nil
		SnapBuilt.Add(1)
	}
}

// NewCanvas allocates a transparent w x h device-pixel canvas.
func NewCanvas(w, h int, dpr float64, env PaintEnv) *Canvas {
	return &Canvas{Surf: NewSurface(w, h), DPR: dpr, Env: env}
}

// Matches reports whether the canvas was made for these parameters.
func (cv *Canvas) Matches(w, h int, dpr float64, env PaintEnv) bool {
	return cv != nil && cv.Surf.W() == w && cv.Surf.H() == h && cv.DPR == dpr && cv.Env.Background == env.Background
}

// Paint repaints the whole canvas from list.
func (cv *Canvas) Paint(list []render.Op) {
	cv.paint(list, Analyze(list, cv.Surf.W(), cv.Surf.H(), cv.DPR))
}

func (cv *Canvas) paint(list []render.Op, info *ListInfo) {
	cv.settle()
	cv.Surf.Clear()
	cv.snap, cv.lcps = nil, nil
	p := newPainterOn(cv.Surf, cv.DPR, cv.Env, cv.Surf.R)
	p.Run(list)
	cv.Info = info
}

// ParallelBands lets Update repaint the damaged bands concurrently (a variable
// so tests and benchmarks can compare).
var ParallelBands = true

// SnapBuilt and SnapUsed count surface snapshots made and repaints that
// started from one (tests check the snapshot path is exercised).
var SnapBuilt, SnapUsed, SnapExtended atomic.Int64

// Snapshot tuning (variables so tests can force the snapshot path).
var (
	SnapMinOps    = 120 // lists shorter than this are cheap enough to repaint
	SnapStable    = 2   // consecutive updates that must share the prefix
	SnapMinShare  = 4   // the prefix must be at least 1/SnapMinShare of the list
	SnapExtendMin = 2   // a snapshot is moved forward when the stable prefix grows by more than this many ops
)

// Update brings the canvas to list. It returns the rows that were repainted
// (nil when nothing changed) and whether that was the whole canvas.
func (cv *Canvas) Update(list []render.Op) (bands []Band, full bool) {
	cv.settle()
	info := Analyze(list, cv.Surf.W(), cv.Surf.H(), cv.DPR)
	old := cv.Info
	bands, ok := Damage(old, info)
	rows := 0
	for _, b := range bands {
		rows += b.Y1 - b.Y0
	}
	if !ok || rows*10 > cv.Surf.H()*6 {
		cv.paint(list, info)
		return []Band{{cv.Surf.R.Y0, cv.Surf.R.Y1, cv.Surf.R.X0, cv.Surf.R.X1}}, true
	}
	cv.Info = info
	lcp := 0
	for lcp < len(old.Key) && lcp < len(info.Key) && old.Key[lcp] == info.Key[lcp] {
		lcp++
	}
	if len(bands) == 0 {
		return nil, false
	}
	if dbgNarrow {
		println("update: lcp", lcp, "snap", cv.snap != nil, cv.snapN, "ops", len(list), "bands", len(bands))
	}
	if cv.snap != nil && !(cv.snapN <= len(list) && info.Pre[cv.snapN] == cv.snapPre) {
		cv.snap = nil // the part under the snapshot changed
	}
	// the bands are disjoint rows and each is repainted by its own painter, so
	// they can be repainted at the same time: the result is the same bytes
	paintBand := func(b *Band) {
		region := narrow(info, *b, cv.Surf.R)
		b.X0, b.X1 = region.X0, region.X1
		if cv.snap != nil {
			SnapUsed.Add(1)
			for y := b.Y0; y < b.Y1; y++ {
				copy(cv.Surf.Row(y, region.X0, region.X1), cv.snap.Row(y, region.X0, region.X1))
			}
			p := newPainterOver(cv.Surf, cv.DPR, cv.Env, region)
			p.RunRegionFrom(list, info, region, cv.snapN)
			return
		}
		p := newPainterOn(cv.Surf, cv.DPR, cv.Env, region)
		p.RunRegion(list, info, region)
	}
	if len(bands) == 1 || !ParallelBands {
		for bi := range bands {
			paintBand(&bands[bi])
		}
	} else {
		var wg sync.WaitGroup
		for bi := range bands[1:] {
			wg.Add(1)
			go func(b *Band) {
				defer wg.Done()
				paintBand(b)
			}(&bands[bi+1])
		}
		paintBand(&bands[0])
		wg.Wait()
	}
	// keep a snapshot when the same prefix has stayed untouched for a while
	cv.lcps = append(cv.lcps, lcp)
	if len(cv.lcps) > SnapStable {
		cv.lcps = cv.lcps[1:]
	}
	if cv.snap != nil && len(cv.lcps) == SnapStable {
		cv.extendSnapshot(list, info)
	}
	if cv.snap == nil && len(list) >= SnapMinOps && len(cv.lcps) == SnapStable {
		m := cv.lcps[0]
		for _, v := range cv.lcps {
			m = min(m, v)
		}
		if n := safeSnapPoint(list, m); n*SnapMinShare >= len(list) {
			sn := NewSurface(cv.Surf.W(), cv.Surf.H())
			p := newPainterOn(sn, cv.DPR, cv.Env, sn.R)
			for _, op := range list[:n] {
				p.op(op)
			}
			cv.snap, cv.snapN, cv.snapPre = sn, n, info.Pre[n]
			SnapBuilt.Add(1)
		}
	}
	return bands, false
}

// extendSnapshot moves the snapshot forward over the ops that have stayed
// unchanged since it was made: they are painted onto it, inside the rectangle
// they cover, the same way an incremental repaint paints over its backdrop.
// (The snapshot is the surface after list[:snapN] with the current list.)
func (cv *Canvas) extendSnapshot(list []render.Op, info *ListInfo) {
	m := cv.lcps[0]
	for _, v := range cv.lcps {
		m = min(m, v)
	}
	n := safeSnapPoint(list, m)
	if n <= cv.snapN+SnapExtendMin || n > len(list) {
		return
	}
	var hull IRect
	for i := cv.snapN; i < n; i++ {
		hull = hull.union(info.Bounds[i])
	}
	if !hull.Empty() {
		region := narrow(info, Band{hull.Y0, hull.Y1, hull.X0, hull.X1}, cv.Surf.R)
		p := newPainterOver(cv.snap, cv.DPR, cv.Env, region)
		for i := 0; i < cv.snapN; i++ { // re-open the clips and transforms still open
			switch list[i].(type) {
			case render.PushClip, render.PushTransform:
				if int(info.Pair[i]) >= cv.snapN {
					p.op(list[i])
				} else {
					i = int(info.Pair[i])
				}
			case render.PushLayer, render.PushMask:
				i = int(info.Pair[i])
			}
		}
		p.runRegion(list, info, region, cv.snapN, n)
	}
	cv.snapN, cv.snapPre = n, info.Pre[n]
	SnapExtended.Add(1)
}

// safeSnapPoint returns the largest n <= max such that no layer or mask group
// is open between ops n-1 and n: a surface snapshot at n then holds all the
// state that matters (clips and transforms are replayed from the ops).
func safeSnapPoint(list []render.Op, max int) int {
	if max > len(list) {
		max = len(list)
	}
	var stack []bool // true: group holds pixels (layer / mask)
	best, layers := 0, 0
	for i := 0; i < max; i++ {
		switch list[i].(type) {
		case render.PushLayer, render.PushMask:
			stack = append(stack, true)
			layers++
		case render.PushClip, render.PushTransform:
			stack = append(stack, false)
		case render.PopLayer, render.PopMask:
			stack = stack[:len(stack)-1]
			layers--
		case render.PopClip, render.PopTransform:
			stack = stack[:len(stack)-1]
		}
		if layers == 0 {
			best = i + 1
		}
	}
	return best
}

// Image views the canvas pixels (shared, premultiplied).
func (cv *Canvas) Image() *image.RGBA { return cv.Surf.Image() }

// newPainterOn paints on an existing surface, clipped to region, after
// resetting region to the canvas background.
func newPainterOn(s *Surface, dpr float64, env PaintEnv, region IRect) *Painter {
	c := NewContextSurface(s)
	region = region.Intersect(s.R)
	c.st.clip = &clipState{R: region}
	if !region.Empty() {
		for y := region.Y0; y < region.Y1; y++ {
			clear(s.Row(y, region.X0, region.X1))
		}
		if env.Background.A > 0 {
			c.SetFill(Brush{Color: env.Background})
			c.FillRect(float64(region.X0), float64(region.Y0), float64(region.W()), float64(region.H()))
		}
	}
	c.SetTransform(Scale(dpr, dpr))
	c.SetSmoothing(true, QualityLow)
	return &Painter{c: c, env: env, dpr: dpr}
}

// RunRegion executes the ops of list that can reach region (the painter's
// clip), skipping whole groups that cannot.
// newPainterOver is newPainterOn without clearing the region (the caller has
// already put the pixels it wants to draw over there).
func newPainterOver(s *Surface, dpr float64, env PaintEnv, region IRect) *Painter {
	c := NewContextSurface(s)
	c.st.clip = &clipState{R: region.Intersect(s.R)}
	c.SetTransform(Scale(dpr, dpr))
	c.SetSmoothing(true, QualityLow)
	return &Painter{c: c, env: env, dpr: dpr}
}

// RunRegionFrom is RunRegion for a surface that already shows the result of
// list[:start]: those ops are not run again, only the clips and transforms
// that are still open at start are re-established.
func (p *Painter) RunRegionFrom(list []render.Op, info *ListInfo, region IRect, start int) {
	for i := 0; i < start; i++ {
		switch list[i].(type) {
		case render.PushClip, render.PushTransform:
			if int(info.Pair[i]) >= start {
				p.op(list[i])
			} else {
				i = int(info.Pair[i]) // closed before start: nothing to replay
			}
		case render.PushLayer, render.PushMask:
			i = int(info.Pair[i]) // (never open at start)
		}
	}
	p.runRegion(list, info, region, start, len(list))
}

// RunRegion executes the ops of list that can reach region (the painter's
// clip), skipping whole groups that cannot.
func (p *Painter) RunRegion(list []render.Op, info *ListInfo, region IRect) {
	p.runRegion(list, info, region, 0, len(list))
}

func (p *Painter) runRegion(list []render.Op, info *ListInfo, region IRect, start, end int) {
	for i := start; i < end; i++ {
		switch list[i].(type) {
		case render.PopClip, render.PopLayer, render.PopMask, render.PopTransform:
			p.op(list[i])
		case render.PushClip, render.PushLayer, render.PushMask, render.PushTransform:
			if info.Bounds[i].Intersect(region).Empty() {
				i = int(info.Pair[i]) // skip the group
				continue
			}
			p.op(list[i])
		default:
			if info.Bounds[i].Intersect(region).Empty() {
				continue
			}
			p.op(list[i])
		}
	}
	for p.c.LayerDepth() > 0 {
		p.c.EndLayer(1, SourceOver)
	}
}

// narrow returns the rectangle to repaint for a damaged band: the columns the
// changed ops cover, widened until no op that reaches the band crosses its left
// or right edge unless clipping it there is exact. Whole rows used to be
// repainted because the scan converter handles a shape that crosses the clip's
// left or right edge slightly differently from one that does not (the part
// outside is folded into the edge column), and a full paint shows that only at
// the real surface edges. Shapes made of vertical and horizontal edges only
// (plain rectangles, rectangular clips) are not affected, so those may cross;
// every other op is repainted whole or not at all.
func narrow(info *ListInfo, b Band, surf IRect) IRect {
	x0, x1 := b.X0, b.X1
	if x1 <= x0 {
		return IRect{surf.X0, b.Y0, surf.X1, b.Y1}
	}
	x0, x1 = max(x0-damagePad, surf.X0), min(x1+damagePad, surf.X1)
	for changed := true; changed; {
		changed = false
		for i, bo := range info.Bounds {
			if bo.X1 <= bo.X0 || bo.Y1 <= b.Y0 || bo.Y0 >= b.Y1 || info.Exact[i] {
				continue
			}
			cross := (bo.X0 < x0 && bo.X1 > x0) || (bo.X0 < x1 && bo.X1 > x1)
			if cz := info.Corner[i]; cz.ok() && cross {
				// only an edge inside the curved ends matters: move it out of them
				// (to the straight part of the outline, or to the outside of the op)
				nx0, nx1 := x0, x1
				switch {
				case x0 > cz.Lo0 && x0 < cz.Lo1:
					nx0 = cz.Lo0
				case x0 > cz.Hi0 && x0 < cz.Hi1:
					nx0 = cz.Hi0
				}
				switch {
				case x1 > cz.Lo0 && x1 < cz.Lo1:
					nx1 = cz.Lo1
				case x1 > cz.Hi0 && x1 < cz.Hi1:
					nx1 = cz.Hi1
				}
				nx0, nx1 = max(nx0, surf.X0), min(nx1, surf.X1)
				if nx0 != x0 || nx1 != x1 {
					x0, x1 = nx0, nx1
					changed = true
				}
				continue
			}
			if cross {
				nx0, nx1 := min(x0, bo.X0), max(x1, bo.X1)
				if dbgNarrow {
					println("narrow: op", i, info.Desc[i], "widens", x0, x1, "to", nx0, nx1, "band", b.Y0, b.Y1)
				}
				nx0, nx1 = max(nx0, surf.X0), min(nx1, surf.X1)
				if nx0 != x0 || nx1 != x1 {
					x0, x1 = nx0, nx1
					changed = true
				}
			}
		}
	}
	return IRect{x0, b.Y0, x1, b.Y1}
}

var dbgNarrow = os.Getenv("SWEEF_NARROW_DEBUG") != ""

func descs(list []render.Op) []string {
	if !dbgNarrow {
		return make([]string, len(list))
	}
	out := make([]string, len(list))
	for i, o := range list {
		out[i] = fmt.Sprintf("%T %+v", o, o)
		if len(out[i]) > 700 {
			out[i] = out[i][:700]
		}
	}
	return out
}
