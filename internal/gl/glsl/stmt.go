package glsl

type item struct {
	ops []op
	fn  stmtFn
}

func runOps(ops []op) stmtFn {
	switch len(ops) {
	case 0:
		return func(f *Frame) ctl { return ctlNone }
	case 1:
		o := ops[0]
		return func(f *Frame) ctl { o(f); return ctlNone }
	case 2:
		o1, o2 := ops[0], ops[1]
		return func(f *Frame) ctl { o1(f); o2(f); return ctlNone }
	case 3:
		o1, o2, o3 := ops[0], ops[1], ops[2]
		return func(f *Frame) ctl { o1(f); o2(f); o3(f); return ctlNone }
	}
	return func(f *Frame) ctl {
		for _, o := range ops {
			o(f)
		}
		return ctlNone
	}
}

func (c *compiler) seq(items []item) stmtFn {
	var fns []stmtFn
	var pend []op
	flush := func() {
		if len(pend) > 0 {
			fns = append(fns, runOps(pend))
			pend = nil
		}
	}
	for _, it := range items {
		if it.fn == nil {
			pend = append(pend, it.ops...)
			continue
		}
		pend = append(pend, it.ops...)
		flush()
		fns = append(fns, it.fn)
	}
	flush()
	switch len(fns) {
	case 0:
		return func(f *Frame) ctl { return ctlNone }
	case 1:
		return fns[0]
	case 2:
		a, b := fns[0], fns[1]
		return func(f *Frame) ctl {
			if r := a(f); r != ctlNone {
				return r
			}
			return b(f)
		}
	}
	return func(f *Frame) ctl {
		for _, s := range fns {
			if r := s(f); r != ctlNone {
				return r
			}
		}
		return ctlNone
	}
}

// stmts compiles a statement list, optionally in a fresh scope.
func (c *compiler) stmts(list []*Stmt, scope bool) stmtFn {
	savedMark := c.mark
	if scope {
		c.pushScope()
	}
	var items []item
	for _, s := range list {
		c.safe(func() { items = append(items, c.stmt(s)) })
		c.next = c.mark
	}
	if scope {
		c.popScope()
		c.mark = savedMark
		c.next = savedMark
	}
	return c.seq(items)
}

func (c *compiler) stmtBody(s *Stmt) stmtFn {
	// a sub-statement of if/for/while: own scope
	if s.K == SBlock {
		return c.stmts(s.Body, true)
	}
	savedMark := c.mark
	c.pushScope()
	var it item
	c.safe(func() { it = c.stmt(s) })
	c.popScope()
	c.mark, c.next = savedMark, savedMark
	return c.seq([]item{it})
}

func (c *compiler) condition(e *Expr) *V {
	v := c.mat(c.expr(e))
	if v.T != tBool {
		c.fail(e.Line, "", "boolean expression expected")
	}
	return v
}

func (c *compiler) stmt(s *Stmt) item {
	switch s.K {
	case SEmpty:
		return item{}
	case SExpr:
		e := s.E
		if e.K == EPostfix {
			e = &Expr{K: EUnary, S: e.S, Line: e.Line, Args: e.Args}
		}
		v := c.expr(e)
		return item{ops: v.Ops}
	case SDecl:
		var ops []op
		for _, d := range s.Decls {
			ops = append(ops, c.localDecl(d)...)
		}
		return item{ops: ops}
	case SBlock:
		return item{fn: c.stmts(s.Body, true)}
	case SIf:
		cv := c.condition(s.E)
		condOps := cv.Ops
		co := cv.Off
		if cv.C != nil {
			co = c.poolConst(cv.C)
		}
		th := c.stmtBody(s.Then)
		var el stmtFn
		if s.Else != nil {
			el = c.stmtBody(s.Else)
		}
		if len(condOps) == 0 {
			if el == nil {
				return item{fn: func(f *Frame) ctl {
					if f.R[co] != 0 {
						return th(f)
					}
					return ctlNone
				}}
			}
			return item{fn: func(f *Frame) ctl {
				if f.R[co] != 0 {
					return th(f)
				}
				return el(f)
			}}
		}
		condRun := runOps(condOps)
		return item{fn: func(f *Frame) ctl {
			condRun(f)
			if f.R[co] != 0 {
				return th(f)
			}
			if el != nil {
				return el(f)
			}
			return ctlNone
		}}
	case SFor:
		savedMark := c.mark
		c.pushScope()
		defer func() { c.popScope(); c.mark = savedMark; c.next = savedMark }()
		var initFn stmtFn
		if s.Init != nil {
			it := c.stmt(s.Init)
			c.next = c.mark
			initFn = c.seq([]item{it})
		}
		var condRun stmtFn
		co := -1
		if s.E != nil {
			cv := c.condition(s.E)
			co = cv.Off
			if cv.C != nil {
				co = c.poolConst(cv.C)
			}
			condRun = runOps(cv.Ops)
			if len(cv.Ops) == 0 {
				condRun = nil
			}
		}
		c.next = c.mark
		var postRun stmtFn
		if s.Post != nil {
			e := s.Post
			if e.K == EPostfix {
				e = &Expr{K: EUnary, S: e.S, Line: e.Line, Args: e.Args}
			}
			pv := c.expr(e)
			postRun = runOps(pv.Ops)
		}
		c.next = c.mark
		c.loops++
		body := c.stmtBody(s.Then)
		c.loops--
		return item{fn: func(f *Frame) ctl {
			if initFn != nil {
				initFn(f)
			}
			for {
				if co >= 0 {
					if condRun != nil {
						condRun(f)
					}
					if f.R[co] == 0 {
						break
					}
				}
				r := body(f)
				if r != ctlNone {
					if r == ctlBreak {
						break
					}
					if r != ctlContinue {
						return r
					}
				}
				if f.Discarded {
					return ctlDiscard
				}
				if postRun != nil {
					postRun(f)
				}
				f.budget--
				if f.budget < 0 {
					return ctlAbort
				}
			}
			return ctlNone
		}}
	case SWhile:
		savedMark := c.mark
		c.pushScope()
		defer func() { c.popScope(); c.mark = savedMark; c.next = savedMark }()
		cv := c.condition(s.E)
		co := cv.Off
		if cv.C != nil {
			co = c.poolConst(cv.C)
		}
		condRun := runOps(cv.Ops)
		c.next = c.mark
		c.loops++
		body := c.stmtBody(s.Then)
		c.loops--
		return item{fn: func(f *Frame) ctl {
			for {
				condRun(f)
				if f.R[co] == 0 {
					break
				}
				r := body(f)
				if r != ctlNone {
					if r == ctlBreak {
						break
					}
					if r != ctlContinue {
						return r
					}
				}
				if f.Discarded {
					return ctlDiscard
				}
				f.budget--
				if f.budget < 0 {
					return ctlAbort
				}
			}
			return ctlNone
		}}
	case SDo:
		c.loops++
		body := c.stmtBody(s.Then)
		c.loops--
		cv := c.condition(s.E)
		co := cv.Off
		if cv.C != nil {
			co = c.poolConst(cv.C)
		}
		condRun := runOps(cv.Ops)
		return item{fn: func(f *Frame) ctl {
			for {
				r := body(f)
				if r != ctlNone {
					if r == ctlBreak {
						break
					}
					if r != ctlContinue {
						return r
					}
				}
				condRun(f)
				if f.R[co] == 0 {
					break
				}
				if f.Discarded {
					return ctlDiscard
				}
				f.budget--
				if f.budget < 0 {
					return ctlAbort
				}
			}
			return ctlNone
		}}
	case SBreak:
		if c.loops == 0 && !c.inSwitch() {
			c.fail(s.Line, "break", "break statement only allowed in loops and switch")
		}
		return item{fn: func(f *Frame) ctl { return ctlBreak }}
	case SContinue:
		if c.loops == 0 {
			c.fail(s.Line, "continue", "continue statement only allowed in loops")
		}
		return item{fn: func(f *Frame) ctl { return ctlContinue }}
	case SDiscard:
		if c.stage != Fragment {
			c.fail(s.Line, "discard", "discard statement only allowed in fragment shaders")
		}
		c.sh.UsesDiscard = true
		return item{fn: func(f *Frame) ctl { f.Discarded = true; return ctlDiscard }}
	case SReturn:
		fnc := c.curFn
		if s.E == nil {
			if fnc.ret.K != KVoid {
				c.fail(s.Line, "return", "non-void function must return a value")
			}
			return item{fn: func(f *Frame) ctl { return ctlReturn }}
		}
		if fnc.ret.K == KVoid {
			c.fail(s.Line, "return", "void function cannot return a value")
		}
		c.hint = fnc.retOff
		v := c.mat(c.expr(s.E))
		c.hint = -1
		if !sameType(v.T, fnc.ret) {
			c.fail(s.Line, "return", "function return is not matching type:")
		}
		ops := append([]op{}, v.Ops...)
		ops = append(ops, opCopy(fnc.retOff, v.Off, v.T.Size()))
		rs := runOps(ops)
		return item{fn: func(f *Frame) ctl { rs(f); return ctlReturn }}
	case SSwitch:
		return c.switchStmt(s)
	case SCase, SDefault:
		c.fail(s.Line, "case", "case label outside switch")
	}
	c.fail(s.Line, "", "unsupported statement")
	return item{}
}

var switchDepth int

func (c *compiler) inSwitch() bool { return switchDepth > 0 }

func (c *compiler) switchStmt(s *Stmt) item {
	if c.version != 300 {
		c.fail(s.Line, "switch", "switch statements are not supported in GLSL ES 1.00")
	}
	sel := c.mat(c.expr(s.E))
	if !(sel.T == tInt || sel.T == tUint) {
		c.fail(s.Line, "switch", "init-expression in a switch statement must be a scalar integer")
	}
	selRun := runOps(sel.Ops)
	so := sel.Off
	savedMark := c.mark
	c.pushScope()
	defer func() { c.popScope(); c.mark = savedMark; c.next = savedMark }()
	switchDepth++
	defer func() { switchDepth-- }()
	var items []stmtFn
	var cases []uint32
	var caseIdx []int
	defIdx := -1
	var pend []item
	flush := func() {
		if len(pend) > 0 {
			items = append(items, c.seq(pend))
			pend = nil
		}
	}
	for _, st := range s.Body {
		switch st.K {
		case SCase:
			flush()
			cv := c.expr(st.E)
			if cv.C == nil {
				c.fail(st.Line, "case", "case label must be a constant expression")
			}
			cases = append(cases, cv.C[0])
			caseIdx = append(caseIdx, len(items))
		case SDefault:
			flush()
			defIdx = len(items)
		default:
			c.safe(func() { pend = append(pend, c.stmt(st)) })
			c.next = c.mark
		}
	}
	flush()
	// each pending group becomes one stmt; labels index into items
	return item{fn: func(f *Frame) ctl {
		selRun(f)
		v := f.R[so]
		start := -1
		for i, cv := range cases {
			if cv == v {
				start = caseIdx[i]
				break
			}
		}
		if start < 0 {
			start = defIdx
		}
		if start < 0 {
			return ctlNone
		}
		for i := start; i < len(items); i++ {
			r := items[i](f)
			if r == ctlBreak {
				return ctlNone
			}
			if r != ctlNone {
				return r
			}
		}
		return ctlNone
	}}
}

func (c *compiler) localDecl(d *Decl) []op {
	t := c.declType(d, d.T)
	if t.K == KVoid {
		c.fail(d.Line, d.Name, "illegal use of type 'void'")
	}
	if len(d.Name) >= 3 && d.Name[:3] == "gl_" {
		c.fail(d.Line, d.Name, "identifiers starting with \"gl_\" are reserved")
	}
	if d.Q.Storage != "" && d.Q.Storage != "const" {
		c.fail(d.Line, d.Q.Storage, "invalid qualifier")
	}
	if t.K == KSampler {
		c.fail(d.Line, d.Name, "samplers cannot be declared as local variables")
	}
	if d.Init != nil && t.Arr == -1 {
		init := c.expr(d.Init)
		t.Arr = init.T.Arr
	}
	if t.Arr == -1 {
		c.fail(d.Line, d.Name, "implicitly sized array requires an initializer")
	}
	off := c.alloc(t.Size())
	c.mark = c.next
	var ops []op
	declared := false
	defer func() {
		// declare the variable even when its initializer failed, so later
		// uses do not cascade into "undeclared identifier" errors
		if r := recover(); r != nil {
			if !declared {
				func() {
					defer func() { _ = recover() }()
					c.declare(d.Line, &sym{name: d.Name, kind: symVar, t: t, off: off, storage: "local", prec: c.precOf(d.T, t)})
				}()
			}
			panic(r)
		}
	}()
	if d.Q.Storage == "const" {
		if d.Init == nil {
			c.fail(d.Line, d.Name, "variables with qualifier 'const' must be initialized")
		}
		v := c.expr(d.Init)
		if !sameType(v.T, t) {
			c.fail(d.Line, "=", "cannot convert from '%s' to '%s'", c.tlong(v), c.dlong(d.T, t))
		}
		if v.C == nil {
			c.fail(d.Line, d.Name, "initializer of a const variable must be a constant expression")
		}
		c.declare(d.Line, &sym{name: d.Name, kind: symVar, t: t, c: v.C, ro: true, storage: "const"})
		declared = true
		return nil
	}
	if d.Init != nil {
		c.hint = off
		v := c.mat(c.expr(d.Init))
		c.hint = -1
		if !sameType(v.T, t) {
			c.fail(d.Line, "=", "cannot convert from '%s' to '%s'", c.tlong(v), c.dlong(d.T, t))
		}
		ops = append(ops, v.Ops...)
		if v.C != nil {
			ops = append(ops, opCopy(off, c.poolConst(v.C), t.Size()))
		} else if v.Off != off {
			ops = append(ops, opCopy(off, v.Off, t.Size()))
		}
	} else {
		n := t.Size()
		ops = append(ops, func(f *Frame) { clear(f.R[off : off+n]) })
	}
	c.declare(d.Line, &sym{name: d.Name, kind: symVar, t: t, off: off, storage: "local", prec: c.precOf(d.T, t)})
	declared = true
	return ops
}
