package raster

import (
	"encoding/base64"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"net/url"
	"strings"
	"sync"
)

// ---- text ----------------------------------------------------------------------------------

func (r *svgR) fontOf(st *sStyle) string {
	it := ""
	if st.italic {
		it = "italic "
	}
	fam := st.family
	if fam == "" {
		fam = "serif"
	}
	return fmt.Sprintf("%s%d %.4gpx %s", it, st.weight, st.fontSize, fam)
}

// ---- <image> ----------------------------------------------------------------------------

var imgCache sync.Map // href -> image.Image

func decodeDataImage(href string) image.Image {
	if v, ok := imgCache.Load(href); ok {
		im, _ := v.(image.Image)
		return im
	}
	var im image.Image
	if strings.HasPrefix(href, "data:") {
		if i := strings.IndexByte(href, ','); i > 0 {
			meta, body := href[5:i], href[i+1:]
			var data []byte
			if strings.Contains(meta, ";base64") {
				data, _ = base64.StdEncoding.DecodeString(strings.Map(func(r rune) rune {
					if r == ' ' || r == '\n' || r == '\r' {
						return -1
					}
					return r
				}, body))
			} else if s, err := url.PathUnescape(body); err == nil {
				data = []byte(s)
			}
			if len(data) > 0 {
				im, _, _ = image.Decode(strings.NewReader(string(data)))
			}
		}
	}
	imgCache.Store(href, im)
	return im
}

// ---- markers ----------------------------------------------------------------------------

type mvert struct {
	x, y    float64
	in, out Pt
	hasIn   bool
	hasOut  bool
}

func nz(p, fb Pt) Pt {
	if p.X != 0 || p.Y != 0 {
		return p
	}
	return fb
}

func pathVertices(p *Path) []mvert {
	var vs []mvert
	var cur, start Pt
	si := 0 // index in Pts
	subStart := -1
	for _, cmd := range p.Cmds {
		switch cmd {
		case 'M':
			cur = Pt{p.Pts[si], p.Pts[si+1]}
			si += 2
			start = cur
			vs = append(vs, mvert{x: cur.X, y: cur.Y})
			subStart = len(vs) - 1
		case 'L':
			to := Pt{p.Pts[si], p.Pts[si+1]}
			si += 2
			d := Pt{to.X - cur.X, to.Y - cur.Y}
			if len(vs) > 0 {
				vs[len(vs)-1].out, vs[len(vs)-1].hasOut = d, true
			}
			vs = append(vs, mvert{x: to.X, y: to.Y, in: d, hasIn: true})
			cur = to
		case 'Q':
			c1 := Pt{p.Pts[si], p.Pts[si+1]}
			to := Pt{p.Pts[si+2], p.Pts[si+3]}
			si += 4
			d0 := nz(Pt{c1.X - cur.X, c1.Y - cur.Y}, Pt{to.X - cur.X, to.Y - cur.Y})
			d1 := nz(Pt{to.X - c1.X, to.Y - c1.Y}, Pt{to.X - cur.X, to.Y - cur.Y})
			if len(vs) > 0 {
				vs[len(vs)-1].out, vs[len(vs)-1].hasOut = d0, true
			}
			vs = append(vs, mvert{x: to.X, y: to.Y, in: d1, hasIn: true})
			cur = to
		case 'C':
			c1 := Pt{p.Pts[si], p.Pts[si+1]}
			c2 := Pt{p.Pts[si+2], p.Pts[si+3]}
			to := Pt{p.Pts[si+4], p.Pts[si+5]}
			si += 6
			chord := Pt{to.X - cur.X, to.Y - cur.Y}
			d0 := nz(Pt{c1.X - cur.X, c1.Y - cur.Y}, nz(Pt{c2.X - cur.X, c2.Y - cur.Y}, chord))
			d1 := nz(Pt{to.X - c2.X, to.Y - c2.Y}, nz(Pt{to.X - c1.X, to.Y - c1.Y}, chord))
			if len(vs) > 0 {
				vs[len(vs)-1].out, vs[len(vs)-1].hasOut = d0, true
			}
			vs = append(vs, mvert{x: to.X, y: to.Y, in: d1, hasIn: true})
			cur = to
		case 'Z':
			d := Pt{start.X - cur.X, start.Y - cur.Y}
			v := mvert{x: start.X, y: start.Y}
			if d.X != 0 || d.Y != 0 {
				if len(vs) > 0 {
					vs[len(vs)-1].out, vs[len(vs)-1].hasOut = d, true
				}
				v.in, v.hasIn = d, true
			} else if len(vs) > 0 {
				v.in, v.hasIn = vs[len(vs)-1].in, vs[len(vs)-1].hasIn
			}
			if subStart >= 0 && subStart < len(vs) && vs[subStart].hasOut {
				v.out, v.hasOut = vs[subStart].out, true
				// the start vertex also sees the closing segment
				vs[subStart].in, vs[subStart].hasIn = v.in, v.hasIn
			}
			vs = append(vs, v)
			cur = start
		}
	}
	return vs
}

func (v mvert) angle() float64 {
	switch {
	case v.hasIn && v.hasOut:
		a1 := math.Atan2(v.in.Y, v.in.X)
		a2 := math.Atan2(v.out.Y, v.out.X)
		d := a2 - a1
		for d > math.Pi {
			d -= 2 * math.Pi
		}
		for d < -math.Pi {
			d += 2 * math.Pi
		}
		return a1 + d/2
	case v.hasIn:
		return math.Atan2(v.in.Y, v.in.X)
	case v.hasOut:
		return math.Atan2(v.out.Y, v.out.X)
	}
	return 0
}

func (r *svgR) drawMarkers(n *svgNode, p *Path, st sStyle, m Matrix) {
	if st.mStart == "" && st.mMid == "" && st.mEnd == "" {
		return
	}
	vs := pathVertices(p)
	if len(vs) == 0 {
		return
	}
	get := func(ref string) *svgNode {
		id, ok := urlRef(ref)
		if !ok {
			return nil
		}
		mk := r.doc.ByID[id]
		if mk == nil || mk.Tag != "marker" {
			return nil
		}
		return mk
	}
	ms, mm, me := get(st.mStart), get(st.mMid), get(st.mEnd)
	for i, v := range vs {
		var mk *svgNode
		isStart := i == 0
		switch {
		case i == 0:
			mk = ms
		case i == len(vs)-1:
			mk = me
		default:
			mk = mm
		}
		if mk == nil {
			continue
		}
		r.drawMarker(mk, v, isStart, st, m)
	}
}

func (r *svgR) drawMarker(mk *svgNode, v mvert, isStart bool, st sStyle, m Matrix) {
	if r.active[mk] {
		return
	}
	r.active[mk] = true
	defer delete(r.active, mk)
	mw := r.length(mk.A["markerwidth"], r.vw, st.fontSize, 3)
	mh := r.length(mk.A["markerheight"], r.vh, st.fontSize, 3)
	if mw <= 0 || mh <= 0 {
		return
	}
	mst := r.inherit(r.root, mk.A)
	rx := r.length(mk.A["refx"], r.vw, mst.fontSize, 0)
	ry := r.length(mk.A["refy"], r.vh, mst.fontSize, 0)
	ang := 0.0
	switch o := strings.TrimSpace(mk.A["orient"]); o {
	case "auto":
		ang = v.angle()
	case "auto-start-reverse":
		ang = v.angle()
		if isStart {
			ang += math.Pi
		}
	default:
		if f, ok := svgNumber(strings.TrimSuffix(o, "deg")); ok {
			ang = f * math.Pi / 180
		}
	}
	s := 1.0
	if mk.A["markerunits"] != "userSpaceOnUse" {
		s = st.sw
	}
	vb := splitNums(strings.ReplaceAll(mk.A["viewbox"], ",", " "))
	vm, hasVB := viewBoxMatrix(vb, mk.A["preserveaspectratio"], mw, mh)
	if !hasVB {
		vm = Identity
	}
	rpx, rpy := vm.Apply(rx, ry)
	base := m.Mul(Translate(v.x, v.y)).Mul(Rotate(ang)).Mul(Scale(s, s)).Mul(Translate(-rpx, -rpy))
	c := r.c
	c.Save()
	defer c.Restore()
	if ov := mk.A["overflow"]; ov != "visible" && ov != "auto" {
		c.SetTransform(base)
		if hasVB {
			// clip to the viewBox region mapped into the viewport
			c.ClipRect(0, 0, mw, mh)
		} else {
			c.ClipRect(0, 0, mw, mh)
		}
	}
	ovw, ovh := r.vw, r.vh
	if hasVB {
		r.vw, r.vh = vb[2], vb[3]
	}
	r.drawChildren(mk, mst, base.Mul(vm))
	r.vw, r.vh = ovw, ovh
}
