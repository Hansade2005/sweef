// Package evqueue is the unbounded, coalescing event queue every native shell
// uses between its OS event loop and the consumer. The OS loop must never
// block on a slow consumer (it also services redraw and clipboard requests),
// so Push never blocks; bursts of mouse-move, wheel and resize events are
// merged while they wait in the queue.
package evqueue

import (
	"sync"

	"github.com/sweefbrowser/sweef/internal/shell"
)

// maxPending bounds memory if the consumer stops reading. When exceeded,
// the oldest non-critical events (moves/wheels) are dropped first.
const maxPending = 1 << 14

type Queue struct {
	mu     sync.Mutex
	cond   *sync.Cond
	items  []shell.Event
	closed bool
	out    chan shell.Event
	done   chan struct{}
}

// New starts the queue's delivery goroutine.
func New() *Queue {
	q := &Queue{out: make(chan shell.Event, 8), done: make(chan struct{})}
	q.cond = sync.NewCond(&q.mu)
	go q.pump()
	return q
}

// Events is the consumer side. It is closed after Close.
func (q *Queue) Events() <-chan shell.Event { return q.out }

// Push appends ev, merging it into the last pending event when they are the
// same coalescible kind. Safe for concurrent use; never blocks.
func (q *Queue) Push(ev shell.Event) {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	if n := len(q.items); n > 0 {
		last := &q.items[n-1]
		if last.Kind == ev.Kind && coalesce(last, &ev) {
			q.mu.Unlock()
			return
		}
	}
	if len(q.items) >= maxPending {
		q.dropOne()
	}
	q.items = append(q.items, ev)
	q.cond.Signal()
	q.mu.Unlock()
}

func coalesce(last, ev *shell.Event) bool {
	switch ev.Kind {
	case shell.EvMouseMove:
		*last = *ev
		return true
	case shell.EvResize, shell.EvScale:
		*last = *ev
		return true
	case shell.EvWheel:
		if last.Mods != ev.Mods || last.Precise != ev.Precise {
			return false
		}
		dx, dy := last.DX+ev.DX, last.DY+ev.DY
		*last = *ev
		last.DX, last.DY = dx, dy
		return true
	}
	return false
}

func (q *Queue) dropOne() {
	for i, e := range q.items {
		if e.Kind == shell.EvMouseMove || e.Kind == shell.EvWheel {
			q.items = append(q.items[:i], q.items[i+1:]...)
			return
		}
	}
	q.items = q.items[1:]
}

// Close stops delivery and closes Events. Pending events are discarded.
func (q *Queue) Close() {
	q.mu.Lock()
	if !q.closed {
		q.closed = true
		close(q.done)
		q.cond.Broadcast()
	}
	q.mu.Unlock()
}

func (q *Queue) pump() {
	defer close(q.out)
	for {
		q.mu.Lock()
		for len(q.items) == 0 && !q.closed {
			q.cond.Wait()
		}
		if q.closed {
			q.mu.Unlock()
			return
		}
		ev := q.items[0]
		q.items[0] = shell.Event{}
		q.items = q.items[1:]
		if len(q.items) == 0 {
			q.items = nil
		}
		q.mu.Unlock()
		select {
		case q.out <- ev:
		case <-q.done:
			return
		}
	}
}
