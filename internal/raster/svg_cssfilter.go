package raster

import (
	"strings"

	"github.com/sweefbrowser/sweef/internal/render"
)

// CSS filter lists that contain url(#id) references to SVG <filter> elements
// (render names them "svgref:<key>"; their Args hold the element's border box
// in the layer's coordinate system). The layer is opened without CSS filters
// and the steps below run over it when it closes.

type layerStep struct {
	css []Filter // a run of plain CSS filter functions
	ref *render.SVGFilterRef
	box Rect // border box (user space of ctm)
	ctm Matrix
}

// svgRefLayer inspects the filters of a PushLayer. When none is an SVG
// reference it returns its arguments unchanged. Otherwise the CSS filters are
// split into steps, the layer bounds grow to hold every step's output, and
// the layer is opened with no filters.
func (p *Painter) svgRefLayer(fl []Filter, b IRect) (IRect, []Filter, []layerStep) {
	has := false
	for _, f := range fl {
		if render.IsSVGFilterRef(f.Name) {
			has = true
			break
		}
	}
	if !has {
		return b, fl, nil
	}
	c := p.c
	ctm := c.Transform()
	var steps []layerStep
	var cur []Filter
	grown := b
	flush := func() {
		if len(cur) > 0 {
			steps = append(steps, layerStep{css: cur})
			if pad := filterPad(cur); pad > 0 {
				grown = IRect{grown.X0 - pad, grown.Y0 - pad, grown.X1 + pad, grown.Y1 + pad}
			}
			cur = nil
		}
	}
	for _, f := range fl {
		if !render.IsSVGFilterRef(f.Name) {
			cur = append(cur, f)
			continue
		}
		flush()
		ref, ok := render.LookupSVGFilter(f.Name)
		if !ok || len(f.Args) < 4 {
			continue
		}
		st := layerStep{ref: ref, box: Rect{f.Args[0], f.Args[1], f.Args[2], f.Args[3]}, ctm: ctm}
		steps = append(steps, st)
		r := &svgR{c: c, doc: ref.Doc, active: map[*svgNode]bool{}, dpr: ctm.MaxScale()}
		r.root = baseStyle(render.SVGDraw{FontSize: 16, Family: "serif", Weight: 400})
		r.vw, r.vh = 1280, 800
		if spec := r.prepareFilterNode(ref.Node, st.box, true, &r.root, ctm); spec != nil && !spec.region.Empty() {
			grown = grown.Union(spec.region)
		}
	}
	flush()
	return grown, nil, steps
}

// runLayerSteps applies the steps to the open layer surface.
func (p *Painter) runLayerSteps(steps []layerStep) {
	c := p.c
	for _, s := range steps {
		if s.ref == nil {
			ApplyFilters(c.surf, s.css)
			continue
		}
		r := &svgR{c: c, doc: s.ref.Doc, active: map[*svgNode]bool{}, dpr: s.ctm.MaxScale()}
		r.root = baseStyle(render.SVGDraw{FontSize: 16, Family: "serif", Weight: 400})
		r.root.color = RGBA8(0, 0, 0, 255)
		r.vw, r.vh = 1280, 800
		spec := r.prepareFilterNode(s.ref.Node, s.box, true, &r.root, s.ctm)
		if spec == nil {
			continue
		}
		if spec.region.Empty() {
			c.surf.Clear()
			continue
		}
		r.runFilterAt(spec, nil, &r.root, s.ctm, s.box.X, s.box.Y)
	}
}

// isSVGRefName reports whether a filter name is an SVG reference.
func isSVGRefName(n string) bool { return strings.HasPrefix(n, "svgref:") }
