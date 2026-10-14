//go:build (amd64 || arm64) && !windows

package widevinehost

import (
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/ebitengine/purego"

	"github.com/sweefbrowser/sweef/internal/media/cdm"
)

// A fake Content Decryption Module written in Go that speaks the real binary interface: its
// cdm::ContentDecryptionModule_N object is a pointer to a function-pointer table of purego callbacks, it reads
// cdm::InputBuffer_2 out of raw memory, and everything it tells the host goes through the host's vtable with the
// C++ calling convention. Only the dlopen step differs from a real Widevine library. The sandbox has no
// libwidevinecdm to test against; this is the closest honest stand-in.

type fakeCDM struct {
	vtbl uintptr // first word: table pointer, as in C++
	host uintptr // cdm::Host* handed out by GetCdmHostFunc
	ver  int

	mu        sync.Mutex
	sessions  map[string]bool
	keys      map[string][]byte // key id (raw) -> key
	nextSess  int
	initArgs  [3]bool
	timerCtx  uintptr
	certRead  []byte
	fileIOLog []string
	destroyed bool

	// answers the host sent back to the CDM
	platformChallengeFailed atomic.Bool
	outputProtection        atomic.Value // [3]uint32
	storageIDNull           atomic.Bool

	fileio uintptr // the host's FileIO object
}

// fakeClient is the CDM-side cdm::FileIOClient.
type fakeClient struct {
	vtbl uintptr
	cdm  *fakeCDM
	step int
}

var (
	fakeOnce   sync.Once
	fakeVtbl   [cdmSlots]uintptr
	clientVtbl [3]uintptr
	curFake    atomic.Pointer[fakeCDM]
)

func fakeOf(this uintptr) *fakeCDM {
	f, _ := lookup[*fakeCDM](this)
	return f
}

func (f *fakeCDM) hostCall(slot int, args ...uintptr) uintptr { return vcall(f.host, slot, args...) }

func (f *fakeCDM) hostSlotAddr(slot int) uintptr {
	return readPtr(readPtr(f.host) + uintptr(slot)*unsafe.Sizeof(uintptr(0)))
}

func strPtr(a *arena, s string) (uintptr, uintptr) { return a.bytes([]byte(s)), uintptr(len(s)) }

func (f *fakeCDM) reject(id, ex, code uintptr, msg string) {
	a := &arena{}
	defer a.release()
	p, n := strPtr(a, msg)
	f.hostCall(hostOnRejectPromise, id, ex, code, p, n)
}

func initFake() {
	fakeOnce.Do(func() {
		cb := purego.NewCallback
		fakeVtbl[cdmInitialize] = cb(func(this, distinct, persistent, hw uintptr) uintptr {
			f := fakeOf(this)
			f.mu.Lock()
			f.initArgs = [3]bool{distinct&0xff != 0, persistent&0xff != 0, hw&0xff != 0}
			f.mu.Unlock()
			f.hostCall(hostSetTimer, 20, 0xABCD)
			if persistent&0xff != 0 {
				// store a "device certificate" through cdm::FileIO, the way the real CDM provisions itself
				cl := &fakeClient{vtbl: uintptr(unsafe.Pointer(&clientVtbl)), cdm: f}
				pin(uintptr(unsafe.Pointer(cl)), cl)
				io := f.hostCall(hostCreateFileIO, uintptr(unsafe.Pointer(cl)))
				f.mu.Lock()
				f.fileio = io
				f.mu.Unlock()
				if io != 0 {
					a := &arena{}
					defer a.release()
					p, n := strPtr(a, "cert.bin")
					vcall(io, fileOpen, p, n)
				}
			}
			f.hostCall(hostOnInitialized, 1)
			return 0
		})
		fakeVtbl[cdmGetStatusForPolicy] = cb(func(this, id, policy uintptr) uintptr { return 0 })
		fakeVtbl[cdmSetServerCertificate] = cb(func(this, id, cert, size uintptr) uintptr {
			fakeOf(this).reject(id, exNotSupportedError, 0, "no server certificates")
			return 0
		})
		fakeVtbl[cdmCreateSessionAndGenerateReq] = cb(func(this, id, typ, initType, data, size uintptr) uintptr {
			f := fakeOf(this)
			if initType > initWebM {
				f.reject(id, exTypeError, 0, "bad init data type")
				return 0
			}
			f.mu.Lock()
			f.nextSess++
			sid := fmt.Sprintf("wv-session-%d", f.nextSess)
			f.sessions[sid] = true
			f.mu.Unlock()
			init := cBytes(data, uint32(size))
			a := &arena{}
			p, n := strPtr(a, sid)
			f.hostCall(hostOnResolveNewSession, id, p, n)
			a.release()
			// The license request comes from another thread, a little later, like a real CDM's worker.
			go func() {
				a := &arena{}
				defer a.release()
				p, n := strPtr(a, sid)
				mp, mn := strPtr(a, "LICENSE-REQUEST:"+hex.EncodeToString(init))
				f.hostCall(hostOnSessionMessage, p, n, msgLicenseRequest, mp, mn)
			}()
			// and it pokes the optional host services once
			f.hostCall(hostQueryOutputProtection)
			f.hostCall(hostRequestStorageID, 1)
			sp, sn := strPtr(a, "service")
			cp, cn := strPtr(a, "challenge")
			f.hostCall(hostSendPlatformChallenge, sp, sn, cp, cn)
			return 0
		})
		fakeVtbl[cdmLoadSession] = cb(func(this, id, typ, sid, sidSize uintptr) uintptr {
			f := fakeOf(this)
			name := cString(sid, uint32(sidSize))
			f.mu.Lock()
			known := f.sessions[name]
			f.mu.Unlock()
			if known {
				f.hostCall(hostOnResolveNewSession, id, sid, sidSize)
			} else {
				f.hostCall(hostOnResolveNewSession, id, 0, 0)
			}
			return 0
		})
		fakeVtbl[cdmUpdateSession] = cb(func(this, id, sid, sidSize, resp, respSize uintptr) uintptr {
			f := fakeOf(this)
			text := string(cBytes(resp, uint32(respSize)))
			if text == "BAD" {
				f.reject(id, exTypeError, 77, "bad license")
				return 0
			}
			// "keys:<kidhex>=<keyhex>;<kidhex>=<keyhex>"
			a := &arena{}
			defer a.release()
			var infos []keyInformation
			for _, kv := range strings.Split(strings.TrimPrefix(text, "keys:"), ";") {
				parts := strings.SplitN(kv, "=", 2)
				if len(parts) != 2 {
					continue
				}
				kid, _ := hex.DecodeString(parts[0])
				key, _ := hex.DecodeString(parts[1])
				f.mu.Lock()
				f.keys[string(kid)] = key
				f.mu.Unlock()
				infos = append(infos, keyInformation{keyID: a.bytes(kid), keyIDSize: uint32(len(kid)), status: keyUsable})
			}
			// one entry that is not usable, to see statuses mapped
			infos = append(infos, keyInformation{keyID: a.bytes([]byte("0123456789abcdef")), keyIDSize: 16, status: keyOutputRestricted, systemCode: 9})
			arr := a.keep(uintptr(unsafe.Pointer(&infos[0])), infos)
			f.hostCall(hostOnSessionKeysChange, sid, sidSize, 1, arr, uintptr(len(infos)))
			// OnExpirationChange(const char*, uint32, double) takes a double: a typed call so it lands in xmm0
			var expiry func(this, sid, size uintptr, t float64)
			purego.RegisterFunc(&expiry, f.hostSlotAddr(hostOnExpirationChange))
			expiry(f.host, sid, sidSize, 4102444800.0)
			f.hostCall(hostOnResolvePromise, id)
			return 0
		})
		fakeVtbl[cdmCloseSession] = cb(func(this, id, sid, sidSize uintptr) uintptr {
			f := fakeOf(this)
			f.hostCall(hostOnSessionClosed, sid, sidSize)
			f.hostCall(hostOnResolvePromise, id)
			return 0
		})
		fakeVtbl[cdmRemoveSession] = cb(func(this, id, sid, sidSize uintptr) uintptr {
			f := fakeOf(this)
			a := &arena{}
			defer a.release()
			mp, mn := strPtr(a, "RELEASE")
			f.hostCall(hostOnSessionMessage, sid, sidSize, msgLicenseRelease, mp, mn)
			f.hostCall(hostOnResolvePromise, id)
			return 0
		})
		fakeVtbl[cdmTimerExpired] = cb(func(this, ctx uintptr) uintptr {
			f := fakeOf(this)
			f.mu.Lock()
			f.timerCtx = ctx
			f.mu.Unlock()
			return 0
		})
		fakeVtbl[cdmDecrypt] = cb(func(this, inAddr, blockAddr uintptr) uintptr {
			f := fakeOf(this)
			in := (*inputBuffer2)(ptrAt(inAddr)) // read the struct exactly where C++ would
			if in.scheme == schemeUnencrypted {
				return f.emit(blockAddr, cBytes(in.data, in.dataSize), in.timestamp)
			}
			kid := cBytes(in.keyID, in.keyIDSize)
			f.mu.Lock()
			key := f.keys[string(kid)]
			f.mu.Unlock()
			if key == nil {
				return statusNoKey
			}
			s := &cdm.Sample{Data: cBytes(in.data, in.dataSize), KeyID: kid, IV: cBytes(in.iv, in.ivSize),
				Pattern: cdm.Pattern{CryptBlocks: in.cryptBlocks, SkipBlocks: in.skipBlocks}, TimestampUS: in.timestamp}
			switch in.scheme {
			case schemeCenc:
				s.Scheme = cdm.SchemeCenc
			case schemeCbcs:
				s.Scheme = cdm.SchemeCbcs
			}
			if in.numSubsamples > 0 {
				for _, e := range unsafe.Slice((*subsampleEntry)(ptrAt(in.subsamples)), int(in.numSubsamples)) {
					s.Subsamples = append(s.Subsamples, cdm.Subsample{ClearBytes: e.clearBytes, CipherBytes: e.cipherBytes})
				}
			}
			out, err := cdm.DecryptSample(key, s)
			if err != nil {
				return statusDecryptError
			}
			return f.emit(blockAddr, out, in.timestamp)
		})
		fakeVtbl[cdmInitializeAudioDecoder] = cb(func(this, cfg uintptr) uintptr { return statusInitializationError })
		fakeVtbl[cdmInitializeVideoDecoder] = cb(func(this, cfg uintptr) uintptr { return statusInitializationError })
		fakeVtbl[cdmDeinitializeDecoder] = cb(func(this, t uintptr) uintptr { return 0 })
		fakeVtbl[cdmResetDecoder] = cb(func(this, t uintptr) uintptr { return 0 })
		fakeVtbl[cdmDecryptAndDecodeFrame] = cb(func(this, in, frame uintptr) uintptr { return statusDecodeError })
		fakeVtbl[cdmDecryptAndDecodeSamples] = cb(func(this, in, frames uintptr) uintptr { return statusDecodeError })
		fakeVtbl[cdmOnPlatformChallengeResponse] = cb(func(this, resp uintptr) uintptr {
			r := (*platformChallengeResponse)(ptrAt(resp))
			fakeOf(this).platformChallengeFailed.Store(r.signedData == 0 && r.signedDataLength == 0 && r.platformCertificate == 0)
			return 0
		})
		fakeVtbl[cdmOnQueryOutputProtectionStatus] = cb(func(this, result, link, prot uintptr) uintptr {
			fakeOf(this).outputProtection.Store([3]uint32{uint32(result), uint32(link), uint32(prot)})
			return 0
		})
		fakeVtbl[cdmOnStorageID] = cb(func(this, version, id, size uintptr) uintptr {
			fakeOf(this).storageIDNull.Store(id == 0 && size == 0 && version == 1)
			return 0
		})
		fakeVtbl[cdmDestroy] = cb(func(this uintptr) uintptr {
			f := fakeOf(this)
			f.mu.Lock()
			f.destroyed = true
			f.mu.Unlock()
			return 0
		})

		// cdm::FileIOClient: open, then write, then read back, then close.
		clientVtbl[clientOnOpenComplete] = cb(func(this, status uintptr) uintptr {
			c, _ := lookup[*fakeClient](this)
			c.cdm.logIO(fmt.Sprintf("open=%d", status))
			if status == fileIOStatusSuccess {
				a := &arena{}
				defer a.release()
				p, n := strPtr(a, "DEVICE-CERT-v1")
				vcall(c.cdm.fileio, fileWrite, p, n)
			}
			return 0
		})
		clientVtbl[clientOnWriteComplete] = cb(func(this, status uintptr) uintptr {
			c, _ := lookup[*fakeClient](this)
			c.cdm.logIO(fmt.Sprintf("write=%d", status))
			if status == fileIOStatusSuccess {
				vcall(c.cdm.fileio, fileRead)
			}
			return 0
		})
		clientVtbl[clientOnReadComplete] = cb(func(this, status, data, size uintptr) uintptr {
			c, _ := lookup[*fakeClient](this)
			c.cdm.logIO(fmt.Sprintf("read=%d", status))
			c.cdm.mu.Lock()
			c.cdm.certRead = cBytes(data, uint32(size))
			io := c.cdm.fileio
			c.cdm.mu.Unlock()
			vcall(io, fileClose)
			return 0
		})
	})
}

func (f *fakeCDM) logIO(s string) {
	f.mu.Lock()
	f.fileIOLog = append(f.fileIOLog, s)
	f.mu.Unlock()
}

// emit hands out decrypted bytes the way a CDM does: Host::Allocate a Buffer, fill it through Buffer::Data and
// SetSize, give it to the DecryptedBlock.
func (f *fakeCDM) emit(block uintptr, data []byte, ts int64) uintptr {
	buf := f.hostCall(hostAllocate, uintptr(len(data)))
	if buf == 0 {
		return statusDecryptError
	}
	if vcall(buf, bufCapacity) < uintptr(len(data)) {
		return statusDecryptError
	}
	if len(data) > 0 {
		dst := unsafe.Slice((*byte)(ptrAt(vcall(buf, bufData))), len(data))
		copy(dst, data)
	}
	vcall(buf, bufSetSize, uintptr(len(data)))
	vcall(block, blockSetDecryptedBuffer, buf)
	vcall(block, blockSetTimestamp, uintptr(ts))
	return statusSuccess
}

// fakeModule is a Module whose CreateInstance builds a fakeCDM.
type fakeModule struct {
	versions []int // interface versions the fake supports
	mu       sync.Mutex
	inits    int
	deinits  int
	verified [][]HostFileSpec
	asked    []int
}

func (m *fakeModule) Initialize() error { m.mu.Lock(); m.inits++; m.mu.Unlock(); return nil }
func (m *fakeModule) Deinitialize()     { m.mu.Lock(); m.deinits++; m.mu.Unlock() }
func (m *fakeModule) Version() string   { return "9.9.9.fake" }
func (m *fakeModule) Close()            {}
func (m *fakeModule) VerifyHost(files []HostFileSpec) (bool, bool, error) {
	m.mu.Lock()
	m.verified = append(m.verified, files)
	m.mu.Unlock()
	return true, true, nil
}

func (m *fakeModule) CreateInstance(version int, keySystem string, getHost, userData uintptr) uintptr {
	m.mu.Lock()
	m.asked = append(m.asked, version)
	m.mu.Unlock()
	ok := false
	for _, v := range m.versions {
		ok = ok || v == version
	}
	if !ok || keySystem != cdm.WidevineKeySystem {
		return 0
	}
	initFake()
	// ask for the host through the callback, exactly as a CDM does
	r, _, _ := purego.SyscallN(getHost, uintptr(version), userData)
	if r == 0 {
		return 0
	}
	f := &fakeCDM{vtbl: uintptr(unsafe.Pointer(&fakeVtbl)), host: r, ver: version, sessions: map[string]bool{}, keys: map[string][]byte{}}
	addr := uintptr(unsafe.Pointer(f))
	pin(addr, f)
	curFake.Store(f)
	return addr
}
