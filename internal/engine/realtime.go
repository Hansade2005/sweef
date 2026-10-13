package engine

// realtime.go — WebSockets and Web Workers.
//
// Both produce events asynchronously (a socket frame arrives, a worker posts
// a message) while page JavaScript may be running, so they push onto a small
// queue that the event-loop pump drains into the page VM. Frames are also
// logged to the network ring (kind Network.webSocket) so agents can inspect
// realtime traffic the same way they inspect requests.

import (
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"golang.org/x/net/websocket"
	"modernc.org/quickjs"
)

//go:embed dom_prelude_ext.js
var domPreludeExt string

//go:embed worker_prelude.js
var workerPrelude string

type asyncEvent struct {
	kind   string // ws | worker
	id     int64
	typ    string
	data   string
	binary bool
	code   int
	reason string
}

// realtime holds a page VM's sockets, workers and their pending events.
type realtime struct {
	imgPending  atomic.Int32 // detached image requests in flight (imgload.go)
	httpPending atomic.Int32 // fetch/XHR requests in flight (asynchttp.go)
	http        map[int64]httpOutcome
	httpAt      map[int64]time.Time // start of every unanswered request
	mu          sync.Mutex
	queue       []asyncEvent
	nextID      int64
	sockets     map[int64]*websocket.Conn
	workers     map[int64]*workerVM
}

func (j *jsVM) rt() *realtime {
	j.rtOnce.Do(func() {
		j.realtime = &realtime{sockets: map[int64]*websocket.Conn{}, workers: map[int64]*workerVM{}}
	})
	return j.realtime
}

func (r *realtime) push(e asyncEvent) {
	r.mu.Lock()
	r.queue = append(r.queue, e)
	r.mu.Unlock()
}

func (r *realtime) take() []asyncEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	q := r.queue
	r.queue = nil
	return q
}

func (r *realtime) pending() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.queue) > 0
}

func (r *realtime) id() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextID++
	return r.nextID
}

// closeAll tears down sockets and workers when the page goes away.
func (r *realtime) closeAll() {
	r.mu.Lock()
	socks, workers := r.sockets, r.workers
	r.sockets, r.workers = map[int64]*websocket.Conn{}, map[int64]*workerVM{}
	r.mu.Unlock()
	for _, c := range socks {
		c.Close()
	}
	for _, w := range workers {
		w.close()
	}
}

// ---- WebSocket ------------------------------------------------------------------

func (j *jsVM) wsLog(dir, rawurl, data string, binary bool) {
	if len(data) > 2048 {
		data = data[:2048] + "…"
	}
	ev := map[string]any{"direction": dir, "url": rawurl, "binary": binary, "data": data}
	j.tab.netRing.Push(Event{Tab: j.tab.ID, Kind: EvWebSocket, At: time.Now(), Data: ev})
	j.tab.emit(EvWebSocket, ev)
}

func (j *jsVM) wsOpen(rawurl, protocols string) int64 {
	r := j.rt()
	id := r.id()
	t := j.tab
	origin := t.origin()
	if !strings.HasPrefix(origin, "http") {
		origin = "http://localhost"
	}
	go func() {
		if t.IsOfflineMode() {
			r.push(asyncEvent{kind: "ws", id: id, typ: "error"})
			r.push(asyncEvent{kind: "ws", id: id, typ: "close", code: 1006, reason: "offline"})
			return
		}
		cfg, err := websocket.NewConfig(rawurl, origin)
		if err != nil {
			r.push(asyncEvent{kind: "ws", id: id, typ: "error"})
			r.push(asyncEvent{kind: "ws", id: id, typ: "close", code: 1006, reason: err.Error()})
			return
		}
		if protocols != "" {
			cfg.Protocol = strings.Split(protocols, ",")
		}
		cfg.Header = http.Header{}
		cfg.Header.Set("User-Agent", t.UserAgent())
		httpURL := strings.Replace(rawurl, "ws", "http", 1)
		if ck := t.Context().JarRef().ForURL(httpURL); ck != "" {
			cfg.Header.Set("Cookie", ck)
		}
		t.touchNet()
		conn, err := t.eng.dialWebSocket(t.eng.ctx, t.Context(), cfg)
		if err != nil {
			j.wsLog("error", rawurl, err.Error(), false)
			r.push(asyncEvent{kind: "ws", id: id, typ: "error"})
			r.push(asyncEvent{kind: "ws", id: id, typ: "close", code: 1006, reason: err.Error()})
			return
		}
		r.mu.Lock()
		r.sockets[id] = conn
		r.mu.Unlock()
		proto := ""
		if len(conn.Config().Protocol) > 0 {
			proto = conn.Config().Protocol[0]
		}
		j.wsLog("open", rawurl, proto, false)
		r.push(asyncEvent{kind: "ws", id: id, typ: "open", data: proto})
		for {
			var msg []byte
			if err := websocket.Message.Receive(conn, &msg); err != nil {
				r.mu.Lock()
				_, still := r.sockets[id]
				delete(r.sockets, id)
				r.mu.Unlock()
				code := 1006
				if !still {
					code = 1000 // we closed it
				}
				j.wsLog("close", rawurl, err.Error(), false)
				r.push(asyncEvent{kind: "ws", id: id, typ: "close", code: code})
				return
			}
			if utf8.Valid(msg) {
				j.wsLog("receive", rawurl, string(msg), false)
				r.push(asyncEvent{kind: "ws", id: id, typ: "message", data: string(msg)})
			} else {
				b64 := base64.StdEncoding.EncodeToString(msg)
				j.wsLog("receive", rawurl, b64, true)
				r.push(asyncEvent{kind: "ws", id: id, typ: "message", data: b64, binary: true})
			}
		}
	}()
	return id
}

func (j *jsVM) wsSend(id int64, data string, binary bool) error {
	r := j.rt()
	r.mu.Lock()
	conn := r.sockets[id]
	r.mu.Unlock()
	if conn == nil {
		return fmt.Errorf("InvalidStateError: socket %d is not open", id)
	}
	j.wsLog("send", conn.Config().Location.String(), data, binary)
	if binary {
		b, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			return err
		}
		return websocket.Message.Send(conn, b)
	}
	return websocket.Message.Send(conn, data)
}

func (j *jsVM) wsClose(id int64) {
	r := j.rt()
	r.mu.Lock()
	conn := r.sockets[id]
	delete(r.sockets, id)
	r.mu.Unlock()
	if conn != nil {
		conn.Close()
	}
}

// ---- Workers ----------------------------------------------------------------------

type workerVM struct {
	mu      sync.Mutex // the VM: held while the worker runs script, timers or message handlers
	inboxMu sync.Mutex // inbox only, never held across anything that blocks: the page thread posts and polls
	// through it while the worker may be busy (taking mu there deadlocked against realtime.mu, see hasLiveWork)
	vm     *quickjs.VM
	id     int64
	inbox  []string
	closed bool
	// started is set (under mu) once the worker's script has run, so its onmessage is installed. Messages that
	// arrive earlier stay queued: delivering them to a worker that has not run yet would silently drop them.
	started bool
	start   time.Time
	page    *jsVM
}

func (w *workerVM) close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	w.closed = true
	w.vm.Close()
}

func (j *jsVM) workerCreate(rawurl, src, typ string) (int64, error) {
	r := j.rt()
	id := r.id()
	vm, err := quickjs.NewVM()
	if err != nil {
		return 0, err
	}
	vm.SetMemoryLimit(32 << 20)
	vm.SetMaxStackSize(1 << 20)
	vm.SetEvalTimeout(5 * time.Second)
	w := &workerVM{vm: vm, id: id, start: time.Now(), page: j}
	t := j.tab
	reg := func(name string, fn func(a []any) (any, error)) { _ = vm.RegisterHostFunc("__w_"+name, fn) }
	reg("post", func(a []any) (any, error) {
		msg := argS(a, 0)
		var probe struct {
			Err string `json:"__swError"`
		}
		if json.Unmarshal([]byte(msg), &probe) == nil && probe.Err != "" {
			r.push(asyncEvent{kind: "worker", id: id, typ: "error", data: probe.Err})
			return nil, nil
		}
		r.push(asyncEvent{kind: "worker", id: id, typ: "message", data: msg})
		return nil, nil
	})
	reg("log", func(a []any) (any, error) { t.LogConsole(argS(a, 0), argS(a, 1)); return nil, nil })
	reg("http", func(a []any) (any, error) {
		hdr := map[string]string{}
		if len(a) > 2 {
			if m, ok := a[2].(map[string]any); ok {
				for k, v := range m {
					hdr[k] = fmt.Sprint(v)
				}
			}
		}
		u, err := resolveURL(rawurl, argS(a, 1))
		if err != nil {
			return nil, err
		}
		return t.httpForScript(argS(a, 0), u, hdr, argS(a, 3))
	})
	reg("fetchText", func(a []any) (any, error) {
		u, err := resolveURL(rawurl, argS(a, 0))
		if err != nil {
			return nil, err
		}
		body, status, err := t.fetchResourceKind(u, KindScript)
		if err != nil {
			return nil, err
		}
		if status >= 400 {
			return nil, fmt.Errorf("importScripts: HTTP %d for %s", status, u)
		}
		return body, nil
	})
	reg("now", func(a []any) (any, error) { return float64(time.Since(w.start).Microseconds()) / 1000, nil })
	reg("url", func(a []any) (any, error) { return rawurl, nil })
	reg("close", func(a []any) (any, error) { go j.workerTerminate(id); return nil, nil })
	reg("atob", func(a []any) (any, error) {
		b, err := base64.StdEncoding.DecodeString(argS(a, 0))
		return string(b), err
	})
	reg("btoa", func(a []any) (any, error) { return base64.StdEncoding.EncodeToString([]byte(argS(a, 0))), nil })
	glue := `globalThis.__w={post:__w_post,log:__w_log,http:__w_http,fetchText:__w_fetchText,now:__w_now,url:__w_url,close:__w_close,atob:__w_atob,btoa:__w_btoa};`
	if _, err := vm.Eval(glue, quickjs.EvalGlobal); err != nil {
		vm.Close()
		return 0, err
	}
	if _, err := vm.Eval(workerPrelude, quickjs.EvalGlobal); err != nil {
		vm.Close()
		return 0, fmt.Errorf("worker prelude: %w", err)
	}
	if src == "" {
		body, status, err := t.fetchResourceKind(rawurl, KindScript)
		if err != nil {
			vm.Close()
			return 0, err
		}
		if status >= 400 {
			vm.Close()
			return 0, fmt.Errorf("worker script %s: HTTP %d", rawurl, status)
		}
		src = body
	}
	r.mu.Lock()
	r.workers[id] = w
	r.mu.Unlock()
	// Run the worker script on the next pump turn, like a real worker start.
	go func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.closed {
			return
		}
		defer func() { w.started = true }()
		var err error
		if typ == "module" {
			vm.SetModuleLoader(func(_ *quickjs.VM, name string) (string, error) {
				b, _, err := t.fetchResourceKind(name, KindScript)
				return b, err
			}, func(_ *quickjs.VM, base, name string) (string, error) {
				if strings.Contains(name, "://") {
					return name, nil
				}
				b := base
				if !strings.Contains(b, "://") {
					b = rawurl
				}
				return resolveURL(b, name)
			})
			_, err = vm.Eval(src, quickjs.EvalModule)
		} else {
			_, err = vm.Eval(src, quickjs.EvalGlobal)
		}
		if err != nil {
			t.LogConsole("error", jsErrorText(err, "worker "+rawurl))
			r.push(asyncEvent{kind: "worker", id: id, typ: "error", data: err.Error()})
		}
		_, _ = vm.ExecutePendingJobs()
	}()
	return id, nil
}

func (j *jsVM) workerPost(id int64, msg string) {
	r := j.rt()
	r.mu.Lock()
	w := r.workers[id]
	r.mu.Unlock()
	if w == nil {
		return
	}
	w.inboxMu.Lock()
	w.inbox = append(w.inbox, msg)
	w.inboxMu.Unlock()
}

func (j *jsVM) workerTerminate(id int64) {
	r := j.rt()
	r.mu.Lock()
	w := r.workers[id]
	delete(r.workers, id)
	r.mu.Unlock()
	if w != nil {
		w.close()
	}
}

// pumpWorkers delivers queued messages to each worker and runs its timers.
// It returns the milliseconds until the earliest worker timer, or -1.
func (j *jsVM) pumpWorkers() float64 {
	if j.realtime == nil {
		return -1
	}
	r := j.realtime
	r.mu.Lock()
	ws := make([]*workerVM, 0, len(r.workers))
	for _, w := range r.workers {
		ws = append(ws, w)
	}
	r.mu.Unlock()
	next := -1.0
	for _, w := range ws {
		w.mu.Lock()
		if w.closed {
			w.mu.Unlock()
			continue
		}
		if !w.started {
			w.mu.Unlock()
			if next < 0 || next > 1 {
				next = 1 // look again soon: the script is still starting
			}
			continue
		}
		w.inboxMu.Lock()
		inbox := w.inbox
		w.inbox = nil
		w.inboxMu.Unlock()
		for _, m := range inbox {
			if _, err := w.vm.Call("__swwDeliver", m); err != nil {
				j.tab.LogConsole("error", jsErrorText(err, "worker onmessage"))
			}
		}
		_, _ = w.vm.ExecutePendingJobs()
		now := float64(time.Since(w.start).Microseconds()) / 1000
		res, err := w.vm.Call("__swwRunTimers", now)
		_, _ = w.vm.ExecutePendingJobs()
		w.mu.Unlock()
		if err != nil {
			continue
		}
		var n float64 = -1
		switch v := res.(type) {
		case int:
			n = float64(v)
		case float64:
			n = v
		}
		if n >= 0 && (next < 0 || n < next) {
			next = n
		}
	}
	return next
}

// drainAsync hands queued socket and worker events to page JavaScript.
// Returns true when anything was delivered.
func (j *jsVM) drainAsync() bool {
	if j.realtime == nil {
		return false
	}
	evs := j.realtime.take()
	for _, e := range evs {
		switch e.kind {
		case "ws":
			if _, err := j.call("__swWsEvent", e.id, e.typ, e.data, e.binary, e.code, e.reason); err != nil {
				j.tab.LogConsole("error", jsErrorText(err, "websocket "+e.typ))
			}
		case "worker":
			if _, err := j.call("__swWorkerEvent", e.id, e.typ, e.data); err != nil {
				j.tab.LogConsole("error", jsErrorText(err, "worker "+e.typ))
			}
		case "http":
			if _, err := j.call("__swHttpEvent", e.id); err != nil {
				j.tab.LogConsole("error", jsErrorText(err, "request callback"))
			}
		case "img":
			if _, err := j.call("__swImgEvent", e.id, e.typ, e.code, atoiSafe(e.data)); err != nil {
				j.tab.LogConsole("error", jsErrorText(err, "image "+e.typ))
			}
		}
	}
	return len(evs) > 0
}

// hasLiveWork reports open sockets or workers with queued work.
func (j *jsVM) hasLiveWork() bool {
	if j.realtime == nil {
		return false
	}
	r := j.realtime
	if r.imgPending.Load() > 0 || r.httpPending.Load() > 0 {
		return true // a detached image or a page request is on its way: the settle waits for its outcome
	}
	r.mu.Lock()
	if len(r.queue) > 0 {
		r.mu.Unlock()
		return true
	}
	ws := make([]*workerVM, 0, len(r.workers))
	for _, w := range r.workers {
		ws = append(ws, w)
	}
	r.mu.Unlock() // never hold r.mu while taking a worker lock: a starting worker holds its own and pushes to r
	for _, w := range ws {
		w.inboxMu.Lock()
		busy := len(w.inbox) > 0
		w.inboxMu.Unlock()
		if busy {
			return true
		}
	}
	return false
}

var _ = url.Parse

func atoiSafe(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}
