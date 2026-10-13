package notify

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/sweefbrowser/sweef/internal/dbus"
)

const (
	fdoName  = "org.freedesktop.Notifications"
	fdoPath  = "/org/freedesktop/Notifications"
	fdoIface = "org.freedesktop.Notifications"
)

// Freedesktop shows notifications through the org.freedesktop.Notifications service of the session bus (the
// Desktop Notifications Specification): Notify, CloseNotification and the NotificationClosed and ActionInvoked
// signals. Only signals sent by the current owner of the service name are believed.
type Freedesktop struct {
	h       handlerBox
	conn    *dbus.Conn
	app     string
	owner   string // unique name of the service; "" on a peer connection
	caps    map[string]bool
	timeout time.Duration

	mu      sync.Mutex
	server  map[string]uint32 // our id -> the service's id
	ours    map[uint32]string
	byTag   map[string]string // origin|tag -> our id
	clicked map[string]bool   // ids whose click was reported, so the "closed" signal that follows is not
	events  chan Event        // signals are handed to the handler on another goroutine: a handler may call Show
	stop    chan struct{}
}

// OpenFreedesktop connects to the session bus and prepares the sink. It fails (ErrUnavailable) when there is no
// session bus or no notification service on it.
func OpenFreedesktop(appName string) (*Freedesktop, error) {
	conn, err := dbus.Dial(dbus.SessionAddress())
	if err != nil {
		return nil, errors.Join(ErrUnavailable, err)
	}
	f, err := NewFreedesktop(conn, appName)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return f, nil
}

// NewFreedesktop uses an established connection (a bus connection, or a peer connection to a service in tests).
func NewFreedesktop(conn *dbus.Conn, appName string) (*Freedesktop, error) {
	f := &Freedesktop{conn: conn, app: appName, timeout: 5 * time.Second, server: map[string]uint32{}, ours: map[uint32]string{},
		byTag: map[string]string{}, clicked: map[string]bool{},
		events: make(chan Event, 256), stop: make(chan struct{})}
	go f.pump()
	ctx, cancel := context.WithTimeout(context.Background(), f.timeout)
	defer cancel()
	if conn.UniqueName() != "" {
		r, err := conn.Call(ctx, "org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus", "GetNameOwner", "s", fdoName)
		if err != nil || len(r.Body) != 1 {
			return nil, errors.Join(ErrUnavailable, err)
		}
		f.owner, _ = r.Body[0].(string)
	}
	r, err := conn.Call(ctx, fdoName, fdoPath, fdoIface, "GetCapabilities", "")
	if err != nil {
		return nil, errors.Join(ErrUnavailable, err)
	}
	f.caps = map[string]bool{}
	if len(r.Body) == 1 {
		if l, ok := r.Body[0].([]any); ok {
			for _, c := range l {
				if s, ok := c.(string); ok {
					f.caps[s] = true
				}
			}
		}
	}
	conn.OnSignal(f.signal)
	if conn.UniqueName() != "" {
		if err := conn.AddMatch(ctx, "type='signal',sender='"+fdoName+"',interface='"+fdoIface+"'"); err != nil {
			return nil, err
		}
	}
	return f, nil
}

func (f *Freedesktop) Name() string              { return "freedesktop" }
func (f *Freedesktop) SetHandler(fn func(Event)) { f.h.set(fn) }
func (f *Freedesktop) Shutdown() error {
	select {
	case <-f.stop:
	default:
		close(f.stop)
	}
	return f.conn.Close()
}

func (f *Freedesktop) pump() {
	for {
		select {
		case e := <-f.events:
			f.h.emit(e)
		case <-f.stop:
			return
		}
	}
}

func (f *Freedesktop) queue(e Event) {
	select {
	case f.events <- e:
	default: // a flood of events nobody consumes: drop rather than block the bus connection
	}
}

// Capabilities returns what the service reported ("actions", "body", "body-markup", "persistence", ...).
func (f *Freedesktop) Capabilities() []string {
	var out []string
	for c := range f.caps {
		out = append(out, c)
	}
	return out
}

// escapeMarkup makes text safe for services that interpret the body as markup.
func escapeMarkup(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func (f *Freedesktop) Show(n Notification) error {
	var replaces uint32
	key := n.Origin + "|" + n.Tag
	f.mu.Lock()
	if n.Tag != "" {
		if old, ok := f.byTag[key]; ok && old != n.ID {
			replaces = f.server[old]
		}
	}
	f.mu.Unlock()

	actions := []string{}
	if f.caps["actions"] {
		actions = append(actions, "default", "Open")
		for _, a := range n.Actions {
			actions = append(actions, a.ID, a.Title)
		}
	}
	urgency := byte(1)
	if n.RequireInteraction {
		urgency = 2
	}
	hints := map[string]dbus.Variant{
		"urgency":        {Sig: "y", Value: urgency},
		"desktop-entry":  {Sig: "s", Value: strings.ToLower(f.app)},
		"suppress-sound": {Sig: "b", Value: n.Silent},
		"category":       {Sig: "s", Value: "network"},
	}
	if n.Origin != "" {
		hints["x-kde-origin-name"] = dbus.Variant{Sig: "s", Value: n.Origin}
	}
	if n.IconPath != "" {
		hints["image-path"] = dbus.Variant{Sig: "s", Value: n.IconPath}
	}
	summary, body := n.Title, n.Body
	if f.caps["body-markup"] {
		summary, body = escapeMarkup(summary), escapeMarkup(body)
	}
	if n.Origin != "" && !f.caps["body-markup"] {
		// without markup the origin is appended as plain text so the user still sees who sent it
		body = strings.TrimSpace(body + "\n" + n.Origin)
	} else if n.Origin != "" {
		body = strings.TrimSpace(body + "\n<i>" + escapeMarkup(n.Origin) + "</i>")
	}
	expire := int32(-1)
	if n.RequireInteraction {
		expire = 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), f.timeout)
	defer cancel()
	r, err := f.conn.Call(ctx, fdoName, fdoPath, fdoIface, "Notify", "susssasa{sv}i",
		f.app, replaces, n.IconPath, summary, body, actions, hints, expire)
	if err != nil {
		return err
	}
	if len(r.Body) != 1 {
		return errors.New("notify: unexpected reply to Notify")
	}
	id, _ := r.Body[0].(uint32)
	f.mu.Lock()
	if replaces != 0 {
		if prev := f.ours[replaces]; prev != "" {
			delete(f.server, prev)
		}
		delete(f.ours, replaces)
	}
	f.server[n.ID], f.ours[id] = id, n.ID
	if n.Tag != "" {
		f.byTag[key] = n.ID
	}
	delete(f.clicked, n.ID)
	f.mu.Unlock()
	return nil
}

func (f *Freedesktop) Close(id string) {
	f.mu.Lock()
	sid, ok := f.server[id]
	delete(f.server, id)
	delete(f.ours, sid)
	f.mu.Unlock()
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), f.timeout)
	defer cancel()
	_, _ = f.conn.Call(ctx, fdoName, fdoPath, fdoIface, "CloseNotification", "u", sid)
}

func (f *Freedesktop) signal(m *dbus.Message) {
	if m.Interface != fdoIface || (f.owner != "" && m.Sender != f.owner) || len(m.Body) < 2 {
		return
	}
	sid, ok := m.Body[0].(uint32)
	if !ok {
		return
	}
	f.mu.Lock()
	id := f.ours[sid]
	f.mu.Unlock()
	if id == "" {
		return
	}
	switch m.Member {
	case "ActionInvoked":
		key, _ := m.Body[1].(string)
		f.mu.Lock()
		f.clicked[id] = true
		f.mu.Unlock()
		if key == "default" {
			f.queue(Event{Kind: Click, ID: id})
		} else {
			f.queue(Event{Kind: ActionClick, ID: id, Action: key})
		}
	case "NotificationClosed":
		reason, _ := m.Body[1].(uint32)
		f.mu.Lock()
		wasClicked := f.clicked[id]
		delete(f.clicked, id)
		delete(f.ours, sid)
		delete(f.server, id)
		f.mu.Unlock()
		if wasClicked || reason == 3 { // a click closes the bubble; 3 is our own CloseNotification
			return
		}
		r := ReasonUnknown
		switch reason {
		case 1:
			r = ReasonTimeout
		case 2:
			r = ReasonUser
		}
		f.queue(Event{Kind: Closed, ID: id, Reason: r})
	}
}
