package raster

import (
	"github.com/sweefbrowser/sweef/internal/render"
)

// CSS clip-path: PushClipPath / PopClipPath ops.

// clipSubPath builds the path of one clip shape in user space.
func clipSubPath(s render.ClipSub) (*Path, FillRule) {
	pa := NewPath()
	rule := NonZero
	if s.EvenOdd {
		rule = EvenOdd
	}
	switch s.Kind {
	case "rrect":
		if s.Rect.W <= 0 || s.Rect.H <= 0 {
			return pa, rule
		}
		return rrPath(s.Rect, s.Radii), rule
	case "ellipse":
		if s.RX > 0 && s.RY > 0 {
			pa.Ellipse(s.CX, s.CY, s.RX, s.RY)
		}
	case "polygon":
		for i, q := range s.Pts {
			if i == 0 {
				pa.MoveTo(q[0], q[1])
			} else {
				pa.LineTo(q[0], q[1])
			}
		}
		if len(s.Pts) > 0 {
			pa.Close()
		}
	case "path":
		if d, ok := ParseSVGPath(s.D); ok {
			if s.HasM {
				d = d.Transform(Matrix(s.M))
			}
			return d, rule
		}
	}
	return pa, rule
}

func (p *Painter) pushClipPath(o render.PushClipPath) {
	c := p.c
	c.Save()
	switch len(o.Subs) {
	case 0:
		return
	case 1:
		pa, rule := clipSubPath(o.Subs[0])
		if pa.Empty() {
			c.clipWithMask(nil)
			return
		}
		c.ClipPath(pa, rule)
	default:
		var acc *Mask
		for _, s := range o.Subs {
			pa, rule := clipSubPath(s)
			if pa.Empty() {
				continue
			}
			m := FillMask(pa, c.st.ctm, rule, c.ClipBounds())
			acc = maxMasks(acc, m)
		}
		c.clipWithMask(acc)
	}
}

// maxMasks combines two coverage masks with max(); either may be nil.
func maxMasks(a, b *Mask) *Mask {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	r := a.B.Union(b.B)
	out := newMask(r)
	for _, m := range []*Mask{a, b} {
		for y := m.B.Y0; y < m.B.Y1; y++ {
			src := m.Row(y)
			dst := out.Row(y)[m.B.X0-r.X0:]
			for i, v := range src {
				if v > dst[i] {
					dst[i] = v
				}
			}
		}
	}
	return out
}

// clipOp handles the clip-path / mask display-list ops; false when op is not one.
func (p *Painter) clipOp(op render.Op) bool {
	switch o := op.(type) {
	case render.PushClipPath:
		p.pushClipPath(o)
	case render.PopClipPath:
		p.c.Restore()
	case render.PushMask:
		p.pushMask(o)
	case render.PopMask:
		p.popMask()
	default:
		return false
	}
	return true
}
