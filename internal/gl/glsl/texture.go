package glsl

type texSpec struct {
	proj, offset bool
	mode         SampleMode
	ext          string // extension required in ES 1.00
	es1          bool   // legacy name
	cube         bool   // legacy cube name
	fetch, size  bool
}

var texFns = map[string]texSpec{
	"texture":               {},
	"textureProj":           {proj: true},
	"textureLod":            {mode: SampleLod},
	"textureOffset":         {offset: true},
	"textureProjOffset":     {proj: true, offset: true},
	"textureLodOffset":      {mode: SampleLod, offset: true},
	"textureProjLod":        {proj: true, mode: SampleLod},
	"textureProjLodOffset":  {proj: true, mode: SampleLod, offset: true},
	"textureGrad":           {mode: SampleGrad},
	"textureGradOffset":     {mode: SampleGrad, offset: true},
	"textureProjGrad":       {proj: true, mode: SampleGrad},
	"textureProjGradOffset": {proj: true, mode: SampleGrad, offset: true},
	"texelFetch":            {fetch: true},
	"texelFetchOffset":      {fetch: true, offset: true},
	"textureSize":           {size: true},
	"texture2D":             {es1: true},
	"texture2DProj":         {es1: true, proj: true},
	"texture2DLod":          {es1: true, mode: SampleLod, ext: "vs"},
	"texture2DProjLod":      {es1: true, proj: true, mode: SampleLod, ext: "vs"},
	"textureCube":           {es1: true, cube: true},
	"textureCubeLod":        {es1: true, cube: true, mode: SampleLod, ext: "vs"},
	"texture2DLodEXT":       {es1: true, mode: SampleLod, ext: "GL_EXT_shader_texture_lod"},
	"texture2DProjLodEXT":   {es1: true, proj: true, mode: SampleLod, ext: "GL_EXT_shader_texture_lod"},
	"textureCubeLodEXT":     {es1: true, cube: true, mode: SampleLod, ext: "GL_EXT_shader_texture_lod"},
	"texture2DGradEXT":      {es1: true, mode: SampleGrad, ext: "GL_EXT_shader_texture_lod"},
	"texture2DProjGradEXT":  {es1: true, proj: true, mode: SampleGrad, ext: "GL_EXT_shader_texture_lod"},
	"textureCubeGradEXT":    {es1: true, cube: true, mode: SampleGrad, ext: "GL_EXT_shader_texture_lod"},
}

func (c *compiler) textureFn(e *Expr, args []*V, h int) *V {
	sp, ok := texFns[e.S]
	if !ok {
		return nil
	}
	name := e.S
	if sp.es1 && c.version == 300 && sp.ext == "" && name != "texture" {
		c.fail(e.Line, name, "no matching overloaded function found")
	}
	if !sp.es1 && c.version == 100 {
		c.noMatch(e)
	}
	if sp.ext == "vs" && c.stage != Vertex {
		c.fail(e.Line, name, "no matching overloaded function found")
	}
	if sp.ext != "" && sp.ext != "vs" && !c.extEnabled(sp.ext) {
		c.fail(e.Line, name, "extension %s is not enabled", sp.ext)
	}
	if len(args) < 2 && !(sp.size && len(args) == 2) || len(args) == 0 {
		c.noMatch(e)
	}
	sv := args[0]
	if sv.T.K != KSampler || sv.T.Arr > 0 {
		c.noMatch(e)
	}
	sk := sv.T.SK
	dim := sk.Dim()
	shadow := sk.IsShadow()
	if sp.es1 {
		if sp.cube != (dim == 6) || (!sp.cube && dim != 2) {
			c.noMatch(e)
		}
	}
	so := sv.Off
	resK := sk.ResultKind()
	ops := cat(args...)
	if sp.size {
		if len(args) != 2 || args[1].T != tInt {
			c.noMatch(e)
		}
		nc := 2
		if dim == 3 || dim == 22 {
			nc = 3
		}
		rt := vecT(KInt, nc)
		d := c.tmp(nc)
		lo := args[1].Off
		ops = append(ops, func(f *Frame) {
			u := int(f.R[so])
			if u >= len(f.Tex) || f.Tex[u] == nil {
				for i := 0; i < nc; i++ {
					f.R[d+i] = 0
				}
				return
			}
			sz := f.Tex[u].Size(int(int32(f.R[lo])))
			for i := 0; i < nc; i++ {
				f.R[d+i] = uint32(int32(sz[i]))
			}
		})
		return &V{T: rt, Off: d, Ops: ops}
	}
	// coordinate type
	var ncoord int
	switch dim {
	case 2:
		ncoord = 2
	case 3, 6:
		ncoord = 3
	case 22:
		ncoord = 3
	}
	if sp.fetch {
		if len(args) < 3 {
			c.noMatch(e)
		}
		nc := ncoord
		if dim == 6 {
			c.noMatch(e)
		}
		if args[1].T != vecT(KInt, nc) || args[2].T != tInt {
			c.noMatch(e)
		}
		co, lo := args[1].Off, args[2].Off
		var offO int
		if sp.offset {
			if len(args) != 4 {
				c.noMatch(e)
			}
			offO = args[3].Off
		}
		hasOff := sp.offset
		d := c.tmp(4)
		ops = append(ops, func(f *Frame) {
			u := int(f.R[so])
			var res [4]uint32
			if u < len(f.Tex) && f.Tex[u] != nil {
				x, y, z := int(int32(f.R[co])), int(int32(f.R[co+1])), 0
				if nc == 3 {
					z = int(int32(f.R[co+2]))
				}
				if hasOff {
					x += int(int32(f.R[offO]))
					y += int(int32(f.R[offO+1]))
					if nc == 3 {
						z += int(int32(f.R[offO+2]))
					}
				}
				res = f.Tex[u].Texel(x, y, z, int(int32(f.R[lo])))
			} else {
				res[3] = 1
				if resK == KFloat {
					res[3] = bf(1)
				}
			}
			copy(f.R[d:d+4], res[:])
		})
		return &V{T: Type{K: resK, N: 4, M: 1}, Off: d, Ops: ops}
	}
	coordSize := ncoord
	if shadow {
		if dim == 6 || dim == 22 {
			coordSize = 4
		} else {
			coordSize = 3
		}
	}
	if sp.proj {
		if dim == 2 && !shadow {
			// vec3 or vec4
		} else if dim != 2 && dim != 3 {
			c.noMatch(e)
		}
	}
	ct := args[1].T
	projSize := coordSize
	if sp.proj {
		projSize = coordSize + 1
		if dim == 2 && !shadow && ct == vecT(KFloat, 4) {
			projSize = 4
		}
	}
	if ct != vecT(KFloat, projSize) {
		c.noMatch(e)
	}
	idx := 2
	var lodO, bias, gxO, gyO, offO int = -1, -1, -1, -1, -1
	switch sp.mode {
	case SampleLod:
		if idx >= len(args) || args[idx].T != tFloat {
			c.noMatch(e)
		}
		lodO = args[idx].Off
		idx++
	case SampleGrad:
		if idx+1 >= len(args) {
			c.noMatch(e)
		}
		gt := vecT(KFloat, ncoord)
		if dim == 6 {
			gt = vecT(KFloat, 3)
		}
		if args[idx].T != gt || args[idx+1].T != gt {
			c.noMatch(e)
		}
		gxO, gyO = args[idx].Off, args[idx+1].Off
		idx += 2
	}
	if sp.offset {
		if idx >= len(args) {
			c.noMatch(e)
		}
		ot := vecT(KInt, 2)
		if dim == 3 {
			ot = vecT(KInt, 3)
		}
		if dim == 6 || args[idx].T != ot {
			c.noMatch(e)
		}
		offO = args[idx].Off
		idx++
	}
	if idx < len(args) {
		if idx == len(args)-1 && sp.mode == SampleImplicit && args[idx].T == tFloat && c.stage == Fragment {
			bias = args[idx].Off
			idx++
		}
		if idx != len(args) {
			c.noMatch(e)
		}
	}
	if sp.mode == SampleImplicit && c.stage == Fragment {
		c.sh.UsesImplicitLod = true
	}
	c.sites++
	site := c.sites
	mode := sp.mode
	proj := sp.proj
	pcs := projSize
	co := args[1].Off
	nd := ncoord
	isShadow := shadow
	isRaw := resK != KFloat
	var rt Type
	var nres int
	if shadow {
		rt, nres = tFloat, 1
	} else {
		rt, nres = Type{K: resK, N: 4, M: 1}, 4
	}
	d := c.tmp(4)
	ops = append(ops, func(f *Frame) {
		u := int(f.R[so])
		if u >= len(f.Tex) || f.Tex[u] == nil {
			for i := 0; i < nres; i++ {
				f.R[d+i] = 0
			}
			if nres == 4 {
				if isRaw {
					f.R[d+3] = 1
				} else {
					f.R[d+3] = bf(1)
				}
			}
			return
		}
		var rq SampleReq
		var cs [4]float32
		for i := 0; i < pcs; i++ {
			cs[i] = fl(f.R[co+i])
		}
		if proj {
			q := cs[pcs-1]
			if isShadow {
				// shadow proj: ref component is the one before q
			}
			for i := 0; i < pcs-1; i++ {
				cs[i] /= q
			}
			if isShadow && pcs == coordSizeOf(dim, true)+1 {
				// leave
			}
		}
		rq.Coord = cs
		if isShadow {
			if dim == 6 || dim == 22 {
				rq.Ref = cs[3]
			} else {
				rq.Ref = cs[2]
				if dim == 2 {
					rq.Coord[2] = 0
				}
			}
			if dim == 22 {
				rq.Coord[2] = cs[2]
			}
		}
		rq.Mode = mode
		switch mode {
		case SampleLod:
			rq.Lod = fl(f.R[lodO])
		case SampleGrad:
			for i := 0; i < nd; i++ {
				rq.DX[i] = fl(f.R[gxO+i])
				rq.DY[i] = fl(f.R[gyO+i])
			}
		default:
			if f.Quad != nil {
				in := f.scratch[:nd]
				for i := 0; i < nd; i++ {
					in[i] = rq.Coord[i]
				}
				dx, dy := f.Quad(site, in)
				for i := 0; i < nd; i++ {
					rq.DX[i], rq.DY[i] = dx[i], dy[i]
				}
			}
			if bias >= 0 {
				rq.Bias = fl(f.R[bias])
			}
		}
		if offO >= 0 {
			rq.Offset[0], rq.Offset[1] = int32(f.R[offO]), int32(f.R[offO+1])
			if dim == 3 {
				rq.Offset[2] = int32(f.R[offO+2])
			}
		}
		s := f.Tex[u]
		if isRaw {
			res := s.SampleRaw(&rq)
			copy(f.R[d:d+4], res[:])
			return
		}
		res := s.Sample(&rq)
		for i := 0; i < nres; i++ {
			f.R[d+i] = bf(res[i])
		}
	})
	return &V{T: rt, Off: d, Ops: ops}
}

func coordSizeOf(dim int, shadow bool) int {
	if shadow {
		if dim == 6 || dim == 22 {
			return 4
		}
		return 3
	}
	if dim == 2 {
		return 2
	}
	return 3
}
