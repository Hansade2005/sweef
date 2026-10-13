package render

import "time"

// StageHook, when set (SWEEF_STAGES in internal/engine), receives the time of
// each stage of a layout pass: build (styles + box tree), layout, finish
// (boxes, display list, hit testing).
var StageHook func(stage string, d time.Duration)

type stageClock struct{ t time.Time }

func newStageClock() stageClock {
	if StageHook == nil {
		return stageClock{}
	}
	return stageClock{time.Now()}
}

func (s *stageClock) lap(name string) {
	if StageHook == nil || s.t.IsZero() {
		return
	}
	now := time.Now()
	StageHook(name, now.Sub(s.t))
	s.t = now
}
