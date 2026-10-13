package main

import (
	"time"

	"github.com/sweefbrowser/sweef/internal/engine"
)

// loadProgressive is the GUI's loading path in miniature: navigate, paint the
// first frame, then drive the rest of the load in slices, repainting when the
// document changed (the way the window's tab worker does).
func loadProgressive(t *engine.Tab, u string, r *prog, t0 time.Time) error {
	t.SetAsyncImages(true)
	dirty := make(chan struct{}, 1)
	t.SetUpdateHook(func() {
		select {
		case dirty <- struct{}{}:
		default:
		}
	})
	if err := t.NavigateProgressive(u, nil); err != nil {
		return err
	}
	r.ready = time.Since(t0)
	t.PaintRGBA(1280, 760*3, 1, false)
	r.firstFrame = time.Since(t0)
	r.frames = 1
	gen := t.Generation()
	lastPaint := time.Now()
	for t.LoadPending() {
		if r.loaded == 0 && t.LoadLoaded() {
			r.loaded = time.Since(t0)
		}
		more, wait := t.LoadStep(30 * time.Millisecond)
		if g := t.Generation(); g != gen && time.Since(lastPaint) > 120*time.Millisecond {
			t.PaintRGBA(1280, 760*3, 1, false)
			gen, lastPaint = g, time.Now()
			r.frames++
		}
		if !more {
			break
		}
		if wait > 0 {
			time.Sleep(wait)
		}
	}
	if r.loaded == 0 {
		r.loaded = time.Since(t0)
	}
	r.done = time.Since(t0)
	// images still arriving: give them a moment, as the window would
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-dirty:
			t.PaintRGBA(1280, 760*3, 1, false)
			r.frames++
			r.lastPaint = time.Since(t0)
			deadline = time.Now().Add(700 * time.Millisecond)
		case <-time.After(50 * time.Millisecond):
		}
	}
	return nil
}
