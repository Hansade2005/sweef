package compositor

import (
	"math"
	"testing"
)

// run ticks the scroller at 60 Hz from time t0 for up to maxT seconds and returns the end time and the positions.
func run(s *Scroller, t0, maxT float64) (float64, []float64) {
	var ps []float64
	t := t0
	for t-t0 < maxT && s.Animating() {
		t += 1.0 / 60
		ps = append(ps, float64(s.TickAt(t)))
	}
	return t, ps
}

func TestSpringNeverOvershootsAndSettles(t *testing.T) {
	s := NewScroller()
	s.SetMax(10000)
	s.SetClock(func() float64 { return 0 })
	s.Wheel(120)
	end, ps := run(s, 0, 2)
	if s.Animating() || s.Pos != 120 {
		t.Fatalf("did not settle on the target: pos %v animating %v", s.Pos, s.Animating())
	}
	prev := -1.0
	for _, p := range ps {
		if p < prev || p > 120 {
			t.Fatalf("spring moved backwards or overshot: %v after %v", p, prev)
		}
		prev = p
	}
	if end > 0.45 {
		t.Fatalf("a wheel notch took %.2fs to settle", end)
	}
	if ps[0] < 1 {
		t.Fatalf("the first frame must already move, got %v", ps[0])
	}
}

func TestSpringKeepsSpeedAcrossNotches(t *testing.T) {
	s := NewScroller()
	s.SetMax(100000)
	s.SetClock(func() float64 { return 0 })
	s.Wheel(120)
	now := 0.0
	var speeds []float64
	prev := 0.0
	for i := 0; i < 12; i++ { // a notch every 33 ms for 400 ms
		for k := 0; k < 2; k++ {
			now += 1.0 / 60
			p := float64(s.TickAt(now))
			speeds = append(speeds, (p-prev)*60)
			prev = p
		}
		s.Wheel(120)
	}
	// the speed after the first few notches stays near the steady rate of 120 px per 33 ms and never drops to
	// zero between notches
	for _, v := range speeds[4:] {
		if v < 1500 || v > 6000 {
			t.Fatalf("speed %v px/s between notches (want it steady near 3600): %v", v, speeds)
		}
	}
}

func TestSmoothOffJumps(t *testing.T) {
	s := NewScroller()
	s.Smooth = false
	s.SetMax(500)
	s.Wheel(80)
	if s.Pos != 80 || s.Animating() {
		t.Fatal("non-smooth wheel must jump")
	}
	s.PreciseAt(30, 1)
	if s.Pos != 110 || s.Animating() {
		t.Fatal("non-smooth precise must jump and never fling")
	}
	s.EaseTo(300)
	if s.Pos != 300 || s.Animating() {
		t.Fatal("non-smooth ease must jump")
	}
}

func TestPreciseFollowsFingersThenFlings(t *testing.T) {
	s := NewScroller()
	s.SetMax(1e6)
	s.Reset(1000)
	// 8 ms events of 30 px: 3750 px/s
	now := 0.0
	for i := 0; i < 20; i++ {
		now += 0.008
		s.PreciseAt(30, now)
		if got := s.TickAt(now); got != 1000+30*(i+1) {
			t.Fatalf("event %d: the view must follow the fingers exactly, at %d", i, got)
		}
	}
	atLift := s.Pos
	// fingers lift: no events. After the gap the fling starts with the measured speed.
	end, ps := run(s, now, 5)
	if s.Animating() {
		t.Fatal("fling never ended")
	}
	dist := s.Pos - atLift
	want := 3750 * s.FlingTau // exponential decay: total distance is v * tau
	if math.Abs(dist-want) > 0.15*want {
		t.Fatalf("fling distance %.0f, want about %.0f (v*tau)", dist, want)
	}
	prev := atLift
	last := math.MaxFloat64
	for i, p := range ps {
		step := p - prev
		if step < 0 {
			t.Fatal("fling went backwards")
		}
		// after the gap detection the per-frame distance decays monotonically
		if i > 8 && step > last+1 {
			t.Fatalf("fling is not decaying: %v after %v", step, last)
		}
		if i > 8 {
			last = step
		}
		prev = p
	}
	if end-now > 3.5 {
		t.Fatalf("fling lasted %.1fs", end-now)
	}
}

func TestSlowPreciseGestureDoesNotFling(t *testing.T) {
	s := NewScroller()
	s.SetMax(1e6)
	s.Reset(500)
	now := 0.0
	for i := 0; i < 10; i++ {
		now += 0.016
		s.PreciseAt(1, now) // 62 px/s
	}
	pos := s.Pos
	run(s, now, 2)
	if s.Pos != pos || s.Animating() {
		t.Fatalf("a slow drag must stop where the fingers stopped: %v -> %v", pos, s.Pos)
	}
}

func TestFlingStopsAtEdgesAndOnNewInput(t *testing.T) {
	s := NewScroller()
	s.SetMax(1000)
	s.Reset(900)
	now := 0.0
	for i := 0; i < 15; i++ {
		now += 0.008
		s.PreciseAt(40, now)
	}
	run(s, now, 5)
	if s.Pos != 1000 {
		t.Fatalf("a fling into the bottom must stop at the edge, at %v", s.Pos)
	}
	// a fling is stopped by a new wheel notch, which then starts from where the view is
	s.Reset(100)
	s.SetMax(1e6)
	now = 10
	for i := 0; i < 15; i++ {
		now += 0.008
		s.PreciseAt(40, now)
	}
	now += 0.06
	s.TickAt(now) // gap: the fling starts
	if s.mode != modeFling {
		t.Fatalf("expected a fling, mode %v", s.mode)
	}
	pos := s.Pos
	s.SetClock(func() float64 { return now })
	s.Stop()
	if s.Animating() || s.Pos != pos {
		t.Fatal("Stop must freeze the view")
	}
	// reverse direction wheel stops the fling and goes the other way
	s.Reset(100)
	now = 20
	for i := 0; i < 15; i++ {
		now += 0.008
		s.PreciseAt(40, now)
	}
	now += 0.06
	s.TickAt(now)
	s.Wheel(-120)
	if s.mode != modeSpring || s.vel != 0 {
		t.Fatalf("a wheel notch during a fling must take over from rest, mode %v vel %v", s.mode, s.vel)
	}
}

func TestEaseIsMonotoneAndExact(t *testing.T) {
	s := NewScroller()
	s.SetMax(1e6)
	s.Reset(0)
	s.EaseToAt(900, 0)
	end, ps := run(s, 0, 2)
	if s.Pos != 900 || s.Animating() {
		t.Fatalf("ease must end exactly on target, at %v", s.Pos)
	}
	prev := 0.0
	for _, p := range ps {
		if p < prev {
			t.Fatal("ease moved backwards")
		}
		prev = p
	}
	// the curve decelerates: the first half of the time covers more than half of the distance
	half := ps[len(ps)/2-1]
	if half < 600 {
		t.Fatalf("ease-out curve expected, half the time covered only %v of 900", half)
	}
	if want := EaseDuration(900); math.Abs(end-want) > 0.04 {
		t.Fatalf("ease took %.3fs, duration %.3fs", end, want)
	}
	if EaseDuration(50) >= EaseDuration(900) || EaseDuration(1e6) > 0.6 || EaseDuration(0) < 0.1 {
		t.Fatalf("durations: 50px %.2f, 900px %.2f, far %.2f", EaseDuration(50), EaseDuration(900), EaseDuration(1e6))
	}
}

func TestEaseByAccumulates(t *testing.T) {
	s := NewScroller()
	s.SetMax(1e6)
	s.Reset(0)
	s.EaseByAt(800, 0)
	s.TickAt(0.05)
	s.EaseByAt(800, 0.05) // a second PageDown while the first is running
	if s.Target != 1600 {
		t.Fatalf("second page key must add to the running target: %v", s.Target)
	}
	run(s, 0.05, 3)
	if s.Pos != 1600 {
		t.Fatalf("ended at %v", s.Pos)
	}
	// clamped at the end
	s.SetMax(1700)
	s.EaseByAt(800, 5)
	run(s, 5, 3)
	if s.Pos != 1700 {
		t.Fatalf("ease must clamp to the range, %v", s.Pos)
	}
}

func TestStalledFrameDoesNotTeleport(t *testing.T) {
	s := NewScroller()
	s.SetMax(1e6)
	s.Reset(0)
	s.SetClock(func() float64 { return 0 })
	s.Wheel(1000)
	s.TickAt(0.001)
	p := s.TickAt(5) // a 5 s stall
	if p >= 1000 {
		t.Fatalf("a stalled frame jumped to %d", p)
	}
}

func TestSetMaxClampsAndRetargets(t *testing.T) {
	s := NewScroller()
	s.SetMax(1000)
	s.Reset(900)
	s.Wheel(100)
	s.SetMax(500)
	if s.Pos != 500 || s.Target != 500 {
		t.Fatalf("SetMax must clamp: pos %v target %v", s.Pos, s.Target)
	}
}

func TestEaseCurveShape(t *testing.T) {
	if easeCurve(0) != 0 || easeCurve(1) != 1 {
		t.Fatal("end points")
	}
	prev := 0.0
	for i := 1; i <= 100; i++ {
		v := easeCurve(float64(i) / 100)
		if v < prev-1e-9 || v > 1+1e-9 {
			t.Fatalf("curve not monotone/bounded at %d: %v after %v", i, v, prev)
		}
		prev = v
	}
}
