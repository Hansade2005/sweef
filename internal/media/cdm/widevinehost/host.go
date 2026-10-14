//go:build amd64 || arm64

package widevinehost

import (
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"

	"github.com/sweefbrowser/sweef/internal/media/cdm"
)

// The host side of the CDM API: the function-pointer tables the CDM calls. There is one table per C++ class and
// it is shared by every object of that class (and by Host_10, _11 and _12: the three differ only in a trailing
// slot and in enum value ranges, so one table serves all). purego callbacks are never freed and at most about
// 2000 can exist, so the tables are built once per process.

type callbacks struct {
	host   [hostSlots]uintptr
	buffer [bufSlots]uintptr
	block  [blockSlots]uintptr
	file   [fileSlots]uintptr

	getHost uintptr // cdm GetCdmHostFunc
}

var (
	cbsOnce sync.Once
	cbs     callbacks
	cbsErr  error
)

func vtblAddr[T any](t *T) uintptr { return uintptr(unsafe.Pointer(t)) }

func theCallbacks() (*callbacks, error) {
	cbsOnce.Do(func() {
		c := &cbs
		cb := purego.NewCallback

		// ---- cdm::Host_10/_11/_12 ----
		c.host[hostAllocate] = cb(hostAllocateCB)
		c.host[hostSetTimer] = cb(func(this, delayMS, ctx uintptr) uintptr {
			if e := engineOf(this); e != nil {
				e.setTimer(int64(delayMS), ctx)
			}
			return 0
		})
		if c.host[hostGetCurrentWallTime], cbsErr = newWallTimeCallback(); cbsErr != nil {
			return
		}
		c.host[hostOnInitialized] = cb(func(this, success uintptr) uintptr {
			if e := engineOf(this); e != nil {
				e.onInitialized(success&0xff != 0)
			}
			return 0
		})
		c.host[hostOnResolveKeyStatusPromise] = cb(func(this, id, status uintptr) uintptr {
			if e := engineOf(this); e != nil {
				e.resolve(uint32(id), promiseResult{ok: true, keyStatus: uint32(status)})
			}
			return 0
		})
		c.host[hostOnResolveNewSession] = cb(func(this, id, sid, sidSize uintptr) uintptr {
			if e := engineOf(this); e != nil {
				r := promiseResult{ok: true}
				if sid != 0 {
					r.hasSession, r.sessionID = true, cString(sid, uint32(sidSize))
				}
				e.resolve(uint32(id), r)
			}
			return 0
		})
		c.host[hostOnResolvePromise] = cb(func(this, id uintptr) uintptr {
			if e := engineOf(this); e != nil {
				e.resolve(uint32(id), promiseResult{ok: true})
			}
			return 0
		})
		c.host[hostOnRejectPromise] = cb(func(this, id, exception, sysCode, msg, msgSize uintptr) uintptr {
			if e := engineOf(this); e != nil {
				e.resolve(uint32(id), promiseResult{exception: uint32(exception), systemCode: uint32(sysCode), message: cString(msg, uint32(msgSize))})
			}
			return 0
		})
		c.host[hostOnSessionMessage] = cb(func(this, sid, sidSize, typ, msg, msgSize uintptr) uintptr {
			if e := engineOf(this); e != nil {
				e.onSessionMessage(cString(sid, uint32(sidSize)), uint32(typ), cBytes(msg, uint32(msgSize)))
			}
			return 0
		})
		c.host[hostOnSessionKeysChange] = cb(func(this, sid, sidSize, additional, keys, count uintptr) uintptr {
			if e := engineOf(this); e != nil {
				e.onKeysChange(cString(sid, uint32(sidSize)), additional&0xff != 0, keys, uint32(count))
			}
			return 0
		})
		if c.host[hostOnExpirationChange], cbsErr = newExpirationCallback(func(this, sid, sidSize uintptr, t float64) {
			if e := engineOf(this); e != nil {
				e.onExpiration(cString(sid, uint32(sidSize)), t)
			}
		}); cbsErr != nil {
			return
		}
		c.host[hostOnSessionClosed] = cb(func(this, sid, sidSize uintptr) uintptr {
			if e := engineOf(this); e != nil {
				e.onSessionClosed(cString(sid, uint32(sidSize)))
			}
			return 0
		})
		c.host[hostSendPlatformChallenge] = cb(func(this, svc, svcSize, challenge, chSize uintptr) uintptr {
			if e := engineOf(this); e != nil {
				e.failPlatformChallenge()
			}
			return 0
		})
		c.host[hostEnableOutputProtection] = cb(func(this, mask uintptr) uintptr { return 0 })
		c.host[hostQueryOutputProtection] = cb(func(this uintptr) uintptr {
			if e := engineOf(this); e != nil {
				e.answerOutputProtection()
			}
			return 0
		})
		c.host[hostOnDeferredInitDone] = cb(func(this, stream, status uintptr) uintptr { return 0 })
		c.host[hostCreateFileIO] = cb(func(this, client uintptr) uintptr {
			if e := engineOf(this); e != nil {
				return e.createFileIO(client)
			}
			return 0
		})
		c.host[hostRequestStorageID] = cb(func(this, version uintptr) uintptr {
			if e := engineOf(this); e != nil {
				e.answerStorageID(uint32(version))
			}
			return 0
		})
		c.host[hostReportMetrics] = cb(func(this, name, value uintptr) uintptr { return 0 })

		// ---- cdm::Buffer ----
		c.buffer[bufDestroy] = cb(func(this uintptr) uintptr { unpin(this); return 0 })
		c.buffer[bufCapacity] = cb(func(this uintptr) uintptr {
			if b, ok := lookup[*bufferObj](this); ok {
				return uintptr(b.capacity)
			}
			return 0
		})
		c.buffer[bufData] = cb(func(this uintptr) uintptr {
			if b, ok := lookup[*bufferObj](this); ok {
				return uintptr(unsafe.Pointer(&b.data[0]))
			}
			return 0
		})
		c.buffer[bufSetSize] = cb(func(this, size uintptr) uintptr {
			if b, ok := lookup[*bufferObj](this); ok {
				b.size = min(uint32(size), b.capacity)
			}
			return 0
		})
		c.buffer[bufSize] = cb(func(this uintptr) uintptr {
			if b, ok := lookup[*bufferObj](this); ok {
				return uintptr(b.size)
			}
			return 0
		})

		// ---- cdm::DecryptedBlock ----
		c.block[blockSetDecryptedBuffer] = cb(func(this, buf uintptr) uintptr {
			if b, ok := lookup[*blockObj](this); ok {
				b.buffer = buf
			}
			return 0
		})
		c.block[blockDecryptedBuffer] = cb(func(this uintptr) uintptr {
			if b, ok := lookup[*blockObj](this); ok {
				return b.buffer
			}
			return 0
		})
		c.block[blockSetTimestamp] = cb(func(this, ts uintptr) uintptr {
			if b, ok := lookup[*blockObj](this); ok {
				b.timestamp = int64(ts)
			}
			return 0
		})
		c.block[blockTimestamp] = cb(func(this uintptr) uintptr {
			if b, ok := lookup[*blockObj](this); ok {
				return uintptr(b.timestamp)
			}
			return 0
		})

		// ---- cdm::FileIO ----
		c.file[fileOpen] = cb(func(this, name, nameSize uintptr) uintptr {
			if f, ok := lookup[*fileIOObj](this); ok {
				f.open(cString(name, uint32(nameSize)))
			}
			return 0
		})
		c.file[fileRead] = cb(func(this uintptr) uintptr {
			if f, ok := lookup[*fileIOObj](this); ok {
				f.read()
			}
			return 0
		})
		c.file[fileWrite] = cb(func(this, data, size uintptr) uintptr {
			if f, ok := lookup[*fileIOObj](this); ok {
				f.write(cBytes(data, uint32(size)))
			}
			return 0
		})
		c.file[fileClose] = cb(func(this uintptr) uintptr {
			if f, ok := lookup[*fileIOObj](this); ok {
				f.close()
			}
			return 0
		})

		// ---- GetCdmHostFunc ----
		c.getHost = cb(func(hostVersion, userData uintptr) uintptr {
			v := int32(uint32(hostVersion))
			if v < 10 || v > 12 {
				return 0
			}
			if e := engineByHandle(userData); e != nil {
				return e.hostAddr
			}
			return 0
		})
	})
	return &cbs, cbsErr
}

// ---- host-owned C++ objects ----
//
// Each starts with the table pointer that the C++ side dereferences; the Go fields after it are invisible to C.

type hostObj struct {
	vtbl uintptr
	eng  *Engine
}

func engineOf(this uintptr) *Engine {
	if h, ok := lookup[*hostObj](this); ok {
		return h.eng
	}
	return nil
}

// bufferObj is a cdm::Buffer. data is never empty so Data() always returns a valid address.
type bufferObj struct {
	vtbl     uintptr
	data     []byte
	capacity uint32
	size     uint32
}

func hostAllocateCB(this, capacity uintptr) uintptr {
	c, err := theCallbacks()
	if err != nil || capacity > 1<<30 {
		return 0
	}
	b := &bufferObj{vtbl: vtblAddr(&c.buffer), capacity: uint32(capacity)}
	b.data = make([]byte, max(int(capacity), 1))
	addr := uintptr(unsafe.Pointer(b))
	pin(addr, b)
	return addr
}

// blockObj is a cdm::DecryptedBlock.
type blockObj struct {
	vtbl      uintptr
	buffer    uintptr // cdm::Buffer* the CDM stored (a *bufferObj we handed out)
	timestamp int64
}

// ---- FileIO ----

var fileNameRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,256}$`)

// fileStore is the per-origin directory behind cdm::FileIO. Files are opened exclusively: a second FileIO that
// opens a file already open gets kInUse, as the API specifies.
type fileStore struct {
	dir  string
	mu   sync.Mutex
	open map[string]*fileIOObj
}

func newFileStore(dir string) (*fileStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &fileStore{dir: dir, open: map[string]*fileIOObj{}}, nil
}

func validFileName(n string) bool { return fileNameRE.MatchString(n) && n[0] != '_' }

// fileIOObj is a cdm::FileIO.
type fileIOObj struct {
	vtbl   uintptr
	eng    *Engine
	store  *fileStore
	client uintptr // cdm::FileIOClient* (CDM side)
	addr   uintptr

	mu   sync.Mutex
	name string // set once opened successfully
}

func (e *Engine) createFileIO(client uintptr) uintptr {
	e.storeMu.Lock()
	st := e.store
	e.storeMu.Unlock()
	if st == nil || client == 0 {
		return 0 // no persistent state allowed: the CDM must cope with a NULL FileIO
	}
	c, err := theCallbacks()
	if err != nil {
		return 0
	}
	f := &fileIOObj{vtbl: vtblAddr(&c.file), eng: e, store: st, client: client}
	f.addr = uintptr(unsafe.Pointer(f))
	pin(f.addr, f)
	return f.addr
}

func (f *fileIOObj) complete(slot int, args ...uintptr) {
	// "All responses will be called asynchronously": through the CDM thread's queue.
	f.eng.thr.post(func() { touchWallTime(); vcall(f.client, slot, args...) })
}

func (f *fileIOObj) path() string { return filepath.Join(f.store.dir, f.name) }

func (f *fileIOObj) open(name string) {
	if !validFileName(name) {
		f.complete(clientOnOpenComplete, fileIOStatusError)
		return
	}
	f.store.mu.Lock()
	if other := f.store.open[name]; other != nil && other != f {
		f.store.mu.Unlock()
		f.complete(clientOnOpenComplete, fileIOStatusInUse)
		return
	}
	file, err := os.OpenFile(filepath.Join(f.store.dir, name), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		f.store.mu.Unlock()
		f.complete(clientOnOpenComplete, fileIOStatusError)
		return
	}
	file.Close()
	f.store.open[name] = f
	f.store.mu.Unlock()
	f.mu.Lock()
	f.name = name
	f.mu.Unlock()
	f.complete(clientOnOpenComplete, fileIOStatusSuccess)
}

func (f *fileIOObj) opened() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.name != ""
}

func (f *fileIOObj) read() {
	if !f.opened() {
		f.complete(clientOnReadComplete, fileIOStatusError, 0, 0)
		return
	}
	data, err := os.ReadFile(f.path())
	if err != nil {
		f.complete(clientOnReadComplete, fileIOStatusError, 0, 0)
		return
	}
	// The data must stay valid until OnReadComplete returns: keep it pinned across the callback.
	f.eng.thr.post(func() {
		touchWallTime()
		a := &arena{}
		defer a.release()
		vcall(f.client, clientOnReadComplete, fileIOStatusSuccess, a.bytes(data), uintptr(len(data)))
	})
}

func (f *fileIOObj) write(data []byte) {
	if !f.opened() {
		f.complete(clientOnWriteComplete, fileIOStatusError)
		return
	}
	// atomic replace: a crash must not leave a half written certificate
	tmp := f.path() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		f.complete(clientOnWriteComplete, fileIOStatusError)
		return
	}
	if err := os.Rename(tmp, f.path()); err != nil {
		_ = os.Remove(tmp)
		f.complete(clientOnWriteComplete, fileIOStatusError)
		return
	}
	f.complete(clientOnWriteComplete, fileIOStatusSuccess)
}

func (f *fileIOObj) close() {
	f.mu.Lock()
	name := f.name
	f.mu.Unlock()
	if name != "" {
		f.store.mu.Lock()
		if f.store.open[name] == f {
			delete(f.store.open, name)
		}
		f.store.mu.Unlock()
	}
	unpin(f.addr)
}

// ---- host services answered by the Go side ----

func (e *Engine) setTimer(delayMS int64, ctx uintptr) {
	d := time.Duration(delayMS) * time.Millisecond
	if d < 0 {
		d = 0
	}
	time.AfterFunc(d, func() {
		e.thr.post(func() {
			if e.closed.Load() {
				return
			}
			touchWallTime()
			vcall(e.cdmPtr, cdmTimerExpired, ctx)
		})
	})
}

// failPlatformChallenge answers Host::SendPlatformChallenge: Sweef has no platform attestation, so the response is
// the documented "challenge failed" value (all NULL, zero length).
func (e *Engine) failPlatformChallenge() {
	e.thr.post(func() {
		a := &arena{}
		defer a.release()
		_, addr := newObj[platformChallengeResponse](a)
		vcall(e.cdmPtr, cdmOnPlatformChallengeResponse, addr)
	})
}

// answerOutputProtection reports honestly that there is no output protection: the link type is unknown and no
// HDCP is active (Sweef composites in software and cannot enable HDCP).
func (e *Engine) answerOutputProtection() {
	e.thr.post(func() {
		vcall(e.cdmPtr, cdmOnQueryOutputProtectionStatus, queryResultSucceeded, linkTypeUnknown, protectionNone)
	})
}

// answerStorageID reports that no storage ID is available (null pointer, zero size): the CDM must cope.
func (e *Engine) answerStorageID(version uint32) {
	e.thr.post(func() { vcall(e.cdmPtr, cdmOnStorageID, uintptr(version), 0, 0) })
}

// ---- event translation ----

func keyStatusName(s uint32) cdm.KeyStatusKind {
	switch s {
	case keyUsable:
		return cdm.KeyUsable
	case keyExpired:
		return cdm.KeyExpired
	case keyOutputRestricted:
		return cdm.KeyOutputRestricted
	case keyOutputDownscaled:
		return cdm.KeyOutputDownscaled
	case keyReleased:
		return cdm.KeyReleased
	case keyInternalError:
		return cdm.KeyInternalError
	}
	// status-pending, and KeyStatus_2 values this code does not know (usable-in-future): per the header, hosts must
	// tolerate new values; "pending" is the closest EME status.
	return cdm.KeyStatusPending
}

func messageTypeName(t uint32) cdm.MessageType {
	switch t {
	case msgLicenseRenewal:
		return cdm.MessageLicenseRenewal
	case msgLicenseRelease:
		return cdm.MessageLicenseRelease
	case msgIndividualizationReq:
		return cdm.MessageIndividualizationReq
	}
	return cdm.MessageLicenseRequest
}

func exceptionName(x uint32) cdm.Exception {
	switch x {
	case exNotSupportedError:
		return cdm.ExceptionNotSupportedError
	case exInvalidStateError:
		return cdm.ExceptionInvalidStateError
	case exQuotaExceeded:
		return cdm.ExceptionQuotaExceeded
	}
	return cdm.ExceptionTypeError
}

func (e *Engine) sinkOrNil() cdm.EventSink {
	e.sinkMu.RLock()
	defer e.sinkMu.RUnlock()
	return e.sink
}

func (e *Engine) onSessionMessage(sid string, typ uint32, msg []byte) {
	if s := e.sinkOrNil(); s != nil {
		s.SessionMessage(sid, cdm.Message{Type: messageTypeName(typ), Data: msg})
	}
}

func (e *Engine) onKeysChange(sid string, additional bool, keys uintptr, count uint32) {
	s := e.sinkOrNil()
	if s == nil {
		return
	}
	var out []cdm.KeyStatus
	if keys != 0 && count > 0 && count < 1<<16 {
		for _, k := range unsafe.Slice((*keyInformation)(ptrAt(keys)), int(count)) {
			out = append(out, cdm.KeyStatus{KeyID: cBytes(k.keyID, k.keyIDSize), Status: keyStatusName(k.status), SystemCode: k.systemCode})
		}
	}
	s.KeyStatuses(sid, additional, out)
}

func (e *Engine) onExpiration(sid string, t float64) {
	s := e.sinkOrNil()
	if s == nil {
		return
	}
	var tm time.Time // zero: no expiration (the CDM reports 0)
	if t > 0 {
		tm = time.Unix(int64(t), int64((t-float64(int64(t)))*1e9))
	}
	s.Expiration(sid, tm)
}

func (e *Engine) onSessionClosed(sid string) {
	if s := e.sinkOrNil(); s != nil {
		s.SessionClosed(sid)
	}
}
