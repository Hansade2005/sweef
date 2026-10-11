package engine

import (
	"encoding/base64"
	"math"

	"github.com/sweefbrowser/sweef/internal/gl"
)

func mathF32(u uint32) float32 { return math.Float32frombits(u) }

func glVal(v gl.Val) any {
	switch v.K {
	case "n":
		return []any{"n", v.N}
	case "b":
		return []any{"b", v.B}
	case "s":
		return []any{"s", v.S}
	case "f":
		out := make([]any, len(v.F))
		for i, x := range v.F {
			out[i] = float64(x)
		}
		return []any{"f", out}
	case "i":
		out := make([]any, len(v.I))
		for i, x := range v.I {
			out[i] = int(x)
		}
		return []any{"i", out}
	case "u":
		out := make([]any, len(v.U))
		for i, x := range v.U {
			out[i] = float64(x)
		}
		return []any{"u", out}
	case "bv":
		out := make([]any, len(v.BV))
		for i, x := range v.BV {
			out[i] = x
		}
		return []any{"bv", out}
	case "o":
		return []any{"o", v.Obj, int(v.ID)}
	}
	return []any{"null"}
}

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// call dispatches glc(ctx, name, ...args).
func (h *glHost) call(a []any) (any, error) {
	g := h.get(a)
	if g == nil {
		return nil, nil
	}
	name := glS(a, 1)
	c := g.c
	c.Lock()
	defer c.Unlock()
	r := a[2:]
	switch name {
	// ---- errors / state
	case "getError":
		return int(c.GetError()), nil
	case "setError":
		c.SetError(glU(r, 0))
	case "enable":
		c.Enable(glU(r, 0), true)
	case "disable":
		c.Enable(glU(r, 0), false)
	case "isEnabled":
		return c.IsEnabled(glU(r, 0)), nil
	case "viewport":
		c.Viewport(glI(r, 0), glI(r, 1), glI(r, 2), glI(r, 3))
	case "scissor":
		c.Scissor(glI(r, 0), glI(r, 1), glI(r, 2), glI(r, 3))
	case "depthRange":
		c.DepthRange(glF(r, 0), glF(r, 1))
	case "clearColor":
		c.ClearColor(glF(r, 0), glF(r, 1), glF(r, 2), glF(r, 3))
	case "clearDepth":
		c.ClearDepth(glF(r, 0))
	case "clearStencil":
		c.ClearStencil(int32(glI(r, 0)))
	case "colorMask":
		c.ColorMask(glB(r, 0), glB(r, 1), glB(r, 2), glB(r, 3))
	case "depthMask":
		c.DepthMask(glB(r, 0))
	case "depthFunc":
		c.DepthFunc(glU(r, 0))
	case "stencilFuncSeparate":
		c.StencilFuncSeparate(glU(r, 0), glU(r, 1), int32(glI(r, 2)), glU(r, 3))
	case "stencilOpSeparate":
		c.StencilOpSeparate(glU(r, 0), glU(r, 1), glU(r, 2), glU(r, 3))
	case "stencilMaskSeparate":
		c.StencilMaskSeparate(glU(r, 0), glU(r, 1))
	case "blendColor":
		c.BlendColor(glF(r, 0), glF(r, 1), glF(r, 2), glF(r, 3))
	case "blendEquationSeparate":
		c.BlendEquationSeparate(glU(r, 0), glU(r, 1))
	case "blendFuncSeparate":
		c.BlendFuncSeparate(glU(r, 0), glU(r, 1), glU(r, 2), glU(r, 3))
	case "cullFace":
		c.CullFace(glU(r, 0))
	case "frontFace":
		c.FrontFace(glU(r, 0))
	case "lineWidth":
		c.LineWidth(glF(r, 0))
	case "polygonOffset":
		c.PolygonOffset(glF(r, 0), glF(r, 1))
	case "sampleCoverage":
		c.SampleCoverage(glF(r, 0), glB(r, 1))
	case "hint":
		c.Hint(glU(r, 0), glU(r, 1))
	case "pixelStorei":
		c.PixelStore(glU(r, 0), glI(r, 1))
	case "getParameter":
		v, ok := c.GetParameter(glU(r, 0))
		if !ok {
			return nil, nil
		}
		return glVal(v), nil
	case "getIndexedParameter":
		v, ok := c.GetIndexedParameter(glU(r, 0), glI(r, 1))
		if !ok {
			return nil, nil
		}
		return glVal(v), nil
	case "enableExt":
		c.Ext[glS(r, 0)] = true
	case "ext":
		return c.Ext[glS(r, 0)], nil
	case "lose":
		c.LoseContext()
	case "isLost":
		return c.Lost(), nil
	case "getShaderPrecisionFormat":
		lo, hi, p, ok := c.GetShaderPrecisionFormat(glU(r, 0), glU(r, 1))
		if !ok {
			return nil, nil
		}
		return []any{lo, hi, p}, nil
	case "clear":
		c.Clear(glU(r, 0))
	case "clearBuffer":
		// kind, buffer, drawbuffer, values...
		kind := glS(r, 0)
		var f [4]float32
		var iv [4]int32
		switch kind {
		case "f":
			fl := glFloats(r, 3)
			copy(f[:], fl)
			c.ClearBuffer(glU(r, 1), glI(r, 2), 'f', f, iv)
		case "i":
			x := glInts(r, 3)
			copy(iv[:], x)
			c.ClearBuffer(glU(r, 1), glI(r, 2), 'i', f, iv)
		case "u":
			x := glInts(r, 3)
			copy(iv[:], x)
			c.ClearBuffer(glU(r, 1), glI(r, 2), 'u', f, iv)
		case "fi":
			c.ClearBuffer(gl.GL_DEPTH, 0, 'f', [4]float32{glF(r, 3)}, iv)
			c.ClearBuffer(gl.GL_STENCIL, 0, 'i', f, [4]int32{int32(glI(r, 4))})
		}
	case "readPixels":
		// x,y,w,h,format,type,pboOff -> base64 or nil
		x, y, w, hh := glI(r, 0), glI(r, 1), glI(r, 2), glI(r, 3)
		format, typ := glU(r, 4), glU(r, 5)
		if w < 0 || hh < 0 {
			c.SetError(gl.GL_INVALID_VALUE)
			return nil, nil
		}
		pk := c.PackState()
		need := pk.RequiredBytes(uint32(w), uint32(hh), 1, format, typ)
		if c.PackBufferBound() {
			c.ReadPixels(x, y, w, hh, format, typ, nil, glI(r, 6))
			return nil, nil
		}
		if need > 1<<28 {
			c.SetError(gl.GL_OUT_OF_MEMORY)
			return nil, nil
		}
		buf := make([]byte, max(need, 0))
		before := c.PeekError()
		c.ReadPixels(x, y, w, hh, format, typ, buf, 0)
		if c.PeekError() != before {
			return nil, nil
		}
		if fw, fh, ok := c.ReadExtent(); ok && (x < 0 || y < 0 || x+w > fw || y+hh > fh) && w > 0 && hh > 0 {
			// pixels outside the framebuffer must stay untouched: describe the valid window
			pb, rs, base := c.PackLayout(w, format, typ)
			ix0, iy0, ix1, iy1 := max(0, -x), max(0, -y), min(w, fw-x), min(hh, fh-y)
			if ix1 <= ix0 || iy1 <= iy0 {
				return nil, nil
			}
			return []any{b64(buf), pb, rs, base, ix0, iy0, ix1, iy1}, nil
		}
		return b64(buf), nil
	// ---- buffers
	case "createBuffer":
		return int(c.CreateBuffer()), nil
	case "deleteBuffer":
		c.DeleteBuffer(glU(r, 0))
	case "isBuffer":
		return c.IsBuffer(glU(r, 0)), nil
	case "bindBuffer":
		c.BindBuffer(glU(r, 0), glU(r, 1))
	case "bufferData":
		// target, size|nil, data|nil, usage
		if glIsNil(r, 2) {
			sz := glNum(r, 1)
			if sz < 0 || sz > 1<<30 {
				if sz < 0 {
					c.SetError(gl.GL_INVALID_VALUE)
				} else {
					c.SetError(gl.GL_OUT_OF_MEMORY)
				}
				return nil, nil
			}
			c.BufferData(glU(r, 0), int(sz), nil, glU(r, 3))
		} else {
			d := glBytes(r, 2)
			if d == nil {
				d = []byte{}
			}
			c.BufferData(glU(r, 0), len(d), d, glU(r, 3))
		}
	case "bufferSubData":
		c.BufferSubData(glU(r, 0), glI(r, 1), glBytes(r, 2))
	case "copyBufferSubData":
		c.CopyBufferSubData(glU(r, 0), glU(r, 1), glI(r, 2), glI(r, 3), glI(r, 4))
	case "getBufferSubData":
		n := glI(r, 2)
		if n < 0 || n > 1<<28 {
			c.SetError(gl.GL_INVALID_VALUE)
			return nil, nil
		}
		dst := make([]byte, n)
		before := c.PeekError()
		c.GetBufferSubData(glU(r, 0), glI(r, 1), dst)
		if c.PeekError() != before {
			return nil, nil
		}
		return b64(dst), nil
	case "getBufferParameter":
		v, ok := c.GetBufferParameter(glU(r, 0), glU(r, 1))
		if !ok {
			return nil, nil
		}
		return v, nil
	case "bindBufferBase":
		c.BindBufferBase(glU(r, 0), glI(r, 1), glU(r, 2))
	case "bindBufferRange":
		c.BindBufferRange(glU(r, 0), glI(r, 1), glU(r, 2), glI(r, 3), glI(r, 4))
	// ---- vertex arrays
	case "createVertexArray":
		return int(c.CreateVertexArray()), nil
	case "deleteVertexArray":
		c.DeleteVertexArray(glU(r, 0))
	case "isVertexArray":
		return c.IsVertexArray(glU(r, 0)), nil
	case "bindVertexArray":
		c.BindVertexArray(glU(r, 0))
	case "vertexAttribPointer":
		c.VertexAttribPointer(glI(r, 0), glI(r, 1), glU(r, 2), glB(r, 3), glI(r, 4), glI(r, 5), false)
	case "vertexAttribIPointer":
		c.VertexAttribPointer(glI(r, 0), glI(r, 1), glU(r, 2), false, glI(r, 3), glI(r, 4), true)
	case "enableVertexAttribArray":
		c.EnableVertexAttribArray(glI(r, 0), true)
	case "disableVertexAttribArray":
		c.EnableVertexAttribArray(glI(r, 0), false)
	case "vertexAttribDivisor":
		c.VertexAttribDivisor(glI(r, 0), glI(r, 1))
	case "vertexAttrib":
		// idx, kind('f','i','u'), v0..v3
		idx := glI(r, 0)
		var v [4]uint32
		switch glS(r, 1) {
		case "f":
			d := [4]float32{0, 0, 0, 1}
			for k := 0; k < 4; k++ {
				if 2+k < len(r) && r[2+k] != nil {
					d[k] = glF(r, 2+k)
				}
			}
			for k := range v {
				v[k] = math.Float32bits(d[k])
			}
			c.VertexAttrib(idx, gl.GL_FLOAT, v)
		case "i":
			d := [4]int32{0, 0, 0, 1}
			for k := 0; k < 4; k++ {
				if 2+k < len(r) && r[2+k] != nil {
					d[k] = int32(glI(r, 2+k))
				}
			}
			for k := range v {
				v[k] = uint32(d[k])
			}
			c.VertexAttrib(idx, gl.GL_INT, v)
		default:
			d := [4]uint32{0, 0, 0, 1}
			for k := 0; k < 4; k++ {
				if 2+k < len(r) && r[2+k] != nil {
					d[k] = glU(r, 2+k)
				}
			}
			c.VertexAttrib(idx, gl.GL_UNSIGNED_INT, d)
		}
	case "getVertexAttrib":
		v, ok := c.GetVertexAttrib(glI(r, 0), glU(r, 1))
		if !ok {
			return nil, nil
		}
		return glVal(v), nil
	case "getVertexAttribOffset":
		return c.GetVertexAttribOffset(glI(r, 0)), nil
	// ---- draw
	case "drawArrays":
		c.DrawArrays(glU(r, 0), glI(r, 1), glI(r, 2), 1)
	case "drawElements":
		c.DrawElements(glU(r, 0), glI(r, 1), glU(r, 2), glI(r, 3), 1)
	case "drawArraysInstanced":
		c.DrawArrays(glU(r, 0), glI(r, 1), glI(r, 2), glI(r, 3))
	case "drawElementsInstanced":
		c.DrawElements(glU(r, 0), glI(r, 1), glU(r, 2), glI(r, 3), glI(r, 4))
	case "drawBuffers":
		c.DrawBuffers(glUints(r, 0))
	case "readBuffer":
		c.ReadBuffer(glU(r, 0))
	// ---- shaders / programs
	case "createShader":
		return int(c.CreateShader(glU(r, 0))), nil
	case "shaderSource":
		c.ShaderSource(glU(r, 0), glS(r, 1))
	case "compileShader":
		c.CompileShader(glU(r, 0))
	case "getShaderParameter":
		v, ok := c.GetShaderParameter(glU(r, 0), glU(r, 1))
		if !ok {
			return nil, nil
		}
		return v, nil
	case "getShaderInfoLog":
		return c.GetShaderInfoLog(glU(r, 0)), nil
	case "getShaderSource":
		return c.GetShaderSource(glU(r, 0)), nil
	case "deleteShader":
		c.DeleteShader(glU(r, 0))
	case "isShader":
		return c.IsShader(glU(r, 0)), nil
	case "createProgram":
		return int(c.CreateProgram()), nil
	case "deleteProgram":
		c.DeleteProgram(glU(r, 0))
	case "isProgram":
		return c.IsProgram(glU(r, 0)), nil
	case "attachShader":
		c.AttachShader(glU(r, 0), glU(r, 1))
	case "detachShader":
		c.DetachShader(glU(r, 0), glU(r, 1))
	case "getAttachedShaders":
		ids := c.GetAttachedShaders(glU(r, 0))
		out := make([]any, len(ids))
		for i, v := range ids {
			out[i] = int(v)
		}
		return out, nil
	case "bindAttribLocation":
		c.BindAttribLocation(glU(r, 0), glI(r, 1), glS(r, 2))
	case "linkProgram":
		c.LinkProgram(glU(r, 0))
	case "validateProgram":
		c.ValidateProgram(glU(r, 0))
	case "useProgram":
		c.UseProgram(glU(r, 0))
	case "getProgramParameter":
		v, ok := c.GetProgramParameter(glU(r, 0), glU(r, 1))
		if !ok {
			return nil, nil
		}
		return v, nil
	case "getProgramInfoLog":
		return c.GetProgramInfoLog(glU(r, 0)), nil
	case "getAttribLocation":
		return c.GetAttribLocation(glU(r, 0), glS(r, 1)), nil
	case "getUniformLocation":
		return c.GetUniformLocation(glU(r, 0), glS(r, 1)), nil
	case "getActiveAttrib":
		n, t, s, ok := c.GetActiveAttrib(glU(r, 0), glI(r, 1))
		if !ok {
			return nil, nil
		}
		return []any{n, int(t), s}, nil
	case "getActiveUniform":
		n, t, s, ok := c.GetActiveUniform(glU(r, 0), glI(r, 1))
		if !ok {
			return nil, nil
		}
		return []any{n, int(t), s}, nil
	case "getUniform":
		vals, typ, ok := c.GetUniform(glU(r, 0), glI(r, 1))
		if !ok {
			return nil, nil
		}
		out := make([]any, len(vals))
		kind := gl.UniformKind(typ)
		for i, v := range vals {
			switch kind {
			case "int":
				out[i] = int(int32(v))
			case "uint":
				out[i] = float64(v)
			case "bool":
				out[i] = v != 0
			default:
				out[i] = float64(math.Float32frombits(v))
			}
		}
		return []any{int(typ), kind, out}, nil
	case "uniformF":
		c.UniformF(glI(r, 0), glI(r, 1), glFloats(r, 2))
	case "uniformI":
		c.UniformI(glI(r, 0), glI(r, 1), glInts(r, 2), false)
	case "uniformU":
		c.UniformI(glI(r, 0), glI(r, 1), glInts(r, 2), true)
	case "uniformMatrix":
		c.UniformMatrix(glI(r, 0), glI(r, 1), glI(r, 2), glB(r, 3), glFloats(r, 4))
	case "getUniformBlockIndex":
		return float64(c.GetUniformBlockIndex(glU(r, 0), glS(r, 1))), nil
	case "getActiveUniformBlockParameter":
		v, ok := c.GetActiveUniformBlockParameter(glU(r, 0), glI(r, 1), glU(r, 2))
		if !ok {
			return nil, nil
		}
		return glVal(v), nil
	case "getActiveUniformBlockName":
		return c.GetActiveUniformBlockName(glU(r, 0), glI(r, 1)), nil
	case "uniformBlockBinding":
		c.UniformBlockBinding(glU(r, 0), glI(r, 1), glI(r, 2))
	case "getUniformIndices":
		idx := c.GetUniformIndices(glU(r, 0), glStrings(r, 1))
		if idx == nil {
			return nil, nil
		}
		out := make([]any, len(idx))
		for i, v := range idx {
			out[i] = float64(v)
		}
		return out, nil
	case "getActiveUniforms":
		v, ok := c.GetActiveUniforms(glU(r, 0), glUints(r, 1), glU(r, 2))
		if !ok {
			return nil, nil
		}
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = x
		}
		return out, nil
	case "getFragDataLocation":
		return c.GetFragDataLocation(glU(r, 0), glS(r, 1)), nil
	case "transformFeedbackVaryings":
		c.TransformFeedbackVaryings(glU(r, 0), glStrings(r, 1), glU(r, 2))
	case "getTransformFeedbackVarying":
		n, t, s, ok := c.GetTransformFeedbackVarying(glU(r, 0), glI(r, 1))
		if !ok {
			return nil, nil
		}
		return []any{n, int(t), s}, nil
	case "getTranslatedShaderSource":
		return c.GetShaderSource(glU(r, 0)), nil
	// ---- textures
	case "createTexture":
		return int(c.CreateTexture()), nil
	case "deleteTexture":
		c.DeleteTexture(glU(r, 0))
	case "isTexture":
		return c.IsTexture(glU(r, 0)), nil
	case "bindTexture":
		c.BindTexture(glU(r, 0), glU(r, 1))
	case "activeTexture":
		c.ActiveTexture(glU(r, 0))
	case "texParameter":
		c.TexParameter(glU(r, 0), glU(r, 1), glF(r, 2))
	case "getTexParameter":
		v, ok := c.GetTexParameter(glU(r, 0), glU(r, 1))
		if !ok {
			return nil, nil
		}
		return v, nil
	case "generateMipmap":
		c.GenerateMipmap(glU(r, 0))
	case "texImage2D":
		// target level internal w h border format type data|nil pboOff dom
		c.TexImage2D(glU(r, 0), glI(r, 1), glU(r, 2), glI(r, 3), glI(r, 4), glI(r, 5), glU(r, 6), glU(r, 7), glBytes(r, 8), glI(r, 9), glB(r, 10))
	case "texSubImage2D":
		// target level x y w h format type data pboOff dom
		c.TexSubImage2D(glU(r, 0), glI(r, 1), glI(r, 2), glI(r, 3), glI(r, 4), glI(r, 5), glU(r, 6), glU(r, 7), glBytes(r, 8), glI(r, 9), glB(r, 10))
	case "texImage3D":
		c.TexImage3D(glU(r, 0), glI(r, 1), glU(r, 2), glI(r, 3), glI(r, 4), glI(r, 5), glI(r, 6), glU(r, 7), glU(r, 8), glBytes(r, 9), glI(r, 10))
	case "texSubImage3D":
		c.TexSubImage3D(glU(r, 0), glI(r, 1), glI(r, 2), glI(r, 3), glI(r, 4), glI(r, 5), glI(r, 6), glI(r, 7), glU(r, 8), glU(r, 9), glBytes(r, 10), glI(r, 11), glB(r, 12))
	case "texStorage":
		c.TexStorage(glU(r, 0), glI(r, 1), glU(r, 2), glI(r, 3), glI(r, 4), glI(r, 5))
	case "copyTexImage2D":
		c.CopyTexImage2D(glU(r, 0), glI(r, 1), glU(r, 2), glI(r, 3), glI(r, 4), glI(r, 5), glI(r, 6), glI(r, 7))
	case "copyTexSubImage2D":
		c.CopyTexSubImage2D(glU(r, 0), glI(r, 1), glI(r, 2), glI(r, 3), glI(r, 4), glI(r, 5), glI(r, 6), glI(r, 7))
	case "copyTexSubImage3D":
		c.CopyTexSubImage3D(glU(r, 0), glI(r, 1), glI(r, 2), glI(r, 3), glI(r, 4), glI(r, 5), glI(r, 6), glI(r, 7), glI(r, 8))
	// ---- framebuffers / renderbuffers
	case "createFramebuffer":
		return int(c.CreateFramebuffer()), nil
	case "deleteFramebuffer":
		c.DeleteFramebuffer(glU(r, 0))
	case "isFramebuffer":
		return c.IsFramebuffer(glU(r, 0)), nil
	case "bindFramebuffer":
		c.BindFramebuffer(glU(r, 0), glU(r, 1))
	case "framebufferTexture2D":
		c.FramebufferTexture2D(glU(r, 0), glU(r, 1), glU(r, 2), glU(r, 3), glI(r, 4))
	case "framebufferTextureLayer":
		c.FramebufferTextureLayer(glU(r, 0), glU(r, 1), glU(r, 2), glI(r, 3), glI(r, 4))
	case "framebufferRenderbuffer":
		c.FramebufferRenderbuffer(glU(r, 0), glU(r, 1), glU(r, 2), glU(r, 3))
	case "checkFramebufferStatus":
		return int(c.CheckFramebufferStatus(glU(r, 0))), nil
	case "getFramebufferAttachmentParameter":
		v, ok := c.GetFramebufferAttachmentParameter(glU(r, 0), glU(r, 1), glU(r, 2))
		if !ok {
			return nil, nil
		}
		return v, nil
	case "blitFramebuffer":
		c.BlitFramebuffer(glI(r, 0), glI(r, 1), glI(r, 2), glI(r, 3), glI(r, 4), glI(r, 5), glI(r, 6), glI(r, 7), glU(r, 8), glU(r, 9))
	case "invalidateFramebuffer":
		c.InvalidateFramebuffer(glU(r, 0), glUints(r, 1))
	case "createRenderbuffer":
		return int(c.CreateRenderbuffer()), nil
	case "deleteRenderbuffer":
		c.DeleteRenderbuffer(glU(r, 0))
	case "isRenderbuffer":
		return c.IsRenderbuffer(glU(r, 0)), nil
	case "bindRenderbuffer":
		c.BindRenderbuffer(glU(r, 0), glU(r, 1))
	case "renderbufferStorage":
		c.RenderbufferStorage(glU(r, 0), glU(r, 1), glI(r, 2), glI(r, 3), glI(r, 4))
	case "getRenderbufferParameter":
		v, ok := c.GetRenderbufferParameter(glU(r, 0), glU(r, 1))
		if !ok {
			return nil, nil
		}
		return v, nil
	case "getInternalformatParameter":
		v, ok := c.GetInternalformatParameter(glU(r, 0), glU(r, 1), glU(r, 2))
		if !ok {
			return nil, nil
		}
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = int(x)
		}
		return out, nil
	// ---- samplers, queries, sync, tf
	case "createSampler":
		return int(c.CreateSampler()), nil
	case "deleteSampler":
		c.DeleteSampler(glU(r, 0))
	case "isSampler":
		return c.IsSampler(glU(r, 0)), nil
	case "bindSampler":
		c.BindSampler(glI(r, 0), glU(r, 1))
	case "samplerParameter":
		c.SamplerParameter(glU(r, 0), glU(r, 1), glF(r, 2))
	case "getSamplerParameter":
		v, ok := c.GetSamplerParameter(glU(r, 0), glU(r, 1))
		if !ok {
			return nil, nil
		}
		return v, nil
	case "createQuery":
		return int(c.CreateQuery()), nil
	case "deleteQuery":
		c.DeleteQuery(glU(r, 0))
	case "isQuery":
		return c.IsQuery(glU(r, 0)), nil
	case "beginQuery":
		c.BeginQuery(glU(r, 0), glU(r, 1))
	case "endQuery":
		c.EndQuery(glU(r, 0))
	case "getQuery":
		v, ok := c.GetQuery(glU(r, 0), glU(r, 1))
		if !ok {
			return nil, nil
		}
		return int(v), nil
	case "getQueryParameter":
		v, ok := c.GetQueryParameter(glU(r, 0), glU(r, 1))
		if !ok {
			return nil, nil
		}
		return float64(v), nil
	case "fenceSync":
		return int(c.FenceSync()), nil
	case "syncSignaled":
		return c.SyncSignaled(glU(r, 0)), nil
	case "isSync":
		return c.IsSync(glU(r, 0)), nil
	case "deleteSync":
		c.DeleteSync(glU(r, 0))
	case "clientWaitSync":
		return int(c.ClientWaitSync(glU(r, 0))), nil
	case "createTransformFeedback":
		return int(c.CreateTransformFeedback()), nil
	case "deleteTransformFeedback":
		c.DeleteTransformFeedback(glU(r, 0))
	case "isTransformFeedback":
		return c.IsTransformFeedback(glU(r, 0)), nil
	case "bindTransformFeedback":
		c.BindTransformFeedback(glU(r, 0))
	case "beginTransformFeedback":
		c.BeginTransformFeedback(glU(r, 0))
	case "endTransformFeedback":
		c.EndTransformFeedback()
	case "pauseTransformFeedback":
		c.PauseTransformFeedback()
	case "resumeTransformFeedback":
		c.ResumeTransformFeedback()
	default:
		return nil, errUnknownGL(name)
	}
	return nil, nil
}

type errUnknownGL string

func (e errUnknownGL) Error() string { return "webgl: unknown host call " + string(e) }

var _ = base64.StdEncoding
