package render

import (
	"encoding/binary"
	"image"
	"math"
	"reflect"
	"sync/atomic"
	"unsafe"

	"github.com/sweefbrowser/sweef/internal/font"
)

// opsig.go: content signatures of display-list ops. The incremental painter
// (internal/raster, internal/engine) compares the signature of every op of
// the new list with the previous one to find what changed, so a signature
// must cover everything that influences the pixels an op paints. The
// completeness of each signature is pinned by TestOpSigCoversEveryField, which
// mutates every field by reflection.
//
// Signatures are 64-bit hashes; a collision would leave stale pixels, with a
// probability around 2^-64 per compared op.

type sigHasher uint64

func (h *sigHasher) u(x uint64) {
	v := (uint64(*h) ^ x) * 0x9E3779B97F4A7C15
	v ^= v >> 29
	*h = sigHasher(v * 0xBF58476D1CE4E5B9)
}
func (h *sigHasher) f(x float64) { h.u(math.Float64bits(x + 0)) }
func (h *sigHasher) i(x int)     { h.u(uint64(int64(x))) }
func (h *sigHasher) b(x bool) {
	if x {
		h.u(1)
	} else {
		h.u(0)
	}
}
func (h *sigHasher) s(x string) {
	h.i(len(x))
	var w uint64
	n := 0
	for i := 0; i < len(x); i++ {
		w = w<<8 | uint64(x[i])
		if n++; n == 8 {
			h.u(w)
			w, n = 0, 0
		}
	}
	h.u(w)
}
func (h *sigHasher) col(c Color) {
	h.u(uint64(c.R)<<24 | uint64(c.G)<<16 | uint64(c.B)<<8 | uint64(c.A))
}
func (h *sigHasher) rect(r Rect) { h.f(r.X); h.f(r.Y); h.f(r.W); h.f(r.H) }
func (h *sigHasher) radii(r Radii) {
	for _, c := range r {
		h.f(c[0])
		h.f(c[1])
	}
}
func (h *sigHasher) fl(fs []Filter) {
	h.i(len(fs))
	for _, f := range fs {
		h.s(f.Name)
		h.i(len(f.Args))
		for _, a := range f.Args {
			h.f(a)
		}
		h.col(f.Color)
	}
}

func mix(a, b uint64) uint64 {
	h := sigHasher(a)
	h.u(b)
	return uint64(h)
}

// OpSig returns the content signature of op. ok is false for ops whose pixels
// depend on state outside the op (live bitmaps, backdrop filters, 3D layers,
// clip paths): the caller must then repaint everything.
func OpSig(op Op) (sig uint64, ok bool) {
	h := sigHasher(0x243F6A8885A308D3)
	switch o := op.(type) {
	case FillRect:
		h.u(1)
		h.rect(o.Rect)
		h.col(o.Color)
		h.radii(o.Radii)
	case Border:
		h.u(2)
		h.rect(o.Rect)
		for i := 0; i < 4; i++ {
			h.f(o.Widths[i])
			h.col(o.Colors[i])
			h.s(o.Styles[i])
		}
		h.radii(o.Radii)
	case TextRun:
		h.u(3)
		h.f(o.X)
		h.f(o.Y)
		h.f(o.Size)
		h.col(o.Color)
		h.i(len(o.Glyphs))
		for _, g := range o.Glyphs {
			h.u(uint64(uint32(g.R)))
			h.f(g.X)
			h.u(uint64(uintptr(unsafe.Pointer((*font.Face)(g.Face)))))
			h.i(g.GID)
		}
		h.f(o.Width)
		h.b(o.Bold)
		h.b(o.Italic)
		h.b(o.Underline)
		h.b(o.Overline)
		h.b(o.LineThrough)
		h.s(o.DecoStyle)
		h.col(o.DecoColor)
		h.f(o.DecoThick)
		h.f(o.UnderOffset)
		h.i(len(o.Shadows))
		for _, s := range o.Shadows {
			h.f(s.DX)
			h.f(s.DY)
			h.f(s.Blur)
			h.col(s.Color)
		}
		h.f(o.Stroke)
		h.col(o.StrokeColor)
	case Image:
		h.u(4)
		if o.Img == nil {
			h.u(0)
		} else {
			v := reflect.ValueOf(o.Img)
			if v.Kind() != reflect.Ptr {
				return 0, false
			}
			h.u(uint64(v.Pointer()))
			b := o.Img.Bounds()
			h.i(b.Min.X)
			h.i(b.Min.Y)
			h.i(b.Max.X)
			h.i(b.Max.Y)
			if b.Dx()*b.Dy() <= 64*64 {
				// small images (favicons, icons) are cheap to hash by content, which
				// also makes a reused buffer safe
				if m, ok := o.Img.(*image.RGBA); ok {
					for y := b.Min.Y; y < b.Max.Y; y++ {
						off := m.PixOffset(b.Min.X, y)
						row := m.Pix[off : off+4*b.Dx()]
						for i := 0; i+8 <= len(row); i += 8 {
							h.u(binary.LittleEndian.Uint64(row[i:]))
						}
						for i := len(row) &^ 7; i < len(row); i++ {
							h.u(uint64(row[i]))
						}
					}
				}
			}
		}
		h.rect(o.Dst)
		h.rect(o.Src)
		h.radii(o.Radii)
		h.b(o.Smooth)
		if o.Tile == nil {
			h.u(0)
		} else {
			h.u(1)
			h.f(o.Tile.Origin[0])
			h.f(o.Tile.Origin[1])
			h.f(o.Tile.Size[0])
			h.f(o.Tile.Size[1])
			h.f(o.Tile.Step[0])
			h.f(o.Tile.Step[1])
			h.b(o.Tile.RepeatX)
			h.b(o.Tile.RepeatY)
		}
	case Gradient:
		h.u(5)
		h.rect(o.Rect)
		h.s(o.Kind)
		h.radii(o.Radii)
		h.i(len(o.Stops))
		for _, s := range o.Stops {
			h.f(s.Pos)
			h.col(s.Color)
		}
		h.f(o.Angle)
		h.f(o.CX)
		h.f(o.CY)
		h.f(o.RX)
		h.f(o.RY)
		h.f(o.FromAngle)
		h.b(o.Repeating)
		if o.Tile == nil {
			h.u(0)
		} else {
			h.u(1)
			h.f(o.Tile.Origin[0])
			h.f(o.Tile.Origin[1])
			h.f(o.Tile.Size[0])
			h.f(o.Tile.Size[1])
			h.f(o.Tile.Step[0])
			h.f(o.Tile.Step[1])
			h.b(o.Tile.RepeatX)
			h.b(o.Tile.RepeatY)
		}
	case BoxShadow:
		h.u(6)
		h.rect(o.Rect)
		h.radii(o.Radii)
		h.f(o.DX)
		h.f(o.DY)
		h.f(o.Blur)
		h.f(o.Spread)
		h.col(o.Color)
		h.b(o.Inset)
	case PushClip:
		h.u(7)
		h.rect(o.Rect)
		h.radii(o.Radii)
	case PopClip:
		h.u(8)
	case PushLayer:
		h.u(9)
		h.f(o.Opacity)
		h.s(o.Blend)
		h.fl(o.Filter)
		h.rect(o.Bounds)
	case PopLayer:
		h.u(10)
	case PushTransform:
		h.u(11)
		for _, v := range o.M {
			h.f(v)
		}
		h.b(o.Lattice)
		h.f(o.LatX)
		h.f(o.LatY)
	case PopTransform:
		h.u(12)
	case Scrollbar:
		h.u(13)
		h.rect(o.Rect)
		h.b(o.Vertical)
		h.f(o.ThumbOff)
		h.f(o.ThumbLen)
		h.b(o.Dark)
	case ScrollCorner:
		h.u(14)
		h.rect(o.Rect)
		h.b(o.Dark)
	case SVGDraw:
		h.u(15)
		h.rect(o.Dst)
		ds, ok := o.Doc.sig()
		if !ok {
			return 0, false
		}
		h.u(ds)
		h.col(o.Color)
		h.f(o.FontSize)
		h.s(o.Family)
		h.i(o.Weight)
		h.b(o.Italic)
	case PushMask:
		h.u(16)
		h.rect(o.Bounds)
		h.i(len(o.Layers))
		for _, l := range o.Layers {
			h.s(l.Mode)
			h.s(l.Composite)
			h.i(len(l.Ops))
			for _, sub := range l.Ops {
				s, ok := OpSig(sub)
				if !ok {
					return 0, false
				}
				h.u(s)
			}
		}
	case PopMask:
		h.u(17)
	default:
		return 0, false
	}
	return uint64(h), true
}

// ---- SVG scene signatures -------------------------------------------------------------

func strSig(s string) uint64 {
	h := sigHasher(0x13198A2E03707344)
	h.s(s)
	return uint64(h)
}

func mapSig(m map[string]string) uint64 {
	var acc uint64
	for k, v := range m {
		acc += mix(strSig(k), strSig(v)) // order independent
	}
	return mix(uint64(len(m)), acc)
}

// sig is the node's content signature (memoised: icon sprites share nodes).
func (n *SVGNode) sig(depth int) (uint64, bool) {
	if n == nil {
		return 1, true
	}
	if atomic.LoadUint32(&n.sigState) == 1 {
		return atomic.LoadUint64(&n.sigV), true
	}
	if atomic.LoadUint32(&n.sigState) == 2 {
		return 0, false
	}
	if depth > 200 {
		return 0, false
	}
	h := sigHasher(0xA4093822299F31D0)
	h.s(n.Tag)
	h.u(mapSig(n.A))
	h.s(n.Text)
	h.s(n.SubID)
	ok := true
	h.i(len(n.Kids))
	for _, k := range n.Kids {
		s, kok := k.sig(depth + 1)
		ok = ok && kok
		h.u(s)
	}
	if n.Img != nil {
		v := reflect.ValueOf(n.Img)
		if v.Kind() != reflect.Ptr {
			ok = false
		} else {
			h.u(uint64(v.Pointer()))
			b := n.Img.Bounds()
			h.i(b.Dx())
			h.i(b.Dy())
		}
	}
	if n.Sub != nil {
		s, sok := n.Sub.sigDepth(depth + 1)
		ok = ok && sok
		h.u(s)
	}
	h.i(len(n.Ops))
	for _, op := range n.Ops {
		s, sok := OpSig(op)
		ok = ok && sok
		h.u(s)
	}
	if !ok {
		atomic.StoreUint32(&n.sigState, 2)
		return 0, false
	}
	atomic.StoreUint64(&n.sigV, uint64(h))
	atomic.StoreUint32(&n.sigState, 1)
	return uint64(h), true
}

func (d *SVGDoc) sig() (uint64, bool) { return d.sigDepth(0) }

func (d *SVGDoc) sigDepth(depth int) (uint64, bool) {
	if d == nil {
		return 1, true
	}
	if atomic.LoadUint32(&d.sigState) == 1 {
		return atomic.LoadUint64(&d.sigV), true
	}
	if depth > 8 {
		return 0, false
	}
	h := sigHasher(0x082EFA98EC4E6C89)
	rs, ok := d.Root.sig(0)
	h.u(rs)
	var acc uint64
	ids := 0
	for id, nd := range d.ByID {
		s, nok := nd.sig(0)
		ok = ok && nok
		acc += mix(strSig(id), s)
		ids++
	}
	h.i(ids)
	h.u(acc)
	h.u(mapSig(d.Inherit))
	h.f(d.IntrW)
	h.f(d.IntrH)
	h.f(d.Ratio)
	h.s(d.Base)
	h.s(d.Src)
	h.b(d.HasMedia)
	if !ok {
		return 0, false
	}
	atomic.StoreUint64(&d.sigV, uint64(h))
	atomic.StoreUint32(&d.sigState, 1)
	return uint64(h), true
}
