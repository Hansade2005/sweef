package app

import (
	"sync"
	"sync/atomic"
)

// worker runs operations for one engine tab strictly in order on its own
// goroutine. The engine's navigation and JS calls are synchronous and can
// take seconds; the worker keeps them off the UI loop so input never waits
// on the network, and it serialises every access to that tab (so painting
// never races with a script).
type worker struct {
	ops     chan func()
	quit    chan struct{}
	done    chan struct{}
	pending *atomic.Int64
	once    sync.Once
}

func newWorker(pending *atomic.Int64) *worker {
	w := &worker{ops: make(chan func(), 512), quit: make(chan struct{}), done: make(chan struct{}), pending: pending}
	go w.run()
	return w
}

func (w *worker) run() {
	defer close(w.done)
	for {
		select {
		case f := <-w.ops:
			func() {
				defer w.pending.Add(-1)
				defer func() { // a bad page must not kill the browser
					if r := recover(); r != nil && PanicHook != nil {
						PanicHook(r)
					}
				}()
				f()
			}()
		case <-w.quit:
			// drain so Idle accounting stays correct
			for {
				select {
				case <-w.ops:
					w.pending.Add(-1)
				default:
					return
				}
			}
		}
	}
}

// do queues f; it returns false if the worker is stopped.
func (w *worker) do(f func()) bool {
	w.pending.Add(1)
	select {
	case <-w.quit:
		w.pending.Add(-1)
		return false
	default:
	}
	select {
	case w.ops <- f:
		return true
	case <-w.quit:
		w.pending.Add(-1)
		return false
	}
}

// stop ends the goroutine and waits for the operation in flight.
func (w *worker) stop() {
	w.once.Do(func() { close(w.quit) })
	<-w.done
}

// PanicHook, when set, is told about panics recovered on tab workers (tests
// fail on them; the CLI logs them).
var PanicHook func(any)
