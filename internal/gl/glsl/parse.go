package glsl

import (
	"fmt"
	"strconv"
)

// ---- AST -------------------------------------------------------------------

type EK uint8

const (
	EIdent EK = iota
	EInt
	EUint
	EFloat
	EBool
	EUnary   // S = op (prefix: - + ! ~ ++ --)
	EPostfix // S = ++ or --
	EBinary  // S = op
	EAssign  // S = = += ...
	ETernary
	ECall  // S = function name or constructor type name; Type set for constructors
	EIndex // Args[0][Args[1]]
	EField // Args[0].S
	EComma
)

type Expr struct {
	K    EK
	Line int
	S    string
	Args []*Expr
	I    int64
	F    float64
	Type *TypeSpec // constructor of array type or struct
}

type StructField struct {
	Name   string
	T      *TypeSpec
	ArrLen *Expr
	Line   int
}

type StructSpec struct {
	Name   string
	Fields []*StructField
	Line   int
}

type TypeSpec struct {
	Name   string      // basic type or struct name
	Struct *StructSpec // inline struct definition
	ArrLen *Expr       // float[3]
	Arr    bool        // true for float[] / float[3]
	Prec   string
	Line   int
}

type Qual struct {
	Storage   string // const attribute varying uniform in out inout (""=none)
	Interp    string // flat smooth
	Centroid  bool
	Invariant bool
	Prec      string
	Layout    map[string]int
	Std       string // std140 etc.
}

type Decl struct {
	Q      Qual
	T      *TypeSpec
	Name   string
	ArrLen *Expr
	HasArr bool
	Init   *Expr
	Line   int
}

type SK uint8

const (
	SExpr SK = iota
	SDecl
	SBlock
	SIf
	SFor
	SWhile
	SDo
	SReturn
	SBreak
	SContinue
	SDiscard
	SEmpty
	SSwitch
	SCase
	SDefault
)

type Stmt struct {
	K     SK
	Line  int
	E     *Expr
	Decls []*Decl
	Body  []*Stmt
	Then  *Stmt
	Else  *Stmt
	Init  *Stmt
	Post  *Expr
	// switch
}

type Param struct {
	Dir    string // in out inout
	T      *TypeSpec
	Name   string
	ArrLen *Expr
	HasArr bool
	Const  bool
}

type FuncDef struct {
	Ret    *TypeSpec
	Name   string
	Params []*Param
	Body   []*Stmt
	Proto  bool
	Line   int
}

type BlockDecl struct {
	Q      Qual
	Name   string
	Fields []*StructField
	Inst   string
	ArrLen *Expr
	HasArr bool
	Line   int
}

type TopKind uint8

const (
	TopDecl TopKind = iota
	TopFunc
	TopStruct
	TopPrecision
	TopBlock
	TopInvariant
)

type Top struct {
	K      TopKind
	Decls  []*Decl
	Func   *FuncDef
	Struct *StructSpec
	Block  *BlockDecl
	Line   int
}

// ---- Parser ----------------------------------------------------------------

type parser struct {
	toks    []Tok
	i       int
	version int
	structs map[string]bool
	errs    []string
}

type parseErr struct{ msg string }

func (p *parser) fail(t Tok, what string) {
	s := t.S
	if t.K == TEOF {
		s = "<EOF>"
	}
	panic(parseErr{fmt.Sprintf("ERROR: 0:%d: '%s' : %s", t.Line, s, what)})
}

func (p *parser) cur() Tok  { return p.toks[p.i] }
func (p *parser) peek(n int) Tok {
	if p.i+n < len(p.toks) {
		return p.toks[p.i+n]
	}
	return p.toks[len(p.toks)-1]
}
func (p *parser) isP(s string) bool { t := p.cur(); return t.K == TPunct && t.S == s }
func (p *parser) isK(s string) bool { t := p.cur(); return t.K == TIdent && t.S == s }
func (p *parser) acceptP(s string) bool {
	if p.isP(s) {
		p.i++
		return true
	}
	return false
}
func (p *parser) acceptK(s string) bool {
	if p.isK(s) {
		p.i++
		return true
	}
	return false
}
func (p *parser) expectP(s string) Tok {
	t := p.cur()
	if !p.isP(s) {
		p.fail(t, "syntax error")
	}
	p.i++
	return t
}

var basicTypeNames = map[string]bool{
	"void": true, "bool": true, "int": true, "uint": true, "float": true,
	"vec2": true, "vec3": true, "vec4": true, "bvec2": true, "bvec3": true, "bvec4": true,
	"ivec2": true, "ivec3": true, "ivec4": true, "uvec2": true, "uvec3": true, "uvec4": true,
	"mat2": true, "mat3": true, "mat4": true, "mat2x2": true, "mat2x3": true, "mat2x4": true,
	"mat3x2": true, "mat3x3": true, "mat3x4": true, "mat4x2": true, "mat4x3": true, "mat4x4": true,
	"sampler2D": true, "samplerCube": true, "sampler3D": true, "sampler2DArray": true,
	"sampler2DShadow": true, "samplerCubeShadow": true, "sampler2DArrayShadow": true,
	"isampler2D": true, "isampler3D": true, "isamplerCube": true, "isampler2DArray": true,
	"usampler2D": true, "usampler3D": true, "usamplerCube": true, "usampler2DArray": true,
}

var keywords = map[string]bool{
	"attribute": true, "const": true, "uniform": true, "varying": true, "break": true, "continue": true,
	"do": true, "for": true, "while": true, "if": true, "else": true, "in": true, "out": true, "inout": true,
	"true": true, "false": true, "discard": true, "return": true, "struct": true, "precision": true,
	"highp": true, "mediump": true, "lowp": true, "invariant": true, "layout": true, "flat": true,
	"smooth": true, "centroid": true, "switch": true, "case": true, "default": true,
}

func (p *parser) isTypeStart() bool {
	t := p.cur()
	if t.K != TIdent {
		return false
	}
	return basicTypeNames[t.S] || p.structs[t.S]
}

func isQualKeyword(s string) bool {
	switch s {
	case "const", "attribute", "varying", "uniform", "in", "out", "inout", "highp", "mediump", "lowp",
		"invariant", "layout", "flat", "smooth", "centroid":
		return true
	}
	return false
}

func parseTokens(toks []Tok, version int) (tops []*Top, errs []string) {
	p := &parser{toks: toks, version: version, structs: map[string]bool{}}
	defer func() {
		if r := recover(); r != nil {
			if pe, ok := r.(parseErr); ok {
				errs = append(errs, pe.msg)
				return
			}
			panic(r)
		}
	}()
	for p.cur().K != TEOF {
		if p.acceptP(";") {
			continue
		}
		tops = append(tops, p.topLevel()...)
	}
	return tops, nil
}

func (p *parser) qualifiers() Qual {
	var q Qual
	for {
		t := p.cur()
		if t.K != TIdent || !isQualKeyword(t.S) {
			return q
		}
		p.i++
		switch t.S {
		case "const", "attribute", "varying", "uniform", "in", "out", "inout":
			if q.Storage == "in" && t.S == "out" || q.Storage == "out" && t.S == "in" {
				q.Storage = "inout"
			} else if t.S == "const" && q.Storage != "" {
				// const in
				if q.Storage == "in" {
					q.Storage = "const"
				}
			} else if q.Storage == "const" && t.S == "in" {
			} else {
				q.Storage = t.S
			}
		case "highp", "mediump", "lowp":
			q.Prec = t.S
		case "invariant":
			q.Invariant = true
		case "flat", "smooth":
			q.Interp = t.S
		case "centroid":
			q.Centroid = true
		case "layout":
			p.expectP("(")
			q.Layout = map[string]int{}
			for {
				id := p.cur()
				if id.K != TIdent {
					p.fail(id, "syntax error")
				}
				p.i++
				v := 0
				if p.acceptP("=") {
					n := p.cur()
					x, err := strconv.ParseInt(n.S, 0, 64)
					if err != nil {
						p.fail(n, "syntax error")
					}
					v = int(x)
					p.i++
				}
				if id.S == "std140" || id.S == "std430" || id.S == "shared" || id.S == "packed" {
					q.Std = id.S
				} else {
					q.Layout[id.S] = v
				}
				if !p.acceptP(",") {
					break
				}
			}
			p.expectP(")")
		}
	}
}

func (p *parser) typeSpec() *TypeSpec {
	t := p.cur()
	ts := &TypeSpec{Line: t.Line}
	for t.K == TIdent && (t.S == "highp" || t.S == "mediump" || t.S == "lowp") {
		ts.Prec = t.S
		p.i++
		t = p.cur()
	}
	if t.K == TIdent && t.S == "struct" {
		p.i++
		ss := &StructSpec{Line: t.Line}
		if p.cur().K == TIdent && !p.isP("{") {
			ss.Name = p.cur().S
			p.i++
		}
		p.expectP("{")
		for !p.isP("}") {
			fq := p.qualifiers()
			_ = fq
			ft := p.typeSpec()
			for {
				nt := p.cur()
				if nt.K != TIdent {
					p.fail(nt, "syntax error")
				}
				p.i++
				f := &StructField{Name: nt.S, T: ft, Line: nt.Line}
				if p.acceptP("[") {
					f.ArrLen = p.expr()
					p.expectP("]")
				}
				ss.Fields = append(ss.Fields, f)
				if !p.acceptP(",") {
					break
				}
			}
			p.expectP(";")
		}
		p.expectP("}")
		if ss.Name != "" {
			p.structs[ss.Name] = true
		}
		ts.Struct = ss
		ts.Name = ss.Name
	} else if t.K == TIdent && (basicTypeNames[t.S] || p.structs[t.S]) {
		ts.Name = t.S
		p.i++
	} else {
		p.fail(t, "syntax error")
	}
	if p.isP("[") {
		p.i++
		ts.Arr = true
		if !p.isP("]") {
			ts.ArrLen = p.expr()
		}
		p.expectP("]")
	}
	return ts
}

func (p *parser) topLevel() []*Top {
	t := p.cur()
	if p.isK("precision") {
		p.i++
		p.cur()
		p.i++ // precision qualifier
		p.typeSpec()
		p.expectP(";")
		return []*Top{{K: TopPrecision, Line: t.Line}}
	}
	if p.isK("invariant") && p.peek(1).K == TIdent && !p.isTypeStartAt(1) && p.peek(1).S != "varying" && p.peek(1).S != "out" {
		p.i++
		for p.cur().K == TIdent {
			p.i++
			if !p.acceptP(",") {
				break
			}
		}
		p.expectP(";")
		return []*Top{{K: TopInvariant, Line: t.Line}}
	}
	q := p.qualifiers()
	// interface block: storage Name {
	if (q.Storage == "uniform" || q.Storage == "in" || q.Storage == "out") && p.cur().K == TIdent && !p.isTypeStart() && p.peek(1).S == "{" {
		bd := &BlockDecl{Q: q, Name: p.cur().S, Line: t.Line}
		p.i += 2
		for !p.isP("}") {
			p.qualifiers()
			ft := p.typeSpec()
			for {
				nt := p.cur()
				if nt.K != TIdent {
					p.fail(nt, "syntax error")
				}
				p.i++
				f := &StructField{Name: nt.S, T: ft, Line: nt.Line}
				if p.acceptP("[") {
					f.ArrLen = p.expr()
					p.expectP("]")
				}
				bd.Fields = append(bd.Fields, f)
				if !p.acceptP(",") {
					break
				}
			}
			p.expectP(";")
		}
		p.expectP("}")
		if p.cur().K == TIdent {
			bd.Inst = p.cur().S
			p.i++
			if p.acceptP("[") {
				bd.HasArr = true
				bd.ArrLen = p.expr()
				p.expectP("]")
			}
		}
		p.expectP(";")
		return []*Top{{K: TopBlock, Block: bd, Line: t.Line}}
	}
	ts := p.typeSpec()
	if ts.Prec == "" {
		ts.Prec = q.Prec
	}
	// struct declaration only
	if p.isP(";") {
		p.i++
		if ts.Struct != nil {
			return []*Top{{K: TopStruct, Struct: ts.Struct, Line: t.Line}}
		}
		return nil
	}
	nt := p.cur()
	if nt.K != TIdent {
		p.fail(nt, "syntax error")
	}
	// function?
	if p.peek(1).S == "(" && p.peek(1).K == TPunct {
		p.i += 2
		fd := &FuncDef{Ret: ts, Name: nt.S, Line: nt.Line}
		if p.isK("void") && p.peek(1).S == ")" {
			p.i++
		}
		for !p.isP(")") {
			pq := p.qualifiers()
			pt := p.typeSpec()
			pr := &Param{T: pt, Dir: "in", Const: pq.Storage == "const"}
			switch pq.Storage {
			case "in", "out", "inout":
				pr.Dir = pq.Storage
			}
			if p.cur().K == TIdent && !p.isP(",") {
				pr.Name = p.cur().S
				p.i++
			}
			if p.acceptP("[") {
				pr.HasArr = true
				pr.ArrLen = p.expr()
				p.expectP("]")
			}
			fd.Params = append(fd.Params, pr)
			if !p.acceptP(",") {
				break
			}
		}
		p.expectP(")")
		if p.acceptP(";") {
			fd.Proto = true
			return []*Top{{K: TopFunc, Func: fd, Line: nt.Line}}
		}
		fd.Body = p.blockBody()
		return []*Top{{K: TopFunc, Func: fd, Line: nt.Line}}
	}
	// variable declarations
	var tops []*Top
	if ts.Struct != nil && ts.Struct.Name != "" {
		tops = append(tops, &Top{K: TopStruct, Struct: ts.Struct, Line: t.Line})
		ts = &TypeSpec{Name: ts.Struct.Name, Line: ts.Line, Prec: ts.Prec, Arr: ts.Arr, ArrLen: ts.ArrLen}
	}
	decls := p.declarators(q, ts)
	p.expectP(";")
	tops = append(tops, &Top{K: TopDecl, Decls: decls, Line: t.Line})
	return tops
}

func (p *parser) isTypeStartAt(n int) bool {
	t := p.peek(n)
	return t.K == TIdent && (basicTypeNames[t.S] || p.structs[t.S])
}

func (p *parser) declarators(q Qual, ts *TypeSpec) []*Decl {
	var out []*Decl
	for {
		nt := p.cur()
		if nt.K != TIdent || keywords[nt.S] {
			p.fail(nt, "syntax error")
		}
		p.i++
		d := &Decl{Q: q, T: ts, Name: nt.S, Line: nt.Line}
		if p.acceptP("[") {
			d.HasArr = true
			if !p.isP("]") {
				d.ArrLen = p.expr()
			}
			p.expectP("]")
		}
		if p.acceptP("=") {
			d.Init = p.assignExpr()
		}
		out = append(out, d)
		if !p.acceptP(",") {
			break
		}
	}
	return out
}

func (p *parser) blockBody() []*Stmt {
	p.expectP("{")
	var out []*Stmt
	for !p.isP("}") {
		if p.cur().K == TEOF {
			p.fail(p.cur(), "syntax error")
		}
		out = append(out, p.stmt())
	}
	p.expectP("}")
	return out
}

func (p *parser) isDeclStart() bool {
	t := p.cur()
	if t.K != TIdent {
		return false
	}
	if isQualKeyword(t.S) && t.S != "layout" || t.S == "struct" {
		return true
	}
	if basicTypeNames[t.S] || p.structs[t.S] {
		n := p.peek(1)
		if n.K == TPunct && n.S == "(" {
			return false // constructor expression
		}
		if n.K == TPunct && n.S == "[" {
			// float[3](...) constructor vs float[3] a
			k := 2
			depth := 1
			for p.peek(k).K != TEOF && depth > 0 {
				if p.peek(k).S == "[" {
					depth++
				} else if p.peek(k).S == "]" {
					depth--
				}
				k++
			}
			return p.peek(k).K == TIdent
		}
		return true
	}
	return false
}

func (p *parser) stmt() *Stmt {
	t := p.cur()
	s := &Stmt{Line: t.Line}
	if t.K == TPunct {
		switch t.S {
		case "{":
			s.K = SBlock
			s.Body = p.blockBody()
			return s
		case ";":
			p.i++
			s.K = SEmpty
			return s
		}
	}
	if t.K == TIdent {
		switch t.S {
		case "if":
			p.i++
			p.expectP("(")
			s.E = p.expr()
			p.expectP(")")
			s.K = SIf
			s.Then = p.stmt()
			if p.acceptK("else") {
				s.Else = p.stmt()
			}
			return s
		case "for":
			p.i++
			p.expectP("(")
			s.K = SFor
			if p.isP(";") {
				p.i++
			} else if p.isDeclStart() {
				s.Init = p.declStmt()
			} else {
				e := p.expr()
				p.expectP(";")
				s.Init = &Stmt{K: SExpr, E: e, Line: t.Line}
			}
			if !p.isP(";") {
				s.E = p.expr()
			}
			p.expectP(";")
			if !p.isP(")") {
				s.Post = p.expr()
			}
			p.expectP(")")
			s.Then = p.stmt()
			return s
		case "while":
			p.i++
			p.expectP("(")
			s.K = SWhile
			if p.isDeclStart() {
				// while (bool x = ...) : rare; treat as expression-declaration
				p.fail(p.cur(), "syntax error")
			}
			s.E = p.expr()
			p.expectP(")")
			s.Then = p.stmt()
			return s
		case "do":
			p.i++
			s.K = SDo
			s.Then = p.stmt()
			if !p.acceptK("while") {
				p.fail(p.cur(), "syntax error")
			}
			p.expectP("(")
			s.E = p.expr()
			p.expectP(")")
			p.expectP(";")
			return s
		case "return":
			p.i++
			s.K = SReturn
			if !p.isP(";") {
				s.E = p.expr()
			}
			p.expectP(";")
			return s
		case "break":
			p.i++
			p.expectP(";")
			s.K = SBreak
			return s
		case "continue":
			p.i++
			p.expectP(";")
			s.K = SContinue
			return s
		case "discard":
			p.i++
			p.expectP(";")
			s.K = SDiscard
			return s
		case "switch":
			p.i++
			p.expectP("(")
			s.K = SSwitch
			s.E = p.expr()
			p.expectP(")")
			s.Body = p.blockBody()
			return s
		case "case":
			p.i++
			s.K = SCase
			s.E = p.expr()
			p.expectP(":")
			return s
		case "default":
			p.i++
			p.expectP(":")
			s.K = SDefault
			return s
		}
	}
	if p.isDeclStart() {
		return p.declStmt()
	}
	e := p.expr()
	p.expectP(";")
	s.K, s.E = SExpr, e
	return s
}

func (p *parser) declStmt() *Stmt {
	t := p.cur()
	q := p.qualifiers()
	ts := p.typeSpec()
	if ts.Prec == "" {
		ts.Prec = q.Prec
	}
	s := &Stmt{K: SDecl, Line: t.Line}
	if p.isP(";") {
		p.i++
		return s
	}
	s.Decls = p.declarators(q, ts)
	p.expectP(";")
	return s
}

// ---- expressions -----------------------------------------------------------

func (p *parser) expr() *Expr {
	e := p.assignExpr()
	for p.isP(",") {
		t := p.cur()
		p.i++
		r := p.assignExpr()
		e = &Expr{K: EComma, Line: t.Line, Args: []*Expr{e, r}}
	}
	return e
}

func (p *parser) assignExpr() *Expr {
	l := p.ternaryExpr()
	t := p.cur()
	if t.K == TPunct {
		switch t.S {
		case "=", "+=", "-=", "*=", "/=", "%=", "<<=", ">>=", "&=", "|=", "^=":
			p.i++
			r := p.assignExpr()
			return &Expr{K: EAssign, S: t.S, Line: t.Line, Args: []*Expr{l, r}}
		}
	}
	return l
}

func (p *parser) ternaryExpr() *Expr {
	c := p.binExpr(0)
	if p.isP("?") {
		t := p.cur()
		p.i++
		a := p.expr()
		p.expectP(":")
		b := p.assignExpr()
		return &Expr{K: ETernary, Line: t.Line, Args: []*Expr{c, a, b}}
	}
	return c
}

var binPrec = map[string]int{"||": 1, "^^": 2, "&&": 3, "|": 4, "^": 5, "&": 6, "==": 7, "!=": 7, "<": 8, ">": 8, "<=": 8, ">=": 8, "<<": 9, ">>": 9, "+": 10, "-": 10, "*": 11, "/": 11, "%": 11}

func (p *parser) binExpr(min int) *Expr {
	l := p.unaryExpr()
	for {
		t := p.cur()
		if t.K != TPunct {
			return l
		}
		pr, ok := binPrec[t.S]
		if !ok || pr <= min {
			return l
		}
		p.i++
		r := p.binExpr(pr)
		l = &Expr{K: EBinary, S: t.S, Line: t.Line, Args: []*Expr{l, r}}
	}
}

func (p *parser) unaryExpr() *Expr {
	t := p.cur()
	if t.K == TPunct {
		switch t.S {
		case "+", "-", "!", "~", "++", "--":
			p.i++
			a := p.unaryExpr()
			return &Expr{K: EUnary, S: t.S, Line: t.Line, Args: []*Expr{a}}
		}
	}
	return p.postfixExpr()
}

func (p *parser) postfixExpr() *Expr {
	e := p.primary()
	for {
		t := p.cur()
		if t.K != TPunct {
			return e
		}
		switch t.S {
		case "[":
			p.i++
			idx := p.expr()
			p.expectP("]")
			e = &Expr{K: EIndex, Line: t.Line, Args: []*Expr{e, idx}}
		case ".":
			p.i++
			f := p.cur()
			if f.K != TIdent {
				p.fail(f, "syntax error")
			}
			p.i++
			e = &Expr{K: EField, S: f.S, Line: f.Line, Args: []*Expr{e}}
			if f.S == "length" && p.isP("(") {
				p.i++
				p.expectP(")")
				e = &Expr{K: ECall, S: "__length", Line: f.Line, Args: []*Expr{e.Args[0]}}
			}
		case "++", "--":
			p.i++
			e = &Expr{K: EPostfix, S: t.S, Line: t.Line, Args: []*Expr{e}}
		default:
			return e
		}
	}
}

func (p *parser) primary() *Expr {
	t := p.cur()
	switch t.K {
	case TInt:
		p.i++
		v, err := strconv.ParseInt(t.S, 0, 64)
		if err != nil {
			if len(t.S) > 1 && t.S[0] == '0' && t.S[1] != 'x' && t.S[1] != 'X' {
				v, err = strconv.ParseInt(t.S, 8, 64)
			}
			if err != nil {
				u, e2 := strconv.ParseUint(t.S, 0, 64)
				if e2 != nil {
					p.fail(t, "integer constant overflow")
				}
				v = int64(u)
			}
		}
		if v > 0xFFFFFFFF {
			p.fail(t, "integer constant overflow")
		}
		return &Expr{K: EInt, Line: t.Line, I: int64(int32(uint32(v)))}
	case TUint:
		p.i++
		v, err := strconv.ParseUint(t.S, 0, 64)
		if err != nil || v > 0xFFFFFFFF {
			p.fail(t, "integer constant overflow")
		}
		return &Expr{K: EUint, Line: t.Line, I: int64(v)}
	case TFloat:
		p.i++
		v, err := strconv.ParseFloat(t.S, 32)
		if err != nil && v == 0 {
			p.fail(t, "invalid float")
		}
		return &Expr{K: EFloat, Line: t.Line, F: v}
	case TPunct:
		if t.S == "(" {
			p.i++
			e := p.expr()
			p.expectP(")")
			return e
		}
	case TIdent:
		if t.S == "true" || t.S == "false" {
			p.i++
			v := int64(0)
			if t.S == "true" {
				v = 1
			}
			return &Expr{K: EBool, Line: t.Line, I: v}
		}
		if basicTypeNames[t.S] || p.structs[t.S] {
			// constructor
			ts := &TypeSpec{Name: t.S, Line: t.Line}
			p.i++
			if p.isP("[") {
				p.i++
				ts.Arr = true
				if !p.isP("]") {
					ts.ArrLen = p.expr()
				}
				p.expectP("]")
			}
			if !p.isP("(") {
				p.fail(p.cur(), "syntax error")
			}
			args := p.callArgs()
			return &Expr{K: ECall, S: t.S, Line: t.Line, Args: args, Type: ts}
		}
		if keywords[t.S] {
			p.fail(t, "syntax error")
		}
		p.i++
		if p.isP("(") {
			args := p.callArgs()
			return &Expr{K: ECall, S: t.S, Line: t.Line, Args: args}
		}
		return &Expr{K: EIdent, S: t.S, Line: t.Line}
	}
	p.fail(t, "syntax error")
	return nil
}

func (p *parser) callArgs() []*Expr {
	p.expectP("(")
	var args []*Expr
	if p.isK("void") && p.peek(1).S == ")" {
		p.i++
	}
	for !p.isP(")") {
		args = append(args, p.assignExpr())
		if !p.acceptP(",") {
			break
		}
	}
	p.expectP(")")
	return args
}
