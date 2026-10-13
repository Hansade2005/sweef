package engine

// pwa_notify.go: the notification registry. A notification is created by `new Notification()` in a page
// (non-persistent: events go to that page) or by registration.showNotification() (persistent: events go to the
// registration's service worker as notificationclick / notificationclose). The operating system part is a
// notify.Sink supplied through PWAOptions.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sweefbrowser/sweef/internal/notify"
)

const maxNotesPerOrigin = 25

type noteAction struct {
	Action string `json:"action"`
	Title  string `json:"title"`
	Icon   string `json:"icon,omitempty"`
}

// noteReq is a notification as the script described it; it is also what getNotifications() hands back.
type noteReq struct {
	ID                 string       `json:"id,omitempty"`
	Title              string       `json:"title"`
	Body               string       `json:"body"`
	Tag                string       `json:"tag"`
	Icon               string       `json:"icon"`
	Image              string       `json:"image"`
	Badge              string       `json:"badge"`
	Dir                string       `json:"dir"`
	Lang               string       `json:"lang"`
	Renotify           bool         `json:"renotify"`
	Silent             *bool        `json:"silent"`
	RequireInteraction bool         `json:"requireInteraction"`
	Timestamp          int64        `json:"timestamp"`
	Data               string       `json:"data"`
	Actions            []noteAction `json:"actions"`
	Persistent         bool         `json:"persistent"`
	RegID              string       `json:"regId,omitempty"`
}

type pwaNote struct {
	id, origin, tag string
	seq             int64
	persistent      bool
	regKey          string // swManager key of the registration (persistent)
	scope           string
	vm              *jsVM
	tab             *Tab
	req             noteReq
	auto            bool // the generic notification of the user-visible-push policy
}

func (h *pwaHost) notifyFns(f map[string]hostFn) {
	f["pwaNotifyShow"] = func(a []any) (any, error) {
		token := argI(a, 0)
		var req noteReq
		if err := json.Unmarshal([]byte(argS(a, 1)), &req); err != nil {
			return nil, &swError{"TypeError", "bad notification options"}
		}
		if !h.secure() || h.j.tab == nil {
			h.reply(token, nil, &swError{"TypeError", "Notifications need a secure context."})
			return nil, nil
		}
		st := h.st()
		origin, vm, tab := h.origin(), h.j, h.j.tab
		var reg *swReg
		if req.Persistent {
			if reg = st.c.sw().regByID(req.RegID); reg == nil || reg.origin != origin {
				h.reply(token, nil, &swError{"InvalidStateError", "The service worker registration is gone."})
				return nil, nil
			}
		}
		st.goJob(func() {
			id, err := st.showNote(origin, req, reg, vm, tab)
			if err != nil {
				h.reply(token, nil, err)
				return
			}
			h.reply(token, id, nil)
		})
		return nil, nil
	}
	f["pwaNotifyClose"] = func(a []any) (any, error) {
		h.st().closeNote(argS(a, 0), true)
		return nil, nil
	}
	f["pwaNotifyList"] = func(a []any) (any, error) {
		out := []any{}
		rt := h.st().c.sw().regByID(argS(a, 0))
		if rt == nil || rt.origin != h.origin() {
			return out, nil
		}
		tag := argS(a, 1)
		st := h.st()
		st.mu.Lock()
		defer st.mu.Unlock()
		for _, n := range st.notes {
			if n.persistent && n.regKey == regKey(rt.origin, rt.scope) && (tag == "" || n.tag == tag) {
				d := n.req
				d.ID = n.id
				b, _ := json.Marshal(d)
				var m map[string]any
				_ = json.Unmarshal(b, &m)
				out = append(out, m)
			}
		}
		return out, nil
	}
}

func notePermissionError(persistent bool) error {
	if persistent {
		return &swError{"TypeError", "Failed to execute 'showNotification' on 'ServiceWorkerRegistration': No notification permission has been granted for this origin."}
	}
	return &swError{"NotAllowedError", "Notification permission has not been granted for this origin."}
}

func (st *pwaState) showNote(origin string, req noteReq, reg *swReg, vm *jsVM, tab *Tab) (string, error) {
	if st.permission(origin, PermNotifications) != "granted" {
		return "", notePermissionError(req.Persistent)
	}
	if st.o.Sink == nil {
		return "", &swError{"NotSupportedError", "This browser cannot display notifications."}
	}
	var iconPath string
	if req.Icon != "" {
		iconPath = st.fetchIcon(origin, req.Icon)
	}
	n := &pwaNote{origin: origin, tag: req.Tag, persistent: req.Persistent, vm: vm, tab: tab, req: req}
	if reg != nil {
		n.regKey, n.scope = regKey(reg.origin, reg.scope), reg.scope
	}
	return st.registerAndShow(n, iconPath)
}

func (st *pwaState) registerAndShow(n *pwaNote, iconPath string) (string, error) {
	st.mu.Lock()
	st.noteSeq++
	n.id = fmt.Sprintf("n%d", st.noteSeq)
	n.seq = st.noteSeq
	n.req.ID = n.id
	var replaced *pwaNote
	var oldest *pwaNote
	count := 0
	for _, o := range st.notes {
		if o.origin != n.origin {
			continue
		}
		count++
		if n.tag != "" && o.tag == n.tag && o.persistent == n.persistent && o.scope == n.scope {
			replaced = o
		}
		if oldest == nil || o.seq < oldest.seq {
			oldest = o
		}
	}
	if replaced != nil {
		delete(st.notes, replaced.id)
		count--
	}
	if count >= maxNotesPerOrigin && oldest != nil && oldest != replaced {
		delete(st.notes, oldest.id)
	} else {
		oldest = nil
	}
	st.notes[n.id] = n
	st.mu.Unlock()
	if oldest != nil {
		st.o.Sink.Close(oldest.id)
	}

	silent := n.req.Silent != nil && *n.req.Silent
	if replaced != nil && !n.req.Renotify {
		silent = true // a replacement that does not ask to renotify does not make a sound again
	}
	dnd := st.o.DoNotDisturb != nil && st.o.DoNotDisturb()
	if !dnd {
		nn := notify.Notification{ID: n.id, Origin: n.origin, Title: n.req.Title, Body: n.req.Body, IconPath: iconPath, Tag: n.tag,
			Renotify: n.req.Renotify, Silent: silent, RequireInteraction: n.req.RequireInteraction, Timestamp: n.req.Timestamp,
			Lang: n.req.Lang, Dir: n.req.Dir}
		for i, a := range n.req.Actions {
			if i >= 2 {
				break
			}
			nn.Actions = append(nn.Actions, notify.Action{ID: a.Action, Title: a.Title, Icon: a.Icon})
		}
		if err := st.o.Sink.Show(nn); err != nil {
			st.mu.Lock()
			delete(st.notes, n.id)
			st.mu.Unlock()
			return "", &swError{"TypeError", "The notification could not be shown: " + err.Error()}
		}
	}
	if replaced != nil {
		st.o.Sink.Close(replaced.id)
	}
	st.mu.Lock()
	st.shown[n.origin]++
	st.mu.Unlock()
	return n.id, nil
}

// closeNote removes a notification; byScript says the page or worker called close() (its page gets a close event).
func (st *pwaState) closeNote(id string, byScript bool) {
	st.mu.Lock()
	n := st.notes[id]
	delete(st.notes, id)
	st.mu.Unlock()
	if n == nil {
		return
	}
	if st.o.Sink != nil {
		st.o.Sink.Close(id)
	}
	if byScript && !n.persistent && n.vm != nil {
		pushPWA(n.vm, map[string]any{"t": "note", "id": id, "ev": "close"})
	}
}

func (st *pwaState) closeNotesOf(origin string) {
	st.mu.Lock()
	var ids []string
	for id, n := range st.notes {
		if n.origin == origin {
			ids = append(ids, id)
		}
	}
	st.mu.Unlock()
	for _, id := range ids {
		st.closeNote(id, false)
	}
}

func (st *pwaState) closeNotesOfVM(j *jsVM) {
	st.mu.Lock()
	var ids []string
	for id, n := range st.notes {
		if !n.persistent && n.vm == j {
			ids = append(ids, id)
		}
	}
	st.mu.Unlock()
	for _, id := range ids {
		st.closeNote(id, false)
	}
}

func (st *pwaState) dropNotesOf(origin, scope string) {
	st.mu.Lock()
	var ids []string
	for id, n := range st.notes {
		if n.origin == origin && n.persistent && n.scope == scope {
			ids = append(ids, id)
		}
	}
	st.mu.Unlock()
	for _, id := range ids {
		st.closeNote(id, false)
	}
}

// Notifications lists the notifications that are currently shown (for tests and the app's notification list).
type NoteInfo struct {
	ID, Origin, Title, Body, Tag string
	Persistent                   bool
}

// Notifications returns what is shown now.
func (p *PWA) Notifications() []NoteInfo {
	p.st.mu.Lock()
	defer p.st.mu.Unlock()
	var out []NoteInfo
	for _, n := range p.st.notes {
		out = append(out, NoteInfo{ID: n.id, Origin: n.origin, Title: n.req.Title, Body: n.req.Body, Tag: n.tag, Persistent: n.persistent})
	}
	return out
}

// sinkEvent receives what the user did with a notification.
func (st *pwaState) sinkEvent(e notify.Event) {
	st.mu.Lock()
	n := st.notes[e.ID]
	st.mu.Unlock()
	if n == nil {
		return
	}
	switch e.Kind {
	case notify.Click, notify.ActionClick:
		st.mu.Lock()
		delete(st.notes, n.id) // the system closes a notification that was clicked
		st.mu.Unlock()
		action := ""
		if e.Kind == notify.ActionClick {
			action = e.Action
		}
		switch {
		case n.auto:
			st.goJob(func() { st.openOrigin(n.origin) })
		case n.persistent:
			st.goJob(func() { st.persistentEvent(n, "notificationclick", action) })
		default:
			if n.tab != nil && st.o.FocusTab != nil {
				st.o.FocusTab(n.tab)
			}
			if n.vm != nil {
				pushPWA(n.vm, map[string]any{"t": "note", "id": n.id, "ev": "click"})
				pushPWA(n.vm, map[string]any{"t": "note", "id": n.id, "ev": "close"})
			}
		}
	case notify.Closed:
		if e.Reason == notify.ReasonApplication {
			return
		}
		st.mu.Lock()
		delete(st.notes, n.id)
		st.mu.Unlock()
		switch {
		case n.auto:
		case n.persistent:
			st.goJob(func() { st.persistentEvent(n, "notificationclose", "") })
		case n.vm != nil:
			pushPWA(n.vm, map[string]any{"t": "note", "id": n.id, "ev": "close"})
		}
	case notify.Error:
		if n.vm != nil && !n.persistent {
			pushPWA(n.vm, map[string]any{"t": "note", "id": n.id, "ev": "error"})
		}
	}
}

// persistentEvent runs notificationclick / notificationclose in the registration's worker.
func (st *pwaState) persistentEvent(n *pwaNote, kind, action string) {
	m := st.c.sw()
	m.mu.Lock()
	reg := m.regs[n.regKey]
	m.mu.Unlock()
	if reg == nil {
		return
	}
	if kind == "notificationclick" {
		st.allowOpen(n.regKey, 10*time.Second)
	}
	d := n.req
	d.ID = n.id
	_, err := m.dispatchReg(reg, kind, map[string]any{"notification": d, "action": action}, st.o.EventTimeout)
	if err != nil && st.o.Logf != nil {
		st.o.Logf("pwa: %s for %s: %v", kind, n.origin, err)
	}
}

// openOrigin brings the user to the site: a tab of the origin if one is open, else a new one.
func (st *pwaState) openOrigin(origin string) {
	m := st.c.sw()
	m.mu.Lock()
	var t *Tab
	for _, c := range m.clientsOfLocked(origin) {
		t = c.tab
		break
	}
	m.mu.Unlock()
	if t != nil && st.o.FocusTab != nil {
		st.o.FocusTab(t)
		return
	}
	if st.o.OpenWindow != nil {
		_, _ = st.o.OpenWindow(origin + "/")
	}
}

// dispatchReg fires an event in the active worker of a registration, starting the worker's VM when it is not running.
func (m *swManager) dispatchReg(reg *swReg, kind string, payload any, timeout time.Duration) (swEventResult, error) {
	m.mu.Lock()
	w := reg.active
	m.mu.Unlock()
	if w == nil {
		return swEventResult{}, swErr("InvalidStateError", "the registration has no active service worker")
	}
	rt, err := m.runtimeFor(w)
	if err != nil {
		return swEventResult{}, err
	}
	return rt.dispatch(kind, payload, timeout)
}

// ---- icons ------------------------------------------------------------------------------------------------------------------

// fetchIcon downloads a notification icon to a private temp file so the system can show it. It returns "" when the
// icon cannot be had quickly; the notification is shown without it.
func (st *pwaState) fetchIcon(origin, raw string) string {
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		return ""
	}
	sum := sha256.Sum256([]byte(raw))
	base := filepath.Join(os.TempDir(), "sweef-notify")
	if err := os.MkdirAll(base, 0o700); err != nil {
		return ""
	}
	if ents, _ := filepath.Glob(filepath.Join(base, hex.EncodeToString(sum[:8])+".*")); len(ents) > 0 {
		return ents[0]
	}
	type res struct{ path string }
	ch := make(chan res, 1)
	go func() {
		t := st.c.sw().sysTab(origin)
		r, err := t.httpDo(httpOpts{method: "GET", url: raw, kind: KindImage})
		if err != nil || r.status != 200 || len(r.body) == 0 || len(r.body) > 1<<20 {
			ch <- res{}
			return
		}
		ct := http.DetectContentType(r.body)
		ext := map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp"}[ct]
		if ext == "" {
			ch <- res{}
			return
		}
		p := filepath.Join(base, hex.EncodeToString(sum[:8])+ext)
		if os.WriteFile(p, r.body, 0o600) != nil {
			ch <- res{}
			return
		}
		ch <- res{p}
	}()
	select {
	case r := <-ch:
		return r.path
	case <-time.After(4 * time.Second):
		return ""
	case <-st.quit:
		return ""
	}
}
