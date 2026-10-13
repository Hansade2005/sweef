package capture

import (
	"errors"
	"sync"
	"time"
)

// Moder is implemented by sources that know the mode they negotiated with the device. Without it the first frame
// decides the size and the requested rate is reported.
type Moder interface {
	Mode() (w, h int, fps float64)
}

// ErrTimeout is returned when a device was opened but produced no data in time.
var ErrTimeout = errors.New("capture: timeout starting source")

// A Sub is one consumer of a shared source. Items are delivered in order; a consumer that falls behind loses its
// oldest items (a live source never waits for a reader). C is closed when the source ends or the sub is cancelled.
type Sub[T any] struct {
	C      chan *T
	mu     sync.Mutex
	closed bool
	owner  interface{ drop(any) }
}

func (s *Sub[T]) push(v *T) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	for {
		select {
		case s.C <- v:
			s.mu.Unlock()
			return
		default:
		}
		select { // full: drop the oldest
		case <-s.C:
		default:
		}
	}
}

func (s *Sub[T]) close() {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		close(s.C)
	}
	s.mu.Unlock()
}

// Cancel detaches the subscription (idempotent).
func (s *Sub[T]) Cancel() {
	if s.owner != nil {
		s.owner.drop(s)
	}
	s.close()
}

type srcAPI[T, C any] interface {
	Start(C) (<-chan *T, error)
	Stop() error
	Err() error
}

// shared runs one device on behalf of several consumers: getUserMedia called twice, track.clone(), a
// MediaRecorder next to a peer connection. Cameras are exclusive (V4L2 refuses a second streamer), so
// everybody has to share the one stream anyway.
type shared[T, C any] struct {
	open func() (srcAPI[T, C], error)
	free func() // removes the share from its registry

	mu       sync.Mutex
	src      srcAPI[T, C]
	cons     C
	refs     int
	subs     map[*Sub[T]]struct{}
	err      error
	dead     bool
	gen      int // incremented by every (re)start; a pump of an older generation is superseded
	first    *T
	ready    chan struct{} // closed when the first item arrived or start failed
	startErr error
	ended    chan struct{} // closed when the source stopped for good
	mode     func(src srcAPI[T, C], first *T, cons C) any
	modeVal  any
}

func newShared[T, C any](open func() (srcAPI[T, C], error), free func()) *shared[T, C] {
	return &shared[T, C]{open: open, free: free, subs: map[*Sub[T]]struct{}{}, ready: make(chan struct{}), ended: make(chan struct{})}
}

// run starts the device and the pump. Called once by the creator, before the share is visible to waiters' Subscribe.
func (s *shared[T, C]) begin(c C) {
	src, err := s.open()
	if err == nil {
		s.mu.Lock()
		s.src, s.cons = src, c
		s.mu.Unlock()
		var ch <-chan *T
		if ch, err = src.Start(c); err == nil {
			s.mu.Lock()
			s.gen++
			gen := s.gen
			s.mu.Unlock()
			go s.pump(src, ch, gen)
			return
		}
	}
	s.mu.Lock()
	s.startErr, s.dead = err, true
	s.mu.Unlock()
	close(s.ready)
	close(s.ended)
	s.free()
}

func (s *shared[T, C]) pump(src srcAPI[T, C], ch <-chan *T, gen int) {
	firstSeen := false
	for v := range ch {
		s.mu.Lock()
		if s.gen != gen {
			s.mu.Unlock()
			continue // superseded by a reopen: drain and drop
		}
		if !firstSeen {
			firstSeen = true
			if s.first == nil {
				s.first = v
				if s.mode != nil {
					s.modeVal = s.mode(src, v, s.cons)
				}
				close(s.ready)
			}
		}
		subs := make([]*Sub[T], 0, len(s.subs))
		for sub := range s.subs {
			subs = append(subs, sub)
		}
		s.mu.Unlock()
		for _, sub := range subs {
			sub.push(v)
		}
	}
	s.mu.Lock()
	if s.gen != gen { // a deliberate restart (applyConstraints): consumers keep their subscriptions
		s.mu.Unlock()
		return
	}
	s.dead = true
	s.err = src.Err()
	if !firstSeen && s.first == nil {
		s.startErr = s.err
		if s.startErr == nil {
			s.startErr = ErrTimeout
		}
		close(s.ready)
	}
	subs := s.subs
	s.subs = map[*Sub[T]]struct{}{}
	s.mu.Unlock()
	for sub := range subs {
		sub.close()
	}
	close(s.ended)
	s.free()
}

func (s *shared[T, C]) subscribe(buf int) *Sub[T] {
	sub := &Sub[T]{C: make(chan *T, buf), owner: s}
	s.mu.Lock()
	if s.dead {
		s.mu.Unlock()
		sub.close()
		return sub
	}
	s.subs[sub] = struct{}{}
	s.mu.Unlock()
	return sub
}

func (s *shared[T, C]) drop(v any) {
	if sub, ok := v.(*Sub[T]); ok {
		s.mu.Lock()
		delete(s.subs, sub)
		s.mu.Unlock()
	}
}

// release drops one reference; the last one stops the device.
func (s *shared[T, C]) release() {
	s.mu.Lock()
	s.refs--
	last := s.refs <= 0 && !s.dead
	src := s.src
	if last {
		s.dead = true
	}
	s.mu.Unlock()
	if last {
		s.free() // before Stop so a new Acquire opens a fresh device instead of finding this one
		if src != nil {
			_ = src.Stop()
		}
	}
}

// reopen restarts the device with new constraints while keeping every subscription.
func (s *shared[T, C]) reopen(c C) error {
	s.mu.Lock()
	if s.dead {
		s.mu.Unlock()
		return ErrNotFound
	}
	old := s.src
	s.gen++ // supersede the running pump before stopping its source
	s.mu.Unlock()
	_ = old.Stop() // the old pump sees its channel close and returns without ending the share
	src, err := s.open()
	if err == nil {
		var ch <-chan *T
		if ch, err = src.Start(c); err == nil {
			s.mu.Lock()
			s.src, s.cons = src, c
			s.first = nil
			s.ready = make(chan struct{})
			s.gen++
			gen := s.gen
			s.mu.Unlock()
			go s.pump(src, ch, gen)
			return nil
		}
	}
	// could not come back up: end the share so consumers see the failure
	s.mu.Lock()
	s.dead, s.err = true, err
	subs := s.subs
	s.subs = map[*Sub[T]]struct{}{}
	s.mu.Unlock()
	for sub := range subs {
		sub.close()
	}
	close(s.ended)
	s.free()
	return err
}

func (s *shared[T, C]) wait(timeout time.Duration) error {
	s.mu.Lock()
	ready := s.ready
	s.mu.Unlock()
	select {
	case <-ready:
	case <-time.After(timeout):
		return ErrTimeout
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.first == nil {
		if s.startErr != nil {
			return s.startErr
		}
		return ErrTimeout
	}
	return nil
}

func (s *shared[T, C]) isDead() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dead
}
