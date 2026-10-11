package glsl

import (
	"fmt"
	"sort"
	"strings"
)

// Options controls compilation.
type Options struct {
	WebGL2         bool            // allow #version 300 es
	Extensions     map[string]bool // extensions the context exposes (macros defined, behaviours accepted)
	MaxDrawBuffers int
}

const poolCap = 4096

type symKind uint8

const (
	symVar symKind = iota
	symFunc
	symType
)

type sym struct {
	name    string
	kind    symKind
	t       Type
	off     int
	c       []uint32
	ro      bool
	storage string
	fns     []*fn
	iov     *IOVar
	uni     *Uniform
	blk     *Block
}

type fnParam struct {
	t   Type
	dir string
	off int
}

type fn struct {
	name    string
	ret     Type
	params  []fnParam
	retOff  int
	body    stmtFn
	defined bool
	line    int
}

type cerr struct {
	line int
	tok  string
	msg  string
}

type dyn struct{ idx, stride, max int }

// V is a compiled expression.
type V struct {
	T     Type
	Off   int
	Ops   []op
	C     []uint32
	Place bool
	RO    bool
	Dyn   []dyn
	Lanes []int
	Sym   *sym
	roWhy string
}

type compiler struct {
	sh      *Shader
	stage   Stage
	version int
	opt     Options
	errs    []string
	scopes  []map[string]*sym
	next    int
	mark    int
	high    int
	pool    []uint32
	poolIdx map[string]int
	hint    int
	curFn   *fn
	inputs  []*IOVar
	outputs []*IOVar
	unis    []*Uniform
	blocks  []*Block
	globInit []op
	loops   int
	exts    map[string]string
	structs int
	mainFn  *fn
	fragOutLoc int
	line    int
}

func (c *compiler) fail(line int, tok, f string, a ...any) {
	panic(cerr{line, tok, fmt.Sprintf(f, a...)})
}

func (c *compiler) report(e cerr) {
	c.errs = append(c.errs, fmt.Sprintf("ERROR: 0:%d: '%s' : %s", e.line, e.tok, e.msg))
}

// Compile compiles one shader. The returned Shader is never nil; check OK
// and Log.
func Compile(stage Stage, src string, opt Options) *Shader {
	sh := &Shader{Stage: stage, Version: 100, source: src, Exts: map[string]string{},
		PositionOff: -1, PointSizeOff: -1, VertexIDOff: -1, InstanceIDOff: -1, FragCoordOff: -1,
		FrontFacingOff: -1, PointCoordOff: -1, FragColorOff: -1, FragDataOff: -1, FragDepthOff: -1, DepthRangeOff: -1}
	if opt.MaxDrawBuffers == 0 {
		opt.MaxDrawBuffers = 4
	}
	toks, err := lex(src)
	if err != nil {
		sh.Log = err.Error()
		return sh
	}
	predef := map[string]string{"GL_ES": "1"}
	if stage == Fragment {
		predef["GL_FRAGMENT_PRECISION_HIGH"] = "1"
	}
	known := map[string]bool{"GL_OES_standard_derivatives": true, "GL_EXT_frag_depth": true, "GL_EXT_shader_texture_lod": true,
		"GL_EXT_draw_buffers": true, "GL_OES_texture_float": true, "GL_OES_element_index_uint": true}
	supported := map[string]bool{}
	for k := range opt.Extensions {
		if opt.Extensions[k] {
			predef[k] = "1"
			supported[k] = true
		}
	}
	_ = known
	ptoks, pp := preprocess(toks, predef, supported)
	sh.Version = pp.version
	if len(pp.errs) == 0 && pp.version == 300 && !opt.WebGL2 {
		pp.errs = append(pp.errs, "ERROR: 0:1: '#version' : version number not supported")
	}
	if len(pp.errs) > 0 {
		sh.Log = strings.Join(pp.errs, "\n") + "\n"
		return sh
	}
	c := &compiler{sh: sh, stage: stage, version: pp.version, opt: opt, poolIdx: map[string]int{}, hint: -1, exts: map[string]string{}}
	for _, d := range pp.exts {
		c.exts[d.Name] = d.Behavior
		sh.Exts[d.Name] = d.Behavior
	}
	tops, perrs := parseTokens(ptoks, pp.version)
	if len(perrs) > 0 {
		sh.Log = strings.Join(perrs, "\n") + "\n"
		return sh
	}
	c.pool = make([]uint32, 0, 64)
	c.next = poolCap
	c.mark, c.high = poolCap, poolCap
	c.pushScope()
	c.declareBuiltins()
	for _, t := range tops {
		c.top(t)
	}
	if len(c.errs) == 0 {
		if c.mainFn == nil || !c.mainFn.defined {
			c.errs = append(c.errs, "ERROR: 0:1: 'main' : function is undefined")
		} else {
			sh.main = c.mainFn.body
		}
	}
	if len(c.errs) > 0 {
		sh.Log = strings.Join(c.errs, "\n") + "\n"
		return sh
	}
	c.finish()
	sh.OK = true
	return sh
}

func (c *compiler) finish() {
	sh := c.sh
	sh.regs = c.high + 8
	sh.pool = append([]uint32(nil), c.pool...)
	sh.globalInit = c.globInit
	for _, v := range c.inputs {
		sh.Inputs = append(sh.Inputs, *v)
	}
	for _, v := range c.outputs {
		sh.Outputs = append(sh.Outputs, *v)
	}
	for _, v := range c.unis {
		sh.Uniforms = append(sh.Uniforms, *v)
	}
	sh.Blocks = c.blocks
	if sh.Stage == Fragment {
		// assign output locations
		sort.SliceStable(sh.Outputs, func(i, j int) bool { return sh.Outputs[i].Loc < sh.Outputs[j].Loc })
	}
}

// ---- scopes & allocation -----------------------------------------------------

func (c *compiler) pushScope() { c.scopes = append(c.scopes, map[string]*sym{}) }
func (c *compiler) popScope()  { c.scopes = c.scopes[:len(c.scopes)-1] }

func (c *compiler) lookup(name string) *sym {
	for i := len(c.scopes) - 1; i >= 0; i-- {
		if s, ok := c.scopes[i][name]; ok {
			return s
		}
	}
	return nil
}

func (c *compiler) declare(line int, s *sym) {
	top := c.scopes[len(c.scopes)-1]
	if old, ok := top[s.name]; ok && !(old.kind == symFunc && s.kind == symFunc) {
		c.fail(line, s.name, "redefinition")
	}
	top[s.name] = s
}

func (c *compiler) alloc(n int) int {
	off := c.next
	c.next += n
	if c.next > c.high {
		c.high = c.next
	}
	return off
}

// tmp allocates temporaries for the current statement.
func (c *compiler) tmp(n int) int {
	if n == 0 {
		n = 1
	}
	return c.alloc(n)
}

func (c *compiler) dst(h, n int) int {
	if h >= 0 {
		return h
	}
	return c.tmp(n)
}

// poolConst stores lanes in the constant pool and returns the offset.
func (c *compiler) poolConst(lanes []uint32) int {
	key := fmt.Sprint(lanes)
	if off, ok := c.poolIdx[key]; ok {
		return off
	}
	off := len(c.pool)
	if off+len(lanes) > poolCap {
		panic(cerr{1, "constant", "too many constants"})
	}
	c.pool = append(c.pool, lanes...)
	c.poolIdx[key] = off
	return off
}

func cv(t Type, lanes ...uint32) *V { return &V{T: t, C: lanes} }

// ---- built-ins -------------------------------------------------------------

func (c *compiler) builtinVar(name string, t Type, ro bool) int {
	off := c.alloc(t.Size())
	c.scopes[0][name] = &sym{name: name, kind: symVar, t: t, off: off, ro: ro, storage: "builtin"}
	return off
}

func (c *compiler) declareBuiltins() {
	sh := c.sh
	for _, k := range []struct {
		n string
		v int32
	}{{"gl_MaxVertexAttribs", 16}, {"gl_MaxVertexUniformVectors", 1024}, {"gl_MaxVaryingVectors", 30},
		{"gl_MaxVertexTextureImageUnits", 16}, {"gl_MaxCombinedTextureImageUnits", 32}, {"gl_MaxTextureImageUnits", 16},
		{"gl_MaxFragmentUniformVectors", 1024}, {"gl_MaxDrawBuffers", int32(c.opt.MaxDrawBuffers)},
		{"gl_MinProgramTexelOffset", -8}, {"gl_MaxProgramTexelOffset", 7}, {"gl_MaxVaryingComponents", 120},
		{"gl_MaxVertexOutputVectors", 16}, {"gl_MaxFragmentInputVectors", 15}} {
		if k.n == "gl_MaxVaryingComponents" && c.version == 300 || k.n == "gl_MaxVaryingVectors" && c.version == 300 {
			continue
		}
		if (k.n == "gl_MinProgramTexelOffset" || k.n == "gl_MaxProgramTexelOffset" || k.n == "gl_MaxVertexOutputVectors" || k.n == "gl_MaxFragmentInputVectors") && c.version != 300 {
			continue
		}
		c.scopes[0][k.n] = &sym{name: k.n, kind: symVar, t: tInt, c: []uint32{uint32(k.v)}, ro: true}
	}
	// gl_DepthRange
	drT := Type{K: KStruct, N: 1, M: 1, S: &StructT{Name: "gl_DepthRangeParameters", Size: 3, Fields: []Field{
		{"near", tFloat, 0}, {"far", tFloat, 1}, {"diff", tFloat, 2}}}}
	sh.DepthRangeOff = c.builtinVar("gl_DepthRange", drT, true)
	if c.stage == Vertex {
		sh.PositionOff = c.builtinVar("gl_Position", vecT(KFloat, 4), false)
		sh.PointSizeOff = c.builtinVar("gl_PointSize", tFloat, false)
		if c.version == 300 {
			sh.VertexIDOff = c.builtinVar("gl_VertexID", tInt, true)
			sh.InstanceIDOff = c.builtinVar("gl_InstanceID", tInt, true)
		}
	} else {
		sh.FragCoordOff = c.builtinVar("gl_FragCoord", vecT(KFloat, 4), true)
		sh.FrontFacingOff = c.builtinVar("gl_FrontFacing", tBool, true)
		sh.PointCoordOff = c.builtinVar("gl_PointCoord", vecT(KFloat, 2), true)
		if c.version == 100 {
			sh.FragColorOff = c.builtinVar("gl_FragColor", vecT(KFloat, 4), false)
			dt := vecT(KFloat, 4)
			dt.Arr = c.opt.MaxDrawBuffers
			sh.FragDataOff = c.builtinVar("gl_FragData", dt, false)
			if c.extEnabled("GL_EXT_frag_depth") {
				sh.FragDepthOff = c.builtinVar("gl_FragDepthEXT", tFloat, false)
			}
		} else {
			sh.FragDepthOff = c.builtinVar("gl_FragDepth", tFloat, false)
		}
	}
}

// ---- type resolution ----------------------------------------------------------

func (c *compiler) resolveType(ts *TypeSpec, line int) Type {
	var t Type
	if ts.Struct != nil && ts.Struct.Name == "" || ts.Struct != nil && c.lookupStructHere(ts.Struct.Name) == nil {
		t = c.defineStruct(ts.Struct)
	} else if bt, ok := basicType(ts.Name); ok {
		t = bt
	} else if s := c.lookup(ts.Name); s != nil && s.kind == symType {
		t = s.t
	} else {
		c.fail(line, ts.Name, "undeclared identifier")
	}
	if ts.Arr {
		if ts.ArrLen == nil {
			return t // unsized; caller resolves from initializer
		}
		t.Arr = c.arraySize(ts.ArrLen)
	}
	return t
}

func (c *compiler) lookupStructHere(name string) *sym {
	if name == "" {
		return nil
	}
	if s, ok := c.scopes[len(c.scopes)-1][name]; ok && s.kind == symType {
		return s
	}
	return nil
}

func (c *compiler) arraySize(e *Expr) int {
	v := c.expr(e)
	if v.C == nil || !(v.T.K == KInt || v.T.K == KUint) || !v.T.IsScalar() {
		c.fail(e.Line, "[]", "array size must be a constant integer expression")
	}
	n := int(int32(v.C[0]))
	if n <= 0 {
		c.fail(e.Line, "[]", "array size must be greater than zero")
	}
	return n
}

func (c *compiler) defineStruct(ss *StructSpec) Type {
	st := &StructT{Name: ss.Name}
	off := 0
	seen := map[string]bool{}
	for _, f := range ss.Fields {
		ft := c.resolveType(f.T, f.Line)
		if f.ArrLen != nil {
			ft.Arr = c.arraySize(f.ArrLen)
		}
		if ft.K == KVoid {
			c.fail(f.Line, f.Name, "illegal use of type 'void'")
		}
		if seen[f.Name] {
			c.fail(f.Line, f.Name, "duplicate field name in structure")
		}
		seen[f.Name] = true
		st.Fields = append(st.Fields, Field{f.Name, ft, off})
		off += ft.Size()
	}
	st.Size = off
	t := Type{K: KStruct, N: 1, M: 1, S: st}
	if ss.Name != "" {
		c.declare(ss.Line, &sym{name: ss.Name, kind: symType, t: t})
	}
	return t
}

func isFloatKind(t Type) bool { return t.K == KFloat }
