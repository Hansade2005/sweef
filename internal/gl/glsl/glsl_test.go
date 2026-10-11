package glsl

import (
	"math"
	"strings"
	"testing"
)

func fsOpts() Options {
	return Options{WebGL2: true, Extensions: map[string]bool{"GL_OES_standard_derivatives": true, "GL_EXT_shader_texture_lod": true}}
}

func evalColor300(t *testing.T, decls, body string) [4]float32 {
	t.Helper()
	src := "#version 300 es\nprecision highp float;\nprecision highp int;\nout vec4 fc;\n" + decls + "\nvoid main() {\n" + strings.ReplaceAll(body, "gl_FragColor", "fc") + "\n}\n"
	sh := Compile(Fragment, src, fsOpts())
	if !sh.OK {
		t.Fatalf("compile failed:\n%s\nsource:\n%s", sh.Log, src)
	}
	in := sh.NewInstance()
	in.Run()
	var out [4]float32
	for i := range out {
		out[i] = fl(in.R[sh.Outputs[0].Off+i])
	}
	return out
}

// evalColor compiles a fragment shader with the given body in main() and
// returns gl_FragColor.
func evalColor(t *testing.T, decls, body string) [4]float32 {
	t.Helper()
	src := "precision highp float;\n" + decls + "\nvoid main() {\n" + body + "\n}\n"
	sh := Compile(Fragment, src, fsOpts())
	if !sh.OK {
		t.Fatalf("compile failed:\n%s\nsource:\n%s", sh.Log, src)
	}
	in := sh.NewInstance()
	in.Run()
	var out [4]float32
	for i := range out {
		out[i] = fl(in.R[sh.FragColorOff+i])
	}
	return out
}

func near(a, b float32) bool { return math.Abs(float64(a-b)) < 1e-4 }

func expect(t *testing.T, got, want [4]float32) {
	t.Helper()
	for i := range got {
		if !near(got[i], want[i]) {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestArithmetic(t *testing.T) {
	expect(t, evalColor(t, "", "gl_FragColor = vec4(1.0+2.0*3.0, 10.0/4.0, 7.0-2.0, -1.0);"), [4]float32{7, 2.5, 5, -1})
	expect(t, evalColor(t, "", "float a = 3.0; a += 2.0; a *= 2.0; a -= 1.0; a /= 3.0; gl_FragColor = vec4(a);"), [4]float32{3, 3, 3, 3})
	expect(t, evalColor300(t, "", "int i = 7; int j = i / 2; int k = i % 4; gl_FragColor = vec4(float(j), float(k), float(i<<2), float(i>>1));"), [4]float32{3, 3, 28, 3})
}

func TestVectorsSwizzle(t *testing.T) {
	expect(t, evalColor(t, "", "vec4 v = vec4(1.,2.,3.,4.); gl_FragColor = vec4(v.wzyx);"), [4]float32{4, 3, 2, 1})
	expect(t, evalColor(t, "", "vec4 v = vec4(1.,2.,3.,4.); v.xz = vec2(9.,8.); v.yw += 1.0; gl_FragColor = v;"), [4]float32{9, 3, 8, 5})
	expect(t, evalColor(t, "", "vec3 a = vec3(1.0,2.0,3.0); gl_FragColor = vec4(a.zy, a.x*2.0, 1.0);"), [4]float32{3, 2, 2, 1})
	expect(t, evalColor(t, "", "vec4 v = vec4(1.,2.,3.,4.); v.yz = v.xy; gl_FragColor = v;"), [4]float32{1, 1, 2, 4})
	expect(t, evalColor(t, "", "vec4 v = vec4(1.,2.,3.,4.); int i = 2; v[i] = 7.0; gl_FragColor = vec4(v[i], v[1], v.rg);"), [4]float32{7, 2, 1, 2})
}

func TestMatrices(t *testing.T) {
	expect(t, evalColor(t, "", "mat2 m = mat2(1.,2.,3.,4.); vec2 v = m * vec2(1.,1.); gl_FragColor = vec4(v, m[1]);"), [4]float32{4, 6, 3, 4})
	expect(t, evalColor(t, "", "mat3 m = mat3(2.0); vec3 v = vec3(1.,2.,3.)*m; gl_FragColor = vec4(v, m[1][1]);"), [4]float32{2, 4, 6, 2})
	expect(t, evalColor(t, "", "mat4 a = mat4(1.0); mat4 b = mat4(2.0); mat4 c = a*b; gl_FragColor = vec4(c[0][0], c[1][0], determinant(c), transpose(mat2(1.,2.,3.,4.))[0][1]);"), [4]float32{2, 0, 16, 3})
	expect(t, evalColor300(t, "", "mat2 m = inverse(mat2(2.,0.,0.,4.)); gl_FragColor = vec4(m[0][0], m[1][1], 0., 1.);"), [4]float32{0.5, 0.25, 0, 1})
}

func TestControlFlow(t *testing.T) {
	expect(t, evalColor(t, "", "float s = 0.0; for (int i = 0; i < 10; i++) { if (i == 5) continue; if (i == 8) break; s += float(i); } gl_FragColor = vec4(s);"), [4]float32{23, 23, 23, 23})
	expect(t, evalColor(t, "", "int n = 0; while (n < 5) { n++; } int m = 0; do { m += 2; } while (m < 7); gl_FragColor = vec4(float(n), float(m), 0., 0.);"), [4]float32{5, 8, 0, 0})
	expect(t, evalColor(t, "float f(float x) { if (x > 1.0) return x*2.0; return -x; }", "gl_FragColor = vec4(f(2.0), f(0.5), 0.,0.);"), [4]float32{4, -0.5, 0, 0})
	expect(t, evalColor(t, "void sw(inout float a, inout float b) { float t = a; a = b; b = t; } void o(out vec2 r) { r = vec2(5.,6.); }", "float x=1.0, y=2.0; sw(x,y); vec2 q; o(q); gl_FragColor = vec4(x,y,q);"), [4]float32{2, 1, 5, 6})
	expect(t, evalColor(t, "", "float a = true ? 1.0 : 2.0; float b = (1.0 > 2.0 || 3.0 > 2.0) ? 5.0 : 6.0; gl_FragColor = vec4(a, b, 0., 0.);"), [4]float32{1, 5, 0, 0})
}

func TestBuiltins(t *testing.T) {
	expect(t, evalColor(t, "", "gl_FragColor = vec4(sin(0.0), cos(0.0), sqrt(16.0), pow(2.0, 10.0));"), [4]float32{0, 1, 4, 1024})
	expect(t, evalColor(t, "", "gl_FragColor = vec4(mix(0.0, 10.0, 0.25), clamp(5.0, 0.0, 1.0), smoothstep(0.0, 1.0, 0.5), step(0.5, 0.3));"), [4]float32{2.5, 1, 0.5, 0})
	expect(t, evalColor(t, "", "gl_FragColor = vec4(length(vec3(3.,4.,0.)), dot(vec2(1.,2.), vec2(3.,4.)), distance(vec2(0.), vec2(0.,2.)), 0.);"), [4]float32{5, 11, 2, 0})
	expect(t, evalColor(t, "", "vec3 c = cross(vec3(1.,0.,0.), vec3(0.,1.,0.)); gl_FragColor = vec4(c, mod(5.5, 2.0));"), [4]float32{0, 0, 1, 1.5})
	expect(t, evalColor(t, "", "vec3 n = normalize(vec3(0.,3.,4.)); vec3 r = reflect(vec3(1.,-1.,0.), vec3(0.,1.,0.)); gl_FragColor = vec4(n.y, r.xy, float(any(lessThan(vec2(1.,2.), vec2(2.,1.)))));"), [4]float32{0.6, 1, 1, 1})
	expect(t, evalColor(t, "", "gl_FragColor = vec4(floor(1.7), ceil(1.2), fract(1.25), abs(-3.0));"), [4]float32{1, 2, 0.25, 3})
	expect(t, evalColor(t, "", "gl_FragColor = vec4(min(1.0, 2.0), max(vec2(1.,5.), 3.0), sign(-2.0));"), [4]float32{1, 3, 5, -1})
	expect(t, evalColor300(t, "", "gl_FragColor = vec4(float(uint(7)/uint(2)), float(7u>>1u), uintBitsToFloat(floatBitsToUint(2.5)), unpackHalf2x16(packHalf2x16(vec2(1.5,2.5))).y);"), [4]float32{3, 3, 2.5, 2.5})
}

func TestStructsArrays(t *testing.T) {
	expect(t, evalColor(t, "struct L { vec3 c; float k; };", "L l = L(vec3(1.,2.,3.), 4.0); l.c.y += 1.0; gl_FragColor = vec4(l.c, l.k);"), [4]float32{1, 3, 3, 4})
	expect(t, evalColor(t, "const float K[3] = float[3](1.0, 2.0, 3.0);", "float a[3]; for (int i=0;i<3;i++) a[i] = K[i]*2.0; gl_FragColor = vec4(a[0], a[1], a[2], float(a.length()));"), [4]float32{2, 4, 6, 3})
	expect(t, evalColor(t, "", "vec2 v[2]; v[0] = vec2(1.,2.); v[1] = v[0]*2.0; gl_FragColor = vec4(v[1], v[0]);"), [4]float32{2, 4, 1, 2})
}

func TestPreprocessor(t *testing.T) {
	expect(t, evalColor(t, "#define N 4\n#define SQ(x) ((x)*(x))\n#ifdef GL_ES\n#define A 1.0\n#else\n#define A 2.0\n#endif\n#if N > 3 && defined(A)\n#define B 10.0\n#endif\n", "gl_FragColor = vec4(float(N), SQ(3.0), A, B);"), [4]float32{4, 9, 1, 10})
}

func TestConstFolding(t *testing.T) {
	expect(t, evalColor(t, "const float A = 2.0*3.0; const int N = 3; const vec2 V = vec2(A, 1.0);", "float arr[N]; arr[2] = V.x; gl_FragColor = vec4(arr[2], A, float(N), V.y);"), [4]float32{6, 6, 3, 1})
}

func TestErrors(t *testing.T) {
	cases := []struct{ src, want string }{
		{"void main() { gl_FragColor = vec4(x); }", "0:2: 'x' : undeclared identifier"},
		{"void main() {\n float f = 1;\n}", "0:3: '=' : cannot convert from 'const int' to 'mediump float'"},
		{"void main() { vec3 v = vec3(1.0); v.w = 1.0; }", "0:2: 'w' : vector field selection out of range"},
		{"void main() { gl_FragColor = 1.0; }", "cannot convert"},
		{"void main() { int a = 1; a = ; }", "syntax error"},
		{"uniform float u; void main() { u = 1.0; }", "l-value required"},
		{"void main() { foo(1.0); }", "no matching overloaded function found"},
	}
	for _, c := range cases {
		sh := Compile(Fragment, "precision mediump float;\n"+c.src, fsOpts())
		if sh.OK {
			t.Errorf("%q compiled", c.src)
			continue
		}
		if !strings.Contains(sh.Log, "ERROR: 0:") {
			t.Errorf("log format: %q", sh.Log)
		}
		w := c.want
		if !strings.Contains(sh.Log, w) {
			t.Errorf("%q: log %q lacks %q", c.src, sh.Log, w)
		}
	}
}

func TestUniformAndVarying(t *testing.T) {
	src := `precision mediump float;
uniform vec4 u_color; uniform float u_arr[3]; varying vec2 v_uv;
void main() { gl_FragColor = u_color * u_arr[1] + vec4(v_uv, 0., 0.); }`
	sh := Compile(Fragment, src, fsOpts())
	if !sh.OK {
		t.Fatal(sh.Log)
	}
	if len(sh.Uniforms) != 2 || len(sh.Inputs) != 1 {
		t.Fatalf("uniforms %d inputs %d", len(sh.Uniforms), len(sh.Inputs))
	}
	in := sh.NewInstance()
	for _, u := range sh.Uniforms {
		switch u.Name {
		case "u_color":
			for i, v := range []float32{1, 0.5, 0.25, 1} {
				in.R[u.Off+i] = bf(v)
			}
		case "u_arr":
			in.R[u.Off+1] = bf(2)
		}
	}
	in.R[sh.Inputs[0].Off] = bf(0.1)
	in.R[sh.Inputs[0].Off+1] = bf(0.2)
	in.Run()
	got := fl(in.R[sh.FragColorOff+1])
	if !near(got, 1.2) {
		t.Fatalf("got %v", got)
	}
}

func TestES300(t *testing.T) {
	src := `#version 300 es
precision highp float;
precision highp int;
flat in int v_i;
in vec2 v_uv;
layout(location=0) out vec4 o0;
layout(location=1) out vec4 o1;
uniform Block { vec4 a; float b[2]; mat2 m; } blk;
void main() {
  o0 = vec4(float(v_i), v_uv, 1.0) + blk.a;
  o1 = vec4(blk.b[1], blk.m[1], 0.);
  switch (v_i) { case 1: o1.w = 5.0; break; default: o1.w = 6.0; }
}`
	sh := Compile(Fragment, src, fsOpts())
	if !sh.OK {
		t.Fatal(sh.Log)
	}
	if len(sh.Blocks) != 1 || sh.Blocks[0].ByteSize == 0 || len(sh.Outputs) != 2 {
		t.Fatalf("blocks=%d outputs=%d", len(sh.Blocks), len(sh.Outputs))
	}
	in := sh.NewInstance()
	in.R[sh.Inputs[0].Off] = 1
	in.Run()
	if fl(in.R[sh.Outputs[1].Off+3]) != 5 {
		t.Fatalf("switch failed: %v", fl(in.R[sh.Outputs[1].Off+3]))
	}
	// WebGL1 must reject
	o := fsOpts()
	o.WebGL2 = false
	if Compile(Fragment, src, o).OK {
		t.Fatal("ES 300 compiled in WebGL1")
	}
}

func TestVertexShader(t *testing.T) {
	src := `attribute vec3 position; attribute vec2 uv; uniform mat4 mvp; varying vec2 v_uv;
void main() { v_uv = uv; gl_Position = mvp * vec4(position, 1.0); gl_PointSize = 4.0; }`
	sh := Compile(Vertex, src, Options{})
	if !sh.OK {
		t.Fatal(sh.Log)
	}
	in := sh.NewInstance()
	for _, u := range sh.Uniforms {
		for i := 0; i < 4; i++ {
			in.R[u.Off+i*4+i] = bf(2)
		}
	}
	for _, a := range sh.Inputs {
		if a.Name == "position" {
			in.R[a.Off], in.R[a.Off+1], in.R[a.Off+2] = bf(1), bf(2), bf(3)
		}
	}
	in.Run()
	if fl(in.R[sh.PositionOff+1]) != 4 || fl(in.R[sh.PositionOff+3]) != 2 {
		t.Fatalf("position %v", fl(in.R[sh.PositionOff+1]))
	}
}
