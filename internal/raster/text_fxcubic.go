package raster

// Cubic edges in Skia's two analytic walkers.
//
// SkScan_AAAPath walks a path with aaa_walk_convex_edges when
// SkPath::isConvex() (one contour that never turns back on itself) and with
// aaa_walk_edges otherwise. The general walker updates a curved edge by
//
//	cubicEdge->keepContinuous();   // fCx = fX; fSnappedY = fY
//	cubicEdge->updateCubic();
//
// so every new cubic segment starts from the x the previous *line* actually
// reached, and the forward differencing carries on from there. That x is the
// start x plus the slope times the height, and the slope is an FDot6 quotient
// (both x and y differences are truncated to 1/64 pixel), so every segment
// loses on average half a 1/64 pixel towards minus infinity and the error
// accumulates along the curve (up to a tenth of a pixel for a long arc). The
// convex walker never calls keepContinuous, and quadratic edges keep their
// forward-difference chain separate from the snapped x, so neither shows it.
// Measured against Chromium 1194 (text_fxcubic_lab_test.go): the same cubic
// is 0.15% off with an extra, convex-breaking contour absent and 6% off when
// the drift is not modelled.

// fxAdvanceX is the x an edge starting at (x, y0) with slope reaches at y1
// when the general walker steps it row by row: goY(nextY, yShift) adds the
// slope shifted down by 2 for a quarter row step, by 1 for a half row step,
// and the whole slope for a full row.
func fxAdvanceX(x, slope, y0, y1 int) int {
	for y := y0; y < y1; {
		next := y&^(fxOne-1) + fxOne // SkFixedCeilToFixed(y + 1), the 1 being one unit of 16.16
		if next > y1 {
			next = y1
		}
		d := next - y
		switch {
		case d&(fxOne>>2) != 0:
			next = y + fxOne>>2
			x += slope >> 2
		case d&(fxOne>>1) != 0:
			x += slope >> 1
		default:
			x += slope
		}
		y = next
	}
	return x
}

// pathIsConvex mirrors SkPathPriv::ComputeConvexity: a single contour whose
// control polygon never changes turning direction (it may double back at
// most twice). Anything else takes the general walker.
func pathIsConvex(p *Path) bool {
	type pt struct{ x, y float32 }
	var pts []pt
	var verbs []byte
	i := 0
	for _, c := range p.Cmds {
		switch c {
		case 'M', 'L':
			pts = append(pts, pt{float32(p.Pts[i]), float32(p.Pts[i+1])})
			i += 2
		case 'Q':
			pts = append(pts, pt{float32(p.Pts[i]), float32(p.Pts[i+1])}, pt{float32(p.Pts[i+2]), float32(p.Pts[i+3])})
			i += 4
		case 'C':
			pts = append(pts, pt{float32(p.Pts[i]), float32(p.Pts[i+1])}, pt{float32(p.Pts[i+2]), float32(p.Pts[i+3])}, pt{float32(p.Pts[i+4]), float32(p.Pts[i+5])})
			i += 6
		case 'Z':
		default:
			continue
		}
		verbs = append(verbs, c)
	}
	// trailing moves are dropped
	for len(verbs) > 0 && verbs[len(verbs)-1] == 'M' {
		verbs = verbs[:len(verbs)-1]
		pts = pts[:len(pts)-1]
	}
	if len(verbs) == 0 {
		return true
	}
	// IsConcaveBySign: more than three sign changes along x or y
	if len(pts) > 3 {
		sign := func(v float32) int {
			if v < 0 {
				return 1
			}
			return 0
		}
		cur := pts[0]
		first := pts[0]
		rest := pts[1:]
		dxes, dyes, lastSx, lastSy := 0, 0, 2, 2
		visit := func(q pt) bool {
			vx, vy := q.x-cur.x, q.y-cur.y
			if vx != 0 || vy != 0 {
				sx, sy := sign(vx), sign(vy)
				if sx != lastSx {
					dxes++
				}
				if sy != lastSy {
					dyes++
				}
				if dxes > 3 || dyes > 3 {
					return false
				}
				lastSx, lastSy = sx, sy
			}
			return true
		}
		for _, q := range rest {
			if !visit(q) {
				return false
			}
			cur = q
		}
		// second pass: only the closing vector back to the first point
		if !visit(first) {
			return false
		}
	}
	// Convexicator
	const (
		dirNone = iota
		dirLeft
		dirRight
		dirStraight
		dirBack
	)
	var firstPt, lastPt, firstVec, lastVec pt
	expected := dirNone
	reversals := 0
	hasDir := false
	cross := func(a, b pt) float32 { return float32(a.x*b.y) - float32(a.y*b.x) }
	addVec := func(v pt) bool {
		cr := cross(lastVec, v)
		var d int
		switch {
		case cr != cr:
			return true
		case cr == 0:
			if float32(lastVec.x*v.x)+float32(lastVec.y*v.y) < 0 {
				d = dirBack
			} else {
				d = dirStraight
			}
		case cr > 0:
			d = dirRight
		default:
			d = dirLeft
		}
		switch d {
		case dirLeft, dirRight:
			if expected == dirNone {
				expected = d
				hasDir = true
			} else if d != expected {
				hasDir = false
				return false
			}
			lastVec = v
		case dirBack:
			lastVec = v
			reversals++
			return reversals < 3
		}
		return true
	}
	addPt := func(q pt) bool {
		if lastPt == q {
			return true
		}
		if firstPt == lastPt && expected == dirNone && lastVec == (pt{}) {
			lastVec = pt{q.x - lastPt.x, q.y - lastPt.y}
			firstVec = lastVec
		} else if !addVec(pt{q.x - lastPt.x, q.y - lastPt.y}) {
			return false
		}
		lastPt = q
		return true
	}
	closeC := func() bool { return addPt(firstPt) && addVec(firstVec) }
	contour := 0
	needsClose := false
	k := 0
	for _, v := range verbs {
		n := 0
		switch v {
		case 'M', 'L':
			n = 1
		case 'Q':
			n = 2
		case 'C':
			n = 3
		}
		vp := pts[k : k+n]
		k += n
		if contour == 0 {
			if v == 'M' {
				firstPt, lastPt = vp[0], vp[0]
				expected = dirNone
			} else {
				contour++
				needsClose = true
			}
		}
		if contour == 1 {
			if v == 'Z' || v == 'M' {
				if !closeC() {
					return false
				}
				needsClose = false
				contour++
			} else {
				for _, q := range vp {
					if !addPt(q) {
						return false
					}
				}
			}
		} else if v != 'M' {
			return false
		}
	}
	if needsClose && !closeC() {
		return false
	}
	return hasDir || reversals < 3
}
