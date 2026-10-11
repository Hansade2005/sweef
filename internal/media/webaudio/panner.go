package webaudio

import "math"

// Listener is the AudioListener state (params live on a hidden node).
type Listener struct {
	node *Node
}

func newListener(c *Context) *Listener { return &Listener{} }

func (c *Context) ensureListener() *Node {
	if c.Listener.node != nil {
		return c.Listener.node
	}
	const big = 3.4028234663852886e38
	n := c.newNode("listener", 0, 0)
	for _, a := range []string{"positionX", "positionY", "positionZ"} {
		n.addParam(a, 0, -big, big, true)
	}
	n.addParam("forwardX", 0, -big, big, true)
	n.addParam("forwardY", 0, -big, big, true)
	n.addParam("forwardZ", -1, -big, big, true)
	n.addParam("upX", 0, -big, big, true)
	n.addParam("upY", 1, -big, big, true)
	n.addParam("upZ", 0, -big, big, true)
	n.proc = nopProc{}
	c.Listener.node = n
	return n
}

// ListenerNode returns the id of the node holding listener params.
func (c *Context) ListenerNode() int { return c.ensureListener().ID }

type vec struct{ x, y, z float64 }

func (a vec) sub(b vec) vec       { return vec{a.x - b.x, a.y - b.y, a.z - b.z} }
func (a vec) dot(b vec) float64   { return a.x*b.x + a.y*b.y + a.z*b.z }
func (a vec) cross(b vec) vec     { return vec{a.y*b.z - a.z*b.y, a.z*b.x - a.x*b.z, a.x*b.y - a.y*b.x} }
func (a vec) len() float64        { return math.Sqrt(a.dot(a)) }
func (a vec) scale(s float64) vec { return vec{a.x * s, a.y * s, a.z * s} }
func (a vec) norm() vec {
	l := a.len()
	if l == 0 {
		return a
	}
	return a.scale(1 / l)
}

type pannerProc struct {
	distModel  string
	hrtf       bool
	ref, max   float64
	rolloff    float64
	innerAngle float64
	outerAngle float64
	outerGain  float64
}

func (p *pannerProc) process(n *Node, c *Context, start int64, frames int, in [][][]float32) {
	t := float64(start) / c.SampleRate
	pv := func(name string) float64 { return float64(n.Params[name].compute(c, start, 1)[0]) }
	l := c.ensureListener()
	lv := func(name string) float64 { return l.Params[name].ValueAt(t) }
	src := vec{pv("positionX"), pv("positionY"), pv("positionZ")}
	ori := vec{pv("orientationX"), pv("orientationY"), pv("orientationZ")}
	lpos := vec{lv("positionX"), lv("positionY"), lv("positionZ")}
	fwd := vec{lv("forwardX"), lv("forwardY"), lv("forwardZ")}
	up := vec{lv("upX"), lv("upY"), lv("upZ")}

	sl := src.sub(lpos)
	dist := sl.len()
	az, el := 0.0, 0.0
	if dist > 0 {
		slN := sl.norm()
		right := fwd.cross(up)
		if right.len() != 0 {
			right = right.norm()
			fwdN := fwd.norm()
			upv := right.cross(fwdN)
			upProj := slN.dot(upv)
			proj := slN.sub(upv.scale(upProj)).norm()
			az = math.Acos(clampF(proj.dot(right), -1, 1)) * 180 / math.Pi
			if proj.dot(fwdN) < 0 {
				az = 360 - az
			}
			if az >= 0 && az <= 270 {
				az = 90 - az
			} else {
				az = 450 - az
			}
			el = 90 - math.Acos(clampF(slN.dot(upv), -1, 1))*180/math.Pi
			if el > 90 {
				el = 180 - el
			} else if el < -90 {
				el = -180 - el
			}
		}
	}
	_ = el
	// distance gain
	var dg float64
	d := dist
	switch p.distModel {
	case "linear":
		dd := clampF(d, p.ref, p.max)
		if p.max == p.ref {
			dg = 1
		} else {
			dg = 1 - p.rolloff*(dd-p.ref)/(p.max-p.ref)
		}
	case "exponential":
		dd := d
		if dd < p.ref {
			dd = p.ref
		}
		if p.ref == 0 {
			dg = 1
		} else {
			dg = math.Pow(dd/p.ref, -p.rolloff)
		}
	default:
		dd := d
		if dd < p.ref {
			dd = p.ref
		}
		dg = p.ref / (p.ref + p.rolloff*(dd-p.ref))
	}
	if math.IsNaN(dg) || math.IsInf(dg, 0) {
		dg = 1
	}
	// cone gain
	cg := 1.0
	if ori.len() != 0 && !(p.innerAngle == 360 && p.outerAngle == 360) {
		toListener := lpos.sub(src).norm()
		angle := math.Acos(clampF(ori.norm().dot(toListener), -1, 1)) * 180 / math.Pi
		absInner := math.Mod(math.Abs(p.innerAngle), 360)
		if absInner > 180 {
			absInner = 360 - absInner
		}
		absOuter := math.Mod(math.Abs(p.outerAngle), 360)
		if absOuter > 180 {
			absOuter = 360 - absOuter
		}
		a := math.Abs(angle)
		switch {
		case a <= absInner:
			cg = 1
		case a >= absOuter:
			cg = p.outerGain
		default:
			x := (a - absInner) / (absOuter - absInner)
			cg = (1 - x) + p.outerGain*x
		}
	}
	gain := float32(dg * cg)
	// equal-power panning
	a := az
	if a < -180 {
		a = -180
	} else if a > 180 {
		a = 180
	}
	if a < -90 {
		a = -180 - a
	} else if a > 90 {
		a = 180 - a
	}
	out := n.setOut(0, 2)
	s := in[0]
	if len(s) == 1 {
		x := (a + 90) / 180
		gl := float32(math.Cos(x * math.Pi / 2))
		gr := float32(math.Sin(x * math.Pi / 2))
		for i := 0; i < frames; i++ {
			out[0][i] = s[0][i] * gl * gain
			out[1][i] = s[0][i] * gr * gain
		}
		return
	}
	for i := 0; i < frames; i++ {
		l0, r0 := s[0][i], s[1][i]
		if a <= 0 {
			x := (a + 90) / 90
			out[0][i] = (l0 + r0*float32(math.Cos(x*math.Pi/2))) * gain
			out[1][i] = r0 * float32(math.Sin(x*math.Pi/2)) * gain
		} else {
			x := a / 90
			out[0][i] = l0 * float32(math.Cos(x*math.Pi/2)) * gain
			out[1][i] = (r0 + l0*float32(math.Sin(x*math.Pi/2))) * gain
		}
	}
}

func clampF(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
