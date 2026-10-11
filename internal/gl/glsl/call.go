package glsl

func (c *compiler) call(e *Expr, h int) *V {
	if e.S == "__length" {
		b := c.expr(e.Args[0])
		switch {
		case b.T.Arr > 0:
			return cv(tInt, uint32(b.T.Arr))
		case b.T.IsVec():
			return cv(tInt, uint32(b.T.N))
		case b.T.IsMat():
			return cv(tInt, uint32(b.T.M))
		}
		c.fail(e.Line, "length", "length can only be called on arrays, vectors and matrices")
	}
	if e.Type != nil {
		return c.construct(e, h)
	}
	s := c.lookup(e.S)
	if s != nil && s.kind == symType {
		return c.construct(e, h)
	}
	if s != nil && s.kind == symFunc {
		return c.userCall(e, s, h)
	}
	if bt, ok := basicType(e.S); ok && bt.K != KSampler {
		return c.construct(e, h)
	}
	return c.builtin(e, h)
}

func (c *compiler) userCall(e *Expr, s *sym, h int) *V {
	args := make([]*V, len(e.Args))
	for i, a := range e.Args {
		args[i] = c.expr(a)
	}
	var f *fn
	for _, cand := range s.fns {
		if len(cand.params) != len(args) {
			continue
		}
		ok := true
		for i, p := range cand.params {
			if !sameType(p.t, args[i].T) {
				ok = false
				break
			}
		}
		if ok {
			f = cand
			break
		}
	}
	if f == nil {
		c.fail(e.Line, e.S, "no matching overloaded function found")
	}
	if f == c.curFn {
		c.fail(e.Line, e.S, "Recursive function call")
	}
	var ops, post []op
	for i, p := range f.params {
		a := args[i]
		switch p.dir {
		case "in":
			ra := c.mat(a)
			ops = append(ops, ra.Ops...)
			ops = append(ops, opCopy(p.off, ra.Off, p.t.Size()))
		case "out", "inout":
			if !a.Place || a.RO || a.roWhy != "" {
				c.fail(e.Line, e.S, "l-value required for out/inout parameter")
			}
			ops = append(ops, a.Ops...)
			if p.dir == "inout" {
				ra := c.mat(&V{T: a.T, Off: a.Off, Dyn: a.Dyn, Lanes: a.Lanes, Place: true})
				ops = append(ops, ra.Ops...)
				ops = append(ops, opCopy(p.off, ra.Off, p.t.Size()))
			}
			post = append(post, c.store(a, &V{T: p.t, Off: p.off}, e.Line)...)
		}
	}
	ff := f
	ops = append(ops, func(fr *Frame) {
		ff.body(fr)
	})
	ops = append(ops, post...)
	if f.ret.K == KVoid {
		return &V{T: tVoid, Ops: ops}
	}
	n := f.ret.Size()
	d := c.dst(h, n)
	ops = append(ops, opCopy(d, f.retOff, n))
	return &V{T: f.ret, Off: d, Ops: ops}
}

func (c *compiler) construct(e *Expr, h int) *V {
	var t Type
	name := e.S
	if bt, ok := basicType(name); ok {
		t = bt
	} else if s := c.lookup(name); s != nil && s.kind == symType {
		t = s.t
	} else {
		c.fail(e.Line, name, "no matching overloaded function found")
	}
	args := make([]*V, len(e.Args))
	usable := true
	for i, a := range e.Args {
		args[i] = c.mat(c.expr(a))
		if args[i].Place {
			usable = false
		}
	}
	isArr := e.Type != nil && e.Type.Arr
	if isArr {
		n := len(args)
		if e.Type.ArrLen != nil {
			if want := c.arraySize(e.Type.ArrLen); want != n {
				c.fail(e.Line, name, "array constructor needs %d arguments", want)
			}
		}
		t.Arr = n
	}
	if t.K == KSampler || t.K == KVoid {
		c.fail(e.Line, name, "cannot construct this type")
	}
	if len(args) == 0 {
		c.fail(e.Line, name, "constructor needs arguments")
	}
	var ops []op
	for _, a := range args {
		ops = append(ops, a.Ops...)
	}
	n := t.Size()
	d := -1
	if h >= 0 && usable {
		d = h
	} else {
		d = c.tmp(n)
	}
	switch {
	case t.Arr > 0:
		et := t.Elem()
		pos := 0
		for _, a := range args {
			if !sameType(a.T, et) {
				c.fail(e.Line, name, "array constructor argument has wrong type")
			}
			ops = append(ops, opCopy(d+pos, a.Off, et.Size()))
			pos += et.Size()
		}
	case t.K == KStruct:
		if len(args) != len(t.S.Fields) {
			c.fail(e.Line, name, "Number of constructor parameters does not match the number of structure fields")
		}
		for i, f := range t.S.Fields {
			if !sameType(args[i].T, f.T) {
				c.fail(e.Line, name, "Structure constructor arguments do not match structure fields")
			}
			ops = append(ops, opCopy(d+f.Off, args[i].Off, f.T.Size()))
		}
	default:
		for _, a := range args {
			if a.T.Arr > 0 || a.T.K == KStruct || a.T.K == KSampler || a.T.K == KVoid {
				c.fail(e.Line, name, "cannot convert argument to constructor")
			}
		}
		tk := t.K
		switch {
		case t.IsMat():
			if len(args) == 1 && args[0].T.IsScalar() {
				a := args[0]
				// diagonal
				tmp := c.tmp(1)
				ops = append(ops, opConv(tmp, a.Off, 1, a.T.K, KFloat))
				ops = append(ops, func(f *Frame) {
					v := f.R[tmp]
					for i := 0; i < t.M; i++ {
						for j := 0; j < t.N; j++ {
							if i == j {
								f.R[d+i*t.N+j] = v
							} else {
								f.R[d+i*t.N+j] = 0
							}
						}
					}
				})
			} else if len(args) == 1 && args[0].T.IsMat() {
				a := args[0]
				sc, sr, so := a.T.M, a.T.N, a.Off
				tmp := c.tmp(16)
				ops = append(ops, func(f *Frame) {
					for i := 0; i < t.M; i++ {
						for j := 0; j < t.N; j++ {
							v := uint32(0)
							if i < sc && j < sr {
								v = f.R[so+i*sr+j]
							} else if i == j {
								v = bf(1)
							}
							f.R[tmp+i*t.N+j] = v
						}
					}
					copy(f.R[d:d+n], f.R[tmp:tmp+n])
				})
			} else {
				total := 0
				for _, a := range args {
					total += a.T.Size()
				}
				if total < n {
					c.fail(e.Line, name, "not enough data provided for construction")
				}
				pos := 0
				for i, a := range args {
					take := min(a.T.Size(), n-pos)
					if take <= 0 {
						c.fail(e.Line, name, "too many arguments")
					}
					_ = i
					ops = append(ops, opConv(d+pos, a.Off, take, a.T.K, KFloat))
					pos += take
				}
			}
		case t.IsScalar():
			if len(args) != 1 {
				c.fail(e.Line, name, "too many arguments")
			}
			ops = append(ops, opConv(d, args[0].Off, 1, args[0].T.K, tk))
		default: // vector
			if len(args) == 1 && args[0].T.IsScalar() {
				ops = append(ops, opConv(d, args[0].Off, 1, args[0].T.K, tk))
				if t.N > 1 {
					ops = append(ops, opFill(d, d, t.N))
				}
			} else {
				total := 0
				for _, a := range args {
					total += a.T.Size()
				}
				if total < n {
					c.fail(e.Line, name, "not enough data provided for construction")
				}
				pos := 0
				for _, a := range args {
					take := min(a.T.Size(), n-pos)
					if take <= 0 {
						c.fail(e.Line, name, "too many arguments")
					}
					ops = append(ops, opConv(d+pos, a.Off, take, a.T.K, tk))
					pos += take
				}
			}
		}
	}
	return c.foldIf(&V{T: t, Off: d, Ops: ops}, args...)
}
