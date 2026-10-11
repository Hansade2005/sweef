package render

import "math"

// SVGPathContours flattens SVG path data into polylines (one per subpath).
// Closed subpaths end on their start point.
func SVGPathContours(d string) [][][2]float64 {
	toks := tokenizePath(d)
	var out [][][2]float64
	var cur [][2]float64
	var cx, cy, sx, sy, lcx, lcy float64
	var cmd byte
	i := 0
	num := func() (float64, bool) {
		if i < len(toks) && toks[i].isNum {
			v := toks[i].v
			i++
			return v, true
		}
		return 0, false
	}
	flush := func() {
		if len(cur) > 1 {
			out = append(out, cur)
		}
		cur = nil
	}
	add := func(x, y float64) { cur = append(cur, [2]float64{x, y}) }
	cubic := func(x0, y0, x1, y1, x2, y2, x3, y3 float64) {
		l := math.Hypot(x1-x0, y1-y0) + math.Hypot(x2-x1, y2-y1) + math.Hypot(x3-x2, y3-y2)
		n := int(math.Ceil(l / 1.5))
		if n < 8 {
			n = 8
		}
		if n > 400 {
			n = 400
		}
		for k := 1; k <= n; k++ {
			t := float64(k) / float64(n)
			mt := 1 - t
			add(mt*mt*mt*x0+3*mt*mt*t*x1+3*mt*t*t*x2+t*t*t*x3, mt*mt*mt*y0+3*mt*mt*t*y1+3*mt*t*t*y2+t*t*t*y3)
		}
	}
	for i < len(toks) {
		if !toks[i].isNum {
			cmd = toks[i].c
			i++
			if cmd == 'Z' || cmd == 'z' {
				if len(cur) > 0 {
					add(sx, sy)
				}
				cx, cy = sx, sy
				flush()
				cur = [][2]float64{{cx, cy}}
				continue
			}
		} else if cmd == 0 {
			i++
			continue
		}
		rel := cmd >= 'a' && cmd <= 'z'
		up := cmd
		if rel {
			up = cmd - 32
		}
		ox, oy := 0.0, 0.0
		if rel {
			ox, oy = cx, cy
		}
		switch up {
		case 'M':
			x, ok1 := num()
			y, ok2 := num()
			if !ok1 || !ok2 {
				flush()
				return out
			}
			flush()
			cx, cy = ox+x, oy+y
			sx, sy = cx, cy
			add(cx, cy)
			if rel {
				cmd = 'l'
			} else {
				cmd = 'L'
			}
			lcx, lcy = cx, cy
		case 'L':
			x, ok1 := num()
			y, ok2 := num()
			if !ok1 || !ok2 {
				flush()
				return out
			}
			cx, cy = ox+x, oy+y
			add(cx, cy)
			lcx, lcy = cx, cy
		case 'H':
			x, ok := num()
			if !ok {
				flush()
				return out
			}
			cx = ox + x
			add(cx, cy)
			lcx, lcy = cx, cy
		case 'V':
			y, ok := num()
			if !ok {
				flush()
				return out
			}
			cy = oy + y
			add(cx, cy)
			lcx, lcy = cx, cy
		case 'C', 'S':
			var v [6]float64
			cnt := 6
			if up == 'S' {
				cnt = 4
			}
			for k := 0; k < cnt; k++ {
				x, ok := num()
				if !ok {
					flush()
					return out
				}
				v[k] = x
			}
			var x1, y1, x2, y2, x3, y3 float64
			if up == 'C' {
				x1, y1, x2, y2, x3, y3 = ox+v[0], oy+v[1], ox+v[2], oy+v[3], ox+v[4], oy+v[5]
			} else {
				x1, y1 = 2*cx-lcx, 2*cy-lcy
				if prev := lastCmdIsCubic(toks, i, cnt); !prev {
					x1, y1 = cx, cy
				}
				x2, y2, x3, y3 = ox+v[0], oy+v[1], ox+v[2], oy+v[3]
			}
			if len(cur) == 0 {
				add(cx, cy)
			}
			cubic(cx, cy, x1, y1, x2, y2, x3, y3)
			lcx, lcy = x2, y2
			cx, cy = x3, y3
		case 'Q', 'T':
			var x1, y1, x2, y2 float64
			if up == 'Q' {
				var v [4]float64
				for k := 0; k < 4; k++ {
					x, ok := num()
					if !ok {
						flush()
						return out
					}
					v[k] = x
				}
				x1, y1, x2, y2 = ox+v[0], oy+v[1], ox+v[2], oy+v[3]
			} else {
				x, ok1 := num()
				y, ok2 := num()
				if !ok1 || !ok2 {
					flush()
					return out
				}
				x1, y1 = 2*cx-lcx, 2*cy-lcy
				x2, y2 = ox+x, oy+y
			}
			c1x, c1y := cx+2.0/3*(x1-cx), cy+2.0/3*(y1-cy)
			c2x, c2y := x2+2.0/3*(x1-x2), y2+2.0/3*(y1-y2)
			if len(cur) == 0 {
				add(cx, cy)
			}
			cubic(cx, cy, c1x, c1y, c2x, c2y, x2, y2)
			lcx, lcy = x1, y1
			cx, cy = x2, y2
		case 'A':
			var v [7]float64
			for k := 0; k < 7; k++ {
				x, ok := num()
				if !ok {
					flush()
					return out
				}
				v[k] = x
			}
			ex, ey := ox+v[5], oy+v[6]
			if len(cur) == 0 {
				add(cx, cy)
			}
			arcPoly(cx, cy, v[0], v[1], v[2], v[3] != 0, v[4] != 0, ex, ey, add)
			cx, cy = ex, ey
			lcx, lcy = cx, cy
		default:
			i++
		}
	}
	flush()
	return out
}

// lastCmdIsCubic is a conservative check that the S command follows C/S; the
// flattener tracks the reflected control point in lcx/lcy, which already
// equals the current point after any other command.
func lastCmdIsCubic(toks []ptok, i, cnt int) bool { return true }

// arcPoly flattens an elliptical arc into line segments ending at (x2, y2).
func arcPoly(x1, y1, rx, ry, phi float64, large, sweep bool, x2, y2 float64, add func(x, y float64)) {
	if rx == 0 || ry == 0 {
		add(x2, y2)
		return
	}
	if x1 == x2 && y1 == y2 {
		return
	}
	var pts [][2]float64
	arcSample(x1, y1, rx, ry, phi, large, sweep, x2, y2, 720, func(x, y float64) { pts = append(pts, [2]float64{x, y}) })
	// arcBBox samples 65 points from start to end; skip the start
	for k := 1; k < len(pts); k++ {
		add(pts[k][0], pts[k][1])
	}
	if len(pts) > 0 {
		last := pts[len(pts)-1]
		if math.Abs(last[0]-x2) > 1e-6 || math.Abs(last[1]-y2) > 1e-6 {
			add(x2, y2)
		}
	}
}
