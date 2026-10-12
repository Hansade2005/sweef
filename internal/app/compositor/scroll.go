package compositor

import "math"

// Scroller turns wheel input into smooth, pixel-based motion with easing.
// It only needs ticking while Animating() is true, so a still page costs
// no CPU.
type Scroller struct {
	Pos, Target float64 // device px
	Max         float64
	// Tau is the easing time constant in seconds (smaller = snappier).
	Tau float64
	// Smooth false jumps straight to the target.
	Smooth bool
}

// NewScroller returns a smooth scroller.
func NewScroller() *Scroller { return &Scroller{Tau: 0.07, Smooth: true} }

// Reset puts both position and target at p.
func (s *Scroller) Reset(p float64) { s.Pos, s.Target = p, p }

// SetMax updates the scroll range and clamps.
func (s *Scroller) SetMax(m float64) {
	if m < 0 {
		m = 0
	}
	s.Max = m
	s.Target = clamp(s.Target, 0, m)
	s.Pos = clamp(s.Pos, 0, m)
}

// Wheel adds a pixel delta (positive = scroll down).
func (s *Scroller) Wheel(dy float64) {
	s.Target = clamp(s.Target+dy, 0, s.Max)
	if !s.Smooth {
		s.Pos = s.Target
	}
}

// ScrollTo sets the target absolutely (keyboard, scrollbar drag).
func (s *Scroller) ScrollTo(y float64, instant bool) {
	s.Target = clamp(y, 0, s.Max)
	if instant || !s.Smooth {
		s.Pos = s.Target
	}
}

// Animating reports whether Tick still has movement to do.
func (s *Scroller) Animating() bool { return math.Abs(s.Target-s.Pos) >= 0.5 }

// Tick advances by dt seconds and returns the integer position.
func (s *Scroller) Tick(dt float64) int {
	if s.Animating() {
		k := 1 - math.Exp(-dt/s.Tau)
		s.Pos += (s.Target - s.Pos) * k
		if math.Abs(s.Target-s.Pos) < 0.5 {
			s.Pos = s.Target
		}
	}
	return int(math.Round(s.Pos))
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
