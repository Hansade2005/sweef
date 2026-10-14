package dbus

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func enc(t *testing.T, sig string, vals ...any) []byte {
	t.Helper()
	b, err := Encode(binary.LittleEndian, sig, vals...)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Byte sequences worked out by hand from the alignment rules of the specification.
func TestWireGoldenBytes(t *testing.T) {
	for name, c := range map[string]struct {
		sig  string
		vals []any
		want []byte
	}{
		"string":      {"s", []any{"hi"}, []byte{2, 0, 0, 0, 'h', 'i', 0}},
		"byte then u": {"yu", []any{byte(1), uint32(2)}, []byte{1, 0, 0, 0, 2, 0, 0, 0}},
		"strings":     {"as", []any{[]string{"a", "bc"}}, []byte{15, 0, 0, 0, 1, 0, 0, 0, 'a', 0, 0, 0, 2, 0, 0, 0, 'b', 'c', 0}},
		"signature":   {"g", []any{Signature("a{sv}")}, []byte{5, 'a', '{', 's', 'v', '}', 0}},
		"int32 neg":   {"i", []any{int32(-2)}, []byte{0xfe, 0xff, 0xff, 0xff}},
		"bool":        {"b", []any{true}, []byte{1, 0, 0, 0}},
		"u64 aligned": {"yt", []any{byte(7), uint64(1)}, []byte{7, 0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0}},
		"dict": {"a{sv}", []any{map[string]Variant{"k": {"b", true}}},
			[]byte{16, 0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 'k', 0, 1, 'b', 0, 0, 0, 0, 1, 0, 0, 0}},
		"struct": {"(yu)", []any{[]any{byte(9), uint32(3)}}, []byte{9, 0, 0, 0, 3, 0, 0, 0}},
	} {
		got, err := Encode(binary.LittleEndian, c.sig, c.vals...)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !bytes.Equal(got, c.want) {
			t.Errorf("%s: % x, want % x", name, got, c.want)
		}
		back, err := Decode(binary.LittleEndian, c.sig, got)
		if err != nil || len(back) != len(c.vals) {
			t.Errorf("%s: decode: %v %v", name, back, err)
		}
	}
}

func TestDecodeRejectsMalformed(t *testing.T) {
	for name, b := range map[string][]byte{
		"truncated string":   {5, 0, 0, 0, 'a'},
		"no terminator":      {1, 0, 0, 0, 'a', 'b'},
		"huge array":         {0xff, 0xff, 0xff, 0x7f},
		"array past the end": {8, 0, 0, 0, 1},
	} {
		if _, err := Decode(binary.LittleEndian, "as", b); err == nil {
			if _, err := Decode(binary.LittleEndian, "s", b); err == nil {
				t.Errorf("%s: accepted", name)
			}
		}
	}
	if _, err := Encode(binary.LittleEndian, "s", "a\x00b"); err == nil {
		t.Error("NUL in a string must be refused")
	}
	if _, err := Encode(binary.LittleEndian, "su", "a"); err == nil {
		t.Error("missing values must be refused")
	}
	if _, _, err := splitType("a"); err == nil {
		t.Error("incomplete signature")
	}
}

func TestVariantAndNestedRoundTrip(t *testing.T) {
	in := map[string]Variant{"urgency": {"y", byte(2)}, "name": {"s", "x"}, "ok": {"b", false}, "n": {"i", int32(-4)}}
	b := enc(t, "a{sv}", in)
	out, err := Decode(binary.LittleEndian, "a{sv}", b)
	if err != nil {
		t.Fatal(err)
	}
	m := out[0].(map[any]any)
	if m["urgency"].(Variant).Value != byte(2) || m["name"].(Variant).Value != "x" || m["n"].(Variant).Value != int32(-4) || m["ok"].(Variant).Value != false {
		t.Fatalf("%v", m)
	}
	b = enc(t, "a(sv)", []any{Struct{"a", Variant{"u", uint32(1)}}, Struct{"b", Variant{"s", "z"}}})
	out, err = Decode(binary.LittleEndian, "a(sv)", b)
	if err != nil || len(out[0].([]any)) != 2 {
		t.Fatalf("%v %v", out, err)
	}
}

// A client talks to a server over an in-memory pipe, both using this package: authentication, serials,
// replies, errors, signals.
func TestPeerCallsErrorsAndSignals(t *testing.T) {
	a, b := net.Pipe()
	type res struct {
		c   *Conn
		err error
	}
	srvc := make(chan res, 1)
	go func() {
		c, err := NewConn(b, ServerSide())
		srvc <- res{c, err}
	}()
	cl, err := NewConn(a, Peer())
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	sr := <-srvc
	if sr.err != nil {
		t.Fatal(sr.err)
	}
	defer sr.c.Close()
	sr.c.Serve(func(m *Message) (string, []any, *Error) {
		switch m.Member {
		case "Echo":
			return "su", []any{m.Body[0], uint32(len(m.Body[0].(string)))}, nil
		}
		return "", nil, &Error{"org.example.Error.Nope", "no " + m.Member}
	})
	signals := make(chan *Message, 2)
	cl.OnSignal(func(m *Message) { signals <- m })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	m, err := cl.Call(ctx, "", "/o", "org.example.I", "Echo", "s", "hello")
	if err != nil || m.Body[0] != "hello" || m.Body[1] != uint32(5) {
		t.Fatalf("%+v %v", m, err)
	}
	_, err = cl.Call(ctx, "", "/o", "org.example.I", "Missing", "")
	if e, ok := err.(*Error); !ok || e.Name != "org.example.Error.Nope" || e.Msg != "no Missing" {
		t.Fatalf("err = %v", err)
	}
	if err := sr.c.Signal("", "/o", "org.example.I", "Ping", "us", uint32(3), "x"); err != nil {
		t.Fatal(err)
	}
	select {
	case s := <-signals:
		if s.Member != "Ping" || s.Interface != "org.example.I" || s.Path != "/o" || s.Body[0] != uint32(3) || s.Body[1] != "x" {
			t.Fatalf("%+v", s)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no signal")
	}
	// closing ends pending calls
	sr.c.Serve(func(*Message) (string, []any, *Error) { time.Sleep(time.Hour); return "", nil, nil })
	go func() { time.Sleep(50 * time.Millisecond); sr.c.Close() }()
	if _, err := cl.Call(ctx, "", "/o", "i", "Hang", ""); err == nil {
		t.Fatal("a call must fail when the connection closes")
	}
}

// The same code against a real dbus-daemon, and an independent client (dbus-send) calling a service
// implemented here. Skipped when the daemon is not installed.
func startDaemon(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skip("dbus-daemon is not installed")
	}
	cmd := exec.Command(bin, "--session", "--nofork", "--print-address")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Env = append(os.Environ(), "DBUS_SESSION_BUS_ADDRESS=")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start dbus-daemon: %v", err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "unix:") {
		t.Skipf("dbus-daemon printed %q (%v)", line, err)
	}
	return strings.TrimSpace(line)
}

func TestRealDaemonRoundTripAndDBusSend(t *testing.T) {
	addr := startDaemon(t)
	svc, err := Dial(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	if !strings.HasPrefix(svc.UniqueName(), ":1.") {
		t.Fatalf("unique name %q", svc.UniqueName())
	}
	svc.Serve(func(m *Message) (string, []any, *Error) {
		if m.Member != "Join" || m.Signature != "sas" {
			return "", nil, &Error{"org.example.Error.Bad", m.Member + " " + m.Signature}
		}
		parts := []string{m.Body[0].(string)}
		for _, p := range m.Body[1].([]any) {
			parts = append(parts, p.(string))
		}
		return "s", []any{strings.Join(parts, "+")}, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r, err := svc.Call(ctx, "org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus", "RequestName", "su", "org.sweef.Test", uint32(4))
	if err != nil || r.Body[0] != uint32(1) {
		t.Fatalf("RequestName: %v %v", r, err)
	}
	cl, err := Dial(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	m, err := cl.Call(ctx, "org.sweef.Test", "/org/sweef/Test", "org.sweef.Test", "Join", "sas", "a", []string{"b", "c"})
	if err != nil || m.Body[0] != "a+b+c" {
		t.Fatalf("Join: %v %v", m, err)
	}
	if _, err := cl.Call(ctx, "org.sweef.Test", "/org/sweef/Test", "org.sweef.Test", "Nope", ""); err == nil {
		t.Fatal("expected an error reply")
	}
	// signals through the bus
	got := make(chan *Message, 1)
	cl.OnSignal(func(m *Message) { got <- m })
	if err := cl.AddMatch(ctx, "type='signal',interface='org.sweef.Test'"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Signal("", "/org/sweef/Test", "org.sweef.Test", "Event", "us", uint32(7), "s"); err != nil {
		t.Fatal(err)
	}
	select {
	case s := <-got:
		if s.Member != "Event" || s.Body[0] != uint32(7) {
			t.Fatalf("%+v", s)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no signal through the bus")
	}
	// an independent implementation (libdbus tooling) calls the service
	if bin, err := exec.LookPath("dbus-send"); err == nil {
		cmd := exec.Command(bin, "--session", "--print-reply", "--dest=org.sweef.Test", "/org/sweef/Test", "org.sweef.Test.Join", "string:x", "array:string:y,z")
		cmd.Env = append(os.Environ(), "DBUS_SESSION_BUS_ADDRESS="+addr)
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), `"x+y+z"`) {
			t.Fatalf("dbus-send: %v\n%s", err, out)
		}
	}
}
