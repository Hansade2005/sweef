package gl

import "math"

func (c *Context) capSlot(cap uint32) *bool {
	switch cap {
	case GL_BLEND:
		return &c.enBlend
	case GL_CULL_FACE:
		return &c.enCull
	case GL_DEPTH_TEST:
		return &c.enDepth
	case GL_DITHER:
		return &c.enDither
	case GL_POLYGON_OFFSET_FILL:
		return &c.enPoly
	case GL_SAMPLE_ALPHA_TO_COVERAGE:
		return &c.enA2C
	case GL_SAMPLE_COVERAGE:
		return &c.enSampleCov
	case GL_SCISSOR_TEST:
		return &c.enScissor
	case GL_STENCIL_TEST:
		return &c.enStencil
	case GL_RASTERIZER_DISCARD:
		if c.Version == 2 {
			return &c.enRDiscard
		}
	}
	return nil
}

func (c *Context) Enable(cap uint32, on bool) {
	s := c.capSlot(cap)
	if s == nil {
		c.setErr(GL_INVALID_ENUM)
		return
	}
	*s = on
}

func (c *Context) IsEnabled(cap uint32) bool {
	s := c.capSlot(cap)
	if s == nil {
		c.setErr(GL_INVALID_ENUM)
		return false
	}
	return *s
}

func (c *Context) Viewport(x, y, w, h int) {
	if w < 0 || h < 0 {
		c.setErr(GL_INVALID_VALUE)
		return
	}
	c.viewport = [4]int{x, y, min(w, c.Lim.MaxViewportDims[0]), min(h, c.Lim.MaxViewportDims[1])}
}

func (c *Context) Scissor(x, y, w, h int) {
	if w < 0 || h < 0 {
		c.setErr(GL_INVALID_VALUE)
		return
	}
	c.scissor = [4]int{x, y, w, h}
}

func (c *Context) DepthRange(n, f float32) {
	if n > f {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	c.depthRange = [2]float32{clampf(n, 0, 1), clampf(f, 0, 1)}
}

func (c *Context) ClearColor(r, g, b, a float32) { c.clearColor = [4]float32{r, g, b, a} }
func (c *Context) ClearDepth(d float32)          { c.clearDepth = clampf(d, 0, 1) }
func (c *Context) ClearStencil(s int32)          { c.clearStencil = s }
func (c *Context) ColorMask(r, g, b, a bool)     { c.colorMask = [4]bool{r, g, b, a} }
func (c *Context) DepthMask(m bool)              { c.depthMask = m }

func (c *Context) DepthFunc(f uint32) {
	if !validFunc(f) {
		c.setErr(GL_INVALID_ENUM)
		return
	}
	c.depthFunc = f
}

func (c *Context) StencilFuncSeparate(face, fn uint32, ref int32, mask uint32) {
	if !validFunc(fn) {
		c.setErr(GL_INVALID_ENUM)
		return
	}
	switch face {
	case GL_FRONT, GL_FRONT_AND_BACK, GL_BACK:
	default:
		c.setErr(GL_INVALID_ENUM)
		return
	}
	if face != GL_BACK {
		c.stencilF.Func, c.stencilF.Ref, c.stencilF.Mask = fn, ref, mask
	}
	if face != GL_FRONT {
		c.stencilB.Func, c.stencilB.Ref, c.stencilB.Mask = fn, ref, mask
	}
}

func validStencilOp(o uint32) bool {
	switch o {
	case GL_KEEP, GL_ZERO, GL_REPLACE, GL_INCR, GL_DECR, GL_INVERT, GL_INCR_WRAP, GL_DECR_WRAP:
		return true
	}
	return false
}

func (c *Context) StencilOpSeparate(face, fail, zfail, zpass uint32) {
	if !validStencilOp(fail) || !validStencilOp(zfail) || !validStencilOp(zpass) {
		c.setErr(GL_INVALID_ENUM)
		return
	}
	switch face {
	case GL_FRONT, GL_FRONT_AND_BACK, GL_BACK:
	default:
		c.setErr(GL_INVALID_ENUM)
		return
	}
	if face != GL_BACK {
		c.stencilF.Fail, c.stencilF.ZFail, c.stencilF.ZPass = fail, zfail, zpass
	}
	if face != GL_FRONT {
		c.stencilB.Fail, c.stencilB.ZFail, c.stencilB.ZPass = fail, zfail, zpass
	}
}

func (c *Context) StencilMaskSeparate(face uint32, m uint32) {
	switch face {
	case GL_FRONT, GL_FRONT_AND_BACK, GL_BACK:
	default:
		c.setErr(GL_INVALID_ENUM)
		return
	}
	if face != GL_BACK {
		c.stencilF.WMask = m
	}
	if face != GL_FRONT {
		c.stencilB.WMask = m
	}
}

func (c *Context) BlendColor(r, g, b, a float32) {
	c.blendColor = [4]float32{clampf(r, 0, 1), clampf(g, 0, 1), clampf(b, 0, 1), clampf(a, 0, 1)}
}

func validBlendEq(e uint32) bool {
	switch e {
	case GL_FUNC_ADD, GL_FUNC_SUBTRACT, GL_FUNC_REVERSE_SUBTRACT:
		return true
	case GL_MIN, GL_MAX:
		return true
	}
	return false
}

func (c *Context) BlendEquationSeparate(rgb, a uint32) {
	for _, e := range []uint32{rgb, a} {
		if !validBlendEq(e) || (e == GL_MIN || e == GL_MAX) && c.Version == 1 && !c.Ext["EXT_blend_minmax"] {
			c.setErr(GL_INVALID_ENUM)
			return
		}
	}
	c.blendEqRGB, c.blendEqA = rgb, a
}

func validBlendFactor(f uint32, dst bool) bool {
	switch f {
	case GL_ZERO, GL_ONE, GL_SRC_COLOR, GL_ONE_MINUS_SRC_COLOR, GL_DST_COLOR, GL_ONE_MINUS_DST_COLOR, GL_SRC_ALPHA, GL_ONE_MINUS_SRC_ALPHA,
		GL_DST_ALPHA, GL_ONE_MINUS_DST_ALPHA, GL_CONSTANT_COLOR, GL_ONE_MINUS_CONSTANT_COLOR, GL_CONSTANT_ALPHA, GL_ONE_MINUS_CONSTANT_ALPHA:
		return true
	case GL_SRC_ALPHA_SATURATE:
		return !dst
	}
	return false
}

func (c *Context) BlendFuncSeparate(sr, dr, sa, da uint32) {
	if !validBlendFactor(sr, false) || !validBlendFactor(sa, false) || !validBlendFactor(dr, true) || !validBlendFactor(da, true) {
		c.setErr(GL_INVALID_ENUM)
		return
	}
	isConst := func(f uint32) bool { return f == GL_CONSTANT_COLOR || f == GL_ONE_MINUS_CONSTANT_COLOR }
	isConstA := func(f uint32) bool { return f == GL_CONSTANT_ALPHA || f == GL_ONE_MINUS_CONSTANT_ALPHA }
	if (isConst(sr) && isConstA(dr)) || (isConstA(sr) && isConst(dr)) || (isConst(dr) && isConstA(sr)) {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	c.blendSrcRGB, c.blendDstRGB, c.blendSrcA, c.blendDstA = sr, dr, sa, da
}

func (c *Context) CullFace(m uint32) {
	if m != GL_FRONT && m != GL_BACK && m != GL_FRONT_AND_BACK {
		c.setErr(GL_INVALID_ENUM)
		return
	}
	c.cullMode = m
}

func (c *Context) FrontFace(m uint32) {
	if m != GL_CW && m != GL_CCW {
		c.setErr(GL_INVALID_ENUM)
		return
	}
	c.frontFace = m
}

func (c *Context) LineWidth(w float32) {
	if !(w > 0) {
		c.setErr(GL_INVALID_VALUE)
		return
	}
	c.lineWidth = w
}

func (c *Context) PolygonOffset(f, u float32) { c.polyFactor, c.polyUnits = f, u }

func (c *Context) SampleCoverage(v float32, inv bool) {
	c.sampleCovVal, c.sampleCovInv = clampf(v, 0, 1), inv
}

func (c *Context) Hint(target, mode uint32) {
	switch mode {
	case GL_DONT_CARE, GL_FASTEST, GL_NICEST:
	default:
		c.setErr(GL_INVALID_ENUM)
		return
	}
	switch target {
	case GL_GENERATE_MIPMAP_HINT:
		c.hints[0] = mode
	case GL_FRAGMENT_SHADER_DERIVATIVE_HINT:
		if c.Version == 1 && !c.Ext["OES_standard_derivatives"] {
			c.setErr(GL_INVALID_ENUM)
			return
		}
		c.hints[1] = mode
	default:
		c.setErr(GL_INVALID_ENUM)
	}
}

// PixelStore sets pack/unpack state.
func (c *Context) PixelStore(pname uint32, v int) {
	ps := &c.pixStore
	switch pname {
	case GL_UNPACK_FLIP_Y_WEBGL:
		ps.FlipY = v != 0
	case GL_UNPACK_PREMULTIPLY_ALPHA_WEBGL:
		ps.Premul = v != 0
	case GL_UNPACK_COLORSPACE_CONVERSION_WEBGL:
		if v != GL_NONE && v != GL_BROWSER_DEFAULT_WEBGL {
			c.setErr(GL_INVALID_ENUM)
			return
		}
		ps.Colorspace = uint32(v)
	case GL_UNPACK_ALIGNMENT, GL_PACK_ALIGNMENT:
		if v != 1 && v != 2 && v != 4 && v != 8 {
			c.setErr(GL_INVALID_VALUE)
			return
		}
		if pname == GL_UNPACK_ALIGNMENT {
			ps.Unpack.Alignment = v
		} else {
			ps.Pack.Alignment = v
		}
	default:
		if c.Version != 2 {
			c.setErr(GL_INVALID_ENUM)
			return
		}
		if v < 0 {
			c.setErr(GL_INVALID_VALUE)
			return
		}
		switch pname {
		case GL_UNPACK_ROW_LENGTH:
			ps.Unpack.RowLength = v
		case GL_UNPACK_IMAGE_HEIGHT:
			ps.Unpack.ImageHeight = v
		case GL_UNPACK_SKIP_ROWS:
			ps.Unpack.SkipRows = v
		case GL_UNPACK_SKIP_PIXELS:
			ps.Unpack.SkipPixels = v
		case GL_UNPACK_SKIP_IMAGES:
			ps.Unpack.SkipImages = v
		case GL_PACK_ROW_LENGTH:
			ps.Pack.RowLength = v
		case GL_PACK_SKIP_ROWS:
			ps.Pack.SkipRows = v
		case GL_PACK_SKIP_PIXELS:
			ps.Pack.SkipPixels = v
		default:
			c.setErr(GL_INVALID_ENUM)
		}
	}
}

// ---- blit --------------------------------------------------------------------------

func (c *Context) BlitFramebuffer(sx0, sy0, sx1, sy1, dx0, dy0, dx1, dy1 int, mask uint32, filter uint32) {
	if c.Version != 2 {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	if filter != GL_NEAREST && filter != GL_LINEAR {
		c.setErr(GL_INVALID_ENUM)
		return
	}
	if mask&^(GL_COLOR_BUFFER_BIT|GL_DEPTH_BUFFER_BIT|GL_STENCIL_BUFFER_BIT) != 0 {
		c.setErr(GL_INVALID_VALUE)
		return
	}
	src, st1 := c.resolveTarget(c.readFB)
	dst, st2 := c.resolveTarget(c.drawFB)
	if st1 != GL_FRAMEBUFFER_COMPLETE || st2 != GL_FRAMEBUFFER_COMPLETE {
		c.setErr(GL_INVALID_FRAMEBUFFER_OPERATION)
		return
	}
	if src.S > 1 && dst.S > 1 && src.S != dst.S {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	scaled := sx1-sx0 != dx1-dx0 || sy1-sy0 != dy1-dy0
	if src.S > 1 && (scaled || sx1-sx0 != dx1-dx0) {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	c.markDirty()
	dw, dh := dx1-dx0, dy1-dy0
	sw, sh := sx1-sx0, sy1-sy0
	if dw == 0 || dh == 0 || sw == 0 || sh == 0 {
		return
	}
	x0, y0, x1, y1 := c.scissorRect(dst)
	// destination bounds iteration
	lox, hix := min(dx0, dx1), max(dx0, dx1)
	loy, hiy := min(dy0, dy1), max(dy0, dy1)
	lox, loy = max(lox, x0), max(loy, y0)
	hix, hiy = min(hix, x1), min(hiy, y1)
	adw, adh := float64(abs(dw)), float64(abs(dh))
	mapX := func(x int) float64 {
		fx := (float64(x) + 0.5 - float64(min(dx0, dx1))) / adw
		if (dx1 < dx0) != (sx1 < sx0) {
			fx = 1 - fx
		}
		return float64(min(sx0, sx1)) + fx*float64(abs(sw))
	}
	mapY := func(y int) float64 {
		fy := (float64(y) + 0.5 - float64(min(dy0, dy1))) / adh
		if (dy1 < dy0) != (sy1 < sy0) {
			fy = 1 - fy
		}
		return float64(min(sy0, sy1)) + fy*float64(abs(sh))
	}
	if mask&GL_COLOR_BUFFER_BIT != 0 {
		simg := src.readColor(c.readFB)
		if simg != nil {
			for bi := 0; bi < 8; bi++ {
				dimg := dst.colors[bi]
				if dimg == nil || !dst.draw[bi] {
					continue
				}
				if dimg.Fmt.Integer != simg.Fmt.Integer {
					c.setErr(GL_INVALID_OPERATION)
					return
				}
				for y := loy; y < hiy; y++ {
					for x := lox; x < hix; x++ {
						fx, fy := mapX(x), mapY(y)
						var px [4]float32
						var pi [4]int32
						if simg.Fmt.Integer || filter == GL_NEAREST || src.S > 1 && dimg.S > 1 {
							ix, iy := int(math.Floor(fx)), int(math.Floor(fy))
							if ix < 0 || iy < 0 || ix >= simg.W || iy >= simg.H {
								continue
							}
							if src.S > 1 && dimg.S > 1 {
								for s := 0; s < dimg.S; s++ {
									si := simg.idxS(ix, iy, src.rz, s)
									di := dimg.idxS(x, y, dst.colorZ[bi], s)
									copySample(dimg, di, simg, si)
								}
								continue
							}
							px, pi = src.resolvePixel(simg, ix, iy, 0)
						} else {
							px = bilinearResolve(src, simg, fx, fy)
						}
						dimg.storePixelAllSamples(dst, x, y, bi, px, pi, c.colorMask)
					}
				}
			}
		}
	}
	if mask&GL_DEPTH_BUFFER_BIT != 0 && src.depth != nil && dst.depth != nil && src.depth.Fmt.IsDepth() && dst.depth.Fmt.IsDepth() {
		for y := loy; y < hiy; y++ {
			for x := lox; x < hix; x++ {
				ix, iy := int(math.Floor(mapX(x))), int(math.Floor(mapY(y)))
				if ix < 0 || iy < 0 || ix >= src.depth.W || iy >= src.depth.H {
					continue
				}
				for s := 0; s < dst.depth.S; s++ {
					ss := min(s, src.depth.S-1)
					v := src.depth.F[src.depth.idxS(ix, iy, src.depthZ, ss)]
					dst.depth.F[dst.depth.idxS(x, y, dst.depthZ, s)] = quantDepth(dst.depth.Fmt, v)
				}
			}
		}
	}
	if mask&GL_STENCIL_BUFFER_BIT != 0 && src.stencil != nil && dst.stencil != nil && src.stencil.St != nil && dst.stencil.St != nil {
		for y := loy; y < hiy; y++ {
			for x := lox; x < hix; x++ {
				ix, iy := int(math.Floor(mapX(x))), int(math.Floor(mapY(y)))
				if ix < 0 || iy < 0 || ix >= src.stencil.W || iy >= src.stencil.H {
					continue
				}
				for s := 0; s < dst.stencil.S; s++ {
					ss := min(s, src.stencil.S-1)
					dst.stencil.St[dst.stencil.pixS(x, y, dst.stencilZ, s)] = src.stencil.St[src.stencil.pixS(ix, iy, src.stencilZ, ss)]
				}
			}
		}
	}
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func copySample(d *Image, di int, s *Image, si int) {
	switch d.Fmt.Class {
	case ClsU8:
		copy(d.U8[di:di+4], s.U8[si:si+4])
	case ClsF32:
		copy(d.F[di:di+4], s.F[si:si+4])
	case ClsInt:
		copy(d.I[di:di+4], s.I[si:si+4])
	}
}

func bilinearResolve(rt *rtarget, im *Image, fx, fy float64) [4]float32 {
	x0, y0 := int(math.Floor(fx-0.5)), int(math.Floor(fy-0.5))
	ax, ay := float32(fx-0.5-float64(x0)), float32(fy-0.5-float64(y0))
	get := func(x, y int) [4]float32 {
		x, y = min(max(x, 0), im.W-1), min(max(y, 0), im.H-1)
		p, _ := rt.resolvePixel(im, x, y, 0)
		return p
	}
	c00, c10, c01, c11 := get(x0, y0), get(x0+1, y0), get(x0, y0+1), get(x0+1, y0+1)
	var o [4]float32
	for k := 0; k < 4; k++ {
		o[k] = (c00[k]*(1-ax)+c10[k]*ax)*(1-ay) + (c01[k]*(1-ax)+c11[k]*ax)*ay
	}
	return o
}

// storePixelAllSamples writes a resolved pixel into every sample.
func (im *Image) storePixelAllSamples(rt *rtarget, x, y, bi int, px [4]float32, pi [4]int32, mask [4]bool) {
	z := rt.colorZ[bi]
	for s := 0; s < im.S; s++ {
		o := im.idxS(x, y, z, s)
		switch im.Fmt.Class {
		case ClsInt:
			for k := 0; k < 4; k++ {
				if mask[k] {
					im.I[o+k] = pi[k]
				}
			}
		default:
			var cur [4]float32
			if !(mask[0] && mask[1] && mask[2] && mask[3]) {
				cur = loadDst(im, o)
				for k := 0; k < 4; k++ {
					if mask[k] {
						cur[k] = px[k]
					}
				}
				im.setFloat(o, cur)
			} else {
				im.setFloat(o, px)
			}
		}
	}
}

// InvalidateFramebuffer is a no-op hint.
func (c *Context) InvalidateFramebuffer(target uint32, atts []uint32) {
	if _, ok := c.fbForTarget(target); !ok {
		return
	}
}
