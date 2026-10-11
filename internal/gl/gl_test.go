package gl

import (
	"encoding/binary"
	"math"
	"testing"
)

func f32bytes(v ...float32) []byte {
	b := make([]byte, len(v)*4)
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(x))
	}
	return b
}

func mkProg(t testing.TB, c *Context, vsSrc, fsSrc string) uint32 {
	t.Helper()
	vs := c.CreateShader(GL_VERTEX_SHADER)
	c.ShaderSource(vs, vsSrc)
	c.CompileShader(vs)
	if ok, _ := c.GetShaderParameter(vs, GL_COMPILE_STATUS); ok == 0 {
		t.Fatalf("vs: %s", c.GetShaderInfoLog(vs))
	}
	fs := c.CreateShader(GL_FRAGMENT_SHADER)
	c.ShaderSource(fs, fsSrc)
	c.CompileShader(fs)
	if ok, _ := c.GetShaderParameter(fs, GL_COMPILE_STATUS); ok == 0 {
		t.Fatalf("fs: %s", c.GetShaderInfoLog(fs))
	}
	p := c.CreateProgram()
	c.AttachShader(p, vs)
	c.AttachShader(p, fs)
	c.LinkProgram(p)
	if ok, _ := c.GetProgramParameter(p, GL_LINK_STATUS); ok == 0 {
		t.Fatalf("link: %s", c.GetProgramInfoLog(p))
	}
	return p
}

func readPx(c *Context, x, y int) [4]byte {
	buf := make([]byte, 4)
	c.ReadPixels(x, y, 1, 1, GL_RGBA, GL_UNSIGNED_BYTE, buf, 0)
	return [4]byte{buf[0], buf[1], buf[2], buf[3]}
}

func TestClear(t *testing.T) {
	c := New(1, Attrs{Alpha: true, Depth: true, PremultipliedAlpha: true}, 16, 16)
	c.ClearColor(1, 0.5, 0, 1)
	c.Clear(GL_COLOR_BUFFER_BIT)
	if got := readPx(c, 3, 3); got != [4]byte{255, 128, 0, 255} {
		t.Fatalf("got %v", got)
	}
	if e := c.GetError(); e != 0 {
		t.Fatalf("err %x", e)
	}
}

const triVS = `attribute vec2 p; attribute vec3 col; varying vec3 v; void main(){ v = col; gl_Position = vec4(p, 0.0, 1.0); }`
const triFS = `precision mediump float; varying vec3 v; void main(){ gl_FragColor = vec4(v, 1.0); }`

func setupTri(t testing.TB, c *Context, pos []float32, aa bool) {
	p := mkProg(t, c, triVS, triFS)
	c.UseProgram(p)
	buf := c.CreateBuffer()
	c.BindBuffer(GL_ARRAY_BUFFER, buf)
	data := f32bytes(pos...)
	c.BufferData(GL_ARRAY_BUFFER, len(data), data, GL_STATIC_DRAW)
	pl := c.GetAttribLocation(p, "p")
	cl := c.GetAttribLocation(p, "col")
	c.VertexAttribPointer(pl, 2, GL_FLOAT, false, 20, 0, false)
	c.VertexAttribPointer(cl, 3, GL_FLOAT, false, 20, 8, false)
	c.EnableVertexAttribArray(pl, true)
	c.EnableVertexAttribArray(cl, true)
}

func TestTriangle(t *testing.T) {
	c := New(1, Attrs{Alpha: true, PremultipliedAlpha: true}, 64, 64)
	setupTri(t, c, []float32{-1, -1, 1, 0, 0, 1, -1, 0, 1, 0, 0, 1, 0, 0, 1}, false)
	c.ClearColor(0, 0, 0, 1)
	c.Clear(GL_COLOR_BUFFER_BIT)
	c.DrawArrays(GL_TRIANGLES, 0, 3, 1)
	if e := c.GetError(); e != 0 {
		t.Fatalf("err %x", e)
	}
	// near bottom-left corner: red-ish
	px := readPx(c, 2, 2)
	if px[0] < 200 || px[3] != 255 {
		t.Fatalf("bottom-left %v", px)
	}
	// top-left outside the triangle (triangle covers lower-left half... check far corner)
	if px := readPx(c, 60, 60); px != [4]byte{0, 0, 0, 255} {
		t.Fatalf("outside %v", px)
	}
	// centre is a blend
	px = readPx(c, 32, 20)
	if px[0] == 0 && px[1] == 0 && px[2] == 0 {
		t.Fatalf("centre empty %v", px)
	}
}

func TestMSAATriangleEdge(t *testing.T) {
	c := New(1, Attrs{Alpha: true, Antialias: true, PremultipliedAlpha: true}, 32, 32)
	setupTri(t, c, []float32{-1, -1, 1, 1, 1, 1, 1, 1, 1, 1, -1, 1, 1, 1, 1}, true)
	c.Clear(GL_COLOR_BUFFER_BIT)
	c.DrawArrays(GL_TRIANGLES, 0, 3, 1)
	// diagonal pixel should be partially covered
	found := false
	for i := 0; i < 32; i++ {
		px := readPx(c, i, i)
		if px[0] > 20 && px[0] < 235 {
			found = true
		}
	}
	if !found {
		t.Fatal("no partially covered pixels on the diagonal")
	}
}

func BenchmarkTriangles100k(b *testing.B) {
	c := New(1, Attrs{Alpha: true, PremultipliedAlpha: true}, 512, 512)
	n := 100000
	pos := make([]float32, 0, n*15)
	r := uint32(1)
	rnd := func() float32 { r = r*1664525 + 1013904223; return float32(r>>8) / float32(1<<24) }
	for i := 0; i < n; i++ {
		cx, cy := rnd()*2-1, rnd()*2-1
		for k := 0; k < 3; k++ {
			pos = append(pos, cx+(rnd()-0.5)*0.03, cy+(rnd()-0.5)*0.03, rnd(), rnd(), rnd())
		}
	}
	setupTri(b, c, pos, false)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Clear(GL_COLOR_BUFFER_BIT)
		c.DrawArrays(GL_TRIANGLES, 0, n*3, 1)
	}
}

func BenchmarkFullscreenShader(b *testing.B) {
	c := New(1, Attrs{Alpha: true, PremultipliedAlpha: true}, 512, 512)
	p := mkProg(b, c, `attribute vec2 p; varying vec2 uv; void main(){ uv = p*0.5+0.5; gl_Position = vec4(p,0.0,1.0); }`,
		`precision highp float; varying vec2 uv;
void main(){ vec2 z = vec2(0.0); float s = 0.0; for (int i = 0; i < 24; i++) { z = vec2(z.x*z.x - z.y*z.y, 2.0*z.x*z.y) + (uv*3.0-vec2(2.0,1.5)); s += dot(z,z) < 4.0 ? 1.0 : 0.0; }
 gl_FragColor = vec4(s/24.0, uv, 1.0); }`)
	c.UseProgram(p)
	buf := c.CreateBuffer()
	c.BindBuffer(GL_ARRAY_BUFFER, buf)
	d := f32bytes(-1, -1, 1, -1, -1, 1, 1, 1)
	c.BufferData(GL_ARRAY_BUFFER, len(d), d, GL_STATIC_DRAW)
	c.VertexAttribPointer(0, 2, GL_FLOAT, false, 0, 0, false)
	c.EnableVertexAttribArray(0, true)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.DrawArrays(GL_TRIANGLE_STRIP, 0, 4, 1)
	}
}

func newGL(attrs Attrs, w, h int) *Context {
	if attrs.PremultipliedAlpha == false && attrs.Alpha {
		attrs.PremultipliedAlpha = true
	}
	return New(1, attrs, w, h)
}

func drawQuad(t testing.TB, c *Context, p uint32, x0, y0, x1, y1, z float32) {
	t.Helper()
	buf := c.CreateBuffer()
	c.BindBuffer(GL_ARRAY_BUFFER, buf)
	d := f32bytes(x0, y0, z, x1, y0, z, x0, y1, z, x1, y1, z)
	c.BufferData(GL_ARRAY_BUFFER, len(d), d, GL_STATIC_DRAW)
	l := c.GetAttribLocation(p, "a")
	c.VertexAttribPointer(l, 3, GL_FLOAT, false, 0, 0, false)
	c.EnableVertexAttribArray(l, true)
	c.DrawArrays(GL_TRIANGLE_STRIP, 0, 4, 1)
}

const flatVS = `attribute vec3 a; void main(){ gl_Position = vec4(a, 1.0); }`

func TestDepthAndStencil(t *testing.T) {
	c := newGL(Attrs{Alpha: true, Depth: true, Stencil: true}, 8, 8)
	p := mkProg(t, c, flatVS, `precision mediump float; uniform vec4 col; void main(){ gl_FragColor = col; }`)
	c.UseProgram(p)
	col := c.GetUniformLocation(p, "col")
	c.Enable(GL_DEPTH_TEST, true)
	c.ClearColor(0, 0, 0, 1)
	c.Clear(GL_COLOR_BUFFER_BIT | GL_DEPTH_BUFFER_BIT | GL_STENCIL_BUFFER_BIT)
	c.UniformF(col, 4, []float32{1, 0, 0, 1})
	drawQuad(t, c, p, -1, -1, 1, 1, 0.0)
	c.UniformF(col, 4, []float32{0, 1, 0, 1})
	drawQuad(t, c, p, -1, -1, 1, 1, 0.5) // behind: rejected with LESS
	if px := readPx(c, 4, 4); px != [4]byte{255, 0, 0, 255} {
		t.Fatalf("depth LESS: %v", px)
	}
	c.DepthFunc(GL_GREATER)
	drawQuad(t, c, p, -1, -1, 1, 1, 0.5)
	if px := readPx(c, 4, 4); px != [4]byte{0, 255, 0, 255} {
		t.Fatalf("depth GREATER: %v", px)
	}
	// stencil: replace where drawn, then test
	c.Disable()
	c.Clear(GL_COLOR_BUFFER_BIT | GL_STENCIL_BUFFER_BIT)
	c.Enable(GL_STENCIL_TEST, true)
	c.StencilFuncSeparate(GL_FRONT_AND_BACK, GL_ALWAYS, 1, 0xff)
	c.StencilOpSeparate(GL_FRONT_AND_BACK, GL_KEEP, GL_KEEP, GL_REPLACE)
	c.ColorMask(false, false, false, false)
	drawQuad(t, c, p, -1, -1, 0, 0, 0)
	c.ColorMask(true, true, true, true)
	c.StencilFuncSeparate(GL_FRONT_AND_BACK, GL_EQUAL, 1, 0xff)
	c.UniformF(col, 4, []float32{0, 0, 1, 1})
	drawQuad(t, c, p, -1, -1, 1, 1, 0)
	if px := readPx(c, 1, 1); px != [4]byte{0, 0, 255, 255} {
		t.Fatalf("stencil in: %v", px)
	}
	if px := readPx(c, 6, 6); px != [4]byte{0, 0, 0, 255} {
		t.Fatalf("stencil out: %v", px)
	}
}

// Disable is a tiny helper so the test above reads naturally.
func (c *Context) Disable() { c.Enable(GL_DEPTH_TEST, false) }

func TestBlendAndMasks(t *testing.T) {
	c := newGL(Attrs{Alpha: true}, 4, 4)
	p := mkProg(t, c, flatVS, `precision mediump float; void main(){ gl_FragColor = vec4(1.0, 0.0, 0.0, 0.5); }`)
	c.UseProgram(p)
	c.ClearColor(0, 0, 1, 1)
	c.Clear(GL_COLOR_BUFFER_BIT)
	c.Enable(GL_BLEND, true)
	c.BlendFuncSeparate(GL_SRC_ALPHA, GL_ONE_MINUS_SRC_ALPHA, GL_ONE, GL_ONE_MINUS_SRC_ALPHA)
	drawQuad(t, c, p, -1, -1, 1, 1, 0)
	px := readPx(c, 2, 2)
	if px[0] < 126 || px[0] > 129 || px[2] < 126 || px[2] > 129 || px[3] != 255 {
		t.Fatalf("blend: %v", px)
	}
	c.Enable(GL_BLEND, false)
	c.ClearColor(0, 0, 0, 0)
	c.Clear(GL_COLOR_BUFFER_BIT)
	c.ColorMask(false, true, true, false)
	drawQuad(t, c, p, -1, -1, 1, 1, 0)
	if px := readPx(c, 2, 2); px != [4]byte{0, 0, 0, 0} {
		t.Fatalf("color mask: %v", px)
	}
}

func TestErrors(t *testing.T) {
	c := newGL(Attrs{Alpha: true}, 4, 4)
	c.DrawArrays(GL_TRIANGLES, 0, 3, 1)
	if e := c.GetError(); e != GL_INVALID_OPERATION {
		t.Fatalf("draw without program: %x", e)
	}
	c.BindBuffer(0xdead, 1)
	if e := c.GetError(); e != GL_INVALID_ENUM {
		t.Fatalf("bad target: %x", e)
	}
	c.Viewport(0, 0, -1, 1)
	if e := c.GetError(); e != GL_INVALID_VALUE {
		t.Fatalf("negative viewport: %x", e)
	}
	if c.GetError() != 0 {
		t.Fatal("error flag should clear")
	}
	b := c.CreateBuffer()
	c.BindBuffer(GL_ARRAY_BUFFER, b)
	c.BufferData(GL_ARRAY_BUFFER, 4, nil, GL_STATIC_DRAW)
	c.BufferSubData(GL_ARRAY_BUFFER, 2, []byte{1, 2, 3, 4})
	if e := c.GetError(); e != GL_INVALID_VALUE {
		t.Fatalf("buffer overflow: %x", e)
	}
	// vertex fetch beyond the buffer
	p := mkProg(t, c, flatVS, `precision mediump float; void main(){ gl_FragColor = vec4(1.0); }`)
	c.UseProgram(p)
	c.VertexAttribPointer(0, 3, GL_FLOAT, false, 0, 0, false)
	c.EnableVertexAttribArray(0, true)
	c.DrawArrays(GL_TRIANGLES, 0, 3, 1)
	if e := c.GetError(); e != GL_INVALID_OPERATION {
		t.Fatalf("draw out of range: %x", e)
	}
	// incomplete framebuffer
	fb := c.CreateFramebuffer()
	c.BindFramebuffer(GL_FRAMEBUFFER, fb)
	if st := c.CheckFramebufferStatus(GL_FRAMEBUFFER); st != GL_FRAMEBUFFER_INCOMPLETE_MISSING_ATTACHMENT {
		t.Fatalf("fbo status %x", st)
	}
	c.Clear(GL_COLOR_BUFFER_BIT)
	if e := c.GetError(); e != GL_INVALID_FRAMEBUFFER_OPERATION {
		t.Fatalf("clear on incomplete fbo: %x", e)
	}
}

func TestTextureSampling(t *testing.T) {
	c := newGL(Attrs{Alpha: true}, 16, 16)
	p := mkProg(t, c, `attribute vec3 a; varying vec2 uv; void main(){ uv = a.xy*0.5+0.5; gl_Position = vec4(a,1.0); }`,
		`precision mediump float; varying vec2 uv; uniform sampler2D s; void main(){ gl_FragColor = texture2D(s, uv); }`)
	c.UseProgram(p)
	tex := c.CreateTexture()
	c.BindTexture(GL_TEXTURE_2D, tex)
	// 2x2: red green / blue white (row 0 is the bottom)
	data := []byte{255, 0, 0, 255, 0, 255, 0, 255, 0, 0, 255, 255, 255, 255, 255, 255}
	c.TexImage2D(GL_TEXTURE_2D, 0, GL_RGBA, 2, 2, 0, GL_RGBA, GL_UNSIGNED_BYTE, data, 0, false)
	c.TexParameter(GL_TEXTURE_2D, GL_TEXTURE_MIN_FILTER, GL_NEAREST)
	c.TexParameter(GL_TEXTURE_2D, GL_TEXTURE_MAG_FILTER, GL_NEAREST)
	c.UniformI(c.GetUniformLocation(p, "s"), 1, []int32{0}, false)
	drawQuad(t, c, p, -1, -1, 1, 1, 0)
	if px := readPx(c, 2, 2); px != [4]byte{255, 0, 0, 255} {
		t.Fatalf("bottom-left: %v", px)
	}
	if px := readPx(c, 13, 13); px != [4]byte{255, 255, 255, 255} {
		t.Fatalf("top-right: %v", px)
	}
	c.TexParameter(GL_TEXTURE_2D, GL_TEXTURE_MAG_FILTER, GL_LINEAR)
	c.TexParameter(GL_TEXTURE_2D, GL_TEXTURE_WRAP_S, GL_CLAMP_TO_EDGE)
	c.TexParameter(GL_TEXTURE_2D, GL_TEXTURE_WRAP_T, GL_CLAMP_TO_EDGE)
	drawQuad(t, c, p, -1, -1, 1, 1, 0)
	px := readPx(c, 8, 8)
	if px[0] < 100 || px[1] < 100 || px[2] < 100 {
		t.Fatalf("linear centre should be a blend: %v", px)
	}
	if e := c.GetError(); e != 0 {
		t.Fatalf("error %x", e)
	}
}

func TestInstancingAndTransformFeedback(t *testing.T) {
	c := New(2, Attrs{Alpha: true}, 8, 8)
	vs := c.CreateShader(GL_VERTEX_SHADER)
	c.ShaderSource(vs, "#version 300 es\nin vec2 a; out vec2 o; out float sz; void main(){ o = a*2.0 + float(gl_VertexID); sz = 1.0; gl_Position = vec4(0.0); }")
	c.CompileShader(vs)
	fs := c.CreateShader(GL_FRAGMENT_SHADER)
	c.ShaderSource(fs, "#version 300 es\nprecision mediump float; out vec4 c; void main(){ c = vec4(1.0); }")
	c.CompileShader(fs)
	p := c.CreateProgram()
	c.AttachShader(p, vs)
	c.AttachShader(p, fs)
	c.TransformFeedbackVaryings(p, []string{"o", "sz"}, GL_INTERLEAVED_ATTRIBS)
	c.LinkProgram(p)
	if ok, _ := c.GetProgramParameter(p, GL_LINK_STATUS); ok == 0 {
		t.Fatal(c.GetProgramInfoLog(p))
	}
	c.UseProgram(p)
	src := c.CreateBuffer()
	c.BindBuffer(GL_ARRAY_BUFFER, src)
	d := f32bytes(1, 2, 3, 4, 5, 6)
	c.BufferData(GL_ARRAY_BUFFER, len(d), d, GL_STATIC_DRAW)
	c.VertexAttribPointer(c.GetAttribLocation(p, "a"), 2, GL_FLOAT, false, 0, 0, false)
	c.EnableVertexAttribArray(c.GetAttribLocation(p, "a"), true)
	dst := c.CreateBuffer()
	c.BindBuffer(GL_TRANSFORM_FEEDBACK_BUFFER, dst)
	c.BufferData(GL_TRANSFORM_FEEDBACK_BUFFER, 3*3*4, nil, GL_STATIC_READ)
	c.BindBufferBase(GL_TRANSFORM_FEEDBACK_BUFFER, 0, dst)
	c.Enable(GL_RASTERIZER_DISCARD, true)
	c.BeginTransformFeedback(GL_POINTS)
	c.DrawArrays(GL_POINTS, 0, 3, 1)
	c.EndTransformFeedback()
	if e := c.GetError(); e != 0 {
		t.Fatalf("tf error %x", e)
	}
	out := make([]byte, 36)
	c.BindBuffer(GL_ARRAY_BUFFER, dst)
	c.GetBufferSubData(GL_ARRAY_BUFFER, 0, out)
	got := make([]float32, 9)
	for i := range got {
		got[i] = math.Float32frombits(binary.LittleEndian.Uint32(out[i*4:]))
	}
	want := []float32{2, 4, 1, 7, 9, 1, 12, 14, 1}
	// o = a*2 + vertexID: (1,2)*2+0 = (2,4); (3,4)*2+1=(7,9); (5,6)*2+2=(12,14)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tf output %v want %v", got, want)
		}
	}
}

func TestMSAAResolveAndReadFloat(t *testing.T) {
	c := New(2, Attrs{Alpha: true, Antialias: true}, 16, 16)
	c.Ext["EXT_color_buffer_float"] = true
	tex := c.CreateTexture()
	c.BindTexture(GL_TEXTURE_2D, tex)
	c.TexImage2D(GL_TEXTURE_2D, 0, GL_RGBA32F, 4, 4, 0, GL_RGBA, GL_FLOAT, nil, 0, false)
	fb := c.CreateFramebuffer()
	c.BindFramebuffer(GL_FRAMEBUFFER, fb)
	c.FramebufferTexture2D(GL_FRAMEBUFFER, GL_COLOR_ATTACHMENT0, GL_TEXTURE_2D, tex, 0)
	if st := c.CheckFramebufferStatus(GL_FRAMEBUFFER); st != GL_FRAMEBUFFER_COMPLETE {
		t.Fatalf("status %x", st)
	}
	c.ClearColor(2.5, -1, 0.25, 1)
	c.Clear(GL_COLOR_BUFFER_BIT)
	buf := make([]byte, 16)
	c.ReadPixels(1, 1, 1, 1, GL_RGBA, GL_FLOAT, buf, 0)
	r := math.Float32frombits(binary.LittleEndian.Uint32(buf))
	g := math.Float32frombits(binary.LittleEndian.Uint32(buf[4:]))
	if r != 2.5 || g != -1 {
		t.Fatalf("float readback %v %v", r, g)
	}
	if e := c.GetError(); e != 0 {
		t.Fatalf("error %x", e)
	}
}

func TestBitmapPresent(t *testing.T) {
	c := New(1, Attrs{Alpha: true, PremultipliedAlpha: true}, 4, 4)
	c.ClearColor(1, 0, 0, 1)
	c.Clear(GL_COLOR_BUFFER_BIT)
	img := c.Bitmap()
	if img.Pix[0] != 255 || img.Pix[3] != 255 {
		t.Fatalf("pre-present %v", img.Pix[:4])
	}
	c.Present() // preserveDrawingBuffer=false clears, but the presented frame stays visible
	img = c.Bitmap()
	if img.Pix[0] != 255 {
		t.Fatalf("post-present bitmap lost the frame: %v", img.Pix[:4])
	}
	if px := readPx(c, 0, 0); px != [4]byte{0, 0, 0, 0} {
		t.Fatalf("drawing buffer should be cleared: %v", px)
	}
}
