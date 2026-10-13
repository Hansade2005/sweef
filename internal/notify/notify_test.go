package notify

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/dbus"
)

// fakeDaemon is a tiny org.freedesktop.Notifications implementation: it records what Notify receives.
type fakeDaemon struct {
	conn *dbus.Conn
	mu   sync.Mutex
	caps []string
	next uint32
	got  []fakeNote
	shut []uint32
}

type fakeNote struct {
	ID, Replaces             uint32
	App, Icon, Summary, Body string
	Actions                  []string
	Hints                    map[string]dbus.Variant
	Expire                   int32
}

func (d *fakeDaemon) serve(m *dbus.Message) (string, []any, *dbus.Error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	switch m.Member {
	case "GetCapabilities":
		return "as", []any{d.caps}, nil
	case "GetServerInformation":
		return "ssss", []any{"fake", "test", "1", "1.2"}, nil
	case "Notify":
		d.next++
		n := fakeNote{ID: d.next, App: m.Body[0].(string), Replaces: m.Body[1].(uint32), Icon: m.Body[2].(string), Summary: m.Body[3].(string),
			Body: m.Body[4].(string), Expire: m.Body[7].(int32), Hints: map[string]dbus.Variant{}}
		for _, a := range m.Body[5].([]any) {
			n.Actions = append(n.Actions, a.(string))
		}
		for k, v := range m.Body[6].(map[any]any) {
			n.Hints[k.(string)] = v.(dbus.Variant)
		}
		id := d.next
		if n.Replaces != 0 {
			id = n.Replaces
			n.ID = id
		}
		d.got = append(d.got, n)
		return "u", []any{id}, nil
	case "CloseNotification":
		d.shut = append(d.shut, m.Body[0].(uint32))
		return "", nil, nil
	}
	return "", nil, &dbus.Error{Name: "org.freedesktop.DBus.Error.UnknownMethod", Msg: m.Member}
}

func (d *fakeDaemon) notes() []fakeNote {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]fakeNote(nil), d.got...)
}

// peerPair connects a Freedesktop sink to a fake daemon over an in-memory pipe.
func peerPair(t *testing.T, caps ...string) (*Freedesktop, *fakeDaemon, chan Event) {
	t.Helper()
	a, b := net.Pipe()
	d := &fakeDaemon{caps: caps}
	srv := make(chan error, 1)
	go func() {
		c, err := dbus.NewConn(b, dbus.ServerSide())
		if err == nil {
			d.conn = c
			c.Serve(d.serve)
		}
		srv <- err
	}()
	cc, err := dbus.NewConn(a, dbus.Peer())
	if err != nil {
		t.Fatal(err)
	}
	if err := <-srv; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cc.Close(); d.conn.Close() })
	f, err := NewFreedesktop(cc, "Sweef")
	if err != nil {
		t.Fatal(err)
	}
	ev := make(chan Event, 16)
	f.SetHandler(func(e Event) { ev <- e })
	return f, d, ev
}

func next(t *testing.T, ev chan Event) Event {
	t.Helper()
	select {
	case e := <-ev:
		return e
	case <-time.After(5 * time.Second):
		t.Fatal("no event")
		return Event{}
	}
}

func none(t *testing.T, ev chan Event) {
	t.Helper()
	select {
	case e := <-ev:
		t.Fatalf("unexpected event %+v", e)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestFreedesktopNotifyArguments(t *testing.T) {
	f, d, _ := peerPair(t, "actions", "body", "body-markup")
	err := f.Show(Notification{ID: "n1", Origin: "https://chat.example", Title: "A <b> & B", Body: "x < y", IconPath: "/tmp/i.png",
		Silent: true, RequireInteraction: true, Actions: []Action{{ID: "reply", Title: "Reply"}, {ID: "mute", Title: "Mute"}}})
	if err != nil {
		t.Fatal(err)
	}
	n := d.notes()[0]
	if n.App != "Sweef" || n.Summary != "A &lt;b&gt; &amp; B" || n.Icon != "/tmp/i.png" {
		t.Fatalf("%+v", n)
	}
	if !strings.HasPrefix(n.Body, "x &lt; y\n<i>https://chat.example</i>") {
		t.Fatalf("body = %q", n.Body)
	}
	if want := []string{"default", "Open", "reply", "Reply", "mute", "Mute"}; !reflect.DeepEqual(n.Actions, want) {
		t.Fatalf("actions = %v", n.Actions)
	}
	if n.Expire != 0 || n.Hints["urgency"].Value != byte(2) || n.Hints["suppress-sound"].Value != true ||
		n.Hints["x-kde-origin-name"].Value != "https://chat.example" || n.Hints["image-path"].Value != "/tmp/i.png" {
		t.Fatalf("hints/expire: %+v %d", n.Hints, n.Expire)
	}
	// a plain service: no actions, no markup, the origin is appended as text
	f2, d2, _ := peerPair(t, "body")
	if err := f2.Show(Notification{ID: "n2", Origin: "https://a.example", Title: "T<", Body: "b"}); err != nil {
		t.Fatal(err)
	}
	n = d2.notes()[0]
	if len(n.Actions) != 0 || n.Summary != "T<" || n.Body != "b\nhttps://a.example" || n.Expire != -1 || n.Hints["urgency"].Value != byte(1) {
		t.Fatalf("%+v", n)
	}
}

func TestFreedesktopSignalsBecomeEvents(t *testing.T) {
	f, d, ev := peerPair(t, "actions")
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		if err := f.Show(Notification{ID: id, Title: id}); err != nil {
			t.Fatal(err)
		}
	}
	sig := func(member, sigStr string, args ...any) {
		if err := d.conn.Signal("", fdoPath, fdoIface, member, sigStr, args...); err != nil {
			t.Fatal(err)
		}
	}
	sig("ActionInvoked", "us", uint32(1), "default")
	if e := next(t, ev); e.Kind != Click || e.ID != "a" {
		t.Fatalf("%+v", e)
	}
	sig("NotificationClosed", "uu", uint32(1), uint32(2)) // the bubble goes away after the click
	none(t, ev)
	sig("ActionInvoked", "us", uint32(2), "reply")
	if e := next(t, ev); e.Kind != ActionClick || e.ID != "b" || e.Action != "reply" {
		t.Fatalf("%+v", e)
	}
	sig("NotificationClosed", "uu", uint32(3), uint32(2))
	if e := next(t, ev); e.Kind != Closed || e.ID != "c" || e.Reason != ReasonUser {
		t.Fatalf("%+v", e)
	}
	sig("NotificationClosed", "uu", uint32(4), uint32(1))
	if e := next(t, ev); e.Kind != Closed || e.ID != "d" || e.Reason != ReasonTimeout {
		t.Fatalf("%+v", e)
	}
	sig("NotificationClosed", "uu", uint32(5), uint32(3)) // closed by our own CloseNotification
	sig("NotificationClosed", "uu", uint32(99), uint32(2))
	sig("ActionInvoked", "us", uint32(99), "default") // not ours
	none(t, ev)
}

func TestFreedesktopTagReplacesAndCloseIsSilent(t *testing.T) {
	f, d, ev := peerPair(t, "actions")
	f.Show(Notification{ID: "m1", Origin: "https://x.example", Tag: "thread", Title: "one"})
	f.Show(Notification{ID: "m2", Origin: "https://x.example", Tag: "thread", Title: "two"})
	f.Show(Notification{ID: "m3", Origin: "https://y.example", Tag: "thread", Title: "other origin"})
	g := d.notes()
	if g[0].Replaces != 0 || g[1].Replaces != g[0].ID || g[2].Replaces != 0 {
		t.Fatalf("replaces: %d %d %d", g[0].Replaces, g[1].Replaces, g[2].Replaces)
	}
	f.Close("m2")
	f.Close("unknown")
	d.mu.Lock()
	shut := append([]uint32(nil), d.shut...)
	d.mu.Unlock()
	if len(shut) != 1 || shut[0] != g[1].ID {
		t.Fatalf("CloseNotification calls: %v", shut)
	}
	d.conn.Signal("", fdoPath, fdoIface, "NotificationClosed", "uu", g[1].ID, uint32(3))
	none(t, ev)
}

func TestFreedesktopUnavailableWithoutService(t *testing.T) {
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/nonexistent/bus")
	if _, err := OpenFreedesktop("Sweef"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
	t.Setenv("XDG_RUNTIME_DIR", "")
	if _, err := OpenFreedesktop("Sweef"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

// The sink against a real dbus-daemon, with the fake service owning the well-known name.
func TestFreedesktopThroughRealBus(t *testing.T) {
	bin, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skip("dbus-daemon is not installed")
	}
	cmd := exec.Command(bin, "--session", "--nofork", "--print-address")
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	addr, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Skip(err)
	}
	addr = strings.TrimSpace(addr)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", addr)

	if _, err := OpenFreedesktop("Sweef"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("with no service on the bus: %v", err)
	}
	svc, err := dbus.Dial(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	d := &fakeDaemon{caps: []string{"actions", "body"}, conn: svc}
	svc.Serve(d.serve)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if r, err := svc.Call(ctx, "org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus", "RequestName", "su", fdoName, uint32(4)); err != nil || r.Body[0] != uint32(1) {
		t.Fatalf("RequestName: %v %v", r, err)
	}
	f, err := OpenFreedesktop("Sweef")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Shutdown()
	ev := make(chan Event, 8)
	f.SetHandler(func(e Event) { ev <- e })
	if err := f.Show(Notification{ID: "r1", Title: "through the bus"}); err != nil {
		t.Fatal(err)
	}
	if g := d.notes(); len(g) != 1 || g[0].Summary != "through the bus" {
		t.Fatalf("%+v", g)
	}
	svc.Signal("", fdoPath, fdoIface, "ActionInvoked", "us", uint32(1), "default")
	if e := next(t, ev); e.Kind != Click || e.ID != "r1" {
		t.Fatalf("%+v", e)
	}
	// another client on the bus cannot make the browser believe the user clicked
	evil, err := dbus.Dial(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer evil.Close()
	if err := f.Show(Notification{ID: "r2", Title: "second"}); err != nil {
		t.Fatal(err)
	}
	evil.Signal("", fdoPath, fdoIface, "ActionInvoked", "us", uint32(2), "default")
	none(t, ev)
}

func TestRecorderAndFallback(t *testing.T) {
	rec := NewRecorder()
	var toasts []Notification
	fb := NewFallback(rec, func(n Notification) { toasts = append(toasts, n) })
	var got []Event
	fb.SetHandler(func(e Event) { got = append(got, e) })
	if err := fb.Show(Notification{ID: "1", Title: "native"}); err != nil || len(rec.Shown()) != 1 || len(toasts) != 0 {
		t.Fatalf("native path: %v %d %d", err, len(rec.Shown()), len(toasts))
	}
	rec.Click("1")
	rec.ClickAction("1", "go")
	rec.Dismiss("1")
	if len(got) != 3 || got[0].Kind != Click || got[1].Action != "go" || got[2].Kind != Closed || got[2].Reason != ReasonUser {
		t.Fatalf("events: %+v", got)
	}
	rec.FailNext(errors.New("service gone"))
	if err := fb.Show(Notification{ID: "2", Title: "toast"}); err != nil || len(toasts) != 1 || toasts[0].ID != "2" {
		t.Fatalf("fallback path: %v %v", err, toasts)
	}
	fb.Emit(Event{Kind: Click, ID: "2"})
	if got[len(got)-1].ID != "2" {
		t.Fatal("window events must reach the handler")
	}
	rec.FailNext(errors.New("service gone"))
	fb.Show(Notification{ID: "3", Title: "toast again"})
	fb.Close("3")
	if len(rec.Closed()) != 0 {
		t.Fatal("a toast is closed by the window, not by the native sink")
	}
	fb.Close("1")
	if c := rec.Closed(); len(c) != 1 || c[0] != "1" {
		t.Fatalf("closed = %v", c)
	}
	if err := NewFallback(nil, nil).Show(Notification{ID: "x"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("nothing at all: %v", err)
	}
	if NewFallback(nil, func(Notification) {}).Name() != "toast" || fb.Name() != "recorder+toast" {
		t.Fatal("names")
	}
	_ = os.Getpid
}
