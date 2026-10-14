package engine

// wasm_run.go — where wasm code runs, and the hand-off between it and the page thread.
//
// Outermost calls into wasm (an export called from JS, or instantiation with a start function) run on a
// dedicated runner goroutine; the page thread waits for them. Two reasons:
//
//   - wazero's compiled code cannot be interrupted without a per-loop-iteration exit to Go (its
//     WithCloseOnContextDone mode measured 15-25x slower on tight loops, see docs/webassembly.md), so a runaway
//     call cannot be killed. The page thread can, however, stop waiting for it: the host is poisoned, the page
//     keeps running, and the runtime is freed when the runaway call finally ends. Until then it keeps one CPU
//     busy.
//   - JavaScript imports (wasm -> JS) are executed by the page thread, never by the runner: the QuickJS VM has
//     one owner and its working set stays in that thread's cache (running the callbacks on the runner measured
//     2x slower per callback). The runner posts a request, the page thread, which is waiting anyway, serves
//     it and posts the reply.
//
// The hand-off is atomics with a short spin before either side parks: it is paid on every call across the JS
// boundary, and a channel round trip alone cost ~4 microseconds against a ~0.3 microsecond call.
//
// Calls made while wasm is already running (wasm -> JS -> wasm) run inline on the page thread: the outer
// activation is parked inside a host function, so exactly one thread executes wasm at any time.

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/tetratelabs/wazero/api"
	"modernc.org/quickjs"
)

const wasmSpin = 32768 // polls of the hand-off flags before parking; tens of microseconds

var errWasmTimedOut = errors.New("webassembly call timed out")

// cbReq is a wasm -> JS call the runner asks the page thread to make.
type cbReq struct {
	tok, idx int
	args     []any
	res      any
	err      error
	fin      atomic.Uint32
	waiting  atomic.Uint32
	ch       chan struct{}
}

// wcall is one outermost call into wasm.
type wcall struct {
	f func() error
	// state: 0 wasm is running, 1 a JS callback request is in flight, 2 the page thread gave up on the call
	state   atomic.Int32
	fin     atomic.Uint32 // the call returned
	waiting atomic.Uint32 // the page thread is parked on wake
	wake    chan struct{}
	req     atomic.Pointer[cbReq]
	err     error
	jsNano  int64 // page thread only: time spent serving callbacks
}

func (c *wcall) signalPage() {
	if c.waiting.Load() != 0 {
		select {
		case c.wake <- struct{}{}:
		default:
		}
	}
}

// wrunner is the goroutine outermost wasm calls run on.
type wrunner struct {
	h      *wasmHost
	slot   atomic.Pointer[wcall]
	wake   chan struct{}
	parked atomic.Uint32
	stop   atomic.Uint32
}

func (r *wrunner) loop() {
	for {
		c := r.next()
		if c == nil {
			return
		}
		r.run(c)
		if c.state.Load() == 2 { // the page abandoned this call: the host is finished
			return
		}
	}
}

func (r *wrunner) next() *wcall {
	for i := 0; ; i++ {
		if c := r.slot.Swap(nil); c != nil {
			return c
		}
		if r.stop.Load() != 0 {
			return nil
		}
		if i < wasmSpin {
			if i&1023 == 1023 {
				runtime.Gosched()
			}
			continue
		}
		r.parked.Store(1)
		if c := r.slot.Swap(nil); c != nil {
			r.parked.Store(0)
			return c
		}
		if r.stop.Load() != 0 {
			r.parked.Store(0)
			return nil
		}
		<-r.wake
		r.parked.Store(0)
		i = 0
	}
}

func (r *wrunner) run(c *wcall) {
	r.h.inflight.Add(1)
	defer func() {
		if rec := recover(); rec != nil {
			c.err = fmt.Errorf("webassembly call panicked: %v", rec)
		}
		c.fin.Store(1)
		c.signalPage()
		r.h.finishCall(c)
	}()
	c.err = c.f()
}

func (r *wrunner) submit(c *wcall) {
	r.slot.Store(c)
	if r.parked.Load() != 0 {
		select {
		case r.wake <- struct{}{}:
		default:
		}
	}
}

func (r *wrunner) shutdown() {
	r.stop.Store(1)
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// guard runs f, which enters wasm, and waits for it on the page thread, serving the JS callbacks it asks for.
// If the call outlives tmo() (time spent in callbacks excluded) it is abandoned.
func (h *wasmHost) guard(f func() error) error {
	if h.dead {
		return errWasmTimedOut
	}
	if h.depth > 0 { // wasm -> JS -> wasm: the outer activation is parked in a host function
		h.pageNest++
		defer func() { h.pageNest-- }()
		return f()
	}
	if h.run == nil {
		h.run = &wrunner{h: h, wake: make(chan struct{}, 1)}
		go h.run.loop()
	}
	c := &wcall{f: f, wake: make(chan struct{}, 1)}
	h.cur, h.depth = c, 1
	start := time.Now()
	h.run.submit(c)

	finish := func() error {
		h.depth, h.cur = 0, nil
		// Time inside wasm is not page-script time: give it back to the JS interrupt clock, so a long but
		// legitimate wasm call does not get the script that made it killed on return.
		h.raw.forgive(h.vm, time.Since(start)-time.Duration(c.jsNano))
		return c.err
	}
	poll := func() bool {
		if c.fin.Load() != 0 {
			return true
		}
		if rq := c.req.Swap(nil); rq != nil {
			h.serve(c, rq)
		}
		return false
	}
	for i := 0; i < wasmSpin; i++ {
		if poll() {
			return finish()
		}
		if i&1023 == 1023 {
			runtime.Gosched()
		}
	}
	for {
		c.waiting.Store(1)
		if poll() {
			c.waiting.Store(0)
			return finish()
		}
		remaining := h.tmo() - (time.Since(start) - time.Duration(c.jsNano))
		if remaining > 0 {
			t := time.NewTimer(remaining)
			select {
			case <-c.wake:
				t.Stop()
				c.waiting.Store(0)
				continue
			case <-t.C:
			}
		}
		c.waiting.Store(0)
		h.life.Lock()
		if c.fin.Load() != 0 {
			h.life.Unlock()
			return finish()
		}
		if c.state.CompareAndSwap(0, 2) {
			h.dead, h.zomb = true, true
			h.life.Unlock()
			break
		}
		h.life.Unlock() // a callback request is in flight: serve it, then look again
	}
	h.depth, h.cur = 0, nil
	h.raw.forgive(h.vm, time.Since(start)-time.Duration(c.jsNano))
	return errWasmTimedOut
}

// serve runs one JS callback on the page thread and hands the reply back to the runner.
func (h *wasmHost) serve(c *wcall, rq *cbReq) {
	t0 := time.Now()
	rq.res, rq.err = h.runJS(rq.tok, rq.idx, rq.args)
	c.jsNano += int64(time.Since(t0))
	c.state.Store(0)
	rq.fin.Store(1)
	if rq.waiting.Load() != 0 {
		close(rq.ch)
	}
}

// runJS calls the JS dispatcher. Page thread only.
func (h *wasmHost) runJS(tok, idx int, args []any) (any, error) {
	if h.closed || !h.haveDisp {
		return nil, errors.New("webassembly host is closed")
	}
	h.syncMems()
	all := make([]any, 0, 2+len(args))
	all = append(all, tok, idx)
	all = append(all, args...)
	return h.disp.Call(quickjs.UndefinedValue, all...)
}

// finishCall runs on the runner when its call ends; if the page had abandoned it, the runtime that was kept
// alive for it is released now (or when the VM closes, whichever is later).
func (h *wasmHost) finishCall(c *wcall) {
	h.life.Lock()
	defer h.life.Unlock()
	h.inflight.Add(-1)
	if c.state.Load() == 2 {
		h.zomb = false
	}
	if h.wantClose && !h.zomb && h.inflight.Load() == 0 {
		h.reallyClose()
	}
}

// hostFunc builds the wazero host function behind JS import number idx of the instance with token tok.
func (h *wasmHost) hostFunc(tok, idx int, ft wFuncType) api.GoModuleFunction {
	params, results := ft.params, ft.results
	return api.GoModuleFunc(func(ctx context.Context, mod api.Module, stack []uint64) {
		if h.aborted.Load() {
			panic(&wasmJSThrow{msg: "webassembly host is closing", internal: true})
		}
		args := make([]any, len(params))
		for i, p := range params {
			args[i] = wasmDecode(p, stack[i])
		}
		var res any
		var err error
		if h.pageNest > 0 { // wasm running nested on the page thread: call JS directly
			res, err = h.runJS(tok, idx, args)
		} else {
			res, err = h.callJS(tok, idx, args)
		}
		if err != nil {
			panic(&wasmJSThrow{msg: err.Error(), internal: true})
		}
		if s, ok := res.(string); ok {
			if s == wasmErrSentinel {
				panic(&wasmJSThrow{msg: "exception"})
			}
			if len(results) > 1 { // "m:" + comma separated
				parts := strings.Split(strings.TrimPrefix(s, "m:"), ",")
				for i, t := range results {
					if i < len(parts) {
						stack[i] = parseResult(t, parts[i])
					}
				}
				return
			}
		}
		if len(results) == 1 {
			stack[0] = wasmEncode(results[0], res)
		}
	})
}

// callJS is the runner's side of a callback: post the request, wait for the page thread's reply.
func (h *wasmHost) callJS(tok, idx int, args []any) (any, error) {
	c := h.cur
	if c == nil || !c.state.CompareAndSwap(0, 1) { // nil: the page thread already abandoned this call
		panic(&wasmJSThrow{msg: "webassembly call abandoned after exceeding its time budget", internal: true})
	}
	rq := &cbReq{tok: tok, idx: idx, args: args, ch: make(chan struct{})}
	c.req.Store(rq)
	c.signalPage()
	for i := 0; i < wasmSpin; i++ {
		if rq.fin.Load() != 0 {
			return rq.res, rq.err
		}
		if i&1023 == 1023 {
			runtime.Gosched()
		}
	}
	rq.waiting.Store(1)
	if rq.fin.Load() == 0 {
		<-rq.ch
	}
	return rq.res, rq.err
}
