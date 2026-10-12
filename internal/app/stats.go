package app

import (
	"fmt"
	"image"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sweefbrowser/sweef/internal/app/compositor"
	"github.com/sweefbrowser/sweef/internal/app/store"
)

// runStats collects the numbers `sweef gui --stats` prints. Input latency is
// the time from an input event reaching the UI loop to the next Present.
type runStats struct {
	mu                                                           sync.Mutex
	inputAt                                                      time.Time // oldest input not yet presented
	latencies                                                    []time.Duration
	suggest                                                      []time.Duration
	presents                                                     int
	damagePx                                                     int64
	statePushes, uiMessages, chromePaints, pagePaints, animStops atomic.Int64
}

func (s *runStats) markInput() {
	s.mu.Lock()
	if s.inputAt.IsZero() {
		s.inputAt = time.Now()
	}
	s.mu.Unlock()
}

func (s *runStats) notePresent(d image.Rectangle) {
	s.mu.Lock()
	s.presents++
	s.damagePx += int64(d.Dx()) * int64(d.Dy())
	if !s.inputAt.IsZero() {
		s.latencies = appendCap(s.latencies, time.Since(s.inputAt), 4096)
		s.inputAt = time.Time{}
	}
	s.mu.Unlock()
}

func (s *runStats) noteSuggest(d time.Duration) {
	s.mu.Lock()
	s.suggest = appendCap(s.suggest, d, 1024)
	s.mu.Unlock()
}

func (s *runStats) navStart(string, int64) {}

func appendCap(s []time.Duration, d time.Duration, n int) []time.Duration {
	if len(s) >= n {
		s = s[1:]
	}
	return append(s, d)
}

// Stats is a snapshot of timing and resource numbers.
type Stats struct {
	Compositor   compositor.Stats
	InputLatency compositor.Percentiles
	Suggest      compositor.Percentiles
	Presents     int
	DamageAvgPx  int64
	StatePushes  int
	Usage        store.UsageSnapshot
	FirstFrame   time.Duration
	BlockedTotal int64
}

func pct(ds []time.Duration) compositor.Percentiles {
	if len(ds) == 0 {
		return compositor.Percentiles{}
	}
	c := append([]time.Duration(nil), ds...)
	sort.Slice(c, func(i, j int) bool { return c[i] < c[j] })
	return compositor.Percentiles{N: len(c), P50: c[(len(c)-1)/2], P99: c[int(0.99*float64(len(c)-1))], Max: c[len(c)-1]}
}

// Stats returns the current numbers (safe from any goroutine).
func (a *App) Stats() Stats {
	s := &a.stats
	s.mu.Lock()
	st := Stats{InputLatency: pct(s.latencies), Suggest: pct(s.suggest), Presents: s.presents, StatePushes: int(s.statePushes.Load())}
	if s.presents > 0 {
		st.DamageAvgPx = s.damagePx / int64(s.presents)
	}
	s.mu.Unlock()
	st.Compositor = a.comp.Stats()
	st.Usage = a.prof.Stats.Snapshot(a.model.TabCount())
	st.FirstFrame = a.FirstFrameAfter()
	st.BlockedTotal = a.priv.Total()
	return st
}

// Format renders stats for the terminal.
func (s Stats) Format() string {
	var b strings.Builder
	ms := func(d time.Duration) string { return fmt.Sprintf("%.2fms", float64(d.Microseconds())/1000) }
	fmt.Fprintf(&b, "first frame      %s\n", ms(s.FirstFrame))
	fmt.Fprintf(&b, "frames presented %d (avg damage %d px)\n", s.Presents, s.DamageAvgPx)
	fmt.Fprintf(&b, "compose          p50 %s  p99 %s  max %s  (n=%d)\n", ms(s.Compositor.Compose.P50), ms(s.Compositor.Compose.P99), ms(s.Compositor.Compose.Max), s.Compositor.Compose.N)
	kinds := make([]string, 0, len(s.Compositor.Paint))
	for k := range s.Compositor.Paint {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	for _, k := range kinds {
		p := s.Compositor.Paint[k]
		fmt.Fprintf(&b, "paint %-9s  p50 %s  p99 %s  max %s  (n=%d)\n", k, ms(p.P50), ms(p.P99), ms(p.Max), p.N)
	}
	fmt.Fprintf(&b, "input->present   p50 %s  p99 %s  (n=%d)\n", ms(s.InputLatency.P50), ms(s.InputLatency.P99), s.InputLatency.N)
	fmt.Fprintf(&b, "omnibox suggest  p50 %s  p99 %s  (n=%d)\n", ms(s.Suggest.P50), ms(s.Suggest.P99), s.Suggest.N)
	exact := "approx"
	if s.Usage.RSSExact {
		exact = "RSS"
	}
	fmt.Fprintf(&b, "memory           %.1f MiB (%s), heap %.1f MiB, %d goroutines, %d tabs\n",
		float64(s.Usage.RSSBytes)/(1<<20), exact, float64(s.Usage.HeapBytes)/(1<<20), s.Usage.Goroutines, s.Usage.Tabs)
	fmt.Fprintf(&b, "trackers blocked %d\n", s.BlockedTotal)
	return b.String()
}
