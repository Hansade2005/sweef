package gl

import (
	"iter"
	"math"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/sweefbrowser/sweef/internal/gl/glsl"
)

const tileSize = 32

var sampleOff4 = [4][2]int64{{-32, 96}, {96, 32}, {-96, -32}, {32, -96}}

type stopSignal struct{}

// laneCo runs one fragment invocation as a coroutine so that derivative and
// implicit-LOD lookups can synchronise across a 2x2 quad.
type laneCo struct {
	inst     *glsl.Instance
	next     func() (struct{}, bool)
	stop     func()
	syncIn   [4]float32
	syncN    int
	site     int
	hasSync  bool
	dx, dy   [4]float32
	finished bool
	disc     bool
}

func newLaneCo(inst *glsl.Instance) *laneCo {
	lc := &laneCo{inst: inst}
	seq := func(yield func(struct{}) bool) {
		defer func() {
			if r := recover(); r != nil {
				if _, ok := r.(stopSignal); !ok {
					panic(r)
				}
			}
		}()
		inst.Quad = func(site int, in []float32) ([]float32, []float32) {
			lc.site = site
			lc.syncN = copy(lc.syncIn[:], in)
			lc.hasSync = true
			if !yield(struct{}{}) {
				panic(stopSignal{})
			}
			return lc.dx[:lc.syncN], lc.dy[:lc.syncN]
		}
		for {
			lc.disc = inst.Run()
			lc.finished = true
			if !yield(struct{}{}) {
				return
			}
		}
	}
	lc.next, lc.stop = iter.Pull(seq)
	return lc
}

type fragWorker struct {
	dc   *drawCtx
	tris []tri
	sets []*vtxSet
	one  *glsl.Instance
	co   [4]*laneCo
	occ  uint64
	// per-quad scratch
	px [4]pxState
}

type pxState struct {
	x, y   int
	inside bool // in fb bounds
	mask   uint8
	pass   uint8
	e      [3]int64 // edge values at the center
	se     [3]int64 // edge values at the shading position
	z      [4]float32
	out    fragResult
	ok     bool
}

type fragResult struct {
	col      [8][4]uint32
	depth    float32
	hasDepth bool
	discard  bool
}

func (dc *drawCtx) rasterize(tris []tri, sets []*vtxSet) {
	rt := dc.rt
	nx := (rt.W + tileSize - 1) / tileSize
	ny := (rt.H + tileSize - 1) / tileSize
	sx0, sy0, sx1, sy1 := 0, 0, rt.W, rt.H
	if dc.scissorOn {
		sx0, sy0 = max(sx0, dc.scissor[0]), max(sy0, dc.scissor[1])
		sx1, sy1 = min(sx1, dc.scissor[0]+dc.scissor[2]), min(sy1, dc.scissor[1]+dc.scissor[3])
	}
	if sx1 <= sx0 || sy1 <= sy0 {
		return
	}
	counts := make([]int32, nx*ny+1)
	tileRange := func(t *tri) (tx0, ty0, tx1, ty1 int, ok bool) {
		x0, x1 := max(int(t.minX), sx0), min(int(t.maxX), sx1-1)
		y0, y1 := max(int(t.minY), sy0), min(int(t.maxY), sy1-1)
		if x0 > x1 || y0 > y1 {
			return 0, 0, 0, 0, false
		}
		return x0 / tileSize, y0 / tileSize, x1 / tileSize, y1 / tileSize, true
	}
	for i := range tris {
		tx0, ty0, tx1, ty1, ok := tileRange(&tris[i])
		if !ok {
			continue
		}
		for ty := ty0; ty <= ty1; ty++ {
			for tx := tx0; tx <= tx1; tx++ {
				counts[ty*nx+tx+1]++
			}
		}
	}
	for i := 1; i < len(counts); i++ {
		counts[i] += counts[i-1]
	}
	bins := make([]int32, counts[len(counts)-1])
	fill := make([]int32, nx*ny)
	for i := range tris {
		tx0, ty0, tx1, ty1, ok := tileRange(&tris[i])
		if !ok {
			continue
		}
		for ty := ty0; ty <= ty1; ty++ {
			for tx := tx0; tx <= tx1; tx++ {
				k := ty*nx + tx
				bins[counts[k]+fill[k]] = int32(i)
				fill[k]++
			}
		}
	}
	var work []int32
	for k := 0; k < nx*ny; k++ {
		if counts[k+1] > counts[k] {
			work = append(work, int32(k))
		}
	}
	if len(work) == 0 {
		return
	}
	nw := runtime.GOMAXPROCS(0)
	if nw > 8 {
		nw = 8
	}
	if nw > len(work) {
		nw = len(work)
	}
	var next atomic.Int32
	var wg sync.WaitGroup
	run := func() {
		fw := &fragWorker{dc: dc, tris: tris, sets: sets}
		fw.init()
		for {
			wi := int(next.Add(1)) - 1
			if wi >= len(work) {
				break
			}
			k := int(work[wi])
			tx, ty := k%nx, k/nx
			fw.tile(tx*tileSize, ty*tileSize, min((tx+1)*tileSize, rt.W), min((ty+1)*tileSize, rt.H), sx0, sy0, sx1, sy1, bins[counts[k]:counts[k+1]])
		}
		fw.close()
		dc.occ.Add(fw.occ)
	}
	if nw <= 1 {
		run()
		return
	}
	for i := 0; i < nw; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			run()
		}()
	}
	wg.Wait()
}

func (fw *fragWorker) init() {
	dc := fw.dc
	p := dc.p
	n := 1
	if dc.needQuad {
		n = 4
	}
	for i := 0; i < n; i++ {
		in := dc.newInstance(dc.fs, p.fsCopy, dc.blocksFS, &p.pools.fs)
		if i == 0 {
			fw.one = in
		}
		if dc.needQuad {
			fw.co[i] = newLaneCo(in)
		}
	}
}

func (fw *fragWorker) close() {
	dc := fw.dc
	if dc.needQuad {
		for _, lc := range fw.co {
			if lc != nil {
				lc.stop()
				lc.inst.Quad = nil
				dc.p.pools.fs.Put(lc.inst)
			}
		}
	} else if fw.one != nil {
		dc.p.pools.fs.Put(fw.one)
	}
}

// coverage computes the covered sample mask for pixel (x,y).
func (fw *fragWorker) coverage(t *tri, st *pxState) {
	x, y := st.x, st.y
	cx, cy := int64(x)*256+128, int64(y)*256+128
	for i := 0; i < 3; i++ {
		st.e[i] = t.A[i]*cx + t.B[i]*cy + t.C[i]
	}
	st.se = st.e
	S := fw.dc.samples
	if S == 1 {
		if st.e[0]-t.tl[0] >= 0 && st.e[1]-t.tl[1] >= 0 && st.e[2]-t.tl[2] >= 0 {
			st.mask = 1
		} else {
			st.mask = 0
		}
		return
	}
	var m uint8
	first := -1
	for s := 0; s < S; s++ {
		ox, oy := sampleOff4[s][0], sampleOff4[s][1]
		ok := true
		for i := 0; i < 3; i++ {
			if st.e[i]+t.A[i]*ox+t.B[i]*oy-t.tl[i] < 0 {
				ok = false
				break
			}
		}
		if ok {
			m |= 1 << uint(s)
			if first < 0 {
				first = s
			}
		}
	}
	st.mask = m
	// shade at the center when covered, else at the first covered sample
	if m != 0 && !(st.e[0]-t.tl[0] >= 0 && st.e[1]-t.tl[1] >= 0 && st.e[2]-t.tl[2] >= 0) && first >= 0 {
		ox, oy := sampleOff4[first][0], sampleOff4[first][1]
		for i := 0; i < 3; i++ {
			st.se[i] = st.e[i] + t.A[i]*ox + t.B[i]*oy
		}
	}
}

func (fw *fragWorker) tile(tx0, ty0, tx1, ty1, sx0, sy0, sx1, sy1 int, bin []int32) {
	dc := fw.dc
	for _, ti := range bin {
		t := &fw.tris[ti]
		x0, x1 := max(int(t.minX), max(tx0, sx0)), min(int(t.maxX), min(tx1, sx1)-1)
		y0, y1 := max(int(t.minY), max(ty0, sy0)), min(int(t.maxY), min(ty1, sy1)-1)
		if x0 > x1 || y0 > y1 {
			continue
		}
		set := fw.sets[t.set]
		if !dc.needQuad {
			st := &fw.px[0]
			for y := y0; y <= y1; y++ {
				for x := x0; x <= x1; x++ {
					st.x, st.y = x, y
					fw.coverage(t, st)
					if st.mask == 0 {
						continue
					}
					fw.processPixels(t, set, 1)
				}
			}
			continue
		}
		for qy := y0 &^ 1; qy <= y1; qy += 2 {
			for qx := x0 &^ 1; qx <= x1; qx += 2 {
				any := false
				for k := 0; k < 4; k++ {
					st := &fw.px[k]
					st.x, st.y = qx+(k&1), qy+(k>>1)
					st.inside = st.x >= x0 && st.x <= x1 && st.y >= y0 && st.y <= y1 && st.x < tx1 && st.y < ty1
					fw.coverage(t, st)
					if !st.inside {
						st.mask = 0
					}
					if st.mask != 0 {
						any = true
					}
				}
				if any {
					fw.processPixels(t, set, 4)
				}
			}
		}
	}
}

// processPixels runs depth tests, shading and output merging for 1 pixel
// (n==1, state in px[0]) or a quad (n==4).
func (fw *fragWorker) processPixels(t *tri, set *vtxSet, n int) {
	dc := fw.dc
	rt := dc.rt
	anyPass := false
	for k := 0; k < n; k++ {
		st := &fw.px[k]
		st.pass = 0
		if st.mask == 0 {
			continue
		}
		fw.sampleDepths(t, st)
		st.pass = st.mask
		if dc.earlyZ && (dc.depthTest || dc.stencilTest) {
			st.pass = fw.depthStencil(t, st, st.z, st.mask, true)
		}
		if st.pass != 0 {
			anyPass = true
		}
	}
	if !anyPass {
		return
	}
	// shade
	if n == 1 {
		st := &fw.px[0]
		fw.setInputs(fw.one, t, set, st)
		disc := fw.one.Run()
		fw.collect(fw.one, &st.out, disc)
	} else {
		for k := 0; k < 4; k++ {
			fw.setInputs(fw.co[k].inst, t, set, &fw.px[k])
			fw.co[k].finished = false
		}
		fw.runQuad()
		for k := 0; k < 4; k++ {
			fw.collect(fw.co[k].inst, &fw.px[k].out, fw.co[k].disc)
		}
	}
	_ = rt
	for k := 0; k < n; k++ {
		st := &fw.px[k]
		if st.pass == 0 || st.out.discard {
			continue
		}
		pass := st.pass
		z := st.z
		if st.out.hasDepth {
			for s := 0; s < dc.samples; s++ {
				z[s] = quantDepthTo(dc, st.out.depth)
			}
			if !dc.earlyZ {
				// depth value replaced: no polygon offset
			}
		}
		if !dc.earlyZ && (dc.depthTest || dc.stencilTest) {
			pass = fw.depthStencil(t, st, z, st.mask, true)
		}
		if pass == 0 {
			continue
		}
		fw.occ += uint64(popcount8(pass))
		fw.writeColors(st, pass)
	}
}

func popcount8(m uint8) int {
	n := 0
	for ; m != 0; m &= m - 1 {
		n++
	}
	return n
}

func quantDepthTo(dc *drawCtx, z float32) float32 {
	if dc.rt.depth != nil && dc.rt.depth.Fmt.IsDepth() {
		return quantDepth(dc.rt.depth.Fmt, z)
	}
	return z
}

func (fw *fragWorker) runQuad() {
	for {
		allDone := true
		for k := 0; k < 4; k++ {
			lc := fw.co[k]
			if lc.finished {
				lc.hasSync = false
				continue
			}
			lc.hasSync = false
			lc.next()
			if !lc.finished {
				allDone = false
			}
		}
		if allDone {
			return
		}
		// derivatives from the lanes that reached this synchronisation
		// point; lanes that diverged contribute nothing (zero derivative)
		for k := 0; k < 4; k++ {
			lc := fw.co[k]
			if lc.finished || !lc.hasSync {
				continue
			}
			px, py := k^1, k^2
			for i := 0; i < lc.syncN; i++ {
				var dx, dy float32
				if p := fw.co[px]; p.hasSync && p.site == lc.site && p.syncN > i {
					if k&1 == 0 {
						dx = p.syncIn[i] - lc.syncIn[i]
					} else {
						dx = lc.syncIn[i] - p.syncIn[i]
					}
				}
				if p := fw.co[py]; p.hasSync && p.site == lc.site && p.syncN > i {
					if k < 2 {
						dy = p.syncIn[i] - lc.syncIn[i]
					} else {
						dy = lc.syncIn[i] - p.syncIn[i]
					}
				}
				lc.dx[i], lc.dy[i] = dx, dy
			}
		}
	}
}

// sampleDepths computes the depth at each sample position.
func (fw *fragWorker) sampleDepths(t *tri, st *pxState) {
	dc := fw.dc
	S := dc.samples
	var off float32
	if dc.polyOff {
		m := float32(math.Max(math.Abs(float64(t.dzdx)), math.Abs(float64(t.dzdy))))
		r := float32(1.0 / 16777215.0)
		if dc.rt.depth != nil && dc.rt.depth.Fmt.DBits == 16 {
			r = 1.0 / 65535.0
		}
		off = dc.polyFactor*m + dc.polyUnits*r
	}
	for s := 0; s < S; s++ {
		e := st.e
		if S > 1 {
			ox, oy := sampleOff4[s][0], sampleOff4[s][1]
			for i := 0; i < 3; i++ {
				e[i] += t.A[i]*ox + t.B[i]*oy
			}
		}
		z := float64(e[0])*float64(t.Z[0]) + float64(e[1])*float64(t.Z[1]) + float64(e[2])*float64(t.Z[2])
		zf := float32(z*t.invArea) + off
		st.z[s] = zf
	}
}

func cmpFunc(f uint32, a, b float32) bool {
	switch f {
	case GL_NEVER:
		return false
	case GL_LESS:
		return a < b
	case GL_EQUAL:
		return a == b
	case GL_LEQUAL:
		return a <= b
	case GL_GREATER:
		return a > b
	case GL_NOTEQUAL:
		return a != b
	case GL_GEQUAL:
		return a >= b
	}
	return true
}

func cmpFuncU(f uint32, a, b uint32) bool {
	switch f {
	case GL_NEVER:
		return false
	case GL_LESS:
		return a < b
	case GL_EQUAL:
		return a == b
	case GL_LEQUAL:
		return a <= b
	case GL_GREATER:
		return a > b
	case GL_NOTEQUAL:
		return a != b
	case GL_GEQUAL:
		return a >= b
	}
	return true
}

func stencilApply(op uint32, v, ref uint8) uint8 {
	switch op {
	case GL_ZERO:
		return 0
	case GL_REPLACE:
		return ref
	case GL_INCR:
		if v < 255 {
			return v + 1
		}
		return v
	case GL_DECR:
		if v > 0 {
			return v - 1
		}
		return v
	case GL_INVERT:
		return ^v
	case GL_INCR_WRAP:
		return v + 1
	case GL_DECR_WRAP:
		return v - 1
	}
	return v
}

// depthStencil performs stencil and depth tests for the samples in mask and
// updates the buffers; returns the mask of passing samples.
func (fw *fragWorker) depthStencil(t *tri, st *pxState, z [4]float32, mask uint8, write bool) uint8 {
	dc := fw.dc
	rt := dc.rt
	var pass uint8
	sf := &dc.stF
	if !t.front {
		sf = &dc.stB
	}
	for s := 0; s < dc.samples; s++ {
		if mask&(1<<uint(s)) == 0 {
			continue
		}
		stOK := true
		if dc.stencilTest {
			im := rt.stencil
			si := im.pixS(st.x, st.y, rt.stencilZ, s)
			cur := im.St[si]
			stOK = cmpFuncU(sf.Func, uint32(uint8(sf.Ref))&sf.Mask&0xFF, uint32(cur)&sf.Mask&0xFF)
			// Reference is the left operand: ref OP stored
			if !stOK {
				if write {
					im.St[si] = stencilApply(sf.Fail, cur, uint8(sf.Ref))&uint8(sf.WMask) | cur&^uint8(sf.WMask)
				}
				continue
			}
		}
		zOK := true
		var zq float32
		if dc.depthTest {
			zq = quantDepth(rt.depth.Fmt, z[s])
			di := rt.depth.idxS(st.x, st.y, rt.depthZ, s)
			zOK = cmpFunc(dc.depthFunc, zq, rt.depth.F[di])
			if zOK && dc.depthMask && write {
				rt.depth.F[di] = zq
			}
		}
		if dc.stencilTest {
			im := rt.stencil
			si := im.pixS(st.x, st.y, rt.stencilZ, s)
			cur := im.St[si]
			op := sf.ZPass
			if !zOK {
				op = sf.ZFail
			}
			if write {
				im.St[si] = stencilApply(op, cur, uint8(sf.Ref))&uint8(sf.WMask) | cur&^uint8(sf.WMask)
			}
		}
		if zOK {
			pass |= 1 << uint(s)
		}
	}
	return pass
}

// setInputs writes interpolated varyings and built-ins into the instance.
func (fw *fragWorker) setInputs(in *glsl.Instance, t *tri, set *vtxSet, st *pxState) {
	dc := fw.dc
	fs := dc.fs
	r := in.R
	l0, l1, l2 := float64(st.se[0])*t.invArea, float64(st.se[1])*t.invArea, float64(st.se[2])*t.invArea
	w0, w1, w2 := l0*float64(t.IW[0]), l1*float64(t.IW[1]), l2*float64(t.IW[2])
	ws := w0 + w1 + w2
	nvf := dc.nvf
	if nvf > 0 {
		inv := 1 / ws
		a, b, c := int(t.V[0])*nvf, int(t.V[1])*nvf, int(t.V[2])*nvf
		vf := set.vf
		for k, o := range dc.fsF {
			v := (w0*float64(vf[a+k]) + w1*float64(vf[b+k]) + w2*float64(vf[c+k])) * inv
			r[o] = math.Float32bits(float32(v))
		}
	}
	if dc.nvi > 0 {
		fb := int(t.flat) * dc.nvi
		for k, o := range dc.fsI {
			r[o] = set.vi[fb+k]
		}
	}
	if fs.FragCoordOff >= 0 {
		// values at the pixel center
		c0, c1, c2 := float64(st.e[0])*t.invArea, float64(st.e[1])*t.invArea, float64(st.e[2])*t.invArea
		z := c0*float64(t.Z[0]) + c1*float64(t.Z[1]) + c2*float64(t.Z[2])
		wsum := c0*float64(t.IW[0]) + c1*float64(t.IW[1]) + c2*float64(t.IW[2])
		o := fs.FragCoordOff
		r[o] = math.Float32bits(float32(st.x) + 0.5)
		r[o+1] = math.Float32bits(float32(st.y) + 0.5)
		r[o+2] = math.Float32bits(float32(z))
		r[o+3] = math.Float32bits(float32(wsum))
	}
	if fs.FrontFacingOff >= 0 {
		if t.front {
			r[fs.FrontFacingOff] = 1
		} else {
			r[fs.FrontFacingOff] = 0
		}
	}
	if fs.PointCoordOff >= 0 && t.kind == 2 {
		o := fs.PointCoordOff
		half := t.ps / 2
		r[o] = math.Float32bits((float32(st.x) + 0.5 - (t.pcx - half)) / t.ps)
		r[o+1] = math.Float32bits(((t.pcy + half) - (float32(st.y) + 0.5)) / t.ps)
	}
}

func (fw *fragWorker) collect(in *glsl.Instance, out *fragResult, disc bool) {
	dc := fw.dc
	out.discard = disc
	if disc {
		return
	}
	r := in.R
	for _, o := range dc.outs {
		switch {
		case o.bcast:
			for b := 0; b < 8; b++ {
				copy(out.col[b][:], r[o.off:o.off+4])
			}
		case o.n > 0: // gl_FragData array
			for b := 0; b < o.n && b < 8; b++ {
				copy(out.col[b][:], r[o.off+b*4:o.off+b*4+4])
			}
		default:
			if o.buf < 8 {
				out.col[o.buf] = [4]uint32{}
				copy(out.col[o.buf][:o.comp], r[o.off:o.off+o.comp])
				if o.kind == fkFloat {
					for k := o.comp; k < 4; k++ {
						out.col[o.buf][k] = 0
					}
					if o.comp < 4 {
						out.col[o.buf][3] = math.Float32bits(1)
					}
				} else if o.comp < 4 {
					out.col[o.buf][3] = 1
				}
			}
		}
	}
	if dc.fragDepth {
		out.depth = math.Float32frombits(r[dc.fs.FragDepthOff])
		if out.depth < 0 {
			out.depth = 0
		} else if out.depth > 1 {
			out.depth = 1
		}
		out.hasDepth = true
	}
}

func (fw *fragWorker) writeColors(st *pxState, pass uint8) {
	dc := fw.dc
	rt := dc.rt
	for b := 0; b < rt.maxColor; b++ {
		im := rt.colors[b]
		if im == nil || !rt.draw[b] {
			continue
		}
		cm := dc.colorMask
		if rt.noAlpha {
			cm[3] = false
		}
		if !cm[0] && !cm[1] && !cm[2] && !cm[3] {
			continue
		}
		src := st.out.col[b]
		z := rt.colorZ[b]
		switch im.Fmt.Class {
		case ClsInt:
			for s := 0; s < dc.samples; s++ {
				if pass&(1<<uint(s)) == 0 {
					continue
				}
				o := im.idxS(st.x, st.y, z, s)
				for k := 0; k < 4; k++ {
					if cm[k] {
						im.I[o+k] = int32(src[k])
					}
				}
			}
		default:
			var c [4]float32
			for k := 0; k < 4; k++ {
				c[k] = math.Float32frombits(src[k])
			}
			f := im.Fmt
			if f.Class == ClsU8 {
				for k := 0; k < 4; k++ {
					c[k] = clampf(c[k], 0, 1)
				}
			}
			for s := 0; s < dc.samples; s++ {
				if pass&(1<<uint(s)) == 0 {
					continue
				}
				o := im.idxS(st.x, st.y, z, s)
				res := c
				if dc.blend {
					res = dc.blendPx(c, loadDst(im, o))
					if f.Class == ClsU8 {
						for k := 0; k < 4; k++ {
							res[k] = clampf(res[k], 0, 1)
						}
					}
				}
				if cm[0] && cm[1] && cm[2] && cm[3] && f.Class == ClsU8 && !f.SRGB && f.Bits[0] == 8 && f.Bits[3] == 8 {
					im.U8[o], im.U8[o+1], im.U8[o+2], im.U8[o+3] = unormToByte(res[0]), unormToByte(res[1]), unormToByte(res[2]), unormToByte(res[3])
					continue
				}
				if cm[0] && cm[1] && cm[2] && cm[3] {
					im.setFloat(o, res)
					continue
				}
				var cur [4]float32
				switch f.Class {
				case ClsU8:
					cur = [4]float32{float32(im.U8[o]) / 255, float32(im.U8[o+1]) / 255, float32(im.U8[o+2]) / 255, float32(im.U8[o+3]) / 255}
				case ClsF32:
					copy(cur[:], im.F[o:o+4])
				}
				if f.SRGB && f.Class == ClsU8 {
					// keep existing encoded channels, encode new ones
					var tmp [4]uint8
					copy(tmp[:], im.U8[o:o+4])
					im.setFloat(o, res)
					for k := 0; k < 4; k++ {
						if !cm[k] {
							im.U8[o+k] = tmp[k]
						}
					}
					continue
				}
				for k := 0; k < 4; k++ {
					if !cm[k] {
						res[k] = cur[k]
					}
				}
				im.setFloat(o, res)
			}
		}
	}
}

func loadDst(im *Image, o int) [4]float32 {
	switch im.Fmt.Class {
	case ClsU8:
		if im.Fmt.SRGB {
			return [4]float32{srgbToLinear[im.U8[o]], srgbToLinear[im.U8[o+1]], srgbToLinear[im.U8[o+2]], float32(im.U8[o+3]) / 255}
		}
		return [4]float32{float32(im.U8[o]) / 255, float32(im.U8[o+1]) / 255, float32(im.U8[o+2]) / 255, float32(im.U8[o+3]) / 255}
	case ClsF32:
		return [4]float32{im.F[o], im.F[o+1], im.F[o+2], im.F[o+3]}
	}
	return [4]float32{}
}

func (dc *drawCtx) factor(f uint32, src, dst [4]float32, a bool) [4]float32 {
	one := [4]float32{1, 1, 1, 1}
	switch f {
	case GL_ZERO:
		return [4]float32{}
	case GL_ONE:
		return one
	case GL_SRC_COLOR:
		return src
	case GL_ONE_MINUS_SRC_COLOR:
		return [4]float32{1 - src[0], 1 - src[1], 1 - src[2], 1 - src[3]}
	case GL_DST_COLOR:
		return dst
	case GL_ONE_MINUS_DST_COLOR:
		return [4]float32{1 - dst[0], 1 - dst[1], 1 - dst[2], 1 - dst[3]}
	case GL_SRC_ALPHA:
		return [4]float32{src[3], src[3], src[3], src[3]}
	case GL_ONE_MINUS_SRC_ALPHA:
		x := 1 - src[3]
		return [4]float32{x, x, x, x}
	case GL_DST_ALPHA:
		return [4]float32{dst[3], dst[3], dst[3], dst[3]}
	case GL_ONE_MINUS_DST_ALPHA:
		x := 1 - dst[3]
		return [4]float32{x, x, x, x}
	case GL_CONSTANT_COLOR:
		return dc.blendColor
	case GL_ONE_MINUS_CONSTANT_COLOR:
		b := dc.blendColor
		return [4]float32{1 - b[0], 1 - b[1], 1 - b[2], 1 - b[3]}
	case GL_CONSTANT_ALPHA:
		b := dc.blendColor[3]
		return [4]float32{b, b, b, b}
	case GL_ONE_MINUS_CONSTANT_ALPHA:
		b := 1 - dc.blendColor[3]
		return [4]float32{b, b, b, b}
	case GL_SRC_ALPHA_SATURATE:
		m := float32(math.Min(float64(src[3]), float64(1-dst[3])))
		return [4]float32{m, m, m, 1}
	}
	return one
}

func (dc *drawCtx) blendPx(src, dst [4]float32) [4]float32 {
	var res [4]float32
	fsrcRGB := dc.factor(dc.blendF[0], src, dst, false)
	fdstRGB := dc.factor(dc.blendF[1], src, dst, false)
	fsrcA := dc.factor(dc.blendF[2], src, dst, true)
	fdstA := dc.factor(dc.blendF[3], src, dst, true)
	for k := 0; k < 4; k++ {
		var fs, fd float32
		eq := dc.blendEq[0]
		if k == 3 {
			fs, fd = fsrcA[3], fdstA[3]
			eq = dc.blendEq[1]
		} else {
			fs, fd = fsrcRGB[k], fdstRGB[k]
		}
		s, d := src[k]*fs, dst[k]*fd
		switch eq {
		case GL_FUNC_ADD:
			res[k] = s + d
		case GL_FUNC_SUBTRACT:
			res[k] = s - d
		case GL_FUNC_REVERSE_SUBTRACT:
			res[k] = d - s
		case GL_MIN:
			res[k] = float32(math.Min(float64(src[k]), float64(dst[k])))
		case GL_MAX:
			res[k] = float32(math.Max(float64(src[k]), float64(dst[k])))
		}
	}
	return res
}
