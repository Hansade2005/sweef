//go:build linux

package x11

import "time"

// Clipboard support: the CLIPBOARD selection, as owner (SetClipboard) and as
// requestor (Clipboard), including INCR for transfers larger than one
// request in both directions.

type selNotifyEvent struct {
	selection, target, property uint32
}

type incrKey struct{ win, prop uint32 }

type incrSend struct {
	target uint32
	data   []byte
	off    int
}

const clipTimeout = 2 * time.Second

// incrThreshold is the size above which we use INCR when serving.
func (w *Window) incrThreshold() int {
	n := w.c.maxReqBytes - 256
	if n > 1<<18 {
		n = 1 << 18
	}
	return n
}

// SetClipboard takes ownership of CLIPBOARD and serves s.
func (w *Window) SetClipboard(s string) {
	if w.closed.Load() {
		return
	}
	w.ownMu.Lock()
	w.clipText = s
	w.owning = true
	w.ownMu.Unlock()
	w.c.setSelectionOwner(w.win, w.atoms["CLIPBOARD"], 0)
	w.c.sync() // when we return, other clients already see us as the owner
}

// Clipboard returns the current CLIPBOARD text ("" if empty/unavailable).
func (w *Window) Clipboard() string {
	if w.closed.Load() {
		return ""
	}
	w.ownMu.Lock()
	owning, s := w.owning, w.clipText
	w.ownMu.Unlock()
	if owning {
		// SelectionClear may still be in flight; the server knows the truth.
		if o, err := w.c.getSelectionOwner(w.atoms["CLIPBOARD"]); err != nil || o == w.win {
			return s
		}
		w.ownMu.Lock()
		w.owning, w.clipText = false, ""
		w.ownMu.Unlock()
	}

	w.clipMu.Lock()
	defer w.clipMu.Unlock()
	sel := w.atoms["CLIPBOARD"]
	owner, err := w.c.getSelectionOwner(sel)
	if err != nil || owner == 0 {
		return ""
	}
	for _, target := range []uint32{w.atoms["UTF8_STRING"], atomString} {
		if s, ok := w.requestSelection(sel, target, w.atoms["_SWEEF_CLIP"], w.win); ok {
			return s
		}
	}
	return ""
}

// requestSelection converts sel to target into prop of our window and reads
// the result (handling INCR). isText target atoms: UTF8_STRING or STRING.
func (w *Window) requestSelection(sel, target, prop, win uint32) (string, bool) {
	// drain stale notifications
	for drained := false; !drained; {
		select {
		case <-w.selNotify:
		default:
			drained = true
		}
	}
	for drained := false; !drained; {
		select {
		case <-w.propNotify:
		default:
			drained = true
		}
	}
	w.c.deleteProperty(win, prop)
	if err := w.c.convertSelection(win, sel, target, prop, 0); err != nil {
		return "", false
	}
	deadline := time.After(clipTimeout)
	for {
		select {
		case n := <-w.selNotify:
			if n.selection != sel {
				continue
			}
			if n.property == 0 {
				return "", false
			}
			data, ok := w.readSelectionProperty(prop, win)
			if !ok {
				return "", false
			}
			if target == atomString {
				return fromLatin1(data), true
			}
			return string(data), true
		case <-deadline:
			return "", false
		case <-w.c.done:
			return "", false
		}
	}
}

func (w *Window) readSelectionProperty(prop, win uint32) ([]byte, bool) {
	const limit = 256 << 20
	pr, err := w.c.getProperty(win, prop, 0, false, 0, 0) // peek type/size
	if err != nil || pr == nil {
		return nil, false
	}
	if pr.typ != w.atoms["INCR"] {
		all, err := w.c.getPropertyAll(win, prop, 0, true, limit)
		if err != nil || all == nil {
			return nil, false
		}
		return all.value, true
	}
	// INCR: deleting the property starts the transfer.
	w.c.deleteProperty(win, prop)
	var out []byte
	for {
		select {
		case <-w.propNotify:
		case <-time.After(clipTimeout):
			return nil, false
		case <-w.c.done:
			return nil, false
		}
		chunk, err := w.c.getProperty(win, prop, 0, true, 0, 1<<20)
		if err != nil || chunk == nil {
			return nil, false
		}
		if chunk.typ == 0 {
			continue // stale notification: the property is not there (yet)
		}
		if len(chunk.value) == 0 {
			return out, true
		}
		out = append(out, chunk.value...)
		if len(out) > limit {
			return nil, false
		}
	}
}

func (w *Window) onSelectionNotify(ev []byte) {
	n := selNotifyEvent{
		selection: le.Uint32(ev[12:]),
		target:    le.Uint32(ev[16:]),
		property:  le.Uint32(ev[20:]),
	}
	if n.selection == w.atoms["XdndSelection"] {
		go w.dndSelectionArrived(n)
		return
	}
	select {
	case w.selNotify <- n:
	default:
	}
}

// onSelectionRequest serves CLIPBOARD to other clients.
func (w *Window) onSelectionRequest(ev []byte) {
	requestor := le.Uint32(ev[12:])
	selection := le.Uint32(ev[16:])
	target := le.Uint32(ev[20:])
	prop := le.Uint32(ev[24:])
	timeStamp := le.Uint32(ev[4:])
	a := w.atoms

	refuse := func() { w.sendSelectionNotify(requestor, selection, target, 0, timeStamp) }
	w.ownMu.Lock()
	text, owning := w.clipText, w.owning
	w.ownMu.Unlock()
	if selection != a["CLIPBOARD"] || !owning {
		refuse()
		return
	}
	if prop == 0 {
		prop = target // obsolete clients
	}
	switch target {
	case a["TARGETS"]:
		w.c.changeProperty(0, requestor, prop, atomAtom, 32,
			u32s(a["TARGETS"], a["UTF8_STRING"], atomString, a["TEXT"], a["text/plain;charset=utf-8"], a["text/plain"]))
	case a["UTF8_STRING"], a["text/plain;charset=utf-8"], a["text/plain"], a["TEXT"], atomString:
		var data []byte
		typ := target
		if target == atomString {
			data = latin1(text)
		} else {
			data = []byte(text)
			if target == a["TEXT"] || target == a["text/plain"] {
				typ = a["UTF8_STRING"]
			}
		}
		if len(data) > w.incrThreshold() {
			w.startIncr(requestor, prop, typ, data)
		} else {
			w.c.changeProperty(0, requestor, prop, typ, 8, data)
		}
	default:
		refuse()
		return
	}
	w.sendSelectionNotify(requestor, selection, target, prop, timeStamp)
}

func (w *Window) sendSelectionNotify(requestor, selection, target, prop, t uint32) {
	ev := make([]byte, 32)
	ev[0] = evSelectionNotify
	le.PutUint32(ev[4:], t)
	le.PutUint32(ev[8:], requestor)
	le.PutUint32(ev[12:], selection)
	le.PutUint32(ev[16:], target)
	le.PutUint32(ev[20:], prop)
	w.c.sendEvent(requestor, false, 0, ev)
}

// startIncr begins an INCR transfer: announce the size, watch the requestor's
// property deletions and feed chunks as they are consumed.
func (w *Window) startIncr(requestor, prop, typ uint32, data []byte) {
	w.ownMu.Lock()
	w.incr[incrKey{requestor, prop}] = &incrSend{target: typ, data: data}
	w.ownMu.Unlock()
	w.c.changeWindowAttrs(requestor, 0x800, maskPropertyChange)
	w.c.changeProperty(0, requestor, prop, w.atoms["INCR"], 32, u32s(uint32(len(data))))
}

// incrDeleted is called when a property was deleted on some window: if it is
// the target of one of our INCR transfers, send the next chunk.
func (w *Window) incrDeleted(win, prop uint32) {
	w.ownMu.Lock()
	t := w.incr[incrKey{win, prop}]
	if t == nil {
		w.ownMu.Unlock()
		return
	}
	chunk := w.incrThreshold()
	end := t.off + chunk
	if end > len(t.data) {
		end = len(t.data)
	}
	part := t.data[t.off:end]
	t.off = end
	finished := len(part) == 0
	if finished {
		delete(w.incr, incrKey{win, prop})
	}
	typ := t.target
	w.ownMu.Unlock()
	w.c.changeProperty(0, win, prop, typ, 8, part) // empty part terminates the transfer
}
