package glsl

import "fmt"

func (c *compiler) safe(f func()) {
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(cerr); ok {
				c.report(e)
				c.next = c.mark
				return
			}
			panic(r)
		}
	}()
	f()
}

func (c *compiler) top(t *Top) {
	c.safe(func() {
		switch t.K {
		case TopPrecision:
			if len(t.Decls) == 1 {
				c.precs[t.Decls[0].T.Name] = t.Decls[0].Name
			}
		case TopStruct:
			c.defineStruct(t.Struct)
		case TopDecl:
			for _, d := range t.Decls {
				c.safe(func() { c.globalDecl(d) })
			}
		case TopFunc:
			c.funcDef(t.Func)
		case TopBlock:
			c.blockDecl(t.Block)
		}
	})
}

func (c *compiler) declType(d *Decl, ts *TypeSpec) Type {
	t := c.resolveType(ts, d.Line)
	if d.HasArr {
		if t.Arr > 0 {
			c.fail(d.Line, d.Name, "arrays of arrays are not supported")
		}
		if d.ArrLen != nil {
			t.Arr = c.arraySize(d.ArrLen)
		} else {
			t.Arr = -1
		}
	} else if ts.Arr && ts.ArrLen == nil {
		t.Arr = -1
	}
	return t
}

func (c *compiler) globalDecl(d *Decl) {
	t := c.declType(d, d.T)
	if t.K == KVoid {
		c.fail(d.Line, d.Name, "illegal use of type 'void'")
	}
	if len(d.Name) >= 3 && d.Name[:3] == "gl_" {
		c.fail(d.Line, d.Name, "identifiers starting with \"gl_\" are reserved")
	}
	var init *V
	if d.Init != nil {
		if t.Arr == -1 {
			init = c.initExpr(d.Init, t)
			t.Arr = init.T.Arr
		} else {
			init = c.initExpr(d.Init, t)
		}
	}
	if t.Arr == -1 {
		c.fail(d.Line, d.Name, "implicitly sized array requires an initializer")
	}
	q := d.Q
	switch q.Storage {
	case "const":
		if init == nil {
			c.fail(d.Line, d.Name, "variables with qualifier 'const' must be initialized")
		}
		if init.C == nil {
			c.fail(d.Line, d.Name, "global variable initializers must be constant expressions")
		}
		if !sameType(init.T, t) {
			c.fail(d.Line, "=", "cannot convert from '%s' to '%s'", init.T, t)
		}
		c.declare(d.Line, &sym{name: d.Name, kind: symVar, t: t, c: init.C, ro: true, storage: "const"})
	case "uniform":
		if init != nil {
			c.fail(d.Line, d.Name, "cannot initialize this type of qualifier")
		}
		off := c.alloc(t.Size())
		u := &Uniform{Name: d.Name, T: t, Off: off, Block: -1, Prec: c.precOf(d.T, t)}
		c.unis = append(c.unis, u)
		c.declare(d.Line, &sym{name: d.Name, kind: symVar, t: t, off: off, ro: true, storage: "uniform", uni: u, prec: c.precOf(d.T, t)})
	case "attribute", "in", "varying", "out":
		storage := q.Storage
		isInput := storage == "attribute" || (storage == "in") || (storage == "varying" && c.stage == Fragment)
		if c.version == 100 {
			if storage == "attribute" && c.stage != Vertex {
				c.fail(d.Line, d.Name, "'attribute' : supported in vertex shaders only")
			}
			if storage == "in" || storage == "out" {
				c.fail(d.Line, storage, "syntax error")
			}
		} else if storage == "attribute" || storage == "varying" {
			c.fail(d.Line, storage, "syntax error")
		}
		if init != nil {
			c.fail(d.Line, d.Name, "cannot initialize this type of qualifier")
		}
		if t.K == KBool || t.K == KSampler || t.K == KStruct && c.stage == Vertex && isInput {
			c.fail(d.Line, d.Name, "invalid type for %s", storage)
		}
		off := c.alloc(t.Size())
		iov := &IOVar{Name: d.Name, T: t, Off: off, Loc: -1, Invariant: q.Invariant}
		if l, ok := q.Layout["location"]; ok {
			iov.Loc = l
		}
		if q.Interp == "flat" || t.K == KInt || t.K == KUint {
			iov.Flat = true
		}
		if isInput {
			c.inputs = append(c.inputs, iov)
		} else {
			if c.stage == Fragment && c.version == 300 && iov.Loc < 0 {
				iov.Loc = c.fragOutLoc
				c.fragOutLoc += max(1, t.Arr) * max(1, t.M)
			}
			c.outputs = append(c.outputs, iov)
		}
		c.declare(d.Line, &sym{name: d.Name, kind: symVar, t: t, off: off, ro: isInput, storage: storage, iov: iov, prec: c.precOf(d.T, t)})
	case "":
		off := c.alloc(t.Size())
		s := &sym{name: d.Name, kind: symVar, t: t, off: off, storage: "global", prec: c.precOf(d.T, t)}
		c.declare(d.Line, s)
		if init != nil {
			if !sameType(init.T, t) {
				c.fail(d.Line, "=", "cannot convert from '%s' to '%s'", c.tlong(init), c.dlong(d.T, t))
			}
			if init.C == nil {
				c.fail(d.Line, d.Name, "global variable initializers must be constant expressions")
			}
			c.globInit = append(c.globInit, append(append([]op{}, init.Ops...), c.storeConst(off, init))...)
		}
	default:
		c.fail(d.Line, q.Storage, "invalid qualifier")
	}
	c.mark = c.next
}

func (c *compiler) storeConst(off int, v *V) op {
	src := c.poolConst(v.C)
	return opCopy(off, src, v.T.Size())
}

// std140 layout helpers.
func roundUp(x, a int) int { return (x + a - 1) / a * a }

func std140Align(t Type) (align, size int) {
	if t.Arr > 0 {
		e := t.Elem()
		a, s := std140Align(e)
		a = roundUp(a, 16)
		return a, roundUp(s, a) * t.Arr
	}
	switch {
	case t.K == KStruct:
		al, off := 16, 0
		for _, f := range t.S.Fields {
			fa, fs := std140Align(f.T)
			off = roundUp(off, fa)
			off += fs
		}
		return al, roundUp(off, al)
	case t.M > 1:
		return 16, 16 * t.M
	case t.N == 1:
		return 4, 4
	case t.N == 2:
		return 8, 8
	case t.N == 3:
		return 16, 12
	}
	return 16, 16
}

func (c *compiler) blockDecl(b *BlockDecl) {
	if b.Q.Storage != "uniform" || c.version != 300 {
		c.fail(b.Line, b.Name, "interface blocks are only supported for uniforms in ES 3.00")
	}
	blk := &Block{Name: b.Name, Inst: b.Inst, Binding: -1, ArrLen: 0}
	if b.HasArr {
		blk.ArrLen = c.arraySize(b.ArrLen)
	}
	bi := len(c.blocks)
	byteOff := 0
	st := &StructT{Name: b.Name}
	for _, f := range b.Fields {
		ft := c.resolveType(f.T, f.Line)
		if f.ArrLen != nil {
			ft.Arr = c.arraySize(f.ArrLen)
		}
		al, sz := std140Align(ft)
		byteOff = roundUp(byteOff, al)
		off := c.alloc(ft.Size())
		stride := 0
		if ft.Arr > 0 {
			stride = sz / ft.Arr
		}
		blk.Members = append(blk.Members, BlockMember{Name: f.Name, T: ft, Off: off, Byte: byteOff, Stride: stride})
		st.Fields = append(st.Fields, Field{f.Name, ft, off})
		byteOff += sz
	}
	blk.ByteSize = roundUp(byteOff, 16)
	c.blocks = append(c.blocks, blk)
	if b.Inst != "" {
		// registers are not contiguous with struct layout offsets; build the
		// struct with absolute offsets by using a base of 0 and member Off
		// fields as absolute register numbers.
		t := Type{K: KStruct, N: 1, M: 1, S: st}
		st.Size = 0
		c.declare(b.Line, &sym{name: b.Inst, kind: symVar, t: t, off: 0, ro: true, storage: "uniform", blk: blk})
		_ = bi
		return
	}
	for i, m := range blk.Members {
		u := &Uniform{Name: m.Name, T: m.T, Off: m.Off, Block: bi, Prec: ""}
		_ = i
		c.unis = append(c.unis, u)
		c.declare(b.Line, &sym{name: m.Name, kind: symVar, t: m.T, off: m.Off, ro: true, storage: "uniform", uni: u, blk: blk})
	}
}

// ---- functions ---------------------------------------------------------------

func (c *compiler) funcDef(fd *FuncDef) {
	ret := c.resolveType(fd.Ret, fd.Line)
	if fd.Ret.Arr && fd.Ret.ArrLen == nil {
		c.fail(fd.Line, fd.Name, "array return type must have a size")
	}
	var params []fnParam
	for _, p := range fd.Params {
		pt := c.resolveType(p.T, fd.Line)
		if p.HasArr {
			pt.Arr = c.arraySize(p.ArrLen)
		}
		if pt.K == KVoid {
			c.fail(fd.Line, fd.Name, "illegal use of type 'void'")
		}
		params = append(params, fnParam{t: pt, dir: p.Dir})
	}
	if len(fd.Name) > 3 && fd.Name[:3] == "gl_" {
		c.fail(fd.Line, fd.Name, "identifiers starting with \"gl_\" are reserved")
	}
	// find existing overload
	var f *fn
	var set *sym
	if s := c.lookup(fd.Name); s != nil {
		if s.kind != symFunc {
			c.fail(fd.Line, fd.Name, "redefinition")
		}
		set = s
		for _, o := range s.fns {
			if sameParams(o.params, params) {
				if !sameType(o.ret, ret) {
					c.fail(fd.Line, fd.Name, "overloaded functions must have the same return type")
				}
				if o.defined && !fd.Proto {
					c.fail(fd.Line, fd.Name, "function already has a body")
				}
				f = o
			}
		}
	}
	if f == nil {
		f = &fn{name: fd.Name, ret: ret, params: params, line: fd.Line}
		if set == nil {
			set = &sym{name: fd.Name, kind: symFunc}
			c.scopes[0][fd.Name] = set
		}
		set.fns = append(set.fns, f)
	}
	if fd.Name == "main" {
		if len(params) != 0 || ret.K != KVoid {
			c.fail(fd.Line, "main", "function 'main' must have a void return type and no parameters")
		}
		c.mainFn = f
	}
	if fd.Proto {
		return
	}
	// body
	c.curFn = f
	c.next = c.high
	c.mark = c.next
	c.pushScope()
	defer func() {
		c.popScope()
		c.curFn = nil
		c.next, c.mark = c.high, c.high
	}()
	for i, p := range fd.Params {
		off := c.alloc(params[i].t.Size())
		f.params[i].off = off
		if p.Name != "" {
			c.declare(fd.Line, &sym{name: p.Name, kind: symVar, t: params[i].t, off: off, ro: p.Const, storage: "param", prec: c.precOf(p.T, params[i].t)})
		}
	}
	if ret.K != KVoid {
		f.retOff = c.alloc(ret.Size())
	}
	c.mark = c.next
	f.defined = true
	body := c.stmts(fd.Body, false)
	f.body = func(fr *Frame) ctl {
		r := body(fr)
		if r == ctlDiscard {
			fr.Discarded = true
		}
		return ctlNone
	}
	if ret.K != KVoid && !returnsOnAllPaths(fd.Body) {
		c.fail(fd.Line, fd.Name, "function does not return a value")
	}
}

func returnsOnAllPaths(list []*Stmt) bool {
	for _, s := range list {
		if stmtReturns(s) {
			return true
		}
	}
	return false
}

func stmtReturns(s *Stmt) bool {
	switch s.K {
	case SReturn, SDiscard:
		return true
	case SBlock:
		return returnsOnAllPaths(s.Body)
	case SIf:
		return s.Else != nil && stmtReturns(s.Then) && stmtReturns(s.Else)
	case SFor, SWhile, SDo, SSwitch:
		return true // be permissive
	}
	return false
}

func sameParams(a, b []fnParam) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !sameType(a[i].t, b[i].t) {
			return false
		}
	}
	return true
}

func sameType(a, b Type) bool {
	return a.K == b.K && a.N == b.N && a.M == b.M && a.Arr == b.Arr && a.S == b.S && a.SK == b.SK
}

func (c *compiler) funcSigString(name string, args []*V) string {
	s := name + "("
	for i, a := range args {
		if i > 0 {
			s += ", "
		}
		s += a.T.String()
	}
	return s + ")"
}

var _ = fmt.Sprint
