// Package wavefront is the progress counter that the row-parallel loop filters of the video decoders wait on:
// row r of a picture may process unit c once row r-1 has finished unit c+1 or so. The waiting row spins for a
// moment (the units are short, so the wait usually is too) and then sleeps on a condition variable, instead
// of yielding in a loop, which burns the CPU the other rows need when the machine is busy.
package wavefront

import (
	"runtime"
	"sync"
	"sync/atomic"
)

// Rows tracks the number of finished units of every row.
type Rows struct {
	done  []atomic.Int32
	await []atomic.Int32 // goroutines of the next row that sleep or are about to
	mu    []sync.Mutex
	cond  []*sync.Cond
}

// New makes the counters of n rows, all at zero.
func New(n int) *Rows {
	r := &Rows{
		done:  make([]atomic.Int32, n),
		await: make([]atomic.Int32, n),
		mu:    make([]sync.Mutex, n),
		cond:  make([]*sync.Cond, n),
	}
	for i := range r.cond {
		r.cond[i] = sync.NewCond(&r.mu[i])
	}
	return r
}

// Done records that row has finished n units.
func (r *Rows) Done(row, n int) {
	r.done[row].Store(int32(n))
	if r.await[row].Load() != 0 {
		r.mu[row].Lock()
		r.cond[row].Broadcast()
		r.mu[row].Unlock()
	}
}

// Wait blocks until row has finished at least n units.
func (r *Rows) Wait(row, n int) {
	want := int32(n)
	if r.done[row].Load() >= want {
		return
	}
	for spin := 0; spin < 200; spin++ {
		if r.done[row].Load() >= want {
			return
		}
		if spin >= 100 {
			runtime.Gosched()
		}
	}
	r.await[row].Add(1)
	r.mu[row].Lock()
	for r.done[row].Load() < want {
		r.cond[row].Wait()
	}
	r.mu[row].Unlock()
	r.await[row].Add(-1)
}
