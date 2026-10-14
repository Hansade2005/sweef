package cdm

import "sync"

// EventQueue is an unbounded FIFO that feeds a channel, so a CDM callback (which runs on a CDM or reader
// goroutine and must not block) can always enqueue and a slow consumer only delays itself. Backends in other
// packages use it for Session.Events.
type EventQueue struct {
	mu     sync.Mutex
	items  []Event
	closed bool
	wake   chan struct{}
	abort  chan struct{}
	out    chan Event
	once   sync.Once
}

// NewEventQueue starts a queue; its pump goroutine ends once the queue is closed and drained, or aborted.
func NewEventQueue() *EventQueue {
	q := &EventQueue{wake: make(chan struct{}, 1), abort: make(chan struct{}), out: make(chan Event)}
	go q.pump()
	return q
}

// C is the consumer side; it is closed after Close once everything queued was delivered, or at once by Abort.
func (q *EventQueue) C() <-chan Event { return q.out }

// Push appends an event (dropped after Close).
func (q *EventQueue) Push(e Event) {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	q.items = append(q.items, e)
	q.mu.Unlock()
	q.signal()
}

// Close stops accepting events; queued events are still delivered, then C is closed.
func (q *EventQueue) Close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.signal()
}

// Abort drops whatever is queued and closes C.
func (q *EventQueue) Abort() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.once.Do(func() { close(q.abort) })
	q.signal()
}

func (q *EventQueue) signal() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *EventQueue) pump() {
	defer close(q.out)
	for {
		q.mu.Lock()
		var e Event
		have := len(q.items) > 0
		if have {
			e = q.items[0]
			q.items[0] = Event{}
			q.items = q.items[1:]
		}
		closed := q.closed
		q.mu.Unlock()
		if have {
			select {
			case q.out <- e:
			case <-q.abort:
				return
			}
			continue
		}
		if closed {
			return
		}
		select {
		case <-q.wake:
		case <-q.abort:
			return
		}
	}
}
