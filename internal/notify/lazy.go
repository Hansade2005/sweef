package notify

import (
	"sync"
	"time"
)

// Lazy opens a native sink the first time a notification is shown, so that a browser that never shows one never
// connects to the desktop's notification service (and tests on a developer's desktop do not talk to it). When
// opening fails, the failure is remembered for retryAfter before the next attempt.
type Lazy struct {
	h          handlerBox
	open       func() (Sink, error)
	retryAfter time.Duration
	now        func() time.Time

	mu       sync.Mutex
	sink     Sink
	failedAt time.Time
	failed   bool
}

// NewLazy wraps an opener such as func() (Sink, error) { return Native("Sweef") }.
func NewLazy(open func() (Sink, error)) *Lazy {
	return &Lazy{open: open, retryAfter: 30 * time.Second, now: time.Now}
}

func (l *Lazy) Name() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.sink != nil {
		return l.sink.Name()
	}
	return "lazy"
}

func (l *Lazy) SetHandler(f func(Event)) { l.h.set(f) }

func (l *Lazy) get() (Sink, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.sink != nil {
		return l.sink, nil
	}
	if l.failed && l.now().Sub(l.failedAt) < l.retryAfter {
		return nil, ErrUnavailable
	}
	s, err := l.open()
	if err != nil {
		l.failed, l.failedAt = true, l.now()
		return nil, err
	}
	l.failed = false
	s.SetHandler(l.h.emit)
	l.sink = s
	return s, nil
}

func (l *Lazy) Show(n Notification) error {
	s, err := l.get()
	if err != nil {
		return err
	}
	if err := s.Show(n); err != nil {
		// the service went away (a restarted desktop session): forget it and try again next time
		l.mu.Lock()
		if l.sink == s {
			l.sink = nil
		}
		l.mu.Unlock()
		_ = s.Shutdown()
		return err
	}
	return nil
}

func (l *Lazy) Close(id string) {
	l.mu.Lock()
	s := l.sink
	l.mu.Unlock()
	if s != nil {
		s.Close(id)
	}
}

func (l *Lazy) Shutdown() error {
	l.mu.Lock()
	s := l.sink
	l.sink = nil
	l.mu.Unlock()
	if s != nil {
		return s.Shutdown()
	}
	return nil
}
