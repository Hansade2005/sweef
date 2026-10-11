package render

import (
	"math"

	"golang.org/x/net/html"

	"github.com/sweefbrowser/sweef/internal/style"
)

// CSS 3D transforms: matrix construction (float64, CSS order of operations),
// perspective, the 3D rendering context rules and the projection of boxes for
// getBoundingClientRect / getClientRects. Painting lives in paint_3d.go.

// Mat4 is a 4x4 matrix acting on column vectors: p' = M * p, M[row][col].
type Mat4 [4][4]float64

func ident4() Mat4 {
	return Mat4{{1, 0, 0, 0}, {0, 1, 0, 0}, {0, 0, 1, 0}, {0, 0, 0, 1}}
}

// mul returns a*b (b is applied first).
func (a Mat4) mul(b Mat4) Mat4 {
	var o Mat4
	for i := 0; i < 4; i++ {
		for j := 0; j < 4; j++ {
			o[i][j] = a[i][0]*b[0][j] + a[i][1]*b[1][j] + a[i][2]*b[2][j] + a[i][3]*b[3][j]
		}
	}
	return o
}

// apply maps (x, y, z, 1) and returns the homogeneous result.
func (a Mat4) apply(x, y, z float64) (X, Y, Z, W float64) {
	X = a[0][0]*x + a[0][1]*y + a[0][2]*z + a[0][3]
	Y = a[1][0]*x + a[1][1]*y + a[1][2]*z + a[1][3]
	Z = a[2][0]*x + a[2][1]*y + a[2][2]*z + a[2][3]
	W = a[3][0]*x + a[3][1]*y + a[3][2]*z + a[3][3]
	return
}

// project maps a point on the z=0 plane and divides by w (Blink's
// ProjectPoint: no clipping, a negative w simply mirrors the point).
func (a Mat4) project(x, y float64) (float64, float64) {
	X, Y, _, W := a.apply(x, y, 0)
	if W == 0 || W == 1 {
		return X, Y
	}
	return X / W, Y / W
}

func translate4(x, y, z float64) Mat4 {
	m := ident4()
	m[0][3], m[1][3], m[2][3] = x, y, z
	return m
}

func scale4(x, y, z float64) Mat4 {
	m := ident4()
	m[0][0], m[1][1], m[2][2] = x, y, z
	return m
}

// sinCosDeg is exact at multiples of 90 degrees like gfx::Transform.
func sinCosDeg(d float64) (s, c float64) {
	if r := math.Mod(d, 90); r == 0 {
		switch q := int(math.Mod(math.Mod(d/90, 4)+4, 4)); q {
		case 0:
			return 0, 1
		case 1:
			return 1, 0
		case 2:
			return 0, -1
		default:
			return -1, 0
		}
	}
	a := d * math.Pi / 180
	return math.Sin(a), math.Cos(a)
}

func rotate4(x, y, z, deg float64) Mat4 {
	l := math.Sqrt(x*x + y*y + z*z)
	if l == 0 {
		return ident4()
	}
	x, y, z = x/l, y/l, z/l
	// half-angle form as in the CSS spec
	var sc, sq float64
	if math.Mod(deg, 90) == 0 {
		// exact: sin(a)/2 and (1-cos a)/2
		s, c := sinCosDeg(deg)
		sc = s / 2
		sq = (1 - c) / 2
	} else {
		a := deg * math.Pi / 360
		sc = math.Sin(a) * math.Cos(a)
		sq = math.Sin(a) * math.Sin(a)
	}
	m := ident4()
	m[0][0] = 1 - 2*(y*y+z*z)*sq
	m[0][1] = 2 * (x*y*sq - z*sc)
	m[0][2] = 2 * (x*z*sq + y*sc)
	m[1][0] = 2 * (x*y*sq + z*sc)
	m[1][1] = 1 - 2*(x*x+z*z)*sq
	m[1][2] = 2 * (y*z*sq - x*sc)
	m[2][0] = 2 * (x*z*sq - y*sc)
	m[2][1] = 2 * (y*z*sq + x*sc)
	m[2][2] = 1 - 2*(x*x+y*y)*sq
	return m
}

func perspective4(d float64) Mat4 {
	m := ident4()
	if d < 1 {
		d = 1
	}
	m[3][2] = -1 / d
	return m
}

func skew4(axDeg, ayDeg float64) Mat4 {
	m := ident4()
	m[0][1] = math.Tan(axDeg * math.Pi / 180)
	m[1][0] = math.Tan(ayDeg * math.Pi / 180)
	return m
}

// is2D reports whether m only holds a 2D affine transform.
func (a Mat4) is2D() bool {
	return a[2][0] == 0 && a[2][1] == 0 && a[2][2] == 1 && a[2][3] == 0 &&
		a[0][2] == 0 && a[1][2] == 0 && a[3][2] == 0 &&
		a[3][0] == 0 && a[3][1] == 0 && a[3][3] == 1
}

// plane returns the 3x3 homography (row-major over x, y, w) of the z=0 plane.
func (a Mat4) plane() [9]float64 {
	return [9]float64{a[0][0], a[0][1], a[0][3], a[1][0], a[1][1], a[1][3], a[3][0], a[3][1], a[3][3]}
}

// affine6 returns the CSS matrix() form of the z=0 plane when it is affine.
func (a Mat4) affine6() ([6]float64, bool) {
	if a[3][0] != 0 || a[3][1] != 0 || a[3][3] == 0 {
		return identity, false
	}
	w := a[3][3]
	return [6]float64{a[0][0] / w, a[1][0] / w, a[0][1] / w, a[1][1] / w, a[0][3] / w, a[1][3] / w}, true
}

func (a Mat4) det() float64 {
	// cofactor expansion along the first row
	var d float64
	for j := 0; j < 4; j++ {
		d += a[0][j] * a.cof(0, j)
	}
	return d
}

// cof is the signed cofactor of element (r, c).
func (a Mat4) cof(r, c int) float64 {
	var m [3][3]float64
	ri := 0
	for i := 0; i < 4; i++ {
		if i == r {
			continue
		}
		ci := 0
		for j := 0; j < 4; j++ {
			if j == c {
				continue
			}
			m[ri][ci] = a[i][j]
			ci++
		}
		ri++
	}
	d := m[0][0]*(m[1][1]*m[2][2]-m[1][2]*m[2][1]) - m[0][1]*(m[1][0]*m[2][2]-m[1][2]*m[2][0]) + m[0][2]*(m[1][0]*m[2][1]-m[1][1]*m[2][0])
	if (r+c)%2 == 1 {
		d = -d
	}
	return d
}

// backFaceVisible reports whether a layer whose front faces +z shows its back
// side after the transform (gfx::Transform::IsBackFaceVisible).
func (a Mat4) backFaceVisible() bool {
	d := a.det()
	if d == 0 || math.IsNaN(d) {
		return false
	}
	return a.cof(2, 2)*d < 0
}

func (a Mat4) invert() (Mat4, bool) {
	d := a.det()
	if d == 0 || math.IsNaN(d) {
		return ident4(), false
	}
	var o Mat4
	for i := 0; i < 4; i++ {
		for j := 0; j < 4; j++ {
			o[j][i] = a.cof(i, j) / d
		}
	}
	return o, true
}

// from6 embeds a CSS matrix(a,b,c,d,e,f).
func from6(m [6]float64) Mat4 {
	r := ident4()
	r[0][0], r[1][0], r[0][1], r[1][1], r[0][3], r[1][3] = m[0], m[1], m[2], m[3], m[4], m[5]
	return r
}

// ---- per-element matrices ---------------------------------------------------------------

// has3DOp reports whether the transform properties of b contain a 3D function
// or a 3D value (anything that is not a 2D transform).
func (b *lbox) has3DOp() bool {
	st := b.st
	if st.TOriginZ != 0 && b.hasTransform() {
		return true
	}
	for _, list := range [][]style.TOp{st.Translate, st.Rotate, st.Scale, st.Transform} {
		for _, op := range list {
			switch op.Fn {
			case "translatez", "translate3d", "scalez", "scale3d", "rotatex", "rotatey", "rotate3d", "matrix3d", "perspective":
				return true
			}
		}
	}
	return false
}

// use4 reports whether b's matrices come from the 4x4 builder (3D functions or
// a transform-box other than the border box).
func (b *lbox) use4() bool {
	return b.has3DOp() || b.st.TransformBox == "content-box" || b.st.TransformBox == "fill-box"
}

// refBox returns the transform reference box of b in absolute coordinates.
func (b *lbox) refBox() (x, y, w, h float64) {
	switch b.st.TransformBox {
	case "content-box", "fill-box":
		return b.ax + b.bor[3] + b.pad[3], b.ay + b.bor[0] + b.pad[0], b.w - b.hExtra(), b.h - b.vExtra()
	}
	return b.ax, b.ay, b.w, b.h
}

func opMat4(op style.TOp, w, h float64) Mat4 {
	lv := func(i int, base float64) float64 { return op.A[i].Resolve(base) }
	num := func(i int, d float64) float64 {
		if i < op.N {
			return op.A[i].V
		}
		return d
	}
	switch op.Fn {
	case "translate":
		y := 0.0
		if op.N > 1 {
			y = lv(1, h)
		}
		return translate4(lv(0, w), y, 0)
	case "translatex":
		return translate4(lv(0, w), 0, 0)
	case "translatey":
		return translate4(0, lv(0, h), 0)
	case "translatez":
		return translate4(0, 0, lv(0, 0))
	case "translate3d":
		return translate4(lv(0, w), lv(1, h), lv(2, 0))
	case "scale":
		sx := num(0, 1)
		sy := sx
		if op.N > 1 {
			sy = num(1, sx)
		}
		return scale4(sx, sy, 1)
	case "scalex":
		return scale4(num(0, 1), 1, 1)
	case "scaley":
		return scale4(1, num(0, 1), 1)
	case "scalez":
		return scale4(1, 1, num(0, 1))
	case "scale3d":
		return scale4(num(0, 1), num(1, 1), num(2, 1))
	case "rotate", "rotatez":
		return rotate4(0, 0, 1, num(0, 0))
	case "rotatex":
		return rotate4(1, 0, 0, num(0, 0))
	case "rotatey":
		return rotate4(0, 1, 0, num(0, 0))
	case "rotate3d":
		return rotate4(num(0, 0), num(1, 0), num(2, 0), num(3, 0))
	case "skew":
		return skew4(num(0, 0), num(1, 0))
	case "skewx":
		return skew4(num(0, 0), 0)
	case "skewy":
		return skew4(0, num(0, 0))
	case "matrix":
		return from6([6]float64{num(0, 1), num(1, 0), num(2, 0), num(3, 1), num(4, 0), num(5, 0)})
	case "matrix3d":
		if op.M == nil {
			return ident4()
		}
		var m Mat4
		for c := 0; c < 4; c++ {
			for r := 0; r < 4; r++ {
				m[r][c] = op.M[c*4+r]
			}
		}
		return m
	case "perspective":
		if op.N == 0 {
			return ident4()
		}
		return perspective4(lv(0, 0))
	}
	return ident4()
}

// xfList is the transform of b about its origin, before the origin shift:
// translate * rotate * scale * transform.
func (b *lbox) xfList(w, h float64) Mat4 {
	st := b.st
	m := ident4()
	for _, ops := range [][]style.TOp{st.Translate, st.Rotate, st.Scale, st.Transform} {
		for _, op := range ops {
			m = m.mul(opMat4(op, w, h))
		}
	}
	return m
}

// own4 returns the transform of b in absolute coordinates (about its transform
// origin); the box origin is first moved by (sx, sy) (paint-offset rounding).
func (b *lbox) own4(sx, sy float64) Mat4 {
	rx, ry, rw, rh := b.refBox()
	ox := b.st.TransformOrigin[0].Resolve(rw)
	oy := b.st.TransformOrigin[1].Resolve(rh)
	oz := b.st.TOriginZ
	px, py := rx+ox, ry+oy
	m := b.xfList(rw, rh)
	return translate4(px+sx, py+sy, oz).mul(m).mul(translate4(-px, -py, -oz))
}

// xfContainer is the box whose perspective and 3D context b belongs to: the
// containing block for out-of-flow boxes, the layout parent otherwise, skipping
// anonymous boxes.
func (b *lbox) xfContainer() *lbox {
	var c *lbox
	if b.oof && b.cbBox != nil {
		c = b.cbBox
	} else if b.lp != nil {
		c = b.lp
	} else {
		c = b.parent
	}
	for c != nil && (c.n == nil || c.anon) && c.parent != nil {
		c = c.parent
	}
	return c
}

// persp4 is the perspective matrix b applies to its children, in absolute
// coordinates (identity when none).
func (b *lbox) persp4() (Mat4, bool) {
	d := b.st.Perspective
	if d <= 0 || b.kind == kInline || b.kind == kText {
		return ident4(), false
	}
	ox := b.st.PerspectiveOrigin[0].Resolve(b.w)
	oy := b.st.PerspectiveOrigin[1].Resolve(b.h)
	px, py := b.ax+ox, b.ay+oy
	return translate4(px, py, 0).mul(perspective4(d)).mul(translate4(-px, -py, 0)), true
}

// groupsFlat reports whether b has a grouping property, which forces
// transform-style: flat.
func (b *lbox) groupsFlat() bool {
	st := b.st
	if b.clips() {
		ox, oy := b.overflowX(), b.overflowY()
		if ox != "clip" && ox != "visible" || oy != "clip" && oy != "visible" {
			return true
		}
	}
	return st.OpacitySet && st.Opacity < 1 || len(st.Filter) > 0 || len(st.BackdropFilter) > 0 || st.ClipPath || st.MaskImage ||
		st.MixBlend != "" && st.MixBlend != "normal" || st.Isolation == "isolate" ||
		st.Contain != "" && (contains(st.Contain, "paint") || contains(st.Contain, "strict") || contains(st.Contain, "content"))
}

// preserves3D reports whether b's used transform-style is preserve-3d.
func (b *lbox) preserves3D() bool {
	return b.st.TransformStyle == "preserve-3d" && !b.groupsFlat()
}

// local4 is the matrix from b's local space to its container's space: its own
// transform with the container's perspective applied on top. ok is false when
// it is the identity.
func (b *lbox) local4(sx, sy float64) (Mat4, bool) {
	m := ident4()
	ok := false
	if b.hasTransform() {
		m = b.own4(sx, sy)
		ok = true
	} else if sx != 0 || sy != 0 {
		m = translate4(sx, sy, 0)
		ok = true
	}
	if c := b.xfContainer(); c != nil && c != b {
		if pm, has := c.persp4(); has {
			m = pm.mul(m)
			ok = true
		}
	}
	return m, ok
}

// ---- projection for getBoundingClientRect ------------------------------------------------

// Xf3D is the 3D transform data of a laid-out element for geometry queries.
type Xf3D struct {
	L        [16]float64 // local space -> container space, row-major Mat4
	Preserve bool        // transform-style: preserve-3d is in effect
}

func (x *Xf3D) mat() Mat4 {
	var m Mat4
	for i := 0; i < 4; i++ {
		for j := 0; j < 4; j++ {
			m[i][j] = x.L[i*4+j]
		}
	}
	return m
}

func mat16(m Mat4) (o [16]float64) {
	for i := 0; i < 4; i++ {
		for j := 0; j < 4; j++ {
			o[i*4+j] = m[i][j]
		}
	}
	return
}

// xf3dOf builds the Box data for the main box of an element, or nil when the
// element has no 3D involvement (the 2D path then applies unchanged).
func (b *lbox) xf3dOf() *Xf3D {
	pres := b.preserves3D()
	c := b.xfContainer()
	cp := false
	if c != nil && c != b {
		_, cp = c.persp4()
	}
	if !b.has3DOp() && !pres && !cp && !(c != nil && c.preserves3D() && b.hasTransform()) {
		return nil
	}
	m, _ := b.local4(0, 0)
	return &Xf3D{L: mat16(m), Preserve: pres}
}

// projectChain maps points (z=0, document coordinates) of n through the
// transforms of n and its ancestors with Blink's accumulate / flatten rules
// and returns the screen points. ok is false when no 3D data is involved.
func (r *Result) projectChain(n *html.Node, pts [][2]float64) ([][2]float64, bool) {
	has3 := false
	for e := n; e != nil; e = e.Parent {
		if bx := r.Boxes[e]; bx != nil && bx.Xf3 != nil {
			has3 = true
			break
		}
	}
	if !has3 {
		return nil, false
	}
	type p4 struct{ x, y, z, w float64 }
	cur := make([]p4, len(pts))
	for i, p := range pts {
		cur[i] = p4{p[0], p[1], 0, 1}
	}
	flatten := func() {
		for i, q := range cur {
			if q.w != 0 && q.w != 1 {
				q.x, q.y = q.x/q.w, q.y/q.w
			}
			cur[i] = p4{q.x, q.y, 0, 1}
		}
	}
	for e := n; e != nil; e = e.Parent {
		bx := r.Boxes[e]
		var m Mat4
		have := false
		if bx != nil {
			if bx.Xf3 != nil {
				m, have = bx.Xf3.mat(), true
			} else if bx.HasTransform {
				m, have = from6(bx.Transform), true
			}
		}
		if have {
			for i, q := range cur {
				// the vector is homogeneous: the incoming w scales the translation
				cur[i] = p4{m[0][0]*q.x + m[0][1]*q.y + m[0][2]*q.z + m[0][3]*q.w,
					m[1][0]*q.x + m[1][1]*q.y + m[1][2]*q.z + m[1][3]*q.w,
					m[2][0]*q.x + m[2][1]*q.y + m[2][2]*q.z + m[2][3]*q.w,
					m[3][0]*q.x + m[3][1]*q.y + m[3][2]*q.z + m[3][3]*q.w}
			}
		}
		par := e.Parent
		preserve := false
		if par != nil {
			if pb := r.Boxes[par]; pb != nil && pb.Xf3 != nil && pb.Xf3.Preserve {
				preserve = true
			}
		}
		if !preserve {
			flatten()
		}
	}
	flatten()
	out := make([][2]float64, len(cur))
	for i, q := range cur {
		out[i] = [2]float64{q.x, q.y}
	}
	return out, true
}

func boundsOf(pts [][2]float64) Rect {
	x0, y0, x1, y1 := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, q := range pts {
		x0, x1 = math.Min(x0, q[0]), math.Max(x1, q[0])
		y0, y1 = math.Min(y0, q[1]), math.Max(y1, q[1])
	}
	return Rect{x0, y0, x1 - x0, y1 - y0}
}

func rectCorners(rc Rect) [][2]float64 {
	return [][2]float64{{rc.X, rc.Y}, {rc.Right(), rc.Y}, {rc.X, rc.Bottom()}, {rc.Right(), rc.Bottom()}}
}

// ClientRects returns the per-fragment rectangles of n as getClientRects
// reports them: every fragment (line box piece or column) projected through the
// transform chain.
func (r *Result) ClientRects(n *html.Node) []Rect {
	bx := r.Boxes[n]
	if bx == nil || bx.Display == "none" {
		return nil
	}
	frs := bx.Frags
	if len(frs) == 0 {
		frs = []Rect{bx.Border}
	}
	out := make([]Rect, 0, len(frs))
	for _, f := range frs {
		if pts, ok := r.projectChain(n, rectCorners(f)); ok {
			out = append(out, boundsOf(pts))
			continue
		}
		rc, _ := r.rectOfFrag(n, f)
		out = append(out, rc)
	}
	return out
}
