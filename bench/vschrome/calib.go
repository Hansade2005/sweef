//go:build linux

package main

import (
	"fmt"
	"os"
	"sort"
	"time"
)

// calibrate measures the floor of both detectors with an X client whose pixels change at a known instant
// (flasher). The flasher writes ChangeWindowAttributes+ClearArea in one write; the server paints during that request,
// so (detection instant - write instant) is the detector's own overhead: socket + server scheduling + our wake-up
// (damage), or half a poll period + a GetImage round trip (poll). Browsers pay this on top of their real latency;
// the report prints it next to the results and does not subtract it.
type calibResult struct {
	Damage []float64 `json:"damage_ms"`
	Poll   []float64 `json:"poll_ms"`
	PollOK bool      `json:"-"`
}

func calibrate(display string, roi rect, n int) (*calibResult, error) {
	obs, err := dialX(display)
	if err != nil {
		return nil, err
	}
	defer obs.close()
	drv, err := dialX(display)
	if err != nil {
		return nil, err
	}
	defer drv.close()
	fl, err := newFlasher(drv, rect{roi.x + 20, roi.y + 10, 200, 20})
	if err != nil {
		return nil, err
	}
	defer fl.destroy()
	dm, err := newDamage(obs)
	if err != nil {
		return nil, err
	}
	res := &calibResult{}
	obs.drain()
	// damage detector
	for i := 0; i < n; i++ {
		time.Sleep(time.Duration(20+i%7*3) * time.Millisecond)
		obs.drain()
		base, _ := obs.grab(roi)
		t0 := fl.flip()
		dl := time.Now().Add(time.Second)
		for {
			ev, ok, err := obs.nextEvent(dl)
			if err != nil || !ok {
				return nil, fmt.Errorf("calibration: no damage event")
			}
			r, isD := dm.notify(ev.data)
			if !isD || !r.intersects(roi) {
				continue
			}
			cur, _ := obs.grab(roi)
			if cur.equal(base) {
				continue
			}
			res.Damage = append(res.Damage, float64(ev.at.Sub(t0).Microseconds())/1000)
			break
		}
	}
	// poll detector: tight GetImage loop on a small strip around the flasher
	strip := rect{roi.x + 20, roi.y + 10, 200, 20}
	for i := 0; i < n; i++ {
		time.Sleep(time.Duration(20+i%7*3) * time.Millisecond)
		base, _ := obs.grab(strip)
		t0 := fl.flip()
		dl := time.Now().Add(time.Second)
		for time.Now().Before(dl) {
			cur, err := obs.grab(strip)
			if err != nil {
				return nil, err
			}
			if !cur.equal(base) {
				res.Poll = append(res.Poll, float64(time.Since(t0).Microseconds())/1000)
				break
			}
		}
	}
	if len(res.Damage) != n || len(res.Poll) != n {
		return nil, fmt.Errorf("calibration incomplete")
	}
	return res, nil
}

func pct(v []float64, p float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	i := int(p/100*float64(len(s)) + 0.5)
	if i >= len(s) {
		i = len(s) - 1
	}
	if i < 0 {
		i = 0
	}
	return s[i]
}

func cmdCalib() {
	os.MkdirAll("/tmp/claude-0/shot", 0o755)
	xs, err := startXvfb("/tmp/claude-0/shot")
	if err != nil {
		panic(err)
	}
	defer xs.stop()
	r, err := calibrate(xs.display, rect{100, 40, 800, 200}, 100)
	if err != nil {
		panic(err)
	}
	fmt.Printf("damage: p50 %.3f p90 %.3f p99 %.3f max %.3f\n", pct(r.Damage, 50), pct(r.Damage, 90), pct(r.Damage, 99), pct(r.Damage, 100))
	fmt.Printf("poll:   p50 %.3f p90 %.3f p99 %.3f max %.3f\n", pct(r.Poll, 50), pct(r.Poll, 90), pct(r.Poll, 99), pct(r.Poll, 100))
}
