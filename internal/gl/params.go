package gl

// Val is a typed result of a getter, marshalled by the host layer.
type Val struct {
	K   string // "n" number, "b" bool, "s" string, "f" float32 array, "i" int32 array, "u" uint32 array, "bv" bool array, "o" object, "null"
	N   float64
	B   bool
	S   string
	F   []float32
	I   []int32
	U   []uint32
	BV  []bool
	Obj string
	ID  uint32
}

func vn(n int) Val        { return Val{K: "n", N: float64(n)} }
func vb(b bool) Val       { return Val{K: "b", B: b} }
func vs(s string) Val     { return Val{K: "s", S: s} }
func vf(f ...float32) Val { return Val{K: "f", F: f} }
func vi(i ...int32) Val   { return Val{K: "i", I: i} }
func vo(kind string, id uint32) Val {
	if id == 0 {
		return Val{K: "null"}
	}
	return Val{K: "o", Obj: kind, ID: id}
}

func idOfBuf(b *Buffer) uint32 {
	if b == nil {
		return 0
	}
	return b.ID
}

func (c *Context) fbBits(which string) int {
	rt, _ := c.resolveTarget(c.drawFB)
	var f *Format
	switch which {
	case "r", "g", "b", "a":
		im := rt.colors[0]
		if c.drawFB != nil && !c.drawFB.isDefault {
			im = nil
			for i := range rt.colors {
				if rt.colors[i] != nil {
					im = rt.colors[i]
					break
				}
			}
		}
		if im == nil {
			return 0
		}
		f = im.Fmt
		if c.drawFB.isDefault {
			if which == "a" && !c.Attrs.Alpha {
				return 0
			}
		}
		return f.Bits[map[string]int{"r": 0, "g": 1, "b": 2, "a": 3}[which]]
	case "d":
		if rt.depth != nil && rt.depth.Fmt.IsDepth() {
			if c.drawFB.isDefault {
				return 24
			}
			return rt.depth.Fmt.DBits
		}
	case "s":
		if rt.stencil != nil && rt.stencil.St != nil {
			return 8
		}
	}
	return 0
}

// GetParameter implements gl.getParameter.
func (c *Context) GetParameter(p uint32) (Val, bool) {
	L := &c.Lim
	v2 := c.Version == 2
	switch p {
	case GL_ACTIVE_TEXTURE:
		return vn(GL_TEXTURE0 + c.activeUnit), true
	case GL_ALIASED_LINE_WIDTH_RANGE:
		return vf(1, 1), true
	case GL_ALIASED_POINT_SIZE_RANGE:
		return vf(1, 1023), true
	case GL_ALPHA_BITS:
		return vn(c.fbBits("a")), true
	case GL_RED_BITS:
		return vn(c.fbBits("r")), true
	case GL_GREEN_BITS:
		return vn(c.fbBits("g")), true
	case GL_BLUE_BITS:
		return vn(c.fbBits("b")), true
	case GL_DEPTH_BITS:
		return vn(c.fbBits("d")), true
	case GL_STENCIL_BITS:
		return vn(c.fbBits("s")), true
	case GL_ARRAY_BUFFER_BINDING:
		return vo("buffer", idOfBuf(c.arrayBuf)), true
	case GL_ELEMENT_ARRAY_BUFFER_BINDING:
		return vo("buffer", idOfBuf(c.vao.Element)), true
	case GL_BLEND:
		return vb(c.enBlend), true
	case GL_BLEND_COLOR:
		return vf(c.blendColor[:]...), true
	case GL_BLEND_DST_ALPHA:
		return vn(int(c.blendDstA)), true
	case GL_BLEND_DST_RGB:
		return vn(int(c.blendDstRGB)), true
	case GL_BLEND_SRC_ALPHA:
		return vn(int(c.blendSrcA)), true
	case GL_BLEND_SRC_RGB:
		return vn(int(c.blendSrcRGB)), true
	case GL_BLEND_EQUATION_ALPHA:
		return vn(int(c.blendEqA)), true
	case GL_BLEND_EQUATION_RGB:
		return vn(int(c.blendEqRGB)), true
	case GL_COLOR_CLEAR_VALUE:
		return vf(c.clearColor[:]...), true
	case GL_COLOR_WRITEMASK:
		return Val{K: "bv", BV: c.colorMask[:]}, true
	case GL_COMPRESSED_TEXTURE_FORMATS:
		return Val{K: "u", U: []uint32{}}, true
	case GL_CULL_FACE:
		return vb(c.enCull), true
	case GL_CULL_FACE_MODE:
		return vn(int(c.cullMode)), true
	case GL_CURRENT_PROGRAM:
		if c.program == nil {
			return vo("program", 0), true
		}
		return vo("program", c.program.ID), true
	case GL_DEPTH_CLEAR_VALUE:
		return vn2(float64(c.clearDepth)), true
	case GL_DEPTH_FUNC:
		return vn(int(c.depthFunc)), true
	case GL_DEPTH_RANGE:
		return vf(c.depthRange[0], c.depthRange[1]), true
	case GL_DEPTH_TEST:
		return vb(c.enDepth), true
	case GL_DEPTH_WRITEMASK:
		return vb(c.depthMask), true
	case GL_DITHER:
		return vb(c.enDither), true
	case GL_FRAMEBUFFER_BINDING:
		return vo("framebuffer", fbID(c.drawFB)), true
	case GL_FRONT_FACE:
		return vn(int(c.frontFace)), true
	case GL_GENERATE_MIPMAP_HINT:
		return vn(int(c.hints[0])), true
	case GL_IMPLEMENTATION_COLOR_READ_FORMAT:
		rt, _ := c.resolveTarget(c.readFB)
		if im := rt.readColor(c.readFB); im != nil && im.Fmt.Integer {
			return vn(int(GL_RGBA_INTEGER)), true
		}
		return vn(int(GL_RGBA)), true
	case GL_IMPLEMENTATION_COLOR_READ_TYPE:
		rt, _ := c.resolveTarget(c.readFB)
		if im := rt.readColor(c.readFB); im != nil {
			if im.Fmt.Integer {
				if im.Fmt.Unsigned {
					return vn(int(GL_UNSIGNED_INT)), true
				}
				return vn(int(GL_INT)), true
			}
			if im.Fmt.Class == ClsF32 && !im.Fmt.Snorm {
				return vn(int(GL_FLOAT)), true
			}
		}
		return vn(int(GL_UNSIGNED_BYTE)), true
	case GL_LINE_WIDTH:
		return vn2(float64(c.lineWidth)), true
	case GL_MAX_COMBINED_TEXTURE_IMAGE_UNITS:
		return vn(L.MaxCombinedTexUnits), true
	case GL_MAX_CUBE_MAP_TEXTURE_SIZE:
		return vn(L.MaxCubeMapSize), true
	case GL_MAX_FRAGMENT_UNIFORM_VECTORS:
		return vn(L.MaxFragUniformVecs), true
	case GL_MAX_RENDERBUFFER_SIZE:
		return vn(L.MaxRenderbufferSize), true
	case GL_MAX_TEXTURE_IMAGE_UNITS:
		return vn(L.MaxTextureUnits), true
	case GL_MAX_TEXTURE_SIZE:
		return vn(L.MaxTextureSize), true
	case GL_MAX_VARYING_VECTORS:
		return vn(L.MaxVaryingVecs), true
	case GL_MAX_VERTEX_ATTRIBS:
		return vn(L.MaxVertexAttribs), true
	case GL_MAX_VERTEX_TEXTURE_IMAGE_UNITS:
		return vn(L.MaxVertexTexUnits), true
	case GL_MAX_VERTEX_UNIFORM_VECTORS:
		return vn(L.MaxVertexUniformVecs), true
	case GL_MAX_VIEWPORT_DIMS:
		return vi(int32(L.MaxViewportDims[0]), int32(L.MaxViewportDims[1])), true
	case GL_PACK_ALIGNMENT:
		return vn(c.pixStore.Pack.Alignment), true
	case GL_UNPACK_ALIGNMENT:
		return vn(c.pixStore.Unpack.Alignment), true
	case GL_POLYGON_OFFSET_FACTOR:
		return vn2(float64(c.polyFactor)), true
	case GL_POLYGON_OFFSET_UNITS:
		return vn2(float64(c.polyUnits)), true
	case GL_POLYGON_OFFSET_FILL:
		return vb(c.enPoly), true
	case GL_RENDERBUFFER_BINDING:
		if c.rb == nil {
			return vo("renderbuffer", 0), true
		}
		return vo("renderbuffer", c.rb.ID), true
	case GL_RENDERER:
		return vs("WebKit WebGL"), true
	case GL_VENDOR:
		return vs("WebKit"), true
	case GL_VERSION:
		if v2 {
			return vs("WebGL 2.0 (OpenGL ES 3.0 Chromium)"), true
		}
		return vs("WebGL 1.0 (OpenGL ES 2.0 Chromium)"), true
	case GL_SHADING_LANGUAGE_VERSION:
		if v2 {
			return vs("WebGL GLSL ES 3.00 (OpenGL ES GLSL ES 3.0 Chromium)"), true
		}
		return vs("WebGL GLSL ES 1.0 (OpenGL ES GLSL ES 1.0 Chromium)"), true
	case GL_SAMPLE_BUFFERS:
		rt, _ := c.resolveTarget(c.drawFB)
		return vn(b2i(rt.S > 1)), true
	case GL_SAMPLES:
		rt, _ := c.resolveTarget(c.drawFB)
		if rt.S > 1 {
			return vn(rt.S), true
		}
		return vn(0), true
	case GL_SAMPLE_COVERAGE_INVERT:
		return vb(c.sampleCovInv), true
	case GL_SAMPLE_COVERAGE_VALUE:
		return vn2(float64(c.sampleCovVal)), true
	case GL_SAMPLE_ALPHA_TO_COVERAGE:
		return vb(c.enA2C), true
	case GL_SAMPLE_COVERAGE:
		return vb(c.enSampleCov), true
	case GL_SCISSOR_BOX:
		return vi(int32(c.scissor[0]), int32(c.scissor[1]), int32(c.scissor[2]), int32(c.scissor[3])), true
	case GL_SCISSOR_TEST:
		return vb(c.enScissor), true
	case GL_STENCIL_BACK_FAIL:
		return vn(int(c.stencilB.Fail)), true
	case GL_STENCIL_BACK_FUNC:
		return vn(int(c.stencilB.Func)), true
	case GL_STENCIL_BACK_PASS_DEPTH_FAIL:
		return vn(int(c.stencilB.ZFail)), true
	case GL_STENCIL_BACK_PASS_DEPTH_PASS:
		return vn(int(c.stencilB.ZPass)), true
	case GL_STENCIL_BACK_REF:
		return vn(int(c.stencilB.Ref)), true
	case GL_STENCIL_BACK_VALUE_MASK:
		return vn(int(c.stencilB.Mask)), true
	case GL_STENCIL_BACK_WRITEMASK:
		return vn(int(c.stencilB.WMask)), true
	case GL_STENCIL_CLEAR_VALUE:
		return vn(int(c.clearStencil)), true
	case GL_STENCIL_FAIL:
		return vn(int(c.stencilF.Fail)), true
	case GL_STENCIL_FUNC:
		return vn(int(c.stencilF.Func)), true
	case GL_STENCIL_PASS_DEPTH_FAIL:
		return vn(int(c.stencilF.ZFail)), true
	case GL_STENCIL_PASS_DEPTH_PASS:
		return vn(int(c.stencilF.ZPass)), true
	case GL_STENCIL_REF:
		return vn(int(c.stencilF.Ref)), true
	case GL_STENCIL_TEST:
		return vb(c.enStencil), true
	case GL_STENCIL_VALUE_MASK:
		return vn(int(c.stencilF.Mask)), true
	case GL_STENCIL_WRITEMASK:
		return vn(int(c.stencilF.WMask)), true
	case GL_SUBPIXEL_BITS:
		return vn(4), true
	case GL_TEXTURE_BINDING_2D, GL_TEXTURE_BINDING_CUBE_MAP:
		slot := 0
		if p == GL_TEXTURE_BINDING_CUBE_MAP {
			slot = 1
		}
		if t := c.units[c.activeUnit].tex[slot]; t != nil {
			return vo("texture", t.ID), true
		}
		return vo("texture", 0), true
	case GL_UNPACK_COLORSPACE_CONVERSION_WEBGL:
		return vn(int(c.pixStore.Colorspace)), true
	case GL_UNPACK_FLIP_Y_WEBGL:
		return vb(c.pixStore.FlipY), true
	case GL_UNPACK_PREMULTIPLY_ALPHA_WEBGL:
		return vb(c.pixStore.Premul), true
	case GL_VIEWPORT:
		return vi(int32(c.viewport[0]), int32(c.viewport[1]), int32(c.viewport[2]), int32(c.viewport[3])), true
	case GL_MAX_TEXTURE_MAX_ANISOTROPY_EXT:
		if c.Ext["EXT_texture_filter_anisotropic"] {
			return vn(16), true
		}
	case GL_UNMASKED_VENDOR_WEBGL:
		if c.Ext["WEBGL_debug_renderer_info"] {
			return vs("Sweef"), true
		}
	case GL_UNMASKED_RENDERER_WEBGL:
		if c.Ext["WEBGL_debug_renderer_info"] {
			return vs("Sweef Software Renderer"), true
		}
	case GL_MAX_DRAW_BUFFERS_WEBGL:
		if c.Ext["WEBGL_draw_buffers"] || v2 {
			return vn(L.MaxDrawBuffers), true
		}
	case GL_VERTEX_ARRAY_BINDING_OES:
		if c.Ext["OES_vertex_array_object"] || v2 {
			return vo("vao", vaoID(c.vao)), true
		}
	case GL_FRAGMENT_SHADER_DERIVATIVE_HINT:
		if c.Ext["OES_standard_derivatives"] || v2 {
			return vn(int(c.hints[1])), true
		}
	case GL_TIMESTAMP_EXT, GL_GPU_DISJOINT_EXT:
		return vn(0), true
	}
	if p >= GL_DRAW_BUFFER0 && p < GL_DRAW_BUFFER0+16 && (v2 || c.Ext["WEBGL_draw_buffers"]) {
		i := int(p - GL_DRAW_BUFFER0)
		if i < L.MaxDrawBuffers {
			if c.drawFB.isDefault {
				return vn(int(c.drawFB.DrawBuffers[0])), true
			}
			return vn(int(c.drawFB.DrawBuffers[i])), true
		}
	}
	if p == GL_MAX_COLOR_ATTACHMENTS && (v2 || c.Ext["WEBGL_draw_buffers"]) {
		return vn(L.MaxColorAttachments), true
	}
	if v2 {
		switch p {
		case GL_COPY_READ_BUFFER_BINDING:
			return vo("buffer", idOfBuf(c.copyRead)), true
		case GL_COPY_WRITE_BUFFER_BINDING:
			return vo("buffer", idOfBuf(c.copyWrite)), true
		case GL_DRAW_FRAMEBUFFER_BINDING:
			return vo("framebuffer", fbID(c.drawFB)), true
		case GL_READ_FRAMEBUFFER_BINDING:
			return vo("framebuffer", fbID(c.readFB)), true
		case GL_MAX_3D_TEXTURE_SIZE:
			return vn(L.Max3DTextureSize), true
		case GL_MAX_ARRAY_TEXTURE_LAYERS:
			return vn(L.MaxArrayLayers), true
		case GL_MAX_COMBINED_FRAGMENT_UNIFORM_COMPONENTS:
			return vn(245760), true
		case GL_MAX_COMBINED_VERTEX_UNIFORM_COMPONENTS:
			return vn(245760), true
		case GL_MAX_COMBINED_UNIFORM_BLOCKS:
			return vn(60), true
		case GL_MAX_ELEMENTS_INDICES:
			return vn(2147483647), true
		case GL_MAX_ELEMENTS_VERTICES:
			return vn(2147483647), true
		case GL_MAX_FRAGMENT_INPUT_COMPONENTS:
			return vn(128), true
		case GL_MAX_FRAGMENT_UNIFORM_BLOCKS:
			return vn(14), true
		case GL_MAX_FRAGMENT_UNIFORM_COMPONENTS:
			return vn(16384), true
		case GL_MAX_PROGRAM_TEXEL_OFFSET:
			return vn(7), true
		case GL_MIN_PROGRAM_TEXEL_OFFSET:
			return vn(-8), true
		case GL_MAX_SAMPLES:
			return vn(L.MaxSamples), true
		case GL_MAX_SERVER_WAIT_TIMEOUT:
			return vn(0), true
		case GL_MAX_TEXTURE_LOD_BIAS:
			return vn(15), true
		case GL_MAX_TRANSFORM_FEEDBACK_INTERLEAVED_COMPONENTS:
			return vn(128), true
		case GL_MAX_TRANSFORM_FEEDBACK_SEPARATE_ATTRIBS:
			return vn(4), true
		case GL_MAX_TRANSFORM_FEEDBACK_SEPARATE_COMPONENTS:
			return vn(4), true
		case GL_MAX_UNIFORM_BLOCK_SIZE:
			return vn(L.MaxUBOSize), true
		case GL_MAX_UNIFORM_BUFFER_BINDINGS:
			return vn(L.MaxUBOBindings), true
		case GL_MAX_VARYING_COMPONENTS:
			return vn(124), true
		case GL_MAX_VERTEX_OUTPUT_COMPONENTS:
			return vn(128), true
		case GL_MAX_VERTEX_UNIFORM_BLOCKS:
			return vn(14), true
		case GL_MAX_VERTEX_UNIFORM_COMPONENTS:
			return vn(16384), true
		case GL_PACK_ROW_LENGTH:
			return vn(c.pixStore.Pack.RowLength), true
		case GL_PACK_SKIP_PIXELS:
			return vn(c.pixStore.Pack.SkipPixels), true
		case GL_PACK_SKIP_ROWS:
			return vn(c.pixStore.Pack.SkipRows), true
		case GL_UNPACK_ROW_LENGTH:
			return vn(c.pixStore.Unpack.RowLength), true
		case GL_UNPACK_SKIP_PIXELS:
			return vn(c.pixStore.Unpack.SkipPixels), true
		case GL_UNPACK_SKIP_ROWS:
			return vn(c.pixStore.Unpack.SkipRows), true
		case GL_UNPACK_SKIP_IMAGES:
			return vn(c.pixStore.Unpack.SkipImages), true
		case GL_UNPACK_IMAGE_HEIGHT:
			return vn(c.pixStore.Unpack.ImageHeight), true
		case GL_PIXEL_PACK_BUFFER_BINDING:
			return vo("buffer", idOfBuf(c.packBuf)), true
		case GL_PIXEL_UNPACK_BUFFER_BINDING:
			return vo("buffer", idOfBuf(c.unpackBuf)), true
		case GL_RASTERIZER_DISCARD:
			return vb(c.enRDiscard), true
		case GL_READ_BUFFER:
			return vn(int(c.readFB.ReadBuffer)), true
		case GL_SAMPLER_BINDING:
			if s := c.units[c.activeUnit].sampler; s != nil {
				return vo("sampler", s.ID), true
			}
			return vo("sampler", 0), true
		case GL_TEXTURE_BINDING_2D_ARRAY, GL_TEXTURE_BINDING_3D:
			slot := 3
			if p == GL_TEXTURE_BINDING_3D {
				slot = 2
			}
			if t := c.units[c.activeUnit].tex[slot]; t != nil {
				return vo("texture", t.ID), true
			}
			return vo("texture", 0), true
		case GL_TRANSFORM_FEEDBACK_ACTIVE:
			return vb(c.tf.Active), true
		case GL_TRANSFORM_FEEDBACK_PAUSED:
			return vb(c.tf.Paused), true
		case GL_TRANSFORM_FEEDBACK_BINDING:
			if c.tf == c.defTF {
				return vo("tf", 0), true
			}
			return vo("tf", c.tf.ID), true
		case GL_TRANSFORM_FEEDBACK_BUFFER_BINDING:
			return vo("buffer", idOfBuf(c.tfBuf)), true
		case GL_UNIFORM_BUFFER_BINDING:
			return vo("buffer", idOfBuf(c.uboGeneric)), true
		case GL_UNIFORM_BUFFER_OFFSET_ALIGNMENT:
			return vn(256), true
		case GL_VERTEX_ARRAY_BINDING:
			return vo("vao", vaoID(c.vao)), true
		case GL_MAX_CLIENT_WAIT_TIMEOUT_WEBGL:
			return vn(0), true
		case 0x8D6B: // MAX_ELEMENT_INDEX
			return vn2(1073741823), true
		}
	}
	c.setErr(GL_INVALID_ENUM)
	return Val{}, false
}

func vn2(f float64) Val { return Val{K: "n", N: f} }

func fbID(f *Framebuffer) uint32 {
	if f == nil || f.isDefault {
		return 0
	}
	return f.ID
}

func vaoID(v *VAO) uint32 {
	if v == nil {
		return 0
	}
	return v.ID
}

// GetIndexedParameter implements getIndexedParameter.
func (c *Context) GetIndexedParameter(p uint32, idx int) (Val, bool) {
	switch p {
	case GL_UNIFORM_BUFFER_BINDING, GL_UNIFORM_BUFFER_START, GL_UNIFORM_BUFFER_SIZE:
		if idx < 0 || idx >= len(c.ubo) {
			c.setErr(GL_INVALID_VALUE)
			return Val{}, false
		}
		b := c.ubo[idx]
		switch p {
		case GL_UNIFORM_BUFFER_BINDING:
			return vo("buffer", idOfBuf(b.Buf)), true
		case GL_UNIFORM_BUFFER_START:
			return vn(b.Off), true
		}
		return vn(b.Size), true
	case GL_TRANSFORM_FEEDBACK_BUFFER_BINDING, GL_TRANSFORM_FEEDBACK_BUFFER_START, GL_TRANSFORM_FEEDBACK_BUFFER_SIZE:
		if idx < 0 || idx >= 4 {
			c.setErr(GL_INVALID_VALUE)
			return Val{}, false
		}
		b := c.tf.Bufs[idx]
		switch p {
		case GL_TRANSFORM_FEEDBACK_BUFFER_BINDING:
			return vo("buffer", idOfBuf(b.Buf)), true
		case GL_TRANSFORM_FEEDBACK_BUFFER_START:
			return vn(b.Off), true
		}
		return vn(b.Size), true
	}
	c.setErr(GL_INVALID_ENUM)
	return Val{}, false
}

// GetShaderPrecisionFormat returns rangeMin, rangeMax, precision.
func (c *Context) GetShaderPrecisionFormat(shader, prec uint32) (int, int, int, bool) {
	if shader != GL_VERTEX_SHADER && shader != GL_FRAGMENT_SHADER {
		c.setErr(GL_INVALID_ENUM)
		return 0, 0, 0, false
	}
	switch prec {
	case GL_LOW_FLOAT, GL_MEDIUM_FLOAT:
		return 15, 15, 10, true
	case GL_HIGH_FLOAT:
		return 127, 127, 23, true
	case GL_LOW_INT, GL_MEDIUM_INT:
		return 15, 14, 0, true
	case GL_HIGH_INT:
		return 31, 30, 0, true
	}
	c.setErr(GL_INVALID_ENUM)
	return 0, 0, 0, false
}

// GetVertexAttrib implements getVertexAttrib.
func (c *Context) GetVertexAttrib(idx int, pname uint32) (Val, bool) {
	if idx < 0 || idx >= c.Lim.MaxVertexAttribs {
		c.setErr(GL_INVALID_VALUE)
		return Val{}, false
	}
	a := &c.vao.Attribs[idx]
	switch pname {
	case GL_VERTEX_ATTRIB_ARRAY_BUFFER_BINDING:
		return vo("buffer", idOfBuf(a.Buf)), true
	case GL_VERTEX_ATTRIB_ARRAY_ENABLED:
		return vb(a.Enabled), true
	case GL_VERTEX_ATTRIB_ARRAY_SIZE:
		if a.Size == 0 {
			return vn(4), true
		}
		return vn(a.Size), true
	case GL_VERTEX_ATTRIB_ARRAY_STRIDE:
		return vn(a.Stride), true
	case GL_VERTEX_ATTRIB_ARRAY_TYPE:
		if a.Type == 0 {
			return vn(int(GL_FLOAT)), true
		}
		return vn(int(a.Type)), true
	case GL_VERTEX_ATTRIB_ARRAY_NORMALIZED:
		return vb(a.Normalized), true
	case GL_CURRENT_VERTEX_ATTRIB:
		g := c.generic[idx]
		switch c.genericTyp[idx] {
		case GL_INT:
			return vi(int32(g[0]), int32(g[1]), int32(g[2]), int32(g[3])), true
		case GL_UNSIGNED_INT:
			return Val{K: "u", U: g[:]}, true
		}
		return vf(f32from(g[0]), f32from(g[1]), f32from(g[2]), f32from(g[3])), true
	case GL_VERTEX_ATTRIB_ARRAY_DIVISOR:
		if c.Version == 2 || c.Ext["ANGLE_instanced_arrays"] {
			return vn(a.Divisor), true
		}
	case GL_VERTEX_ATTRIB_ARRAY_INTEGER:
		if c.Version == 2 {
			return vb(a.Integer), true
		}
	}
	c.setErr(GL_INVALID_ENUM)
	return Val{}, false
}

func (c *Context) GetVertexAttribOffset(idx int) int {
	if idx < 0 || idx >= c.Lim.MaxVertexAttribs {
		c.setErr(GL_INVALID_VALUE)
		return 0
	}
	return c.vao.Attribs[idx].Offset
}

// ---- uniform blocks -------------------------------------------------------------

func (c *Context) GetUniformBlockIndex(pid uint32, name string) uint32 {
	p := c.progs[pid]
	if p == nil {
		c.setErr(GL_INVALID_VALUE)
		return GL_INVALID_INDEX
	}
	for i, b := range p.Blocks {
		if b.Name == name {
			return uint32(i)
		}
	}
	return GL_INVALID_INDEX
}

func (c *Context) GetActiveUniformBlockParameter(pid uint32, idx int, pname uint32) (Val, bool) {
	p := c.progs[pid]
	if p == nil || idx < 0 || idx >= len(p.Blocks) {
		c.setErr(GL_INVALID_VALUE)
		return Val{}, false
	}
	b := p.Blocks[idx]
	switch pname {
	case GL_UNIFORM_BLOCK_BINDING:
		return vn(b.Binding), true
	case GL_UNIFORM_BLOCK_DATA_SIZE:
		return vn(b.DataSize), true
	case GL_UNIFORM_BLOCK_ACTIVE_UNIFORMS:
		return vn(len(b.Members)), true
	case GL_UNIFORM_BLOCK_ACTIVE_UNIFORM_INDICES:
		u := make([]uint32, len(b.Members))
		for i, m := range b.Members {
			u[i] = uint32(m)
		}
		return Val{K: "u", U: u}, true
	case GL_UNIFORM_BLOCK_REFERENCED_BY_VERTEX_SHADER:
		return vb(b.RefVS), true
	case GL_UNIFORM_BLOCK_REFERENCED_BY_FRAGMENT_SHADER:
		return vb(b.RefFS), true
	}
	c.setErr(GL_INVALID_ENUM)
	return Val{}, false
}

func (c *Context) GetActiveUniformBlockName(pid uint32, idx int) string {
	p := c.progs[pid]
	if p == nil || idx < 0 || idx >= len(p.Blocks) {
		c.setErr(GL_INVALID_VALUE)
		return ""
	}
	return p.Blocks[idx].Name
}

func (c *Context) UniformBlockBinding(pid uint32, idx, binding int) {
	p := c.progs[pid]
	if p == nil || idx < 0 || idx >= len(p.Blocks) || binding < 0 || binding >= c.Lim.MaxUBOBindings {
		c.setErr(GL_INVALID_VALUE)
		return
	}
	p.Blocks[idx].Binding = binding
}

func (c *Context) GetUniformIndices(pid uint32, names []string) []uint32 {
	p := c.progs[pid]
	if p == nil {
		c.setErr(GL_INVALID_VALUE)
		return nil
	}
	out := make([]uint32, len(names))
	for i, n := range names {
		out[i] = GL_INVALID_INDEX
		for j, l := range p.Leaves {
			if l.Name == n || l.Array && l.Name == n+"[0]" {
				out[i] = uint32(j)
				break
			}
		}
	}
	return out
}

func (c *Context) GetActiveUniforms(pid uint32, idxs []uint32, pname uint32) ([]float64, bool) {
	p := c.progs[pid]
	if p == nil {
		c.setErr(GL_INVALID_VALUE)
		return nil, false
	}
	out := make([]float64, len(idxs))
	for i, ix := range idxs {
		if int(ix) >= len(p.Leaves) {
			c.setErr(GL_INVALID_VALUE)
			return nil, false
		}
		l := p.Leaves[ix]
		switch pname {
		case GL_UNIFORM_TYPE:
			out[i] = float64(l.GLType)
		case GL_UNIFORM_SIZE:
			out[i] = float64(l.Size)
		case GL_UNIFORM_BLOCK_INDEX:
			out[i] = float64(l.Block)
		case GL_UNIFORM_OFFSET:
			if l.Block >= 0 {
				out[i] = float64(l.Offset)
			} else {
				out[i] = -1
			}
		case GL_UNIFORM_ARRAY_STRIDE:
			if l.Block >= 0 && l.Array {
				out[i] = float64(l.Stride)
			} else {
				out[i] = -1
				if l.Block < 0 {
					out[i] = -1
				} else {
					out[i] = 0
				}
			}
		case GL_UNIFORM_MATRIX_STRIDE:
			if l.Block >= 0 && l.Cols > 1 {
				out[i] = 16
			} else if l.Block >= 0 {
				out[i] = 0
			} else {
				out[i] = -1
			}
		case GL_UNIFORM_IS_ROW_MAJOR:
			out[i] = 0
		default:
			c.setErr(GL_INVALID_ENUM)
			return nil, false
		}
	}
	return out, true
}

func (c *Context) GetFragDataLocation(pid uint32, name string) int {
	p := c.progs[pid]
	if p == nil {
		c.setErr(GL_INVALID_VALUE)
		return -1
	}
	if !p.Linked {
		c.setErr(GL_INVALID_OPERATION)
		return -1
	}
	for _, o := range p.fs.Outputs {
		if o.Name == name {
			return o.Loc
		}
	}
	return -1
}

// GetInternalformatParameter returns supported sample counts.
func (c *Context) GetInternalformatParameter(target, internal, pname uint32) ([]int32, bool) {
	if target != GL_RENDERBUFFER {
		c.setErr(GL_INVALID_ENUM)
		return nil, false
	}
	f := formats[internal]
	if f == nil {
		c.setErr(GL_INVALID_ENUM)
		return nil, false
	}
	if pname != GL_SAMPLES {
		c.setErr(GL_INVALID_ENUM)
		return nil, false
	}
	if f.Integer || f.IsColor() && !f.ColorRend || f.IsColor() && !c.colorRenderable(f) {
		return []int32{}, true
	}
	return []int32{4}, true
}

// GetUniformValues helps the host decode GetUniform results.
func f32from(u uint32) float32 { return f32frombits(u) }
