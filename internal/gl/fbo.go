package gl

import (
	"image"
)

// rtarget is a resolved framebuffer ready for rendering.
type rtarget struct {
	W, H, S  int
	colors   [8]*Image
	colorZ   [8]int
	draw     [8]bool
	depth    *Image
	depthZ   int
	stencil  *Image
	stencilZ int
	noAlpha  bool
	maxColor int
	rz       int // layer of the last readColor
}

func (c *Context) colorRenderable(f *Format) bool {
	if !f.IsColor() || !f.ColorRend {
		return false
	}
	if f.Class == ClsF32 && !f.Snorm {
		if c.Version == 2 {
			return c.Ext["EXT_color_buffer_float"] || (f.Half && c.Ext["EXT_color_buffer_half_float"])
		}
		if f.Half {
			return c.Ext["EXT_color_buffer_half_float"] || c.Ext["WEBGL_color_buffer_float"]
		}
		return c.Ext["WEBGL_color_buffer_float"]
	}
	return true
}

// Resize reallocates the default framebuffer (contents are lost).
func (c *Context) Resize(w, h int) {
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	c.W, c.H = w, h
	s := 1
	if c.Attrs.Antialias {
		s = 4
	}
	c.defColor = NewImage(formats[GL_RGBA8], w, h, 1, s)
	c.defDepth = nil
	switch {
	case c.Attrs.Depth && c.Attrs.Stencil:
		c.defDepth = NewImage(formats[GL_DEPTH24_STENCIL8], w, h, 1, s)
	case c.Attrs.Depth:
		c.defDepth = NewImage(formats[GL_DEPTH_COMPONENT24], w, h, 1, s)
	case c.Attrs.Stencil:
		c.defDepth = NewImage(formats[GL_STENCIL_INDEX8], w, h, 1, s)
	}
	if c.defDepth != nil && c.defDepth.F != nil {
		for i := range c.defDepth.F {
			c.defDepth.F[i] = 1
		}
	}
	if !c.Attrs.Alpha {
		for i := 3; i < len(c.defColor.U8); i += 4 {
			c.defColor.U8[i] = 255
		}
	}
	if !c.sized {
		c.viewport = [4]int{0, 0, w, h}
		c.scissor = [4]int{0, 0, w, h}
		c.sized = true
	}
	c.bmMu.Lock()
	c.presented = nil
	c.bmMu.Unlock()
	c.dirty = false
}

// resolveTarget validates a framebuffer and returns its render target.
func (c *Context) resolveTarget(fb *Framebuffer) (*rtarget, uint32) {
	rt := &rtarget{}
	if fb == nil || fb.isDefault {
		rt.W, rt.H, rt.S = c.W, c.H, c.defColor.S
		rt.colors[0] = c.defColor
		rt.draw[0] = c.defFBDrawEnabled()
		rt.depth, rt.stencil = c.defDepth, c.defDepth
		rt.noAlpha = !c.Attrs.Alpha
		rt.maxColor = 1
		return rt, GL_FRAMEBUFFER_COMPLETE
	}
	have := false
	sizeSet := false
	setSize := func(w, h, s int) bool {
		if !sizeSet {
			rt.W, rt.H, rt.S = w, h, s
			sizeSet = true
			return true
		}
		if rt.W != w || rt.H != h {
			return false
		}
		if rt.S != s {
			return false
		}
		return true
	}
	imgOf := func(a *Attach) (*Image, int, uint32) {
		switch a.Kind {
		case 1:
			if a.Tex == nil {
				return nil, 0, GL_FRAMEBUFFER_INCOMPLETE_ATTACHMENT
			}
			im := a.Tex.levelImg(a.Face, a.Level)
			if im == nil || im.W == 0 || im.H == 0 {
				return nil, 0, GL_FRAMEBUFFER_INCOMPLETE_ATTACHMENT
			}
			if a.Layer >= im.D {
				return nil, 0, GL_FRAMEBUFFER_INCOMPLETE_ATTACHMENT
			}
			return im, a.Layer, 0
		case 2:
			if a.RB == nil || a.RB.Img == nil {
				return nil, 0, GL_FRAMEBUFFER_INCOMPLETE_ATTACHMENT
			}
			return a.RB.Img, 0, 0
		}
		return nil, 0, 0
	}
	sampleMismatch := false
	for i := 0; i < 8; i++ {
		a := &fb.Color[i]
		if a.Kind == 0 {
			continue
		}
		im, z, e := imgOf(a)
		if e != 0 {
			return rt, e
		}
		if !c.colorRenderable(im.Fmt) {
			return rt, GL_FRAMEBUFFER_INCOMPLETE_ATTACHMENT
		}
		if !setSize(im.W, im.H, im.S) {
			if rt.W == im.W && rt.H == im.H {
				sampleMismatch = true
			} else {
				return rt, GL_FRAMEBUFFER_INCOMPLETE_DIMENSIONS
			}
		}
		rt.colors[i], rt.colorZ[i] = im, z
		rt.draw[i] = fb.DrawBuffers[i] == uint32(GL_COLOR_ATTACHMENT0+i)
		rt.maxColor = i + 1
		have = true
	}
	if fb.Depth.Kind != 0 {
		im, z, e := imgOf(&fb.Depth)
		if e != 0 {
			return rt, e
		}
		if !im.Fmt.IsDepth() {
			return rt, GL_FRAMEBUFFER_INCOMPLETE_ATTACHMENT
		}
		if !setSize(im.W, im.H, im.S) {
			if rt.W == im.W && rt.H == im.H {
				sampleMismatch = true
			} else {
				return rt, GL_FRAMEBUFFER_INCOMPLETE_DIMENSIONS
			}
		}
		rt.depth, rt.depthZ = im, z
		have = true
	}
	if fb.Stencil.Kind != 0 {
		im, z, e := imgOf(&fb.Stencil)
		if e != 0 {
			return rt, e
		}
		if !im.Fmt.IsStencil() {
			return rt, GL_FRAMEBUFFER_INCOMPLETE_ATTACHMENT
		}
		if !setSize(im.W, im.H, im.S) {
			if rt.W == im.W && rt.H == im.H {
				sampleMismatch = true
			} else {
				return rt, GL_FRAMEBUFFER_INCOMPLETE_DIMENSIONS
			}
		}
		rt.stencil, rt.stencilZ = im, z
		have = true
	}
	if rt.depth != nil && rt.stencil != nil && rt.depth != rt.stencil {
		return rt, GL_FRAMEBUFFER_UNSUPPORTED
	}
	if !have {
		return rt, GL_FRAMEBUFFER_INCOMPLETE_MISSING_ATTACHMENT
	}
	if sampleMismatch {
		return rt, GL_FRAMEBUFFER_INCOMPLETE_MULTISAMPLE
	}
	if rt.W == 0 || rt.H == 0 {
		return rt, GL_FRAMEBUFFER_INCOMPLETE_ATTACHMENT
	}
	return rt, GL_FRAMEBUFFER_COMPLETE
}

func (c *Context) defFBDrawEnabled() bool {
	return c.defFB.DrawBuffers[0] != GL_NONE
}

// readColor returns the read-buffer image of the framebuffer, or nil.
func (rt *rtarget) readColor(fb *Framebuffer) *Image {
	if fb == nil || fb.isDefault {
		if fb != nil && fb.ReadBuffer == GL_NONE {
			return nil
		}
		return rt.colors[0]
	}
	rb := fb.ReadBuffer
	if rb < GL_COLOR_ATTACHMENT0 || rb >= GL_COLOR_ATTACHMENT0+8 {
		return nil
	}
	i := int(rb - GL_COLOR_ATTACHMENT0)
	if rt.colors[i] != nil {
		rt.rz = rt.colorZ[i]
	}
	return rt.colors[i]
}

// resolvePixel averages the samples of a pixel.
func (rt *rtarget) resolvePixel(im *Image, x, y, z int) ([4]float32, [4]int32) {
	f := im.Fmt
	if f.Class == ClsInt {
		i := im.idxS(x, y, z+rt.rz, 0)
		var r [4]int32
		copy(r[:], im.I[i:i+4])
		return [4]float32{float32(r[0]), float32(r[1]), float32(r[2]), float32(r[3])}, r
	}
	var acc [4]float32
	for s := 0; s < im.S; s++ {
		i := im.idxS(x, y, z+rt.rz, s)
		switch f.Class {
		case ClsU8:
			for k := 0; k < 4; k++ {
				acc[k] += float32(im.U8[i+k]) / 255
			}
		case ClsF32:
			for k := 0; k < 4; k++ {
				acc[k] += im.F[i+k]
			}
		}
	}
	n := float32(im.S)
	for k := 0; k < 4; k++ {
		acc[k] /= n
	}
	return acc, [4]int32{}
}

// ---- framebuffer objects API --------------------------------------------------

func (c *Context) CreateFramebuffer() uint32 {
	id := c.newID()
	f := &Framebuffer{ID: id, ReadBuffer: GL_COLOR_ATTACHMENT0}
	f.DrawBuffers[0] = GL_COLOR_ATTACHMENT0
	c.fbs[id] = f
	return id
}

func (c *Context) IsFramebuffer(id uint32) bool { return c.fbs[id] != nil }

func (c *Context) DeleteFramebuffer(id uint32) {
	f := c.fbs[id]
	if f == nil {
		return
	}
	if c.readFB == f {
		c.readFB = c.defFB
	}
	if c.drawFB == f {
		c.drawFB = c.defFB
	}
	delete(c.fbs, id)
}

func (c *Context) BindFramebuffer(target, id uint32) {
	if target != GL_FRAMEBUFFER && !(c.Version == 2 && (target == GL_READ_FRAMEBUFFER || target == GL_DRAW_FRAMEBUFFER)) {
		c.setErr(GL_INVALID_ENUM)
		return
	}
	fb := c.defFB
	if id != 0 {
		fb = c.fbs[id]
		if fb == nil {
			c.setErr(GL_INVALID_OPERATION)
			return
		}
	}
	if target != GL_DRAW_FRAMEBUFFER {
		c.readFB = fb
	}
	if target != GL_READ_FRAMEBUFFER {
		c.drawFB = fb
	}
}

func (c *Context) fbForTarget(target uint32) (*Framebuffer, bool) {
	switch target {
	case GL_FRAMEBUFFER, GL_DRAW_FRAMEBUFFER:
		if target != GL_FRAMEBUFFER && c.Version != 2 {
			break
		}
		return c.drawFB, true
	case GL_READ_FRAMEBUFFER:
		if c.Version == 2 {
			return c.readFB, true
		}
	}
	c.setErr(GL_INVALID_ENUM)
	return nil, false
}

func (c *Context) attachSlot(fb *Framebuffer, att uint32) *Attach {
	switch {
	case att >= GL_COLOR_ATTACHMENT0 && att < GL_COLOR_ATTACHMENT0+uint32(c.maxColorAttach()):
		return &fb.Color[att-GL_COLOR_ATTACHMENT0]
	case att == GL_DEPTH_ATTACHMENT:
		return &fb.Depth
	case att == GL_STENCIL_ATTACHMENT:
		return &fb.Stencil
	}
	return nil
}

func (c *Context) FramebufferTexture2D(target, att, textarget, id uint32, level int) {
	fb, ok := c.fbForTarget(target)
	if !ok {
		return
	}
	if fb.isDefault {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	var t *Texture
	face := 0
	if id != 0 {
		t = c.textures[id]
		if t == nil {
			c.setErr(GL_INVALID_OPERATION)
			return
		}
		switch {
		case textarget == GL_TEXTURE_2D && t.Target == GL_TEXTURE_2D:
		case textarget >= GL_TEXTURE_CUBE_MAP_POSITIVE_X && textarget <= GL_TEXTURE_CUBE_MAP_NEGATIVE_Z && t.Target == GL_TEXTURE_CUBE_MAP:
			face = int(textarget - GL_TEXTURE_CUBE_MAP_POSITIVE_X)
		case textarget == GL_TEXTURE_2D || (textarget >= GL_TEXTURE_CUBE_MAP_POSITIVE_X && textarget <= GL_TEXTURE_CUBE_MAP_NEGATIVE_Z):
			c.setErr(GL_INVALID_OPERATION)
			return
		default:
			c.setErr(GL_INVALID_ENUM)
			return
		}
		if level < 0 || c.Version == 1 && level != 0 && !c.Ext["OES_fbo_render_mipmap"] {
			c.setErr(GL_INVALID_VALUE)
			return
		}
	}
	c.setAttach(fb, att, Attach{Kind: b2u8(t != nil), Tex: t, Face: face, Level: level})
}

func b2u8(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}

func (c *Context) setAttach(fb *Framebuffer, att uint32, a Attach) {
	if att == GL_DEPTH_STENCIL_ATTACHMENT {
		fb.Depth, fb.Stencil = a, a
		return
	}
	slot := c.attachSlot(fb, att)
	if slot == nil {
		c.setErr(GL_INVALID_ENUM)
		return
	}
	if a.Kind == 0 {
		*slot = Attach{}
		return
	}
	*slot = a
}

func (c *Context) FramebufferTextureLayer(target, att, id uint32, level, layer int) {
	if c.Version != 2 {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	fb, ok := c.fbForTarget(target)
	if !ok {
		return
	}
	if fb.isDefault {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	var t *Texture
	if id != 0 {
		t = c.textures[id]
		if t == nil || (t.Target != GL_TEXTURE_3D && t.Target != GL_TEXTURE_2D_ARRAY) {
			c.setErr(GL_INVALID_OPERATION)
			return
		}
	}
	if level < 0 || layer < 0 {
		c.setErr(GL_INVALID_VALUE)
		return
	}
	c.setAttach(fb, att, Attach{Kind: b2u8(t != nil), Tex: t, Level: level, Layer: layer})
}

func (c *Context) FramebufferRenderbuffer(target, att, rbTarget, id uint32) {
	fb, ok := c.fbForTarget(target)
	if !ok {
		return
	}
	if rbTarget != GL_RENDERBUFFER {
		c.setErr(GL_INVALID_ENUM)
		return
	}
	if fb.isDefault {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	var r *Renderbuffer
	if id != 0 {
		r = c.rbs[id]
		if r == nil {
			c.setErr(GL_INVALID_OPERATION)
			return
		}
	}
	c.setAttach(fb, att, Attach{Kind: func() uint8 {
		if r != nil {
			return 2
		}
		return 0
	}(), RB: r})
}

func (c *Context) CheckFramebufferStatus(target uint32) uint32 {
	fb, ok := c.fbForTarget(target)
	if !ok {
		return 0
	}
	_, st := c.resolveTarget(fb)
	if c.Version == 1 {
		// WebGL1: depth+stencil must use DEPTH_STENCIL_ATTACHMENT
		if !fb.isDefault && fb.Depth.Kind != 0 && fb.Stencil.Kind != 0 && st == GL_FRAMEBUFFER_COMPLETE && fb.Depth != fb.Stencil {
			return GL_FRAMEBUFFER_UNSUPPORTED
		}
	}
	return st
}

func (c *Context) GetFramebufferAttachmentParameter(target, att, pname uint32) (int, bool) {
	fb, ok := c.fbForTarget(target)
	if !ok {
		return 0, false
	}
	if fb.isDefault {
		if c.Version == 1 {
			c.setErr(GL_INVALID_OPERATION)
			return 0, false
		}
		var f *Format
		switch att {
		case GL_BACK:
			f = formats[GL_RGBA8]
		case GL_DEPTH:
			if c.defDepth != nil && c.defDepth.Fmt.IsDepth() {
				f = c.defDepth.Fmt
			}
		case GL_STENCIL:
			if c.defDepth != nil && c.defDepth.Fmt.IsStencil() {
				f = c.defDepth.Fmt
			}
		default:
			c.setErr(GL_INVALID_ENUM)
			return 0, false
		}
		switch pname {
		case GL_FRAMEBUFFER_ATTACHMENT_OBJECT_TYPE:
			if f == nil {
				return int(GL_NONE), true
			}
			return int(GL_FRAMEBUFFER_DEFAULT), true
		case GL_FRAMEBUFFER_ATTACHMENT_RED_SIZE:
			if f != nil {
				return f.Bits[0], true
			}
		case GL_FRAMEBUFFER_ATTACHMENT_GREEN_SIZE:
			if f != nil {
				return f.Bits[1], true
			}
		case GL_FRAMEBUFFER_ATTACHMENT_BLUE_SIZE:
			if f != nil {
				return f.Bits[2], true
			}
		case GL_FRAMEBUFFER_ATTACHMENT_ALPHA_SIZE:
			if f != nil {
				if att == GL_BACK && !c.Attrs.Alpha {
					return 0, true
				}
				return f.Bits[3], true
			}
		case GL_FRAMEBUFFER_ATTACHMENT_DEPTH_SIZE:
			if f != nil {
				return f.DBits, true
			}
		case GL_FRAMEBUFFER_ATTACHMENT_STENCIL_SIZE:
			if f != nil {
				return f.SBits, true
			}
		case GL_FRAMEBUFFER_ATTACHMENT_COMPONENT_TYPE:
			return int(GL_UNSIGNED_NORMALIZED), true
		case GL_FRAMEBUFFER_ATTACHMENT_COLOR_ENCODING:
			return int(GL_LINEAR), true
		}
		if f == nil {
			return 0, true
		}
		c.setErr(GL_INVALID_ENUM)
		return 0, false
	}
	var a *Attach
	if att == GL_DEPTH_STENCIL_ATTACHMENT {
		if fb.Depth != fb.Stencil && !(fb.Depth.Kind == fb.Stencil.Kind && fb.Depth.Tex == fb.Stencil.Tex && fb.Depth.RB == fb.Stencil.RB) {
			c.setErr(GL_INVALID_OPERATION)
			return 0, false
		}
		a = &fb.Depth
	} else {
		a = c.attachSlot(fb, att)
	}
	if a == nil {
		c.setErr(GL_INVALID_ENUM)
		return 0, false
	}
	switch pname {
	case GL_FRAMEBUFFER_ATTACHMENT_OBJECT_TYPE:
		switch a.Kind {
		case 1:
			return int(GL_TEXTURE), true
		case 2:
			return int(GL_RENDERBUFFER), true
		}
		return int(GL_NONE), true
	case GL_FRAMEBUFFER_ATTACHMENT_OBJECT_NAME:
		switch a.Kind {
		case 1:
			return int(a.Tex.ID), true
		case 2:
			return int(a.RB.ID), true
		}
		c.setErr(GL_INVALID_ENUM)
		return 0, false
	case GL_FRAMEBUFFER_ATTACHMENT_TEXTURE_LEVEL:
		if a.Kind == 1 {
			return a.Level, true
		}
	case GL_FRAMEBUFFER_ATTACHMENT_TEXTURE_CUBE_MAP_FACE:
		if a.Kind == 1 {
			if a.Tex.Target == GL_TEXTURE_CUBE_MAP {
				return int(GL_TEXTURE_CUBE_MAP_POSITIVE_X) + a.Face, true
			}
			return 0, true
		}
	case GL_FRAMEBUFFER_ATTACHMENT_TEXTURE_LAYER:
		if a.Kind == 1 && c.Version == 2 {
			return a.Layer, true
		}
	}
	var f *Format
	switch a.Kind {
	case 1:
		if im := a.Tex.levelImg(a.Face, a.Level); im != nil {
			f = im.Fmt
		}
	case 2:
		if a.RB.Img != nil {
			f = a.RB.Img.Fmt
		}
	}
	if f != nil {
		switch pname {
		case GL_FRAMEBUFFER_ATTACHMENT_RED_SIZE:
			return f.Bits[0], true
		case GL_FRAMEBUFFER_ATTACHMENT_GREEN_SIZE:
			return f.Bits[1], true
		case GL_FRAMEBUFFER_ATTACHMENT_BLUE_SIZE:
			return f.Bits[2], true
		case GL_FRAMEBUFFER_ATTACHMENT_ALPHA_SIZE:
			return f.Bits[3], true
		case GL_FRAMEBUFFER_ATTACHMENT_DEPTH_SIZE:
			return f.DBits, true
		case GL_FRAMEBUFFER_ATTACHMENT_STENCIL_SIZE:
			return f.SBits, true
		case GL_FRAMEBUFFER_ATTACHMENT_COMPONENT_TYPE:
			switch {
			case f.Integer && f.Unsigned:
				return int(GL_UNSIGNED_INT), true
			case f.Integer:
				return int(GL_INT), true
			case f.Class == ClsF32 && f.Snorm:
				return int(GL_SIGNED_NORMALIZED), true
			case f.Class == ClsF32 && f.Float:
				return int(GL_FLOAT), true
			}
			return int(GL_UNSIGNED_NORMALIZED), true
		case GL_FRAMEBUFFER_ATTACHMENT_COLOR_ENCODING:
			if f.SRGB {
				return int(GL_SRGB), true
			}
			return int(GL_LINEAR), true
		}
	}
	c.setErr(GL_INVALID_ENUM)
	return 0, false
}

func (c *Context) DrawBuffers(bufs []uint32) {
	fb := c.drawFB
	if len(bufs) > c.maxColorAttach() {
		c.setErr(GL_INVALID_VALUE)
		return
	}
	if fb.isDefault {
		if len(bufs) != 1 || bufs[0] != GL_BACK && bufs[0] != GL_NONE {
			c.setErr(GL_INVALID_OPERATION)
			return
		}
		fb.DrawBuffers[0] = bufs[0]
		return
	}
	var nb [8]uint32
	for i, b := range bufs {
		if b != GL_NONE && b != GL_COLOR_ATTACHMENT0+uint32(i) {
			if b >= GL_COLOR_ATTACHMENT0 && b < GL_COLOR_ATTACHMENT0+uint32(c.Lim.MaxColorAttachments) || b == GL_BACK {
				c.setErr(GL_INVALID_OPERATION)
			} else {
				c.setErr(GL_INVALID_ENUM)
			}
			return
		}
		nb[i] = b
	}
	fb.DrawBuffers = nb
}

func (c *Context) ReadBuffer(src uint32) {
	fb := c.readFB
	if fb.isDefault {
		if src != GL_BACK && src != GL_NONE {
			c.setErr(GL_INVALID_OPERATION)
			return
		}
		fb.ReadBuffer = src
		return
	}
	if src != GL_NONE && (src < GL_COLOR_ATTACHMENT0 || src >= GL_COLOR_ATTACHMENT0+uint32(c.Lim.MaxColorAttachments)) {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	fb.ReadBuffer = src
}

// ---- clear -----------------------------------------------------------------------

func (c *Context) scissorRect(rt *rtarget) (x0, y0, x1, y1 int) {
	x0, y0, x1, y1 = 0, 0, rt.W, rt.H
	if c.enScissor {
		x0 = max(x0, c.scissor[0])
		y0 = max(y0, c.scissor[1])
		x1 = min(x1, c.scissor[0]+c.scissor[2])
		y1 = min(y1, c.scissor[1]+c.scissor[3])
	}
	return
}

func (c *Context) clearColorBuffer(rt *rtarget, i int, col [4]float32, ci [4]int32, mask [4]bool) {
	im := rt.colors[i]
	if im == nil {
		return
	}
	x0, y0, x1, y1 := c.scissorRect(rt)
	if rt.noAlpha {
		col[3] = 1
		mask[3] = false
	}
	f := im.Fmt
	var tmp *Image
	switch f.Class {
	case ClsU8:
		tmp = &Image{Fmt: f, U8: make([]uint8, 4), clearConv: true}
		tmp.setFloat(0, col)
	case ClsF32:
		tmp = &Image{Fmt: f, F: make([]float32, 4)}
		tmp.setFloat(0, col)
	}
	z := rt.colorZ[i]
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			base := im.idxS(x, y, z, 0)
			for s := 0; s < im.S; s++ {
				o := base + s*4
				for k := 0; k < 4; k++ {
					if !mask[k] {
						continue
					}
					switch f.Class {
					case ClsU8:
						im.U8[o+k] = tmp.U8[k]
					case ClsF32:
						im.F[o+k] = tmp.F[k]
					case ClsInt:
						im.I[o+k] = ci[k]
					}
				}
			}
		}
	}
}

func (c *Context) clearDepthBuffer(rt *rtarget, d float32) {
	im := rt.depth
	if im == nil || !im.Fmt.IsDepth() || !c.depthMask {
		return
	}
	x0, y0, x1, y1 := c.scissorRect(rt)
	q := quantDepth(im.Fmt, d)
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			base := im.idxS(x, y, rt.depthZ, 0)
			for s := 0; s < im.S; s++ {
				im.F[base+s] = q
			}
		}
	}
}

func (c *Context) clearStencilBuffer(rt *rtarget, v int32, wmask uint32) {
	im := rt.stencil
	if im == nil || im.St == nil {
		return
	}
	x0, y0, x1, y1 := c.scissorRect(rt)
	m := uint8(wmask)
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			base := im.pixS(x, y, rt.stencilZ, 0)
			for s := 0; s < im.S; s++ {
				im.St[base+s] = uint8(v)&m | im.St[base+s]&^m
			}
		}
	}
}

func (c *Context) Clear(mask uint32) {
	if mask&^(GL_COLOR_BUFFER_BIT|GL_DEPTH_BUFFER_BIT|GL_STENCIL_BUFFER_BIT) != 0 {
		c.setErr(GL_INVALID_VALUE)
		return
	}
	rt, st := c.resolveTarget(c.drawFB)
	if st != GL_FRAMEBUFFER_COMPLETE {
		c.setErr(GL_INVALID_FRAMEBUFFER_OPERATION)
		return
	}
	c.markDirty()
	if mask&GL_COLOR_BUFFER_BIT != 0 {
		for i := 0; i < 8; i++ {
			if rt.colors[i] != nil && rt.draw[i] && rt.colors[i].Fmt.Class != ClsInt {
				c.clearColorBuffer(rt, i, c.clearColor, [4]int32{}, c.colorMask)
			} else if rt.colors[i] != nil && rt.draw[i] {
				c.clearColorBuffer(rt, i, c.clearColor, [4]int32{}, c.colorMask)
			}
		}
	}
	if mask&GL_DEPTH_BUFFER_BIT != 0 {
		c.clearDepthBuffer(rt, c.clearDepth)
	}
	if mask&GL_STENCIL_BUFFER_BIT != 0 {
		c.clearStencilBuffer(rt, c.clearStencil, c.stencilF.WMask)
	}
}

// ClearBuffer implements clearBufferfv/iv/uiv/fi. kind: 'f','i','u'.
func (c *Context) ClearBuffer(buffer uint32, drawbuffer int, kind byte, f [4]float32, iv [4]int32) {
	rt, st := c.resolveTarget(c.drawFB)
	if st != GL_FRAMEBUFFER_COMPLETE {
		c.setErr(GL_INVALID_FRAMEBUFFER_OPERATION)
		return
	}
	switch buffer {
	case GL_COLOR:
		if drawbuffer < 0 || drawbuffer >= c.Lim.MaxDrawBuffers {
			c.setErr(GL_INVALID_VALUE)
			return
		}
		im := rt.colors[drawbuffer]
		if im == nil || !rt.draw[drawbuffer] {
			return
		}
		if (kind == 'f') == im.Fmt.Integer {
			c.setErr(GL_INVALID_OPERATION)
			return
		}
		if kind == 'u' && !im.Fmt.Unsigned || kind == 'i' && im.Fmt.Unsigned {
			c.setErr(GL_INVALID_OPERATION)
			return
		}
		c.markDirty()
		c.clearColorBuffer(rt, drawbuffer, f, iv, c.colorMask)
	case GL_DEPTH:
		if kind != 'f' {
			c.setErr(GL_INVALID_ENUM)
			return
		}
		c.markDirty()
		c.clearDepthBuffer(rt, f[0])
	case GL_STENCIL:
		if kind != 'i' {
			c.setErr(GL_INVALID_ENUM)
			return
		}
		c.markDirty()
		c.clearStencilBuffer(rt, iv[0], c.stencilF.WMask)
	default:
		c.setErr(GL_INVALID_ENUM)
	}
}

func (c *Context) markDirty() { c.dirty = true }

// ---- reading -----------------------------------------------------------------------

// ReadPixels reads the read framebuffer into dst (or the pack buffer when bound).
func (c *Context) ReadPixels(x, y, w, h int, format, typ uint32, dst []byte, pboOff int) {
	if w < 0 || h < 0 {
		c.setErr(GL_INVALID_VALUE)
		return
	}
	rt, st := c.resolveTarget(c.readFB)
	if st != GL_FRAMEBUFFER_COMPLETE {
		c.setErr(GL_INVALID_FRAMEBUFFER_OPERATION)
		return
	}
	src := rt.readColor(c.readFB)
	if src == nil {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	f := src.Fmt
	// validate format/type
	okCombo := false
	switch {
	case format == GL_RGBA && typ == GL_UNSIGNED_BYTE && (f.Class == ClsU8 || f.Class == ClsF32 && f.Snorm):
		okCombo = true
	case format == GL_RGBA && typ == GL_FLOAT && f.Class == ClsF32 && !f.Snorm:
		okCombo = true
	case format == GL_RGBA && (typ == GL_HALF_FLOAT || typ == GL_HALF_FLOAT_OES) && f.Class == ClsF32 && f.Half:
		okCombo = c.Version == 2 || typ == GL_HALF_FLOAT_OES && c.Ext["EXT_color_buffer_half_float"]
	case format == GL_RGBA_INTEGER && f.Integer && (typ == GL_INT && !f.Unsigned || typ == GL_UNSIGNED_INT && f.Unsigned):
		okCombo = true
	case c.Version == 2 && !f.Integer && f.Class == ClsU8 && (format == GL_RGBA && typ == GL_UNSIGNED_INT_2_10_10_10_REV && f.Internal == GL_RGB10_A2 ||
		format == GL_RGB && typ == GL_UNSIGNED_BYTE || format == GL_RG && typ == GL_UNSIGNED_BYTE || format == GL_RED && typ == GL_UNSIGNED_BYTE):
		okCombo = true
	case c.Version == 2 && !f.Integer && f.Class == ClsF32 && !f.Snorm && (format == GL_RED || format == GL_RG || format == GL_RGB) && typ == GL_FLOAT:
		okCombo = true
	}
	if !okCombo {
		switch format {
		case GL_RGBA, GL_RGB, GL_ALPHA, GL_LUMINANCE, GL_LUMINANCE_ALPHA, GL_RED, GL_RG, GL_RGBA_INTEGER, GL_RED_INTEGER, GL_RG_INTEGER, GL_RGB_INTEGER:
			if c.Version == 1 && format == GL_RGBA && typ != GL_UNSIGNED_BYTE {
				c.setErr(GL_INVALID_ENUM)
			} else {
				c.setErr(GL_INVALID_OPERATION)
			}
		default:
			c.setErr(GL_INVALID_ENUM)
		}
		return
	}
	pk := c.pixStore.Pack
	need := pk.RequiredBytes(uint32(w), uint32(h), 1, format, typ)
	if c.packBuf != nil && c.Version == 2 {
		if pboOff < 0 || pboOff+need > len(c.packBuf.Data) {
			c.setErr(GL_INVALID_OPERATION)
			return
		}
		dst = c.packBuf.Data[pboOff:]
	} else if need > len(dst) {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	if w == 0 || h == 0 {
		return
	}
	// resolve region into a single-sample image (zero outside)
	tmp := NewImage(f, w, h, 1, 1)
	for yy := 0; yy < h; yy++ {
		for xx := 0; xx < w; xx++ {
			sx, sy := x+xx, y+yy
			if sx < 0 || sy < 0 || sx >= src.W || sy >= src.H {
				continue
			}
			di := tmp.idx(xx, yy, 0)
			if f.Class == ClsU8 && src.S == 1 {
				si := src.idxS(sx, sy, rt.rz, 0)
				copy(tmp.U8[di:di+4], src.U8[si:si+4])
				continue
			}
			px, ri := rt.resolvePixel(src, sx, sy, 0)
			switch f.Class {
			case ClsU8:
				for k := 0; k < 4; k++ {
					tmp.U8[di+k] = unormToByte(px[k])
				}
			case ClsF32:
				copy(tmp.F[di:di+4], px[:])
			case ClsInt:
				copy(tmp.I[di:di+4], ri[:])
			}
		}
	}
	if rt.noAlpha && f.Class == ClsU8 {
		for i := 3; i < len(tmp.U8); i += 4 {
			tmp.U8[i] = 255
		}
	}
	tmp.Download(0, 0, w, h, format, typ, dst, pk)
}

// SnapshotImage resolves the default framebuffer into an RGBA image (top row
// first), honouring alpha and premultipliedAlpha attributes.
func (c *Context) SnapshotImage() *image.RGBA {
	src := c.defColor
	w, h := c.W, c.H
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	s := src.S
	for y := 0; y < h; y++ {
		dy := h - 1 - y
		for x := 0; x < w; x++ {
			var r, g, b, a int
			base := src.idxS(x, y, 0, 0)
			if s == 1 {
				r, g, b, a = int(src.U8[base]), int(src.U8[base+1]), int(src.U8[base+2]), int(src.U8[base+3])
			} else {
				for k := 0; k < s; k++ {
					o := base + k*4
					r += int(src.U8[o])
					g += int(src.U8[o+1])
					b += int(src.U8[o+2])
					a += int(src.U8[o+3])
				}
				r, g, b, a = (r+s/2)/s, (g+s/2)/s, (b+s/2)/s, (a+s/2)/s
			}
			if !c.Attrs.Alpha {
				a = 255
			} else if !c.Attrs.PremultipliedAlpha {
				r, g, b = (r*a+127)/255, (g*a+127)/255, (b*a+127)/255
			}
			if r > a {
				r = a
			}
			if g > a {
				g = a
			}
			if b > a {
				b = a
			}
			o := img.PixOffset(x, dy)
			img.Pix[o], img.Pix[o+1], img.Pix[o+2], img.Pix[o+3] = uint8(r), uint8(g), uint8(b), uint8(a)
		}
	}
	return img
}

// Present snapshots the drawing buffer for the compositor and, unless
// preserveDrawingBuffer is set, clears it.
func (c *Context) Present() {
	for _, sy := range c.syncs {
		sy.Signaled = true
	}
	for _, q := range c.queries {
		if q.pending {
			q.pending, q.Ready = false, true
		}
	}
	if !c.dirty {
		return
	}
	img := c.SnapshotImage()
	c.bmMu.Lock()
	c.presented = img
	c.bmMu.Unlock()
	c.dirty = false
	if !c.Attrs.PreserveDrawingBuffer {
		c.clearDefault()
	}
}

func (c *Context) clearDefault() {
	for i := range c.defColor.U8 {
		c.defColor.U8[i] = 0
	}
	if !c.Attrs.Alpha {
		for i := 3; i < len(c.defColor.U8); i += 4 {
			c.defColor.U8[i] = 255
		}
	}
	if d := c.defDepth; d != nil {
		for i := range d.F {
			d.F[i] = 1
		}
		for i := range d.St {
			d.St[i] = 0
		}
	}
}

// Bitmap returns the current picture for screenshots and drawImage.
func (c *Context) Bitmap() *image.RGBA {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dirty || c.Attrs.PreserveDrawingBuffer {
		return c.SnapshotImage()
	}
	c.bmMu.Lock()
	p := c.presented
	c.bmMu.Unlock()
	if p != nil {
		return p
	}
	return c.SnapshotImage()
}

// ReadExtent returns the size of the read framebuffer's color buffer.
func (c *Context) ReadExtent() (w, h int, ok bool) {
	rt, st := c.resolveTarget(c.readFB)
	if st != GL_FRAMEBUFFER_COMPLETE {
		return 0, 0, false
	}
	src := rt.readColor(c.readFB)
	if src == nil {
		return 0, 0, false
	}
	return src.W, src.H, true
}

// PackLayout returns bytes per pixel, row stride and base offset for a read.
func (c *Context) PackLayout(w int, format, typ uint32) (pb, rowStride, base int) {
	pk := c.pixStore.Pack
	pb = pixelBytes(format, typ)
	rowStride = pk.rowStride(w, pb)
	base = pk.SkipRows*rowStride + pk.SkipPixels*pb
	return
}
