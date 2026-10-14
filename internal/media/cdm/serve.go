package cdm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

// Engine is the helper-side boundary of the CDM host protocol: one CDM instance, shaped like the promise based
// CDM C++ API (cdm::ContentDecryptionModule_N) but with the promises turned into return values. The Widevine
// engine (internal/media/cdm/widevinehost) implements it on top of a dlopen'ed CDM; the tests implement it with an
// in-process fake. Serve connects an Engine to a protocol stream.
//
// An Engine must be safe for concurrent use. Events (SetSink) may be delivered from any goroutine and must be
// delivered, in CDM order, before the call that caused them returns.
type Engine interface {
	Info() EngineInfo
	Initialize(ctx context.Context, p InitParams) error
	SetServerCertificate(ctx context.Context, cert []byte) error
	// CreateSession creates a session and generates the request; the first message is delivered as an event.
	CreateSession(ctx context.Context, typ SessionType, initDataType string, initData []byte) (sessionID string, err error)
	LoadSession(ctx context.Context, typ SessionType, sessionID string) (found bool, err error)
	UpdateSession(ctx context.Context, sessionID string, response []byte) error
	CloseSession(ctx context.Context, sessionID string) error
	RemoveSession(ctx context.Context, sessionID string) error
	Decrypt(ctx context.Context, s *Sample) ([]byte, error)
	SetSink(EventSink)
	Close() error
}

// EventSink receives what the CDM reports on its own initiative (the cdm::Host callbacks).
type EventSink interface {
	SessionMessage(sessionID string, m Message)
	KeyStatuses(sessionID string, additionalUsableKey bool, keys []KeyStatus)
	Expiration(sessionID string, t time.Time)
	SessionClosed(sessionID string)
}

// Serve runs the helper side of the protocol on rw until the client disconnects, sends Shutdown, or ctx is done.
// It closes the engine before returning. The clean end of the stream returns nil.
func Serve(ctx context.Context, rw io.ReadWriter, eng Engine) error {
	s := &server{rw: rw, eng: eng}
	eng.SetSink(s)
	defer eng.Close()
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	defer func() {
		cancel() // in-flight requests see a cancelled context
		wg.Wait()
	}()
	// If ctx ends while a read is pending the read only returns when the caller closes the stream.
	for {
		f, err := ReadFrame(rw)
		if err != nil {
			if errors.Is(err, io.EOF) || ctx.Err() != nil {
				return nil
			}
			return err
		}
		if f.Type == TypeShutdown {
			s.reply(f.ID, nil, nil)
			return nil
		}
		if f.Type == TypeHello {
			// answered inline so it is the first frame on the wire
			s.handle(ctx, f)
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.handle(ctx, f)
		}()
	}
}

type server struct {
	rw  io.ReadWriter
	eng Engine
	wmu sync.Mutex
}

func (s *server) write(f Frame) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_ = WriteFrame(s.rw, f)
}

func (s *server) reply(id uint32, payload any, err error) {
	if err != nil {
		s.write(Frame{Type: TypeErr, ID: id, Payload: encodeError(err)})
		return
	}
	var b []byte
	switch v := payload.(type) {
	case nil:
	case []byte:
		b = v
	default:
		b, err = json.Marshal(v)
		if err != nil {
			s.write(Frame{Type: TypeErr, ID: id, Payload: encodeError(err)})
			return
		}
	}
	s.write(Frame{Type: TypeOK, ID: id, Payload: b})
}

func (s *server) handle(ctx context.Context, f Frame) {
	var err error
	fail := func(e error) { err = e }
	decode := func(v any) bool {
		if e := json.Unmarshal(f.Payload, v); e != nil {
			fail(Errorf(ExceptionTypeError, "bad %s payload: %v", f.Type, e))
			return false
		}
		return true
	}
	switch f.Type {
	case TypeHello:
		var h helloReq
		if !decode(&h) {
			break
		}
		if h.Magic != protocolMagic || h.Version != ProtocolVersion {
			fail(fmt.Errorf("cdm: protocol mismatch (client %q v%d, host v%d)", h.Magic, h.Version, ProtocolVersion))
			break
		}
		s.reply(f.ID, helloResp{Version: ProtocolVersion, EngineInfo: s.eng.Info()}, nil)
		return
	case TypeInitialize:
		var p InitParams
		if !decode(&p) {
			break
		}
		err = s.eng.Initialize(ctx, p)
	case TypeSetServerCertificate:
		var r certReq
		if !decode(&r) {
			break
		}
		err = s.eng.SetServerCertificate(ctx, r.Cert)
		if errors.Is(err, ErrUnsupported) {
			s.reply(f.ID, supportedResp{Supported: false}, nil)
			return
		}
		if err == nil {
			s.reply(f.ID, supportedResp{Supported: true}, nil)
			return
		}
	case TypeCreateSession:
		var r createReq
		if !decode(&r) {
			break
		}
		var id string
		id, err = s.eng.CreateSession(ctx, r.Type, r.InitDataType, r.InitData)
		if err == nil {
			s.reply(f.ID, sessionResp{SessionID: id}, nil)
			return
		}
	case TypeLoadSession:
		var r loadReq
		if !decode(&r) {
			break
		}
		var found bool
		found, err = s.eng.LoadSession(ctx, r.Type, r.SessionID)
		if err == nil {
			s.reply(f.ID, foundResp{Found: found}, nil)
			return
		}
	case TypeUpdateSession:
		var r updateReq
		if !decode(&r) {
			break
		}
		err = s.eng.UpdateSession(ctx, r.SessionID, r.Response)
	case TypeCloseSession:
		var r sessionReq
		if !decode(&r) {
			break
		}
		err = s.eng.CloseSession(ctx, r.SessionID)
	case TypeRemoveSession:
		var r sessionReq
		if !decode(&r) {
			break
		}
		err = s.eng.RemoveSession(ctx, r.SessionID)
	case TypeDecrypt:
		var smp *Sample
		smp, err = decodeSample(f.Payload)
		if err != nil {
			err = fmt.Errorf("%w: %v", ErrDecrypt, err)
			break
		}
		var out []byte
		out, err = s.eng.Decrypt(ctx, smp)
		if err == nil {
			s.reply(f.ID, out, nil)
			return
		}
	default:
		err = fmt.Errorf("cdm: unknown request type %s", f.Type)
	}
	s.reply(f.ID, nil, err)
}

// EventSink implementation: events go straight to the wire, in call order.

func (s *server) SessionMessage(id string, m Message) {
	b, _ := json.Marshal(eventMessageP{SessionID: id, Type: m.Type, Data: m.Data})
	s.write(Frame{Type: TypeEventMessage, Payload: b})
}

func (s *server) KeyStatuses(id string, additional bool, keys []KeyStatus) {
	p := eventKeysP{SessionID: id, Additional: additional, Keys: []keyJSON{}}
	for _, k := range keys {
		p.Keys = append(p.Keys, keyJSON{KeyID: k.KeyID, Status: k.Status, SystemCode: k.SystemCode})
	}
	b, _ := json.Marshal(p)
	s.write(Frame{Type: TypeEventKeys, Payload: b})
}

func (s *server) Expiration(id string, t time.Time) {
	b, _ := json.Marshal(eventExpirationP{SessionID: id, UnixMilli: timeToMilli(t)})
	s.write(Frame{Type: TypeEventExpiration, Payload: b})
}

func (s *server) SessionClosed(id string) {
	b, _ := json.Marshal(eventClosedP{SessionID: id})
	s.write(Frame{Type: TypeEventClosed, Payload: b})
}
