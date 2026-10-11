package gl

import "math"

func (c *Context) CreateTexture() uint32 {
	id := c.newID()
	c.textures[id] = newTexture(id)
	return id
}

func (c *Context) IsTexture(id uint32) bool {
	t := c.textures[id]
	return t != nil && t.Target != 0
}

func (c *Context) DeleteTexture(id uint32) {
	t := c.textures[id]
	if t == nil {
		return
	}
	for u := range c.units {
		for k := range c.units[u].tex {
			if c.units[u].tex[k] == t {
				c.units[u].tex[k] = nil
			}
		}
	}
	for _, f := range c.fbs {
		f.detachTex(t)
	}
	delete(c.textures, id)
	delete(c.viewCache, t)
}

func (f *Framebuffer) detachTex(t *Texture) {
	for i := range f.Color {
		if f.Color[i].Tex == t {
			f.Color[i] = Attach{}
		}
	}
	if f.Depth.Tex == t {
		f.Depth = Attach{}
	}
	if f.Stencil.Tex == t {
		f.Stencil = Attach{}
	}
}

func texSlot(target uint32, ver int) int {
	switch target {
	case GL_TEXTURE_2D:
		return 0
	case GL_TEXTURE_CUBE_MAP:
		return 1
	case GL_TEXTURE_3D:
		if ver == 2 {
			return 2
		}
	case GL_TEXTURE_2D_ARRAY:
		if ver == 2 {
			return 3
		}
	}
	return -1
}

func (c *Context) ActiveTexture(e uint32) {
	u := int(e) - GL_TEXTURE0
	if u < 0 || u >= c.Lim.MaxCombinedTexUnits {
		c.setErr(GL_INVALID_ENUM)
		return
	}
	c.activeUnit = u
}

func (c *Context) BindTexture(target, id uint32) {
	s := texSlot(target, c.Version)
	if s < 0 {
		c.setErr(GL_INVALID_ENUM)
		return
	}
	if id == 0 {
		c.units[c.activeUnit].tex[s] = nil
		return
	}
	t := c.textures[id]
	if t == nil {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	if t.Target != 0 && t.Target != target {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	t.Target = target
	c.units[c.activeUnit].tex[s] = t
}

// boundTex returns the texture bound for a target (and the cube face index
// for face targets).
func (c *Context) boundTex(target uint32) (*Texture, int, bool) {
	face := 0
	tt := target
	if target >= GL_TEXTURE_CUBE_MAP_POSITIVE_X && target <= GL_TEXTURE_CUBE_MAP_NEGATIVE_Z {
		face = int(target - GL_TEXTURE_CUBE_MAP_POSITIVE_X)
		tt = GL_TEXTURE_CUBE_MAP
	}
	s := texSlot(tt, c.Version)
	if s < 0 {
		c.setErr(GL_INVALID_ENUM)
		return nil, 0, false
	}
	t := c.units[c.activeUnit].tex[s]
	if t == nil {
		c.setErr(GL_INVALID_OPERATION)
		return nil, 0, false
	}
	return t, face, true
}

func (c *Context) TexParameter(target, pname uint32, v float32) {
	s := texSlot(target, c.Version)
	if s < 0 {
		c.setErr(GL_INVALID_ENUM)
		return
	}
	t := c.units[c.activeUnit].tex[s]
	if t == nil {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	u := uint32(v)
	switch pname {
	case GL_TEXTURE_MIN_FILTER:
		if !validMin(u) {
			c.setErr(GL_INVALID_ENUM)
			return
		}
		t.MinF = u
	case GL_TEXTURE_MAG_FILTER:
		if u != GL_NEAREST && u != GL_LINEAR {
			c.setErr(GL_INVALID_ENUM)
			return
		}
		t.MagF = u
	case GL_TEXTURE_WRAP_S, GL_TEXTURE_WRAP_T:
		if !validWrap(u) {
			c.setErr(GL_INVALID_ENUM)
			return
		}
		if pname == GL_TEXTURE_WRAP_S {
			t.WrapS = u
		} else {
			t.WrapT = u
		}
	case GL_TEXTURE_WRAP_R:
		if c.Version != 2 {
			c.setErr(GL_INVALID_ENUM)
			return
		}
		if !validWrap(u) {
			c.setErr(GL_INVALID_ENUM)
			return
		}
		t.WrapR = u
	case GL_TEXTURE_MIN_LOD, GL_TEXTURE_MAX_LOD, GL_TEXTURE_BASE_LEVEL, GL_TEXTURE_MAX_LEVEL, GL_TEXTURE_COMPARE_MODE, GL_TEXTURE_COMPARE_FUNC:
		if c.Version != 2 {
			c.setErr(GL_INVALID_ENUM)
			return
		}
		switch pname {
		case GL_TEXTURE_MIN_LOD:
			t.MinLod = v
		case GL_TEXTURE_MAX_LOD:
			t.MaxLod = v
		case GL_TEXTURE_BASE_LEVEL:
			if v < 0 {
				c.setErr(GL_INVALID_VALUE)
				return
			}
			t.BaseLevel = int(v)
		case GL_TEXTURE_MAX_LEVEL:
			if v < 0 {
				c.setErr(GL_INVALID_VALUE)
				return
			}
			t.MaxLevel = int(v)
		case GL_TEXTURE_COMPARE_MODE:
			if u != GL_NONE && u != GL_COMPARE_REF_TO_TEXTURE {
				c.setErr(GL_INVALID_ENUM)
				return
			}
			t.CmpMode = u
		default:
			if !validFunc(u) {
				c.setErr(GL_INVALID_ENUM)
				return
			}
			t.CmpFunc = u
		}
	case GL_TEXTURE_MAX_ANISOTROPY_EXT:
		if !c.Ext["EXT_texture_filter_anisotropic"] {
			c.setErr(GL_INVALID_ENUM)
			return
		}
		if v < 1 {
			c.setErr(GL_INVALID_VALUE)
			return
		}
		t.Aniso = v
	default:
		c.setErr(GL_INVALID_ENUM)
	}
}

func (c *Context) GetTexParameter(target, pname uint32) (float64, bool) {
	s := texSlot(target, c.Version)
	if s < 0 {
		c.setErr(GL_INVALID_ENUM)
		return 0, false
	}
	t := c.units[c.activeUnit].tex[s]
	if t == nil {
		c.setErr(GL_INVALID_OPERATION)
		return 0, false
	}
	switch pname {
	case GL_TEXTURE_MIN_FILTER:
		return float64(t.MinF), true
	case GL_TEXTURE_MAG_FILTER:
		return float64(t.MagF), true
	case GL_TEXTURE_WRAP_S:
		return float64(t.WrapS), true
	case GL_TEXTURE_WRAP_T:
		return float64(t.WrapT), true
	}
	if c.Version == 2 {
		switch pname {
		case GL_TEXTURE_WRAP_R:
			return float64(t.WrapR), true
		case GL_TEXTURE_MIN_LOD:
			return float64(t.MinLod), true
		case GL_TEXTURE_MAX_LOD:
			return float64(t.MaxLod), true
		case GL_TEXTURE_BASE_LEVEL:
			return float64(t.BaseLevel), true
		case GL_TEXTURE_MAX_LEVEL:
			return float64(t.MaxLevel), true
		case GL_TEXTURE_COMPARE_MODE:
			return float64(t.CmpMode), true
		case GL_TEXTURE_COMPARE_FUNC:
			return float64(t.CmpFunc), true
		case GL_TEXTURE_IMMUTABLE_FORMAT:
			return float64(b2i(t.Immutable)), true
		case GL_TEXTURE_IMMUTABLE_LEVELS:
			return float64(t.ImmLevels), true
		}
	}
	if pname == GL_TEXTURE_MAX_ANISOTROPY_EXT && c.Ext["EXT_texture_filter_anisotropic"] {
		return float64(t.Aniso), true
	}
	c.setErr(GL_INVALID_ENUM)
	return 0, false
}

// ---- format resolution --------------------------------------------------------

var (
	fmtL8    = &Format{Internal: GL_LUMINANCE, Base: GL_LUMINANCE, Class: ClsU8, Chan: 1, Bits: [4]int{8, 0, 0, 0}, Filterable: true, Luminance: true}
	fmtLA8   = &Format{Internal: GL_LUMINANCE_ALPHA, Base: GL_LUMINANCE_ALPHA, Class: ClsU8, Chan: 2, Bits: [4]int{8, 0, 0, 8}, Filterable: true, Luminance: true}
	fmtA8    = &Format{Internal: GL_ALPHA, Base: GL_ALPHA, Class: ClsU8, Chan: 1, Bits: [4]int{0, 0, 0, 8}, Filterable: true, AlphaOnly: true}
	fmtL32F  = &Format{Internal: GL_LUMINANCE, Base: GL_LUMINANCE, Class: ClsF32, Chan: 1, Bits: [4]int{32, 0, 0, 0}, Float: true, Luminance: true}
	fmtLA32F = &Format{Internal: GL_LUMINANCE_ALPHA, Base: GL_LUMINANCE_ALPHA, Class: ClsF32, Chan: 2, Bits: [4]int{32, 0, 0, 32}, Float: true, Luminance: true}
	fmtA32F  = &Format{Internal: GL_ALPHA, Base: GL_ALPHA, Class: ClsF32, Chan: 1, Bits: [4]int{0, 0, 0, 32}, Float: true, AlphaOnly: true}
	fmtL16F  = &Format{Internal: GL_LUMINANCE, Base: GL_LUMINANCE, Class: ClsF32, Chan: 1, Bits: [4]int{16, 0, 0, 0}, Float: true, Half: true, Filterable: true, Luminance: true}
	fmtLA16F = &Format{Internal: GL_LUMINANCE_ALPHA, Base: GL_LUMINANCE_ALPHA, Class: ClsF32, Chan: 2, Bits: [4]int{16, 0, 0, 16}, Float: true, Half: true, Filterable: true, Luminance: true}
	fmtA16F  = &Format{Internal: GL_ALPHA, Base: GL_ALPHA, Class: ClsF32, Chan: 1, Bits: [4]int{0, 0, 0, 16}, Float: true, Half: true, Filterable: true, AlphaOnly: true}
)

type combo struct{ format, typ uint32 }

var combos = map[uint32][]combo{}

func init() {
	add := func(in uint32, format uint32, types ...uint32) {
		for _, t := range types {
			combos[in] = append(combos[in], combo{format, t})
		}
	}
	add(GL_R8, GL_RED, GL_UNSIGNED_BYTE)
	add(GL_R8_SNORM, GL_RED, GL_BYTE)
	add(GL_R16F, GL_RED, GL_HALF_FLOAT, GL_FLOAT)
	add(GL_R32F, GL_RED, GL_FLOAT)
	add(GL_R8UI, GL_RED_INTEGER, GL_UNSIGNED_BYTE)
	add(GL_R8I, GL_RED_INTEGER, GL_BYTE)
	add(GL_R16UI, GL_RED_INTEGER, GL_UNSIGNED_SHORT)
	add(GL_R16I, GL_RED_INTEGER, GL_SHORT)
	add(GL_R32UI, GL_RED_INTEGER, GL_UNSIGNED_INT)
	add(GL_R32I, GL_RED_INTEGER, GL_INT)
	add(GL_RG8, GL_RG, GL_UNSIGNED_BYTE)
	add(GL_RG8_SNORM, GL_RG, GL_BYTE)
	add(GL_RG16F, GL_RG, GL_HALF_FLOAT, GL_FLOAT)
	add(GL_RG32F, GL_RG, GL_FLOAT)
	add(GL_RG8UI, GL_RG_INTEGER, GL_UNSIGNED_BYTE)
	add(GL_RG8I, GL_RG_INTEGER, GL_BYTE)
	add(GL_RG16UI, GL_RG_INTEGER, GL_UNSIGNED_SHORT)
	add(GL_RG16I, GL_RG_INTEGER, GL_SHORT)
	add(GL_RG32UI, GL_RG_INTEGER, GL_UNSIGNED_INT)
	add(GL_RG32I, GL_RG_INTEGER, GL_INT)
	add(GL_RGB8, GL_RGB, GL_UNSIGNED_BYTE)
	add(GL_SRGB8, GL_RGB, GL_UNSIGNED_BYTE)
	add(GL_RGB565, GL_RGB, GL_UNSIGNED_BYTE, GL_UNSIGNED_SHORT_5_6_5)
	add(GL_RGB8_SNORM, GL_RGB, GL_BYTE)
	add(GL_R11F_G11F_B10F, GL_RGB, GL_UNSIGNED_INT_10F_11F_11F_REV, GL_HALF_FLOAT, GL_FLOAT)
	add(GL_RGB9_E5, GL_RGB, GL_UNSIGNED_INT_5_9_9_9_REV, GL_HALF_FLOAT, GL_FLOAT)
	add(GL_RGB16F, GL_RGB, GL_HALF_FLOAT, GL_FLOAT)
	add(GL_RGB32F, GL_RGB, GL_FLOAT)
	add(GL_RGB8UI, GL_RGB_INTEGER, GL_UNSIGNED_BYTE)
	add(GL_RGB8I, GL_RGB_INTEGER, GL_BYTE)
	add(GL_RGB16UI, GL_RGB_INTEGER, GL_UNSIGNED_SHORT)
	add(GL_RGB16I, GL_RGB_INTEGER, GL_SHORT)
	add(GL_RGB32UI, GL_RGB_INTEGER, GL_UNSIGNED_INT)
	add(GL_RGB32I, GL_RGB_INTEGER, GL_INT)
	add(GL_RGBA8, GL_RGBA, GL_UNSIGNED_BYTE)
	add(GL_SRGB8_ALPHA8, GL_RGBA, GL_UNSIGNED_BYTE)
	add(GL_RGBA8_SNORM, GL_RGBA, GL_BYTE)
	add(GL_RGB5_A1, GL_RGBA, GL_UNSIGNED_BYTE, GL_UNSIGNED_SHORT_5_5_5_1, GL_UNSIGNED_INT_2_10_10_10_REV)
	add(GL_RGBA4, GL_RGBA, GL_UNSIGNED_BYTE, GL_UNSIGNED_SHORT_4_4_4_4)
	add(GL_RGB10_A2, GL_RGBA, GL_UNSIGNED_INT_2_10_10_10_REV)
	add(GL_RGB10_A2UI, GL_RGBA_INTEGER, GL_UNSIGNED_INT_2_10_10_10_REV)
	add(GL_RGBA16F, GL_RGBA, GL_HALF_FLOAT, GL_FLOAT)
	add(GL_RGBA32F, GL_RGBA, GL_FLOAT)
	add(GL_RGBA8UI, GL_RGBA_INTEGER, GL_UNSIGNED_BYTE)
	add(GL_RGBA8I, GL_RGBA_INTEGER, GL_BYTE)
	add(GL_RGBA16UI, GL_RGBA_INTEGER, GL_UNSIGNED_SHORT)
	add(GL_RGBA16I, GL_RGBA_INTEGER, GL_SHORT)
	add(GL_RGBA32UI, GL_RGBA_INTEGER, GL_UNSIGNED_INT)
	add(GL_RGBA32I, GL_RGBA_INTEGER, GL_INT)
	add(GL_DEPTH_COMPONENT16, GL_DEPTH_COMPONENT, GL_UNSIGNED_SHORT, GL_UNSIGNED_INT)
	add(GL_DEPTH_COMPONENT24, GL_DEPTH_COMPONENT, GL_UNSIGNED_INT)
	add(GL_DEPTH_COMPONENT32F, GL_DEPTH_COMPONENT, GL_FLOAT)
	add(GL_DEPTH24_STENCIL8, GL_DEPTH_STENCIL, GL_UNSIGNED_INT_24_8)
	add(GL_DEPTH32F_STENCIL8, GL_DEPTH_STENCIL, GL_FLOAT_32_UNSIGNED_INT_24_8_REV)
}

// resolveTexFormat validates internalformat/format/type and returns the
// storage format. A zero error means success.
func (c *Context) resolveTexFormat(internal, format, typ uint32) (*Format, uint32) {
	switch typ {
	case GL_UNSIGNED_BYTE, GL_UNSIGNED_SHORT_4_4_4_4, GL_UNSIGNED_SHORT_5_5_5_1, GL_UNSIGNED_SHORT_5_6_5, GL_FLOAT, GL_UNSIGNED_SHORT, GL_UNSIGNED_INT:
	case GL_HALF_FLOAT_OES:
		if c.Version == 2 || !c.Ext["OES_texture_half_float"] {
			return nil, GL_INVALID_ENUM
		}
	case GL_UNSIGNED_INT_24_8:
		if c.Version == 1 && !c.Ext["WEBGL_depth_texture"] {
			return nil, GL_INVALID_ENUM
		}
	case GL_BYTE, GL_SHORT, GL_INT, GL_HALF_FLOAT, GL_UNSIGNED_INT_2_10_10_10_REV, GL_UNSIGNED_INT_10F_11F_11F_REV, GL_UNSIGNED_INT_5_9_9_9_REV, GL_FLOAT_32_UNSIGNED_INT_24_8_REV:
		if c.Version != 2 {
			return nil, GL_INVALID_ENUM
		}
	default:
		return nil, GL_INVALID_ENUM
	}
	switch format {
	case GL_ALPHA, GL_RGB, GL_RGBA, GL_LUMINANCE, GL_LUMINANCE_ALPHA, GL_DEPTH_COMPONENT, GL_DEPTH_STENCIL:
	case GL_RED, GL_RG, GL_RED_INTEGER, GL_RG_INTEGER, GL_RGB_INTEGER, GL_RGBA_INTEGER:
		if c.Version != 2 {
			return nil, GL_INVALID_ENUM
		}
	default:
		return nil, GL_INVALID_ENUM
	}
	if c.Version == 1 {
		if internal != format {
			if (internal == GL_SRGB_EXT || internal == GL_SRGB_ALPHA_EXT) && c.Ext["EXT_sRGB"] {
				return nil, 0
			}
			return nil, GL_INVALID_OPERATION
		}
		switch format {
		case GL_DEPTH_COMPONENT:
			if !c.Ext["WEBGL_depth_texture"] {
				return nil, GL_INVALID_VALUE
			}
			switch typ {
			case GL_UNSIGNED_SHORT:
				return formats[GL_DEPTH_COMPONENT16], 0
			case GL_UNSIGNED_INT:
				return formats[GL_DEPTH_COMPONENT24], 0
			}
			return nil, GL_INVALID_OPERATION
		case GL_DEPTH_STENCIL:
			if !c.Ext["WEBGL_depth_texture"] {
				return nil, GL_INVALID_VALUE
			}
			if typ != GL_UNSIGNED_INT_24_8 {
				return nil, GL_INVALID_OPERATION
			}
			return formats[GL_DEPTH24_STENCIL8], 0
		}
		switch typ {
		case GL_UNSIGNED_BYTE:
			switch format {
			case GL_RGBA:
				return formats[GL_RGBA8], 0
			case GL_RGB:
				return formats[GL_RGB8], 0
			case GL_LUMINANCE:
				return fmtL8, 0
			case GL_LUMINANCE_ALPHA:
				return fmtLA8, 0
			case GL_ALPHA:
				return fmtA8, 0
			}
		case GL_UNSIGNED_SHORT_4_4_4_4:
			if format == GL_RGBA {
				return formats[GL_RGBA4], 0
			}
		case GL_UNSIGNED_SHORT_5_5_5_1:
			if format == GL_RGBA {
				return formats[GL_RGB5_A1], 0
			}
		case GL_UNSIGNED_SHORT_5_6_5:
			if format == GL_RGB {
				return formats[GL_RGB565], 0
			}
		case GL_FLOAT:
			if !c.Ext["OES_texture_float"] {
				return nil, GL_INVALID_ENUM
			}
			switch format {
			case GL_RGBA:
				return formats[GL_RGBA32F], 0
			case GL_RGB:
				return formats[GL_RGB32F], 0
			case GL_LUMINANCE:
				return fmtL32F, 0
			case GL_LUMINANCE_ALPHA:
				return fmtLA32F, 0
			case GL_ALPHA:
				return fmtA32F, 0
			}
		case GL_HALF_FLOAT_OES:
			switch format {
			case GL_RGBA:
				return formats[GL_RGBA16F], 0
			case GL_RGB:
				return formats[GL_RGB16F], 0
			case GL_LUMINANCE:
				return fmtL16F, 0
			case GL_LUMINANCE_ALPHA:
				return fmtLA16F, 0
			case GL_ALPHA:
				return fmtA16F, 0
			}
		}
		return nil, GL_INVALID_OPERATION
	}
	// WebGL 2
	switch internal {
	case GL_RGB, GL_RGBA, GL_LUMINANCE, GL_LUMINANCE_ALPHA, GL_ALPHA:
		if internal != format {
			return nil, GL_INVALID_OPERATION
		}
		switch {
		case typ == GL_UNSIGNED_BYTE:
			switch format {
			case GL_RGBA:
				return formats[GL_RGBA8], 0
			case GL_RGB:
				return formats[GL_RGB8], 0
			case GL_LUMINANCE:
				return fmtL8, 0
			case GL_LUMINANCE_ALPHA:
				return fmtLA8, 0
			default:
				return fmtA8, 0
			}
		case typ == GL_UNSIGNED_SHORT_4_4_4_4 && format == GL_RGBA:
			return formats[GL_RGBA4], 0
		case typ == GL_UNSIGNED_SHORT_5_5_5_1 && format == GL_RGBA:
			return formats[GL_RGB5_A1], 0
		case typ == GL_UNSIGNED_SHORT_5_6_5 && format == GL_RGB:
			return formats[GL_RGB565], 0
		}
		return nil, GL_INVALID_OPERATION
	}
	f := formats[internal]
	if f == nil || internal == GL_STENCIL_INDEX8 || internal == GL_LUMINANCE || internal == GL_ALPHA || internal == GL_LUMINANCE_ALPHA {
		return nil, GL_INVALID_ENUM
	}
	for _, cb := range combos[internal] {
		if cb.format == format && cb.typ == typ {
			return f, 0
		}
	}
	return nil, GL_INVALID_OPERATION
}

func (c *Context) checkTexSize(target uint32, level, w, h, d int) bool {
	if level < 0 || w < 0 || h < 0 || d < 0 {
		c.setErr(GL_INVALID_VALUE)
		return false
	}
	max := c.Lim.MaxTextureSize
	if target == GL_TEXTURE_3D {
		max = c.Lim.Max3DTextureSize
	} else if target >= GL_TEXTURE_CUBE_MAP_POSITIVE_X && target <= GL_TEXTURE_CUBE_MAP_NEGATIVE_Z {
		max = c.Lim.MaxCubeMapSize
	}
	if level > 31 || w > max>>uint(level) && w > 1 || h > max>>uint(level) && h > 1 {
		c.setErr(GL_INVALID_VALUE)
		return false
	}
	if target == GL_TEXTURE_2D_ARRAY && d > c.Lim.MaxArrayLayers || target == GL_TEXTURE_3D && d > c.Lim.Max3DTextureSize>>uint(level) && d > 1 {
		c.setErr(GL_INVALID_VALUE)
		return false
	}
	return true
}

// srcData prepares the client bytes for an upload: either the supplied data
// or the bound PIXEL_UNPACK_BUFFER contents at pboOff. ok=false on error.
func (c *Context) srcData(data []byte, pboOff int, w, h, d int, format, typ uint32, pk Pack, needed bool) ([]byte, bool) {
	if c.unpackBuf != nil && c.Version == 2 {
		if pboOff < 0 || pboOff > len(c.unpackBuf.Data) {
			c.setErr(GL_INVALID_OPERATION)
			return nil, false
		}
		data = c.unpackBuf.Data[pboOff:]
		if pk.RequiredBytes(uint32(w), uint32(h), uint32(d), format, typ) > len(data) {
			c.setErr(GL_INVALID_OPERATION)
			return nil, false
		}
		return data, true
	}
	if data == nil {
		return nil, !needed || func() bool { c.setErr(GL_INVALID_VALUE); return false }()
	}
	if pk.RequiredBytes(uint32(w), uint32(h), uint32(d), format, typ) > len(data) {
		c.setErr(GL_INVALID_OPERATION)
		return nil, false
	}
	return data, true
}

// TexImage2D uploads (or allocates when data is nil) a 2D / cube face level.
// dom marks data as RGBA8 non-premultiplied pixels from a DOM source.
func (c *Context) TexImage2D(target uint32, level int, internal uint32, w, h, border int, format, typ uint32, data []byte, pboOff int, dom bool) {
	t, face, ok := c.boundTex(target)
	if !ok {
		return
	}
	if target == GL_TEXTURE_3D || target == GL_TEXTURE_2D_ARRAY {
		c.setErr(GL_INVALID_ENUM)
		return
	}
	if border != 0 {
		c.setErr(GL_INVALID_VALUE)
		return
	}
	if !c.checkTexSize(target, level, w, h, 1) {
		return
	}
	if t.Immutable {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	f, e := c.resolveTexFormat(internal, format, typ)
	if e != 0 {
		c.setErr(e)
		return
	}
	if f == nil {
		f = c.extFormat(internal)
		if f == nil {
			c.setErr(GL_INVALID_ENUM)
			return
		}
	}
	_ = 0
	if target >= GL_TEXTURE_CUBE_MAP_POSITIVE_X && target <= GL_TEXTURE_CUBE_MAP_NEGATIVE_Z && w != h {
		c.setErr(GL_INVALID_VALUE)
		return
	}
	if f.IsDepth() && (data != nil && !dom) && c.Version == 1 {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	pk := c.pixStore.Unpack
	sf, st := format, typ
	if dom {
		sf, st = GL_RGBA, GL_UNSIGNED_BYTE
		pk = Pack{Alignment: 1}
	}
	src, ok := c.srcData(data, pboOff, w, h, 1, sf, st, pk, false)
	if !ok {
		return
	}
	im := NewImage(f, w, h, 1, 1)
	if f.IsDepth() {
		for i := range im.F {
			im.F[i] = 1
		}
	}
	if (src != nil || dom) && w > 0 && h > 0 {
		if dom && src == nil {
			c.setErr(GL_INVALID_VALUE)
			return
		}
		im.Upload(0, 0, 0, w, h, 1, sf, st, src, pk, c.pixStore.FlipY, c.pixStore.Premul && f.Chan == 4 || c.pixStore.Premul && dom && f.Chan == 4)
	}
	t.setLevel(face, level, im)
	delete(c.viewCache, t)
}

func (c *Context) extFormat(internal uint32) *Format {
	return nil
}

func (c *Context) TexSubImage2D(target uint32, level, x, y, w, h int, format, typ uint32, data []byte, pboOff int, dom bool) {
	t, face, ok := c.boundTex(target)
	if !ok {
		return
	}
	if target == GL_TEXTURE_3D || target == GL_TEXTURE_2D_ARRAY {
		c.setErr(GL_INVALID_ENUM)
		return
	}
	im := t.levelImg(face, level)
	if im == nil {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	if x < 0 || y < 0 || w < 0 || h < 0 || x+w > im.W || y+h > im.H || level < 0 {
		c.setErr(GL_INVALID_VALUE)
		return
	}
	if !dom {
		if e := c.subFormatCheck(im.Fmt, format, typ); e != 0 {
			c.setErr(e)
			return
		}
	}
	pk := c.pixStore.Unpack
	sf, st := format, typ
	if dom {
		sf, st = GL_RGBA, GL_UNSIGNED_BYTE
		pk = Pack{Alignment: 1}
	}
	src, ok := c.srcData(data, pboOff, w, h, 1, sf, st, pk, true)
	if !ok {
		return
	}
	if w == 0 || h == 0 {
		return
	}
	im.Upload(x, y, 0, w, h, 1, sf, st, src, pk, c.pixStore.FlipY, c.pixStore.Premul && im.Fmt.Chan == 4)
}

func (c *Context) TexImage3D(target uint32, level int, internal uint32, w, h, d, border int, format, typ uint32, data []byte, pboOff int) {
	if c.Version != 2 {
		c.setErr(GL_INVALID_ENUM)
		return
	}
	if target != GL_TEXTURE_3D && target != GL_TEXTURE_2D_ARRAY {
		c.setErr(GL_INVALID_ENUM)
		return
	}
	t, _, ok := c.boundTex(target)
	if !ok {
		return
	}
	if border != 0 || !c.checkTexSize(target, level, w, h, d) {
		if border != 0 {
			c.setErr(GL_INVALID_VALUE)
		}
		return
	}
	if t.Immutable {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	f, e := c.resolveTexFormat(internal, format, typ)
	if e != 0 {
		c.setErr(e)
		return
	}
	if f.IsDepth() {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	pk := c.pixStore.Unpack
	src, ok := c.srcData(data, pboOff, w, h, d, format, typ, pk, false)
	if !ok {
		return
	}
	im := NewImage(f, w, h, d, 1)
	if src != nil && w > 0 && h > 0 && d > 0 {
		im.Upload(0, 0, 0, w, h, d, format, typ, src, pk, c.pixStore.FlipY, c.pixStore.Premul && f.Chan == 4)
	}
	t.setLevel(0, level, im)
	delete(c.viewCache, t)
}

func (c *Context) TexSubImage3D(target uint32, level, x, y, z, w, h, d int, format, typ uint32, data []byte, pboOff int, dom bool) {
	if c.Version != 2 || (target != GL_TEXTURE_3D && target != GL_TEXTURE_2D_ARRAY) {
		c.setErr(GL_INVALID_ENUM)
		return
	}
	t, _, ok := c.boundTex(target)
	if !ok {
		return
	}
	im := t.levelImg(0, level)
	if im == nil {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	if x < 0 || y < 0 || z < 0 || w < 0 || h < 0 || d < 0 || x+w > im.W || y+h > im.H || z+d > im.D {
		c.setErr(GL_INVALID_VALUE)
		return
	}
	pk := c.pixStore.Unpack
	sf, st := format, typ
	if dom {
		sf, st = GL_RGBA, GL_UNSIGNED_BYTE
		pk = Pack{Alignment: 1}
	} else if e := c.subFormatCheck(im.Fmt, format, typ); e != 0 {
		c.setErr(e)
		return
	}
	src, ok := c.srcData(data, pboOff, w, h, d, sf, st, pk, true)
	if !ok || w == 0 || h == 0 || d == 0 {
		return
	}
	im.Upload(x, y, z, w, h, d, sf, st, src, pk, c.pixStore.FlipY, c.pixStore.Premul && im.Fmt.Chan == 4)
}

func (c *Context) TexStorage(target uint32, levels int, internal uint32, w, h, d int) {
	if c.Version != 2 {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	t, _, ok := c.boundTex(target)
	if !ok {
		return
	}
	f := formats[internal]
	if f == nil || internal == GL_LUMINANCE || internal == GL_ALPHA || internal == GL_LUMINANCE_ALPHA {
		c.setErr(GL_INVALID_ENUM)
		return
	}
	if levels < 1 || w < 1 || h < 1 || d < 1 {
		c.setErr(GL_INVALID_VALUE)
		return
	}
	if levels > mipCount(w, h, d) {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	if t.Immutable {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	if !c.checkTexSize(target, 0, w, h, d) {
		return
	}
	nf := 1
	if target == GL_TEXTURE_CUBE_MAP {
		nf = 6
		if w != h {
			c.setErr(GL_INVALID_VALUE)
			return
		}
	}
	for face := 0; face < nf; face++ {
		lw, lh, ld := w, h, d
		for l := 0; l < levels; l++ {
			dd := 1
			if target == GL_TEXTURE_3D || target == GL_TEXTURE_2D_ARRAY {
				dd = ld
			}
			im := NewImage(f, lw, lh, dd, 1)
			if f.IsDepth() {
				for i := range im.F {
					im.F[i] = 1
				}
			}
			t.setLevel(face, l, im)
			lw, lh = max(1, lw/2), max(1, lh/2)
			if target == GL_TEXTURE_3D {
				ld = max(1, ld/2)
			}
		}
	}
	t.Immutable, t.ImmLevels = true, levels
	delete(c.viewCache, t)
}

func (c *Context) GenerateMipmap(target uint32) {
	s := texSlot(target, c.Version)
	if s < 0 {
		c.setErr(GL_INVALID_ENUM)
		return
	}
	t := c.units[c.activeUnit].tex[s]
	if t == nil {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	base := 0
	if c.Version == 2 {
		base = t.BaseLevel
	}
	nf := 1
	if target == GL_TEXTURE_CUBE_MAP {
		nf = 6
	}
	im0 := t.levelImg(0, base)
	if im0 == nil {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	if c.Version == 1 && (!isPOT(im0.W) || !isPOT(im0.H)) {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	f := im0.Fmt
	if f.IsDepth() || f.Integer || (f.Class == ClsF32 && !f.Filterable && c.Version == 1 && !c.Ext["OES_texture_float_linear"] && false) {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	for face := 0; face < nf; face++ {
		src := t.levelImg(face, base)
		if src == nil {
			c.setErr(GL_INVALID_OPERATION)
			return
		}
		l := base
		for src.W > 1 || src.H > 1 || (src.D > 1 && target == GL_TEXTURE_3D) {
			var nxt *Image
			if target == GL_TEXTURE_3D {
				nxt = downsample3D(src)
			} else {
				nxt = downsample(src)
			}
			l++
			t.setLevel(face, l, nxt)
			src = nxt
		}
	}
	delete(c.viewCache, t)
}

func downsample3D(src *Image) *Image {
	if src.D <= 1 {
		return downsample(src)
	}
	d2 := max(1, src.D/2)
	w, h := max(1, src.W/2), max(1, src.H/2)
	dst := NewImage(src.Fmt, w, h, d2, 1)
	f := src.Fmt
	for z := 0; z < d2; z++ {
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				var acc [4]float32
				n := float32(0)
				for dz := 0; dz < 2; dz++ {
					for dy := 0; dy < 2; dy++ {
						for dx := 0; dx < 2; dx++ {
							sx, sy, sz := min(2*x+dx, src.W-1), min(2*y+dy, src.H-1), min(2*z+dz, src.D-1)
							v := src.Get(sx, sy, sz)
							for k := 0; k < 4; k++ {
								acc[k] += v[k]
							}
							n++
						}
					}
				}
				for k := 0; k < 4; k++ {
					acc[k] /= n
				}
				i := dst.idx(x, y, z)
				if f.Class == ClsU8 {
					dst.setFloat(i, acc)
				} else {
					dst.setFloat(i, acc)
				}
			}
		}
	}
	return dst
}

// CopyTexImage2D copies from the read framebuffer.
func (c *Context) CopyTexImage2D(target uint32, level int, internal uint32, x, y, w, h, border int) {
	t, face, ok := c.boundTex(target)
	if !ok {
		return
	}
	if border != 0 || w < 0 || h < 0 || !c.checkTexSize(target, level, w, h, 1) {
		if border != 0 {
			c.setErr(GL_INVALID_VALUE)
		}
		return
	}
	rt, status := c.resolveTarget(c.readFB)
	if status != GL_FRAMEBUFFER_COMPLETE {
		c.setErr(GL_INVALID_FRAMEBUFFER_OPERATION)
		return
	}
	src := rt.readColor(c.readFB)
	if src == nil {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	var f *Format
	switch internal {
	case GL_RGBA:
		f = formats[GL_RGBA8]
	case GL_RGB:
		f = formats[GL_RGB8]
	case GL_LUMINANCE:
		f = fmtL8
	case GL_LUMINANCE_ALPHA:
		f = fmtLA8
	case GL_ALPHA:
		f = fmtA8
	default:
		if c.Version == 2 {
			f = formats[internal]
		}
	}
	if f == nil || f.IsDepth() {
		c.setErr(GL_INVALID_ENUM)
		return
	}
	if src.Fmt.Integer != f.Integer {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	im := NewImage(f, w, h, 1, 1)
	c.copyRegion(im, 0, 0, 0, src, rt, x, y, w, h)
	t.setLevel(face, level, im)
	delete(c.viewCache, t)
}

func (c *Context) CopyTexSubImage2D(target uint32, level, xo, yo, x, y, w, h int) {
	t, face, ok := c.boundTex(target)
	if !ok {
		return
	}
	im := t.levelImg(face, level)
	if im == nil {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	if xo < 0 || yo < 0 || w < 0 || h < 0 || xo+w > im.W || yo+h > im.H {
		c.setErr(GL_INVALID_VALUE)
		return
	}
	rt, status := c.resolveTarget(c.readFB)
	if status != GL_FRAMEBUFFER_COMPLETE {
		c.setErr(GL_INVALID_FRAMEBUFFER_OPERATION)
		return
	}
	src := rt.readColor(c.readFB)
	if src == nil {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	c.copyRegion(im, xo, yo, 0, src, rt, x, y, w, h)
}

func (c *Context) CopyTexSubImage3D(target uint32, level, xo, yo, zo, x, y, w, h int) {
	t, _, ok := c.boundTex(target)
	if !ok || c.Version != 2 {
		return
	}
	im := t.levelImg(0, level)
	if im == nil {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	if xo < 0 || yo < 0 || zo < 0 || zo >= im.D || xo+w > im.W || yo+h > im.H {
		c.setErr(GL_INVALID_VALUE)
		return
	}
	rt, status := c.resolveTarget(c.readFB)
	if status != GL_FRAMEBUFFER_COMPLETE {
		c.setErr(GL_INVALID_FRAMEBUFFER_OPERATION)
		return
	}
	src := rt.readColor(c.readFB)
	if src == nil {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	c.copyRegion(im, xo, yo, zo, src, rt, x, y, w, h)
}

// copyRegion copies framebuffer pixels (resolving multisampling) into an image.
func (c *Context) copyRegion(dst *Image, dx, dy, dz int, src *Image, rt *rtarget, x, y, w, h int) {
	for yy := 0; yy < h; yy++ {
		for xx := 0; xx < w; xx++ {
			sx, sy := x+xx, y+yy
			var px [4]float32
			var ri [4]int32
			if sx >= 0 && sy >= 0 && sx < src.W && sy < src.H {
				px, ri = rt.resolvePixel(src, sx, sy, 0)
			}
			i := dst.idx(dx+xx, dy+yy, dz)
			switch dst.Fmt.Class {
			case ClsInt:
				copy(dst.I[i:i+4], ri[:])
			default:
				if src.Fmt.Chan < 4 && !src.Fmt.Luminance {
					px[3] = 1
				}
				if dst.Fmt.Luminance {
					px[1], px[2] = px[0], px[0]
				}
				if dst.Fmt.AlphaOnly {
					px[0], px[1], px[2] = 0, 0, 0
				}
				if dst.Fmt.Chan < 4 && !dst.Fmt.AlphaOnly && dst.Fmt.Base != GL_LUMINANCE_ALPHA {
					px[3] = 1
				}
				dst.setFloat(i, px)
			}
		}
	}
}

var _ = math.Pi

// subFormatCheck validates texSubImage format/type against the level's format.
func (c *Context) subFormatCheck(f *Format, format, typ uint32) uint32 {
	internal := f.Internal
	if c.Version == 1 {
		internal = f.Base
		if f.IsDepth() {
			return GL_INVALID_OPERATION
		}
	}
	got, e := c.resolveTexFormat(internal, format, typ)
	if e != 0 {
		return e
	}
	if got == nil || got.Class != f.Class && !(f.Class == ClsU8 && got.Class == ClsU8) {
		return GL_INVALID_OPERATION
	}
	if c.Version == 1 && (got.Bits != f.Bits) {
		return GL_INVALID_OPERATION
	}
	return 0
}
