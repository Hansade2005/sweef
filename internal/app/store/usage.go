package store

import (
	"runtime"
	"sync/atomic"
	"time"
)

// Usage reports real runtime metrics. Nothing here is estimated or invented:
// memory comes from the OS (Linux /proc/self/status VmRSS; elsewhere the Go
// runtime's Sys, reported with RSSExact=false), tab counts come from the model.
type Usage struct {
	started time.Time
	blocked atomic.Int64
	pages   atomic.Int64
}

// UsageSnapshot is the value shown on the new-tab page / settings.
type UsageSnapshot struct {
	RSSBytes   uint64 `json:"rssBytes"`
	RSSExact   bool   `json:"rssExact"`
	HeapBytes  uint64 `json:"heapBytes"`
	Goroutines int    `json:"goroutines"`
	UptimeSec  int64  `json:"uptimeSec"`
	Tabs       int    `json:"tabs"`
	Blocked    int64  `json:"blocked"`
	Pages      int64  `json:"pages"`
}

func NewUsage() *Usage { return &Usage{started: time.Now()} }

// AddBlocked counts blocked requests.
func (u *Usage) AddBlocked(n int) { u.blocked.Add(int64(n)) }

// AddPage counts a loaded page.
func (u *Usage) AddPage() { u.pages.Add(1) }

// Snapshot samples the process now; tabs is passed in by the caller.
func (u *Usage) Snapshot(tabs int) UsageSnapshot {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	rss, exact := ReadRSS()
	if !exact {
		rss = ms.Sys
	}
	return UsageSnapshot{RSSBytes: rss, RSSExact: exact, HeapBytes: ms.HeapAlloc, Goroutines: runtime.NumGoroutine(),
		UptimeSec: int64(time.Since(u.started).Seconds()), Tabs: tabs, Blocked: u.blocked.Load(), Pages: u.pages.Load()}
}
