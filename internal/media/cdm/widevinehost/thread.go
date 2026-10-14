//go:build amd64 || arm64

package widevinehost

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"time"
)

// cdmThread is the one OS thread every call into the CDM is made on, as in Chromium (where the CDM lives on a
// single task runner). Jobs queue on it; a job that waits for an asynchronous answer (a promise) keeps running
// other queued jobs, so a CDM timer or a file IO completion that the answer depends on is never starved.
type cdmThread struct {
	jobs chan func()
	quit chan struct{}
	done chan struct{}
	once sync.Once
}

func newCDMThread() *cdmThread {
	t := &cdmThread{jobs: make(chan func(), 256), quit: make(chan struct{}), done: make(chan struct{})}
	go t.run()
	return t
}

var errThreadClosed = errors.New("widevinehost: CDM thread closed")

func (t *cdmThread) run() {
	runtime.LockOSThread()
	defer close(t.done)
	for {
		select {
		case f := <-t.jobs:
			f()
		case <-t.quit:
			// drain what is queued so waiters are released
			for {
				select {
				case f := <-t.jobs:
					f()
				default:
					return
				}
			}
		}
	}
}

// do runs f on the CDM thread and waits for it.
func (t *cdmThread) do(f func()) error {
	done := make(chan struct{})
	job := func() { f(); close(done) }
	select {
	case t.jobs <- job:
	case <-t.quit:
		return errThreadClosed
	}
	select {
	case <-done:
		return nil
	case <-t.done:
		select {
		case <-done:
			return nil
		default:
			return errThreadClosed
		}
	}
}

// post queues f without waiting; it reports false when the thread is closed.
func (t *cdmThread) post(f func()) bool {
	select {
	case <-t.quit:
		return false
	default:
	}
	select {
	case t.jobs <- f:
		return true
	case <-t.quit:
		return false
	}
}

func (t *cdmThread) close() {
	t.once.Do(func() { close(t.quit) })
	<-t.done
}

// await blocks the CDM thread until ch yields, running queued jobs meanwhile. It must be called from a job.
func await[T any](ctx context.Context, t *cdmThread, ch <-chan T, timeout time.Duration) (T, error) {
	var zero T
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case v := <-ch:
			return v, nil
		case f := <-t.jobs:
			f()
		case <-ctx.Done():
			return zero, ctx.Err()
		case <-timer.C:
			return zero, errors.New("widevinehost: the CDM did not answer in time")
		}
	}
}
