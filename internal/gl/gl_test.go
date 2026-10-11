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
