package cdm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// Remote is a Backend whose CDM lives in a helper process; it speaks the framed protocol of wire.go over rwc.
// The Widevine key system is a Remote that talks to cmd/sweef-cdm.
type Remote struct {
	rwc     io.ReadWriteCloser
	info    EngineInfo
	onClose func() error

	wmu    sync.Mutex
	nextID atomic.Uint32

	mu       sync.Mutex
	pending  map[uint32]*rcall
	sessions map[string]*remoteSession
	orphans  map[string][]Event // events for sessions the client has not registered yet
	dead     error
	closed   bool
	done     chan struct{} // closed when the read loop ended
}

type rcall struct {
	ch   chan rresult
	onOK func([]byte) // runs on the read loop, before any later frame is processed
}

type rresult struct {
	payload []byte
	err     error
}

const (
	maxOrphanSessions = 256
	maxOrphanEvents   = 64
)

// NewRemote performs the Hello and Initialize exchange on rwc and returns the backend. onClose (may be nil) runs
// from Close after the stream is closed; it reaps the helper process.
func NewRemote(ctx context.Context, rwc io.ReadWriteCloser, init InitParams, onClose func() error) (*Remote, error) {
	r := &Remote{
		rwc: rwc, onClose: onClose,
		pending:  map[uint32]*rcall{},
		sessions: map[string]*remoteSession{},
		orphans:  map[string][]Event{},
		done:     make(chan struct{}),
	}
	go r.readLoop()
	hello, _ := json.Marshal(helloReq{Magic: protocolMagic, Version: ProtocolVersion, Client: "sweef"})
	var hr helloResp
	if _, err := r.rpc(ctx, TypeHello, hello, func(p []byte) { _ = json.Unmarshal(p, &hr) }); err != nil {
		r.Close()
		return nil, fmt.Errorf("cdm: host handshake: %w", err)
	}
	if hr.Version != ProtocolVersion {
		r.Close()
		return nil, fmt.Errorf("cdm: host speaks protocol v%d, want v%d", hr.Version, ProtocolVersion)
	}
	r.info = hr.EngineInfo
	ib, _ := json.Marshal(init)
	if _, err := r.rpc(ctx, TypeInitialize, ib, nil); err != nil {
		r.Close()
		return nil, fmt.Errorf("cdm: initializing %s: %w", hr.KeySystem, err)
	}
	return r, nil
}

// Info is what the helper reported about its CDM (version, interface version, ...).
func (r *Remote) Info() EngineInfo { return r.info }

func (r *Remote) KeySystem() string { return r.info.KeySystem }
func (r *Remote) Mode() DecryptMode {
	if r.info.Mode == 0 {
		return ModeDecrypt
	}
	return r.info.Mode
}

func (r *Remote) readLoop() {
	defer close(r.done)
	var err error
	for {
		var f Frame
		f, err = ReadFrame(r.rwc)
		if err != nil {
			break
		}
		switch f.Type {
		case TypeOK, TypeErr:
			r.mu.Lock()
			c := r.pending[f.ID]
			delete(r.pending, f.ID)
			r.mu.Unlock()
			if c == nil {
				continue // the caller gave up (context)
			}
			if f.Type == TypeErr {
				c.ch <- rresult{err: decodeError(f.Payload)}
				continue
			}
			if c.onOK != nil {
				c.onOK(f.Payload)
			}
			c.ch <- rresult{payload: f.Payload}
		default:
			r.dispatchEvent(f)
		}
	}
	r.fail(err)
}

// fail ends the connection: pending calls fail, sessions close.
func (r *Remote) fail(cause error) {
	r.mu.Lock()
	if r.dead == nil {
		if cause == nil || errors.Is(cause, io.EOF) || r.closed {
			cause = ErrHelperDied
			if r.closed {
				cause = ErrClosed
			}
		} else {
			cause = fmt.Errorf("%w: %v", ErrHelperDied, cause)
		}
		r.dead = cause
	}
	dead := r.dead
	pend := r.pending
	r.pending = map[uint32]*rcall{}
	sess := r.sessions
	r.sessions = map[string]*remoteSession{}
	r.mu.Unlock()
	for _, c := range pend {
		c.ch <- rresult{err: dead}
	}
	for _, s := range sess {
		s.end(false)
	}
}

func (r *Remote) rpc(ctx context.Context, t FrameType, payload []byte, onOK func([]byte)) ([]byte, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultCallTimeout)
		defer cancel()
	}
	id := r.nextID.Add(1)
	c := &rcall{ch: make(chan rresult, 1), onOK: onOK}
	r.mu.Lock()
	if r.dead != nil {
		err := r.dead
		r.mu.Unlock()
		return nil, err
	}
	r.pending[id] = c
	r.mu.Unlock()
	r.wmu.Lock()
	err := WriteFrame(r.rwc, Frame{Type: t, ID: id, Payload: payload})
	r.wmu.Unlock()
	if err != nil {
		r.mu.Lock()
		delete(r.pending, id)
		r.mu.Unlock()
		return nil, fmt.Errorf("%w: %v", ErrHelperDied, err)
	}
	select {
	case res := <-c.ch:
		return res.payload, res.err
	case <-ctx.Done():
		r.mu.Lock()
		delete(r.pending, id)
		r.mu.Unlock()
		return nil, ctx.Err()
	}
}

func (r *Remote) SetServerCertificate(ctx context.Context, cert []byte) (bool, error) {
	b, _ := json.Marshal(certReq{Cert: cert})
	var resp supportedResp
	if _, err := r.rpc(ctx, TypeSetServerCertificate, b, func(p []byte) { _ = json.Unmarshal(p, &resp) }); err != nil {
		return false, err
	}
	return resp.Supported, nil
}

func (r *Remote) CreateSession(ctx context.Context, typ SessionType) (Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dead != nil {
		return nil, r.dead
	}
	return &remoteSession{r: r, typ: typ, q: NewEventQueue(), first: make(chan Message, 1)}, nil
}

func (r *Remote) Decrypt(ctx context.Context, s *Sample) ([]byte, error) {
	p, err := encodeSample(s)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDecrypt, err)
	}
	return r.rpc(ctx, TypeDecrypt, p, nil)
}

// Close shuts the helper down and closes every session. It is safe to call more than once.
func (r *Remote) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		<-r.done
		return nil
	}
	r.closed = true
	dead := r.dead != nil
	r.mu.Unlock()
	if !dead {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, _ = r.rpc(ctx, TypeShutdown, nil, nil)
		cancel()
	}
	_ = r.rwc.Close()
	<-r.done
	var err error
	if r.onClose != nil {
		err = r.onClose()
	}
	return err
}

// ---- events ----

func (r *Remote) dispatchEvent(f Frame) {
	switch f.Type {
	case TypeEventMessage:
		var p eventMessageP
		if json.Unmarshal(f.Payload, &p) == nil {
			r.deliver(p.SessionID, Event{Type: EventMessage, Message: Message{Type: p.Type, Data: p.Data}})
		}
	case TypeEventKeys:
		var p eventKeysP
		if json.Unmarshal(f.Payload, &p) == nil {
			ks := make([]KeyStatus, 0, len(p.Keys))
			for _, k := range p.Keys {
				ks = append(ks, KeyStatus{KeyID: k.KeyID, Status: k.Status, SystemCode: k.SystemCode})
			}
			r.deliver(p.SessionID, Event{Type: EventKeyStatusesChange, KeyStatuses: ks, AdditionalUsableKey: p.Additional})
		}
	case TypeEventExpiration:
		var p eventExpirationP
		if json.Unmarshal(f.Payload, &p) == nil {
			r.deliver(p.SessionID, Event{Type: EventExpirationChange, Expiration: milliToTime(p.UnixMilli)})
		}
	case TypeEventClosed:
		var p eventClosedP
		if json.Unmarshal(f.Payload, &p) == nil {
			r.deliver(p.SessionID, Event{Type: EventClosed})
		}
	}
}

// deliver routes an event to its session, or parks it until the session is registered (a CDM may report a
// message before the call that created the session returned).
func (r *Remote) deliver(sid string, ev Event) {
	r.mu.Lock()
	s := r.sessions[sid]
	if s == nil {
		if _, ok := r.orphans[sid]; ok || len(r.orphans) < maxOrphanSessions {
			if len(r.orphans[sid]) < maxOrphanEvents {
				r.orphans[sid] = append(r.orphans[sid], ev)
			}
		}
		r.mu.Unlock()
		return
	}
	r.mu.Unlock()
	s.handle(ev)
}

func (r *Remote) register(sid string, s *remoteSession) {
	r.mu.Lock()
	r.sessions[sid] = s
	parked := r.orphans[sid]
	delete(r.orphans, sid)
	r.mu.Unlock()
	for _, ev := range parked {
		s.handle(ev)
	}
}

func (r *Remote) unregister(sid string) {
	r.mu.Lock()
	delete(r.sessions, sid)
	delete(r.orphans, sid)
	r.mu.Unlock()
}

// ---- sessions ----

type remoteSession struct {
	r   *Remote
	typ SessionType
	q   *EventQueue

	mu          sync.Mutex
	id          string
	awaitFirst  bool
	first       chan Message
	keys        []KeyStatus
	expiration  time.Time
	closed      bool
	idAssigned  bool
	generateRan bool
}

func (s *remoteSession) ID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.id
}

func (s *remoteSession) Events() <-chan Event { return s.q.C() }

func (s *remoteSession) KeyStatuses() []KeyStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]KeyStatus, len(s.keys))
	copy(out, s.keys)
	return out
}

func (s *remoteSession) Expiration() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.expiration
}

// handle runs on the read loop.
func (s *remoteSession) handle(ev Event) {
	switch ev.Type {
	case EventMessage:
		s.mu.Lock()
		if s.awaitFirst {
			s.awaitFirst = false
			s.mu.Unlock()
			s.first <- ev.Message
			return
		}
		s.mu.Unlock()
	case EventKeyStatusesChange:
		s.mu.Lock()
		s.keys = append([]KeyStatus(nil), ev.KeyStatuses...)
		s.mu.Unlock()
	case EventExpirationChange:
		s.mu.Lock()
		s.expiration = ev.Expiration
		s.mu.Unlock()
	case EventClosed:
		s.end(true)
		return
	}
	s.q.Push(ev)
}

// end closes the local side (idempotent).
func (s *remoteSession) end(unregister bool) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	id := s.id
	s.mu.Unlock()
	if unregister && id != "" {
		s.r.unregister(id)
	}
	s.q.Push(Event{Type: EventClosed})
	s.q.Close()
}

func (s *remoteSession) GenerateRequest(ctx context.Context, initDataType string, initData []byte) (Message, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return Message{}, Errorf(ExceptionInvalidStateError, "session is closed")
	}
	if s.generateRan || s.idAssigned {
		s.mu.Unlock()
		return Message{}, Errorf(ExceptionInvalidStateError, "a request was already generated")
	}
	s.generateRan = true
	s.awaitFirst = true
	s.mu.Unlock()
	b, _ := json.Marshal(createReq{Type: s.typ, InitDataType: initDataType, InitData: initData})
	_, err := s.r.rpc(ctx, TypeCreateSession, b, func(p []byte) {
		var resp sessionResp
		if json.Unmarshal(p, &resp) != nil || resp.SessionID == "" {
			return
		}
		s.mu.Lock()
		s.id, s.idAssigned = resp.SessionID, true
		s.mu.Unlock()
		s.r.register(resp.SessionID, s)
	})
	if err != nil {
		s.mu.Lock()
		s.awaitFirst = false
		s.mu.Unlock()
		return Message{}, err
	}
	if s.ID() == "" {
		return Message{}, Errorf(ExceptionInvalidStateError, "the CDM host returned no session id")
	}
	wctx := ctx
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		wctx, cancel = context.WithTimeout(ctx, defaultCallTimeout)
		defer cancel()
	}
	select {
	case m := <-s.first:
		return m, nil
	case <-wctx.Done():
		return Message{}, wctx.Err()
	case <-s.r.done:
		return Message{}, s.r.deadErr()
	}
}

func (r *Remote) deadErr() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dead != nil {
		return r.dead
	}
	return ErrHelperDied
}

func (s *remoteSession) Load(ctx context.Context, sessionID string) (bool, error) {
	s.mu.Lock()
	if s.closed || s.idAssigned || s.generateRan {
		s.mu.Unlock()
		return false, Errorf(ExceptionInvalidStateError, "session is already in use")
	}
	s.generateRan = true
	s.mu.Unlock()
	b, _ := json.Marshal(loadReq{Type: s.typ, SessionID: sessionID})
	found := false
	_, err := s.r.rpc(ctx, TypeLoadSession, b, func(p []byte) {
		var resp foundResp
		if json.Unmarshal(p, &resp) != nil || !resp.Found {
			return
		}
		found = true
		s.mu.Lock()
		s.id, s.idAssigned = sessionID, true
		s.mu.Unlock()
		s.r.register(sessionID, s)
	})
	if err != nil {
		return false, err
	}
	return found, nil
}

func (s *remoteSession) call(ctx context.Context, t FrameType, payload any) error {
	if s.ID() == "" {
		return Errorf(ExceptionInvalidStateError, "the session has no id yet")
	}
	b, _ := json.Marshal(payload)
	_, err := s.r.rpc(ctx, t, b, nil)
	return err
}

func (s *remoteSession) Update(ctx context.Context, response []byte) error {
	return s.call(ctx, TypeUpdateSession, updateReq{SessionID: s.ID(), Response: response})
}

func (s *remoteSession) Remove(ctx context.Context) error {
	return s.call(ctx, TypeRemoveSession, sessionReq{SessionID: s.ID()})
}

func (s *remoteSession) Close(ctx context.Context) error {
	if s.ID() == "" {
		s.end(false)
		return nil
	}
	err := s.call(ctx, TypeCloseSession, sessionReq{SessionID: s.ID()})
	s.end(true)
	return err
}
