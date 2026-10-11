package style

import (
	"strings"
)

// CSS Masking: clip-path (basic shapes, path(), url(#id), geometry boxes) and
// mask / mask-* (image layers). Parsing only; geometry is resolved by layout.

// BasicShape is a clip-path <basic-shape>.
type BasicShape struct {
	Kind string // inset, circle, ellipse, polygon, path, rect, xywh
	// inset / rect / xywh: edges. inset: top right bottom left offsets;
	// rect(): top right bottom left (auto = LAuto); xywh(): x y width height.
	Edges    [4]Length
	Round    [4][2]Length // corner radii tl tr br bl (horizontal, vertical)
	HasRound bool
	// circle: R[0]; ellipse: R[0], R[1]. RKw holds closest-side / farthest-side.
	R      [2]Length
	RKw    [2]string
	Pos    [2]Length // center, resolved against the reference box (percent of its size)
	Pts    [][2]Length
	D      string // path() data
	Evenod bool
}

// ClipPathVal is a computed clip-path.
type ClipPathVal struct {
	Shape *BasicShape
	URL   string // url(...) reference (usually "#id")
	Box   string // geometry box: "" (= border-box), margin-box, border-box, padding-box, content-box, fill-box, stroke-box, view-box
}

// MaskLayer is one mask layer: a background-like layer plus the mask mode and
// compositing operator.
type MaskLayer struct {
	BgLayer
	Mode      string // match-source (default), alpha, luminance
	Composite string // add (default), subtract, intersect, exclude
}

func isGeomBox(w string) bool {
	switch w {
	case "margin-box", "border-box", "padding-box", "content-box", "fill-box", "stroke-box", "view-box":
		return true
	}
	return false
}

// clipPathValue parses a clip-path value; ok=false for invalid values.
func (s *cstate) clipPathValue(v string) (*ClipPathVal, bool) {
	v = strings.TrimSpace(v)
	if strings.ToLower(v) == "none" {
		return nil, true
	}
	cp := &ClipPathVal{}
	for _, t := range fields(v) {
		lt := strings.ToLower(t)
		switch {
		case isGeomBox(lt):
			if cp.Box != "" {
				return nil, false
			}
			cp.Box = lt
		case strings.HasPrefix(lt, "url("):
			name, args, ok := funcArgs(t)
			if !ok || name != "url" || cp.Shape != nil || cp.URL != "" {
				return nil, false
			}
			cp.URL = urlArg(args)
		default:
			if cp.Shape != nil || cp.URL != "" {
				return nil, false
			}
			sh, ok := s.basicShape(t)
			if !ok {
				return nil, false
			}
			cp.Shape = sh
		}
	}
	if cp.Shape == nil && cp.URL == "" && cp.Box == "" {
		return nil, false
	}
	return cp, true
}

func (s *cstate) basicShape(t string) (*BasicShape, bool) {
	name, args, ok := funcArgs(t)
	if !ok {
		return nil, false
	}
	name = strings.ToLower(name)
	sh := &BasicShape{Kind: name}
	switch name {
	case "inset", "rect", "xywh":
		parts := fields(args)
		var edgeToks []string
		var roundToks []string
		for i, p := range parts {
			if strings.ToLower(p) == "round" {
				roundToks = parts[i+1:]
				break
			}
			edgeToks = append(edgeToks, p)
		}
		var edges []Length
		for _, e := range edgeToks {
			if name == "rect" && strings.ToLower(e) == "auto" {
				edges = append(edges, Length{K: LAuto})
				continue
			}
			l, ok := s.length(strings.ToLower(e), false)
			if !ok {
				return nil, false
			}
			edges = append(edges, l)
		}
		switch name {
		case "xywh":
			if len(edges) != 4 {
				return nil, false
			}
			copy(sh.Edges[:], edges)
		default:
			if len(edges) < 1 || len(edges) > 4 {
				return nil, false
			}
			if name == "rect" && len(edges) != 4 {
				return nil, false
			}
			sh.Edges = expand4(edges)
		}
		if roundToks != nil {
			r, ok := s.cornerRadii(roundToks)
			if !ok {
				return nil, false
			}
			sh.Round, sh.HasRound = r, true
		} else if len(edgeToks) != len(parts) {
			return nil, false
		}
	case "circle", "ellipse":
		var toks []string
		var posToks []string
		parts := fields(args)
		for i, p := range parts {
			if strings.ToLower(p) == "at" {
				posToks = parts[i+1:]
				break
			}
			toks = append(toks, p)
		}
		want := 1
		if name == "ellipse" {
			want = 2
		}
		if len(toks) != 0 && len(toks) != want {
			return nil, false
		}
		for i, tk := range toks {
			lt := strings.ToLower(tk)
			if lt == "closest-side" || lt == "farthest-side" {
				sh.RKw[i] = lt
				continue
			}
			l, ok := s.nonNegLen(lt)
			if !ok {
				return nil, false
			}
			sh.R[i] = l
		}
		if len(toks) == 0 {
			sh.RKw[0], sh.RKw[1] = "closest-side", "closest-side"
		}
		sh.Pos = [2]Length{Pct(50), Pct(50)}
		if posToks != nil {
			x, y, ok := s.positionPair(posToks)
			if !ok {
				return nil, false
			}
			sh.Pos = [2]Length{x, y}
		}
	case "polygon":
		parts := splitTop(args, ',')
		for i, p := range parts {
			p = strings.TrimSpace(p)
			lp := strings.ToLower(p)
			if i == 0 && (lp == "nonzero" || lp == "evenodd") {
				sh.Evenod = lp == "evenodd"
				continue
			}
			f := fields(lp)
			if len(f) != 2 {
				return nil, false
			}
			x, ok1 := s.length(f[0], false)
			y, ok2 := s.length(f[1], false)
			if !ok1 || !ok2 {
				return nil, false
			}
			sh.Pts = append(sh.Pts, [2]Length{x, y})
		}
		if len(sh.Pts) == 0 {
			return nil, false
		}
	case "path":
		parts := splitTop(args, ',')
		for i, p := range parts {
			p = strings.TrimSpace(p)
			lp := strings.ToLower(p)
			if i == 0 && (lp == "nonzero" || lp == "evenodd") && len(parts) > 1 {
				sh.Evenod = lp == "evenodd"
				continue
			}
			if len(p) < 2 || (p[0] != '"' && p[0] != '\'') {
				return nil, false
			}
			sh.D = unquote(p)
		}
		if sh.D == "" {
			return nil, false
		}
	default:
		return nil, false
	}
	return sh, true
}

// cornerRadii parses "<lp>{1,4} [/ <lp>{1,4}]".
func (s *cstate) cornerRadii(f []string) ([4][2]Length, bool) {
	var hs, vs []Length
	cur := &hs
	for _, t := range f {
		if t == "/" {
			cur = &vs
			continue
		}
		if strings.Contains(t, "/") && t != "/" {
			// "10px/5px" without spaces
			a, b, _ := strings.Cut(t, "/")
			for i, x := range []string{a, b} {
				if i == 1 {
					cur = &vs
				}
				if x == "" {
					continue
				}
				l, ok := s.nonNegLen(strings.ToLower(x))
				if !ok {
					return [4][2]Length{}, false
				}
				*cur = append(*cur, l)
			}
			continue
		}
		l, ok := s.nonNegLen(strings.ToLower(t))
		if !ok {
			return [4][2]Length{}, false
		}
		*cur = append(*cur, l)
	}
	if len(hs) < 1 || len(hs) > 4 || len(vs) > 4 {
		return [4][2]Length{}, false
	}
	if len(vs) == 0 {
		vs = hs
	}
	h, v := expand4(hs), expand4(vs)
	var out [4][2]Length
	for i := 0; i < 4; i++ {
		out[i] = [2]Length{h[i], v[i]}
	}
	return out, true
}

// ---- mask --------------------------------------------------------------------------------------

func defaultMaskLayer() MaskLayer {
	bl := defaultLayer()
	bl.Origin = "border-box"
	return MaskLayer{BgLayer: bl, Mode: "match-source", Composite: "add"}
}

func maskBox(w string) (string, bool) {
	switch w {
	case "border-box", "padding-box", "content-box":
		return w, true
	case "margin-box":
		return "margin-box", true
	case "fill-box":
		return "content-box", true
	case "stroke-box", "view-box":
		return "border-box", true
	}
	return "", false
}

// maskShorthand parses mask / -webkit-mask.
func (s *cstate) maskShorthand(v string) bool {
	c := s.c
	var out []MaskLayer
	for _, ly := range splitTop(v, ',') {
		ml := defaultMaskLayer()
		var posToks, sizeToks, boxes []string
		inSize := false
		for _, t := range fields(strings.TrimSpace(ly)) {
			lt := strings.ToLower(t)
			switch {
			case t == "/":
				inSize = true
			case lt == "none":
			case strings.HasPrefix(lt, "url(") || strings.Contains(lt, "gradient(") || strings.HasPrefix(lt, "image-set(") || strings.HasPrefix(lt, "-webkit-image-set("):
				img, ok := s.imageValue(t)
				if !ok {
					return false
				}
				ml.Image = img
			case lt == "repeat-x":
				ml.RepeatX, ml.RepeatY = "repeat", "no-repeat"
			case lt == "repeat-y":
				ml.RepeatX, ml.RepeatY = "no-repeat", "repeat"
			case lt == "repeat" || lt == "no-repeat" || lt == "space" || lt == "round":
				if ml.RepeatSet {
					ml.RepeatY = lt
				} else {
					ml.RepeatX, ml.RepeatY = lt, lt
					ml.RepeatSet = true
				}
			case lt == "add" || lt == "subtract" || lt == "intersect" || lt == "exclude":
				ml.Composite = lt
			case lt == "alpha" || lt == "luminance" || lt == "match-source":
				ml.Mode = lt
			case lt == "no-clip":
				boxes = append(boxes, lt)
			case isGeomBox(lt):
				boxes = append(boxes, lt)
			default:
				if inSize {
					sizeToks = append(sizeToks, lt)
				} else {
					posToks = append(posToks, lt)
				}
			}
		}
		switch len(boxes) {
		case 1:
			if b, ok := maskBox(boxes[0]); ok {
				ml.Origin, ml.Clip = b, b
			} else if boxes[0] == "no-clip" {
				ml.Clip = "no-clip"
			}
		case 2:
			if b, ok := maskBox(boxes[0]); ok {
				ml.Origin = b
			}
			if b, ok := maskBox(boxes[1]); ok {
				ml.Clip = b
			} else if boxes[1] == "no-clip" {
				ml.Clip = "no-clip"
			}
		}
		if len(posToks) > 0 {
			x, y, ok := s.positionPair(posToks)
			if !ok {
				return false
			}
			ml.PosX, ml.PosY = x, y
		}
		if len(sizeToks) > 0 && !s.bgSize(&ml.BgLayer, sizeToks) {
			return false
		}
		out = append(out, ml)
	}
	c.Mask = out
	return true
}

// maskLonghand parses one mask-* longhand (also the -webkit- forms).
func (s *cstate) maskLonghand(name, v, l string) bool {
	c := s.c
	prefixed := strings.HasPrefix(name, "-webkit-")
	name = strings.TrimPrefix(name, "-webkit-")
	if (name == "mask-position-x" || name == "mask-position-y") && !prefixed {
		return false // Chromium only knows the -webkit- forms
	}
	switch name {
	case "mask-border", "mask-border-source", "mask-border-mode", "mask-border-slice", "mask-border-width", "mask-border-outset", "mask-border-repeat", "mask-type", "mask-box-image", "mask-box-image-source",
		"mask-box-image-slice", "mask-box-image-width", "mask-box-image-outset", "mask-box-image-repeat":
		return true
	}
	items := splitTop(v, ',')
	n := len(items)
	if n == 0 {
		return false
	}
	old := len(c.Mask)
	for len(c.Mask) < n {
		c.Mask = append(c.Mask, defaultMaskLayer())
	}
	layers := make([]MaskLayer, len(c.Mask))
	copy(layers, c.Mask)
	c.Mask = layers
	for i := range c.Mask {
		it := strings.TrimSpace(items[i%n])
		lt := strings.ToLower(it)
		ml := &c.Mask[i]
		switch name {
		case "mask-image":
			img, ok := s.imageValue(it)
			if !ok {
				return false
			}
			ml.Image = img
		case "mask-position":
			x, y, ok := s.positionPair(strings.Fields(lt))
			if !ok {
				return false
			}
			ml.PosX, ml.PosY = x, y
		case "mask-position-x":
			x, _, ok := s.positionPair(append(strings.Fields(lt), "center"))
			if len(strings.Fields(lt)) == 1 && !isPosKeyword(lt) {
				x2, ok2 := s.length(lt, false)
				x, ok = x2, ok2
			}
			if !ok {
				return false
			}
			ml.PosX = x
		case "mask-position-y":
			var y Length
			var ok bool
			if isPosKeyword(lt) {
				_, y, ok = s.positionPair([]string{"center", lt})
			} else {
				y, ok = s.length(lt, false)
			}
			if !ok {
				return false
			}
			ml.PosY = y
		case "mask-size":
			if !s.bgSize(&ml.BgLayer, strings.Fields(lt)) {
				return false
			}
		case "mask-repeat":
			f := strings.Fields(lt)
			switch {
			case len(f) == 1 && f[0] == "repeat-x":
				ml.RepeatX, ml.RepeatY = "repeat", "no-repeat"
			case len(f) == 1 && f[0] == "repeat-y":
				ml.RepeatX, ml.RepeatY = "no-repeat", "repeat"
			case len(f) == 1:
				ml.RepeatX, ml.RepeatY = f[0], f[0]
			case len(f) == 2:
				ml.RepeatX, ml.RepeatY = f[0], f[1]
			default:
				return false
			}
		case "mask-origin":
			if lt == "border" || lt == "padding" || lt == "content" {
				lt += "-box"
			}
			b, ok := maskBox(lt)
			if !ok {
				return false
			}
			ml.Origin = b
		case "mask-clip":
			if lt == "border" || lt == "padding" || lt == "content" {
				lt += "-box"
			}
			if lt == "no-clip" {
				ml.Clip = lt
			} else if b, ok := maskBox(lt); ok {
				ml.Clip = b
			} else if lt != "text" {
				return false
			}
		case "mask-mode", "mask-source-type":
			switch lt {
			case "alpha", "luminance", "match-source":
				ml.Mode = lt
			case "auto":
				ml.Mode = "match-source"
			default:
				return false
			}
		case "mask-composite":
			switch lt {
			case "add", "subtract", "intersect", "exclude":
				ml.Composite = lt
			case "source-over":
				ml.Composite = "add"
			case "source-in":
				ml.Composite = "intersect"
			case "source-out":
				ml.Composite = "subtract"
			case "xor":
				ml.Composite = "exclude"
			default:
				return false
			}
		default:
			return false
		}
	}
	if name == "mask-image" && len(c.Mask) > n {
		c.Mask = c.Mask[:n]
	}
	_ = old
	return true
}

// applyMask handles every mask property; handled=false when name is not one.
func (s *cstate) applyMask(name, v, l string) (handled, ok bool) {
	base := strings.TrimPrefix(name, "-webkit-")
	if base != "mask" && !strings.HasPrefix(base, "mask-") {
		return false, false
	}
	if l == "none" && (base == "mask" || base == "mask-image") {
		s.c.Mask = nil
		s.c.MaskImage = false
		return true, true
	}
	var ok2 bool
	if base == "mask" {
		ok2 = s.maskShorthand(v)
	} else {
		ok2 = s.maskLonghand(name, v, l)
	}
	if !ok2 {
		return true, false
	}
	has := false
	for _, ml := range s.c.Mask {
		if !ml.Image.IsNone() {
			has = true
		}
	}
	s.c.MaskImage = has
	return true, true
}
