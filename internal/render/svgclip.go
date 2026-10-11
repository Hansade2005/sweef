package render

import (
	"sync"

	"golang.org/x/net/html"

	"github.com/sweefbrowser/sweef/internal/style"
)

// SVGClipShape is a parsed CSS clip-path basic shape on an SVG element
// (circle(), inset(), polygon(), path(), ... plus the reference box).
type SVGClipShape struct {
	Shape *style.BasicShape
	Box   string // "" (= border-box, which is the stroke box for SVG), fill-box, stroke-box, view-box, ...
}

var clipShapeCache struct {
	sync.Mutex
	eng *style.Engine
	m   map[string]*SVGClipShape
}

// ParseSVGClip parses the value of a clip-path property holding a basic shape
// (not url(#id)) with the style engine's parser, the same one that serves
// clip-path on HTML elements.
func ParseSVGClip(v string) (*SVGClipShape, bool) {
	clipShapeCache.Lock()
	defer clipShapeCache.Unlock()
	if cs, ok := clipShapeCache.m[v]; ok {
		return cs, cs != nil
	}
	if clipShapeCache.eng == nil {
		clipShapeCache.eng = style.NewEngineQ(&style.Env{Width: 1280, Height: 800, DPR: 1, ColorScheme: "light"}, nil, false)
		clipShapeCache.m = map[string]*SVGClipShape{}
	}
	n := &html.Node{Type: html.ElementNode, Data: "div", Attr: []html.Attribute{{Key: "style", Val: "clip-path:" + v}}}
	st := clipShapeCache.eng.Compute(n, clipShapeCache.eng.InitialStyle(), "")
	var cs *SVGClipShape
	if st != nil && st.ClipPathV != nil && st.ClipPathV.Shape != nil {
		cs = &SVGClipShape{Shape: st.ClipPathV.Shape, Box: st.ClipPathV.Box}
	}
	if len(clipShapeCache.m) > 2048 {
		clipShapeCache.m = map[string]*SVGClipShape{}
	}
	clipShapeCache.m[v] = cs
	return cs, cs != nil
}

// Resolve resolves the shape against reference box R.
func (c *SVGClipShape) Resolve(R Rect) ClipSub { return shapeSub(c.Shape, R) }
