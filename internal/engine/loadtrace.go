package engine

// loadtrace.go: a per-navigation timeline. Every phase of a page load (document
// request, parse, stylesheet and script fetches, script execution, lifecycle
// events, first layout and paint) records a mark with its start offset and
// duration, so "why is this page slow" has an answer without a profiler. The
// timeline is always collected (a few dozen small structs per page) and read
// with Tab.LoadTimeline; the connection phases (DNS, connect, TLS, first byte)
// are added to it when Tab.TraceNetwork(true) is set (they cost an httptrace
// per request).

import (
	"context"
	"crypto/tls"
	"net/http/httptrace"
	"sync"
	"time"
)

// TraceMark is one phase of a navigation.
type TraceMark struct {
	Name   string        `json:"name"`
	At     time.Duration `json:"at"`  // offset of the start from the beginning of the navigation
	Dur    time.Duration `json:"dur"` // 0 for an instant
	Detail string        `json:"detail,omitempty"`
}

type loadTrace struct {
	mu    sync.Mutex
	t0    time.Time
	marks []TraceMark
}

const maxTraceMarks = 4096

func newLoadTrace() *loadTrace { return &loadTrace{t0: time.Now()} }

func (l *loadTrace) add(m TraceMark) {
	l.mu.Lock()
	if len(l.marks) < maxTraceMarks {
		l.marks = append(l.marks, m)
	}
	l.mu.Unlock()
}

// trace returns the current navigation's timeline (never nil).
func (t *Tab) trace() *loadTrace {
	if l := t.tr.Load(); l != nil {
		return l
	}
	l := newLoadTrace()
	if t.tr.CompareAndSwap(nil, l) {
		return l
	}
	return t.tr.Load()
}

// resetTrace starts a new timeline at "now" (a navigation begins).
func (t *Tab) resetTrace() { t.tr.Store(newLoadTrace()) }

// tmark records an instant.
func (t *Tab) tmark(name, detail string) {
	l := t.trace()
	l.add(TraceMark{Name: name, At: time.Since(l.t0), Detail: detail})
}

// tspan records a phase that began at start and ends now.
func (t *Tab) tspan(name, detail string, start time.Time) {
	l := t.trace()
	l.add(TraceMark{Name: name, At: start.Sub(l.t0), Dur: time.Since(start), Detail: detail})
}

// LoadTimeline returns a copy of the marks of the current navigation, in the
// order they were recorded.
func (t *Tab) LoadTimeline() []TraceMark {
	l := t.trace()
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]TraceMark(nil), l.marks...)
}

// TraceNetwork switches the per-request connection phases (DNS, connect, TLS,
// time to first byte) on or off for this tab.
func (t *Tab) TraceNetwork(on bool) { t.traceNet.Store(on) }

// withNetTrace attaches an httptrace to ctx when network tracing is on.
func (t *Tab) withNetTrace(ctx context.Context, what string) context.Context {
	if !t.traceNet.Load() {
		return ctx
	}
	var dns, conn, tlsS, wrote time.Time
	var host string
	return httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		DNSStart: func(i httptrace.DNSStartInfo) { dns, host = time.Now(), i.Host },
		DNSDone:  func(httptrace.DNSDoneInfo) { t.tspan("net.dns", host, dns) },
		ConnectStart: func(network, addr string) {
			conn = time.Now()
		},
		ConnectDone: func(network, addr string, err error) { t.tspan("net.connect", addr, conn) },
		TLSHandshakeStart: func() {
			tlsS = time.Now()
		},
		TLSHandshakeDone: func(tls.ConnectionState, error) { t.tspan("net.tls", what, tlsS) },
		GotConn: func(i httptrace.GotConnInfo) {
			reuse := "new"
			if i.Reused {
				reuse = "reused"
			}
			t.tmark("net.conn", what+" "+reuse)
		},
		WroteRequest:         func(httptrace.WroteRequestInfo) { wrote = time.Now() },
		GotFirstResponseByte: func() { t.tspan("net.ttfb", what, wrote) },
	})
}

// ---- window hooks for progressive rendering ---------------------------------------

// SetAsyncImages makes layout start image downloads in the background and go on
// without them (the box keeps its declared size, or none) instead of waiting for
// each image in turn; the tab calls the update hook, at most every imageRelayoutEvery, when
// images have arrived so the window lays out and paints again. Windows turn it
// on; the headless commands leave it off and get every image in the first layout.
// It has no effect while a Data Saver policy is in force (its gates are
// synchronous).
func (t *Tab) SetAsyncImages(on bool) { t.asyncImg.Store(on) }

// SetUpdateHook registers f, called (from any goroutine, never with an engine lock held) when
// the tab has new content a window should paint: images that arrived after the
// layout that asked for them. nil removes it.
func (t *Tab) SetUpdateHook(f func()) {
	if f == nil {
		t.onUpdate.Store(nil)
		return
	}
	t.onUpdate.Store(&f)
}

const imageRelayoutEvery = 90 * time.Millisecond

// imageArrived schedules one relayout for the images that came in meanwhile.
func (t *Tab) imageArrived(e *tabEnv) {
	if t.closed.Load() || !t.imgTimer.CompareAndSwap(false, true) {
		return
	}
	time.AfterFunc(imageRelayoutEvery, func() {
		t.imgTimer.Store(false)
		if t.closed.Load() {
			return
		}
		t.mu.RLock()
		d := t.dom
		t.mu.RUnlock()
		if d == nil || d != e.doc {
			return // another document by now
		}
		d.Touch()
		if f := t.onUpdate.Load(); f != nil {
			(*f)()
		}
	})
}
