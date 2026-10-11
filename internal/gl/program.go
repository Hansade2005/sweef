package gl

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/sweefbrowser/sweef/internal/gl/glsl"
)

// Shader is a GL shader object.
type Shader struct {
	ID       uint32
	Type     uint32
	Source   string
	Compiled *glsl.Shader
	Log      string
	OK       bool
	deleted  bool
	refs     int
}

type ukind uint8

const (
	ukFloat ukind = iota
	ukInt
	ukUint
	ukBool
	ukSampler
)

// UniformLeaf is one active uniform (a basic type or array of a basic type).
type UniformLeaf struct {
	Name   string // as reported by getActiveUniform (arrays carry "[0]")
	GLType uint32
	Size   int // array length (1 for non-arrays)
	Lanes  int // lanes per element
	Store  int
	Base   int // first location
	Kind   ukind
	Array  bool
	Rows   int
	Cols   int
	vsOff  int
	fsOff  int
	Block  int // index in program blocks, -1 for default block
	Offset int // block byte offset
	Stride int // array stride (bytes)
	MatStr int
	prec   string
}

type locEntry struct {
	leaf *UniformLeaf
	elem int
}

type copyRange struct{ store, reg, n int }

// ActiveAttrib describes an active vertex attribute.
type ActiveAttrib struct {
	Name   string
	GLType uint32
	Size   int
	Loc    int
	Off    int
	Kind   Kind
	Lanes  int
	Cols   int
}

// Kind of attribute shader variable.
type Kind uint8

const (
	AKFloat Kind = iota
	AKInt
	AKUint
)

type varyLink struct {
	vsOff, fsOff int
	n            int
	flat         bool
	name         string
}

// UBlock is a program-level uniform block.
type UBlock struct {
	Name     string
	Binding  int
	DataSize int
	VS, FS   int // index in shader blocks or -1
	Members  []int
	RefVS    bool
	RefFS    bool
}

type tfOut struct {
	name string
	off  int
	n    int
	typ  uint32
	size int
}

type poolPair struct{ vs, fs sync.Pool }

// Program is a GL program object.
type Program struct {
	ID            uint32
	Linked        bool
	Log           string
	Validated     bool
	deleted       bool
	attached      []*Shader
	vs, fs        *glsl.Shader
	bindAttr      map[string]int
	Attribs       []ActiveAttrib
	Leaves        []*UniformLeaf
	locs          []locEntry
	store         []uint32
	vsCopy        []copyRange
	fsCopy        []copyRange
	vary          []varyLink
	varyLanes     int
	Blocks        []*UBlock
	tfNames       []string
	tfMode        uint32
	tfOuts        []tfOut
	tfLanes       int
	pools         *poolPair
	maxAttrib     int
	depthOut      bool
	samplerLeaves []*UniformLeaf
}

func glTypeOf(t glsl.Type) uint32 {
	e := t.Elem()
	switch e.K {
	case glsl.KFloat:
		if e.M > 1 {
			names := map[[2]int]uint32{{2, 2}: GL_FLOAT_MAT2, {3, 3}: GL_FLOAT_MAT3, {4, 4}: GL_FLOAT_MAT4, {2, 3}: GL_FLOAT_MAT2x3,
				{2, 4}: GL_FLOAT_MAT2x4, {3, 2}: GL_FLOAT_MAT3x2, {3, 4}: GL_FLOAT_MAT3x4, {4, 2}: GL_FLOAT_MAT4x2, {4, 3}: GL_FLOAT_MAT4x3}
			return names[[2]int{e.M, e.N}]
		}
		return []uint32{GL_FLOAT, GL_FLOAT, GL_FLOAT_VEC2, GL_FLOAT_VEC3, GL_FLOAT_VEC4}[e.N]
	case glsl.KInt:
		return []uint32{GL_INT, GL_INT, GL_INT_VEC2, GL_INT_VEC3, GL_INT_VEC4}[e.N]
	case glsl.KUint:
		return []uint32{GL_UNSIGNED_INT, GL_UNSIGNED_INT, GL_UNSIGNED_INT_VEC2, GL_UNSIGNED_INT_VEC3, GL_UNSIGNED_INT_VEC4}[e.N]
	case glsl.KBool:
		return []uint32{GL_BOOL, GL_BOOL, GL_BOOL_VEC2, GL_BOOL_VEC3, GL_BOOL_VEC4}[e.N]
	case glsl.KSampler:
		return map[glsl.SamplerKind]uint32{glsl.Sampler2D: GL_SAMPLER_2D, glsl.SamplerCube: GL_SAMPLER_CUBE, glsl.Sampler3D: GL_SAMPLER_3D,
			glsl.Sampler2DArray: GL_SAMPLER_2D_ARRAY, glsl.Sampler2DShadow: GL_SAMPLER_2D_SHADOW, glsl.SamplerCubeShadow: GL_SAMPLER_CUBE_SHADOW,
			glsl.Sampler2DArrayShadow: GL_SAMPLER_2D_ARRAY_SHADOW, glsl.ISampler2D: GL_INT_SAMPLER_2D, glsl.ISampler3D: GL_INT_SAMPLER_3D,
			glsl.ISamplerCube: GL_INT_SAMPLER_CUBE, glsl.ISampler2DArray: GL_INT_SAMPLER_2D_ARRAY, glsl.USampler2D: GL_UNSIGNED_INT_SAMPLER_2D,
			glsl.USampler3D: GL_UNSIGNED_INT_SAMPLER_3D, glsl.USamplerCube: GL_UNSIGNED_INT_SAMPLER_CUBE,
			glsl.USampler2DArray: GL_UNSIGNED_INT_SAMPLER_2D_ARRAY}[e.SK]
	}
	return 0
}

func kindOf(t glsl.Type) ukind {
	switch t.K {
	case glsl.KInt:
		return ukInt
	case glsl.KUint:
		return ukUint
	case glsl.KBool:
		return ukBool
	case glsl.KSampler:
		return ukSampler
	}
	return ukFloat
}

// flatten appends the leaves of a uniform variable.
func flatten(out *[]*UniformLeaf, name string, t glsl.Type, reg int, isVS bool) {
	switch {
	case t.K == glsl.KStruct && t.Arr == 0:
		for _, f := range t.S.Fields {
			flatten(out, name+"."+f.Name, f.T, reg+f.Off, isVS)
		}
	case t.K == glsl.KStruct:
		es := t.Elem().Size()
		for i := 0; i < t.Arr; i++ {
			flatten(out, fmt.Sprintf("%s[%d]", name, i), t.Elem(), reg+i*es, isVS)
		}
	default:
		e := t.Elem()
		l := &UniformLeaf{Name: name, GLType: glTypeOf(t), Size: 1, Lanes: e.Size(), Kind: kindOf(e), Rows: e.N, Cols: e.M, vsOff: -1, fsOff: -1, Block: -1}
		if t.Arr > 0 {
			l.Size = t.Arr
			l.Array = true
			l.Name = name + "[0]"
		}
		if isVS {
			l.vsOff = reg
		} else {
			l.fsOff = reg
		}
		*out = append(*out, l)
	}
}

// ---- Context API: shaders -----------------------------------------------------

func (c *Context) CreateShader(typ uint32) uint32 {
	if typ != GL_VERTEX_SHADER && typ != GL_FRAGMENT_SHADER {
		c.setErr(GL_INVALID_ENUM)
		return 0
	}
	id := c.newID()
	c.shaders[id] = &Shader{ID: id, Type: typ}
	return id
}

func (c *Context) ShaderSource(id uint32, src string) {
	if s := c.shaders[id]; s != nil {
		s.Source = src
	} else {
		c.setErr(GL_INVALID_VALUE)
	}
}

func (c *Context) glslOptions() glsl.Options {
	ex := map[string]bool{}
	if c.Ext["OES_standard_derivatives"] {
		ex["GL_OES_standard_derivatives"] = true
	}
	if c.Ext["EXT_frag_depth"] {
		ex["GL_EXT_frag_depth"] = true
	}
	if c.Ext["EXT_shader_texture_lod"] {
		ex["GL_EXT_shader_texture_lod"] = true
	}
	if c.Ext["WEBGL_draw_buffers"] {
		ex["GL_EXT_draw_buffers"] = true
	}
	mdb := c.Lim.MaxDrawBuffers
	if c.Version == 1 && !c.Ext["WEBGL_draw_buffers"] {
		mdb = 1
	}
	return glsl.Options{WebGL2: c.Version == 2, Extensions: ex, MaxDrawBuffers: mdb}
}

func (c *Context) CompileShader(id uint32) {
	s := c.shaders[id]
	if s == nil {
		c.setErr(GL_INVALID_VALUE)
		return
	}
	stage := glsl.Vertex
	if s.Type == GL_FRAGMENT_SHADER {
		stage = glsl.Fragment
	}
	sh := glsl.Compile(stage, s.Source, c.glslOptions())
	s.Compiled, s.Log, s.OK = sh, sh.Log, sh.OK
}

func (c *Context) GetShaderParameter(id uint32, pname uint32) (int, bool) {
	s := c.shaders[id]
	if s == nil {
		c.setErr(GL_INVALID_VALUE)
		return 0, false
	}
	switch pname {
	case GL_SHADER_TYPE:
		return int(s.Type), true
	case GL_DELETE_STATUS:
		return b2i(s.deleted), true
	case GL_COMPILE_STATUS:
		return b2i(s.OK), true
	}
	c.setErr(GL_INVALID_ENUM)
	return 0, false
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (c *Context) GetShaderInfoLog(id uint32) string {
	if s := c.shaders[id]; s != nil {
		return nulTerm(s.Log)
	}
	c.setErr(GL_INVALID_VALUE)
	return ""
}

func (c *Context) GetShaderSource(id uint32) string {
	if s := c.shaders[id]; s != nil {
		return s.Source
	}
	return ""
}

func (c *Context) DeleteShader(id uint32) {
	if s := c.shaders[id]; s != nil {
		s.deleted = true
		if s.refs == 0 {
			delete(c.shaders, id)
		}
	}
}

func (c *Context) IsShader(id uint32) bool { return c.shaders[id] != nil }

// ---- programs --------------------------------------------------------------

func (c *Context) CreateProgram() uint32 {
	id := c.newID()
	c.progs[id] = &Program{ID: id, bindAttr: map[string]int{}}
	return id
}

func (c *Context) IsProgram(id uint32) bool { return c.progs[id] != nil }

func (c *Context) DeleteProgram(id uint32) {
	p := c.progs[id]
	if p == nil {
		return
	}
	p.deleted = true
	if c.program != p {
		c.releaseProgram(p)
	}
}

func (c *Context) releaseProgram(p *Program) {
	for _, s := range p.attached {
		s.refs--
		if s.deleted && s.refs <= 0 {
			delete(c.shaders, s.ID)
		}
	}
	p.attached = nil
	delete(c.progs, p.ID)
}

func (c *Context) AttachShader(pid, sid uint32) {
	p, s := c.progs[pid], c.shaders[sid]
	if p == nil || s == nil {
		c.setErr(GL_INVALID_VALUE)
		return
	}
	for _, a := range p.attached {
		if a.Type == s.Type {
			c.setErr(GL_INVALID_OPERATION)
			return
		}
	}
	p.attached = append(p.attached, s)
	s.refs++
}

func (c *Context) DetachShader(pid, sid uint32) {
	p, s := c.progs[pid], c.shaders[sid]
	if p == nil || s == nil {
		c.setErr(GL_INVALID_VALUE)
		return
	}
	for i, a := range p.attached {
		if a == s {
			p.attached = append(p.attached[:i], p.attached[i+1:]...)
			s.refs--
			if s.deleted && s.refs <= 0 {
				delete(c.shaders, s.ID)
			}
			return
		}
	}
	c.setErr(GL_INVALID_OPERATION)
}

func (c *Context) GetAttachedShaders(pid uint32) []uint32 {
	p := c.progs[pid]
	if p == nil {
		c.setErr(GL_INVALID_VALUE)
		return nil
	}
	var out []uint32
	for _, s := range p.attached {
		out = append(out, s.ID)
	}
	return out
}

func (c *Context) BindAttribLocation(pid uint32, loc int, name string) {
	p := c.progs[pid]
	if p == nil {
		c.setErr(GL_INVALID_VALUE)
		return
	}
	if loc < 0 || loc >= c.Lim.MaxVertexAttribs {
		c.setErr(GL_INVALID_VALUE)
		return
	}
	if strings.HasPrefix(name, "gl_") {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	p.bindAttr[name] = loc
}

func (c *Context) LinkProgram(pid uint32) {
	p := c.progs[pid]
	if p == nil {
		c.setErr(GL_INVALID_VALUE)
		return
	}
	prevStore, prevLeaves := p.store, p.Leaves
	_ = prevStore
	_ = prevLeaves
	p.Linked = false
	p.Log = ""
	var vs, fs *Shader
	for _, s := range p.attached {
		if s.Type == GL_VERTEX_SHADER {
			vs = s
		} else {
			fs = s
		}
	}
	fail := func(f string, a ...any) {
		p.Log = "ERROR: " + fmt.Sprintf(f, a...) + "\n"
		p.Linked = false
	}
	if vs == nil || fs == nil {
		fail("Must have a vertex and a fragment shader attached")
		return
	}
	if !vs.OK || !fs.OK {
		fail("Compile the shader before linking")
		return
	}
	if vs.Compiled.Version != fs.Compiled.Version {
		fail("Versions of linked shaders have to match")
		return
	}
	v, f := vs.Compiled, fs.Compiled
	np := &Program{ID: p.ID, attached: p.attached, bindAttr: p.bindAttr, tfNames: p.tfNames, tfMode: p.tfMode, vs: v, fs: f, deleted: p.deleted}
	// attributes
	used := map[int]bool{}
	for name, loc := range np.bindAttr {
		_ = name
		_ = loc
	}
	type pend struct {
		in IOVarRef
	}
	var needAuto []glsl.IOVar
	for _, in := range v.Inputs {
		if !in.Used {
			continue
		}
		cols := max(1, in.T.M)
		loc := -1
		if in.Loc >= 0 {
			loc = in.Loc
		} else if l, ok := np.bindAttr[in.Name]; ok {
			loc = l
		}
		if loc >= 0 {
			for k := 0; k < cols*max(1, in.T.Arr); k++ {
				if used[loc+k] {
					fail("Attribute '%s' aliases another attribute at location %d", in.Name, loc+k)
					return
				}
				used[loc+k] = true
			}
			np.addAttrib(in, loc)
		} else {
			needAuto = append(needAuto, in)
		}
	}
	for _, in := range needAuto {
		cols := max(1, in.T.M) * max(1, in.T.Arr)
		loc := -1
		for l := 0; l+cols <= c.Lim.MaxVertexAttribs; l++ {
			ok := true
			for k := 0; k < cols; k++ {
				if used[l+k] {
					ok = false
				}
			}
			if ok {
				loc = l
				break
			}
		}
		if loc < 0 {
			fail("Too many vertex attributes")
			return
		}
		for k := 0; k < cols; k++ {
			used[loc+k] = true
		}
		np.addAttrib(in, loc)
	}
	// varyings
	lanes := 0
	for _, in := range f.Inputs {
		if !in.Used {
			continue
		}
		var out *glsl.IOVar
		for i := range v.Outputs {
			if v.Outputs[i].Name == in.Name {
				out = &v.Outputs[i]
			}
		}
		if out == nil {
			fail("Varying (named %s) is not declared in vertex shader.", in.Name)
			return
		}
		if out.T != in.T {
			fail("Types for varying '%s' differ between vertex and fragment shaders", in.Name)
			return
		}
		np.vary = append(np.vary, varyLink{vsOff: out.Off, fsOff: in.Off, n: in.T.Size(), flat: in.Flat || out.Flat, name: in.Name})
		lanes += in.T.Size()
	}
	if (lanes+3)/4 > c.Lim.MaxVaryingVecs {
		fail("Too many varyings")
		return
	}
	np.varyLanes = lanes
	// uniforms
	byName := map[string]*UniformLeaf{}
	collect := func(sh *glsl.Shader, isVS bool) bool {
		for _, u := range sh.Uniforms {
			if !u.Used || u.Block >= 0 {
				continue
			}
			var leaves []*UniformLeaf
			flatten(&leaves, u.Name, u.T, u.Off, isVS)
			for _, l := range leaves {
				if o, ok := byName[l.Name]; ok {
					if o.GLType != l.GLType || o.Size != l.Size {
						fail("Uniform '%s' differs between vertex and fragment shaders", l.Name)
						return false
					}
					if o.prec != "" && u.Prec != "" && o.prec != u.Prec && o.Kind != ukSampler {
						fail("Precisions of uniform '%s' differ between VERTEX and FRAGMENT shaders", u.Name)
						return false
					}
					if isVS {
						o.vsOff = l.vsOff
					} else {
						o.fsOff = l.fsOff
					}
					continue
				}
				l.prec = u.Prec
				byName[l.Name] = l
				np.Leaves = append(np.Leaves, l)
			}
		}
		return true
	}
	if !collect(v, true) || !collect(f, false) {
		return
	}
	// blocks
	if v.Version == 300 {
		bmap := map[string]*UBlock{}
		for si, sh := range []*glsl.Shader{v, f} {
			for bi, b := range sh.Blocks {
				ub := bmap[b.Name]
				if ub == nil {
					ub = &UBlock{Name: b.Name, VS: -1, FS: -1, DataSize: b.ByteSize}
					bmap[b.Name] = ub
					np.Blocks = append(np.Blocks, ub)
				} else if ub.DataSize != b.ByteSize {
					fail("Uniform block '%s' differs between shaders", b.Name)
					return
				}
				if si == 0 {
					ub.VS = bi
					ub.RefVS = b.Used
				} else {
					ub.FS = bi
					ub.RefFS = b.Used
				}
			}
		}
		for bi, ub := range np.Blocks {
			// active only if referenced
			sh, idx := v, ub.VS
			if idx < 0 {
				sh, idx = f, ub.FS
			}
			for mi, m := range sh.Blocks[idx].Members {
				var leaves []*UniformLeaf
				pre := ""
				if sh.Blocks[idx].Inst != "" {
					pre = ub.Name + "."
				}
				flatten(&leaves, pre+m.Name, m.T, m.Off, true)
				_ = mi
				for _, l := range leaves {
					l.Block = bi
					l.Offset = m.Byte
					l.Stride = m.Stride

					np.Leaves = append(np.Leaves, l)
					ub.Members = append(ub.Members, len(np.Leaves)-1)
				}
			}
		}
	}
	// assign store and locations
	loc := 0
	storeN := 0
	for _, l := range np.Leaves {
		if l.Block >= 0 {
			continue
		}
		l.Store = storeN
		storeN += l.Lanes * l.Size
		l.Base = loc
		for e := 0; e < l.Size; e++ {
			np.locs = append(np.locs, locEntry{l, e})
		}
		loc += l.Size
		if l.vsOff >= 0 {
			np.vsCopy = append(np.vsCopy, copyRange{l.Store, l.vsOff, l.Lanes * l.Size})
		}
		if l.fsOff >= 0 {
			np.fsCopy = append(np.fsCopy, copyRange{l.Store, l.fsOff, l.Lanes * l.Size})
		}
		if l.Kind == ukSampler {
			np.samplerLeaves = append(np.samplerLeaves, l)
		}
	}
	np.store = make([]uint32, storeN)
	// transform feedback varyings
	if len(np.tfNames) > 0 {
		for _, name := range np.tfNames {
			base := name
			var found *glsl.IOVar
			for i := range v.Outputs {
				if v.Outputs[i].Name == base {
					found = &v.Outputs[i]
				}
			}
			if found == nil {
				fail("Transform feedback varying '%s' does not exist", name)
				return
			}
			np.tfOuts = append(np.tfOuts, tfOut{name: name, off: found.Off, n: found.T.Size(), typ: glTypeOf(found.T), size: max(1, found.T.Arr)})
			np.tfLanes += found.T.Size()
		}
		if np.tfMode == GL_SEPARATE_ATTRIBS && len(np.tfOuts) > 4 {
			fail("Too many transform feedback separate attributes")
			return
		}
	}
	np.pools = &poolPair{}
	np.Linked = true
	np.Log = ""
	*p = *np
}

type IOVarRef struct{}

func (p *Program) addAttrib(in glsl.IOVar, loc int) {
	k := AKFloat
	switch in.T.K {
	case glsl.KInt:
		k = AKInt
	case glsl.KUint:
		k = AKUint
	}
	p.Attribs = append(p.Attribs, ActiveAttrib{Name: in.Name, GLType: glTypeOf(in.T), Size: max(1, in.T.Arr), Loc: loc, Off: in.Off, Kind: k, Lanes: in.T.Size(), Cols: max(1, in.T.M)})
	sort.Slice(p.Attribs, func(i, j int) bool { return p.Attribs[i].Loc < p.Attribs[j].Loc })
}

func (c *Context) GetProgramParameter(pid uint32, pname uint32) (int, bool) {
	p := c.progs[pid]
	if p == nil {
		c.setErr(GL_INVALID_VALUE)
		return 0, false
	}
	switch pname {
	case GL_DELETE_STATUS:
		return b2i(p.deleted), true
	case GL_LINK_STATUS:
		return b2i(p.Linked), true
	case GL_VALIDATE_STATUS:
		return b2i(p.Validated), true
	case GL_ATTACHED_SHADERS:
		return len(p.attached), true
	case GL_ACTIVE_ATTRIBUTES:
		return len(p.Attribs), true
	case GL_ACTIVE_UNIFORMS:
		n := 0
		for _, l := range p.Leaves {
			_ = l
			n++
		}
		return n, true
	}
	if c.Version == 2 {
		switch pname {
		case GL_ACTIVE_UNIFORM_BLOCKS:
			return len(p.Blocks), true
		case GL_TRANSFORM_FEEDBACK_BUFFER_MODE:
			return int(p.tfMode), true
		case GL_TRANSFORM_FEEDBACK_VARYINGS:
			return len(p.tfNames), true
		}
	}
	c.setErr(GL_INVALID_ENUM)
	return 0, false
}

func nulTerm(s string) string {
	if s == "" {
		return s
	}
	return s + "\x00"
}

func (c *Context) GetProgramInfoLog(pid uint32) string {
	if p := c.progs[pid]; p != nil {
		return nulTerm(p.Log)
	}
	c.setErr(GL_INVALID_VALUE)
	return ""
}

func (c *Context) ValidateProgram(pid uint32) {
	p := c.progs[pid]
	if p == nil {
		c.setErr(GL_INVALID_VALUE)
		return
	}
	p.Validated = p.Linked
}

func (c *Context) UseProgram(pid uint32) {
	if pid == 0 {
		c.setProgram(nil)
		return
	}
	p := c.progs[pid]
	if p == nil {
		c.setErr(GL_INVALID_VALUE)
		return
	}
	if !p.Linked {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	c.setProgram(p)
}

func (c *Context) setProgram(p *Program) {
	old := c.program
	c.program = p
	if old != nil && old.deleted && old != p {
		c.releaseProgram(old)
	}
}

func (c *Context) GetAttribLocation(pid uint32, name string) int {
	p := c.progs[pid]
	if p == nil {
		c.setErr(GL_INVALID_VALUE)
		return -1
	}
	if !p.Linked {
		c.setErr(GL_INVALID_OPERATION)
		return -1
	}
	if strings.HasPrefix(name, "gl_") {
		return -1
	}
	for _, a := range p.Attribs {
		if a.Name == name {
			return a.Loc
		}
	}
	return -1
}

// GetActiveAttrib returns name, type, size.
func (c *Context) GetActiveAttrib(pid uint32, idx int) (string, uint32, int, bool) {
	p := c.progs[pid]
	if p == nil {
		c.setErr(GL_INVALID_VALUE)
		return "", 0, 0, false
	}
	if idx < 0 || idx >= len(p.Attribs) {
		c.setErr(GL_INVALID_VALUE)
		return "", 0, 0, false
	}
	a := p.Attribs[idx]
	return a.Name, a.GLType, a.Size, true
}

func (c *Context) GetActiveUniform(pid uint32, idx int) (string, uint32, int, bool) {
	p := c.progs[pid]
	if p == nil {
		c.setErr(GL_INVALID_VALUE)
		return "", 0, 0, false
	}
	if idx < 0 || idx >= len(p.Leaves) {
		c.setErr(GL_INVALID_VALUE)
		return "", 0, 0, false
	}
	l := p.Leaves[idx]
	name := l.Name
	return name, l.GLType, l.Size, true
}

// GetUniformLocation resolves "name", "name[i]", "s.m", "s[i].m[j]".
func (c *Context) GetUniformLocation(pid uint32, name string) int {
	p := c.progs[pid]
	if p == nil {
		c.setErr(GL_INVALID_VALUE)
		return -1
	}
	if !p.Linked {
		c.setErr(GL_INVALID_OPERATION)
		return -1
	}
	for _, l := range p.Leaves {
		if l.Block >= 0 {
			continue
		}
		if l.Name == name {
			return l.Base
		}
		if l.Array {
			base := strings.TrimSuffix(l.Name, "[0]")
			if name == base {
				return l.Base
			}
			if strings.HasPrefix(name, base+"[") && strings.HasSuffix(name, "]") {
				var i int
				if _, err := fmt.Sscanf(name[len(base):], "[%d]", &i); err == nil && i >= 0 && i < l.Size && fmt.Sprintf("%s[%d]", base, i) == name {
					return l.Base + i
				}
			}
		}
	}
	return -1
}

// ---- uniform setters --------------------------------------------------------

func (c *Context) leafFor(loc int) (*UniformLeaf, int, bool) {
	if loc == -1 {
		return nil, 0, false // silently ignored
	}
	p := c.program
	if p == nil {
		c.setErr(GL_INVALID_OPERATION)
		return nil, 0, false
	}
	if loc < 0 || loc >= len(p.locs) {
		c.setErr(GL_INVALID_OPERATION)
		return nil, 0, false
	}
	e := p.locs[loc]
	return e.leaf, e.elem, true
}

// UniformF sets float / vector uniforms from values (count groups of n).
func (c *Context) UniformF(loc int, n int, vals []float32) {
	l, el, ok := c.leafFor(loc)
	if !ok {
		return
	}
	if len(vals)%n != 0 || len(vals) == 0 {
		c.setErr(GL_INVALID_VALUE)
		return
	}
	cnt := len(vals) / n
	if l.Kind == ukInt || l.Kind == ukUint || l.Kind == ukSampler || (l.Rows != n || l.Cols > 1) {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	if l.Kind == ukBool {
		// bool via float
	}
	if cnt > 1 && !l.Array {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	cnt = min(cnt, l.Size-el)
	p := c.program
	for i := 0; i < cnt*n; i++ {
		v := vals[i]
		if l.Kind == ukBool {
			p.store[l.Store+el*l.Lanes+i] = b2u32(v != 0)
		} else {
			p.store[l.Store+el*l.Lanes+i] = f32bits(v)
		}
	}
}

func b2u32(b bool) uint32 {
	if b {
		return 1
	}
	return 0
}

// UniformI sets int/bool/sampler uniforms. unsigned selects uniformNui.
func (c *Context) UniformI(loc int, n int, vals []int32, unsigned bool) {
	l, el, ok := c.leafFor(loc)
	if !ok {
		return
	}
	if len(vals)%n != 0 || len(vals) == 0 {
		c.setErr(GL_INVALID_VALUE)
		return
	}
	cnt := len(vals) / n
	okk := false
	switch l.Kind {
	case ukInt, ukBool:
		okk = !unsigned
	case ukUint:
		okk = unsigned
	case ukSampler:
		okk = !unsigned && n == 1
	}
	if l.Kind == ukBool && unsigned {
		okk = true
	}
	if !okk || l.Rows != n || l.Cols > 1 {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	if cnt > 1 && !l.Array {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	if l.Kind == ukSampler {
		for _, v := range vals {
			if v < 0 || int(v) >= c.Lim.MaxCombinedTexUnits {
				c.setErr(GL_INVALID_VALUE)
				return
			}
		}
	}
	cnt = min(cnt, l.Size-el)
	p := c.program
	for i := 0; i < cnt*n; i++ {
		v := vals[i]
		if l.Kind == ukBool {
			p.store[l.Store+el*l.Lanes+i] = b2u32(v != 0)
		} else {
			p.store[l.Store+el*l.Lanes+i] = uint32(v)
		}
	}
}

// UniformMatrix sets matrix uniforms: cols x rows, transpose must be false.
func (c *Context) UniformMatrix(loc int, cols, rows int, transpose bool, vals []float32) {
	l, el, ok := c.leafFor(loc)
	if !ok {
		return
	}
	n := cols * rows
	if len(vals)%n != 0 || len(vals) == 0 {
		c.setErr(GL_INVALID_VALUE)
		return
	}
	if transpose && c.Version == 1 {
		c.setErr(GL_INVALID_VALUE)
		return
	}
	if l.Kind != ukFloat || l.Cols != cols || l.Rows != rows {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	cnt := len(vals) / n
	if cnt > 1 && !l.Array {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	cnt = min(cnt, l.Size-el)
	p := c.program
	for k := 0; k < cnt; k++ {
		dst := p.store[l.Store+(el+k)*l.Lanes:]
		src := vals[k*n : (k+1)*n]
		if transpose {
			for cc := 0; cc < cols; cc++ {
				for r := 0; r < rows; r++ {
					dst[cc*rows+r] = f32bits(src[r*cols+cc])
				}
			}
		} else {
			for i := 0; i < n; i++ {
				dst[i] = f32bits(src[i])
			}
		}
	}
}

// GetUniform returns the uniform's current lanes as raw words with its type info.
func (c *Context) GetUniform(pid uint32, loc int) (vals []uint32, glType uint32, ok bool) {
	p := c.progs[pid]
	if p == nil {
		c.setErr(GL_INVALID_VALUE)
		return
	}
	if !p.Linked {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	if loc < 0 || loc >= len(p.locs) {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	e := p.locs[loc]
	l := e.leaf
	vals = append([]uint32(nil), p.store[l.Store+e.elem*l.Lanes:l.Store+(e.elem+1)*l.Lanes]...)
	return vals, l.GLType, true
}

// UniformInfo describes how to interpret GetUniform data.
func UniformKind(glType uint32) string {
	switch glType {
	case GL_INT, GL_INT_VEC2, GL_INT_VEC3, GL_INT_VEC4, GL_SAMPLER_2D, GL_SAMPLER_CUBE, GL_SAMPLER_3D, GL_SAMPLER_2D_ARRAY, GL_SAMPLER_2D_SHADOW,
		GL_SAMPLER_CUBE_SHADOW, GL_SAMPLER_2D_ARRAY_SHADOW, GL_INT_SAMPLER_2D, GL_INT_SAMPLER_3D, GL_INT_SAMPLER_CUBE, GL_INT_SAMPLER_2D_ARRAY,
		GL_UNSIGNED_INT_SAMPLER_2D, GL_UNSIGNED_INT_SAMPLER_3D, GL_UNSIGNED_INT_SAMPLER_CUBE, GL_UNSIGNED_INT_SAMPLER_2D_ARRAY:
		return "int"
	case GL_UNSIGNED_INT, GL_UNSIGNED_INT_VEC2, GL_UNSIGNED_INT_VEC3, GL_UNSIGNED_INT_VEC4:
		return "uint"
	case GL_BOOL, GL_BOOL_VEC2, GL_BOOL_VEC3, GL_BOOL_VEC4:
		return "bool"
	}
	return "float"
}
