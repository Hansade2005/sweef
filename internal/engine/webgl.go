package engine

// webgl.go — host side of the WebGL implementation. The GL state machine,
// GLSL interpreter and rasterizer live in internal/gl; this file exposes them
// to the page as __sw.gl* host functions that webgl_prelude.js wraps in the
// WebGLRenderingContext / WebGL2RenderingContext API. Drawing buffers are
// published through the bitmap registry so screenshots, toDataURL and
// drawImage see the pixels.

import (
	_ "embed"
	"encoding/base64"
	"fmt"
	"image"
	"sync"

	"modernc.org/quickjs"

	"github.com/sweefbrowser/sweef/internal/gl"
)

//go:embed webgl_prelude.js
var webglPreludeJS string

func init() {
	registerPrelude("webgl", webglPreludeJS)
	registerHostExtension("webgl", func(j *jsVM) map[string]hostFn {
		h := &glHost{j: j, ctxs: map[int]*glCtx{}, next: 1}
		return map[string]hostFn{
			"glConsts":  h.consts,
			"glCreate":  h.create,
			"glResize":  h.resize,
			"glDestroy": h.destroy,
			"glPresent": h.present,
			"glc":       h.call,
		}
	})
}

type glHost struct {
	j    *jsVM
	mu   sync.Mutex
	ctxs map[int]*glCtx
	next int
}

type glCtx struct {
	c    *gl.Context
	node int64
	h    *glHost
}

// Bitmap implements BitmapSource.
func (g *glCtx) Bitmap() image.Image {
	img := g.c.Bitmap()
	if img == nil {
		return nil
	}
	return img
}

// Size implements BitmapSource.
func (g *glCtx) Size() (int, int) { return g.c.W, g.c.H }

func glNum(a []any, i int) float64 {
	if i >= len(a) {
		return 0
	}
	switch v := a[i].(type) {
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case float64:
		return v
	case bool:
		if v {
			return 1
		}
	}
	return 0
}

func glI(a []any, i int) int      { return int(glNum(a, i)) }
func glU(a []any, i int) uint32   { return uint32(int64(glNum(a, i))) }
func glB(a []any, i int) bool     { return i < len(a) && truthyAny(a[i]) }
func glS(a []any, i int) string   { return argS(a, i) }
func glF(a []any, i int) float32  { return float32(glNum(a, i)) }
func glIsNil(a []any, i int) bool { return i >= len(a) || a[i] == nil }

// glBytes decodes a base64 argument (nil when absent).
func glBytes(a []any, i int) []byte {
	if i >= len(a) || a[i] == nil {
		return nil
	}
	s, ok := a[i].(string)
	if !ok {
		return nil
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil
	}
	return b
}

func glFloats(a []any, i int) []float32 {
	if i >= len(a) {
		return nil
	}
	switch v := a[i].(type) {
	case *quickjs.Object:
		var f []float64
		if err := v.Into(&f); err != nil {
			return nil
		}
		out := make([]float32, len(f))
		for k, x := range f {
			out[k] = float32(x)
		}
		return out
	case []any:
		out := make([]float32, len(v))
		for k, x := range v {
			out[k] = float32(glNum([]any{x}, 0))
		}
		return out
	case string:
		b, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			return nil
		}
		out := make([]float32, len(b)/4)
		for k := range out {
			out[k] = f32le(b[k*4:])
		}
		return out
	}
	return nil
}

func glInts(a []any, i int) []int32 {
	if i >= len(a) {
		return nil
	}
	switch v := a[i].(type) {
	case *quickjs.Object:
		var f []float64
		if err := v.Into(&f); err != nil {
			return nil
		}
		out := make([]int32, len(f))
		for k, x := range f {
			out[k] = int32(int64(x))
		}
		return out
	case []any:
		out := make([]int32, len(v))
		for k, x := range v {
			out[k] = int32(int64(glNum([]any{x}, 0)))
		}
		return out
	case string:
		b, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			return nil
		}
		out := make([]int32, len(b)/4)
		for k := range out {
			out[k] = int32(u32le(b[k*4:]))
		}
		return out
	}
	return nil
}

func glUints(a []any, i int) []uint32 {
	x := glInts(a, i)
	out := make([]uint32, len(x))
	for k, v := range x {
		out[k] = uint32(v)
	}
	return out
}

func u32le(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

func f32le(b []byte) float32 { return mathF32(u32le(b)) }

func glStrings(a []any, i int) []string {
	if i >= len(a) {
		return nil
	}
	if o, ok := a[i].(*quickjs.Object); ok {
		var ss []string
		if err := o.Into(&ss); err != nil {
			return nil
		}
		return ss
	}
	v, _ := a[i].([]any)
	out := make([]string, len(v))
	for k, x := range v {
		out[k] = fmt.Sprint(x)
	}
	return out
}

func (h *glHost) consts(a []any) (any, error) {
	conv := func(m map[string]uint32) map[string]any {
		o := make(map[string]any, len(m))
		for k, v := range m {
			o[k] = int(v)
			if v > 0x7FFFFFFF {
				o[k] = float64(v)
			}
		}
		return o
	}
	return map[string]any{"c1": conv(gl.Constants1), "c2": conv(gl.Constants2), "ext": conv(gl.ExtConstants)}, nil
}

func (h *glHost) get(a []any) *glCtx {
	id := glI(a, 0)
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.ctxs[id]
}

// create(version, alpha, depth, stencil, antialias, premultiplied, preserve, w, h, nodeId)
func (h *glHost) create(a []any) (any, error) {
	attrs := gl.Attrs{Alpha: glB(a, 1), Depth: glB(a, 2), Stencil: glB(a, 3), Antialias: glB(a, 4), PremultipliedAlpha: glB(a, 5), PreserveDrawingBuffer: glB(a, 6)}
	w, hh := glI(a, 7), glI(a, 8)
	if w < 0 || hh < 0 || w > 16384 || hh > 16384 || w*hh > 64<<20 {
		return 0, nil
	}
	c := gl.New(glI(a, 0), attrs, w, hh)
	g := &glCtx{c: c, node: int64(glI(a, 9)), h: h}
	h.mu.Lock()
	id := h.next
	h.next++
	h.ctxs[id] = g
	h.mu.Unlock()
	if g.node != 0 && h.j.tab != nil {
		h.j.tab.SetBitmapSource(g.node, g)
	}
	return id, nil
}

func (h *glHost) resize(a []any) (any, error) {
	g := h.get(a)
	if g == nil {
		return false, nil
	}
	w, hh := glI(a, 1), glI(a, 2)
	if w < 0 || hh < 0 || w > 16384 || hh > 16384 || w*hh > 64<<20 {
		return false, nil
	}
	g.c.Resize(w, hh)
	return true, nil
}

func (h *glHost) destroy(a []any) (any, error) {
	id := glI(a, 0)
	h.mu.Lock()
	g := h.ctxs[id]
	delete(h.ctxs, id)
	h.mu.Unlock()
	if g != nil && g.node != 0 && h.j.tab != nil {
		h.j.tab.SetBitmapSource(g.node, nil)
	}
	return nil, nil
}

func (h *glHost) present(a []any) (any, error) {
	if g := h.get(a); g != nil {
		g.c.Present()
	}
	return nil, nil
}
