//go:build darwin

package fairplay

// UNVERIFIED: see the package comment.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"

	"github.com/sweefbrowser/sweef/internal/media/cdm"
)

var (
	loadOnce  sync.Once
	loadErr   error
	fpKeySys  objc.ID // AVContentKeySystemFairPlayStreaming (an NSString constant)
	newQueue  func(label string, attr uintptr) uintptr
	delegates objc.ID // the shared delegate object

	sessionsMu sync.Mutex
	sessions   = map[objc.ID]*backend{} // AVContentKeySession -> backend
)

func sel(name string) objc.SEL                         { return objc.RegisterName(name) }
func send(o objc.ID, name string, args ...any) objc.ID { return o.Send(sel(name), args...) }
func class(name string) objc.ID                        { return objc.ID(objc.GetClass(name)) }

func nsString(s string) objc.ID { return send(class("NSString"), "stringWithUTF8String:", s) }

func nsData(b []byte) objc.ID {
	if len(b) == 0 {
		return send(class("NSData"), "data")
	}
	return send(class("NSData"), "dataWithBytes:length:", uintptr(unsafe.Pointer(&b[0])), uint(len(b)))
}

func goBytes(d objc.ID) []byte {
	if d == 0 {
		return nil
	}
	n := objc.Send[uint](d, sel("length"))
	if n == 0 {
		return nil
	}
	p := objc.Send[uintptr](d, sel("bytes"))
	return append([]byte(nil), unsafe.Slice((*byte)(*(*unsafe.Pointer)(unsafe.Pointer(&p))), int(n))...)
}

func goString(ns objc.ID) string {
	if ns == 0 {
		return ""
	}
	p := objc.Send[uintptr](ns, sel("UTF8String"))
	if p == 0 {
		return ""
	}
	return string(goBytes(send(ns, "dataUsingEncoding:", uint(4)))) // NSUTF8StringEncoding
}

func load() error {
	loadOnce.Do(func() {
		av, err := purego.Dlopen("/System/Library/Frameworks/AVFoundation.framework/AVFoundation", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			loadErr = err
			return
		}
		if _, err := purego.Dlopen("/System/Library/Frameworks/Foundation.framework/Foundation", purego.RTLD_NOW|purego.RTLD_GLOBAL); err != nil {
			loadErr = err
			return
		}
		sym, err := purego.Dlsym(av, "AVContentKeySystemFairPlayStreaming")
		if err != nil {
			loadErr = fmt.Errorf("AVContentKeySystemFairPlayStreaming: %w", err)
			return
		}
		fpKeySys = objc.ID(*(*uintptr)(*(*unsafe.Pointer)(unsafe.Pointer(&sym)))) // the symbol is an NSString*
		if objc.GetClass("AVContentKeySession") == 0 {
			loadErr = errors.New("AVContentKeySession is not available on this macOS version")
			return
		}
		sys, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			loadErr = err
			return
		}
		purego.RegisterLibFunc(&newQueue, sys, "dispatch_queue_create")
		loadErr = registerDelegate()
	})
	return loadErr
}

// registerDelegate creates the AVContentKeySessionDelegate class: didProvideContentKeyRequest starts the SPC
// request, didFail reports the error.
func registerDelegate() error {
	cls, err := objc.RegisterClass("SweefContentKeyDelegate", objc.GetClass("NSObject"), nil, nil, []objc.MethodDef{
		{Cmd: sel("contentKeySession:didProvideContentKeyRequest:"), Fn: func(self objc.ID, _ objc.SEL, session, request objc.ID) {
			sessionsMu.Lock()
			b := sessions[session]
			sessionsMu.Unlock()
			if b != nil {
				b.onRequest(request)
			}
		}},
		{Cmd: sel("contentKeySession:contentKeyRequest:didFailWithError:"), Fn: func(self objc.ID, _ objc.SEL, session, request, nsErr objc.ID) {
			sessionsMu.Lock()
			b := sessions[session]
			sessionsMu.Unlock()
			if b != nil {
				b.onFail(request, nsErr)
			}
		}},
	})
	if err != nil {
		return err
	}
	delegates = send(send(objc.ID(cls), "alloc"), "init")
	return nil
}

type provider struct {
	opts Options
}

func newProvider(o Options) cdm.Provider { return &provider{opts: o} }

func (p *provider) KeySystem() string { return KeySystem }

func (p *provider) Availability() cdm.Availability {
	if !p.opts.Enable && os.Getenv(EnableEnv) != "1" {
		return cdm.Availability{Reason: "the FairPlay backend is unverified and disabled; set " + EnableEnv + "=1 to try it"}
	}
	if err := load(); err != nil {
		return cdm.Availability{Reason: "AVContentKeySession: " + err.Error()}
	}
	return cdm.Availability{Supported: true}
}

func (p *provider) Capabilities() cdm.Capabilities {
	return cdm.Capabilities{
		InitDataTypes:         []string{"skd", "sinf", "cenc"},
		SessionTypes:          []cdm.SessionType{cdm.SessionTemporary},
		VideoCodecs:           []string{"avc1", "hevc"},
		AudioCodecs:           []string{"mp4a", "ac-3", "ec-3"},
		Containers:            []string{"video/mp4", "audio/mp4"},
		Schemes:               []cdm.Scheme{cdm.SchemeCbcs, cdm.SchemeCenc},
		Robustness:            []cdm.RobustnessLevel{{Name: ""}},
		DistinctiveIdentifier: cdm.NotAllowed,
		PersistentState:       cdm.Optional,
		Mode:                  cdm.ModePlatformRender,
		Unverified:            true,
		Notes:                 "UNVERIFIED; license exchange only, AVFoundation playback handoff is not implemented",
	}
}

func (p *provider) New(ctx context.Context, cfg cdm.InstanceConfig) (cdm.Backend, error) {
	if av := p.Availability(); !av.Supported {
		return nil, fmt.Errorf("%w: %s", cdm.ErrUnsupported, av.Reason)
	}
	pool := send(send(class("NSAutoreleasePool"), "alloc"), "init")
	defer send(pool, "release")
	s := send(class("AVContentKeySession"), "contentKeySessionWithKeySystem:", fpKeySys)
	if s == 0 {
		return nil, errors.New("fairplay: AVContentKeySession could not be created")
	}
	send(s, "retain")
	b := &backend{session: s, queue: newQueue("sweef.fairplay", 0), reqs: map[string]*request{}}
	sessionsMu.Lock()
	sessions[s] = b
	sessionsMu.Unlock()
	send(s, "setDelegate:queue:", delegates, b.queue)
	return b, nil
}

type backend struct {
	session objc.ID
	queue   uintptr

	mu     sync.Mutex
	cert   []byte
	reqs   map[string]*request // key identifier (skd URI) -> pending request
	closed bool
}

func (b *backend) KeySystem() string     { return KeySystem }
func (b *backend) Mode() cdm.DecryptMode { return cdm.ModePlatformRender }
func (b *backend) Decrypt(context.Context, *cdm.Sample) ([]byte, error) {
	return nil, fmt.Errorf("%w: FairPlay decrypts and decodes inside AVFoundation", cdm.ErrUnsupported)
}

// SetServerCertificate stores the FairPlay application certificate (the "server certificate" of EME).
func (b *backend) SetServerCertificate(ctx context.Context, cert []byte) (bool, error) {
	if len(cert) == 0 {
		return false, cdm.Errorf(cdm.ExceptionTypeError, "empty certificate")
	}
	b.mu.Lock()
	b.cert = append([]byte(nil), cert...)
	b.mu.Unlock()
	return true, nil
}

func (b *backend) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	b.mu.Unlock()
	sessionsMu.Lock()
	delete(sessions, b.session)
	sessionsMu.Unlock()
	send(b.session, "expire")
	send(b.session, "release")
	return nil
}

type request struct {
	obj    objc.ID // AVContentKeyRequest (retained)
	owner  *sess
	keyID  []byte
	result chan result
}

type result struct {
	spc []byte
	err error
}

type sess struct {
	b   *backend
	q   *cdm.EventQueue
	mu  sync.Mutex
	id  string
	key string // skd identifier of this session's request
	st  []cdm.KeyStatus

	pending chan result
}

func (b *backend) CreateSession(ctx context.Context, typ cdm.SessionType) (cdm.Session, error) {
	if typ != cdm.SessionTemporary {
		return nil, cdm.Errorf(cdm.ExceptionNotSupportedError, "FairPlay backend: temporary sessions only")
	}
	return &sess{b: b, q: cdm.NewEventQueue()}, nil
}

func (s *sess) ID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.id
}
func (s *sess) Events() <-chan cdm.Event { return s.q.C() }
func (s *sess) KeyStatuses() []cdm.KeyStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]cdm.KeyStatus(nil), s.st...)
}
func (s *sess) Expiration() time.Time { return time.Time{} }

func (s *sess) GenerateRequest(ctx context.Context, initDataType string, initData []byte) (cdm.Message, error) {
	uri, err := keyURI(initDataType, initData)
	if err != nil {
		return cdm.Message{}, err
	}
	s.b.mu.Lock()
	haveCert := len(s.b.cert) > 0
	s.b.mu.Unlock()
	if !haveCert {
		return cdm.Message{}, cdm.Errorf(cdm.ExceptionInvalidStateError, "FairPlay needs the application certificate: call setServerCertificate first")
	}
	s.mu.Lock()
	s.key = uri
	s.id = fmt.Sprintf("fps-%x", time.Now().UnixNano())
	s.pending = make(chan result, 1)
	pending := s.pending
	s.mu.Unlock()
	s.b.mu.Lock()
	s.b.reqs[uri] = &request{owner: s, keyID: []byte(uri), result: pending}
	s.b.mu.Unlock()

	pool := send(send(class("NSAutoreleasePool"), "alloc"), "init")
	send(s.b.session, "processContentKeyRequestWithIdentifier:initializationData:options:", nsString(uri), objc.ID(0), objc.ID(0))
	send(pool, "release")

	select {
	case r := <-pending:
		if r.err != nil {
			return cdm.Message{}, r.err
		}
		return cdm.Message{Type: cdm.MessageLicenseRequest, Data: r.spc}, nil
	case <-ctx.Done():
		return cdm.Message{}, ctx.Err()
	case <-time.After(60 * time.Second):
		return cdm.Message{}, errors.New("fairplay: AVContentKeySession did not produce a key request")
	}
}

// onRequest runs on the delegate queue when AVFoundation provides the content key request.
func (b *backend) onRequest(req objc.ID) {
	ident := goString(send(req, "identifier"))
	b.mu.Lock()
	r := b.reqs[ident]
	cert := b.cert
	b.mu.Unlock()
	if r == nil {
		return
	}
	send(req, "retain")
	r.obj = req
	contentID := []byte(strings.TrimPrefix(ident, "skd://"))
	block := objc.NewBlock(func(_ objc.Block, spc objc.ID, nsErr objc.ID) {
		if nsErr != 0 || spc == 0 {
			r.result <- result{err: fmt.Errorf("fairplay: makeStreamingContentKeyRequestData failed: %s", goString(send(nsErr, "localizedDescription")))}
			return
		}
		r.result <- result{spc: goBytes(spc)}
	})
	send(req, "makeStreamingContentKeyRequestDataForApp:contentIdentifier:options:completionHandler:",
		nsData(cert), nsData(contentID), objc.ID(0), block)
}

func (b *backend) onFail(req, nsErr objc.ID) {
	ident := goString(send(req, "identifier"))
	b.mu.Lock()
	r := b.reqs[ident]
	b.mu.Unlock()
	if r == nil {
		return
	}
	err := fmt.Errorf("fairplay: key request failed: %s", goString(send(nsErr, "localizedDescription")))
	select {
	case r.result <- result{err: err}:
	default:
	}
	if r.owner != nil {
		r.owner.q.Push(cdm.Event{Type: cdm.EventKeyStatusesChange, KeyStatuses: []cdm.KeyStatus{{KeyID: r.keyID, Status: cdm.KeyInternalError}}})
	}
}

func (s *sess) Load(ctx context.Context, sessionID string) (bool, error) {
	return false, cdm.Errorf(cdm.ExceptionNotSupportedError, "FairPlay backend has no persistent sessions")
}

// Update completes the request with the Content Key Context from the key server.
func (s *sess) Update(ctx context.Context, response []byte) error {
	s.mu.Lock()
	key := s.key
	s.mu.Unlock()
	s.b.mu.Lock()
	r := s.b.reqs[key]
	s.b.mu.Unlock()
	if r == nil || r.obj == 0 {
		return cdm.Errorf(cdm.ExceptionInvalidStateError, "no outstanding FairPlay key request")
	}
	if len(response) == 0 {
		return cdm.Errorf(cdm.ExceptionTypeError, "empty response")
	}
	pool := send(send(class("NSAutoreleasePool"), "alloc"), "init")
	defer send(pool, "release")
	resp := send(class("AVContentKeyResponse"), "contentKeyResponseWithFairPlayStreamingKeyResponseData:", nsData(response))
	if resp == 0 {
		return cdm.Errorf(cdm.ExceptionTypeError, "AVContentKeyResponse rejected the key response")
	}
	send(r.obj, "processContentKeyResponse:", resp)
	ks := []cdm.KeyStatus{{KeyID: r.keyID, Status: cdm.KeyUsable}}
	s.mu.Lock()
	s.st = ks
	s.mu.Unlock()
	s.q.Push(cdm.Event{Type: cdm.EventKeyStatusesChange, KeyStatuses: ks, AdditionalUsableKey: true})
	return nil
}

func (s *sess) Remove(ctx context.Context) error {
	return cdm.Errorf(cdm.ExceptionInvalidStateError, "Remove is for persistent sessions")
}

func (s *sess) Close(ctx context.Context) error {
	s.mu.Lock()
	key := s.key
	s.mu.Unlock()
	s.b.mu.Lock()
	r := s.b.reqs[key]
	delete(s.b.reqs, key)
	s.b.mu.Unlock()
	if r != nil && r.obj != 0 {
		send(r.obj, "release")
	}
	s.q.Push(cdm.Event{Type: cdm.EventClosed})
	s.q.Close()
	return nil
}
