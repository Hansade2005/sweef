// Package gl is a software implementation of OpenGL ES 2.0/3.0 semantics as
// exposed by WebGL 1 and 2: GL state machine, texture sampling, a tile based
// parallel rasterizer and a GLSL ES interpreter (package glsl).
package gl

import (
	"math"
	"image"
	"sync"

	"github.com/sweefbrowser/sweef/internal/gl/glsl"
)

// Attrs are the WebGLContextAttributes.
type Attrs struct {
	Alpha                 bool
	Depth                 bool
	Stencil               bool
	Antialias             bool
	PremultipliedAlpha    bool
	PreserveDrawingBuffer bool
}

// Buffer is a GL buffer object.
type Buffer struct {
	ID      uint32
	Data    []byte
	Usage   uint32
	Target  uint32 // first bound target (WebGL1 type rules)
	deleted bool
}

// Attrib is one vertex attribute array state.
type Attrib struct {
	Enabled    bool
	Size       int
	Type       uint32
	Normalized bool
	Integer    bool
	Stride     int
	Offset     int
	Buf        *Buffer
	Divisor    int
}

// VAO is a vertex array object.
type VAO struct {
	ID      uint32
	Attribs [16]Attrib
	Element *Buffer
}

// Renderbuffer is a GL renderbuffer.
type Renderbuffer struct {
	ID         uint32
	Img        *Image
	Internal   uint32
	W, H       int
	Samples    int
	deleted    bool
}

// Attach is a framebuffer attachment.
type Attach struct {
	Kind   uint8 // 0 none, 1 texture, 2 renderbuffer
	Tex    *Texture
	Face   int
	Level  int
	Layer  int
	RB     *Renderbuffer
}

// Framebuffer is a GL framebuffer object.
type Framebuffer struct {
	ID          uint32
	Color       [8]Attach
	Depth       Attach
	Stencil     Attach
	DrawBuffers [8]uint32
	ReadBuffer  uint32
	isDefault   bool
	deleted     bool
}

type stencilState struct {
	Func  uint32
	Ref   int32
	Mask  uint32
	Fail  uint32
	ZFail uint32
	ZPass uint32
	WMask uint32
}

type texUnit struct {
	tex     [4]*Texture // 2D, cube, 3D, 2DArray
	sampler *SamplerObj
}

// SamplerObj is a WebGL2 sampler object.
type SamplerObj struct {
	ID      uint32
	P       SamplerParams
	deleted bool
}

type bufBinding struct {
	Buf  *Buffer
	Off  int
	Size int
}

// Query is an occlusion / any-samples-passed query.
type Query struct {
	ID      uint32
	Target  uint32
	Result  uint64
	Active  bool
	Ready   bool
	Created bool
	deleted bool
}

// Sync is a fence object (always signalled immediately).
type Sync struct {
	ID uint32
}

// TransformFeedback is a transform feedback object.
type TransformFeedback struct {
	ID      uint32
	Bufs    [4]bufBinding
	Active  bool
	Paused  bool
	Mode    uint32
	Written int
	deleted bool
}

// Context is a WebGL rendering context.
type Context struct {
	mu      sync.Mutex
	Version int
	Attrs   Attrs
	W, H    int

	errCode uint32
	lost    bool

	nextID  uint32
	buffers map[uint32]*Buffer
	textures map[uint32]*Texture
	rbs     map[uint32]*Renderbuffer
	fbs     map[uint32]*Framebuffer
	vaos    map[uint32]*VAO
	progs   map[uint32]*Program
	shaders map[uint32]*Shader
	queries map[uint32]*Query
	samplers map[uint32]*SamplerObj
	tfs     map[uint32]*TransformFeedback
	syncs   map[uint32]*Sync

	// bindings
	arrayBuf   *Buffer
	copyRead   *Buffer
	copyWrite  *Buffer
	packBuf    *Buffer
	unpackBuf  *Buffer
	ubo        [24]bufBinding
	uboGeneric *Buffer
	tfBuf      *Buffer
	vao        *VAO
	defVAO     *VAO
	units      [32]texUnit
	activeUnit int
	readFB     *Framebuffer
	drawFB     *Framebuffer
	defFB      *Framebuffer
	rb         *Renderbuffer
	program    *Program
	tf         *TransformFeedback
	defTF      *TransformFeedback
	tfBufGen   [4]bufBinding
	activeQ    map[uint32]*Query
	generic    [16][4]uint32
	genericTyp [16]uint32 // GL_FLOAT, GL_INT, GL_UNSIGNED_INT

	// state
	viewport     [4]int
	scissor      [4]int
	depthRange   [2]float32
	clearColor   [4]float32
	clearDepth   float32
	clearStencil int32
	colorMask    [4]bool
	depthMask    bool
	depthFunc    uint32
	stencilF     stencilState
	stencilB     stencilState
	blendColor   [4]float32
	blendEqRGB   uint32
	blendEqA     uint32
	blendSrcRGB  uint32
	blendDstRGB  uint32
	blendSrcA    uint32
	blendDstA    uint32
	cullMode     uint32
	frontFace    uint32
	lineWidth    float32
	polyFactor   float32
	polyUnits    float32
	sampleCovVal float32
	sampleCovInv bool
	enBlend, enCull, enDepth, enStencil, enScissor, enPoly, enDither, enRDiscard, enA2C, enSampleCov bool
	hints        [2]uint32
	pixStore     struct {
		FlipY, Premul bool
		Colorspace    uint32
		Unpack, Pack  Pack
	}

	// extensions enabled by the page
	Ext map[string]bool

	// presentation
	dirty     bool
	presented *image.RGBA
	bmMu      sync.Mutex

	// caches
	viewCache map[*Texture]*texView
	occCount  uint64 // samples passed counter for active occlusion queries
	tfWritePos [4]int
	defColor   *Image
	defDepth   *Image

	// limits
	Lim Limits

	// info for debug
	Stats struct{ Draws, Tris int }
}

// Limits holds implementation limits reported through getParameter.
type Limits struct {
	MaxTextureSize       int
	MaxCubeMapSize       int
	Max3DTextureSize     int
	MaxArrayLayers       int
	MaxRenderbufferSize  int
	MaxViewportDims      [2]int
	MaxVertexAttribs     int
	MaxVertexUniformVecs int
	MaxFragUniformVecs   int
	MaxVaryingVecs       int
	MaxTextureUnits      int
	MaxVertexTexUnits    int
	MaxCombinedTexUnits  int
	MaxDrawBuffers       int
	MaxColorAttachments  int
	MaxSamples           int
	MaxUBOBindings       int
	MaxUBOSize           int
}

// DefaultLimits returns SwiftShader-like limits.
func DefaultLimits() Limits {
	return Limits{MaxTextureSize: 8192, MaxCubeMapSize: 8192, Max3DTextureSize: 2048, MaxArrayLayers: 2048,
		MaxRenderbufferSize: 8192, MaxViewportDims: [2]int{8192, 8192}, MaxVertexAttribs: 16, MaxVertexUniformVecs: 256,
		MaxFragUniformVecs: 224, MaxVaryingVecs: 15, MaxTextureUnits: 16, MaxVertexTexUnits: 16, MaxCombinedTexUnits: 32,
		MaxDrawBuffers: 8, MaxColorAttachments: 8, MaxSamples: 4, MaxUBOBindings: 24, MaxUBOSize: 65536}
}

// New creates a context with a drawing buffer of w x h pixels.
func New(version int, attrs Attrs, w, h int) *Context {
	c := &Context{Version: version, Attrs: attrs, nextID: 1, Ext: map[string]bool{}, Lim: DefaultLimits(),
		buffers: map[uint32]*Buffer{}, textures: map[uint32]*Texture{}, rbs: map[uint32]*Renderbuffer{},
		fbs: map[uint32]*Framebuffer{}, vaos: map[uint32]*VAO{}, progs: map[uint32]*Program{}, shaders: map[uint32]*Shader{},
		queries: map[uint32]*Query{}, samplers: map[uint32]*SamplerObj{}, tfs: map[uint32]*TransformFeedback{}, syncs: map[uint32]*Sync{},
		activeQ: map[uint32]*Query{}, viewCache: map[*Texture]*texView{}}
	if version == 1 {
		c.Lim.MaxDrawBuffers = 1
		c.Lim.MaxColorAttachments = 1
	}
	c.defVAO = &VAO{}
	c.vao = c.defVAO
	c.defTF = &TransformFeedback{}
	c.tf = c.defTF
	c.defFB = &Framebuffer{isDefault: true, ReadBuffer: GL_BACK}
	c.defFB.DrawBuffers[0] = GL_BACK
	c.readFB, c.drawFB = c.defFB, c.defFB
	c.resetState()
	c.Resize(w, h)
	return c
}

func (c *Context) resetState() {
	c.colorMask = [4]bool{true, true, true, true}
	c.depthMask = true
	c.depthFunc = GL_LESS
	c.depthRange = [2]float32{0, 1}
	c.clearDepth = 1
	st := stencilState{Func: GL_ALWAYS, Mask: 0xFFFFFFFF, Fail: GL_KEEP, ZFail: GL_KEEP, ZPass: GL_KEEP, WMask: 0xFFFFFFFF}
	c.stencilF, c.stencilB = st, st
	c.blendEqRGB, c.blendEqA = GL_FUNC_ADD, GL_FUNC_ADD
	c.blendSrcRGB, c.blendSrcA = GL_ONE, GL_ONE
	c.blendDstRGB, c.blendDstA = GL_ZERO, GL_ZERO
	c.cullMode = GL_BACK
	c.frontFace = GL_CCW
	c.lineWidth = 1
	c.enDither = true
	c.pixStore.Unpack = Pack{Alignment: 4}
	c.pixStore.Pack = Pack{Alignment: 4}
	c.pixStore.Colorspace = GL_BROWSER_DEFAULT_WEBGL
	for i := range c.genericTyp {
		c.genericTyp[i] = GL_FLOAT
		c.generic[i] = [4]uint32{0, 0, 0, floatBits1}
	}
	c.hints = [2]uint32{GL_DONT_CARE, GL_DONT_CARE}
}

const floatBits1 = 0x3f800000

// ---- errors ------------------------------------------------------------------

func (c *Context) setErr(e uint32) {
	if c.errCode == GL_NO_ERROR {
		c.errCode = e
	}
}

// GetError returns and clears the error flag.
func (c *Context) GetError() uint32 {
	if c.lost {
		return GL_NO_ERROR
	}
	e := c.errCode
	c.errCode = GL_NO_ERROR
	return e
}

// SetError lets the host raise an error (for example INVALID_OPERATION).
func (c *Context) SetError(e uint32) { c.setErr(e) }

// Lost reports context loss.
func (c *Context) Lost() bool { return c.lost }

// LoseContext marks the context lost.
func (c *Context) LoseContext() {
	c.lost = true
}

// RestoreContext re-initialises all state.
func (c *Context) RestoreContext() {
	w, h := c.W, c.H
	*c = *New(c.Version, c.Attrs, w, h)
}

func (c *Context) newID() uint32 {
	id := c.nextID
	c.nextID++
	return id
}

// Compile-time use of glsl in this file.
var _ = glsl.Vertex

func f32bits(f float32) uint32 { return math.Float32bits(f) }
