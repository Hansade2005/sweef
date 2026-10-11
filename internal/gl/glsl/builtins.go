package glsl

import (
	"math"
	"math/bits"
)

func isGenF(t Type) bool { return t.K == KFloat && t.Arr == 0 && t.M == 1 }
func isGenI(t Type) bool { return (t.K == KInt || t.K == KUint) && t.Arr == 0 && t.M == 1 }

func f32(x float64) float32 { return float32(x) }

var unaryF = map[string]func(float32) float32{
	"radians":     func(x float32) float32 { return x * (math.Pi / 180) },
	"degrees":     func(x float32) float32 { return x * (180 / math.Pi) },
	"sin":         func(x float32) float32 { return f32(math.Sin(float64(x))) },
	"cos":         func(x float32) float32 { return f32(math.Cos(float64(x))) },
	"tan":         func(x float32) float32 { return f32(math.Tan(float64(x))) },
	"asin":        func(x float32) float32 { return f32(math.Asin(float64(x))) },
	"acos":        func(x float32) float32 { return f32(math.Acos(float64(x))) },
	"atan":        func(x float32) float32 { return f32(math.Atan(float64(x))) },
	"sinh":        func(x float32) float32 { return f32(math.Sinh(float64(x))) },
	"cosh":        func(x float32) float32 { return f32(math.Cosh(float64(x))) },
	"tanh":        func(x float32) float32 { return f32(math.Tanh(float64(x))) },
	"asinh":       func(x float32) float32 { return f32(math.Asinh(float64(x))) },
	"acosh":       func(x float32) float32 { return f32(math.Acosh(float64(x))) },
	"atanh":       func(x float32) float32 { return f32(math.Atanh(float64(x))) },
	"exp":         func(x float32) float32 { return f32(math.Exp(float64(x))) },
	"log":         func(x float32) float32 { return f32(math.Log(float64(x))) },
	"exp2":        func(x float32) float32 { return f32(math.Exp2(float64(x))) },
	"log2":        func(x float32) float32 { return f32(math.Log2(float64(x))) },
	"sqrt":        func(x float32) float32 { return f32(math.Sqrt(float64(x))) },
	"inversesqrt": func(x float32) float32 { return f32(1 / math.Sqrt(float64(x))) },
	"floor":       func(x float32) float32 { return f32(math.Floor(float64(x))) },
	"ceil":        func(x float32) float32 { return f32(math.Ceil(float64(x))) },
	"fract":       func(x float32) float32 { return x - f32(math.Floor(float64(x))) },
	"trunc":       func(x float32) float32 { return f32(math.Trunc(float64(x))) },
	"round":       func(x float32) float32 { return f32(math.Floor(float64(x) + 0.5)) },
	"roundEven":   func(x float32) float32 { return f32(math.RoundToEven(float64(x))) },
	"abs":         func(x float32) float32 { return f32(math.Abs(float64(x))) },
	"sign": func(x float32) float32 {
		if x > 0 {
			return 1
		} else if x < 0 {
			return -1
		}
		return 0
	},
}

func lanewise1(d, a, n int, fn func(float32) float32) op {
	if n == 1 {
		return func(f *Frame) { f.R[d] = bf(fn(fl(f.R[a]))) }
	}
	return func(f *Frame) {
		r := f.R
		for i := 0; i < n; i++ {
			r[d+i] = bf(fn(fl(r[a+i])))
		}
	}
}

func lanewise2(d, a, b, n, sa, sb int, fn func(x, y float32) float32) op {
	return func(f *Frame) {
		r := f.R
		for i := 0; i < n; i++ {
			r[d+i] = bf(fn(fl(r[a+i*sa]), fl(r[b+i*sb])))
		}
	}
}

func lanewise3(d, a, b, cc, n, sa, sb, sc int, fn func(x, y, z float32) float32) op {
	return func(f *Frame) {
		r := f.R
		for i := 0; i < n; i++ {
			r[d+i] = bf(fn(fl(r[a+i*sa]), fl(r[b+i*sb]), fl(r[cc+i*sc])))
		}
	}
}

func fmin(x, y float32) float32 {
	if y < x {
		return y
	}
	return x
}
func fmax(x, y float32) float32 {
	if y > x {
		return y
	}
	return x
}

func stride(t Type, n int) int {
	if t.Size() == 1 && n > 1 {
		return 0
	}
	return 1
}

func cat(vs ...*V) []op {
	var ops []op
	for _, v := range vs {
		ops = append(ops, v.Ops...)
	}
	return ops
}

func (c *compiler) noMatch(e *Expr) {
	c.fail(e.Line, e.S, "no matching overloaded function found")
}

func (c *compiler) extEnabled(name string) bool {
	b := c.exts[name]
	return (b == "enable" || b == "require" || b == "warn") && c.opt.Extensions[name]
}

func (c *compiler) builtin(e *Expr, h int) *V {
	name := e.S
	args := make([]*V, len(e.Args))
	for i, a := range e.Args {
		args[i] = c.mat(c.expr(a))
	}
	nargs := len(args)
	line := e.Line
	if v := c.textureFn(e, args, h); v != nil {
		return v
	}
	a0 := func() Type { return args[0].T }
	need := func(n int) {
		if nargs != n {
			c.noMatch(e)
		}
	}
	mk := func(t Type, d int, ops []op) *V {
		return c.foldIf(&V{T: t, Off: d, Ops: ops}, args...)
	}
	if fn, ok := unaryF[name]; ok && name != "atan" || ok && nargs == 1 {
		need(1)
		t := a0()
		if name == "abs" || name == "sign" {
			if isGenI(t) && c.version == 300 {
				n := t.Size()
				d := c.dst(h, n)
				s := args[0].Off
				isAbs := name == "abs"
				o := func(f *Frame) {
					for i := 0; i < n; i++ {
						v := int32(f.R[s+i])
						if isAbs {
							if v < 0 {
								v = -v
							}
						} else if v > 0 {
							v = 1
						} else if v < 0 {
							v = -1
						}
						f.R[d+i] = uint32(v)
					}
				}
				return mk(t, d, append(cat(args...), o))
			}
		}
		if !isGenF(t) {
			c.noMatch(e)
		}
		if (name == "sinh" || name == "cosh" || name == "tanh" || name == "asinh" || name == "acosh" || name == "atanh" ||
			name == "trunc" || name == "round" || name == "roundEven") && c.version != 300 {
			c.noMatch(e)
		}
		n := t.Size()
		d := c.dst(h, n)
		return mk(t, d, append(cat(args...), lanewise1(d, args[0].Off, n, fn)))
	}
	switch name {
	case "atan":
		need(2)
		return c.gen2(e, args, h, func(y, x float32) float32 { return f32(math.Atan2(float64(y), float64(x))) }, false)
	case "pow":
		need(2)
		return c.gen2(e, args, h, func(x, y float32) float32 { return f32(math.Pow(float64(x), float64(y))) }, false)
	case "mod":
		need(2)
		return c.gen2(e, args, h, func(x, y float32) float32 { return x - y*f32(math.Floor(float64(x/y))) }, true)
	case "min", "max":
		need(2)
		t := a0()
		if isGenI(t) && c.version == 300 {
			b := args[1].T
			if !(sameType(t, b) || b.IsScalar() && b.K == t.K) {
				c.noMatch(e)
			}
			n := t.Size()
			d := c.dst(h, n)
			sa, sb := stride(t, n), stride(b, n)
			ao, bo := args[0].Off, args[1].Off
			isMin, uns := name == "min", t.K == KUint
			o := func(f *Frame) {
				for i := 0; i < n; i++ {
					x, y := f.R[ao+i*sa], f.R[bo+i*sb]
					var less bool
					if uns {
						less = y < x
					} else {
						less = int32(y) < int32(x)
					}
					if less == isMin {
						f.R[d+i] = y
					} else {
						f.R[d+i] = x
					}
				}
			}
			return mk(t, d, append(cat(args...), o))
		}
		if name == "min" {
			return c.gen2(e, args, h, fmin, true)
		}
		return c.gen2(e, args, h, fmax, true)
	case "step":
		need(2)
		// step(edge, x): edge may be scalar
		t, b := args[0].T, args[1].T
		if !isGenF(t) || !isGenF(b) || !(sameType(t, b) || t.IsScalar()) {
			c.noMatch(e)
		}
		n := b.Size()
		d := c.dst(h, n)
		o := lanewise2(d, args[0].Off, args[1].Off, n, stride(t, n), 1, func(edge, x float32) float32 {
			if x < edge {
				return 0
			}
			return 1
		})
		return mk(b, d, append(cat(args...), o))
	case "clamp":
		need(3)
		t := a0()
		if isGenI(t) && c.version == 300 {
			n := t.Size()
			d := c.dst(h, n)
			s1, s2 := stride(args[1].T, n), stride(args[2].T, n)
			ao, lo, hi := args[0].Off, args[1].Off, args[2].Off
			uns := t.K == KUint
			o := func(f *Frame) {
				for i := 0; i < n; i++ {
					x, l, hh := f.R[ao+i], f.R[lo+i*s1], f.R[hi+i*s2]
					if uns {
						x = min(max(x, l), hh)
					} else {
						x = uint32(min(max(int32(x), int32(l)), int32(hh)))
					}
					f.R[d+i] = x
				}
			}
			return mk(t, d, append(cat(args...), o))
		}
		if !isGenF(t) || !(sameType(t, args[1].T) && sameType(t, args[2].T) || args[1].T == tFloat && args[2].T == tFloat) {
			c.noMatch(e)
		}
		n := t.Size()
		d := c.dst(h, n)
		o := lanewise3(d, args[0].Off, args[1].Off, args[2].Off, n, 1, stride(args[1].T, n), stride(args[2].T, n),
			func(x, lo, hi float32) float32 { return fmin(fmax(x, lo), hi) })
		return mk(t, d, append(cat(args...), o))
	case "mix":
		need(3)
		t := a0()
		if !isGenF(t) || !sameType(t, args[1].T) {
			c.noMatch(e)
		}
		n := t.Size()
		d := c.dst(h, n)
		if args[2].T.K == KBool && c.version == 300 {
			if !sameType(vecT(KBool, t.N), args[2].T) && !(t.N == 1 && args[2].T == tBool) {
				c.noMatch(e)
			}
			x, y, s := args[0].Off, args[1].Off, args[2].Off
			o := func(f *Frame) {
				for i := 0; i < n; i++ {
					if f.R[s+i] != 0 {
						f.R[d+i] = f.R[y+i]
					} else {
						f.R[d+i] = f.R[x+i]
					}
				}
			}
			return mk(t, d, append(cat(args...), o))
		}
		if !(sameType(t, args[2].T) || args[2].T == tFloat) {
			c.noMatch(e)
		}
		o := lanewise3(d, args[0].Off, args[1].Off, args[2].Off, n, 1, 1, stride(args[2].T, n),
			func(x, y, a float32) float32 { return x*(1-a) + y*a })
		return mk(t, d, append(cat(args...), o))
	case "smoothstep":
		need(3)
		t := args[2].T
		if !isGenF(t) || !(sameType(t, args[0].T) && sameType(t, args[1].T) || args[0].T == tFloat && args[1].T == tFloat) {
			c.noMatch(e)
		}
		n := t.Size()
		d := c.dst(h, n)
		o := lanewise3(d, args[0].Off, args[1].Off, args[2].Off, n, stride(args[0].T, n), stride(args[1].T, n), 1,
			func(e0, e1, x float32) float32 {
				tt := fmin(fmax((x-e0)/(e1-e0), 0), 1)
				return tt * tt * (3 - 2*tt)
			})
		return mk(t, d, append(cat(args...), o))
	case "length":
		need(1)
		t := a0()
		if !isGenF(t) {
			c.noMatch(e)
		}
		n := t.N
		d := c.dst(h, 1)
		s := args[0].Off
		o := func(f *Frame) {
			var sum float32
			for i := 0; i < n; i++ {
				v := fl(f.R[s+i])
				sum += v * v
			}
			f.R[d] = bf(f32(math.Sqrt(float64(sum))))
		}
		return mk(tFloat, d, append(cat(args...), o))
	case "distance":
		need(2)
		t := a0()
		if !isGenF(t) || !sameType(t, args[1].T) {
			c.noMatch(e)
		}
		n := t.N
		d := c.dst(h, 1)
		a, b := args[0].Off, args[1].Off
		o := func(f *Frame) {
			var sum float32
			for i := 0; i < n; i++ {
				v := fl(f.R[a+i]) - fl(f.R[b+i])
				sum += v * v
			}
			f.R[d] = bf(f32(math.Sqrt(float64(sum))))
		}
		return mk(tFloat, d, append(cat(args...), o))
	case "dot":
		need(2)
		t := a0()
		if !isGenF(t) || !sameType(t, args[1].T) {
			c.noMatch(e)
		}
		n := t.N
		d := c.dst(h, 1)
		a, b := args[0].Off, args[1].Off
		var o op
		switch n {
		case 2:
			o = func(f *Frame) { r := f.R; r[d] = bf(fl(r[a])*fl(r[b]) + fl(r[a+1])*fl(r[b+1])) }
		case 3:
			o = func(f *Frame) {
				r := f.R
				r[d] = bf(fl(r[a])*fl(r[b]) + fl(r[a+1])*fl(r[b+1]) + fl(r[a+2])*fl(r[b+2]))
			}
		case 4:
			o = func(f *Frame) {
				r := f.R
				r[d] = bf(fl(r[a])*fl(r[b]) + fl(r[a+1])*fl(r[b+1]) + fl(r[a+2])*fl(r[b+2]) + fl(r[a+3])*fl(r[b+3]))
			}
		default:
			o = func(f *Frame) { f.R[d] = bf(fl(f.R[a]) * fl(f.R[b])) }
		}
		return mk(tFloat, d, append(cat(args...), o))
	case "cross":
		need(2)
		if args[0].T != vecT(KFloat, 3) || args[1].T != vecT(KFloat, 3) {
			c.noMatch(e)
		}
		d := c.dst(h, 3)
		if h >= 0 {
			d = c.tmp(3)
		}
		a, b := args[0].Off, args[1].Off
		o := func(f *Frame) {
			r := f.R
			ax, ay, az := fl(r[a]), fl(r[a+1]), fl(r[a+2])
			bx, by, bz := fl(r[b]), fl(r[b+1]), fl(r[b+2])
			r[d], r[d+1], r[d+2] = bf(ay*bz-az*by), bf(az*bx-ax*bz), bf(ax*by-ay*bx)
		}
		return mk(vecT(KFloat, 3), d, append(cat(args...), o))
	case "normalize":
		need(1)
		t := a0()
		if !isGenF(t) {
			c.noMatch(e)
		}
		n := t.N
		d := c.dst(h, n)
		s := args[0].Off
		o := func(f *Frame) {
			var sum float32
			for i := 0; i < n; i++ {
				v := fl(f.R[s+i])
				sum += v * v
			}
			inv := f32(1 / math.Sqrt(float64(sum)))
			for i := 0; i < n; i++ {
				f.R[d+i] = bf(fl(f.R[s+i]) * inv)
			}
		}
		return mk(t, d, append(cat(args...), o))
	case "faceforward":
		need(3)
		t := a0()
		if !isGenF(t) || !sameType(t, args[1].T) || !sameType(t, args[2].T) {
			c.noMatch(e)
		}
		n := t.N
		d := c.dst(h, n)
		nn, i, nref := args[0].Off, args[1].Off, args[2].Off
		o := func(f *Frame) {
			var dt float32
			for k := 0; k < n; k++ {
				dt += fl(f.R[nref+k]) * fl(f.R[i+k])
			}
			for k := 0; k < n; k++ {
				v := fl(f.R[nn+k])
				if dt >= 0 {
					v = -v
				}
				f.R[d+k] = bf(v)
			}
		}
		return mk(t, d, append(cat(args...), o))
	case "reflect":
		need(2)
		t := a0()
		if !isGenF(t) || !sameType(t, args[1].T) {
			c.noMatch(e)
		}
		n := t.N
		d := c.dst(h, n)
		if h >= 0 {
			d = c.tmp(n)
		}
		i, nn := args[0].Off, args[1].Off
		o := func(f *Frame) {
			var dt float32
			for k := 0; k < n; k++ {
				dt += fl(f.R[nn+k]) * fl(f.R[i+k])
			}
			for k := 0; k < n; k++ {
				f.R[d+k] = bf(fl(f.R[i+k]) - 2*dt*fl(f.R[nn+k]))
			}
		}
		return mk(t, d, append(cat(args...), o))
	case "refract":
		need(3)
		t := a0()
		if !isGenF(t) || !sameType(t, args[1].T) || args[2].T != tFloat {
			c.noMatch(e)
		}
		n := t.N
		d := c.dst(h, n)
		if h >= 0 {
			d = c.tmp(n)
		}
		i, nn, eta := args[0].Off, args[1].Off, args[2].Off
		o := func(f *Frame) {
			var dt float32
			for k := 0; k < n; k++ {
				dt += fl(f.R[nn+k]) * fl(f.R[i+k])
			}
			et := fl(f.R[eta])
			kk := 1 - et*et*(1-dt*dt)
			if kk < 0 {
				for k := 0; k < n; k++ {
					f.R[d+k] = 0
				}
				return
			}
			sq := f32(math.Sqrt(float64(kk)))
			for k := 0; k < n; k++ {
				f.R[d+k] = bf(et*fl(f.R[i+k]) - (et*dt+sq)*fl(f.R[nn+k]))
			}
		}
		return mk(t, d, append(cat(args...), o))
	case "matrixCompMult":
		need(2)
		if !args[0].T.IsMat() || !sameType(args[0].T, args[1].T) {
			c.noMatch(e)
		}
		n := args[0].T.Size()
		d := c.dst(h, n)
		return mk(args[0].T, d, append(cat(args...), lanewise2(d, args[0].Off, args[1].Off, n, 1, 1, func(x, y float32) float32 { return x * y })))
	case "outerProduct":
		need(2)
		ta, tb := args[0].T, args[1].T
		if !isGenF(ta) || !isGenF(tb) || ta.N < 2 || tb.N < 2 {
			c.noMatch(e)
		}
		rt := matT(tb.N, ta.N)
		d := c.tmp(rt.Size())
		cc, rr, a, b := tb.N, ta.N, args[0].Off, args[1].Off
		o := func(f *Frame) {
			for i := 0; i < cc; i++ {
				for j := 0; j < rr; j++ {
					f.R[d+i*rr+j] = bf(fl(f.R[a+j]) * fl(f.R[b+i]))
				}
			}
		}
		return mk(rt, d, append(cat(args...), o))
	case "transpose":
		need(1)
		t := a0()
		if !t.IsMat() {
			c.noMatch(e)
		}
		rt := matT(t.N, t.M)
		d := c.tmp(rt.Size())
		cc, rr, s := t.M, t.N, args[0].Off
		o := func(f *Frame) {
			for i := 0; i < cc; i++ {
				for j := 0; j < rr; j++ {
					f.R[d+j*cc+i] = f.R[s+i*rr+j]
				}
			}
		}
		return mk(rt, d, append(cat(args...), o))
	case "determinant":
		need(1)
		t := a0()
		if !t.IsMat() || t.M != t.N {
			c.noMatch(e)
		}
		d := c.dst(h, 1)
		s, n := args[0].Off, t.N
		o := func(f *Frame) {
			var m [16]float32
			for i := 0; i < n*n; i++ {
				m[i] = fl(f.R[s+i])
			}
			f.R[d] = bf(det(m[:], n))
		}
		return mk(tFloat, d, append(cat(args...), o))
	case "inverse":
		need(1)
		t := a0()
		if !t.IsMat() || t.M != t.N || c.version != 300 {
			c.noMatch(e)
		}
		d := c.tmp(t.Size())
		s, n := args[0].Off, t.N
		o := func(f *Frame) {
			var m, out [16]float32
			for i := 0; i < n*n; i++ {
				m[i] = fl(f.R[s+i])
			}
			inverse(m[:], out[:], n)
			for i := 0; i < n*n; i++ {
				f.R[d+i] = bf(out[i])
			}
		}
		return mk(t, d, append(cat(args...), o))
	case "lessThan", "lessThanEqual", "greaterThan", "greaterThanEqual", "equal", "notEqual":
		need(2)
		t := args[0].T
		if !sameType(t, args[1].T) || !t.IsVec() || (t.K == KBool && name != "equal" && name != "notEqual") {
			c.noMatch(e)
		}
		n := t.N
		d := c.dst(h, n)
		a, b, k := args[0].Off, args[1].Off, t.K
		o := func(f *Frame) {
			r := f.R
			for i := 0; i < n; i++ {
				var lt, eq bool
				switch k {
				case KFloat:
					x, y := fl(r[a+i]), fl(r[b+i])
					lt, eq = x < y, x == y
				case KInt:
					x, y := int32(r[a+i]), int32(r[b+i])
					lt, eq = x < y, x == y
				default:
					x, y := r[a+i], r[b+i]
					lt, eq = x < y, x == y
				}
				var res bool
				switch name {
				case "lessThan":
					res = lt
				case "lessThanEqual":
					res = lt || eq
				case "greaterThan":
					res = !lt && !eq
				case "greaterThanEqual":
					res = !lt
				case "equal":
					res = eq
				default:
					res = !eq
				}
				if (name == "greaterThan" || name == "greaterThanEqual") && k == KFloat {
					x, y := fl(r[a+i]), fl(r[b+i])
					if name == "greaterThan" {
						res = x > y
					} else {
						res = x >= y
					}
				}
				if (name == "lessThanEqual") && k == KFloat {
					res = fl(r[a+i]) <= fl(r[b+i])
				}
				r[d+i] = b2u(res)
			}
		}
		return mk(vecT(KBool, n), d, append(cat(args...), o))
	case "any", "all":
		need(1)
		t := a0()
		if t.K != KBool || !t.IsVec() {
			c.noMatch(e)
		}
		n, s, d := t.N, args[0].Off, c.dst(h, 1)
		isAny := name == "any"
		o := func(f *Frame) {
			res := !isAny
			for i := 0; i < n; i++ {
				if isAny && f.R[s+i] != 0 {
					res = true
				}
				if !isAny && f.R[s+i] == 0 {
					res = false
				}
			}
			f.R[d] = b2u(res)
		}
		return mk(tBool, d, append(cat(args...), o))
	case "not":
		need(1)
		t := a0()
		if t.K != KBool || !t.IsVec() {
			c.noMatch(e)
		}
		n, s, d := t.N, args[0].Off, c.dst(h, t.N)
		o := func(f *Frame) {
			for i := 0; i < n; i++ {
				f.R[d+i] = f.R[s+i] ^ 1
			}
		}
		return mk(t, d, append(cat(args...), o))
	case "isnan", "isinf":
		need(1)
		t := a0()
		if !isGenF(t) || c.version != 300 {
			c.noMatch(e)
		}
		n, s, d := t.Size(), args[0].Off, c.dst(h, t.Size())
		isn := name == "isnan"
		o := func(f *Frame) {
			for i := 0; i < n; i++ {
				v := fl(f.R[s+i])
				if isn {
					f.R[d+i] = b2u(v != v)
				} else {
					f.R[d+i] = b2u(math.IsInf(float64(v), 0))
				}
			}
		}
		return mk(Type{K: KBool, N: t.N, M: 1}, d, append(cat(args...), o))
	case "floatBitsToInt", "floatBitsToUint", "intBitsToFloat", "uintBitsToFloat":
		need(1)
		t := a0()
		if c.version != 300 {
			c.noMatch(e)
		}
		var rt Type
		switch name {
		case "floatBitsToInt":
			rt = Type{K: KInt, N: t.N, M: 1}
			if !isGenF(t) {
				c.noMatch(e)
			}
		case "floatBitsToUint":
			rt = Type{K: KUint, N: t.N, M: 1}
			if !isGenF(t) {
				c.noMatch(e)
			}
		case "intBitsToFloat":
			rt = Type{K: KFloat, N: t.N, M: 1}
			if t.K != KInt || t.M != 1 {
				c.noMatch(e)
			}
		default:
			rt = Type{K: KFloat, N: t.N, M: 1}
			if t.K != KUint || t.M != 1 {
				c.noMatch(e)
			}
		}
		return mk(rt, args[0].Off, cat(args...))
	case "dFdx", "dFdy", "fwidth":
		need(1)
		t := a0()
		if !isGenF(t) {
			c.noMatch(e)
		}
		if c.version == 100 && !c.extEnabled("GL_OES_standard_derivatives") {
			c.fail(line, name, "extension GL_OES_standard_derivatives is not enabled")
		}
		c.sh.UsesDerivatives = true
		n := t.Size()
		d := c.dst(h, n)
		s := args[0].Off
		kind := name
		o := func(f *Frame) {
			r := f.R
			if f.Quad == nil {
				for i := 0; i < n; i++ {
					r[d+i] = 0
				}
				return
			}
			in := f.scratch[:n]
			for i := 0; i < n; i++ {
				in[i] = fl(r[s+i])
			}
			dx, dy := f.Quad(in)
			for i := 0; i < n; i++ {
				switch kind {
				case "dFdx":
					r[d+i] = bf(dx[i])
				case "dFdy":
					r[d+i] = bf(dy[i])
				default:
					r[d+i] = bf(fmax(dx[i], -dx[i]) + fmax(dy[i], -dy[i]))
				}
			}
		}
		return &V{T: t, Off: d, Ops: append(cat(args...), o)}
	case "packHalf2x16", "packUnorm2x16", "packSnorm2x16":
		need(1)
		if args[0].T != vecT(KFloat, 2) || c.version != 300 {
			c.noMatch(e)
		}
		d, s := c.dst(h, 1), args[0].Off
		o := func(f *Frame) {
			x, y := fl(f.R[s]), fl(f.R[s+1])
			switch name {
			case "packHalf2x16":
				f.R[d] = uint32(Float32ToHalf(x)) | uint32(Float32ToHalf(y))<<16
			case "packUnorm2x16":
				f.R[d] = uint32(math.RoundToEven(float64(clamp01(x)*65535))) | uint32(math.RoundToEven(float64(clamp01(y)*65535)))<<16
			default:
				f.R[d] = uint32(uint16(int16(math.RoundToEven(float64(fmin(fmax(x, -1), 1)*32767))))) | uint32(uint16(int16(math.RoundToEven(float64(fmin(fmax(y, -1), 1)*32767)))))<<16
			}
		}
		return mk(tUint, d, append(cat(args...), o))
	case "unpackHalf2x16", "unpackUnorm2x16", "unpackSnorm2x16":
		need(1)
		if args[0].T != tUint || c.version != 300 {
			c.noMatch(e)
		}
		d, s := c.tmp(2), args[0].Off
		o := func(f *Frame) {
			u := f.R[s]
			lo, hi := uint16(u), uint16(u>>16)
			switch name {
			case "unpackHalf2x16":
				f.R[d], f.R[d+1] = bf(HalfToFloat32(lo)), bf(HalfToFloat32(hi))
			case "unpackUnorm2x16":
				f.R[d], f.R[d+1] = bf(float32(lo)/65535), bf(float32(hi)/65535)
			default:
				f.R[d] = bf(fmax(float32(int16(lo))/32767, -1))
				f.R[d+1] = bf(fmax(float32(int16(hi))/32767, -1))
			}
		}
		return mk(vecT(KFloat, 2), d, append(cat(args...), o))
	case "bitCount":
		need(1)
		t := a0()
		if !isGenI(t) {
			c.noMatch(e)
		}
		n, s, d := t.Size(), args[0].Off, c.dst(h, t.Size())
		o := func(f *Frame) {
			for i := 0; i < n; i++ {
				f.R[d+i] = uint32(bits.OnesCount32(f.R[s+i]))
			}
		}
		return mk(Type{K: KInt, N: t.N, M: 1}, d, append(cat(args...), o))
	}
	c.noMatch(e)
	return nil
}

func clamp01(x float32) float32 { return fmin(fmax(x, 0), 1) }

// gen2 handles gentype f(gentype, gentype) with an optional scalar second operand.
func (c *compiler) gen2(e *Expr, args []*V, h int, fn func(x, y float32) float32, scalar2 bool) *V {
	t, b := args[0].T, args[1].T
	if !isGenF(t) || !isGenF(b) {
		c.noMatch(e)
	}
	if !sameType(t, b) && !(scalar2 && b == tFloat) {
		c.noMatch(e)
	}
	n := t.Size()
	d := c.dst(h, n)
	o := lanewise2(d, args[0].Off, args[1].Off, n, 1, stride(b, n), fn)
	return c.foldIf(&V{T: t, Off: d, Ops: append(cat(args...), o)}, args...)
}

func det(m []float32, n int) float32 {
	switch n {
	case 2:
		return m[0]*m[3] - m[2]*m[1]
	case 3:
		return m[0]*(m[4]*m[8]-m[7]*m[5]) - m[3]*(m[1]*m[8]-m[7]*m[2]) + m[6]*(m[1]*m[5]-m[4]*m[2])
	}
	var s float32
	for c := 0; c < 4; c++ {
		var sub [9]float32
		k := 0
		for cc := 0; cc < 4; cc++ {
			if cc == c {
				continue
			}
			for r := 1; r < 4; r++ {
				sub[k] = m[cc*4+r]
				k++
			}
		}
		v := m[c*4] * det(sub[:], 3)
		if c%2 == 1 {
			v = -v
		}
		s += v
	}
	return s
}

func inverse(m, out []float32, n int) {
	// Gauss-Jordan in float64.
	var a [4][8]float64
	for i := 0; i < n; i++ {
		for j := 0; j < n; j++ {
			a[i][j] = float64(m[j*n+i]) // row i, col j
		}
		a[i][n+i] = 1
	}
	for col := 0; col < n; col++ {
		p := col
		for r := col + 1; r < n; r++ {
			if math.Abs(a[r][col]) > math.Abs(a[p][col]) {
				p = r
			}
		}
		a[col], a[p] = a[p], a[col]
		pv := a[col][col]
		if pv == 0 {
			for i := 0; i < n*n; i++ {
				out[i] = float32(math.NaN())
			}
			return
		}
		for j := 0; j < 2*n; j++ {
			a[col][j] /= pv
		}
		for r := 0; r < n; r++ {
			if r != col {
				f := a[r][col]
				for j := 0; j < 2*n; j++ {
					a[r][j] -= f * a[col][j]
				}
			}
		}
	}
	for i := 0; i < n; i++ {
		for j := 0; j < n; j++ {
			out[j*n+i] = float32(a[i][n+j])
		}
	}
}

// Float32ToHalf converts to IEEE binary16 (round to nearest even).
func Float32ToHalf(f float32) uint16 {
	b := math.Float32bits(f)
	sign := uint16(b>>16) & 0x8000
	exp := int((b >> 23) & 0xFF)
	man := b & 0x7FFFFF
	if exp == 255 {
		if man != 0 {
			return sign | 0x7E00
		}
		return sign | 0x7C00
	}
	e := exp - 127 + 15
	if e >= 31 {
		return sign | 0x7C00
	}
	if e <= 0 {
		if e < -10 {
			return sign
		}
		man |= 0x800000
		shift := uint(14 - e)
		half := uint16(man >> shift)
		rem := man & (1<<shift - 1)
		mid := uint32(1) << (shift - 1)
		if rem > mid || rem == mid && half&1 == 1 {
			half++
		}
		return sign | half
	}
	half := uint16(e<<10) | uint16(man>>13)
	rem := man & 0x1FFF
	if rem > 0x1000 || rem == 0x1000 && half&1 == 1 {
		half++
	}
	return sign | half
}

// HalfToFloat32 converts IEEE binary16 to float32.
func HalfToFloat32(h uint16) float32 {
	sign := uint32(h&0x8000) << 16
	exp := uint32(h>>10) & 0x1F
	man := uint32(h & 0x3FF)
	switch {
	case exp == 0:
		if man == 0 {
			return math.Float32frombits(sign)
		}
		e := uint32(127 - 15 + 1)
		for man&0x400 == 0 {
			man <<= 1
			e--
		}
		man &= 0x3FF
		return math.Float32frombits(sign | e<<23 | man<<13)
	case exp == 31:
		return math.Float32frombits(sign | 0x7F800000 | man<<13)
	}
	return math.Float32frombits(sign | (exp+127-15)<<23 | man<<13)
}
