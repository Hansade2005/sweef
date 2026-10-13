//go:build linux

package main

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"time"
)

// session is one running browser under one fresh Xvfb, observed and driven from the outside through X11 only.
type session struct {
	xs   *xserver
	obs  *xconn // observer: DAMAGE events and GetImage
	drv  *xconn // driver: XTEST input
	dm   *damage
	xt   *xtest
	win  xwin
	spec *browserSpec
	l    *launched
	lay  layout
	rec  *Recorder
	fix  *fixtureServer
	dir  string
	// debug: write PNGs of key moments here ("" = off)
	shots string
}

func (s *session) abs(r rect) rect { return rect{r.x + s.win.x, r.y + s.win.y, r.w, r.h} }

func (s *session) shot(name string) {
	if s.shots == "" {
		return
	}
	r := rect{s.win.x, s.win.y, s.win.w, s.win.h}
	if im, err := s.obs.grab(r); err == nil {
		os.MkdirAll(s.shots, 0o755)
		savePNG(im, filepath.Join(s.shots, name+".png"))
	}
}

// attach connects the observer and the driver to the X server and enables DAMAGE and XTEST.
func (s *session) attach() error {
	var err error
	if s.obs, err = dialX(s.xs.display); err != nil {
		return err
	}
	if s.drv, err = dialX(s.xs.display); err != nil {
		return err
	}
	if s.dm, err = newDamage(s.obs); err != nil {
		return err
	}
	if s.xt, err = newXtest(s.drv); err != nil {
		return err
	}
	return nil
}

func (s *session) detach() {
	if s.obs != nil {
		s.obs.close()
	}
	if s.drv != nil {
		s.drv.close()
	}
}

// findWindow waits for the browser's main window (the viewable top-level window of window size) and focuses it.
// There is no window manager, so this is also what a WM would do for focus.
func (s *session) findWindow(timeout time.Duration) (time.Time, error) {
	end := time.Now().Add(timeout)
	for time.Now().Before(end) {
		for _, w := range s.obs.topWindows() {
			if w.w >= winW-40 && w.h >= winH-40 && !w.override {
				s.win = w
				s.drv.setInputFocus(w.id)
				return time.Now(), s.drv.sync()
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	return time.Time{}, fmt.Errorf("no browser window within %s", timeout)
}

// dmgIn returns the next DAMAGE rectangle that intersects any of rs before deadline.
func (s *session) dmgIn(deadline time.Time, rs ...rect) (xevent, rect, bool) {
	for {
		ev, ok, err := s.obs.nextEvent(deadline)
		if err != nil || !ok {
			return xevent{}, rect{}, false
		}
		r, isD := s.dm.notify(ev.data)
		if !isD {
			continue
		}
		for _, q := range rs {
			if r.intersects(q) {
				return ev, r, true
			}
		}
	}
}

// waitQuiet returns once no damage touched r for quiet (or after max).
func (s *session) waitQuiet(r rect, quiet, max time.Duration) bool {
	end := time.Now().Add(max)
	last := time.Now()
	for {
		dl := last.Add(quiet)
		if dl.After(end) {
			dl = end
		}
		ev, _, ok := s.dmgIn(dl, r)
		if !ok {
			return time.Now().Before(end) || time.Since(last) >= quiet
		}
		last = ev.at
		if time.Now().After(end) {
			return false
		}
	}
}

// changeTimes is the outcome of one trigger-to-pixels measurement.
type changeTimes struct {
	Any     float64 // ms from the trigger to the first pixel change in the "any" region; -1 = none within the timeout
	Content float64 // same for the content region (-1 when not asked for / none)
}

// measure runs trigger (which must return the instant the input request was written) and reports when the pixels of
// anyROI (and optionally contentROI) first differ from what they were just before the trigger. Both are absolute
// root coordinates. Detection is event driven: DAMAGE tells us that something was drawn inside the region, then the
// pixels are read back and compared with the baseline; a damage event that leaves the pixels identical (a browser
// re-presenting an unchanged frame) is ignored. The time reported is when this process read the damage event.
func (s *session) measure(trigger func() (time.Time, error), anyROI rect, contentROI *rect, timeout time.Duration) (changeTimes, error) {
	res := changeTimes{Any: -1, Content: -1}
	s.obs.drain()
	rs := []rect{anyROI}
	if contentROI != nil {
		rs = append(rs, *contentROI)
	}
	base, err := s.obs.grabMany(rs)
	if err != nil {
		return res, err
	}
	s.obs.drain()
	t0, err := trigger()
	if err != nil {
		return res, err
	}
	deadline := t0.Add(timeout)
	for {
		ev, r, ok := s.dmgIn(deadline, rs...)
		if !ok {
			return res, nil
		}
		if res.Any < 0 && r.intersects(anyROI) {
			cur, err := s.obs.grab(anyROI)
			if err != nil {
				return res, err
			}
			if !cur.equal(base[0]) {
				res.Any = float64(ev.at.Sub(t0).Nanoseconds()) / 1e6
			}
		}
		if contentROI != nil && res.Content < 0 && r.intersects(*contentROI) {
			cur, err := s.obs.grab(*contentROI)
			if err != nil {
				return res, err
			}
			if !cur.equal(base[1]) {
				res.Content = float64(ev.at.Sub(t0).Nanoseconds()) / 1e6
				if res.Any < 0 { // content is inside "any" by construction of the callers
					res.Any = res.Content
				}
			}
		}
		if res.Any >= 0 && (contentROI == nil || res.Content >= 0) {
			return res, nil
		}
	}
}

// alignCaret waits (up to max) for the next caret blink toggle inside r so that the key press that follows lands just
// after a toggle and the next toggle is ~500 ms away, i.e. cannot be mistaken for the response to the key.
func (s *session) alignCaret(r rect, max time.Duration) {
	base, err := s.obs.grab(r)
	if err != nil {
		return
	}
	end := time.Now().Add(max)
	for {
		_, _, ok := s.dmgIn(end, r)
		if !ok {
			return
		}
		cur, err := s.obs.grab(r)
		if err == nil && !cur.equal(base) {
			time.Sleep(25 * time.Millisecond)
			return
		}
	}
}

const keyHold = 45 * time.Millisecond

// keyLatency presses ch (down only, then up after keyHold) and measures key-down write to first changed pixel in roi.
func (s *session) keyLatency(ch rune, roi rect) (float64, error) {
	var rel func()
	var t0 time.Time
	r, err := s.measure(func() (time.Time, error) {
		var err error
		t0, rel, err = s.xt.press(ch)
		return t0, err
	}, roi, nil, 2*time.Second)
	if rel != nil {
		if d := keyHold - time.Since(t0); d > 0 {
			time.Sleep(d)
		}
		rel()
	}
	return r.Any, err
}

// chordLatency is the same for Ctrl+<ch>.
func (s *session) chordLatency(ch rune, anyROI rect, content *rect) (changeTimes, error) {
	var rel func()
	var t0 time.Time
	r, err := s.measure(func() (time.Time, error) {
		var err error
		t0, rel, err = s.xt.chord(keyCtrl, ch)
		return t0, err
	}, anyROI, content, 3*time.Second)
	if rel != nil {
		if d := keyHold - time.Since(t0); d > 0 {
			time.Sleep(d)
		}
		rel()
	}
	return r, err
}

func (s *session) tabSwitchLatency(anyROI rect, content *rect) (changeTimes, error) {
	var rel func()
	var t0 time.Time
	r, err := s.measure(func() (time.Time, error) {
		m, _ := s.xt.code(keyCtrl)
		k, err := s.xt.code('\t')
		if err != nil {
			return time.Time{}, err
		}
		if _, err = s.xt.keyDown(m.code); err != nil {
			return time.Time{}, err
		}
		t0, err = s.xt.keyDown(k.code)
		rel = func() { s.xt.keyUp(k.code); s.xt.keyUp(m.code) }
		return t0, err
	}, anyROI, content, 3*time.Second)
	if rel != nil {
		if d := keyHold - time.Since(t0); d > 0 {
			time.Sleep(d)
		}
		rel()
	}
	return r, err
}

// chrome-level helpers (unmeasured)

func (s *session) focusOmnibox() error { return s.xt.chordTap(keyCtrl, 'l') }

// typeURLAndGo types url into the omnibox and returns the instant of the Enter key-down.
func (s *session) typeURL(url string) error {
	if err := s.focusOmnibox(); err != nil {
		return err
	}
	time.Sleep(150 * time.Millisecond)
	if err := s.xt.typeString(url, 4*time.Millisecond); err != nil {
		return err
	}
	// remove any inline autocompletion that the omnibox appended after the caret
	if err := s.xt.tap(keyDelete); err != nil {
		return err
	}
	time.Sleep(120 * time.Millisecond)
	return nil
}

func (s *session) enter() (time.Time, func(), error) { return s.xt.press('\n') }

// keyText is the fixed text typed in the "dropdown open" scenario: exactly 30 letters. No spaces: a trailing space
// changes no pixel in some omnibox implementations (nothing to detect), which would turn a key into a "missed" sample.
const keyText = "thequickbrownfoxjumpsoverlazyd"

func spacing(rng *rand.Rand) time.Duration { return time.Duration(80+rng.Intn(150)) * time.Millisecond }
