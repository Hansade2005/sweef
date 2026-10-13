//go:build linux

package main

import (
	"fmt"
	"math/rand"
	"os"
	"time"
)

// crossCheck validates the DAMAGE-based detector against the method the brief names as the fallback: a tight GetImage
// poll. In one session per browser it types the 30 letters twice into the open dropdown, alternating the detector per
// key (even keys: damage, odd keys: poll) on the same region (the omnibox text strip), so that both see the same
// browser state. If damage detection were biased the two distributions would differ.
func (h *harness) crossCheck(spec *browserSpec) (*CrossOut, error) {
	prof, err := freshProfile(spec, h.cfg.OutDir)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(prof)
	xs, err := startXvfb(h.cfg.LogDir)
	if err != nil {
		return nil, err
	}
	defer xs.stop()
	rec, err := NewRecorder(os.Getenv("HTTPS_PROXY"))
	if err != nil {
		return nil, err
	}
	defer rec.Close()
	s := &session{xs: xs, spec: spec, lay: layouts[spec.Name], rec: rec, fix: h.fix}
	if err := s.attach(); err != nil {
		return nil, err
	}
	defer s.detach()
	tag := h.tag()
	run := tag + "-x"
	l, err := spec.launch(launchOpts{Display: xs.display, Profile: prof, Proxy: rec.Addr(), URL: h.pageURL("/article", run), LogDir: h.cfg.LogDir, Tag: tag})
	if err != nil {
		return nil, err
	}
	defer l.stop()
	if _, err := s.findWindow(30 * time.Second); err != nil {
		return nil, err
	}
	if _, ok := h.fix.waitBeacon(run, "load", 60*time.Second); !ok {
		return nil, fmt.Errorf("article did not load")
	}
	time.Sleep(3 * time.Second)
	roi := s.abs(s.lay.TextStrip)
	rng := rand.New(rand.NewSource(7))
	out := &CrossOut{}
	if err := s.focusOmnibox(); err != nil {
		return nil, err
	}
	s.waitQuiet(roi, 300*time.Millisecond, 2*time.Second)
	s.keyLatency('a', roi)
	for round := 0; round < 3; round++ {
		for i, ch := range keyText {
			s.waitQuiet(roi, 60*time.Millisecond, time.Second)
			if i%2 == 0 {
				v, err := s.keyLatency(ch, roi)
				if err != nil {
					return nil, err
				}
				if v >= 0 {
					out.Damage = append(out.Damage, v)
				}
			} else {
				v, err := s.keyLatencyPoll(ch, roi)
				if err != nil {
					return nil, err
				}
				if v >= 0 {
					out.Poll = append(out.Poll, v)
				}
			}
			time.Sleep(spacing(rng))
		}
		s.xt.tap(keyEsc)
		time.Sleep(150 * time.Millisecond)
		s.xt.tap(keyEsc)
		s.waitQuiet(roi, 300*time.Millisecond, time.Second)
		s.focusOmnibox()
		s.waitQuiet(roi, 300*time.Millisecond, time.Second)
		s.keyLatency('a', roi)
	}
	return out, nil
}

// keyLatencyPoll is keyLatency with a tight GetImage loop instead of DAMAGE events.
func (s *session) keyLatencyPoll(ch rune, roi rect) (float64, error) {
	s.obs.drain()
	base, err := s.obs.grab(roi)
	if err != nil {
		return -1, err
	}
	t0, rel, err := s.xt.press(ch)
	if err != nil {
		return -1, err
	}
	res := -1.0
	end := t0.Add(2 * time.Second)
	for time.Now().Before(end) {
		cur, err := s.obs.grab(roi)
		if err != nil {
			return -1, err
		}
		if !cur.equal(base) {
			res = ms(time.Since(t0))
			break
		}
	}
	if d := keyHold - time.Since(t0); d > 0 {
		time.Sleep(d)
	}
	rel()
	return res, nil
}
