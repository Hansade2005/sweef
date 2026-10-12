package store

import (
	"encoding/json"
	"sync"
	"time"
)

const (
	sessionFile = "session.json"
	sessionPrev = "session.prev.json"
)

// SessionTab is one restorable tab.
type SessionTab struct {
	URL     string   `json:"url"`
	Title   string   `json:"title,omitempty"`
	Scroll  int      `json:"scroll,omitempty"`
	Pinned  bool     `json:"pinned,omitempty"`
	History []string `json:"history,omitempty"`
	HIdx    int      `json:"hidx,omitempty"`
}

// SessionWindow is one restorable window.
type SessionWindow struct {
	Tabs   []SessionTab `json:"tabs"`
	Active int          `json:"active"`
}

// Session is the whole restorable state. Incognito windows are never in it.
type Session struct {
	V         int             `json:"v"`
	Windows   []SessionWindow `json:"windows"`
	SavedAt   time.Time       `json:"savedAt"`
	CleanExit bool            `json:"cleanExit"`
}

// SessionStore writes the session debounced and crash-safe: the new file is
// written to a temp file, fsynced and renamed over the old one, and the
// previous good generation is kept as session.prev.json so even a torn
// final file still restores.
type SessionStore struct {
	b        Backend
	mu       sync.Mutex
	timer    *time.Timer
	pending  func() Session
	Debounce time.Duration
	writes   int
	closed   bool
}

func OpenSession(b Backend) *SessionStore {
	return &SessionStore{b: b, Debounce: 750 * time.Millisecond}
}

// Load returns the saved session; falls back to the previous generation when
// the primary is torn. ok is false when there is nothing to restore.
func (s *SessionStore) Load() (Session, bool) {
	for _, name := range []string{sessionFile, sessionPrev} {
		data, err := s.b.ReadFile(name)
		if err != nil {
			continue
		}
		var ss Session
		if json.Unmarshal(data, &ss) == nil && ss.V > 0 {
			return ss, true
		}
	}
	return Session{}, false
}

// Save writes immediately.
func (s *SessionStore) Save(ss Session) error {
	ss.V = 1
	ss.SavedAt = time.Now()
	data, err := json.Marshal(ss)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeLocked(data)
}

func (s *SessionStore) writeLocked(data []byte) error {
	// keep the previous generation, then replace the primary atomically
	if old, err := s.b.ReadFile(sessionFile); err == nil {
		var probe Session
		if json.Unmarshal(old, &probe) == nil && probe.V > 0 {
			_ = s.b.WriteAtomic(sessionPrev, old)
		}
	}
	s.writes++
	return s.b.WriteAtomic(sessionFile, data)
}

// Schedule requests a save; calls within Debounce coalesce and snap is
// evaluated when the timer fires (so it captures the latest state).
func (s *SessionStore) Schedule(snap func() Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.pending = snap
	if s.timer != nil {
		return // already armed; it will pick up the newest snap
	}
	s.timer = time.AfterFunc(s.Debounce, s.fire)
}

func (s *SessionStore) fire() {
	s.mu.Lock()
	snap := s.pending
	s.pending, s.timer = nil, nil
	s.mu.Unlock()
	if snap == nil {
		return
	}
	ss := snap()
	ss.V = 1
	ss.SavedAt = time.Now()
	if data, err := json.Marshal(ss); err == nil {
		s.mu.Lock()
		_ = s.writeLocked(data)
		s.mu.Unlock()
	}
}

// Flush runs a pending save now and stops the timer.
func (s *SessionStore) Flush() {
	s.mu.Lock()
	t := s.timer
	has := s.pending != nil
	if t != nil {
		t.Stop()
	}
	s.timer = nil
	s.mu.Unlock()
	if has {
		s.fire()
	}
}

// Stop flushes and refuses further scheduling.
func (s *SessionStore) Stop() {
	s.Flush()
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
}

// Writes reports how many times the file was written (tests).
func (s *SessionStore) Writes() int { s.mu.Lock(); defer s.mu.Unlock(); return s.writes }
