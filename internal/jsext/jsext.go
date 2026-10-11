// Package jsext lets subsystems outside the engine (canvas 2D, WebGL, media
// elements) add host functions and prelude JavaScript to every page VM
// without editing the engine's shared lists.
//
// An Extension registers in an init function:
//
//	func init() {
//		jsext.Register(jsext.Extension{
//			Name:    "canvas",
//			Prelude: preludeJS, // runs after the DOM prelude
//			Funcs:   map[string]jsext.Func{"fillRect": fillRect},
//		})
//	}
//
// Its functions appear to the prelude as __sw.canvas_fillRect(...). The
// prelude also sees globalThis.__swInternals (the DOM prelude's wrapper id
// symbol, wrap/idOf, slots, timers, the clock and URL helpers; see
// dom_prelude.js) for the duration of its evaluation.
//
// Arguments and results cross the boundary as the engine's host values:
// numbers arrive as float64 or int64, strings, bools, nil, []any and
// map[string]any. Large binary data should travel as base64 strings or as
// JS typed arrays converted with __swInternals helpers.
package jsext

import (
	"sort"
	"sync"
	"time"

	fnet "github.com/sweefbrowser/sweef/internal/net"
	"golang.org/x/net/html"
)

// Host is the page context a host function runs in. It is bound to the
// tab and document current at call time.
type Host interface {
	// Doc is the current document (nil when the VM is not bound).
	Doc() *fnet.Doc
	// Node resolves a DOM node id (the prelude's wrapper id) to its node.
	Node(id int64) *html.Node
	// ResolveURL resolves a reference against the document base URL.
	ResolveURL(ref string) string
	// Fetch GETs a sub-resource through the tab (cookies, routes, cache,
	// data: URLs). Content type is the response's, lower-cased.
	Fetch(url string) (body []byte, contentType string, status int, err error)
	// Log appends to the page console ("log", "warn", "error", ...).
	Log(level, msg string)
	// Now is the page clock (performance.now() epoch is VM start).
	Now() time.Time
	// Invalidate tells the engine the rendering changed (a canvas was
	// drawn, a video frame advanced) so cached frames are dropped.
	Invalidate()
	// Key identifies the page (tab + document) for per-page state; it
	// changes on navigation. Extensions key their state maps by it.
	Key() any
}

// Func is one host function.
type Func func(h Host, args []any) (any, error)

// Extension bundles host functions with the prelude that uses them.
type Extension struct {
	Name    string
	Prelude string
	Funcs   map[string]Func
	// Dispose, when set, is called with Host.Key() when a page goes away
	// so per-page state can be freed.
	Dispose func(key any)
	// Order sorts extensions' preludes (lower first); ties by Name.
	Order int
}

var (
	mu   sync.RWMutex
	exts = map[string]Extension{}
)

// Register adds or replaces an extension. Call from init.
func Register(e Extension) {
	mu.Lock()
	exts[e.Name] = e
	mu.Unlock()
}

// All returns the registered extensions in prelude order.
func All() []Extension {
	mu.RLock()
	out := make([]Extension, 0, len(exts))
	for _, e := range exts {
		out = append(out, e)
	}
	mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Order != out[j].Order {
			return out[i].Order < out[j].Order
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Helpers for argument decoding, shared by extensions.

// Float returns args[i] as float64 (NaN-free: missing -> 0).
func Float(a []any, i int) float64 {
	if i >= len(a) {
		return 0
	}
	switch v := a[i].(type) {
	case float64:
		return v
	case int64:
		return float64(v)
	case int:
		return float64(v)
	case int32:
		return float64(v)
	case bool:
		if v {
			return 1
		}
	}
	return 0
}

// Int returns args[i] as int64.
func Int(a []any, i int) int64 { return int64(Float(a, i)) }

// String returns args[i] as a string ("" when missing or not a string).
func String(a []any, i int) string {
	if i < len(a) {
		if s, ok := a[i].(string); ok {
			return s
		}
	}
	return ""
}

// Bool returns args[i] as a bool.
func Bool(a []any, i int) bool {
	if i < len(a) {
		switch v := a[i].(type) {
		case bool:
			return v
		case float64:
			return v != 0
		case int64:
			return v != 0
		}
	}
	return false
}
