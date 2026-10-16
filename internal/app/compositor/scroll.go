package compositor

import (
	"math"
	"time"
)

// Scroller turns scroll input into smooth, pixel-based motion. It only needs ticking while Animating() is true,
// so a still page costs no CPU. Everything is in device pixels and time is passed in (seconds on any monotonic
// clock), so the physics is deterministic and testable.
//
// Four kinds of motion, chosen by the input that starts them:
//
//   - Spring (a mouse-wheel notch, an arrow key): the target moves by the delta and the position follows it with a
//     critically damped spring, so quick repeated input accumulates, keeps its speed from one notch to the next
//     and never overshoots.
//   - Direct (a precision touchpad: precise pixel deltas): the position follows the fingers exactly. When the deltas
//     stop and the last ones were fast, the gesture ends in a fling.
//   - Fling: the speed measured at the end of a direct gesture decays exponentially (Tau) until the view stops or
//     reaches an edge.
//   - Ease (PageUp/PageDown/Space, Home/End, programmatic smooth scrolling, anchor jumps): a timed move along a
//     cubic-bezier ease curve whose duration grows with the distance.
type Scroller struct {
	Pos, Target float64 // device px
	Max         float64
	// Tau is the time constant in seconds of the spring (settle time is about 4 Tau; smaller is snappier).
	Tau float64
	// FlingTau is the decay time constant of a fling in seconds.
	FlingTau float64
	// Smooth false jumps straight to the target and never flings.
	Smooth bool

	mode  scrollMode
	vel   float64 // px/s: spring velocity, or the fling speed
	last  float64 // time of the previous tick
	hasT  bool
	clock func() float64
	virt  bool    // Tick(dt) drives a virtual clock (fixed-step callers and tests)
	vt    float64 // the virtual clock

	// ease
	easeFrom, easeTo, easeT0, easeDur float64

	// direct gesture
	evs    [directKeep]directEv
	nev    int
	lastIn float64
}

type scrollMode uint8

const (
	modeIdle scrollMode = iota
	modeSpring
	modeDirect
	modeFling
	modeEase
)

type directEv struct{ t, dy float64 }

const (
	directKeep   = 12
	directGap    = 0.05  // s without precise input that ends a direct gesture
	directWindow = 0.08  // s of input the fling speed is measured over
	flingMin     = 180.0 // px/s: slower gestures end where they are
	flingStop    = 12.0  // px/s: a fling slower than this has finished
	maxFlingV    = 14000.0
	// headStart is how much of the first frame of a move is already used up when the input arrives: the frame the
	// input triggers is drawn at once, so it shows the move as it is one frame into it, not the position it started at
	headStart = 0.012
)

var start = time.Now()

func nowSecs() float64 { return time.Since(start).Seconds() }

// NewScroller returns a smooth scroller.
func NewScroller() *Scroller { return &Scroller{Tau: 0.045, FlingTau: 0.32, Smooth: true} }

// SetClock replaces the time source (tests).
func (s *Scroller) SetClock(f func() float64) { s.clock = f }

func (s *Scroller) now() float64 {
	switch {
	case s.clock != nil:
		return s.clock()
	case s.virt:
		return s.vt
	}
	return nowSecs()
}

// Reset puts both position and target at p and stops all motion.
func (s *Scroller) Reset(p float64) {
	s.Pos, s.Target = p, p
	s.stop()
}

func (s *Scroller) stop() {
	s.mode = modeIdle
	s.vel = 0
	s.nev = 0
}

// SetMax updates the scroll range and clamps.
func (s *Scroller) SetMax(m float64) {
	if m < 0 {
		m = 0
	}
	s.Max = m
	s.Target = clamp(s.Target, 0, m)
	s.Pos = clamp(s.Pos, 0, m)
	if s.mode == modeIdle {
		s.Target = s.Pos
	}
}

// Wheel adds a pixel delta (positive = scroll down) as a spring move: a mouse-wheel notch or an arrow key.
func (s *Scroller) Wheel(dy float64) {
	if s.mode == modeEase || s.mode == modeFling || s.mode == modeDirect {
		s.Target = s.Pos
		s.vel = 0
	}
	s.Target = clamp(s.Target+dy, 0, s.Max)
	if !s.Smooth {
		s.Pos = s.Target
		s.stop()
		return
	}
	if s.mode != modeSpring {
		s.last, s.hasT = s.now()-headStart, true
	}
	s.mode = modeSpring
}

// ScrollTo sets the target absolutely (scrollbar drag, restore, programmatic jump). instant moves there at
// once; otherwise the move is a short eased one.
func (s *Scroller) ScrollTo(y float64, instant bool) {
	y = clamp(y, 0, s.Max)
	if instant || !s.Smooth {
		s.Pos, s.Target = y, y
		s.stop()
		return
	}
	s.EaseTo(y)
}

// EaseDuration is how long an eased move over dist px takes (seconds).
func EaseDuration(dist float64) float64 {
	d := 0.12 + 0.19*math.Sqrt(math.Abs(dist)/400)
	return clamp(d, 0.12, 0.6)
}

// EaseTo starts a timed move from the current position to y (clamped) along the standard ease curve. A move
// that is already running is retargeted from where it is.
func (s *Scroller) EaseTo(y float64) {
	s.EaseToAt(y, s.now())
}

// EaseToAt is EaseTo with an explicit time.
func (s *Scroller) EaseToAt(y, now float64) {
	y = clamp(y, 0, s.Max)
	if !s.Smooth {
		s.Pos, s.Target = y, y
		s.stop()
		return
	}
	if math.Abs(y-s.Pos) < 0.5 {
		s.Pos, s.Target = y, y
		s.stop()
		return
	}
	s.mode = modeEase
	s.easeFrom, s.easeTo, s.easeT0 = s.Pos, y, now-headStart
	s.easeDur = EaseDuration(y - s.Pos)
	s.Target = y
	s.vel = 0
	s.last, s.hasT = now, true
}

// EaseBy eases by dy from where the view is going (the target of a running ease or spring), so repeated page keys
// add up instead of restarting.
func (s *Scroller) EaseBy(dy float64) { s.EaseByAt(dy, s.now()) }

// EaseByAt is EaseBy with an explicit time.
func (s *Scroller) EaseByAt(dy, now float64) {
	base := s.Pos
	if s.mode == modeEase || s.mode == modeSpring {
		base = s.Target
	}
	s.EaseToAt(base+dy, now)
}

// Precise applies a pixel-accurate delta from a precision touchpad: the view follows the fingers, and when they
// lift at speed the gesture continues as a fling.
func (s *Scroller) Precise(dy float64) { s.PreciseAt(dy, s.now()) }

// PreciseAt is Precise with an explicit time.
func (s *Scroller) PreciseAt(dy, now float64) {
	if !s.Smooth {
		s.Wheel(dy)
		return
	}
	if s.mode != modeDirect {
		s.mode = modeDirect
		s.nev = 0
		s.vel = 0
	}
	s.Pos = clamp(s.Pos+dy, 0, s.Max)
	s.Target = s.Pos
	s.lastIn = now
	s.last, s.hasT = now, true
	if s.nev == directKeep {
		copy(s.evs[:], s.evs[1:])
		s.nev--
	}
	s.evs[s.nev] = directEv{now, dy}
	s.nev++
}

// Stop halts any motion where it is (a click, a key press, a touch down stops a fling).
func (s *Scroller) Stop() {
	s.Target = s.Pos
	s.stop()
}

// Animating reports whether Tick still has movement to do.
func (s *Scroller) Animating() bool { return s.mode != modeIdle }

// Tick advances a virtual clock by dt seconds and returns the integer position: the call for fixed-step callers
// and tests. Callers with real timestamps use TickAt.
func (s *Scroller) Tick(dt float64) int {
	if !s.virt {
		s.virt = true
		s.vt = s.last
	}
	s.vt += dt
	return s.TickAt(s.vt)
}

// TickNow advances to the current time (the scroller's clock) and returns the integer position.
func (s *Scroller) TickNow() int { return s.TickAt(s.now()) }

// TickAt advances to time now and returns the integer position.
func (s *Scroller) TickAt(now float64) int {
	dt := now - s.last
	if !s.hasT {
		dt = 0
	}
	s.last, s.hasT = now, true
	return s.tickAt(now, dt)
}

func (s *Scroller) tickAt(now, dt float64) int {
	if dt < 0 {
		dt = 0
	}
	if dt > 0.1 {
		dt = 0.1 // a stalled frame must not teleport the view
	}
	switch s.mode {
	case modeSpring:
		s.stepSpring(dt)
	case modeEase:
		t := (now - s.easeT0) / s.easeDur
		if t >= 1 {
			s.Pos, s.Target = s.easeTo, s.easeTo
			s.stop()
		} else {
			s.Pos = s.easeFrom + (s.easeTo-s.easeFrom)*easeCurve(t)
		}
	case modeDirect:
		if now-s.lastIn > directGap {
			s.endDirect()
		}
	case modeFling:
		s.stepFling(dt)
	}
	return int(math.Round(s.Pos))
}

// stepSpring advances the critically damped spring toward Target by dt using the closed-form solution, so any
// dt is stable.
func (s *Scroller) stepSpring(dt float64) {
	w := 1 / s.Tau
	e := s.Pos - s.Target
	v := s.vel
	ex := math.Exp(-w * dt)
	c2 := v + w*e
	s.Pos = s.Target + (e+c2*dt)*ex
	s.vel = (v - w*c2*dt) * ex
	if math.Abs(s.Pos-s.Target) < 0.4 && math.Abs(s.vel) < 8 {
		s.Pos = s.Target
		s.stop()
	}
}

func (s *Scroller) endDirect() {
	v := s.releaseVelocity()
	s.nev = 0
	if math.Abs(v) < flingMin || s.Pos <= 0 && v < 0 || s.Pos >= s.Max && v > 0 {
		s.Target = s.Pos
		s.stop()
		return
	}
	s.mode = modeFling
	s.vel = clamp(v, -maxFlingV, maxFlingV)
	s.Target = clamp(s.Pos+s.vel*s.FlingTau, 0, s.Max)
}

// releaseVelocity is the scroll speed (px/s) over the last directWindow seconds of the gesture. The delta of the
// oldest event in the window happened before its timestamp, over an interval nobody measured, so it only marks
// the start of the span the others are divided by.
func (s *Scroller) releaseVelocity() float64 {
	if s.nev < 2 {
		return 0
	}
	last := s.evs[s.nev-1].t
	i := s.nev - 1
	for i > 0 && last-s.evs[i-1].t <= directWindow {
		i--
	}
	span := last - s.evs[i].t
	if span < 0.012 {
		return 0
	}
	var sum float64
	for k := i + 1; k < s.nev; k++ {
		sum += s.evs[k].dy
	}
	return sum / span
}

func (s *Scroller) stepFling(dt float64) {
	k := math.Exp(-dt / s.FlingTau)
	// exact integral of v0*exp(-t/tau) over dt
	s.Pos += s.vel * s.FlingTau * (1 - k)
	s.vel *= k
	if s.Pos <= 0 || s.Pos >= s.Max {
		s.Pos = clamp(s.Pos, 0, s.Max)
		s.Target = s.Pos
		s.stop()
		return
	}
	s.Target = clamp(s.Pos+s.vel*s.FlingTau, 0, s.Max)
	if math.Abs(s.vel) < flingStop {
		s.Target = s.Pos
		s.stop()
	}
}

// easeCurve is cubic-bezier(0.2, 0, 0, 1): quick start, long soft landing.
func easeCurve(t float64) float64 {
	if t <= 0 {
		return 0
	}
	if t >= 1 {
		return 1
	}
	const x1, y1, x2, y2 = 0.2, 0.0, 0.0, 1.0
	bez := func(a, b, u float64) float64 { // 1-D cubic bezier with end points 0 and 1
		return 3*a*u*(1-u)*(1-u) + 3*b*u*u*(1-u) + u*u*u
	}
	u := t
	for i := 0; i < 8; i++ { // Newton on x(u) = t
		x := bez(x1, x2, u) - t
		d := 3*x1*(1-u)*(1-u) + 6*(x2-x1)*u*(1-u) + 3*(1-x2)*u*u
		if math.Abs(x) < 1e-6 || d < 1e-6 {
			break
		}
		u -= x / d
	}
	u = clamp(u, 0, 1)
	return bez(y1, y2, u)
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
