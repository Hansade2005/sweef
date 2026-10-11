package render

import (
	"math"
	"strconv"
	"strings"
)

// Shared SVG text layout: character collection (addressable characters,
// whitespace collapsing, the x/y/dx/dy/rotate lists), the chunk / anchor /
// textLength / textPath placement. Both the geometry pass (render) and the
// painter (raster) use it, each supplying advances from its own font
// plumbing.

// SVGTextChar is one addressable character of a <text> element.
type SVGTextChar struct {
	R          rune
	Node       *SVGNode // innermost content element that holds the character
	Path       *SVGNode // enclosing <textPath>, nil when not on a path
	HasX, HasY bool
	X, Y       float64
	DX, DY     float64
	Rot        float64 // degrees
}

// SVGTextLen is a textLength adjustment over chars [From, To).
type SVGTextLen struct {
	From, To int
	Length   float64
	Glyphs   bool // lengthAdjust=spacingAndGlyphs
}

func isTextContent(tag string) bool {
	switch tag {
	case "tspan", "a", "textpath":
		return true
	}
	return false
}

// CollectSVGText flattens the text content of root (a <text> element) into
// addressable characters with the positioning attribute lists resolved.
// ln converts a length string of a node to user units (axis 'x' or 'y').
// ok is false when a <textPath> in the content has no usable target (Chromium
// renders nothing for such an element).
func CollectSVGText(root *SVGNode, ln func(n *SVGNode, s string, axis byte) float64) (chars []SVGTextChar, lens []SVGTextLen) {
	type elRange struct {
		n        *SVGNode
		from, to int
	}
	var ranges []elRange
	prevSpace := true
	var walk func(n *SVGNode, path *SVGNode, depth int)
	walk = func(n *SVGNode, path *SVGNode, depth int) {
		if depth > 40 {
			return
		}
		from := len(chars)
		preserve := strings.EqualFold(n.A["xml:space"], "preserve") || strings.EqualFold(n.A["space"], "preserve")
		for _, k := range n.Kids {
			switch {
			case k.Tag == "#text":
				for _, r := range k.Text {
					if r == '\r' {
						continue
					}
					if r == '\n' || r == '\t' {
						if preserve {
							r = ' '
						} else {
							r = ' '
						}
					}
					if r == ' ' && !preserve {
						if prevSpace {
							continue
						}
						prevSpace = true
					} else {
						prevSpace = false
					}
					chars = append(chars, SVGTextChar{R: r, Node: n, Path: path})
				}
			case k.A != nil && isTextContent(k.Tag):
				if k.A["display"] == "none" {
					continue
				}
				p := path
				if k.Tag == "textpath" {
					p = k
				}
				walk(k, p, depth+1)
			}
		}
		ranges = append(ranges, elRange{n, from, len(chars)})
	}
	walk(root, nil, 0)
	// the last collapsed space of the whole element is not rendered
	for len(chars) > 0 && chars[len(chars)-1].R == ' ' && !strings.EqualFold(root.A["xml:space"], "preserve") {
		chars = chars[:len(chars)-1]
		for i := range ranges {
			if ranges[i].to > len(chars) {
				ranges[i].to = len(chars)
			}
			if ranges[i].from > len(chars) {
				ranges[i].from = len(chars)
			}
		}
	}
	// ranges were appended post-order: apply lists pre-order (outer first) so
	// descendants override
	for i := len(ranges) - 1; i >= 0; i-- {
		rg := ranges[i]
		n := rg.n
		apply := func(key string, axis byte, set func(c *SVGTextChar, v float64)) {
			v, ok := n.A[key]
			if !ok || strings.TrimSpace(v) == "" {
				return
			}
			fs := strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r' })
			for k, f := range fs {
				if rg.from+k >= rg.to {
					break
				}
				set(&chars[rg.from+k], ln(n, f, axis))
			}
		}
		apply("x", 'x', func(c *SVGTextChar, v float64) { c.HasX, c.X = true, v })
		apply("y", 'y', func(c *SVGTextChar, v float64) { c.HasY, c.Y = true, v })
		apply("dx", 'x', func(c *SVGTextChar, v float64) { c.DX = v })
		apply("dy", 'y', func(c *SVGTextChar, v float64) { c.DY = v })
		if v, ok := n.A["rotate"]; ok {
			fs := strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' })
			var rots []float64
			for _, f := range fs {
				rots = append(rots, svgNum(strings.TrimSuffix(f, "deg"), 0))
			}
			if len(rots) > 0 {
				for k := rg.from; k < rg.to; k++ {
					j := k - rg.from
					if j >= len(rots) {
						j = len(rots) - 1
					}
					chars[k].Rot = rots[j]
				}
			}
		}
		if tl, ok := n.A["textlength"]; ok && rg.to > rg.from {
			if v := ln(n, tl, 'x'); v > 0 || strings.TrimSpace(tl) == "0" {
				lens = append(lens, SVGTextLen{From: rg.from, To: rg.to, Length: v, Glyphs: n.A["lengthadjust"] == "spacingAndGlyphs"})
			}
		}
	}
	// inner textLength adjustments first
	for i, j := 0, len(lens)-1; i < j; i, j = i+1, j-1 {
		lens[i], lens[j] = lens[j], lens[i]
	}
	sortLens(lens)
	return chars, lens
}

func sortLens(l []SVGTextLen) {
	// smaller ranges first so nested adjustments apply before enclosing ones
	for i := 1; i < len(l); i++ {
		for j := i; j > 0 && (l[j].To-l[j].From) < (l[j-1].To-l[j-1].From); j-- {
			l[j], l[j-1] = l[j-1], l[j]
		}
	}
}

// ---- text paths ---------------------------------------------------------------------

// SVGTextPath measures a path for text-on-a-path layout.
type SVGTextPath struct {
	Contours [][][2]float64
	Start    float64 // startOffset in path units
	lens     [][]float64
	total    float64
	inited   bool
}

func (p *SVGTextPath) init() {
	if p.inited {
		return
	}
	p.inited = true
	p.total = 0
	p.lens = nil
	for _, c := range p.Contours {
		cum := make([]float64, len(c))
		for i := 1; i < len(c); i++ {
			cum[i] = cum[i-1] + math.Hypot(c[i][0]-c[i-1][0], c[i][1]-c[i-1][1])
		}
		p.lens = append(p.lens, cum)
		if len(cum) > 0 {
			p.total += cum[len(cum)-1]
		}
	}
}

// Scale multiplies the path (and its start offset) by k.
func (p *SVGTextPath) Scale(k float64) {
	for _, c := range p.Contours {
		for i := range c {
			c[i][0] *= k
			c[i][1] *= k
		}
	}
	p.Start *= k
	p.inited = false
}

// Length is the total length of all contours.
func (p *SVGTextPath) Length() float64 { p.init(); return p.total }

// At returns the point and unit tangent at distance s along the path.
func (p *SVGTextPath) At(s float64) (x, y, tx, ty float64, ok bool) {
	p.init()
	if s < 0 || s > p.total || p.total <= 0 {
		return 0, 0, 1, 0, false
	}
	for ci, c := range p.Contours {
		cum := p.lens[ci]
		if len(cum) < 2 {
			continue
		}
		l := cum[len(cum)-1]
		if s > l && ci < len(p.Contours)-1 {
			s -= l
			continue
		}
		if s > l {
			s = l
		}
		// binary search for the segment
		lo, hi := 1, len(c)-1
		for lo < hi {
			mid := (lo + hi) / 2
			if cum[mid] >= s {
				hi = mid
			} else {
				lo = mid + 1
			}
		}
		for lo < len(c)-1 && cum[lo]-cum[lo-1] == 0 {
			lo++
		}
		a, b := c[lo-1], c[lo]
		sl := cum[lo] - cum[lo-1]
		if sl == 0 {
			return a[0], a[1], 1, 0, true
		}
		t := (s - cum[lo-1]) / sl
		return a[0] + (b[0]-a[0])*t, a[1] + (b[1]-a[1])*t, (b[0] - a[0]) / sl, (b[1] - a[1]) / sl, true
	}
	return 0, 0, 1, 0, false
}

// ---- layout -------------------------------------------------------------------------

// SVGTextIn describes one character for LayoutSVGText.
type SVGTextIn struct {
	SVGTextChar
	Adv    float64 // advance including letter and word spacing
	YOff   float64 // baseline offset (dominant/alignment/baseline-shift), positive = down
	Anchor int     // 0 start, 1 middle, 2 end (of the character's own style)
	Space  bool    // the character is whitespace (still advances)
	PathM  *SVGTextPath
}

// SVGGlyphPos is the placement of one character: the glyph origin on its
// baseline, its rotation (radians) and horizontal scale.
type SVGGlyphPos struct {
	X, Y   float64
	Angle  float64
	ScaleX float64
	Adv    float64
	Hidden bool
}

// LayoutSVGText places characters. lens are the textLength adjustments (inner
// first).
func LayoutSVGText(in []SVGTextIn, lens []SVGTextLen) []SVGGlyphPos {
	n := len(in)
	out := make([]SVGGlyphPos, n)
	adv := make([]float64, n)
	scale := make([]float64, n)
	for i := range in {
		adv[i] = in[i].Adv
		scale[i] = 1
	}
	for _, l := range lens {
		if l.To > n {
			l.To = n
		}
		cnt := l.To - l.From
		if cnt <= 0 {
			continue
		}
		var sum float64
		for i := l.From; i < l.To; i++ {
			sum += adv[i]
		}
		if l.Glyphs {
			if sum > 0 {
				f := l.Length / sum
				for i := l.From; i < l.To; i++ {
					adv[i] *= f
					scale[i] *= f
				}
			}
		} else if cnt > 1 {
			d := (l.Length - sum) / float64(cnt-1)
			for i := l.From; i < l.To-1; i++ {
				adv[i] += d
			}
		}
	}
	penX, penY := 0.0, 0.0
	type chunk struct {
		from, to   int
		x0, x1     float64
		anchor     int
		onPath     bool
		pathShift  float64
		pathLength float64
	}
	var chunks []chunk
	cur := -1
	var curPath *SVGNode
	pathS := 0.0
	startChunk := func(i int, onPath bool) {
		chunks = append(chunks, chunk{from: i, to: i, anchor: in[i].Anchor, onPath: onPath})
		cur = len(chunks) - 1
	}
	for i := range in {
		c := &in[i]
		if c.Path != curPath {
			curPath = c.Path
			cur = -1
			if curPath != nil && c.PathM != nil {
				pathS = c.PathM.Start
				penY = 0
			}
		}
		onPath := c.Path != nil && c.PathM != nil
		if onPath {
			if cur < 0 {
				startChunk(i, true)
			}
			pathS += c.DX
			penY += c.DY
			out[i] = SVGGlyphPos{X: pathS, Y: penY + c.YOff, ScaleX: scale[i], Adv: adv[i]}
			ck := &chunks[cur]
			if i == ck.from {
				ck.x0 = pathS
			}
			pathS += adv[i]
			ck.x1 = pathS
			ck.to = i + 1
			continue
		}
		if cur < 0 || c.HasX || c.HasY {
			if c.HasX {
				penX = c.X
			}
			if c.HasY {
				penY = c.Y
			}
			startChunk(i, false)
		}
		penX += c.DX
		penY += c.DY
		ck := &chunks[cur]
		if i == ck.from {
			ck.x0 = penX
		}
		out[i] = SVGGlyphPos{X: penX, Y: penY + c.YOff, ScaleX: scale[i], Adv: adv[i], Angle: c.Rot * math.Pi / 180}
		penX += adv[i]
		ck.x1 = penX
		ck.to = i + 1
	}
	for _, ck := range chunks {
		w := ck.x1 - ck.x0
		shift := 0.0
		switch ck.anchor {
		case 1:
			shift = -w / 2
		case 2:
			shift = -w
		}
		if !ck.onPath {
			for i := ck.from; i < ck.to; i++ {
				out[i].X += shift
			}
			continue
		}
		pm := in[ck.from].PathM
		for i := ck.from; i < ck.to; i++ {
			s := out[i].X + shift
			off := out[i].Y
			mid := s + out[i].Adv/2
			px, py, tx, ty, ok := pm.At(mid)
			if !ok {
				out[i].Hidden = true
				continue
			}
			ang := math.Atan2(ty, tx)
			// glyph origin: back along the tangent by half the advance, offset along the normal
			ox := px - tx*out[i].Adv/2 - ty*(-off)
			oy := py - ty*out[i].Adv/2 + tx*(-off)
			_ = ox
			_ = oy
			out[i].X = px - tx*out[i].Adv/2 + (-ty)*off
			out[i].Y = py - ty*out[i].Adv/2 + tx*off
			out[i].Angle = ang + in[i].Rot*math.Pi/180
		}
	}
	return out
}

// SVGParsePct resolves a startOffset-style number or percentage of total.
func SVGParsePct(s string, total float64) float64 {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "%") {
		v, err := strconv.ParseFloat(strings.TrimSuffix(s, "%"), 64)
		if err == nil {
			return v / 100 * total
		}
		return 0
	}
	return svgLen(s, total, 0)
}
