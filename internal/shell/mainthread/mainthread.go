// Package mainthread pins the Go program's main goroutine to the process
// main thread and lets the rest of the program run on goroutines while the
// OS UI loop owns that thread. Cocoa requires this (NSApplication and all
// windows must live on the main thread); on Linux and Windows the shells
// run their own loops on private threads and Run merely provides the same
// program shape everywhere:
//
//	func main() {
//	    mainthread.Run(func() {        // app code runs on a goroutine
//	        win, _ := auto.Open(cfg)
//	        ...
//	    })                             // returns when the func returns
//	}
package mainthread

import (
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
)

func init() {
	// init runs on the main goroutine, which is on the main thread; locking
	// here keeps it there for the life of the program.
	runtime.LockOSThread()
}

// Platform is the OS-specific main-thread loop (registered by the cocoa
// package). Run blocks on the main thread until Stop is called. Post queues
// f to run on the main thread and may be called from any goroutine.
type Platform interface {
	Run()
	Post(f func())
	Stop()
}

var (
	mu       sync.Mutex
	platform Platform = newChanLoop()
	running  atomic.Bool
	stopped  = make(chan struct{}) // closed when Run's app function returned
)

func stoppedCh() <-chan struct{} {
	mu.Lock()
	defer mu.Unlock()
	return stopped
}

// SetPlatform replaces the default channel-based loop. Call from init().
func SetPlatform(p Platform) {
	mu.Lock()
	platform = p
	mu.Unlock()
}

func cur() Platform {
	mu.Lock()
	defer mu.Unlock()
	return platform
}

// ErrNotRunning is returned by Do/Post when Run is not active.
var ErrNotRunning = errors.New("mainthread: Run is not active (call mainthread.Run from main)")

// Run executes app on a new goroutine and runs the platform loop on the
// calling (main) thread until app returns. It must be called from main().
func Run(app func()) {
	mu.Lock()
	p := platform
	done := make(chan struct{})
	stopped = done
	if c, ok := p.(*chanLoop); ok {
		c.reset()
	}
	mu.Unlock()
	running.Store(true)
	go func() {
		defer func() {
			running.Store(false)
			close(done)
			p.Stop()
		}()
		app()
	}()
	p.Run()
}

// Running reports whether Run is active.
func Running() bool { return running.Load() }

// Post runs f on the main thread without waiting for it.
func Post(f func()) error {
	if !running.Load() {
		return ErrNotRunning
	}
	cur().Post(f)
	return nil
}

// Do runs f on the main thread and waits for it to finish. A panic in f
// is re-raised in the caller. Do must not be called from the main thread
// itself (i.e. from inside another Do/Post callback): it would deadlock.
func Do(f func()) error {
	if !running.Load() {
		return ErrNotRunning
	}
	done := make(chan any, 1)
	cur().Post(func() {
		defer func() { done <- recover() }()
		f()
	})
	select {
	case r := <-done:
		if r != nil {
			panic(r)
		}
		return nil
	case <-stoppedCh():
		return ErrNotRunning
	}
}

// chanLoop is the default platform: a plain task queue.
type chanLoop struct {
	q chan func()

	mu      sync.Mutex
	stop    chan struct{}
	stopped bool
}

func newChanLoop() *chanLoop {
	return &chanLoop{q: make(chan func(), 256), stop: make(chan struct{})}
}

func (c *chanLoop) stopCh() chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stop
}

// reset prepares the loop for another Run (tests; programs run once).
func (c *chanLoop) reset() {
	c.mu.Lock()
	c.stop = make(chan struct{})
	c.stopped = false
	c.mu.Unlock()
}

func (c *chanLoop) Run() {
	stop := c.stopCh()
	for {
		select {
		case f := <-c.q:
			f()
		case <-stop:
			return
		}
	}
}

func (c *chanLoop) Post(f func()) {
	select {
	case c.q <- f:
	case <-c.stopCh():
	}
}

func (c *chanLoop) Stop() {
	c.mu.Lock()
	if !c.stopped {
		c.stopped = true
		close(c.stop)
	}
	c.mu.Unlock()
}
