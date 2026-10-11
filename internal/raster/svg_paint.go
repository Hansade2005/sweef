package raster

import (
	"math"
	"strings"
)

// shaderFor resolves a paint (color or paint server) to a shader.
func (r *svgR) shaderFor(pt sPaint, opacity float64, n *svgNode, bb Rect, hasBB bool, st *sStyle, m Matrix) Shader {
	switch pt.kind {
	case pColor:
		c := pt.col
		c.A *= opacity
		return Solid(c)
	case pURL:
		node := r.doc.ByID[pt.ref]
		if node == nil {
			if pt.fb != nil {
				return r.shaderFor(*pt.fb, opacity, n, bb, hasBB, st, m)
			}
			return nil
		}
		switch node.Tag {
		case "lineargradient", "radialgradient":
			sh, fallbackSolid, ok := r.gradientShader(node, opacity, bb, hasBB, st, m)
			if !ok {
				return nil
			}
			if sh == nil {
				c := fallbackSolid
				c.A *= opacity
				return Solid(c)
			}
			return sh
		case "pattern":
			return r.patternShader(node, opacity, bb, hasBB, st, m)
		}
		if pt.fb != nil {
			return r.shaderFor(*pt.fb, opacity, n, bb, hasBB, st, m)
		}
	}
	return nil
}

// chainAttr looks an attribute up through the href chain of a paint server.
func (r *svgR) chainAttr(n *svgNode, key string) (string, bool) {
	for i := 0; i < 16 && n != nil; i++ {
		if v, ok := n.A[key]; ok {
			return v, true
		}
		id := strings.TrimPrefix(strings.TrimSpace(n.A["href"]), "#")
		n = r.doc.ByID[id]
	}
	return "", false
}

func (r *svgR) gradientStops(n *svgNode, st *sStyle) []GradStop {
	for i := 0; i < 16 && n != nil; i++ {
		var stops []GradStop
		for _, k := range n.Kids {
			if k.Tag != "stop" {
				continue
			}
			off := 0.0
			if v := strings.TrimSpace(k.A["offset"]); v != "" {
				if strings.HasSuffix(v, "%") {
					f, _ := svgNumber(v[:len(v)-1])
					off = f / 100
				} else {
					off, _ = svgNumber(v)
				}
			}
			off = clamp01(off)
			if len(stops) > 0 && off < stops[len(stops)-1].Pos {
				off = stops[len(stops)-1].Pos
			}
			col := RGBA8(0, 0, 0, 255)
			cur := st.color
			if v, ok := k.A["color"]; ok {
				if c, ok := ParseColor(v, st.color); ok {
					cur = c
				}
			}
			if v, ok := k.A["stop-color"]; ok {
				if c, ok := ParseColor(v, cur); ok {
					col = c
				}
			}
			if v, ok := k.A["stop-opacity"]; ok {
				col.A *= parseOpacity(v, 1)
			}
			stops = append(stops, GradStop{Pos: off, Color: col})
		}
		if len(stops) > 0 {
			return stops
		}
		id := strings.TrimPrefix(strings.TrimSpace(n.A["href"]), "#")
		n = r.doc.ByID[id]
	}
	return nil
}

func (r *svgR) gradientShader(n *svgNode, opacity float64, bb Rect, hasBB bool, st *sStyle, m Matrix) (Shader, Color, bool) {
	stops := r.gradientStops(n, st)
	if len(stops) == 0 {
		return nil, Color{}, false
	}
	if len(stops) == 1 {
		return nil, stops[0].Color, true
	}
	units, _ := r.chainAttr(n, "gradientunits")
	obb := units != "userSpaceOnUse"
	if obb && (!hasBB || bb.W <= 0 || bb.H <= 0) {
		return nil, Color{}, false
	}
	// find the node that defines the geometry (follow href for kind attrs)
	get := func(k string) (string, bool) { return r.chainAttr(n, k) }
	coord := func(k string, dflt float64, axis byte) float64 {
		v, ok := get(k)
		if !ok {
			return dflt
		}
		if obb {
			v = strings.TrimSpace(v)
			if strings.HasSuffix(v, "%") {
				f, _ := svgNumber(v[:len(v)-1])
				return f / 100
			}
			f, ok := svgNumber(v)
			if !ok {
				return dflt
			}
			return f
		}
		return r.length(v, r.refFor(axis), st.fontSize, dflt)
	}
	pct := func(f float64, axis byte) float64 {
		if obb {
			return f
		}
		return f * r.refFor(axis)
	}
	var def GradientDef
	if n.Tag == "lineargradient" {
		def.Kind = LinearGrad
		def.X0, def.Y0 = coord("x1", pct(0, 'x'), 'x'), coord("y1", pct(0, 'y'), 'y')
		def.X1, def.Y1 = coord("x2", pct(1, 'x'), 'x'), coord("y2", pct(0, 'y'), 'y')
	} else {
		def.Kind = RadialGrad
		cx, cy := coord("cx", pct(0.5, 'x'), 'x'), coord("cy", pct(0.5, 'y'), 'y')
		rr := coord("r", pct(0.5, 'd'), 'd')
		fx, fy := coord("fx", cx, 'x'), coord("fy", cy, 'y')
		fr := coord("fr", 0, 'd')
		if rr <= 0 {
			return nil, stops[len(stops)-1].Color, true
		}
		def.X0, def.Y0, def.R0 = fx, fy, fr
		def.X1, def.Y1, def.R1 = cx, cy, rr
	}
	spread, _ := get("spreadmethod")
	switch spread {
	case "repeat":
		def.Repeat = true
	case "reflect":
		// unfold one reflected period into a 2x repeating gradient
		var ns []GradStop
		for _, s := range stops {
			ns = append(ns, GradStop{Pos: s.Pos / 2, Color: s.Color})
		}
		for i := len(stops) - 1; i >= 0; i-- {
			ns = append(ns, GradStop{Pos: 1 - stops[i].Pos/2, Color: stops[i].Color})
		}
		stops = ns
		def.Repeat = true
		def.X1, def.Y1 = def.X0+2*(def.X1-def.X0), def.Y0+2*(def.Y1-def.Y0)
		def.R1 = def.R0 + 2*(def.R1-def.R0)
	}
	def.Stops = stops
	gm := m
	if obb {
		gm = gm.Mul(Matrix{bb.W, 0, 0, bb.H, bb.X, bb.Y})
	}
	if t, ok := get("gradienttransform"); ok {
		gm = gm.Mul(svgTransform(t))
	}
	if gm.Det() == 0 {
		return nil, Color{}, false
	}
	return NewGradientShader(def, gm, opacity), Color{}, true
}

func (r *svgR) patternShader(n *svgNode, opacity float64, bb Rect, hasBB bool, st *sStyle, m Matrix) Shader {
	if r.active[n] || r.depth > 20 {
		return nil
	}
	get := func(k string) (string, bool) { return r.chainAttr(n, k) }
	units, _ := get("patternunits")
	obb := units != "userSpaceOnUse"
	cunits, _ := get("patterncontentunits")
	cobb := cunits == "objectBoundingBox"
	if (obb || cobb) && (!hasBB || bb.W <= 0 || bb.H <= 0) {
		return nil
	}
	dim := func(k string, axis byte) float64 {
		v, ok := get(k)
		if !ok {
			return 0
		}
		if obb {
			v = strings.TrimSpace(v)
			if strings.HasSuffix(v, "%") {
				f, _ := svgNumber(v[:len(v)-1])
				return f / 100
			}
			f, _ := svgNumber(v)
			return f
		}
		return r.length(v, r.refFor(axis), st.fontSize, 0)
	}
	x, y, w, h := dim("x", 'x'), dim("y", 'y'), dim("width", 'x'), dim("height", 'y')
	if obb {
		x, y, w, h = bb.X+x*bb.W, bb.Y+y*bb.H, w*bb.W, h*bb.H
	}
	if w <= 0 || h <= 0 {
		return nil
	}
	pm := m
	if t, ok := get("patterntransform"); ok {
		pm = pm.Mul(svgTransform(t))
	}
	// content node (first in chain with children)
	cn := n
	for i := 0; i < 16 && cn != nil; i++ {
		has := false
		for _, k := range cn.Kids {
			if k.Tag != "#text" {
				has = true
			}
		}
		if has {
			break
		}
		cn = r.doc.ByID[strings.TrimPrefix(strings.TrimSpace(cn.A["href"]), "#")]
	}
	if cn == nil {
		return nil
	}
	sc := pm.MaxScale()
	if sc <= 0 || math.IsNaN(sc) {
		return nil
	}
	pw, ph := int(math.Ceil(w*sc-1e-6)), int(math.Ceil(h*sc-1e-6))
	if pw < 1 {
		pw = 1
	}
	if ph < 1 {
		ph = 1
	}
	for pw*ph > 16<<20 {
		pw, ph = pw/2+1, ph/2+1
	}
	tile := NewSurface(pw, ph)
	tc := NewContextSurface(tile)
	tc.SetSmoothing(true, QualityLow)
	sx, sy := float64(pw)/w, float64(ph)/h
	cm := Scale(sx, sy)
	vbs, _ := get("viewbox")
	par, _ := get("preserveaspectratio")
	vb := splitNums(strings.ReplaceAll(vbs, ",", " "))
	sr := r.sub(tc)
	ovw, ovh := sr.vw, sr.vh
	if vm, ok := viewBoxMatrix(vb, par, w, h); ok {
		cm = cm.Mul(vm)
		sr.vw, sr.vh = vb[2], vb[3]
	} else if cobb {
		cm = cm.Mul(Matrix{bb.W, 0, 0, bb.H, 0, 0})
	}
	_ = ovw
	_ = ovh
	r.active[n] = true
	pst := r.inherit(r.root, n.A)
	if cn != n {
		pst = r.inherit(pst, cn.A)
	}
	sr.drawChildren(cn, pst, cm)
	delete(r.active, n)
	im := pm.Mul(Translate(x, y)).Mul(Scale(1/sx, 1/sy))
	is := NewImageShader(tile, im, true, true, FilterBilinear, opacity)
	return is
}
