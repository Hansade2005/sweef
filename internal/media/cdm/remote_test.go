package cdm

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// backendEngine is the fake CDM of the tests: an Engine (the Go-side boundary the helper process serves) that
// is implemented by an in-process Clear Key backend. It stands in for the dlopen'ed Widevine library, which
// cannot exist in this sandbox. It reports events the way a real CDM does: the first message is sent before
// CreateSession returns, key status changes before UpdateSession returns.
type backendEngine struct {
	b    Backend
	sink EventSink

	// individualize makes the first request an individualization request, answered by a license request after the
	// first Update (the shape Widevine has on a machine without a device certificate).
	individualize bool

	mu       sync.Mutex
	sessions map[string]Session
	init     InitParams
	closed   bool
	n        int
	pendingL map[string][]byte // session -> init data waiting for individualization
}

func newBackendEngine() *backendEngine {
	return &backendEngine{b: NewClearKey(), sessions: map[string]Session{}, pendingL: map[string][]byte{}}
}

func (e *backendEngine) Info() EngineInfo {
	return EngineInfo{KeySystem: ClearKeySystem, CDMVersion: "fake-1.0", InterfaceVersion: 10, Mode: ModeDecrypt}
}
func (e *backendEngine) SetSink(s EventSink) { e.sink = s }
func (e *backendEngine) Initialize(ctx context.Context, p InitParams) error {
	e.mu.Lock()
	e.init = p
	e.mu.Unlock()
	return nil
}
func (e *backendEngine) SetServerCertificate(ctx context.Context, cert []byte) error {
	return ErrUnsupported
}
func (e *backendEngine) CreateSession(ctx context.Context, typ SessionType, initDataType string, initData []byte) (string, error) {
	s, err := e.b.CreateSession(ctx, typ)
	if err != nil {
		return "", err
	}
	msg, err := s.GenerateRequest(ctx, initDataType, initData)
	if err != nil {
		return "", err
	}
	id := s.ID()
	e.mu.Lock()
	e.sessions[id] = s
	ind := e.individualize
	if ind {
		e.pendingL[id] = msg.Data
	}
	e.mu.Unlock()
	if ind {
		// message first, as a real CDM may: it overtakes our reply on the wire
		e.sink.SessionMessage(id, Message{Type: MessageIndividualizationReq, Data: []byte("provision-me")})
	} else {
		e.sink.SessionMessage(id, msg)
	}
	return id, nil
}
func (e *backendEngine) LoadSession(ctx context.Context, typ SessionType, id string) (bool, error) {
	return false, nil
}
func (e *backendEngine) session(id string) (Session, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := e.sessions[id]
	if s == nil {
		return nil, Errorf(ExceptionInvalidStateError, "unknown session %q", id)
	}
	return s, nil
}
func (e *backendEngine) UpdateSession(ctx context.Context, id string, response []byte) error {
	s, err := e.session(id)
	if err != nil {
		return err
	}
	e.mu.Lock()
	lic, waiting := e.pendingL[id]
	delete(e.pendingL, id)
	e.mu.Unlock()
	if waiting {
		// the response was a device certificate; now the real license request follows as an event
		e.sink.SessionMessage(id, Message{Type: MessageLicenseRequest, Data: lic})
		return nil
	}
	if err := s.Update(ctx, response); err != nil {
		return err
	}
	select {
	case ev := <-s.Events():
		if ev.Type == EventKeyStatusesChange {
			e.sink.KeyStatuses(id, ev.AdditionalUsableKey, ev.KeyStatuses)
			e.sink.Expiration(id, time.Unix(4102444800, 0)) // year 2100
		}
	case <-time.After(2 * time.Second):
		return errors.New("fake engine: no key status event")
	}
	return nil
}
func (e *backendEngine) CloseSession(ctx context.Context, id string) error {
	s, err := e.session(id)
	if err != nil {
		return err
	}
	_ = s.Close(ctx)
	e.sink.SessionClosed(id)
	return nil
}
func (e *backendEngine) RemoveSession(ctx context.Context, id string) error {
	return Errorf(ExceptionInvalidStateError, "not a persistent session")
}
func (e *backendEngine) Decrypt(ctx context.Context, s *Sample) ([]byte, error) {
	return e.b.Decrypt(ctx, s)
}
func (e *backendEngine) Close() error {
	e.mu.Lock()
	e.closed = true
	e.mu.Unlock()
	return e.b.Close()
}

// connect starts Serve on one end of a pipe and returns the Remote on the other.
func connect(t *testing.T, eng Engine) (*Remote, chan error) {
	t.Helper()
	c, s := net.Pipe()
	served := make(chan error, 1)
	go func() { served <- Serve(context.Background(), s, eng); s.Close() }()
	r, err := NewRemote(context.Background(), c, InitParams{Origin: "https://example.com", PersistentState: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r, served
}

func TestRemoteClearKeyOverProtocol(t *testing.T) {
	eng := newBackendEngine()
	r, served := connect(t, eng)
	ctx := context.Background()

	if r.KeySystem() != ClearKeySystem || r.Mode() != ModeDecrypt || r.Info().CDMVersion != "fake-1.0" || r.Info().InterfaceVersion != 10 {
		t.Fatalf("info %+v", r.Info())
	}
	eng.mu.Lock()
	if eng.init.Origin != "https://example.com" || !eng.init.PersistentState {
		t.Fatalf("Initialize params did not arrive: %+v", eng.init)
	}
	eng.mu.Unlock()
	if ok, err := r.SetServerCertificate(ctx, []byte("c")); ok || err != nil {
		t.Fatalf("SetServerCertificate: %v %v (an unsupported CDM resolves false)", ok, err)
	}

	kid, key := randBytes(16), randBytes(16)
	s, err := r.CreateSession(ctx, SessionTemporary)
	if err != nil {
		t.Fatal(err)
	}
	initData := []byte(`{"kids":["` + b64.EncodeToString(kid) + `"]}`)
	msg, err := s.GenerateRequest(ctx, "keyids", initData)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Type != MessageLicenseRequest || !bytes.Contains(msg.Data, []byte(b64.EncodeToString(kid))) {
		t.Fatalf("first message %q %s", msg.Type, msg.Data)
	}
	if s.ID() == "" {
		t.Fatal("no session id")
	}
	if err := s.Update(ctx, clearKeyLicense(kid, key)); err != nil {
		t.Fatal(err)
	}
	// Events that the CDM reported while handling Update are visible as soon as Update returns.
	if ks := s.KeyStatuses(); len(ks) != 1 || ks[0].Status != KeyUsable || !bytes.Equal(ks[0].KeyID, kid) {
		t.Fatalf("key statuses right after Update: %+v", ks)
	}
	if want := time.Unix(4102444800, 0); !s.Expiration().Equal(want) {
		t.Fatalf("expiration %v want %v", s.Expiration(), want)
	}
	var sawKeys, sawExp bool
	for i := 0; i < 2; i++ {
		select {
		case ev := <-s.Events():
			switch ev.Type {
			case EventKeyStatusesChange:
				sawKeys = ev.AdditionalUsableKey && len(ev.KeyStatuses) == 1
			case EventExpirationChange:
				sawExp = !ev.Expiration.IsZero()
			}
		case <-time.After(2 * time.Second):
			t.Fatal("missing events")
		}
	}
	if !sawKeys || !sawExp {
		t.Fatalf("events: keys=%v expiration=%v", sawKeys, sawExp)
	}

	// Decrypt through the protocol, both schemes, with subsamples.
	plain := randBytes(3000)
	subs := []Subsample{{12, 700}, {0, 1000}, {5, 1283}}
	for _, c := range []struct {
		scheme Scheme
		iv     []byte
		pat    Pattern
	}{{SchemeCenc, randBytes(8), Pattern{}}, {SchemeCbcs, randBytes(16), Pattern{1, 9}}} {
		enc := encryptRef(t, key, c.scheme, c.iv, c.pat, subs, plain)
		got, err := r.Decrypt(ctx, &Sample{Data: enc, KeyID: kid, IV: c.iv, Subsamples: subs, Scheme: c.scheme, Pattern: c.pat, TimestampUS: 99})
		if err != nil {
			t.Fatalf("%s: %v", c.scheme, err)
		}
		if !bytes.Equal(got, plain) {
			t.Fatalf("%s: decrypted data differs", c.scheme)
		}
	}
	// typed errors survive the wire
	if _, err := r.Decrypt(ctx, &Sample{Data: plain, KeyID: randBytes(16), IV: randBytes(8), Scheme: SchemeCenc}); !errors.Is(err, ErrNoKey) {
		t.Fatalf("no key: %v", err)
	}
	if _, err := r.Decrypt(ctx, &Sample{Data: plain, KeyID: kid, IV: randBytes(3), Scheme: SchemeCenc}); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("bad iv: %v", err)
	}
	var ce *Error
	if err := s.Remove(ctx); !errors.As(err, &ce) || ce.Exception != ExceptionInvalidStateError {
		t.Fatalf("Remove: %v", err)
	}

	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	closed := false
	for ev := range s.Events() {
		if ev.Type == EventClosed {
			closed = true
		}
	}
	if !closed {
		t.Fatal("no closed event")
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after Shutdown")
	}
	eng.mu.Lock()
	defer eng.mu.Unlock()
	if !eng.closed {
		t.Fatal("engine not closed when Serve ended")
	}
}

func TestRemoteIndividualizationFirstThenLicenseRequest(t *testing.T) {
	eng := newBackendEngine()
	eng.individualize = true
	r, _ := connect(t, eng)
	ctx := context.Background()
	kid := randBytes(16)
	s, _ := r.CreateSession(ctx, SessionTemporary)
	msg, err := s.GenerateRequest(ctx, "keyids", []byte(`{"kids":["`+b64.EncodeToString(kid)+`"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if msg.Type != MessageIndividualizationReq || string(msg.Data) != "provision-me" {
		t.Fatalf("first message %q %q", msg.Type, msg.Data)
	}
	if err := s.Update(ctx, []byte("device certificate")); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-s.Events():
		if ev.Type != EventMessage || ev.Message.Type != MessageLicenseRequest {
			t.Fatalf("event %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the license request after provisioning never arrived")
	}
}

func TestRemoteHelperDeathFailsEverything(t *testing.T) {
	eng := newBackendEngine()
	c, s := net.Pipe()
	go Serve(context.Background(), s, eng)
	r, err := NewRemote(context.Background(), c, InitParams{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx := context.Background()
	kid := randBytes(16)
	sess, _ := r.CreateSession(ctx, SessionTemporary)
	if _, err := sess.GenerateRequest(ctx, "keyids", []byte(`{"kids":["`+b64.EncodeToString(kid)+`"]}`)); err != nil {
		t.Fatal(err)
	}
	s.Close() // the helper process dies
	deadline := time.After(2 * time.Second)
	for {
		_, err := r.Decrypt(ctx, &Sample{Data: []byte{1}, Scheme: SchemeNone})
		if errors.Is(err, ErrHelperDied) {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("still alive: %v", err)
		case <-time.After(5 * time.Millisecond):
		}
	}
	closed := false
	for ev := range sess.Events() {
		closed = closed || ev.Type == EventClosed
	}
	if !closed {
		t.Fatal("sessions must be closed when the helper dies")
	}
	if _, err := r.CreateSession(ctx, SessionTemporary); !errors.Is(err, ErrHelperDied) {
		t.Fatalf("CreateSession after death: %v", err)
	}
}

func TestRemoteContextCancel(t *testing.T) {
	eng := &slowEngine{backendEngine: newBackendEngine(), release: make(chan struct{})}
	r, _ := connect(t, eng)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := r.Decrypt(ctx, &Sample{Data: []byte{1}})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	close(eng.release)
}

type slowEngine struct {
	*backendEngine
	release chan struct{}
}

func (e *slowEngine) Decrypt(ctx context.Context, s *Sample) ([]byte, error) {
	<-e.release
	return s.Data, nil
}

func TestServeRejectsProtocolMismatch(t *testing.T) {
	c, s := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- Serve(context.Background(), s, newBackendEngine()) }()
	if err := WriteFrame(c, Frame{Type: TypeHello, ID: 1, Payload: []byte(`{"magic":"SWEEF-CDM","version":99}`)}); err != nil {
		t.Fatal(err)
	}
	f, err := ReadFrame(c)
	if err != nil || f.Type != TypeErr || f.ID != 1 {
		t.Fatalf("%+v %v", f, err)
	}
	if err := WriteFrame(c, Frame{Type: FrameType(77), ID: 2}); err != nil {
		t.Fatal(err)
	}
	f, err = ReadFrame(c)
	if err != nil || f.Type != TypeErr || f.ID != 2 {
		t.Fatalf("unknown request type: %+v %v", f, err)
	}
	c.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// ---- framing ----

func TestFrameRoundTripAndCorruption(t *testing.T) {
	var buf bytes.Buffer
	frames := []Frame{
		{TypeHello, 1, []byte(`{}`)},
		{TypeDecrypt, 0xdeadbeef, randBytes(100000)},
		{TypeShutdown, 7, nil},
		{TypeEventClosed, 0, []byte("x")},
	}
	for _, f := range frames {
		if err := WriteFrame(&buf, f); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range frames {
		got, err := ReadFrame(&buf)
		if err != nil {
			t.Fatal(err)
		}
		if got.Type != want.Type || got.ID != want.ID || !bytes.Equal(got.Payload, want.Payload) {
			t.Fatalf("got %v/%d/%d bytes want %v/%d/%d bytes", got.Type, got.ID, len(got.Payload), want.Type, want.ID, len(want.Payload))
		}
	}
	if _, err := ReadFrame(&buf); err != io.EOF {
		t.Fatalf("clean end of stream: %v", err)
	}

	// byte-exact layout: length 5+3, type, id, payload
	buf.Reset()
	_ = WriteFrame(&buf, Frame{Type: TypeUpdateSession, ID: 0x01020304, Payload: []byte("abc")})
	want := []byte{0, 0, 0, 8, 6, 1, 2, 3, 4, 'a', 'b', 'c'}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Fatalf("wire bytes % x want % x", buf.Bytes(), want)
	}

	// truncated body
	if _, err := ReadFrame(bytes.NewReader(want[:10])); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("truncated body: %v", err)
	}
	// truncated length
	if _, err := ReadFrame(bytes.NewReader(want[:2])); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("truncated length: %v", err)
	}
	// length shorter than the header
	if _, err := ReadFrame(bytes.NewReader([]byte{0, 0, 0, 2, 1, 2})); err == nil {
		t.Fatal("frame shorter than its header accepted")
	}
	// absurd length must not allocate
	huge := make([]byte, 4)
	binary.BigEndian.PutUint32(huge, MaxFrameSize+1)
	if _, err := ReadFrame(bytes.NewReader(huge)); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("oversized frame: %v", err)
	}
	if err := WriteFrame(io.Discard, Frame{Payload: make([]byte, MaxFrameSize)}); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("oversized write: %v", err)
	}
}

func TestSampleAndErrorEncoding(t *testing.T) {
	in := &Sample{Data: randBytes(300), KeyID: randBytes(16), IV: randBytes(16), Scheme: SchemeCbcs, Pattern: Pattern{1, 9},
		Subsamples: []Subsample{{1, 2}, {3, 4}}, TimestampUS: -5}
	b, err := encodeSample(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := decodeSample(b)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Data, in.Data) || !bytes.Equal(out.KeyID, in.KeyID) || !bytes.Equal(out.IV, in.IV) || out.Scheme != in.Scheme ||
		out.Pattern != in.Pattern || len(out.Subsamples) != 2 || out.Subsamples[1] != in.Subsamples[1] || out.TimestampUS != -5 {
		t.Fatalf("sample changed in transit: %+v", out)
	}
	for _, bad := range [][]byte{nil, {0, 0}, {0, 0, 0, 99, '{'}, {0, 0, 0, 2, 'x', 'y'}} {
		if _, err := decodeSample(bad); err == nil {
			t.Errorf("decodeSample(%v) accepted", bad)
		}
	}

	for _, e := range []error{
		ErrNoKey, ErrNeedMoreData, ErrClosed, ErrDecrypt, ErrDecode, ErrUnsupported,
		&Error{Exception: ExceptionQuotaExceeded, SystemCode: 7, Message: "full"},
		wrapped(ErrDecrypt, "bad block"), wrapped(ErrUnsupported, "no cbc1"),
	} {
		got := decodeError(encodeError(e))
		var a, b *Error
		switch {
		case errors.As(e, &a):
			if !errors.As(got, &b) || *a != *b {
				t.Errorf("%v became %v", e, got)
			}
		default:
			if !errors.Is(got, errors.Unwrap(e)) && !errors.Is(got, e) {
				t.Errorf("%v became %v", e, got)
			}
			if got.Error() != e.Error() {
				t.Errorf("message %q became %q", e.Error(), got.Error())
			}
		}
	}
	if err := decodeError([]byte("garbage")); err == nil {
		t.Fatal("garbage decoded to nil")
	}
	if err := decodeError(encodeError(errors.New("boom"))); err == nil || err.Error() != "cdm: boom" {
		t.Fatalf("%v", err)
	}
	if TypeCreateSession.String() != "CreateSession" || FrameType(200).String() != "FrameType(200)" {
		t.Fatal("FrameType.String")
	}
}

func wrapped(sentinel error, msg string) error { return &wrapErr{sentinel, msg} }

type wrapErr struct {
	s   error
	msg string
}

func (w *wrapErr) Error() string { return w.s.Error() + ": " + w.msg }
func (w *wrapErr) Unwrap() error { return w.s }
