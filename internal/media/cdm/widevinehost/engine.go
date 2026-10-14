//go:build amd64 || arm64

package widevinehost

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/sweefbrowser/sweef/internal/media/cdm"
)

// Config configures Open.
type Config struct {
	// Library is the CDM shared library (libwidevinecdm.so / widevinecdm.dll / libwidevinecdm.dylib).
	Library string
	// KeySystem is passed to CreateCdmInstance; default com.widevine.alpha.
	KeySystem string
	// InterfaceVersions lists the cdm::ContentDecryptionModule_N versions to try, in order of preference. Only 10, 11
	// and 12 are understood; the default is 11, 10.
	InterfaceVersions []int
	// HostFiles are the files handed to VerifyCdmHost_0. The default is the running executable with its
	// castLabs EVS signature <exe>.sig, plus the CDM library with <library>.sig when that exists. Missing .sig
	// files are passed as "no signature file"; nothing is ever synthesized.
	HostFiles []HostFileSpec
	// Module replaces dlopen'ing Library (tests).
	Module Module
	// Logf receives diagnostics (nil: none).
	Logf func(format string, args ...any)
}

var promiseTimeout = 90 * time.Second

type promiseResult struct {
	ok         bool
	hasSession bool
	sessionID  string
	exception  uint32
	systemCode uint32
	message    string
	keyStatus  uint32
}

func (r promiseResult) err() error {
	return &cdm.Error{Exception: exceptionName(r.exception), SystemCode: r.systemCode, Message: r.message}
}

// Engine implements cdm.Engine on top of a loaded CDM.
type Engine struct {
	cfg      Config
	mod      Module
	thr      *cdmThread
	handle   uintptr // the user_data the CDM passes back to GetCdmHostFunc
	hostPtr  *hostObj
	hostAddr uintptr
	cdmPtr   uintptr
	version  int
	info     cdm.EngineInfo

	sinkMu sync.RWMutex
	sink   cdm.EventSink

	pmu      sync.Mutex
	promises map[uint32]chan promiseResult
	ids      idGen
	initCh   chan bool

	storeMu sync.Mutex
	store   *fileStore

	closed    atomic.Bool
	closeOnce sync.Once
	stopClock chan struct{}
}

var _ cdm.Engine = (*Engine)(nil)

var (
	enginesMu  sync.Mutex
	engines            = map[uintptr]*Engine{}
	nextHandle uintptr = 0x5eef0000
)

func engineByHandle(h uintptr) *Engine {
	enginesMu.Lock()
	defer enginesMu.Unlock()
	return engines[h]
}

func registerEngine(e *Engine) {
	enginesMu.Lock()
	nextHandle++
	e.handle = nextHandle
	engines[e.handle] = e
	enginesMu.Unlock()
}

func unregisterEngine(e *Engine) {
	enginesMu.Lock()
	delete(engines, e.handle)
	enginesMu.Unlock()
}

// Open loads the CDM, initializes the module, optionally verifies the host, and creates one CDM instance.
func Open(cfg Config) (*Engine, error) {
	if cfg.KeySystem == "" {
		cfg.KeySystem = cdm.WidevineKeySystem
	}
	versions := wantedVersions(cfg.InterfaceVersions)
	c, err := theCallbacks()
	if err != nil {
		return nil, err
	}
	mod := cfg.Module
	if mod == nil {
		if cfg.Library == "" {
			return nil, errors.New("widevinehost: no CDM library given")
		}
		if mod, err = loadModule(cfg.Library); err != nil {
			return nil, err
		}
	}
	e := &Engine{
		cfg: cfg, mod: mod, thr: newCDMThread(),
		promises:  map[uint32]chan promiseResult{},
		initCh:    make(chan bool, 1),
		stopClock: make(chan struct{}),
	}
	e.hostPtr = &hostObj{vtbl: vtblAddr(&c.host), eng: e}
	e.hostAddr = uintptr(unsafe.Pointer(e.hostPtr))
	pin(e.hostAddr, e.hostPtr)
	registerEngine(e)
	startWallClock(e.stopClock)

	var openErr error
	if err := e.thr.do(func() { openErr = e.openOnThread(c, versions) }); err != nil {
		openErr = err
	}
	if openErr != nil {
		e.shutdown(false)
		return nil, openErr
	}
	return e, nil
}

func wantedVersions(in []int) []int {
	if len(in) == 0 {
		return []int{11, 10}
	}
	var out []int
	for _, v := range in {
		if v >= 10 && v <= 12 && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	slices.SortFunc(out, func(a, b int) int { return b - a })
	return out
}

func (e *Engine) logf(format string, args ...any) {
	if e.cfg.Logf != nil {
		e.cfg.Logf(format, args...)
	}
}

func (e *Engine) openOnThread(c *callbacks, versions []int) error {
	touchWallTime()
	if err := e.mod.Initialize(); err != nil {
		return err
	}
	vmp := false
	specs := e.cfg.HostFiles
	if specs == nil {
		specs = defaultHostFiles(e.cfg.Library)
	}
	if len(specs) > 0 {
		called, accepted, err := e.mod.VerifyHost(specs)
		switch {
		case err != nil:
			e.logf("VerifyCdmHost_0: %v", err)
		case !called:
			e.logf("this CDM does not export VerifyCdmHost_0")
		default:
			e.logf("VerifyCdmHost_0 returned %v (host file signatures: %s)", accepted, describeSigs(specs))
		}
		vmp = called && specs[0].Sig != "" && fileExists(specs[0].Sig)
	}
	for _, v := range versions {
		if p := e.mod.CreateInstance(v, e.cfg.KeySystem, c.getHost, e.handle); p != 0 {
			e.cdmPtr, e.version = p, v
			break
		}
	}
	if e.cdmPtr == 0 {
		return fmt.Errorf("the CDM does not provide cdm::ContentDecryptionModule_%v for %q", versions, e.cfg.KeySystem)
	}
	e.info = cdm.EngineInfo{KeySystem: e.cfg.KeySystem, CDMVersion: e.mod.Version(), InterfaceVersion: e.version, Mode: cdm.ModeDecrypt, VMPSigned: vmp}
	return nil
}

// defaultHostFiles is what Chrome-style VMP verification gets from a helper process: the helper executable and
// its signature, and the CDM library with its signature if one is shipped next to it.
func defaultHostFiles(library string) []HostFileSpec {
	var specs []HostFileSpec
	if exe, err := os.Executable(); err == nil {
		s := HostFileSpec{Path: exe}
		if sig := exe + ".sig"; fileExists(sig) {
			s.Sig = sig
		}
		specs = append(specs, s)
	}
	if library != "" {
		s := HostFileSpec{Path: library}
		if sig := library + ".sig"; fileExists(sig) {
			s.Sig = sig
		}
		specs = append(specs, s)
	}
	return specs
}

func describeSigs(specs []HostFileSpec) string {
	s := ""
	for _, f := range specs {
		if s != "" {
			s += ", "
		}
		sig := "none"
		if f.Sig != "" {
			sig = "present"
		}
		s += filepath.Base(f.Path) + ": " + sig
	}
	return s
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

func (e *Engine) Info() cdm.EngineInfo { return e.info }

func (e *Engine) SetSink(s cdm.EventSink) {
	e.sinkMu.Lock()
	e.sink = s
	e.sinkMu.Unlock()
}

// ---- promises ----

func (e *Engine) newPromise() (uint32, chan promiseResult) {
	id := e.ids.next()
	ch := make(chan promiseResult, 1)
	e.pmu.Lock()
	e.promises[id] = ch
	e.pmu.Unlock()
	return id, ch
}

func (e *Engine) dropPromise(id uint32) {
	e.pmu.Lock()
	delete(e.promises, id)
	e.pmu.Unlock()
}

func (e *Engine) resolve(id uint32, r promiseResult) {
	e.pmu.Lock()
	ch := e.promises[id]
	delete(e.promises, id)
	e.pmu.Unlock()
	if ch != nil {
		ch <- r
	}
}

func (e *Engine) onInitialized(ok bool) {
	select {
	case e.initCh <- ok:
	default:
	}
}

// promiseCall runs start on the CDM thread and waits for its promise.
func (e *Engine) promiseCall(ctx context.Context, start func(id uint32, a *arena)) (promiseResult, error) {
	var res promiseResult
	var err error
	terr := e.thr.do(func() {
		touchWallTime()
		a := &arena{}
		defer a.release()
		id, ch := e.newPromise()
		start(id, a)
		res, err = await(ctx, e.thr, ch, promiseTimeout)
		if err != nil {
			e.dropPromise(id)
		}
	})
	if terr != nil {
		return res, terr
	}
	return res, err
}

func (e *Engine) Initialize(ctx context.Context, p cdm.InitParams) error {
	if p.PersistentState && p.StorageDir != "" {
		st, err := newFileStore(p.StorageDir)
		if err != nil {
			return fmt.Errorf("creating the CDM storage directory: %w", err)
		}
		e.storeMu.Lock()
		e.store = st
		e.storeMu.Unlock()
	}
	var ok bool
	var err error
	terr := e.thr.do(func() {
		touchWallTime()
		vcall(e.cdmPtr, cdmInitialize, b2u(p.DistinctiveIdentifier), b2u(p.PersistentState && p.StorageDir != ""), b2u(p.HardwareSecure))
		ok, err = await(ctx, e.thr, e.initCh, promiseTimeout)
	})
	if terr != nil {
		return terr
	}
	if err != nil {
		return err
	}
	if !ok {
		return cdm.Errorf(cdm.ExceptionInvalidStateError, "the CDM failed to initialize")
	}
	return nil
}

func (e *Engine) SetServerCertificate(ctx context.Context, cert []byte) error {
	res, err := e.promiseCall(ctx, func(id uint32, a *arena) {
		vcall(e.cdmPtr, cdmSetServerCertificate, uintptr(id), a.bytes(cert), uintptr(len(cert)))
	})
	if err != nil {
		return err
	}
	if !res.ok {
		if exceptionName(res.exception) == cdm.ExceptionNotSupportedError {
			return cdm.ErrUnsupported
		}
		return res.err()
	}
	return nil
}

func sessionTypeCode(t cdm.SessionType) (uintptr, error) {
	switch t {
	case cdm.SessionTemporary, "":
		return sessionTemporary, nil
	case cdm.SessionPersistentLicense:
		return sessionPersistentLicense, nil
	}
	return 0, cdm.Errorf(cdm.ExceptionTypeError, "unknown session type %q", t)
}

func initDataTypeCode(t string) (uintptr, error) {
	switch t {
	case "cenc":
		return initCenc, nil
	case "keyids":
		return initKeyIDs, nil
	case "webm":
		return initWebM, nil
	}
	return 0, cdm.Errorf(cdm.ExceptionNotSupportedError, "init data type %q is not supported by Widevine", t)
}

func (e *Engine) CreateSession(ctx context.Context, typ cdm.SessionType, initDataType string, initData []byte) (string, error) {
	st, err := sessionTypeCode(typ)
	if err != nil {
		return "", err
	}
	it, err := initDataTypeCode(initDataType)
	if err != nil {
		return "", err
	}
	res, err := e.promiseCall(ctx, func(id uint32, a *arena) {
		vcall(e.cdmPtr, cdmCreateSessionAndGenerateReq, uintptr(id), st, it, a.bytes(initData), uintptr(len(initData)))
	})
	if err != nil {
		return "", err
	}
	if !res.ok {
		return "", res.err()
	}
	if !res.hasSession || res.sessionID == "" {
		return "", cdm.Errorf(cdm.ExceptionInvalidStateError, "the CDM returned no session id")
	}
	return res.sessionID, nil
}

func (e *Engine) LoadSession(ctx context.Context, typ cdm.SessionType, sessionID string) (bool, error) {
	st, err := sessionTypeCode(typ)
	if err != nil {
		return false, err
	}
	res, err := e.promiseCall(ctx, func(id uint32, a *arena) {
		vcall(e.cdmPtr, cdmLoadSession, uintptr(id), st, a.bytes([]byte(sessionID)), uintptr(len(sessionID)))
	})
	if err != nil {
		return false, err
	}
	if !res.ok {
		return false, res.err()
	}
	return res.hasSession, nil // NULL session id: not found
}

func (e *Engine) UpdateSession(ctx context.Context, sessionID string, response []byte) error {
	res, err := e.promiseCall(ctx, func(id uint32, a *arena) {
		vcall(e.cdmPtr, cdmUpdateSession, uintptr(id), a.bytes([]byte(sessionID)), uintptr(len(sessionID)), a.bytes(response), uintptr(len(response)))
	})
	if err != nil {
		return err
	}
	if !res.ok {
		return res.err()
	}
	return nil
}

func (e *Engine) sessionOp(ctx context.Context, slot int, sessionID string) error {
	res, err := e.promiseCall(ctx, func(id uint32, a *arena) {
		vcall(e.cdmPtr, slot, uintptr(id), a.bytes([]byte(sessionID)), uintptr(len(sessionID)))
	})
	if err != nil {
		return err
	}
	if !res.ok {
		return res.err()
	}
	return nil
}

func (e *Engine) CloseSession(ctx context.Context, sessionID string) error {
	return e.sessionOp(ctx, cdmCloseSession, sessionID)
}

func (e *Engine) RemoveSession(ctx context.Context, sessionID string) error {
	return e.sessionOp(ctx, cdmRemoveSession, sessionID)
}

// Decrypt implements cdm.Engine for the desktop CDM in decrypt-only mode.
func (e *Engine) Decrypt(ctx context.Context, s *cdm.Sample) ([]byte, error) {
	var scheme uint32
	switch s.Scheme {
	case cdm.SchemeNone:
		return append([]byte(nil), s.Data...), nil
	case cdm.SchemeCenc:
		scheme = schemeCenc
	case cdm.SchemeCbcs:
		scheme = schemeCbcs
	default:
		return nil, fmt.Errorf("%w: the CDM API only has cenc and cbcs, not %q", cdm.ErrUnsupported, s.Scheme)
	}
	var out []byte
	var err error
	if terr := e.thr.do(func() { out, err = e.decryptOnThread(s, scheme) }); terr != nil {
		return nil, terr
	}
	return out, err
}

func (e *Engine) decryptOnThread(s *cdm.Sample, scheme uint32) ([]byte, error) {
	touchWallTime()
	c, err := theCallbacks()
	if err != nil {
		return nil, err
	}
	a := &arena{}
	defer a.release()

	in, inAddr := newObj[inputBuffer2](a)
	in.data, in.dataSize = a.bytes(s.Data), uint32(len(s.Data))
	in.scheme = scheme
	in.keyID, in.keyIDSize = a.bytes(s.KeyID), uint32(len(s.KeyID))
	in.iv, in.ivSize = a.bytes(s.IV), uint32(len(s.IV))
	if n := len(s.Subsamples); n > 0 {
		subs := make([]subsampleEntry, n)
		for i, ss := range s.Subsamples {
			subs[i] = subsampleEntry{ss.ClearBytes, ss.CipherBytes}
		}
		in.subsamples = a.keep(uintptr(unsafe.Pointer(&subs[0])), subs)
		in.numSubsamples = uint32(n)
	}
	in.cryptBlocks, in.skipBlocks = s.Pattern.CryptBlocks, s.Pattern.SkipBlocks
	in.timestamp = s.TimestampUS

	blk, blkAddr := newObj[blockObj](a)
	blk.vtbl = vtblAddr(&c.block)

	st := uint32(vcall(e.cdmPtr, cdmDecrypt, inAddr, blkAddr))
	buf := blk.buffer
	var out []byte
	if st == statusSuccess {
		if b, ok := lookup[*bufferObj](buf); ok {
			out = append([]byte(nil), b.data[:min(b.size, uint32(len(b.data)))]...)
		}
	}
	if buf != 0 {
		unpin(buf) // ownership of the buffer passed to us with the block
	}
	switch st {
	case statusSuccess:
		if buf == 0 {
			return nil, fmt.Errorf("%w: the CDM reported success without an output buffer", cdm.ErrDecrypt)
		}
		return out, nil
	case statusNoKey:
		return nil, cdm.ErrNoKey
	case statusDecryptError:
		return nil, fmt.Errorf("%w: the CDM reported a decryption error", cdm.ErrDecrypt)
	case statusDecodeError:
		return nil, fmt.Errorf("%w: the CDM reported a decode error", cdm.ErrDecode)
	case statusNeedMoreData:
		return nil, cdm.ErrNeedMoreData
	}
	return nil, fmt.Errorf("%w: CDM status %d", cdm.ErrDecrypt, st)
}

// Close destroys the CDM instance and deinitializes the module. It is safe to call more than once.
func (e *Engine) Close() error {
	e.shutdown(true)
	return nil
}

func (e *Engine) shutdown(destroy bool) {
	e.closeOnce.Do(func() {
		e.closed.Store(true)
		if destroy && e.cdmPtr != 0 {
			_ = e.thr.do(func() {
				vcall(e.cdmPtr, cdmDestroy)
				e.mod.Deinitialize()
			})
		}
		e.thr.close()
		close(e.stopClock)
		unregisterEngine(e)
		unpin(e.hostAddr)
		e.mod.Close()
	})
}
