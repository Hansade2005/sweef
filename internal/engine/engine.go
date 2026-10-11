// Package engine is Sweef's browser core. It is intentionally *not* a fork
// of Blink/WebKit (which cost >100 MiB RSS before loading a page). Instead it
// is an agent-first micro-engine:
//
//   - HTML5-compliant tokenizer/tree builder (golang.org/x/net/html)
//   - CSS selector + cascade resolution for query/computed-style needs
//   - A real JS runtime via embedded QuickJS (cgo, ~1.5 MiB heap idle)
//   - Network stack with cookie jar, cache, HAR capture, request interception
//   - Structured text renderer that fits 320x200 and up; optional PNG blit
//
// Everything is exposed as small goroutine-per-tab state machines so a tab
// costs < 2 MiB instead of Chromium's ~60 MiB renderer process.
package engine

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

var (
	ErrNoTab      = errors.New("engine: no such tab")
	ErrClosed     = errors.New("engine: closed")
	ErrNotFound   = errors.New("engine: node not found")
	ErrTimeout    = errors.New("engine: timeout waiting for condition")
)

// Options tune the whole browser instance for low-memory machines.
type Options struct {
	MaxRSSMiB        int           // soft budget; GC pressure + eviction kicks in above this
	MaxCacheMiB      int           // HTTP memory cache ceiling
	Headless         bool          // always true today; "headful" streams to a thin client
	DisableJS        bool          // pure fetch+DOM mode for tiny agents
	ConsoleRingSize  int           // console messages retained per tab
	NetworkRingSize  int           // network events retained per tab
	StateRingSize    int           // actionability/state log entries retained
	DefaultUserAgent string
	ProxyURL         string
}

func DefaultOptions() Options {
	return Options{
		MaxRSSMiB:       48,
		MaxCacheMiB:     8,
		Headless:        true,
		ConsoleRingSize: 512,
		NetworkRingSize: 512,
		StateRingSize:   256,
		DefaultUserAgent: "Sweef/0.1 (agent; lightweight)",
	}
}

// EventKind enumerates everything SDKs/MCP can subscribe to.
type EventKind string

const (
	EvNavigated        EventKind = "Page.navigate"
	EvLoad             EventKind = "Page.load"
	EvDomMutated       EventKind = "DOM.nodeInserted"
	EvConsole          EventKind = "Log.entry"
	EvRequest          EventKind = "Network.request"
	EvResponse         EventKind = "Network.response"
	EvFailure          EventKind = "Network.failure"
	EvDialog           EventKind = "Page.javascript dialog"
	EvPopup            EventKind = "Page.popup"
	EvCrash            EventKind = "Page.crashed"
	EvDownload         EventKind = "Browser.download"
)

// Event is one broadcast entry. Ring buffers store these verbatim so history
// queries are O(n) over a bounded window, never unbounded.
type Event struct {
	SID   int64      `json:"sid"`
	Tab   string     `json:"tab"`
	Kind  EventKind  `json:"kind"`
	At    time.Time  `json:"at"`
	Data  any        `json:"data,omitempty"`
}

// Bus fans out events to subscribers (MCP bridge, CDP shim, SDK sockets).
type Bus struct {
	mu   sync.RWMutex
	subs map[uint64]chan Event
	next uint64
}

func NewBus() *Bus { return &Bus{subs: map[uint64]chan Event{}} }

func (b *Bus) Subscribe(depth int) (uint64, <-chan Event) {
	if depth <= 0 {
		depth = 64
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	id := b.next
	b.next++
	ch := make(chan Event, depth)
	b.subs[id] = ch
	return id, ch
}

func (b *Bus) Unsubscribe(id uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ch, ok := b.subs[id]; ok {
		close(ch)
		delete(b.subs, id)
	}
}

func (b *Bus) Publish(e Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subs {
		select {
		case ch <- e:
		default: // slow consumer drops; ring buffer still holds history
		}
	}
}

// Engine owns tabs, the event bus, and global resources (cookie jar, cache).
type Engine struct {
	opts Options
	bus  *Bus

	mu     sync.RWMutex
	tabs   map[string]*Tab
	order  []string
	nextID atomic.Int64

	httpClient *http.Client // lazily built: cookie jar + bounded transport

	ctx    context.Context
	cancel context.CancelFunc
	closed atomic.Bool
}

func New(opts Options) *Engine {
	ctx, cancel := context.WithCancel(context.Background())
	e := &Engine{opts: opts, bus: NewBus(), tabs: map[string]*Tab{}, ctx: ctx, cancel: cancel}
	runtimeInit(&e.opts)
	return e
}

func (e *Engine) Bus() *Bus          { return e.bus }
func (e *Engine) Opts() Options      { return e.opts }
func (e *Engine) Context() context.Context { return e.ctx }

func (e *Engine) newTabID() string {
	return fmt.Sprintf("tab-%d", e.nextID.Add(1))
}

// NewTab creates a blank tab. Cost target: < 2 MiB resident.
func (e *Engine) NewTab(url string) (*Tab, error) {
	if e.closed.Load() {
		return nil, ErrClosed
	}
	e.mu.Lock()
	t := newTab(e, e.newTabID())
	e.tabs[t.ID] = t
	e.order = append(e.order, t.ID)
	e.mu.Unlock()
	if url != "" && url != "about:blank" {
		if err := t.Navigate(url, nil); err != nil {
			return t, err
		}
	} else {
		t.SetDocument("<html><head><title>New Tab</title></head><body></body></html>", "about:blank")
	}
	return t, nil
}

func (e *Engine) Tab(id string) (*Tab, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	t, ok := e.tabs[id]
	if !ok {
		return nil, ErrNoTab
	}
	return t, nil
}

// ActiveTab returns the most recently created tab (single-window semantics).
func (e *Engine) ActiveTab() (*Tab, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if len(e.order) == 0 {
		return nil, ErrNoTab
	}
	return e.tabs[e.order[len(e.order)-1]], nil
}

func (e *Engine) ListTabs() []*Tab {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]*Tab, 0, len(e.order))
	for _, id := range e.order {
		out = append(out, e.tabs[id])
	}
	return out
}

func (e *Engine) CloseTab(id string) error {
	e.mu.Lock()
	t, ok := e.tabs[id]
	if ok {
		delete(e.tabs, id)
		for i, o := range e.order {
			if o == id {
				e.order = append(e.order[:i], e.order[i+1:]...)
				break
			}
		}
	}
	e.mu.Unlock()
	if !ok {
		return ErrNoTab
	}
	t.close()
	return nil
}

func (e *Engine) Close() error {
	if !e.closed.CompareAndSwap(false, true) {
		return nil
	}
	e.mu.Lock()
	tabs := make([]*Tab, 0, len(e.tabs))
	for _, t := range e.tabs {
		tabs = append(tabs, t)
	}
	e.tabs = map[string]*Tab{}
	e.order = nil
	e.mu.Unlock()
	for _, t := range tabs {
		t.close()
	}
	e.cancel()
	shutdownRuntime()
	return nil
}
