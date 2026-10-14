package pushd

import (
	"sync"
	"time"
)

// limiter is a token bucket per key. Idle buckets are forgotten, so the map stays bounded by the number of
// keys seen within one refill period plus a sweep interval.
type limiter struct {
	rate, burst float64
	now         func() time.Time
	mu          sync.Mutex
	b           map[string]*bucket
	lastSweep   time.Time
}

type bucket struct {
	tokens float64
	at     time.Time
}

func newLimiter(rate, burst float64, now func() time.Time) *limiter {
	if burst < 1 {
		burst = 1
	}
	return &limiter{rate: rate, burst: burst, now: now, b: map[string]*bucket{}}
}

func (l *limiter) allow(key string) bool {
	t := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if t.Sub(l.lastSweep) > time.Minute {
		for k, b := range l.b {
			if t.Sub(b.at) > time.Duration(l.burst/l.rate*float64(time.Second))+time.Second {
				delete(l.b, k)
			}
		}
		l.lastSweep = t
	}
	b := l.b[key]
	if b == nil {
		if len(l.b) >= 100000 { // refuse to grow without bound under a flood of distinct keys
			return false
		}
		b = &bucket{tokens: l.burst, at: t}
		l.b[key] = b
	}
	if d := t.Sub(b.at).Seconds(); d > 0 {
		b.tokens += d * l.rate
		if b.tokens > l.burst {
			b.tokens = l.burst
		}
		b.at = t
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
