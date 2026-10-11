package glsl

import (
	"fmt"
	"strings"
)

func (c *compiler) initExpr(e *Expr, t Type) *V { return c.expr(e) }

func (c *compiler) cp(v *V) *V { n := *v; return &n }

func allConst(vs ...*V) bool {
	for _, v := range vs {
		if v.C == nil {
			return false
		}
	}
	return true
}

// foldIf evaluates v's ops at compile time when all operands are constants.
func (c *compiler) foldIf(v *V, operands ...*V) *V {
	if !allConst(operands...) || v.T.K == KVoid {
		return v
	}
	fr := &Frame{R: make([]uint32, c.high+64), budget: 1 << 20}
	copy(fr.R, c.pool)
	for _, o := range v.Ops {
		o(fr)
	}
	n := v.T.Size()
	out := &V{T: v.T, C: append([]uint32(nil), fr.R[v.Off:v.Off+n]...)}
	return out
}

func (c *compiler) addrFn(v *V) func(r []uint32) int {
	base := v.Off
	ds := v.Dyn
	switch len(ds) {
	case 0:
		return func(r []uint32) int { return base }
	case 1:
		d := ds[0]
		return func(r []uint32) int {
			i := int(int32(r[d.idx]))
			if i < 0 {
				i = 0
			} else if i >= d.max {
				i = d.max - 1
			}
			return base + i*d.stride
		}
	}
	return func(r []uint32) int {
		a := base
		for _, d := range ds {
			i := int(int32(r[d.idx]))
			if i < 0 {
				i = 0
			} else if i >= d.max {
				i = d.max - 1
			}
			a += i * d.stride
		}
		return a
	}
}

// mat makes v readable at a static contiguous register offset.
func (c *compiler) mat(v *V) *V {
	if v.C != nil {
		nv := c.cp(v)
		nv.Off = c.poolConst(v.C)
		return nv
	}
	if len(v.Dyn) == 0 && v.Lanes == nil {
		return v
	}
	nv := c.cp(v)
	n := v.T.Size()
	d := c.tmp(n)
	addr := c.addrFn(v)
	lanes := v.Lanes
	var o op
	if lanes != nil {
		o = func(f *Frame) {
			r := f.R
			a := addr(r)
			for i, l := range lanes {
				r[d+i] = r[a+l]
			}
		}
	} else {
		o = func(f *Frame) { a := addr(f.R); copy(f.R[d:d+n], f.R[a:a+n]) }
	}
	nv.Ops = append(append([]op{}, v.Ops...), o)
	nv.Off, nv.Dyn, nv.Lanes, nv.Place = d, nil, nil, false
	return nv
}

func (c *compiler) store(lv, src *V, line int) []op {
	size := lv.T.Size()
	var ops []op
	s := src.Off
	switch {
	case len(lv.Dyn) == 0 && lv.Lanes == nil:
		if src.C != nil {
			s = c.poolConst(src.C)
		}
		if lv.Off != s {
			ops = append(ops, opCopy(lv.Off, s, size))
		}
	case len(lv.Dyn) == 0:
		d, ls := lv.Off, lv.Lanes
		ops = append(ops, func(f *Frame) {
			r := f.R
			var t [4]uint32
			for i := range ls {
				t[i] = r[s+i]
			}
			for i, l := range ls {
				r[d+l] = t[i]
			}
		})
	default:
		addr := c.addrFn(lv)
		ls := lv.Lanes
		if ls != nil {
			ops = append(ops, func(f *Frame) {
				r := f.R
				a := addr(r)
				var t [4]uint32
				for i := range ls {
					t[i] = r[s+i]
				}
				for i, l := range ls {
					r[a+l] = t[i]
				}
			})
		} else {
			ops = append(ops, func(f *Frame) { a := addr(f.R); copy(f.R[a:a+size], f.R[s:s+size]) })
		}
	}
	return ops
}

func (c *compiler) lvCheck(lv *V, line int, opname string) {
	if !lv.Place || lv.RO {
		why := ""
		if lv.Sym != nil {
			switch lv.Sym.storage {
			case "uniform":
				why = fmt.Sprintf(" (can't modify a uniform \"%s\")", lv.Sym.name)
			case "const":
				why = " (can't modify a const)"
			case "attribute", "in", "varying":
				why = " (can't modify an input)"
			}
		}
		c.fail(line, c.opName(opname), "l-value required%s", why)
	}
}

func (c *compiler) expr(e *Expr) *V {
	h := c.hint
	c.hint = -1
	c.line = e.Line
	switch e.K {
	case EInt:
		return cv(tInt, uint32(int32(e.I)))
	case EUint:
		return cv(tUint, uint32(e.I))
	case EFloat:
		return cv(tFloat, bf(float32(e.F)))
	case EBool:
		return cv(tBool, uint32(e.I))
	case EIdent:
		return c.ident(e)
	case EUnary:
		return c.unary(e, h)
	case EPostfix:
		return c.incdec(e, true, h)
	case EBinary:
		return c.binaryExpr(e, h)
	case EAssign:
		return c.assign(e)
	case ETernary:
		return c.ternary(e, h)
	case EComma:
		a := c.expr(e.Args[0])
		b := c.expr(e.Args[1])
		nb := c.cp(b)
		nb.Ops = append(append([]op{}, a.Ops...), b.Ops...)
		return nb
	case EIndex:
		b := c.expr(e.Args[0])
		i := c.expr(e.Args[1])
		return c.index(b, i, e.Line)
	case EField:
		b := c.expr(e.Args[0])
		return c.field(b, e.S, e.Line)
	case ECall:
		return c.call(e, h)
	}
	c.fail(e.Line, "", "unsupported expression")
	return nil
}

func (c *compiler) ident(e *Expr) *V {
	s := c.lookup(e.S)
	if s == nil {
		c.fail(e.Line, e.S, "undeclared identifier")
	}
	switch s.kind {
	case symFunc:
		c.fail(e.Line, e.S, "function name is not a valid identifier here")
	case symType:
		c.fail(e.Line, e.S, "type name used as variable")
	}
	if s.iov != nil {
		s.iov.Used = true
	}
	if s.uni != nil {
		s.uni.Used = true
	}
	if s.blk != nil {
		s.blk.Used = true
	}
	switch e.S {
	case "gl_FragCoord":
		c.sh.UsesFragCoord = true
	case "gl_FragDepth", "gl_FragDepthEXT":
		c.sh.WritesFragDepth = true
	case "gl_FragColor":
		c.sh.WritesFragColor = true
	case "gl_FragData":
		c.sh.WritesFragData = true
	case "gl_PointSize":
		c.sh.WritesPointSize = true
	}
	if s.c != nil {
		return &V{T: s.t, C: s.c, Sym: s}
	}
	return &V{T: s.t, Off: s.off, Place: true, RO: s.ro, Sym: s, prec: s.prec}
}

func (c *compiler) index(b, i *V, line int) *V {
	i = c.mat(i)
	if !(i.T == tInt || i.T == tUint) {
		c.fail(line, "[]", "array index must be scalar integer")
	}
	bt := b.T
	var elemT Type
	var stride, count int
	switch {
	case bt.Arr > 0:
		elemT = bt.Elem()
		stride, count = elemT.Size(), bt.Arr
	case bt.IsMat():
		elemT = vecT(KFloat, bt.N)
		stride, count = bt.N, bt.M
	case bt.IsVec():
		elemT = bt.ScalarOf()
		stride, count = 1, bt.N
	default:
		c.fail(line, "[", "left of '[' is not of type array, matrix, or vector")
	}
	if i.C != nil {
		idx := int(int32(i.C[0]))
		if idx < 0 || idx >= count {
			c.fail(line, "[", "array index out of range '%d'", idx)
		}
		if b.C != nil {
			return &V{T: elemT, C: append([]uint32(nil), b.C[idx*stride:(idx+1)*stride]...)}
		}
		nb := c.cp(b)
		nb.T = elemT
		if b.Lanes != nil {
			nb.Off += b.Lanes[idx]
			nb.Lanes = nil
		} else {
			nb.Off += idx * stride
		}
		nb.Ops = append(append([]op{}, b.Ops...), i.Ops...)
		return nb
	}
	nb := c.cp(b)
	if b.Lanes != nil {
		nb = c.cp(c.mat(b))
	}
	if nb.C != nil {
		nb.Off = c.poolConst(nb.C)
		nb.C = nil
		nb.Place, nb.RO = true, true
	}
	nb.T = elemT
	nb.Dyn = append(append([]dyn{}, nb.Dyn...), dyn{i.Off, stride, count})
	nb.Ops = append(append([]op{}, nb.Ops...), i.Ops...)
	return nb
}

func swizzleIndex(ch byte) (set, idx int) {
	for si, s := range []string{"xyzw", "rgba", "stpq"} {
		if k := strings.IndexByte(s, ch); k >= 0 {
			return si, k
		}
	}
	return -1, -1
}

func (c *compiler) field(b *V, name string, line int) *V {
	if b.T.Arr > 0 {
		if name == "length" {
			c.fail(line, name, "length() requires call syntax")
		}
		c.fail(line, name, "invalid field selection")
	}
	if b.T.K == KStruct {
		for _, f := range b.T.S.Fields {
			if f.Name == name {
				if b.C != nil {
					return &V{T: f.T, C: append([]uint32(nil), b.C[f.Off:f.Off+f.T.Size()]...)}
				}
				nb := c.cp(b)
				nb.T = f.T
				if b.Sym != nil && b.Sym.blk != nil {
					nb.Off = f.Off
				} else {
					nb.Off += f.Off
				}
				return nb
			}
		}
		c.fail(line, name, "invalid field selection")
	}
	if !b.T.IsVec() && !b.T.IsScalar() {
		c.fail(line, name, "field selection requires structure, vector, or interface block on left hand side")
	}
	n := b.T.N
	if len(name) > 4 || len(name) == 0 {
		c.fail(line, name, "illegal vector field selection")
	}
	pos := make([]int, len(name))
	set := -1
	for i := 0; i < len(name); i++ {
		s, k := swizzleIndex(name[i])
		if s < 0 || set >= 0 && s != set {
			c.fail(line, name, "illegal vector field selection")
		}
		set = s
		if k >= n {
			c.fail(line, name, "vector field selection out of range")
		}
		pos[i] = k
	}
	rt := b.T.ScalarOf()
	if len(pos) > 1 {
		rt = vecT(b.T.K, len(pos))
	}
	if b.C != nil {
		out := make([]uint32, len(pos))
		for i, p := range pos {
			out[i] = b.C[p]
		}
		return &V{T: rt, C: out}
	}
	nb := c.cp(b)
	nb.T = rt
	contig := true
	for i := 1; i < len(pos); i++ {
		if pos[i] != pos[0]+i {
			contig = false
		}
	}
	if b.Lanes == nil && contig {
		nb.Off += pos[0]
		return nb
	}
	nl := make([]int, len(pos))
	seen := map[int]bool{}
	for i, p := range pos {
		if b.Lanes != nil {
			nl[i] = b.Lanes[p]
		} else {
			nl[i] = p
		}
		if seen[p] {
			nb.RO = true // duplicate component: not assignable
			nb.roWhy = "dup"
		}
		seen[p] = true
	}
	nb.Lanes = nl
	return nb
}

// ---- operators ---------------------------------------------------------------

func (c *compiler) unary(e *Expr, h int) *V {
	oper := e.S
	if oper == "++" || oper == "--" {
		return c.incdec(e, false, h)
	}
	a := c.mat(c.expr(e.Args[0]))
	t := a.T
	if t.Arr > 0 || t.K == KStruct || t.K == KSampler || t.K == KVoid {
		c.fail(e.Line, oper, "wrong operand type - no operation '%s' exists that takes an operand of type %s", oper, t)
	}
	n := t.Size()
	switch oper {
	case "+":
		if t.K == KBool {
			c.fail(e.Line, oper, "wrong operand type - no operation '+' exists that takes an operand of type %s", t)
		}
		return a
	case "-":
		if t.K == KBool {
			c.fail(e.Line, oper, "wrong operand type - no operation '-' exists that takes an operand of type %s", t)
		}
		d := c.dst(h, n)
		s := a.Off
		var o op
		if t.K == KFloat {
			o = func(f *Frame) {
				r := f.R
				for i := 0; i < n; i++ {
					r[d+i] = r[s+i] ^ 0x80000000
				}
			}
		} else {
			o = func(f *Frame) {
				r := f.R
				for i := 0; i < n; i++ {
					r[d+i] = -r[s+i]
				}
			}
		}
		return c.foldIf(&V{T: t, Off: d, Ops: append(append([]op{}, a.Ops...), o)}, a)
	case "!":
		if t != tBool {
			c.fail(e.Line, oper, "wrong operand type - no operation '!' exists that takes an operand of type %s", t)
		}
		d := c.dst(h, 1)
		s := a.Off
		o := func(f *Frame) { f.R[d] = f.R[s] ^ 1 }
		return c.foldIf(&V{T: t, Off: d, Ops: append(append([]op{}, a.Ops...), o)}, a)
	case "~":
		if !(t.K == KInt || t.K == KUint) || c.version != 300 {
			c.fail(e.Line, oper, "wrong operand type - no operation '~' exists that takes an operand of type %s", t)
		}
		d := c.dst(h, n)
		s := a.Off
		o := func(f *Frame) {
			for i := 0; i < n; i++ {
				f.R[d+i] = ^f.R[s+i]
			}
		}
		return c.foldIf(&V{T: t, Off: d, Ops: append(append([]op{}, a.Ops...), o)}, a)
	}
	c.fail(e.Line, oper, "unsupported unary operator")
	return nil
}

func addOne(k Kind, dec bool) uint32 {
	if k == KFloat {
		if dec {
			return bf(-1)
		}
		return bf(1)
	}
	if dec {
		return 0xFFFFFFFF
	}
	return 1
}

func (c *compiler) incdec(e *Expr, post bool, h int) *V {
	lv := c.expr(e.Args[0])
	c.lvCheck(lv, e.Line, e.S)
	t := lv.T
	if !t.IsNumeric() && !(t.K == KFloat) || t.Arr > 0 || t.K == KStruct {
		c.fail(e.Line, e.S, "wrong operand type - no operation '%s' exists that takes an operand of type %s", e.S, t)
	}
	n := t.Size()
	one := addOne(t.K, e.S == "--")
	var ops []op
	ops = append(ops, lv.Ops...)
	if len(lv.Dyn) == 0 && lv.Lanes == nil {
		off := lv.Off
		res := &V{T: t, Off: off}
		if post {
			d := c.tmp(n)
			ops = append(ops, opCopy(d, off, n))
			res.Off = d
		}
		if t.K == KFloat {
			if n == 1 {
				if e.S == "++" {
					ops = append(ops, func(f *Frame) { f.R[off] = bf(fl(f.R[off]) + 1) })
				} else {
					ops = append(ops, func(f *Frame) { f.R[off] = bf(fl(f.R[off]) - 1) })
				}
			} else {
				ops = append(ops, func(f *Frame) {
					for i := 0; i < n; i++ {
						f.R[off+i] = bf(fl(f.R[off+i]) + fl(one))
					}
				})
			}
		} else if n == 1 {
			ops = append(ops, func(f *Frame) { f.R[off] += one })
		} else {
			ops = append(ops, func(f *Frame) {
				for i := 0; i < n; i++ {
					f.R[off+i] += one
				}
			})
		}
		res.Ops = ops
		return res
	}
	rd := c.mat(&V{T: t, Off: lv.Off, Dyn: lv.Dyn, Lanes: lv.Lanes, Place: true})
	ops = append(ops, rd.Ops...)
	nv := c.tmp(n)
	old := rd.Off
	if post {
		old = c.tmp(n)
		ops = append(ops, opCopy(old, rd.Off, n))
	}
	src := rd.Off
	k := t.K
	ops = append(ops, func(f *Frame) {
		for i := 0; i < n; i++ {
			if k == KFloat {
				f.R[nv+i] = bf(fl(f.R[src+i]) + fl(one))
			} else {
				f.R[nv+i] = f.R[src+i] + one
			}
		}
	})
	ops = append(ops, c.store(lv, &V{T: t, Off: nv}, e.Line)...)
	res := &V{T: t, Off: nv, Ops: ops}
	if post {
		res.Off = old
	}
	return res
}

func (c *compiler) binaryExpr(e *Expr, h int) *V {
	oper := e.S
	if oper == "&&" || oper == "||" {
		a := c.mat(c.expr(e.Args[0]))
		b := c.mat(c.expr(e.Args[1]))
		if a.T != tBool || b.T != tBool {
			c.fail(e.Line, oper, "wrong operand types - no operation '%s' exists that takes a left-hand operand of type '%s' and a right operand of type '%s' (and there is no acceptable conversion)", oper, a.T, b.T)
		}
		d := c.dst(h, 1)
		ao, bo := a.Off, b.Off
		bRun := runOps(b.Ops)
		isAnd := oper == "&&"
		o := func(f *Frame) {
			v := f.R[ao]
			if (v != 0) == isAnd {
				bRun(f)
				v = f.R[bo]
			}
			f.R[d] = v
		}
		r := &V{T: tBool, Off: d, Ops: append(append([]op{}, a.Ops...), o)}
		return c.foldIf2(r, a, b)
	}
	a := c.mat(c.expr(e.Args[0]))
	b := c.mat(c.expr(e.Args[1]))
	return c.binop(oper, a, b, e.Line, h)
}

// foldIf2 folds short-circuit expressions whose operands are constant.
func (c *compiler) foldIf2(v *V, a, b *V) *V {
	if a.C != nil && b.C != nil {
		return c.foldIf(v, a, b)
	}
	return v
}

func (c *compiler) binErr(oper string, a, b *V, line int) {
	c.fail(line, oper, "wrong operand types - no operation '%s' exists that takes a left-hand operand of type '%s' and a right operand of type '%s' (and there is no acceptable conversion)", oper, a.T, b.T)
}

func (c *compiler) binop(oper string, a, b *V, line int, h int) *V {
	ta, tb := a.T, b.T
	if ta.Arr > 0 || tb.Arr > 0 || ta.K == KSampler || tb.K == KSampler || ta.K == KVoid || tb.K == KVoid {
		if (oper == "==" || oper == "!=") && sameType(ta, tb) && ta.K != KSampler && ta.K != KVoid {
			return c.compare(oper, a, b, h)
		}
		c.binErr(oper, a, b, line)
	}
	if ta.K == KStruct || tb.K == KStruct {
		if (oper == "==" || oper == "!=") && sameType(ta, tb) {
			return c.compare(oper, a, b, h)
		}
		c.binErr(oper, a, b, line)
	}
	ops := append(append([]op{}, a.Ops...), b.Ops...)
	switch oper {
	case "==", "!=":
		if !sameType(ta, tb) {
			c.binErr(oper, a, b, line)
		}
		return c.compare(oper, a, b, h)
	case "<", "<=", ">", ">=":
		if !sameType(ta, tb) || !ta.IsScalar() || ta.K == KBool {
			c.binErr(oper, a, b, line)
		}
		d := c.dst(h, 1)
		r := &V{T: tBool, Off: d, Ops: append(ops, cmpScalar(oper, ta.K, d, a.Off, b.Off))}
		return c.foldIf(r, a, b)
	case "^^":
		if ta != tBool || tb != tBool {
			c.binErr(oper, a, b, line)
		}
		d := c.dst(h, 1)
		ao, bo := a.Off, b.Off
		r := &V{T: tBool, Off: d, Ops: append(ops, func(f *Frame) { f.R[d] = f.R[ao] ^ f.R[bo] })}
		return c.foldIf(r, a, b)
	case "%", "&", "|", "^", "<<", ">>":
		if c.version != 300 {
			c.fail(line, oper, "operator not supported in GLSL ES 1.00")
		}
		if !(ta.K == KInt || ta.K == KUint) || !(tb.K == KInt || tb.K == KUint) || ta.M > 1 || tb.M > 1 {
			c.binErr(oper, a, b, line)
		}
		if oper != "<<" && oper != ">>" && ta.K != tb.K {
			c.binErr(oper, a, b, line)
		}
	case "+", "-", "*", "/":
		if ta.K == KBool || tb.K == KBool || ta.K != tb.K {
			c.binErr(oper, a, b, line)
		}
	default:
		c.binErr(oper, a, b, line)
	}
	// numeric arithmetic
	var rt Type
	var o op
	isF := ta.K == KFloat
	// matrix cases
	if ta.M > 1 || tb.M > 1 {
		if !isF {
			c.binErr(oper, a, b, line)
		}
		switch {
		case oper == "*" && ta.M > 1 && tb.M > 1:
			if ta.M != tb.N {
				c.binErr(oper, a, b, line)
			}
			rt = matT(tb.M, ta.N)
			d := c.dst(h, rt.Size())
			if h >= 0 && (h == a.Off || h == b.Off) {
				d = c.tmp(rt.Size())
			}
			o = opMatMul(d, a.Off, b.Off, ta.M, ta.N, tb.M)
			return c.foldIf(&V{T: rt, Off: d, Ops: append(ops, o)}, a, b)
		case oper == "*" && ta.M > 1 && tb.IsVec():
			if ta.M != tb.N {
				c.binErr(oper, a, b, line)
			}
			rt = vecT(KFloat, ta.N)
			d := c.dst(h, rt.Size())
			if h >= 0 && h == b.Off {
				d = c.tmp(rt.Size())
			}
			return c.foldIf(&V{T: rt, Off: d, Ops: append(ops, opMatVec(d, a.Off, b.Off, ta.M, ta.N))}, a, b)
		case oper == "*" && ta.IsVec() && tb.M > 1:
			if ta.N != tb.N {
				c.binErr(oper, a, b, line)
			}
			rt = vecT(KFloat, tb.M)
			d := c.dst(h, rt.Size())
			if h >= 0 && h == a.Off {
				d = c.tmp(rt.Size())
			}
			return c.foldIf(&V{T: rt, Off: d, Ops: append(ops, opVecMat(d, a.Off, b.Off, tb.M, tb.N))}, a, b)
		case ta.M > 1 && tb.M > 1:
			if ta.M != tb.M || ta.N != tb.N {
				c.binErr(oper, a, b, line)
			}
			rt = ta
		case ta.M > 1 && tb.IsScalar():
			rt = ta
		case tb.M > 1 && ta.IsScalar():
			rt = tb
		default:
			c.binErr(oper, a, b, line)
		}
	} else {
		switch {
		case ta.N == tb.N:
			rt = ta
		case ta.IsScalar() && tb.IsVec():
			rt = tb
		case ta.IsVec() && tb.IsScalar():
			rt = ta
		default:
			c.binErr(oper, a, b, line)
		}
		if (oper == "<<" || oper == ">>") && ta.K != rt.K {
			rt.K = ta.K
		}
	}
	n := rt.Size()
	sa, sb := 1, 1
	if a.T.Size() == 1 && n > 1 {
		sa = 0
	}
	if b.T.Size() == 1 && n > 1 {
		sb = 0
	}
	d := c.dst(h, n)
	if isF {
		o = fbin(oper[0], d, a.Off, b.Off, n, sa, sb)
	} else {
		o = ibin(oper, ta.K == KUint, d, a.Off, b.Off, n, sa, sb)
	}
	return c.foldIf(&V{T: rt, Off: d, Ops: append(ops, o)}, a, b)
}

func (c *compiler) compare(oper string, a, b *V, h int) *V {
	d := c.dst(h, 1)
	k := a.T.K
	n := a.T.Size()
	o := opEq(k, d, a.Off, b.Off, n, oper == "!=")
	ops := append(append([]op{}, a.Ops...), b.Ops...)
	return c.foldIf(&V{T: tBool, Off: d, Ops: append(ops, o)}, a, b)
}

func (c *compiler) assign(e *Expr) *V {
	lv := c.expr(e.Args[0])
	c.lvCheck(lv, e.Line, e.S)
	if lv.roWhy == "dup" {
		c.fail(e.Line, e.S, "l-value required")
	}
	if lv.T.K == KSampler {
		c.fail(e.Line, e.S, "l-value required")
	}
	static := len(lv.Dyn) == 0 && lv.Lanes == nil
	if e.S == "=" {
		if static {
			c.hint = lv.Off
		}
		rhs := c.mat(c.expr(e.Args[1]))
		c.hint = -1
		if !sameType(lv.T, rhs.T) {
			c.assignMismatch(e.Line, lv, rhs)
		}
		ops := append(append([]op{}, lv.Ops...), rhs.Ops...)
		ops = append(ops, c.store(lv, rhs, e.Line)...)
		return &V{T: lv.T, Off: rhs.Off, Ops: ops}
	}
	bop := e.S[:len(e.S)-1]
	rd := c.mat(&V{T: lv.T, Off: lv.Off, Dyn: lv.Dyn, Lanes: lv.Lanes, Place: true})
	h := -1
	if static {
		h = lv.Off
	}
	rhs := c.mat(c.expr(e.Args[1]))
	res := c.binop(bop, rd, rhs, e.Line, h)
	if !sameType(res.T, lv.T) {
		c.fail(e.Line, e.S, "cannot convert from '%s' to '%s'", res.T, lv.T)
	}
	res = c.mat(res)
	ops := append(append([]op{}, lv.Ops...), res.Ops...)
	ops = append(ops, c.store(lv, res, e.Line)...)
	return &V{T: lv.T, Off: res.Off, Ops: ops}
}

func (c *compiler) ternary(e *Expr, h int) *V {
	cond := c.condition(e.Args[0])
	a := c.mat(c.expr(e.Args[1]))
	b := c.mat(c.expr(e.Args[2]))
	if !sameType(a.T, b.T) {
		c.fail(e.Line, "?:", "ternary operator types must match")
	}
	if a.T.K == KSampler || a.T.K == KVoid {
		c.fail(e.Line, "?:", "ternary operator is not allowed for this type")
	}
	if cond.C != nil {
		if cond.C[0] != 0 {
			return a
		}
		return b
	}
	n := a.T.Size()
	d := c.dst(h, n)
	co, ao, bo := cond.Off, a.Off, b.Off
	aRun, bRun := runOps(a.Ops), runOps(b.Ops)
	o := func(f *Frame) {
		if f.R[co] != 0 {
			aRun(f)
			copy(f.R[d:d+n], f.R[ao:ao+n])
		} else {
			bRun(f)
			copy(f.R[d:d+n], f.R[bo:bo+n])
		}
	}
	if n == 1 {
		o = func(f *Frame) {
			if f.R[co] != 0 {
				aRun(f)
				f.R[d] = f.R[ao]
			} else {
				bRun(f)
				f.R[d] = f.R[bo]
			}
		}
	}
	return &V{T: a.T, Off: d, Ops: append(append([]op{}, cond.Ops...), o)}
}

func vtn(v *V) string {
	if v.C != nil {
		return "const " + v.T.String()
	}
	return v.T.String()
}

func (c *compiler) opName(op string) string {
	if op == "=" {
		return "assign"
	}
	return op
}

func baseName(k Kind) string {
	return map[Kind]string{KBool: "bool", KInt: "int", KUint: "uint", KFloat: "float"}[k]
}

func typeDesc(t Type) string {
	var s string
	switch {
	case t.Arr > 0:
		e := t.Elem()
		return fmt.Sprintf("%d-element array of %s", t.Arr, typeDesc(e))
	case t.K == KStruct:
		return "structure"
	case t.K == KSampler:
		return t.String()
	case t.M > 1:
		s = fmt.Sprintf("%dX%d matrix of float", t.M, t.N)
	case t.N > 1:
		s = fmt.Sprintf("%d-component vector of %s", t.N, baseName(t.K))
	default:
		s = baseName(t.K)
	}
	return s
}

// precOf resolves the precision of a declared variable.
func (c *compiler) precOf(ts *TypeSpec, t Type) string {
	if ts != nil && ts.Prec != "" {
		return ts.Prec
	}
	switch t.K {
	case KFloat:
		return c.precs["float"]
	case KInt, KUint:
		return c.precs["int"]
	case KSampler:
		if c.stage == Fragment {
			return "lowp"
		}
		return c.precs["sampler2D"]
	}
	return ""
}

func (c *compiler) dlong(ts *TypeSpec, t Type) string {
	return strings.TrimSpace(c.precOf(ts, t) + " " + typeDesc(t))
}

// tlong describes an expression's type the way ANGLE prints it.
func (c *compiler) tlong(v *V) string {
	var parts []string
	if v.C != nil {
		parts = append(parts, "const")
	} else if v.Sym != nil {
		switch v.Sym.name {
		case "gl_Position":
			parts = append(parts, "Position")
		case "gl_FragColor":
			parts = append(parts, "FragColor")
		case "gl_FragData":
			parts = append(parts, "FragData")
		case "gl_PointSize":
			parts = append(parts, "PointSize")
		default:
			switch v.Sym.storage {
			case "uniform", "attribute", "varying":
				parts = append(parts, v.Sym.storage)
			case "in":
				parts = append(parts, "in")
			case "out":
				parts = append(parts, "out")
			}
		}
	}
	if v.C == nil {
		p := v.prec
		if p == "" && v.T.K != KBool && v.T.K != KSampler {
			// results of expressions take the precision of their operands; assume the default
			if v.T.K == KFloat {
				p = c.precs["float"]
			} else {
				p = c.precs["int"]
			}
		}
		if p != "" {
			parts = append(parts, p)
		}
	}
	parts = append(parts, typeDesc(v.T))
	return strings.Join(parts, " ")
}

func (c *compiler) assignMismatch(line int, lv, rhs *V) {
	if lv.T.K == rhs.T.K && lv.T.Arr == 0 && rhs.T.Arr == 0 && lv.T.K != KStruct && lv.T.K != KSampler && (lv.T.N != rhs.T.N || lv.T.M != rhs.T.M) {
		c.fail(line, "=", "dimension mismatch\nERROR: 0:%d: 'assign' : cannot convert from '%s' to '%s'", line, c.tlong(rhs), c.tlong(lv))
	}
	c.fail(line, "=", "cannot convert from '%s' to '%s'", c.tlong(rhs), c.tlong(lv))
}
