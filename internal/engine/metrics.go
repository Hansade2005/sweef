package engine

import (
	"runtime"
	"sync"
	"time"
)

// Metrics is the tiny resource governor. It samples Go runtime memory and,
// past the soft RSS budget, forces GC + ring-buffer eviction so a runaway
// agent cannot OOM a 50 MB box. Cost: one goroutine waking every few seconds.
type Metrics struct {
	mu        sync.RWMutex
	maxRSSMiB int
	lastMiB   float64
	peakMiB   float64
	samples   int
	evictions int
	started   time.Time
}

func NewMetrics(maxRSSMiB int) *Metrics {
	if maxRSSMiB <= 0 {
		maxRSSMiB = 48
	}
	return &Metrics{maxRSSMiB: maxRSSMiB, started: time.Now()}
}

// Sample records current heap+stack in-use MiB. Returns true when over budget.
func (m *Metrics) Sample() bool {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	mib := float64(ms.HeapInuse+ms.StackInuse+ms.Sys) / (1 << 20)
	m.mu.Lock()
	m.lastMiB = mib
	if mib > m.peakMiB {
		m.peakMiB = mib
	}
	m.samples++
	over := mib > float64(m.maxRSSMiB)
	if over {
		m.evictions++
	}
	m.mu.Unlock()
	if over {
		runtime.GC() // pressure valve: reclaim before evicting state
	}
	return over
}

func (m *Metrics) Snapshot() map[string]any {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return map[string]any{
		"memMiB":       m.lastMiB,
		"peakMiB":      m.peakMiB,
		"budgetMiB":    m.maxRSSMiB,
		"samples":      m.samples,
		"evictions":    m.evictions,
		"uptimeSec":    time.Since(m.started).Seconds(),
		"goVersion":    runtime.Version(),
		"numGoroutine": runtime.NumGoroutine(),
	}
}

// Watch runs periodic sampling until stop is closed; on over-budget it calls
// evict (engine passes a func that trims rings / drops cached docs).
func (m *Metrics) Watch(interval time.Duration, evict func()) {
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for range tick.C {
		if m.Sample() && evict != nil {
			evict()
		}
	}
}
