package engine

// clipboard.go: the Async Clipboard API (navigator.clipboard.read/write/readText/writeText, ClipboardItem).
//
// The page-side API lives in platformjs/pwa.js next to the Permissions API it depends on; this file is the host:
// where the data is kept (a Clipboard the embedder installs, a per-context memory clipboard otherwise) and the
// rules. Writing needs a visible page and a secure context. Reading needs the "clipboard-read" permission, which
// the embedder grants or asks the user for through PWAOptions.Permission/Request, and a user gesture to ask.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// PermClipboardRead is the permission name the engine asks the embedder about before a page may read the clipboard.
const PermClipboardRead = "clipboard-read"

// ClipboardEntry is one flavour of the clipboard contents: a MIME type ("text/plain", "text/html", "image/png",
// "web text/custom") and its bytes.
type ClipboardEntry struct {
	Type string
	Data []byte
}

// Clipboard is the system clipboard as the engine sees it. Implementations must be safe for concurrent use.
type Clipboard interface {
	// Read returns every flavour currently on the clipboard (nil when it is empty).
	Read() ([]ClipboardEntry, error)
	// Write replaces the clipboard contents.
	Write([]ClipboardEntry) error
}

// MemoryClipboard is a Clipboard that lives in the process: the default of a BrowserContext, and what the tests use.
type MemoryClipboard struct {
	mu      sync.Mutex
	entries []ClipboardEntry
}

// Read implements Clipboard.
func (m *MemoryClipboard) Read() ([]ClipboardEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ClipboardEntry, len(m.entries))
	for i, e := range m.entries {
		out[i] = ClipboardEntry{e.Type, append([]byte(nil), e.Data...)}
	}
	return out, nil
}

// Write implements Clipboard.
func (m *MemoryClipboard) Write(es []ClipboardEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = make([]ClipboardEntry, len(es))
	for i, e := range es {
		m.entries[i] = ClipboardEntry{e.Type, append([]byte(nil), e.Data...)}
	}
	return nil
}

// SetClipboard installs the clipboard the pages of this context use (nil: a memory clipboard of its own).
func (c *BrowserContext) SetClipboard(cb Clipboard) {
	c.clipMu.Lock()
	c.clip = cb
	c.clipMu.Unlock()
}

// Clipboard returns the clipboard of the context.
func (c *BrowserContext) Clipboard() Clipboard {
	c.clipMu.Lock()
	defer c.clipMu.Unlock()
	if c.clip == nil {
		c.clip = &MemoryClipboard{}
	}
	return c.clip
}

// clipMaxBytes bounds what a page may put on the clipboard in one write (Chromium: far larger; this keeps a runaway
// page from filling memory through base64 and JSON).
const clipMaxBytes = 64 << 20

func init() {
	registerHostExtension("clipboard", func(j *jsVM) map[string]hostFn {
		visible := func() bool { return j.tab != nil && !j.tab.closed.Load() }
		return map[string]hostFn{
			// clipRead returns the entries as JSON [{type, b64}]; the permission was checked by the page's call to
			// pwaPermission/pwaRequestPermission, and is checked again here.
			"clipRead": func(a []any) (any, error) {
				t := j.tab
				if !visible() || !(&pwaHost{j: j}).secure() {
					return nil, fmt.Errorf("NotAllowedError: Read permission denied.")
				}
				if (&pwaHost{j: j}).st().permission((&pwaHost{j: j}).origin(), PermClipboardRead) != "granted" {
					return nil, fmt.Errorf("NotAllowedError: Read permission denied.")
				}
				es, err := t.Context().Clipboard().Read()
				if err != nil {
					return nil, fmt.Errorf("DataError: %v", err)
				}
				type item struct {
					Type string `json:"type"`
					B64  string `json:"b64"`
				}
				out := make([]item, 0, len(es))
				for _, e := range es {
					out = append(out, item{e.Type, base64.StdEncoding.EncodeToString(e.Data)})
				}
				b, _ := json.Marshal(out)
				return string(b), nil
			},
			// clipWrite takes JSON [{type, b64}].
			"clipWrite": func(a []any) (any, error) {
				t := j.tab
				h := &pwaHost{j: j}
				if !visible() || !h.secure() {
					return nil, fmt.Errorf("NotAllowedError: Write permission denied.")
				}
				var in []struct {
					Type string `json:"type"`
					B64  string `json:"b64"`
				}
				if err := json.Unmarshal([]byte(argS(a, 0)), &in); err != nil {
					return nil, fmt.Errorf("DataError: bad clipboard data")
				}
				total := 0
				es := make([]ClipboardEntry, 0, len(in))
				for _, it := range in {
					b, err := base64.StdEncoding.DecodeString(it.B64)
					if err != nil {
						return nil, fmt.Errorf("DataError: bad clipboard data")
					}
					total += len(b)
					if total > clipMaxBytes {
						return nil, fmt.Errorf("DataError: The clipboard data is too large.")
					}
					es = append(es, ClipboardEntry{strings.ToLower(it.Type), b})
				}
				if err := t.Context().Clipboard().Write(es); err != nil {
					return nil, fmt.Errorf("DataError: %v", err)
				}
				t.emit(EvClipboard, ClipboardWrite{Origin: h.origin()})
				return nil, nil
			},
		}
	})
}

// EvClipboard is published when a page wrote to the clipboard. Data is ClipboardWrite.
const EvClipboard EventKind = "Page.clipboard"

// ClipboardWrite is the payload of EvClipboard.
type ClipboardWrite struct{ Origin string }
