package render

import "math"

// Paint-offset snapping.
//
// Chromium paints every box relative to the origin of its nearest "paint offset
// translation" container. An element with a transform (other than a pure
// translation), with will-change: transform or with contain: paint gets such a
// container: its paint offset is rounded to a whole pixel before the transform
// is applied, and the fractional remainder is dropped, so its content starts
// on a pixel boundary of its own local space (left: 10.3px paints at 10, 10.5
// at 11). A translation-only transform (including translateZ, identity
// matrices and 360deg rotations) rounds the same way for the box itself but
// keeps the fraction for the content, so the content is snapped on the parent
// lattice. Opacity, filters, perspective, clip-path, mask, overflow and
// position: fixed do not change the paint offset. Measured against Chromium
// 1194 with testdata/fidelity/xformsnap.html.

func translationOnly(m [6]float64) bool {
	const eps = 1e-9
	return math.Abs(m[0]-1) < eps && math.Abs(m[3]-1) < eps && math.Abs(m[1]) < eps && math.Abs(m[2]) < eps
}

// dropsFraction reports whether b opens a local pixel lattice (its subpixel
// paint offset is discarded for its content).
func (b *lbox) dropsFraction() bool {
	st := b.st
	if b.hasTransform() && !translationOnly(b.matrixFor(b.w, b.h)) {
		return true
	}
	if st.WillChange != "" && contains(st.WillChange, "transform") {
		return true
	}
	if st.Contain != "" && (contains(st.Contain, "paint") || contains(st.Contain, "strict") || contains(st.Contain, "content")) {
		return true
	}
	return false
}

// frameMatrix returns the paint matrix of a transformed (or lattice-opening)
// box and the lattice origin for its content. The box origin A is moved to the
// rounded position A' on the parent lattice before the transform is applied.
func (p *painter) frameMatrix(b *lbox, drop bool) (M [6]float64, latX, latY float64) {
	dpr := p.c.opts.DPR
	if dpr <= 0 {
		dpr = 1
	}
	latX, latY = p.latX, p.latY
	if !drop {
		if b.hasTransform() {
			return b.fullMatrix(), latX, latY
		}
		return identity, latX, latY
	}
	rx := p.latX + math.Round((b.ax-p.latX)*dpr)/dpr
	ry := p.latY + math.Round((b.ay-p.latY)*dpr)/dpr
	latX, latY = b.ax, b.ay
	if !b.hasTransform() {
		return [6]float64{1, 0, 0, 1, rx - b.ax, ry - b.ay}, latX, latY
	}
	m := b.matrixFor(b.w, b.h)
	bx, by, bw, bh := b.refBox()
	ox := b.st.TransformOrigin[0].Resolve(bw) + bx - b.ax
	oy := b.st.TransformOrigin[1].Resolve(bh) + by - b.ay
	px, py := b.ax+ox, b.ay+oy
	qx, qy := rx+ox, ry+oy
	e := m[4] + qx - (m[0]*px + m[2]*py)
	f := m[5] + qy - (m[1]*px + m[3]*py)
	return [6]float64{m[0], m[1], m[2], m[3], e, f}, latX, latY
}
