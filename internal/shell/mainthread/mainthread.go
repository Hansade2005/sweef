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
)

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
	p := cur()
	running.Store(true)
	go func() {
		defer func() {
			running.Store(false)
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
	if r := <-done; r != nil {
		panic(r)
	}
	return nil
}

// chanLoop is the default platform: a plain task queue.
type chanLoop struct {
	q    chan func()
	stop chan struct{}
	once sync.Once
}

func newChanLoop() *chanLoop {
	return &chanLoop{q: make(chan func(), 256), stop: make(chan struct{})}
}

func (c *chanLoop) Run() {
	for {
		select {
		case f := <-c.q:
			f()
		case <-c.stop:
			return
		}
	}
}

func (c *chanLoop) Post(f func()) {
	select {
	case c.q <- f:
	case <-c.stop:
	}
}

func (c *chanLoop) Stop() { c.once.Do(func() { close(c.stop) }) }
