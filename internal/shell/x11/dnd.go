//go:build linux

package x11

import (
	"net/url"
	"strings"
	"sync"

	"github.com/sweefbrowser/sweef/internal/shell"
)

// XDND (drag and drop) target side, protocol version 5. Only file drops
// (text/uri-list) are accepted; they surface as EvDropFiles.

type dndState struct {
	mu      sync.Mutex
	source  uint32
	version uint32
	accept  bool
}

func (w *Window) dndInit() {
	w.c.changeProperty(0, w.win, w.atoms["XdndAware"], atomAtom, 32, u32s(5))
}

func (w *Window) onDndMessage(typ uint32, ev []byte) {
	a := w.atoms
	switch typ {
	case a["XdndEnter"]:
		src := le.Uint32(ev[12:])
		flags := le.Uint32(ev[16:])
		w.dnd.mu.Lock()
		w.dnd.source = src
		w.dnd.version = flags >> 24
		w.dnd.accept = false
		w.dnd.mu.Unlock()
		uri := a["text/uri-list"]
		for i := 0; i < 3; i++ {
			if le.Uint32(ev[20+4*i:]) == uri {
				w.dnd.mu.Lock()
				w.dnd.accept = true
				w.dnd.mu.Unlock()
			}
		}
		if flags&1 != 0 { // more than three types: read XdndTypeList
			go func() {
				pr, err := w.c.getProperty(src, a["XdndTypeList"], atomAtom, false, 0, 1024)
				if err != nil || pr == nil {
					return
				}
				for i := 0; i+4 <= len(pr.value); i += 4 {
					if le.Uint32(pr.value[i:]) == uri {
						w.dnd.mu.Lock()
						w.dnd.accept = true
						w.dnd.mu.Unlock()
					}
				}
			}()
		}
	case a["XdndPosition"]:
		w.dnd.mu.Lock()
		src, acc := w.dnd.source, w.dnd.accept
		w.dnd.mu.Unlock()
		if src != le.Uint32(ev[12:]) {
			return
		}
		flags, action := uint32(0), uint32(0)
		if acc {
			flags, action = 1, a["XdndActionCopy"]
		}
		w.c.sendEvent(src, false, 0, clientMessage32(src, a["XdndStatus"], [5]uint32{w.win, flags, 0, 0, action}))
	case a["XdndLeave"]:
		w.dnd.mu.Lock()
		w.dnd.source, w.dnd.accept = 0, false
		w.dnd.mu.Unlock()
	case a["XdndDrop"]:
		w.dnd.mu.Lock()
		src, acc := w.dnd.source, w.dnd.accept
		w.dnd.mu.Unlock()
		if src == 0 || !acc {
			if src != 0 {
				w.dndFinish(src, false)
			}
			return
		}
		stamp := le.Uint32(ev[20:])
		w.c.convertSelection(w.win, a["XdndSelection"], a["text/uri-list"], a["_SWEEF_DND"], stamp)
	}
}

func (w *Window) dndFinish(src uint32, ok bool) {
	a := w.atoms
	flags, action := uint32(0), uint32(0)
	if ok {
		flags, action = 1, a["XdndActionCopy"]
	}
	w.c.sendEvent(src, false, 0, clientMessage32(src, a["XdndFinished"], [5]uint32{w.win, flags, action, 0, 0}))
	w.dnd.mu.Lock()
	w.dnd.source, w.dnd.accept = 0, false
	w.dnd.mu.Unlock()
}

// dndSelectionArrived runs on its own goroutine (it issues round trips).
func (w *Window) dndSelectionArrived(n selNotifyEvent) {
	w.dnd.mu.Lock()
	src := w.dnd.source
	w.dnd.mu.Unlock()
	if n.property == 0 {
		if src != 0 {
			w.dndFinish(src, false)
		}
		return
	}
	pr, err := w.c.getPropertyAll(w.win, n.property, 0, true, 8<<20)
	if err != nil || pr == nil {
		if src != 0 {
			w.dndFinish(src, false)
		}
		return
	}
	files := ParseURIList(string(pr.value))
	if len(files) > 0 {
		w.q.Push(shell.Event{Kind: shell.EvDropFiles, Files: files})
	}
	if src != 0 {
		w.dndFinish(src, len(files) > 0)
	}
}

// ParseURIList extracts local file paths from a text/uri-list payload.
func ParseURIList(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		u, err := url.Parse(line)
		if err != nil || u.Scheme != "file" {
			continue
		}
		if u.Host != "" && u.Host != "localhost" {
			continue
		}
		if p := u.Path; p != "" {
			out = append(out, p)
		}
	}
	return out
}
