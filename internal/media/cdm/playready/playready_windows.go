//go:build windows

package playready

// UNVERIFIED: see the package comment. Every identifier marked "UNVERIFIED" below was written from documentation and
// memory, not checked against the Windows SDK headers.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/sweefbrowser/sweef/internal/media/cdm"
)

// ---- COM plumbing ----

var (
	ole32                           = windows.NewLazySystemDLL("ole32.dll")
	procCoInitializeEx              = ole32.NewProc("CoInitializeEx")
	procCoCreateInstance            = ole32.NewProc("CoCreateInstance")
	procCoTaskMemFree               = ole32.NewProc("CoTaskMemFree")
	propsys                         = windows.NewLazySystemDLL("propsys.dll")
	procPSCreateMemoryPropertyStore = propsys.NewProc("PSCreateMemoryPropertyStore")
)

const (
	coinitMultithreaded = 0x0
	clsctxInprocServer  = 0x1
	eNoInterface        = 0x80004002
)

var (
	// CLSID_MFMediaEngineClassFactory: checked against mingw-w64's mfmediaengine.h (EXTERN_GUID).
	clsidMFMediaEngineClassFactory = windows.GUID{Data1: 0xb44392da, Data2: 0x499b, Data3: 0x446b, Data4: [8]byte{0xa4, 0xcb, 0x00, 0x5f, 0xea, 0xd0, 0xe6, 0xd5}}

	// The interface IDs below are UNVERIFIED (from memory of mfmediaengine.h / mfcontentdecryptionmodule.h).
	iidMFMediaEngineClassFactory4     = windows.GUID{Data1: 0xfbe256c1, Data2: 0x43cf, Data3: 0x4a9b, Data4: [8]byte{0x8c, 0xb8, 0xce, 0x86, 0x32, 0xa3, 0x41, 0x86}}
	iidMFContentDecryptionModuleFact  = windows.GUID{Data1: 0x7d5abf16, Data2: 0x4cbb, Data3: 0x4e08, Data4: [8]byte{0xb9, 0x77, 0x9b, 0xa5, 0x90, 0x49, 0x94, 0x3e}}
	iidMFContentDecryptionModuleAcces = windows.GUID{Data1: 0xa853d1f4, Data2: 0xe2a0, Data3: 0x4303, Data4: [8]byte{0x9e, 0xdc, 0xf1, 0xa6, 0x8e, 0xe4, 0x31, 0x36}}
	iidMFContentDecryptionModule      = windows.GUID{Data1: 0x87be986c, Data2: 0x10be, Data3: 0x4943, Data4: [8]byte{0xbf, 0x48, 0x4b, 0x54, 0xce, 0x19, 0x83, 0xa2}}
	iidMFCDMSession                   = windows.GUID{Data1: 0x4e233efd, Data2: 0x1dd2, Data3: 0x49e8, Data4: [8]byte{0xb5, 0x77, 0xd6, 0x3e, 0xee, 0x4c, 0x0d, 0x33}}
	iidMFCDMSessionCallbacks          = windows.GUID{Data1: 0x3f96ee40, Data2: 0xad81, Data3: 0x4096, Data4: [8]byte{0x84, 0x70, 0x59, 0xa4, 0xb7, 0x70, 0xf8, 0x9a}}
	iidIUnknown                       = windows.GUID{Data4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}} // IID_IUnknown
	// IID_IPropertyStore is well known and stable.
	iidPropertyStore = windows.GUID{Data1: 0x886d8eeb, Data2: 0x8cf2, Data3: 0x4446, Data4: [8]byte{0x8d, 0x02, 0xcd, 0xba, 0x1d, 0xbd, 0xcf, 0x99}}
)

// propertyKey is PROPERTYKEY: a format GUID and a property id (20 bytes).
type propertyKey struct {
	fmtid windows.GUID
	pid   uint32
}

// propVariant is PROPVARIANT on 64-bit Windows (24 bytes): vt, three reserved words, 16 bytes of union.
type propVariant struct {
	vt  uint16
	_   [3]uint16
	val [2]uintptr
}

const (
	vtUI4          = 19
	vtBSTR         = 8
	pidFirstUsable = 2
)

// Property keys of the MF EME configuration. The origin id key is the one Chromium defines in
// media_foundation_cdm_util.cc (verified from that source); the store path and the MF_EME_* keys are UNVERIFIED.
var (
	keyOriginID  = propertyKey{windows.GUID{Data1: 0x1218a3e2, Data2: 0xcfb0, Data3: 0x4c98, Data4: [8]byte{0x90, 0xe5, 0x5f, 0x58, 0x18, 0xd4, 0xb6, 0x7e}}, pidFirstUsable}
	keyStorePath = propertyKey{windows.GUID{Data1: 0x77d993b9, Data2: 0xba61, Data3: 0x4bb7, Data4: [8]byte{0x92, 0xc6, 0x18, 0xc8, 0x6a, 0x18, 0x9c, 0x06}}, pidFirstUsable} // UNVERIFIED
)

// ptr converts an address returned by COM to an unsafe.Pointer (the addresses are not Go pointers).
func ptr(a uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&a)) }

type hresult uintptr

func (h hresult) failed() bool { return int32(h) < 0 }

func (h hresult) err(what string) error {
	if !h.failed() {
		return nil
	}
	return fmt.Errorf("playready: %s failed: HRESULT 0x%08x", what, uint32(h))
}

// com is a COM interface pointer.
type com uintptr

// call invokes the method at vtable index slot with this as the first argument.
func (c com) call(slot int, args ...uintptr) hresult {
	vtbl := *(*uintptr)(ptr(uintptr(c)))
	fn := *(*uintptr)(ptr(vtbl + uintptr(slot)*unsafe.Sizeof(uintptr(0))))
	all := append([]uintptr{uintptr(c)}, args...)
	r, _, _ := syscallN(fn, all...)
	return hresult(r)
}

func (c com) release() {
	if c != 0 {
		c.call(2)
	}
}

// IUnknown: 0 QueryInterface, 1 AddRef, 2 Release.
//
// Vtable slots of the Media Foundation CDM interfaces (UNVERIFIED, in the order of the MSDN method lists):
const (
	// IMFMediaEngineClassFactory4
	slotCreateCDMFactory = 3 // (LPCWSTR keySystem, REFIID riid, LPVOID* ppv)
	// IMFContentDecryptionModuleFactory
	slotFactoryIsTypeSupported = 3 // (LPCWSTR keySystem, LPCWSTR contentType) -> BOOL
	slotFactoryCreateAccess    = 4 // (LPCWSTR keySystem, IPropertyStore** configs, DWORD n, IMFContentDecryptionModuleAccess** out)
	// IMFContentDecryptionModuleAccess
	slotAccessCreateCDM = 3 // (IPropertyStore* props, IMFContentDecryptionModule** out)
	// IMFContentDecryptionModule
	slotCDMCreateSession        = 6 // (MF_MEDIAKEYSESSION_TYPE, IMFContentDecryptionModuleSessionCallbacks*, IMFContentDecryptionModuleSession**)
	slotCDMSetServerCertificate = 7 // (const BYTE*, DWORD)
	// IMFContentDecryptionModuleSession
	slotSessGetSessionID  = 3 // (LPWSTR*)
	slotSessGetExpiration = 4 // (double*)
	slotSessGetKeyStatus  = 5 // (MFMediaKeyStatus** , UINT*)
	slotSessLoad          = 6 // (LPCWSTR sessionId, BOOL* loaded)
	slotSessGenerate      = 7 // (LPCWSTR initDataType, const BYTE*, DWORD)
	slotSessUpdate        = 8 // (const BYTE*, DWORD)
	slotSessClose         = 9
	slotSessRemove        = 10
	// IMFContentDecryptionModuleSessionCallbacks
	slotCBKeyMessage       = 3 // (MF_MEDIAKEYSESSION_MESSAGETYPE, const BYTE*, DWORD, LPCWSTR destinationURL)
	slotCBKeyStatusChanged = 4 // ()
)

// MF_MEDIAKEYSESSION_TYPE and MF_MEDIAKEYSESSION_MESSAGETYPE values (UNVERIFIED).
const (
	mfSessionTemporary         = 0
	mfSessionPersistentLicense = 1
)

// mfMediaKeyStatus is MFMediaKeyStatus: {BYTE* pbKeyId; UINT cbKeyId; MF_MEDIAKEY_STATUS eMediaKeyStatus} (16 bytes).
type mfMediaKeyStatus struct {
	keyID     uintptr
	keyIDSize uint32
	status    uint32
}

func syscallN(fn uintptr, args ...uintptr) (uintptr, uintptr, error) {
	r1, r2, e := syscall.SyscallN(fn, args...)
	if e != 0 {
		return r1, r2, e
	}
	return r1, r2, nil
}

func utf16Ptr(s string) uintptr {
	p, _ := windows.UTF16PtrFromString(s)
	return uintptr(unsafe.Pointer(p))
}

// ---- the dedicated COM thread ----
//
// Media Foundation's CDM objects are free threaded; calls still come from a thread that did CoInitializeEx, so every
// call runs on one locked OS thread.

type comThread struct {
	jobs chan func()
	once sync.Once
}

var theThread = &comThread{jobs: make(chan func(), 64)}

func (t *comThread) start() {
	t.once.Do(func() {
		ready := make(chan struct{})
		go func() {
			runtime.LockOSThread()
			procCoInitializeEx.Call(0, coinitMultithreaded)
			close(ready)
			for f := range t.jobs {
				f()
			}
		}()
		<-ready
	})
}

func (t *comThread) do(f func()) {
	t.start()
	done := make(chan struct{})
	t.jobs <- func() { f(); close(done) }
	<-done
}

// ---- provider ----

type provider struct {
	ks   string
	opts Options

	mu      sync.Mutex
	probed  bool
	factory com
	reason  string
}

func newProvider(ks string, o Options) cdm.Provider { return &provider{ks: ks, opts: o} }

func (p *provider) KeySystem() string { return p.ks }

func (p *provider) enabled() bool { return p.opts.Enable || os.Getenv(EnableEnv) == "1" }

func (p *provider) probe() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.probed {
		return
	}
	p.probed = true
	if !p.enabled() {
		p.reason = "the PlayReady backend is unverified and disabled; set " + EnableEnv + "=1 to try it"
		return
	}
	theThread.do(func() {
		var class com
		hr := hresult(0)
		r, _, _ := procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidMFMediaEngineClassFactory)), 0, clsctxInprocServer,
			uintptr(unsafe.Pointer(&iidMFMediaEngineClassFactory4)), uintptr(unsafe.Pointer(&class)))
		if hr = hresult(r); hr.failed() {
			p.reason = fmt.Sprintf("Media Foundation Media Engine factory unavailable (HRESULT 0x%08x)", uint32(hr))
			return
		}
		defer class.release()
		var factory com
		ks := utf16Ptr(p.ks)
		hr = class.call(slotCreateCDMFactory, ks, uintptr(unsafe.Pointer(&iidMFContentDecryptionModuleFact)), uintptr(unsafe.Pointer(&factory)))
		if hr.failed() {
			p.reason = fmt.Sprintf("no %s content decryption module factory (HRESULT 0x%08x)", p.ks, uint32(hr))
			return
		}
		if supported := factory.call(slotFactoryIsTypeSupported, ks, 0); supported&0xff == 0 {
			factory.release()
			p.reason = p.ks + " is not supported by this Windows installation"
			return
		}
		p.factory = factory
	})
}

func (p *provider) Availability() cdm.Availability {
	p.probe()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.factory == 0 {
		return cdm.Availability{Reason: p.reason}
	}
	return cdm.Availability{Supported: true}
}

func (p *provider) Capabilities() cdm.Capabilities {
	return cdm.Capabilities{
		InitDataTypes: []string{"cenc", "keyids"},
		SessionTypes:  []cdm.SessionType{cdm.SessionTemporary, cdm.SessionPersistentLicense},
		VideoCodecs:   []string{"avc1", "hevc"},
		AudioCodecs:   []string{"mp4a"},
		Containers:    []string{"video/mp4", "audio/mp4"},
		Schemes:       []cdm.Scheme{cdm.SchemeCenc, cdm.SchemeCbcs},
		// "3000" is the hardware (SL3000) PlayReady level Chromium passes; "2000" the software level.
		Robustness:            []cdm.RobustnessLevel{{Name: ""}, {Name: "2000"}, {Name: "3000", Hardware: true}},
		DistinctiveIdentifier: cdm.Required,
		PersistentState:       cdm.Required,
		Mode:                  cdm.ModePlatformRender,
		Unverified:            true,
		Notes:                 "UNVERIFIED; license exchange only, Media Engine playback handoff is not implemented",
	}
}

func (p *provider) New(ctx context.Context, cfg cdm.InstanceConfig) (cdm.Backend, error) {
	if av := p.Availability(); !av.Supported {
		return nil, fmt.Errorf("%w: %s", cdm.ErrUnsupported, av.Reason)
	}
	b := &backend{ks: p.ks, sessions: map[com]*session{}}
	var err error
	theThread.do(func() { err = b.create(p, cfg) })
	if err != nil {
		return nil, err
	}
	return b, nil
}

type backend struct {
	ks  string
	cdm com

	mu       sync.Mutex
	sessions map[com]*session
	closed   bool
}

// create builds the IMFContentDecryptionModuleAccess and the CDM, as media_foundation_cdm_util.cc does.
func (b *backend) create(p *provider, cfg cdm.InstanceConfig) error {
	// The access configuration (UNVERIFIED keys: robustness, persisted state, distinctive id) is left to the
	// platform defaults; an empty property store is a valid, if permissive, configuration request.
	cfgStore, err := newPropertyStore()
	if err != nil {
		return err
	}
	defer cfgStore.release()
	var access com
	configs := [1]uintptr{uintptr(cfgStore)}
	if hr := p.factory.call(slotFactoryCreateAccess, utf16Ptr(p.ks), uintptr(unsafe.Pointer(&configs[0])), 1, uintptr(unsafe.Pointer(&access))); hr.failed() {
		return hr.err("CreateContentDecryptionModuleAccess")
	}
	defer access.release()

	props, err := newPropertyStore()
	if err != nil {
		return err
	}
	defer props.release()
	if cfg.Origin != "" {
		if err := props.setString(keyOriginID, cfg.Origin); err != nil {
			return err
		}
	}
	if cfg.StorageDir != "" {
		dir := filepath.Join(cfg.StorageDir, "playready")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		if err := props.setString(keyStorePath, dir); err != nil {
			return err
		}
	}
	if hr := access.call(slotAccessCreateCDM, uintptr(props), uintptr(unsafe.Pointer(&b.cdm))); hr.failed() {
		return hr.err("CreateContentDecryptionModule")
	}
	return nil
}

func (b *backend) KeySystem() string     { return b.ks }
func (b *backend) Mode() cdm.DecryptMode { return cdm.ModePlatformRender }
func (b *backend) Decrypt(context.Context, *cdm.Sample) ([]byte, error) {
	return nil, fmt.Errorf("%w: PlayReady decrypts inside Media Foundation's protected media path", cdm.ErrUnsupported)
}

func (b *backend) SetServerCertificate(ctx context.Context, cert []byte) (bool, error) {
	var hr hresult
	theThread.do(func() {
		if len(cert) == 0 {
			hr = hresult(0x80070057) // E_INVALIDARG
			return
		}
		hr = b.cdm.call(slotCDMSetServerCertificate, uintptr(unsafe.Pointer(&cert[0])), uintptr(len(cert)))
	})
	if hr.failed() {
		return false, nil
	}
	return true, nil
}

func (b *backend) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	sessions := b.sessions
	b.sessions = map[com]*session{}
	b.mu.Unlock()
	theThread.do(func() {
		for _, s := range sessions {
			s.closeNative()
		}
		b.cdm.release()
	})
	return nil
}

// ---- sessions ----

type session struct {
	b   *backend
	typ cdm.SessionType
	q   *cdm.EventQueue
	cb  *callbackObj

	mu         sync.Mutex
	native     com
	id         string
	first      chan cdm.Message
	awaitFirst bool
	statuses   []cdm.KeyStatus
	closed     bool
}

func (b *backend) CreateSession(ctx context.Context, typ cdm.SessionType) (cdm.Session, error) {
	t := mfSessionTemporary
	switch typ {
	case cdm.SessionTemporary:
	case cdm.SessionPersistentLicense:
		t = mfSessionPersistentLicense
	default:
		return nil, cdm.Errorf(cdm.ExceptionNotSupportedError, "session type %q", typ)
	}
	s := &session{b: b, typ: typ, q: cdm.NewEventQueue(), first: make(chan cdm.Message, 1)}
	s.cb = newCallbackObj(s)
	var hr hresult
	theThread.do(func() {
		hr = b.cdm.call(slotCDMCreateSession, uintptr(t), s.cb.addr, uintptr(unsafe.Pointer(&s.native)))
	})
	if hr.failed() {
		s.cb.release()
		return nil, hr.err("CreateSession")
	}
	return s, nil
}

func (s *session) ID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.id
}
func (s *session) Events() <-chan cdm.Event { return s.q.C() }
func (s *session) KeyStatuses() []cdm.KeyStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]cdm.KeyStatus(nil), s.statuses...)
}

func (s *session) Expiration() time.Time {
	var ms float64
	theThread.do(func() {
		if s.native != 0 {
			s.native.call(slotSessGetExpiration, uintptr(unsafe.Pointer(&ms)))
		}
	})
	if ms == 0 || math.IsNaN(ms) || math.IsInf(ms, 0) {
		return time.Time{}
	}
	return time.UnixMilli(int64(ms))
}

func (s *session) loadID() {
	var p uintptr
	if hr := s.native.call(slotSessGetSessionID, uintptr(unsafe.Pointer(&p))); hr.failed() || p == 0 {
		return
	}
	id := windows.UTF16PtrToString((*uint16)(ptr(p)))
	procCoTaskMemFree.Call(p)
	s.mu.Lock()
	s.id = id
	s.mu.Unlock()
}

func (s *session) GenerateRequest(ctx context.Context, initDataType string, initData []byte) (cdm.Message, error) {
	if len(initData) == 0 {
		return cdm.Message{}, cdm.Errorf(cdm.ExceptionTypeError, "empty init data")
	}
	s.mu.Lock()
	s.awaitFirst = true
	s.mu.Unlock()
	var hr hresult
	theThread.do(func() {
		hr = s.native.call(slotSessGenerate, utf16Ptr(initDataType), uintptr(unsafe.Pointer(&initData[0])), uintptr(len(initData)))
		if !hr.failed() {
			s.loadID()
		}
	})
	if hr.failed() {
		return cdm.Message{}, hr.err("GenerateRequest")
	}
	select {
	case m := <-s.first:
		return m, nil
	case <-ctx.Done():
		return cdm.Message{}, ctx.Err()
	case <-time.After(60 * time.Second):
		return cdm.Message{}, errors.New("playready: no license request message")
	}
}

func (s *session) Load(ctx context.Context, sessionID string) (bool, error) {
	var hr hresult
	var loaded int32
	theThread.do(func() {
		hr = s.native.call(slotSessLoad, utf16Ptr(sessionID), uintptr(unsafe.Pointer(&loaded)))
		if !hr.failed() && loaded != 0 {
			s.loadID()
		}
	})
	if hr.failed() {
		return false, hr.err("Load")
	}
	return loaded != 0, nil
}

func (s *session) simple(slot int, what string, args ...uintptr) error {
	var hr hresult
	theThread.do(func() { hr = s.native.call(slot, args...) })
	return hr.err(what)
}

func (s *session) Update(ctx context.Context, response []byte) error {
	if len(response) == 0 {
		return cdm.Errorf(cdm.ExceptionTypeError, "empty response")
	}
	return s.simple(slotSessUpdate, "Update", uintptr(unsafe.Pointer(&response[0])), uintptr(len(response)))
}

func (s *session) Remove(ctx context.Context) error { return s.simple(slotSessRemove, "Remove") }

func (s *session) Close(ctx context.Context) error {
	var err error
	theThread.do(func() { err = s.closeNativeErr() })
	return err
}

func (s *session) closeNative() { _ = s.closeNativeErr() }

func (s *session) closeNativeErr() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	native := s.native
	s.mu.Unlock()
	var err error
	if native != 0 {
		err = native.call(slotSessClose).err("Close")
		native.release()
	}
	s.q.Push(cdm.Event{Type: cdm.EventClosed})
	s.q.Close()
	s.cb.release()
	return err
}

// keyMessage runs on a Media Foundation thread.
func (s *session) keyMessage(typ uint32, msg []byte) {
	mt := cdm.MessageLicenseRequest
	switch typ {
	case 1:
		mt = cdm.MessageLicenseRenewal
	case 2:
		mt = cdm.MessageLicenseRelease
	case 3:
		mt = cdm.MessageIndividualizationReq
	}
	m := cdm.Message{Type: mt, Data: msg}
	s.mu.Lock()
	if s.awaitFirst {
		s.awaitFirst = false
		s.mu.Unlock()
		s.first <- m
		return
	}
	s.mu.Unlock()
	s.q.Push(cdm.Event{Type: cdm.EventMessage, Message: m})
}

// keyStatusChanged runs on a Media Foundation thread.
func (s *session) keyStatusChanged() {
	var arr uintptr
	var n uint32
	if hr := s.native.call(slotSessGetKeyStatus, uintptr(unsafe.Pointer(&arr)), uintptr(unsafe.Pointer(&n))); hr.failed() || arr == 0 {
		return
	}
	var out []cdm.KeyStatus
	for _, k := range unsafe.Slice((*mfMediaKeyStatus)(ptr(arr)), int(n)) {
		out = append(out, cdm.KeyStatus{KeyID: append([]byte(nil), unsafe.Slice((*byte)(ptr(k.keyID)), int(k.keyIDSize))...), Status: mapStatus(k.status)})
		procCoTaskMemFree.Call(k.keyID)
	}
	procCoTaskMemFree.Call(arr)
	s.mu.Lock()
	s.statuses = out
	s.mu.Unlock()
	usable := false
	for _, k := range out {
		usable = usable || k.Status == cdm.KeyUsable
	}
	s.q.Push(cdm.Event{Type: cdm.EventKeyStatusesChange, KeyStatuses: out, AdditionalUsableKey: usable})
}

// MF_MEDIAKEY_STATUS (UNVERIFIED order).
func mapStatus(v uint32) cdm.KeyStatusKind {
	switch v {
	case 0:
		return cdm.KeyUsable
	case 1:
		return cdm.KeyExpired
	case 2:
		return cdm.KeyOutputDownscaled
	case 3, 7:
		return cdm.KeyOutputRestricted
	case 4:
		return cdm.KeyStatusPending
	case 6:
		return cdm.KeyReleased
	}
	return cdm.KeyInternalError
}

// ---- the callbacks object (IMFContentDecryptionModuleSessionCallbacks implemented in Go) ----

type callbackObj struct {
	vtbl uintptr // first word: pointer to the table below
	sess *session
	addr uintptr
}

var (
	cbOnce   sync.Once
	cbVtbl   [5]uintptr
	cbPinned sync.Map // addr -> *callbackObj
)

func newCallbackObj(s *session) *callbackObj {
	cbOnce.Do(func() {
		cbVtbl[0] = windows.NewCallback(func(this, riid, ppv uintptr) uintptr { // QueryInterface
			id := *(*windows.GUID)(ptr(riid))
			if id == iidIUnknown || id == iidMFCDMSessionCallbacks {
				*(*uintptr)(ptr(ppv)) = this
				return 0
			}
			*(*uintptr)(ptr(ppv)) = 0
			return eNoInterface
		})
		cbVtbl[1] = windows.NewCallback(func(this uintptr) uintptr { return 2 })          // AddRef: the object is pinned
		cbVtbl[2] = windows.NewCallback(func(this uintptr) uintptr { return 1 })          // Release
		cbVtbl[3] = windows.NewCallback(func(this, typ, msg, size, url uintptr) uintptr { // KeyMessage
			if o, ok := cbPinned.Load(this); ok {
				var data []byte
				if msg != 0 && size > 0 {
					data = append([]byte(nil), unsafe.Slice((*byte)(ptr(msg)), int(uint32(size)))...)
				}
				o.(*callbackObj).sess.keyMessage(uint32(typ), data)
			}
			return 0
		})
		cbVtbl[4] = windows.NewCallback(func(this uintptr) uintptr { // KeyStatusChanged
			if o, ok := cbPinned.Load(this); ok {
				o.(*callbackObj).sess.keyStatusChanged()
			}
			return 0
		})
	})
	o := &callbackObj{vtbl: uintptr(unsafe.Pointer(&cbVtbl)), sess: s}
	o.addr = uintptr(unsafe.Pointer(o))
	cbPinned.Store(o.addr, o)
	return o
}

func (o *callbackObj) release() { cbPinned.Delete(o.addr) }

// ---- IPropertyStore ----

type propertyStore com

// IPropertyStore: 3 GetCount, 4 GetAt, 5 GetValue, 6 SetValue(REFPROPERTYKEY, REFPROPVARIANT), 7 Commit.
func newPropertyStore() (propertyStore, error) {
	var ps com
	r, _, _ := procPSCreateMemoryPropertyStore.Call(uintptr(unsafe.Pointer(&iidPropertyStore)), uintptr(unsafe.Pointer(&ps)))
	if hr := hresult(r); hr.failed() {
		return 0, hr.err("PSCreateMemoryPropertyStore")
	}
	return propertyStore(ps), nil
}

func (p propertyStore) release() { com(p).release() }

func (p propertyStore) setString(k propertyKey, s string) error {
	u16, err := windows.UTF16PtrFromString(s)
	if err != nil {
		return err
	}
	// VT_BSTR needs a BSTR (length-prefixed); SysAllocString makes one.
	sysAlloc := windows.NewLazySystemDLL("oleaut32.dll").NewProc("SysAllocString")
	bstr, _, _ := sysAlloc.Call(uintptr(unsafe.Pointer(u16)))
	if bstr == 0 {
		return errors.New("playready: SysAllocString failed")
	}
	pv := propVariant{vt: vtBSTR, val: [2]uintptr{bstr}}
	hr := com(p).call(6, uintptr(unsafe.Pointer(&k)), uintptr(unsafe.Pointer(&pv)))
	return hr.err("IPropertyStore::SetValue")
}
