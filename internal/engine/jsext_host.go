package engine

import (
	"strings"
	"sync/atomic"
	"time"

	"github.com/sweefbrowser/sweef/internal/jsext"
	fnet "github.com/sweefbrowser/sweef/internal/net"
	"golang.org/x/net/html"
)

// jsext_host.go binds jsext extensions (canvas, WebGL, media) into page VMs:
// their host functions join the __sw object and their preludes are compiled
// together with the DOM prelude.

// extHost implements jsext.Host for one VM; it resolves the tab and
// document at call time because pooled VMs are bound after creation.
type extHost struct{ j *jsVM }

func (h extHost) Doc() *fnet.Doc {
	j := h.j
	if j.tab == nil {
		return nil
	}
	j.tab.mu.RLock()
	defer j.tab.mu.RUnlock()
	return j.tab.dom
}

func (h extHost) Node(id int64) *html.Node {
	if d := h.Doc(); d != nil {
		return d.NodeByID(id)
	}
	return nil
}

func (h extHost) ResolveURL(ref string) string {
	if d := h.Doc(); d != nil {
		if u, err := resolveURL(d.BaseURL(), ref); err == nil {
			return u
		}
	}
	return ref
}

func (h extHost) Fetch(u string) ([]byte, string, int, error) {
	t := h.j.tab
	if t == nil {
		return nil, "", 0, errNoTab
	}
	if strings.HasPrefix(u, "data:") {
		mt, data, err := decodeDataURL(u)
		return data, strings.ToLower(mt), 200, err
	}
	r, err := t.httpForScript("GET", h.ResolveURL(u), nil, "")
	if err != nil {
		return nil, "", 0, err
	}
	status, _ := r["status"].(int)
	body, _ := r["body"].(string)
	hd, _ := r["headers"].(map[string]any)
	ct, _ := hd["content-type"].(string)
	return []byte(body), strings.ToLower(ct), status, nil
}

func (h extHost) Log(level, msg string) {
	if h.j.tab != nil {
		h.j.tab.LogConsole(level, msg)
	}
}

func (h extHost) Now() time.Time { return time.Now() }

func (h extHost) Invalidate() {
	if h.j.tab != nil {
		h.j.tab.renderGen.Add(1)
	}
}

func (h extHost) Key() any {
	if d := h.Doc(); d != nil {
		return d
	}
	return h.j
}

var errNoTab = errString("no tab")

type errString string

func (e errString) Error() string { return string(e) }

// extPreludes is the JS appended after the DOM prelude: the internals
// handle for extensions, each extension's prelude, then the handle is
// removed so page scripts never see it.
func extPreludes() string {
	exts := jsext.All()
	if len(exts) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, e := range exts {
		if e.Prelude == "" {
			continue
		}
		sb.WriteString("\n;(function(){\n")
		sb.WriteString(e.Prelude)
		sb.WriteString("\n})();\n")
	}
	sb.WriteString("delete globalThis.__swInternals;\n")
	return sb.String()
}

// extHostFuncs lists the extension functions as host entries named
// <ext>_<fn>.
func (j *jsVM) extHostFuncs() []struct {
	name string
	fn   func(a []any) (any, error)
} {
	var out []struct {
		name string
		fn   func(a []any) (any, error)
	}
	host := extHost{j}
	for _, e := range jsext.All() {
		for name, f := range e.Funcs {
			f := f
			out = append(out, struct {
				name string
				fn   func(a []any) (any, error)
			}{e.Name + "_" + name, func(a []any) (any, error) { return f(host, a) }})
		}
	}
	return out
}

// disposeExt frees extension state for a page that is going away.
func disposeExt(key any) {
	for _, e := range jsext.All() {
		if e.Dispose != nil {
			e.Dispose(key)
		}
	}
}

// renderGenOf is read by screenshot caching.
func (t *Tab) renderGeneration() uint64 { return t.renderGen.Load() }

var _ = atomic.Uint64{}
