package glsl

import "math"

// Stage is a shader stage.
type Stage uint8

const (
	Vertex Stage = iota
	Fragment
)

func (s Stage) String() string {
	if s == Vertex {
		return "vertex"
	}
	return "fragment"
}

type ctl uint8

const (
	ctlNone ctl = iota
	ctlBreak
	ctlContinue
	ctlReturn
	ctlDiscard
	ctlAbort
)

type op func(f *Frame)
type stmtFn func(f *Frame) ctl

// SampleMode selects how the level of detail is chosen.
type SampleMode uint8

const (
	SampleImplicit SampleMode = iota // derive from DX/DY (or level 0 when zero)
	SampleLod                        // explicit Lod
	SampleGrad                       // explicit DX/DY
)

// SampleReq is a texture lookup.
type SampleReq struct {
	Coord  [4]float32 // s,t,r and array layer / cube face already in Coord[2] or [3]
	Ref    float32    // shadow comparison value
	DX, DY [3]float32 // derivatives of Coord (normalized texture coordinates)
	Lod    float32
	Bias   float32
	Mode   SampleMode
	Offset [3]int32
}

// Sampler is the texture side of the executor.
type Sampler interface {
	// Sample returns the filtered RGBA (shadow samplers return the
	// comparison result in all channels).
	Sample(r *SampleReq) [4]float32
	// SampleRaw performs a lookup in an integer texture; the result holds
	// raw int32/uint32 bits.
	SampleRaw(r *SampleReq) [4]uint32
	// Texel fetches without filtering. For float textures the result holds
	// float32 bits.
	Texel(x, y, z, lod int) [4]uint32
	// Size returns width, height, depth (or layers) of a level.
	Size(lod int) [3]int
}

// Frame is the execution state of one shader invocation.
type Frame struct {
	R         []uint32
	Tex       []Sampler
	Quad      func(in []float32) (dx, dy []float32)
	Discarded bool
	budget    int32
	sh        *Shader
	scratch   [32]float32
}

func fl(u uint32) float32 { return math.Float32frombits(u) }
func bf(f float32) uint32 { return math.Float32bits(f) }
func b2u(b bool) uint32 {
	if b {
		return 1
	}
	return 0
}

// IOVar describes a shader input or output variable.
type IOVar struct {
	Name      string
	T         Type
	Off       int
	Loc       int // layout(location) or -1
	Flat      bool
	Used      bool
	Invariant bool
}

// Uniform describes a uniform variable.
type Uniform struct {
	Name  string
	T     Type
	Off   int
	Block int // index into Blocks, -1 for default block
	Used  bool
	Prec  string
}

// BlockMember is one member of a uniform block with its std140 placement.
type BlockMember struct {
	Name   string
	T      Type
	Off    int // register offset
	Byte   int // byte offset in the std140 buffer
	Stride int // array stride in bytes (arrays), else 0
	Mat    int // matrix column stride in bytes
}

// Block describes a uniform block.
type Block struct {
	Name     string
	Inst     string
	Binding  int
	Members  []BlockMember
	ByteSize int
	Used     bool
	ArrLen   int
}

// Shader is a compiled shader.
type Shader struct {
	Stage   Stage
	Version int
	OK      bool
	Log     string
	Exts    map[string]string

	Inputs   []IOVar
	Outputs  []IOVar
	Uniforms []Uniform
	Blocks   []*Block

	// Built-in variable registers (-1 when absent).
	PositionOff, PointSizeOff, VertexIDOff, InstanceIDOff int
	FragCoordOff, FrontFacingOff, PointCoordOff           int
	FragColorOff, FragDataOff, FragDepthOff               int
	DepthRangeOff                                         int
	WritesFragDepth                                       bool
	WritesPointSize                                       bool
	WritesFragColor, WritesFragData                       bool
	UsesDerivatives                                       bool // dFdx/dFdy/fwidth
	UsesImplicitLod                                       bool // texture lookup without explicit lod
	UsesDiscard                                           bool
	UsesFragCoord                                         bool

	regs       int
	pool       []uint32
	main       stmtFn
	globalInit []op
	source     string
	ast        []*Top
}

// Instance executes a shader. Each goroutine needs its own Instance.
type Instance struct {
	Frame
}

// NewInstance creates an execution instance with its own register file.
func (sh *Shader) NewInstance() *Instance {
	in := &Instance{}
	in.R = make([]uint32, sh.regs)
	copy(in.R, sh.pool)
	in.sh = sh
	return in
}

// Shader returns the compiled shader.
func (in *Instance) Shader() *Shader { return in.sh }

// Run executes main (after global initializers). It returns true when the
// invocation was discarded or aborted.
func (in *Instance) Run() (discarded bool) {
	f := &in.Frame
	f.Discarded = false
	f.budget = 1 << 23
	for _, o := range in.sh.globalInit {
		o(f)
	}
	c := in.sh.main(f)
	return c == ctlDiscard || f.Discarded || c == ctlAbort
}
