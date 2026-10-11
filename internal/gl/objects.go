package gl

import (
	"math"
)

// ---- buffers -----------------------------------------------------------------

func (c *Context) CreateBuffer() uint32 {
	id := c.newID()
	c.buffers[id] = &Buffer{ID: id, Usage: GL_STATIC_DRAW}
	return id
}

func (c *Context) IsBuffer(id uint32) bool {
	b := c.buffers[id]
	return b != nil && b.Target != 0
}

func (c *Context) DeleteBuffer(id uint32) {
	b := c.buffers[id]
	if b == nil {
		return
	}
	b.deleted = true
	for _, v := range c.vaos {
		v.unbindBuf(b)
	}
	c.defVAO.unbindBuf(b)
	if c.arrayBuf == b {
		c.arrayBuf = nil
	}
	if c.copyRead == b {
		c.copyRead = nil
	}
	if c.copyWrite == b {
		c.copyWrite = nil
	}
	if c.packBuf == b {
		c.packBuf = nil
	}
	if c.unpackBuf == b {
		c.unpackBuf = nil
	}
	if c.uboGeneric == b {
		c.uboGeneric = nil
	}
	if c.tfBuf == b {
		c.tfBuf = nil
	}
	for i := range c.ubo {
		if c.ubo[i].Buf == b {
			c.ubo[i] = bufBinding{}
		}
	}
	for _, t := range c.tfs {
		for i := range t.Bufs {
			if t.Bufs[i].Buf == b {
				t.Bufs[i] = bufBinding{}
			}
		}
	}
	for i := range c.tfBufGen {
		if c.tfBufGen[i].Buf == b {
			c.tfBufGen[i] = bufBinding{}
		}
	}
	delete(c.buffers, id)
}

func (v *VAO) unbindBuf(b *Buffer) {
	for i := range v.Attribs {
		if v.Attribs[i].Buf == b {
			v.Attribs[i].Buf = nil
		}
	}
	if v.Element == b {
		v.Element = nil
	}
}

func (c *Context) bufSlot(target uint32) **Buffer {
	switch target {
	case GL_ARRAY_BUFFER:
		return &c.arrayBuf
	case GL_ELEMENT_ARRAY_BUFFER:
		return &c.vao.Element
	}
	if c.Version == 2 {
		switch target {
		case GL_COPY_READ_BUFFER:
			return &c.copyRead
		case GL_COPY_WRITE_BUFFER:
			return &c.copyWrite
		case GL_PIXEL_PACK_BUFFER:
			return &c.packBuf
		case GL_PIXEL_UNPACK_BUFFER:
			return &c.unpackBuf
		case GL_UNIFORM_BUFFER:
			return &c.uboGeneric
		case GL_TRANSFORM_FEEDBACK_BUFFER:
			return &c.tfBuf
		}
	}
	return nil
}

func (c *Context) BindBuffer(target, id uint32) {
	slot := c.bufSlot(target)
	if slot == nil {
		c.setErr(GL_INVALID_ENUM)
		return
	}
	if id == 0 {
		*slot = nil
		return
	}
	b := c.buffers[id]
	if b == nil {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	if c.Version == 1 || true {
		isElem := target == GL_ELEMENT_ARRAY_BUFFER
		if b.Target != 0 && (b.Target == GL_ELEMENT_ARRAY_BUFFER) != isElem && c.Version == 1 {
			c.setErr(GL_INVALID_OPERATION)
			return
		}
		if b.Target != 0 && c.Version == 2 && ((b.Target == GL_ELEMENT_ARRAY_BUFFER) != isElem) {
			c.setErr(GL_INVALID_OPERATION)
			return
		}
	}
	if b.Target == 0 {
		b.Target = target
	}
	*slot = b
}

func (c *Context) BufferData(target uint32, size int, data []byte, usage uint32) {
	slot := c.bufSlot(target)
	if slot == nil {
		c.setErr(GL_INVALID_ENUM)
		return
	}
	switch usage {
	case GL_STREAM_DRAW, GL_STATIC_DRAW, GL_DYNAMIC_DRAW:
	case GL_STREAM_READ, GL_STREAM_COPY, GL_STATIC_READ, GL_STATIC_COPY, GL_DYNAMIC_READ, GL_DYNAMIC_COPY:
		if c.Version != 2 {
			c.setErr(GL_INVALID_ENUM)
			return
		}
	default:
		c.setErr(GL_INVALID_ENUM)
		return
	}
	b := *slot
	if b == nil {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	if size < 0 {
		c.setErr(GL_INVALID_VALUE)
		return
	}
	if data != nil {
		b.Data = append(b.Data[:0:0], data...)
	} else {
		b.Data = make([]byte, size)
	}
	b.Usage = usage
}

func (c *Context) BufferSubData(target uint32, off int, data []byte) {
	slot := c.bufSlot(target)
	if slot == nil {
		c.setErr(GL_INVALID_ENUM)
		return
	}
	b := *slot
	if b == nil {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	if off < 0 {
		c.setErr(GL_INVALID_VALUE)
		return
	}
	if off+len(data) > len(b.Data) {
		c.setErr(GL_INVALID_VALUE)
		return
	}
	copy(b.Data[off:], data)
}

func (c *Context) CopyBufferSubData(rt, wt uint32, ro, wo, size int) {
	rs, ws := c.bufSlot(rt), c.bufSlot(wt)
	if rs == nil || ws == nil {
		c.setErr(GL_INVALID_ENUM)
		return
	}
	r, w := *rs, *ws
	if r == nil || w == nil {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	if ro < 0 || wo < 0 || size < 0 || ro+size > len(r.Data) || wo+size > len(w.Data) {
		c.setErr(GL_INVALID_VALUE)
		return
	}
	if r == w && ro < wo+size && wo < ro+size {
		c.setErr(GL_INVALID_VALUE)
		return
	}
	copy(w.Data[wo:wo+size], r.Data[ro:ro+size])
}

func (c *Context) GetBufferSubData(target uint32, off int, dst []byte) {
	slot := c.bufSlot(target)
	if slot == nil || *slot == nil {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	b := *slot
	if off < 0 || off+len(dst) > len(b.Data) {
		c.setErr(GL_INVALID_VALUE)
		return
	}
	copy(dst, b.Data[off:])
}

func (c *Context) GetBufferParameter(target, pname uint32) (int, bool) {
	slot := c.bufSlot(target)
	if slot == nil {
		c.setErr(GL_INVALID_ENUM)
		return 0, false
	}
	b := *slot
	if b == nil {
		c.setErr(GL_INVALID_OPERATION)
		return 0, false
	}
	switch pname {
	case GL_BUFFER_SIZE:
		return len(b.Data), true
	case GL_BUFFER_USAGE:
		return int(b.Usage), true
	}
	c.setErr(GL_INVALID_ENUM)
	return 0, false
}

// BindBufferRange binds an indexed buffer target (UNIFORM_BUFFER or
// TRANSFORM_FEEDBACK_BUFFER).
func (c *Context) BindBufferRange(target uint32, index int, id uint32, off, size int) {
	var b *Buffer
	if id != 0 {
		b = c.buffers[id]
		if b == nil {
			c.setErr(GL_INVALID_OPERATION)
			return
		}
	}
	switch target {
	case GL_UNIFORM_BUFFER:
		if index < 0 || index >= len(c.ubo) {
			c.setErr(GL_INVALID_VALUE)
			return
		}
		if b != nil && (off%256 != 0 || off < 0 || size <= 0 || off+size > len(b.Data)) {
			c.setErr(GL_INVALID_VALUE)
			return
		}
		if b != nil && b.Target == 0 {
			b.Target = target
		}
		c.ubo[index] = bufBinding{b, off, size}
		c.uboGeneric = b
	case GL_TRANSFORM_FEEDBACK_BUFFER:
		if index < 0 || index >= 4 {
			c.setErr(GL_INVALID_VALUE)
			return
		}
		if c.tf.Active {
			c.setErr(GL_INVALID_OPERATION)
			return
		}
		if b != nil && b.Target == 0 {
			b.Target = target
		}
		c.tf.Bufs[index] = bufBinding{b, off, size}
		c.tfBuf = b
	default:
		c.setErr(GL_INVALID_ENUM)
	}
}

func (c *Context) BindBufferBase(target uint32, index int, id uint32) {
	var size int
	if id != 0 {
		if b := c.buffers[id]; b != nil {
			size = len(b.Data)
		}
	}
	if target == GL_TRANSFORM_FEEDBACK_BUFFER {
		if index < 0 || index >= 4 {
			c.setErr(GL_INVALID_VALUE)
			return
		}
		var b *Buffer
		if id != 0 {
			b = c.buffers[id]
			if b == nil {
				c.setErr(GL_INVALID_OPERATION)
				return
			}
			if b.Target == 0 {
				b.Target = target
			}
		}
		c.tf.Bufs[index] = bufBinding{b, 0, size}
		c.tfBuf = b
		return
	}
	if target == GL_UNIFORM_BUFFER {
		if index < 0 || index >= len(c.ubo) {
			c.setErr(GL_INVALID_VALUE)
			return
		}
		var b *Buffer
		if id != 0 {
			b = c.buffers[id]
			if b == nil {
				c.setErr(GL_INVALID_OPERATION)
				return
			}
			if b.Target == 0 {
				b.Target = target
			}
		}
		c.ubo[index] = bufBinding{b, 0, size}
		c.uboGeneric = b
		return
	}
	c.setErr(GL_INVALID_ENUM)
}

// ---- VAOs ---------------------------------------------------------------------

func (c *Context) CreateVertexArray() uint32 {
	id := c.newID()
	c.vaos[id] = &VAO{ID: id}
	return id
}

func (c *Context) DeleteVertexArray(id uint32) {
	if v := c.vaos[id]; v != nil {
		if c.vao == v {
			c.vao = c.defVAO
		}
		delete(c.vaos, id)
	}
}

func (c *Context) IsVertexArray(id uint32) bool { return c.vaos[id] != nil }

func (c *Context) BindVertexArray(id uint32) {
	if id == 0 {
		c.vao = c.defVAO
		return
	}
	v := c.vaos[id]
	if v == nil {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	c.vao = v
}

// VertexAttribPointer records an attribute array.
func (c *Context) VertexAttribPointer(idx, size int, typ uint32, norm bool, stride, off int, integer bool) {
	if idx < 0 || idx >= c.Lim.MaxVertexAttribs {
		c.setErr(GL_INVALID_VALUE)
		return
	}
	if size < 1 || size > 4 || stride < 0 || off < 0 {
		c.setErr(GL_INVALID_VALUE)
		return
	}
	ts := 0
	switch typ {
	case GL_BYTE, GL_UNSIGNED_BYTE:
		ts = 1
	case GL_SHORT, GL_UNSIGNED_SHORT:
		ts = 2
	case GL_FLOAT:
		ts = 4
		if integer {
			c.setErr(GL_INVALID_ENUM)
			return
		}
	case GL_INT, GL_UNSIGNED_INT:
		ts = 4
		if c.Version != 2 && !integer {
			c.setErr(GL_INVALID_ENUM)
			return
		}
	case GL_HALF_FLOAT:
		ts = 2
		if c.Version != 2 || integer {
			c.setErr(GL_INVALID_ENUM)
			return
		}
	case GL_FIXED:
		ts = 4
		if c.Version != 2 && false {
			c.setErr(GL_INVALID_ENUM)
		}
	case GL_INT_2_10_10_10_REV, GL_UNSIGNED_INT_2_10_10_10_REV:
		ts = 4
		if c.Version != 2 || size != 4 && false {
			c.setErr(GL_INVALID_ENUM)
			return
		}
	default:
		c.setErr(GL_INVALID_ENUM)
		return
	}
	if c.Version == 1 && stride > 255 {
		c.setErr(GL_INVALID_VALUE)
		return
	}
	if ts > 0 && (stride%ts != 0 || off%ts != 0) {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	if c.arrayBuf == nil && (off != 0 || true) && c.Version == 1 && false {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	if c.arrayBuf == nil {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	a := &c.vao.Attribs[idx]
	a.Size, a.Type, a.Normalized, a.Stride, a.Offset, a.Buf, a.Integer = size, typ, norm, stride, off, c.arrayBuf, integer
}

func (c *Context) EnableVertexAttribArray(idx int, on bool) {
	if idx < 0 || idx >= c.Lim.MaxVertexAttribs {
		c.setErr(GL_INVALID_VALUE)
		return
	}
	c.vao.Attribs[idx].Enabled = on
}

func (c *Context) VertexAttribDivisor(idx, d int) {
	if idx < 0 || idx >= c.Lim.MaxVertexAttribs {
		c.setErr(GL_INVALID_VALUE)
		return
	}
	c.vao.Attribs[idx].Divisor = d
}

// VertexAttrib sets a generic attribute value (kind GL_FLOAT / GL_INT / GL_UNSIGNED_INT).
func (c *Context) VertexAttrib(idx int, kind uint32, v [4]uint32) {
	if idx < 0 || idx >= c.Lim.MaxVertexAttribs {
		c.setErr(GL_INVALID_VALUE)
		return
	}
	c.generic[idx] = v
	c.genericTyp[idx] = kind
}

// ---- renderbuffers -----------------------------------------------------------

func (c *Context) CreateRenderbuffer() uint32 {
	id := c.newID()
	c.rbs[id] = &Renderbuffer{ID: id, Internal: GL_RGBA4}
	return id
}

func (c *Context) IsRenderbuffer(id uint32) bool {
	return c.rbs[id] != nil && c.rbs[id].ID != 0 && c.rbs[id].Img != nil || c.rbs[id] != nil
}

func (c *Context) DeleteRenderbuffer(id uint32) {
	r := c.rbs[id]
	if r == nil {
		return
	}
	if c.rb == r {
		c.rb = nil
	}
	for _, f := range c.fbs {
		f.detachRB(r)
	}
	delete(c.rbs, id)
}

func (f *Framebuffer) detachRB(r *Renderbuffer) {
	for i := range f.Color {
		if f.Color[i].RB == r {
			f.Color[i] = Attach{}
		}
	}
	if f.Depth.RB == r {
		f.Depth = Attach{}
	}
	if f.Stencil.RB == r {
		f.Stencil = Attach{}
	}
}

func (c *Context) BindRenderbuffer(target, id uint32) {
	if target != GL_RENDERBUFFER {
		c.setErr(GL_INVALID_ENUM)
		return
	}
	if id == 0 {
		c.rb = nil
		return
	}
	r := c.rbs[id]
	if r == nil {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	c.rb = r
}

func (c *Context) RenderbufferStorage(target, internal uint32, w, h int, samples int) {
	if target != GL_RENDERBUFFER {
		c.setErr(GL_INVALID_ENUM)
		return
	}
	r := c.rb
	if r == nil {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	var f *Format
	if c.Version == 1 {
		switch internal {
		case GL_RGBA4, GL_RGB565, GL_RGB5_A1, GL_DEPTH_COMPONENT16, GL_STENCIL_INDEX8:
			f = formats[internal]
		case GL_DEPTH_STENCIL:
			f = formats[GL_DEPTH24_STENCIL8]
		case GL_SRGB8_ALPHA8:
			if c.Ext["EXT_sRGB"] {
				f = formats[internal]
			}
		case GL_RGBA32F, GL_RGBA16F, GL_RGB16F:
			if c.Ext["WEBGL_color_buffer_float"] || c.Ext["EXT_color_buffer_half_float"] {
				f = formats[internal]
			}
		case GL_RGBA8, GL_RGB8:
			// not in WebGL1 core
		}
	} else {
		f = formats[internal]
		if f != nil && f.IsColor() && f.Class == ClsF32 && !f.Snorm && !c.Ext["EXT_color_buffer_float"] && !(f.Half && c.Ext["EXT_color_buffer_half_float"]) {
			f = nil
		}
		if f != nil && f.IsColor() && !f.ColorRend {
			f = nil
		}
		if f != nil && (f.Internal == GL_LUMINANCE || f.Internal == GL_ALPHA || f.Internal == GL_LUMINANCE_ALPHA) {
			f = nil
		}
	}
	if f == nil {
		c.setErr(GL_INVALID_ENUM)
		return
	}
	if w < 0 || h < 0 || w > c.Lim.MaxRenderbufferSize || h > c.Lim.MaxRenderbufferSize || samples < 0 || samples > c.Lim.MaxSamples {
		c.setErr(GL_INVALID_VALUE)
		return
	}
	if f.Integer && samples > 0 {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	if samples > 0 {
		samples = 4
	}
	r.Internal, r.W, r.H, r.Samples = internal, w, h, samples
	if w == 0 || h == 0 {
		r.Img = nil
		return
	}
	r.Img = NewImage(f, w, h, 1, max(samples, 1))
	if f.IsDepth() {
		for i := range r.Img.F {
			r.Img.F[i] = 1
		}
	}
}

func (c *Context) GetRenderbufferParameter(target, pname uint32) (int, bool) {
	if target != GL_RENDERBUFFER {
		c.setErr(GL_INVALID_ENUM)
		return 0, false
	}
	r := c.rb
	if r == nil {
		c.setErr(GL_INVALID_OPERATION)
		return 0, false
	}
	f := formats[r.Internal]
	if r.Img != nil {
		f = r.Img.Fmt
	}
	switch pname {
	case GL_RENDERBUFFER_WIDTH:
		return r.W, true
	case GL_RENDERBUFFER_HEIGHT:
		return r.H, true
	case GL_RENDERBUFFER_INTERNAL_FORMAT:
		return int(r.Internal), true
	case GL_RENDERBUFFER_SAMPLES:
		if c.Version == 2 {
			return r.Samples, true
		}
	case GL_RENDERBUFFER_RED_SIZE:
		return f.Bits[0], true
	case GL_RENDERBUFFER_GREEN_SIZE:
		return f.Bits[1], true
	case GL_RENDERBUFFER_BLUE_SIZE:
		return f.Bits[2], true
	case GL_RENDERBUFFER_ALPHA_SIZE:
		return f.Bits[3], true
	case GL_RENDERBUFFER_DEPTH_SIZE:
		return f.DBits, true
	case GL_RENDERBUFFER_STENCIL_SIZE:
		return f.SBits, true
	}
	c.setErr(GL_INVALID_ENUM)
	return 0, false
}

// ---- samplers / queries / sync -------------------------------------------------

func (c *Context) CreateSampler() uint32 {
	id := c.newID()
	c.samplers[id] = &SamplerObj{ID: id, P: SamplerParams{MinF: GL_NEAREST_MIPMAP_LINEAR, MagF: GL_LINEAR, WrapS: GL_REPEAT, WrapT: GL_REPEAT, WrapR: GL_REPEAT, MinLod: -1000, MaxLod: 1000, CmpMode: GL_NONE, CmpFunc: GL_LEQUAL}}
	return id
}

func (c *Context) DeleteSampler(id uint32) {
	if s := c.samplers[id]; s != nil {
		for i := range c.units {
			if c.units[i].sampler == s {
				c.units[i].sampler = nil
			}
		}
		delete(c.samplers, id)
	}
}

func (c *Context) IsSampler(id uint32) bool { return c.samplers[id] != nil }

func (c *Context) BindSampler(unit int, id uint32) {
	if unit < 0 || unit >= c.Lim.MaxCombinedTexUnits {
		c.setErr(GL_INVALID_VALUE)
		return
	}
	if id == 0 {
		c.units[unit].sampler = nil
		return
	}
	s := c.samplers[id]
	if s == nil {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	c.units[unit].sampler = s
}

func (c *Context) SamplerParameter(id, pname uint32, f float32) {
	s := c.samplers[id]
	if s == nil {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	v := uint32(f)
	switch pname {
	case GL_TEXTURE_MIN_FILTER:
		if !validMin(v) {
			c.setErr(GL_INVALID_ENUM)
			return
		}
		s.P.MinF = v
	case GL_TEXTURE_MAG_FILTER:
		if v != GL_NEAREST && v != GL_LINEAR {
			c.setErr(GL_INVALID_ENUM)
			return
		}
		s.P.MagF = v
	case GL_TEXTURE_WRAP_S, GL_TEXTURE_WRAP_T, GL_TEXTURE_WRAP_R:
		if !validWrap(v) {
			c.setErr(GL_INVALID_ENUM)
			return
		}
		switch pname {
		case GL_TEXTURE_WRAP_S:
			s.P.WrapS = v
		case GL_TEXTURE_WRAP_T:
			s.P.WrapT = v
		default:
			s.P.WrapR = v
		}
	case GL_TEXTURE_MIN_LOD:
		s.P.MinLod = f
	case GL_TEXTURE_MAX_LOD:
		s.P.MaxLod = f
	case GL_TEXTURE_COMPARE_MODE:
		if v != GL_NONE && v != GL_COMPARE_REF_TO_TEXTURE {
			c.setErr(GL_INVALID_ENUM)
			return
		}
		s.P.CmpMode = v
	case GL_TEXTURE_COMPARE_FUNC:
		if !validFunc(v) {
			c.setErr(GL_INVALID_ENUM)
			return
		}
		s.P.CmpFunc = v
	default:
		c.setErr(GL_INVALID_ENUM)
	}
}

func (c *Context) GetSamplerParameter(id, pname uint32) (float64, bool) {
	s := c.samplers[id]
	if s == nil {
		c.setErr(GL_INVALID_OPERATION)
		return 0, false
	}
	switch pname {
	case GL_TEXTURE_MIN_FILTER:
		return float64(s.P.MinF), true
	case GL_TEXTURE_MAG_FILTER:
		return float64(s.P.MagF), true
	case GL_TEXTURE_WRAP_S:
		return float64(s.P.WrapS), true
	case GL_TEXTURE_WRAP_T:
		return float64(s.P.WrapT), true
	case GL_TEXTURE_WRAP_R:
		return float64(s.P.WrapR), true
	case GL_TEXTURE_MIN_LOD:
		return float64(s.P.MinLod), true
	case GL_TEXTURE_MAX_LOD:
		return float64(s.P.MaxLod), true
	case GL_TEXTURE_COMPARE_MODE:
		return float64(s.P.CmpMode), true
	case GL_TEXTURE_COMPARE_FUNC:
		return float64(s.P.CmpFunc), true
	}
	c.setErr(GL_INVALID_ENUM)
	return 0, false
}

func validMin(v uint32) bool {
	switch v {
	case GL_NEAREST, GL_LINEAR, GL_NEAREST_MIPMAP_NEAREST, GL_LINEAR_MIPMAP_NEAREST, GL_NEAREST_MIPMAP_LINEAR, GL_LINEAR_MIPMAP_LINEAR:
		return true
	}
	return false
}

func validWrap(v uint32) bool {
	return v == GL_REPEAT || v == GL_CLAMP_TO_EDGE || v == GL_MIRRORED_REPEAT
}

func validFunc(v uint32) bool { return v >= GL_NEVER && v <= GL_ALWAYS }

func (c *Context) CreateQuery() uint32 {
	id := c.newID()
	c.queries[id] = &Query{ID: id}
	return id
}

func (c *Context) DeleteQuery(id uint32) {
	if q := c.queries[id]; q != nil {
		if c.activeQ[q.Target] == q {
			delete(c.activeQ, q.Target)
		}
		delete(c.queries, id)
	}
}

func (c *Context) IsQuery(id uint32) bool { q := c.queries[id]; return q != nil && q.Created }

func (c *Context) BeginQuery(target, id uint32) {
	if target != GL_ANY_SAMPLES_PASSED && target != GL_ANY_SAMPLES_PASSED_CONSERVATIVE && target != GL_TRANSFORM_FEEDBACK_PRIMITIVES_WRITTEN {
		c.setErr(GL_INVALID_ENUM)
		return
	}
	q := c.queries[id]
	if q == nil || c.activeQ[target] != nil || (q.Created && q.Target != target) {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	q.Target, q.Created, q.Active, q.Ready, q.Result = target, true, true, false, 0
	c.activeQ[target] = q
}

func (c *Context) EndQuery(target uint32) {
	q := c.activeQ[target]
	if q == nil {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	q.Active = false
	q.pending = true
	delete(c.activeQ, target)
}

func (c *Context) GetQuery(target, pname uint32) (uint32, bool) {
	if pname != GL_CURRENT_QUERY {
		c.setErr(GL_INVALID_ENUM)
		return 0, false
	}
	if target != GL_ANY_SAMPLES_PASSED && target != GL_ANY_SAMPLES_PASSED_CONSERVATIVE && target != GL_TRANSFORM_FEEDBACK_PRIMITIVES_WRITTEN {
		c.setErr(GL_INVALID_ENUM)
		return 0, false
	}
	if q := c.activeQ[target]; q != nil {
		return q.ID, true
	}
	return 0, true
}

func (c *Context) GetQueryParameter(id, pname uint32) (uint64, bool) {
	q := c.queries[id]
	if q == nil || !q.Created || q.Active {
		c.setErr(GL_INVALID_OPERATION)
		return 0, false
	}
	switch pname {
	case GL_QUERY_RESULT_AVAILABLE:
		return uint64(b2i(q.Ready)), true
	case GL_QUERY_RESULT:
		if !q.Ready {
			return 0, true
		}
		if q.Target == GL_TRANSFORM_FEEDBACK_PRIMITIVES_WRITTEN {
			return q.Result, true
		}
		return uint64(b2i(q.Result > 0)), true
	}
	c.setErr(GL_INVALID_ENUM)
	return 0, false
}

func (c *Context) FenceSync() uint32 {
	id := c.newID()
	c.syncs[id] = &Sync{ID: id}
	return id
}

func (c *Context) IsSync(id uint32) bool { return c.syncs[id] != nil }

func (c *Context) DeleteSync(id uint32) { delete(c.syncs, id) }

func (c *Context) ClientWaitSync(id uint32) uint32 {
	s := c.syncs[id]
	if s == nil {
		c.setErr(GL_INVALID_VALUE)
		return GL_WAIT_FAILED
	}
	if !s.Signaled {
		return GL_TIMEOUT_EXPIRED
	}
	return GL_ALREADY_SIGNALED
}

// SyncSignaled reports a fence's status.
func (c *Context) SyncSignaled(id uint32) bool {
	s := c.syncs[id]
	return s != nil && s.Signaled
}

// ---- transform feedback objects ------------------------------------------------

func (c *Context) CreateTransformFeedback() uint32 {
	id := c.newID()
	c.tfs[id] = &TransformFeedback{ID: id}
	return id
}

func (c *Context) DeleteTransformFeedback(id uint32) {
	if t := c.tfs[id]; t != nil {
		if c.tf == t {
			c.tf = c.defTF
		}
		delete(c.tfs, id)
	}
}

func (c *Context) IsTransformFeedback(id uint32) bool { return c.tfs[id] != nil }

func (c *Context) BindTransformFeedback(id uint32) {
	if c.tf.Active && !c.tf.Paused {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	if id == 0 {
		c.tf = c.defTF
		return
	}
	t := c.tfs[id]
	if t == nil {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	c.tf = t
}

func (c *Context) TransformFeedbackVaryings(pid uint32, names []string, mode uint32) {
	p := c.progs[pid]
	if p == nil {
		c.setErr(GL_INVALID_VALUE)
		return
	}
	if mode != GL_INTERLEAVED_ATTRIBS && mode != GL_SEPARATE_ATTRIBS {
		c.setErr(GL_INVALID_ENUM)
		return
	}
	if mode == GL_SEPARATE_ATTRIBS && len(names) > 4 {
		c.setErr(GL_INVALID_VALUE)
		return
	}
	p.tfNames, p.tfMode = names, mode
}

func (c *Context) GetTransformFeedbackVarying(pid uint32, idx int) (string, uint32, int, bool) {
	p := c.progs[pid]
	if p == nil || idx < 0 || idx >= len(p.tfOuts) {
		c.setErr(GL_INVALID_VALUE)
		return "", 0, 0, false
	}
	o := p.tfOuts[idx]
	return o.name, o.typ, o.size, true
}

func (c *Context) BeginTransformFeedback(mode uint32) {
	if c.tf.Active || c.program == nil || len(c.program.tfOuts) == 0 {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	if mode != GL_POINTS && mode != GL_LINES && mode != GL_TRIANGLES {
		c.setErr(GL_INVALID_ENUM)
		return
	}
	n := 1
	if c.program.tfMode == GL_SEPARATE_ATTRIBS {
		n = len(c.program.tfOuts)
	}
	for i := 0; i < n; i++ {
		if c.tf.Bufs[i].Buf == nil {
			c.setErr(GL_INVALID_OPERATION)
			return
		}
	}
	c.tf.Active, c.tf.Paused, c.tf.Mode, c.tf.Written = true, false, mode, 0
	c.tfWritePos = [4]int{}
	for i := range c.tfWritePos {
		c.tfWritePos[i] = c.tf.Bufs[i].Off
	}
}

func (c *Context) EndTransformFeedback() {
	if !c.tf.Active {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	c.tf.Active, c.tf.Paused = false, false
}

func (c *Context) PauseTransformFeedback() {
	if !c.tf.Active || c.tf.Paused {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	c.tf.Paused = true
}

func (c *Context) ResumeTransformFeedback() {
	if !c.tf.Active || !c.tf.Paused {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	c.tf.Paused = false
}

var _ = math.Pi
