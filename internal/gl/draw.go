package gl

import (
	"encoding/binary"
	"math"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/sweefbrowser/sweef/internal/gl/glsl"
)

// vtxSet holds the vertex shader results (and clipped vertices) of one instance.
type vtxSet struct {
	n   int
	pos []float32 // clip space x,y,z,w per vertex
	psz []float32
	vf  []float32 // interpolated varying lanes
	vi  []uint32  // flat varying lanes
	scr []scrVtx
	tf  []uint32
	tfn int
}

type scrVtx struct {
	X, Y int64
	Z    float32
	IW   float32
	code uint8
}

func (s *vtxSet) grow(n, nvf, nvi int) {
	s.n = n
	s.pos = make([]float32, n*4)
	s.psz = make([]float32, n)
	s.vf = make([]float32, n*nvf)
	s.vi = make([]uint32, n*nvi)
	s.scr = make([]scrVtx, n)
}

func (dc *drawCtx) tfLanes(set *vtxSet, v int32) []uint32 {
	return set.tf[int(v)*set.tfn : int(v+1)*set.tfn]
}

// drawCtx is the immutable per-draw snapshot used by workers.
type drawCtx struct {
	c        *Context
	p        *Program
	vs, fs   *glsl.Shader
	rt       *rtarget
	tex      []glsl.Sampler
	needQuad bool
	nvf, nvi int
	vsF, fsF []int // register offsets of interpolated lanes
	vsI, fsI []int
	outs     []fragOut
	vp       [4]int
	dr       [2]float32
	// state copies
	depthTest, depthMask  bool
	depthFunc             uint32
	stencilTest           bool
	stF, stB              stencilState
	blend                 bool
	blendEq               [2]uint32
	blendF                [4]uint32
	blendColor            [4]float32
	colorMask             [4]bool
	cull                  bool
	cullMode              uint32
	frontCCW              bool
	polyOff               bool
	polyFactor, polyUnits float32
	scissor               [4]int
	scissorOn             bool
	samples               int
	version               int
	pointSizeW            bool
	blocksVS, blocksFS    []blockData
	vsPool, fsPool        *sync.Pool
	occ                   *atomic.Uint64
	tfActive              bool
	discard               bool
	earlyZ                bool
	fragDepth             bool
	a2c                   bool
	sampleCov             bool
	covMask               uint8
}

const (
	fkFloat uint8 = iota
	fkInt
	fkUint
)

type blockData struct {
	idx  int // index in the shader's Blocks
	data []byte
}

type fragOut struct {
	off   int
	buf   int // draw buffer index
	comp  int
	kind  uint8
	bcast bool
	n     int
}

func primCount(mode uint32, n int) int {
	switch mode {
	case GL_POINTS:
		return n
	case GL_LINES:
		return n / 2
	case GL_LINE_STRIP:
		return max(0, n-1)
	case GL_LINE_LOOP:
		if n < 2 {
			return 0
		}
		return n
	case GL_TRIANGLES:
		return n / 3
	case GL_TRIANGLE_STRIP, GL_TRIANGLE_FAN:
		return max(0, n-2)
	}
	return 0
}

// DrawArrays draws count vertices starting at first.
func (c *Context) DrawArrays(mode uint32, first, count, instances int) {
	if c.program == nil || !c.program.Linked {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	if first < 0 || count < 0 || instances < 0 {
		c.setErr(GL_INVALID_VALUE)
		return
	}
	c.draw(mode, first, count, false, 0, 0, instances)
}

// DrawElements draws indexed primitives.
func (c *Context) DrawElements(mode uint32, count int, typ uint32, off int, instances int) {
	if count < 0 || off < 0 || instances < 0 {
		c.setErr(GL_INVALID_VALUE)
		return
	}
	c.draw(mode, 0, count, true, typ, off, instances)
}

func (c *Context) draw(mode uint32, first, count int, indexed bool, itype uint32, ioff int, instances int) {
	var isize int
	if indexed {
		switch itype {
		case GL_UNSIGNED_BYTE:
			isize = 1
		case GL_UNSIGNED_SHORT:
			isize = 2
		case GL_UNSIGNED_INT:
			if c.Version == 1 && !c.Ext["OES_element_index_uint"] {
				c.setErr(GL_INVALID_ENUM)
				return
			}
			isize = 4
		default:
			c.setErr(GL_INVALID_ENUM)
			return
		}
	}
	p := c.program
	if p == nil || !p.Linked {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	switch mode {
	case GL_POINTS, GL_LINES, GL_LINE_LOOP, GL_LINE_STRIP, GL_TRIANGLES, GL_TRIANGLE_STRIP, GL_TRIANGLE_FAN:
	default:
		c.setErr(GL_INVALID_ENUM)
		return
	}
	if indexed && ioff%isize != 0 {
		c.setErr(GL_INVALID_OPERATION)
		return
	}
	if c.tf.Active && !c.tf.Paused {
		okMode := (c.tf.Mode == GL_POINTS && mode == GL_POINTS) ||
			(c.tf.Mode == GL_LINES && (mode == GL_LINES || mode == GL_LINE_STRIP || mode == GL_LINE_LOOP)) ||
			(c.tf.Mode == GL_TRIANGLES && (mode == GL_TRIANGLES || mode == GL_TRIANGLE_STRIP || mode == GL_TRIANGLE_FAN))
		if !okMode {
			c.setErr(GL_INVALID_OPERATION)
			return
		}
	}
	rt, st := c.resolveTarget(c.drawFB)
	if st != GL_FRAMEBUFFER_COMPLETE {
		c.setErr(GL_INVALID_FRAMEBUFFER_OPERATION)
		return
	}
	// element buffer
	var idxBuf []byte
	if indexed {
		eb := c.vao.Element
		if eb == nil {
			c.setErr(GL_INVALID_OPERATION)
			return
		}
		if ioff+count*isize > len(eb.Data) {
			c.setErr(GL_INVALID_OPERATION)
			return
		}
		idxBuf = eb.Data[ioff : ioff+count*isize]
	}
	if count == 0 || instances == 0 {
		return
	}
	// index list
	ids := make([]uint32, count)
	maxID := uint32(0)
	restart := indexed && c.Version == 2
	rIdx := uint32(0)
	if restart {
		rIdx = map[int]uint32{1: 0xFF, 2: 0xFFFF, 4: 0xFFFFFFFF}[isize]
	}
	if indexed {
		switch isize {
		case 1:
			for i := range ids {
				ids[i] = uint32(idxBuf[i])
			}
		case 2:
			for i := range ids {
				ids[i] = uint32(binary.LittleEndian.Uint16(idxBuf[i*2:]))
			}
		default:
			for i := range ids {
				ids[i] = binary.LittleEndian.Uint32(idxBuf[i*4:])
			}
		}
	} else {
		for i := range ids {
			ids[i] = uint32(first + i)
		}
	}
	for _, v := range ids {
		if restart && v == rIdx {
			continue
		}
		if v > maxID {
			maxID = v
		}
	}
	// attribute bounds
	vao := c.vao
	for _, a := range p.Attribs {
		for col := 0; col < a.Cols*a.Size; col++ {
			at := &vao.Attribs[a.Loc+col]
			if !at.Enabled || at.Buf == nil {
				continue
			}
			es := attribSize(at)
			stride := at.Stride
			if stride == 0 {
				stride = es
			}
			var last int
			if at.Divisor > 0 {
				last = (instances - 1) / at.Divisor
			} else {
				last = int(maxID)
			}
			if at.Offset+stride*last+es > len(at.Buf.Data) {
				c.setErr(GL_INVALID_OPERATION)
				return
			}
		}
	}
	// uniform blocks
	dc := c.newDrawCtx(p, rt)
	if dc == nil {
		return
	}
	if p.Blocks != nil {
		for _, ub := range p.Blocks {
			if !ub.RefVS && !ub.RefFS {
				continue
			}
			bd := c.ubo[ub.Binding]
			if bd.Buf == nil || bd.Size < ub.DataSize {
				c.setErr(GL_INVALID_OPERATION)
				return
			}
		}
		for _, ub := range p.Blocks {
			bd := c.ubo[ub.Binding]
			if bd.Buf == nil {
				continue
			}
			data := bd.Buf.Data[bd.Off : bd.Off+bd.Size]
			if ub.VS >= 0 {
				dc.blocksVS = append(dc.blocksVS, blockData{ub.VS, data})
			}
			if ub.FS >= 0 {
				dc.blocksFS = append(dc.blocksFS, blockData{ub.FS, data})
			}
		}
	}
	// transform feedback capacity
	nPrims := primCount(mode, count)
	if c.tf.Active && !c.tf.Paused {
		vper := 1
		switch c.tf.Mode {
		case GL_LINES:
			vper = 2
		case GL_TRIANGLES:
			vper = 3
		}
		need := nPrims * vper * instances
		if !c.tfCapacity(p, need) {
			c.setErr(GL_INVALID_OPERATION)
			return
		}
	}
	c.markDirty()
	c.Stats.Draws++
	dc.run(mode, ids, restart, rIdx, instances, first, indexed, maxID)
	if q := c.activeQ[GL_ANY_SAMPLES_PASSED]; q != nil {
		q.Result += dc.occ.Load()
	}
	if q := c.activeQ[GL_ANY_SAMPLES_PASSED_CONSERVATIVE]; q != nil {
		q.Result += dc.occ.Load()
	}
}

func (c *Context) tfCapacity(p *Program, verts int) bool {
	if p.tfMode == GL_INTERLEAVED_ATTRIBS {
		b := c.tf.Bufs[0]
		if b.Buf == nil {
			return false
		}
		return c.tfWritePos[0]+verts*p.tfLanes*4 <= b.Off+min(b.Size, len(b.Buf.Data)-b.Off)
	}
	for i, o := range p.tfOuts {
		b := c.tf.Bufs[i]
		if b.Buf == nil || c.tfWritePos[i]+verts*o.n*4 > b.Off+min(b.Size, len(b.Buf.Data)-b.Off) {
			return false
		}
	}
	return true
}

func attribSize(a *Attrib) int {
	ts := 4
	switch a.Type {
	case GL_BYTE, GL_UNSIGNED_BYTE:
		ts = 1
	case GL_SHORT, GL_UNSIGNED_SHORT, GL_HALF_FLOAT:
		ts = 2
	case GL_INT_2_10_10_10_REV, GL_UNSIGNED_INT_2_10_10_10_REV:
		return 4
	}
	return ts * a.Size
}

func (c *Context) newDrawCtx(p *Program, rt *rtarget) *drawCtx {
	dc := &drawCtx{c: c, p: p, vs: p.vs, fs: p.fs, rt: rt, version: c.Version, occ: &atomic.Uint64{}}
	dc.vp = c.viewport
	dc.dr = c.depthRange
	dc.depthTest = c.enDepth && rt.depth != nil && rt.depth.Fmt.IsDepth()
	dc.depthMask = c.depthMask
	dc.depthFunc = c.depthFunc
	dc.stencilTest = c.enStencil && rt.stencil != nil && rt.stencil.St != nil
	dc.stF, dc.stB = c.stencilF, c.stencilB
	dc.blend = c.enBlend
	dc.blendEq = [2]uint32{c.blendEqRGB, c.blendEqA}
	dc.blendF = [4]uint32{c.blendSrcRGB, c.blendDstRGB, c.blendSrcA, c.blendDstA}
	dc.blendColor = c.blendColor
	dc.colorMask = c.colorMask
	dc.cull = c.enCull
	dc.cullMode = c.cullMode
	dc.frontCCW = c.frontFace == GL_CCW
	dc.polyOff = c.enPoly
	dc.polyFactor, dc.polyUnits = c.polyFactor, c.polyUnits
	dc.scissor = c.scissor
	dc.scissorOn = c.enScissor
	dc.samples = rt.S
	dc.discard = c.enRDiscard
	dc.pointSizeW = p.vs.WritesPointSize
	dc.sampleCov = c.enSampleCov
	dc.a2c = c.enA2C
	// varying lane tables
	for _, v := range p.vary {
		for k := 0; k < v.n; k++ {
			if v.flat {
				dc.vsI = append(dc.vsI, v.vsOff+k)
				dc.fsI = append(dc.fsI, v.fsOff+k)
			} else {
				dc.vsF = append(dc.vsF, v.vsOff+k)
				dc.fsF = append(dc.fsF, v.fsOff+k)
			}
		}
	}
	dc.nvf, dc.nvi = len(dc.vsF), len(dc.vsI)
	// fragment outputs
	fs := p.fs
	if fs.Version == 100 {
		if fs.WritesFragData {
			dc.outs = append(dc.outs, fragOut{off: fs.FragDataOff, buf: 0, comp: 4, kind: fkFloat, n: c.Lim.MaxDrawBuffers})
		} else if fs.WritesFragColor {
			dc.outs = append(dc.outs, fragOut{off: fs.FragColorOff, comp: 4, bcast: true, kind: fkFloat})
		}
	} else {
		for _, o := range fs.Outputs {
			k := fkFloat
			switch o.T.K {
			case glsl.KInt:
				k = fkInt
			case glsl.KUint:
				k = fkUint
			}
			cols := max(1, o.T.M)
			arr := max(1, o.T.Arr)
			for e := 0; e < arr*cols; e++ {
				dc.outs = append(dc.outs, fragOut{off: o.Off + e*o.T.N, buf: o.Loc + e, comp: o.T.N, kind: k})
			}
		}
	}
	dc.fragDepth = fs.WritesFragDepth
	dc.earlyZ = !fs.UsesDiscard && !fs.WritesFragDepth && !dc.a2c
	// textures
	dc.tex = make([]glsl.Sampler, c.Lim.MaxCombinedTexUnits)
	needLod := false
	fl := c.Ext["OES_texture_float_linear"]
	hl := c.Ext["OES_texture_half_float_linear"]
	for _, l := range p.samplerLeaves {
		for e := 0; e < l.Size; e++ {
			unit := int(p.store[l.Store+e])
			if unit < 0 || unit >= len(dc.tex) {
				continue
			}
			slot := -1
			switch l.GLType {
			case GL_SAMPLER_2D, GL_SAMPLER_2D_SHADOW, GL_INT_SAMPLER_2D, GL_UNSIGNED_INT_SAMPLER_2D:
				slot = 0
			case GL_SAMPLER_CUBE, GL_SAMPLER_CUBE_SHADOW, GL_INT_SAMPLER_CUBE, GL_UNSIGNED_INT_SAMPLER_CUBE:
				slot = 1
			case GL_SAMPLER_3D, GL_INT_SAMPLER_3D, GL_UNSIGNED_INT_SAMPLER_3D:
				slot = 2
			case GL_SAMPLER_2D_ARRAY, GL_SAMPLER_2D_ARRAY_SHADOW, GL_INT_SAMPLER_2D_ARRAY, GL_UNSIGNED_INT_SAMPLER_2D_ARRAY:
				slot = 3
			}
			if slot < 0 {
				continue
			}
			t := c.units[unit].tex[slot]
			if t == nil {
				continue
			}
			var sp *SamplerParams
			if so := c.units[unit].sampler; so != nil {
				sp = &so.P
			}
			v := buildView(t, sp, c.Version == 2, fl, hl)
			dc.tex[unit] = v
			if v.complete && (v.mips || normFilter(v.minF) != v.magF) {
				needLod = true
			}
		}
	}
	dc.needQuad = p.fs.UsesDerivatives || (p.fs.UsesImplicitLod && needLod)
	return dc
}

func normFilter(f uint32) uint32 {
	switch f {
	case GL_NEAREST, GL_NEAREST_MIPMAP_NEAREST, GL_NEAREST_MIPMAP_LINEAR:
		return GL_NEAREST
	}
	return GL_LINEAR
}

// ---- instances ------------------------------------------------------------------

func (dc *drawCtx) newInstance(sh *glsl.Shader, copies []copyRange, blocks []blockData, pool *sync.Pool) *glsl.Instance {
	var in *glsl.Instance
	if v := pool.Get(); v != nil {
		in = v.(*glsl.Instance)
	} else {
		in = sh.NewInstance()
	}
	r := in.R
	st := dc.p.store
	for _, cp := range copies {
		copy(r[cp.reg:cp.reg+cp.n], st[cp.store:cp.store+cp.n])
	}
	for _, b := range blocks {
		blk := sh.Blocks[b.idx]
		for _, m := range blk.Members {
			loadStd140(r, m.Off, m.T, b.data, m.Byte)
		}
	}
	if sh.DepthRangeOff >= 0 {
		r[sh.DepthRangeOff] = math.Float32bits(dc.dr[0])
		r[sh.DepthRangeOff+1] = math.Float32bits(dc.dr[1])
		r[sh.DepthRangeOff+2] = math.Float32bits(dc.dr[1] - dc.dr[0])
	}
	in.Tex = dc.tex
	in.Quad = nil
	return in
}

func parallelFor(n, minChunk int, fn func(lo, hi int)) {
	w := runtime.GOMAXPROCS(0)
	if w > 8 {
		w = 8
	}
	if n <= minChunk || w <= 1 {
		fn(0, n)
		return
	}
	chunk := (n + w - 1) / w
	if chunk < minChunk {
		chunk = minChunk
	}
	var wg sync.WaitGroup
	for lo := 0; lo < n; lo += chunk {
		hi := min(lo+chunk, n)
		wg.Add(1)
		go func(lo, hi int) {
			defer wg.Done()
			fn(lo, hi)
		}(lo, hi)
	}
	wg.Wait()
}

const triBatch = 250000

// run executes the draw: vertex processing per instance, assembly, raster.
func (dc *drawCtx) run(mode uint32, ids []uint32, restart bool, rIdx uint32, instances, first int, indexed bool, maxID uint32) {
	c := dc.c
	p := dc.p
	// unique vertices
	var uniq []uint32
	slot := make([]int32, 0)
	var smap map[uint32]int32
	minID := ids[0]
	for _, v := range ids {
		if restart && v == rIdx {
			continue
		}
		if v < minID {
			minID = v
		}
	}
	rng := int(maxID-minID) + 1
	if rng <= 4*len(ids)+1024 {
		slot = make([]int32, rng)
		for i := range slot {
			slot[i] = -1
		}
		for _, v := range ids {
			if restart && v == rIdx {
				continue
			}
			if slot[v-minID] < 0 {
				slot[v-minID] = int32(len(uniq))
				uniq = append(uniq, v)
			}
		}
	} else {
		smap = map[uint32]int32{}
		for _, v := range ids {
			if restart && v == rIdx {
				continue
			}
			if _, ok := smap[v]; !ok {
				smap[v] = int32(len(uniq))
				uniq = append(uniq, v)
			}
		}
	}
	lookup := func(v uint32) int32 {
		if smap != nil {
			return smap[v]
		}
		return slot[v-minID]
	}
	// primitive index stream (slot ids) with restart as -1
	stream := make([]int32, len(ids))
	for i, v := range ids {
		if restart && v == rIdx {
			stream[i] = -1
		} else {
			stream[i] = lookup(v)
		}
	}
	var tris []tri
	var sets []*vtxSet
	flush := func() {
		if len(tris) > 0 && !dc.discard {
			dc.rasterize(tris, sets)
		}
		tris = tris[:0]
		sets = sets[:0]
	}
	for inst := 0; inst < instances; inst++ {
		set := dc.vertexStage(uniq, inst)
		si := int32(len(sets))
		sets = append(sets, set)
		dc.assemble(mode, stream, set, si, &tris, inst)
		if len(tris) > triBatch {
			flush()
			// keep only the current set alive for following assembly
		}
		if len(sets) > 1 && len(tris) == 0 {
			sets = sets[:0]
		}
	}
	flush()
	_ = p
	_ = c
	_ = first
	_ = indexed
}

// ---- vertex stage -----------------------------------------------------------------

func (dc *drawCtx) vertexStage(uniq []uint32, inst int) *vtxSet {
	c := dc.c
	p := dc.p
	vs := dc.vs
	n := len(uniq)
	set := &vtxSet{}
	set.grow(n, dc.nvf, dc.nvi)
	if c.tf.Active && !c.tf.Paused {
		set.tfn = p.tfLanes
		set.tf = make([]uint32, n*p.tfLanes)
	}
	vao := c.vao
	// snapshot attribute state
	type afetch struct {
		at      *Attrib
		a       *ActiveAttrib
		col     int
		generic [4]uint32
		gtyp    uint32
	}
	var fetches []afetch
	for ai := range p.Attribs {
		a := &p.Attribs[ai]
		for col := 0; col < a.Cols; col++ {
			loc := a.Loc + col
			fetches = append(fetches, afetch{at: &vao.Attribs[loc], a: a, col: col, generic: c.generic[loc], gtyp: c.genericTyp[loc]})
		}
	}
	vp := dc.vp
	dr := dc.dr
	W, H := float32(vp[2]), float32(vp[3])
	parallelFor(n, 256, func(lo, hi int) {
		in := dc.newInstance(vs, p.vsCopy, dc.blocksVS, &p.pools.vs)
		r := in.R
		if vs.InstanceIDOff >= 0 {
			r[vs.InstanceIDOff] = uint32(inst)
		}
		for i := lo; i < hi; i++ {
			vid := uniq[i]
			if vs.VertexIDOff >= 0 {
				r[vs.VertexIDOff] = vid
			}
			for fi := range fetches {
				f := &fetches[fi]
				rows := f.a.Lanes / (max(1, f.a.Cols) * max(1, f.a.Size))
				if rows == 0 {
					rows = f.a.Lanes
				}
				dst := f.a.Off + f.col*rows
				if !f.at.Enabled {
					for k := 0; k < rows; k++ {
						r[dst+k] = f.generic[k]
					}
					if f.a.Kind == AKFloat && f.gtyp != GL_FLOAT {
						for k := 0; k < rows; k++ {
							if f.gtyp == GL_INT {
								r[dst+k] = math.Float32bits(float32(int32(f.generic[k])))
							} else {
								r[dst+k] = math.Float32bits(float32(f.generic[k]))
							}
						}
					}
					continue
				}
				idx := int(vid)
				if f.at.Divisor > 0 {
					idx = inst / f.at.Divisor
				}
				fetchAttrib(f.at, idx, f.a.Kind, rows, r[dst:dst+rows])
			}
			in.Run()
			// outputs
			po := vs.PositionOff
			x, y, z, w := math.Float32frombits(r[po]), math.Float32frombits(r[po+1]), math.Float32frombits(r[po+2]), math.Float32frombits(r[po+3])
			set.pos[i*4], set.pos[i*4+1], set.pos[i*4+2], set.pos[i*4+3] = x, y, z, w
			if vs.WritesPointSize {
				set.psz[i] = math.Float32frombits(r[vs.PointSizeOff])
			} else {
				set.psz[i] = 1
			}
			for k, o := range dc.vsF {
				set.vf[i*dc.nvf+k] = math.Float32frombits(r[o])
			}
			for k, o := range dc.vsI {
				set.vi[i*dc.nvi+k] = r[o]
			}
			set.scr[i] = screenVtx(x, y, z, w, vp, dr, W, H)
			if set.tfn > 0 {
				pos := i * set.tfn
				for _, o := range p.tfOuts {
					copy(set.tf[pos:pos+o.n], r[o.off:o.off+o.n])
					pos += o.n
				}
			}
		}
		p.pools.vs.Put(in)
	})
	return set
}

const (
	cNearBit = 1 << iota
	cFarBit
	cLeft
	cRight
	cBottom
	cTop
)

func screenVtx(x, y, z, w float32, vp [4]int, dr [2]float32, W, H float32) scrVtx {
	var code uint8
	if z < -w {
		code |= cNearBit
	}
	if z > w {
		code |= cFarBit
	}
	if x < -w {
		code |= cLeft
	}
	if x > w {
		code |= cRight
	}
	if y < -w {
		code |= cBottom
	}
	if y > w {
		code |= cTop
	}
	s := scrVtx{code: code}
	if w <= 1e-30 || w != w {
		s.code |= cNearBit | 0x80
		return s
	}
	iw := 1 / w
	nx, ny, nz := x*iw, y*iw, z*iw
	sx := float64(vp[0]) + (float64(nx)*0.5+0.5)*float64(W)
	sy := float64(vp[1]) + (float64(ny)*0.5+0.5)*float64(H)
	// guard band: huge coordinates need clipping
	if math.Abs(sx) > 1e6 || math.Abs(sy) > 1e6 {
		s.code |= 0x40
		return s
	}
	s.X = int64(math.Floor(sx*256 + 0.5))
	s.Y = int64(math.Floor(sy*256 + 0.5))
	zw := float32(dr[0]) + (dr[1]-dr[0])*(nz*0.5+0.5)
	s.Z = zw
	s.IW = iw
	return s
}

func fetchAttrib(at *Attrib, idx int, kind Kind, rows int, out []uint32) {
	stride := at.Stride
	es := attribSize(at)
	if stride == 0 {
		stride = es
	}
	off := at.Offset + stride*idx
	d := at.Buf.Data
	var vals [4]float32
	var ivals [4]int32
	n := at.Size
	le16 := func(o int) uint16 { return binary.LittleEndian.Uint16(d[o:]) }
	le32 := func(o int) uint32 { return binary.LittleEndian.Uint32(d[o:]) }
	integer := at.Integer
	if at.Type == GL_INT_2_10_10_10_REV || at.Type == GL_UNSIGNED_INT_2_10_10_10_REV {
		v := le32(off)
		n = 4
		if at.Type == GL_INT_2_10_10_10_REV {
			s10 := func(x uint32) int32 { return int32(x<<22) >> 22 }
			iv := [4]int32{s10(v & 1023), s10(v >> 10 & 1023), s10(v >> 20 & 1023), int32(v>>30) << 30 >> 30}
			ivals = iv
			for k := 0; k < 3; k++ {
				vals[k] = float32(iv[k])
				if at.Normalized {
					vals[k] = max(float32(iv[k])/511, -1)
				}
			}
			vals[3] = float32(iv[3])
			if at.Normalized {
				vals[3] = max(float32(iv[3]), -1)
			}
		} else {
			iv := [4]int32{int32(v & 1023), int32(v >> 10 & 1023), int32(v >> 20 & 1023), int32(v >> 30)}
			ivals = iv
			for k := 0; k < 3; k++ {
				vals[k] = float32(iv[k])
				if at.Normalized {
					vals[k] = float32(iv[k]) / 1023
				}
			}
			vals[3] = float32(iv[3])
			if at.Normalized {
				vals[3] = float32(iv[3]) / 3
			}
		}
	} else {
		for k := 0; k < n; k++ {
			switch at.Type {
			case GL_FLOAT:
				vals[k] = math.Float32frombits(le32(off + k*4))
			case GL_BYTE:
				v := int8(d[off+k])
				ivals[k] = int32(v)
				vals[k] = float32(v)
				if at.Normalized {
					vals[k] = max(float32(v)/127, -1)
				}
			case GL_UNSIGNED_BYTE:
				v := d[off+k]
				ivals[k] = int32(v)
				vals[k] = float32(v)
				if at.Normalized {
					vals[k] = float32(v) / 255
				}
			case GL_SHORT:
				v := int16(le16(off + k*2))
				ivals[k] = int32(v)
				vals[k] = float32(v)
				if at.Normalized {
					vals[k] = max(float32(v)/32767, -1)
				}
			case GL_UNSIGNED_SHORT:
				v := le16(off + k*2)
				ivals[k] = int32(v)
				vals[k] = float32(v)
				if at.Normalized {
					vals[k] = float32(v) / 65535
				}
			case GL_INT:
				v := int32(le32(off + k*4))
				ivals[k] = v
				vals[k] = float32(v)
				if at.Normalized {
					vals[k] = max(float32(float64(v)/2147483647), -1)
				}
			case GL_UNSIGNED_INT:
				v := le32(off + k*4)
				ivals[k] = int32(v)
				vals[k] = float32(v)
				if at.Normalized {
					vals[k] = float32(float64(v) / 4294967295)
				}
			case GL_HALF_FLOAT:
				vals[k] = halfToF32(le16(off + k*2))
			case GL_FIXED:
				vals[k] = float32(int32(le32(off+k*4))) / 65536
			}
		}
	}
	for k := 0; k < rows; k++ {
		if kind == AKFloat {
			var v float32
			if k < n {
				v = vals[k]
				if integer {
					v = float32(ivals[k])
				}
			} else if k == 3 {
				v = 1
			}
			out[k] = math.Float32bits(v)
		} else {
			var v int32
			if k < n {
				v = ivals[k]
				if !integer && at.Type == GL_FLOAT {
					v = int32(vals[k])
				}
			} else if k == 3 {
				v = 1
			}
			out[k] = uint32(v)
		}
	}
}

// ---- primitive assembly ------------------------------------------------------------

type tri struct {
	X, Y         [3]int64
	Z, IW        [3]float32
	V            [3]int32
	flat         int32
	set          int32
	A, B, C      [3]int64
	invArea      float64
	area2        int64
	minX, minY   int32
	maxX, maxY   int32
	front        bool
	kind         uint8 // 0 tri, 1 line, 2 point
	pcx, pcy, ps float32
	dzdx, dzdy   float32
	tl           [3]int64 // fill-rule bias per edge
}

func (dc *drawCtx) assemble(mode uint32, stream []int32, set *vtxSet, si int32, tris *[]tri, inst int) {
	n := len(stream)
	emitTri := func(a, b, c int32, flat int32) {
		dc.tfCapture(set, a, b, c, 3)
		if dc.discard {
			return
		}
		dc.setupTri(set, si, a, b, c, flat, tris)
	}
	emitLine := func(a, b int32) {
		dc.tfCapture(set, a, b, -1, 2)
		if dc.discard {
			return
		}
		dc.setupLine(set, si, a, b, tris)
	}
	emitPoint := func(a int32) {
		dc.tfCapture(set, a, -1, -1, 1)
		if dc.discard {
			return
		}
		dc.setupPoint(set, si, a, tris)
	}
	switch mode {
	case GL_POINTS:
		for i := 0; i < n; i++ {
			if stream[i] >= 0 {
				emitPoint(stream[i])
			}
		}
	case GL_LINES:
		for i := 0; i+1 < n; i += 2 {
			if stream[i] >= 0 && stream[i+1] >= 0 {
				emitLine(stream[i], stream[i+1])
			}
		}
	case GL_LINE_STRIP, GL_LINE_LOOP:
		start := 0
		for i := 0; i <= n; i++ {
			if i == n || stream[i] < 0 {
				seg := stream[start:i]
				for k := 0; k+1 < len(seg); k++ {
					emitLine(seg[k], seg[k+1])
				}
				if mode == GL_LINE_LOOP && len(seg) > 2 {
					emitLine(seg[len(seg)-1], seg[0])
				}
				start = i + 1
			}
		}
	case GL_TRIANGLES:
		for i := 0; i+2 < n; i += 3 {
			a, b, c := stream[i], stream[i+1], stream[i+2]
			if a >= 0 && b >= 0 && c >= 0 {
				emitTri(a, b, c, c)
			}
		}
	case GL_TRIANGLE_STRIP:
		start := 0
		for i := 0; i <= n; i++ {
			if i == n || stream[i] < 0 {
				seg := stream[start:i]
				for k := 0; k+2 < len(seg); k++ {
					if k%2 == 0 {
						emitTri(seg[k], seg[k+1], seg[k+2], seg[k+2])
					} else {
						emitTri(seg[k+1], seg[k], seg[k+2], seg[k+2])
					}
				}
				start = i + 1
			}
		}
	case GL_TRIANGLE_FAN:
		start := 0
		for i := 0; i <= n; i++ {
			if i == n || stream[i] < 0 {
				seg := stream[start:i]
				for k := 1; k+1 < len(seg); k++ {
					emitTri(seg[0], seg[k], seg[k+1], seg[k+1])
				}
				start = i + 1
			}
		}
	}
	_ = inst
}

// tfCapture records transform feedback output for a primitive.
func (dc *drawCtx) tfCapture(set *vtxSet, a, b, c int32, nv int) {
	cx := dc.c
	if !cx.tf.Active || cx.tf.Paused {
		return
	}
	p := dc.p
	if len(p.tfOuts) == 0 {
		return
	}
	vs := []int32{a, b, c}[:nv]
	for _, v := range vs {
		dc.tfWriteVertex(set, v)
	}
	cx.tf.Written++
	for _, q := range []uint32{GL_TRANSFORM_FEEDBACK_PRIMITIVES_WRITTEN} {
		if qq := cx.activeQ[q]; qq != nil {
			qq.Result++
		}
	}
}

func (dc *drawCtx) tfWriteVertex(set *vtxSet, v int32) {
	cx := dc.c
	p := dc.p
	// Need the vertex's raw output lanes: they live in vf/vi only for varyings
	// consumed by the fragment shader, so capture reads from a side table.
	lanes := dc.tfLanes(set, v)
	pos := 0
	for oi, o := range p.tfOuts {
		bi := 0
		if p.tfMode == GL_SEPARATE_ATTRIBS {
			bi = oi
		}
		b := cx.tf.Bufs[bi]
		w := cx.tfWritePos[bi]
		for k := 0; k < o.n; k++ {
			if w+4 <= len(b.Buf.Data) {
				binary.LittleEndian.PutUint32(b.Buf.Data[w:], lanes[pos+k])
			}
			w += 4
		}
		cx.tfWritePos[bi] = w
		pos += o.n
	}
}

// ---- triangle setup ----------------------------------------------------------------

func (dc *drawCtx) setupTri(set *vtxSet, si int32, a, b, c int32, flat int32, out *[]tri) {
	sa, sb, sc := set.scr[a], set.scr[b], set.scr[c]
	if sa.code&sb.code&sc.code&0x3F != 0 {
		return // trivially rejected
	}
	if (sa.code|sb.code|sc.code)&(cNearBit|cFarBit|0x40|0x80) != 0 || dc.needsGuardClip(sa, sb, sc) {
		dc.clipTri(set, si, a, b, c, flat, out)
		return
	}
	dc.finishTri(set, si, [3]int32{a, b, c}, [3]scrVtx{sa, sb, sc}, flat, out, 0)
}

func (dc *drawCtx) needsGuardClip(sa, sb, sc scrVtx) bool {
	lim := int64(1 << 30)
	for _, s := range [3]scrVtx{sa, sb, sc} {
		if s.X > lim || s.X < -lim || s.Y > lim || s.Y < -lim {
			return true
		}
	}
	return false
}

// finishTri builds the edge equations and appends the triangle.
func (dc *drawCtx) finishTri(set *vtxSet, si int32, v [3]int32, s [3]scrVtx, flat int32, out *[]tri, kind uint8) {
	area2 := (s[1].X-s[0].X)*(s[2].Y-s[0].Y) - (s[2].X-s[0].X)*(s[1].Y-s[0].Y)
	if area2 == 0 {
		return
	}
	front := (area2 > 0) == dc.frontCCW
	if kind == 0 && dc.cull {
		switch dc.cullMode {
		case GL_FRONT_AND_BACK:
			return
		case GL_FRONT:
			if front {
				return
			}
		case GL_BACK:
			if !front {
				return
			}
		}
	}
	if kind != 0 {
		front = true
	}
	if area2 < 0 {
		v[1], v[2] = v[2], v[1]
		s[1], s[2] = s[2], s[1]
		area2 = -area2
	}
	t := tri{V: v, flat: flat, set: si, area2: area2, front: front, kind: kind}
	for i := 0; i < 3; i++ {
		t.X[i], t.Y[i], t.Z[i], t.IW[i] = s[i].X, s[i].Y, s[i].Z, s[i].IW
	}
	for i := 0; i < 3; i++ {
		a, b := (i+1)%3, (i+2)%3
		dx, dy := t.X[b]-t.X[a], t.Y[b]-t.Y[a]
		t.A[i] = -dy
		t.B[i] = dx
		t.C[i] = dy*t.X[a] - dx*t.Y[a]
		if dy < 0 || dy == 0 && dx > 0 {
			t.tl[i] = 0
		} else {
			t.tl[i] = 1
		}
	}
	t.invArea = 1 / float64(area2)
	minx, maxx := min(t.X[0], t.X[1], t.X[2]), max(t.X[0], t.X[1], t.X[2])
	miny, maxy := min(t.Y[0], t.Y[1], t.Y[2]), max(t.Y[0], t.Y[1], t.Y[2])
	t.minX, t.maxX = int32(floorDiv(minx, 256)), int32(floorDiv(maxx, 256))
	t.minY, t.maxY = int32(floorDiv(miny, 256)), int32(floorDiv(maxy, 256))
	if t.maxX < 0 || t.maxY < 0 || int(t.minX) >= dc.rt.W || int(t.minY) >= dc.rt.H {
		return
	}
	// depth gradients (per pixel)
	if dc.polyOff {
		inv := t.invArea * 256
		var dzdx, dzdy float64
		for i := 0; i < 3; i++ {
			dzdx += float64(t.Z[i]) * float64(t.A[i]) * inv
			dzdy += float64(t.Z[i]) * float64(t.B[i]) * inv
		}
		t.dzdx, t.dzdy = float32(dzdx), float32(dzdy)
	}
	*out = append(*out, t)
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

// setupLine expands a line into a 1-pixel-wide quad (two triangles).
func (dc *drawCtx) setupLine(set *vtxSet, si int32, a, b int32, out *[]tri) {
	sa, sb := set.scr[a], set.scr[b]
	if sa.code&sb.code&0x3F != 0 {
		return
	}
	if (sa.code|sb.code)&(cNearBit|cFarBit|0x40|0x80) != 0 {
		var ok bool
		a, b, ok = dc.clipLine(set, a, b)
		if !ok {
			return
		}
		sa, sb = set.scr[a], set.scr[b]
	}
	dx, dy := float64(sb.X-sa.X)/256, float64(sb.Y-sa.Y)/256
	l := math.Hypot(dx, dy)
	if l == 0 {
		return
	}
	hw := 0.5
	nx, ny := -dy/l*hw, dx/l*hw
	mk := func(s scrVtx, sx, sy float64) scrVtx {
		r := s
		r.X = s.X + int64(math.Round(sx*256))
		r.Y = s.Y + int64(math.Round(sy*256))
		return r
	}
	c0, c1 := mk(sa, nx, ny), mk(sa, -nx, -ny)
	c2, c3 := mk(sb, -nx, -ny), mk(sb, nx, ny)
	// triangles share attributes of their endpoint
	dc.finishTri(set, si, [3]int32{a, a, b}, [3]scrVtx{c0, c1, c2}, b, out, 1)
	dc.finishTri(set, si, [3]int32{a, b, b}, [3]scrVtx{c0, c2, c3}, b, out, 1)
}

func (dc *drawCtx) setupPoint(set *vtxSet, si int32, a int32, out *[]tri) {
	s := set.scr[a]
	if s.code&0xFF != 0 {
		return
	}
	size := float64(set.psz[a])
	if size < 1 {
		size = 1
	}
	if size > 1024 {
		size = 1024
	}
	h := int64(math.Round(size * 128))
	mk := func(dx, dy int64) scrVtx {
		r := s
		r.X, r.Y = s.X+dx, s.Y+dy
		return r
	}
	p0, p1, p2, p3 := mk(-h, -h), mk(h, -h), mk(h, h), mk(-h, h)
	n0 := len(*out)
	dc.finishTri(set, si, [3]int32{a, a, a}, [3]scrVtx{p0, p1, p2}, a, out, 2)
	dc.finishTri(set, si, [3]int32{a, a, a}, [3]scrVtx{p0, p2, p3}, a, out, 2)
	for i := n0; i < len(*out); i++ {
		t := &(*out)[i]
		t.pcx, t.pcy, t.ps = float32(s.X)/256, float32(s.Y)/256, float32(size)
	}
}

// ---- clipping ------------------------------------------------------------------------

type cvert struct {
	pos [4]float32
	psz float32
	vf  []float32
}

func (dc *drawCtx) loadCV(set *vtxSet, v int32) cvert {
	cv := cvert{psz: set.psz[v]}
	copy(cv.pos[:], set.pos[v*4:v*4+4])
	cv.vf = append([]float32(nil), set.vf[int(v)*dc.nvf:int(v+1)*dc.nvf]...)
	return cv
}

func lerpCV(a, b cvert, t float32) cvert {
	r := cvert{psz: a.psz + (b.psz-a.psz)*t}
	for i := 0; i < 4; i++ {
		r.pos[i] = a.pos[i] + (b.pos[i]-a.pos[i])*t
	}
	r.vf = make([]float32, len(a.vf))
	for i := range a.vf {
		r.vf[i] = a.vf[i] + (b.vf[i]-a.vf[i])*t
	}
	return r
}

// planeDist returns the signed distance of v to clip plane k (>=0 inside).
func planeDist(v cvert, k int) float32 {
	x, y, z, w := v.pos[0], v.pos[1], v.pos[2], v.pos[3]
	const g = 8
	switch k {
	case 0:
		return z + w
	case 1:
		return w - z
	case 2:
		return g*w + x
	case 3:
		return g*w - x
	case 4:
		return g*w + y
	}
	return g*w - y
}

func clipPoly(poly []cvert) []cvert {
	for k := 0; k < 6; k++ {
		if len(poly) == 0 {
			return nil
		}
		var out []cvert
		for i := range poly {
			a, b := poly[i], poly[(i+1)%len(poly)]
			da, db := planeDist(a, k), planeDist(b, k)
			if da >= 0 {
				out = append(out, a)
			}
			if (da >= 0) != (db >= 0) {
				t := da / (da - db)
				out = append(out, lerpCV(a, b, t))
			}
		}
		poly = out
	}
	return poly
}

func (dc *drawCtx) appendCV(set *vtxSet, cv cvert) int32 {
	i := int32(set.n)
	set.n++
	set.pos = append(set.pos, cv.pos[:]...)
	set.psz = append(set.psz, cv.psz)
	set.vf = append(set.vf, cv.vf...)
	set.vi = append(set.vi, make([]uint32, dc.nvi)...)
	W, H := float32(dc.vp[2]), float32(dc.vp[3])
	set.scr = append(set.scr, screenVtx(cv.pos[0], cv.pos[1], cv.pos[2], cv.pos[3], dc.vp, dc.dr, W, H))
	return i
}

func (dc *drawCtx) clipTri(set *vtxSet, si int32, a, b, c int32, flat int32, out *[]tri) {
	poly := clipPoly([]cvert{dc.loadCV(set, a), dc.loadCV(set, b), dc.loadCV(set, c)})
	if len(poly) < 3 {
		return
	}
	idx := make([]int32, len(poly))
	for i, cv := range poly {
		idx[i] = dc.appendCV(set, cv)
	}
	for i := 1; i+1 < len(idx); i++ {
		s := [3]scrVtx{set.scr[idx[0]], set.scr[idx[i]], set.scr[idx[i+1]]}
		if (s[0].code|s[1].code|s[2].code)&0x80 != 0 {
			continue
		}
		dc.finishTri(set, si, [3]int32{idx[0], idx[i], idx[i+1]}, s, flat, out, 0)
	}
}

func (dc *drawCtx) clipLine(set *vtxSet, a, b int32) (int32, int32, bool) {
	va, vb := dc.loadCV(set, a), dc.loadCV(set, b)
	for k := 0; k < 6; k++ {
		da, db := planeDist(va, k), planeDist(vb, k)
		if da < 0 && db < 0 {
			return 0, 0, false
		}
		if da < 0 {
			va = lerpCV(va, vb, da/(da-db))
		} else if db < 0 {
			vb = lerpCV(va, vb, da/(da-db))
		}
	}
	return dc.appendCV(set, va), dc.appendCV(set, vb), true
}
