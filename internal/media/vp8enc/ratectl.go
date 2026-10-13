package vp8enc

import "math"

// rateCtl is a constant-bitrate controller: a virtual buffer tracks the bits spent beyond the
// per-frame budget, the target size of every frame is corrected by it, and the base quantiser
// follows the logarithm of the size error. Key frames may use several frame budgets (the sender
// paces them out); the excess is recovered by the following frames.
type rateCtl struct {
	bps, fps   float64
	minQ, maxQ int
	q          float64
	buf        float64 // bits produced beyond what the budget allowed (negative: unused credit)
	pixels     int
	frames     int
	lastKeyQ   float64
}

func (r *rateCtl) init(bps, fps float64, minQ, maxQ, pixels int) {
	r.bps, r.fps, r.minQ, r.maxQ, r.pixels = bps, fps, minQ, maxQ, pixels
	r.frames = 0
	r.buf = 0
	r.q = r.initialQ()
}

func (r *rateCtl) initialQ() float64 {
	bpp := r.bps / (r.fps * float64(max(r.pixels, 1)))
	q := 40 - 22*math.Log2(bpp/0.1)
	return math.Max(float64(r.minQ), math.Min(float64(r.maxQ), q))
}

func (r *rateCtl) setBitrate(bps float64) { r.bps = bps }
func (r *rateCtl) setFPS(f float64)       { r.fps = f }
func (r *rateCtl) setPixels(p int) {
	r.pixels = p
	r.q = r.initialQ()
	r.frames = 0
}

func (r *rateCtl) budget() float64  { return r.bps / r.fps }
func (r *rateCtl) bufSize() float64 { return r.bps * 0.6 }

// target is the number of bits the next frame should take.
func (r *rateCtl) target(key bool) float64 {
	b := r.budget()
	if key {
		t := b * 4
		if r.frames == 0 {
			t = b * 5
		}
		return math.Min(t, r.bps*0.35)
	}
	t := b - r.buf/(r.fps*0.5)
	return math.Max(0.35*b, math.Min(1.8*b, t))
}

func (r *rateCtl) startQ(key bool) int {
	q := r.q
	if key {
		if r.frames == 0 {
			q = r.initialQ() + 2
		} else {
			q += 5
		}
	}
	return clampI(int(math.Round(q)), r.minQ, r.maxQ)
}

// shouldDrop reports whether the buffer is so far over budget that the next inter frame is skipped.
func (r *rateCtl) shouldDrop() bool { return r.buf > 1.3*r.bufSize() && int(r.q) >= r.maxQ-8 }

func (r *rateCtl) dropped() {
	r.buf -= r.budget()
}

func (r *rateCtl) update(key bool, q int, bits, target float64) {
	r.buf += bits - r.budget()
	r.buf = math.Max(-0.3*r.bps, math.Min(2*r.bufSize(), r.buf))
	r.frames++
	if key {
		r.q = math.Max(float64(r.minQ), float64(q)-3)
		r.lastKeyQ = float64(q)
		return
	}
	err := math.Log(math.Max(bits, 8) / math.Max(target, 8))
	d := 11 * err
	if d > 8 {
		d = 8
	} else if d < -5 {
		d = -5
	}
	r.q = math.Max(float64(r.minQ), math.Min(float64(r.maxQ), r.q+d))
}
