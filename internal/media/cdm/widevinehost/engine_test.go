//go:build (amd64 || arm64) && !windows

package widevinehost

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"

	"github.com/sweefbrowser/sweef/internal/media/cdm"
)

func randBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type stack struct {
	r    *cdm.Remote
	eng  *Engine
	mod  *fakeModule
	fake *fakeCDM
	dir  string
}

// newStack builds browser side Remote -> framed protocol -> cdm.Serve -> this package's Engine -> purego host ->
// fake CDM through its vtable.
func newStack(t *testing.T, fakeVersions, wanted []int, persist bool) *stack {
	t.Helper()
	mod := &fakeModule{versions: fakeVersions}
	eng, err := Open(Config{Module: mod, HostFiles: []HostFileSpec{}, InterfaceVersions: wanted})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	c, s := net.Pipe()
	go cdm.Serve(context.Background(), s, eng)
	init := cdm.InitParams{Origin: "https://example.com", PersistentState: persist}
	if persist {
		init.StorageDir = filepath.Join(dir, "store")
	}
	r, err := cdm.NewRemote(context.Background(), c, init, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return &stack{r: r, eng: eng, mod: mod, fake: curFake.Load(), dir: dir}
}

func kidKey() (kid, key []byte) { return randBytes(16), randBytes(16) }

func license(kid, key []byte) []byte {
	return []byte("keys:" + hex.EncodeToString(kid) + "=" + hex.EncodeToString(key))
}

func TestEngineFullStackThroughFakeCDM(t *testing.T) {
	st := newStack(t, []int{10, 11}, nil, true)
	ctx := context.Background()
	info := st.r.Info()
	if info.CDMVersion != "9.9.9.fake" || info.InterfaceVersion != 11 || info.KeySystem != cdm.WidevineKeySystem || info.Mode != cdm.ModeDecrypt {
		t.Fatalf("%+v", info)
	}
	if st.mod.inits != 1 {
		t.Fatalf("InitializeCdmModule called %d times", st.mod.inits)
	}
	f := st.fake
	f.mu.Lock()
	args := f.initArgs
	f.mu.Unlock()
	if args != [3]bool{false, true, false} {
		t.Fatalf("Initialize(distinctive, persistent, hw) = %v", args)
	}

	// cdm::FileIO round trip: open, write, read, close, with the data really on disk
	waitFor(t, "FileIO round trip", func() bool { f.mu.Lock(); defer f.mu.Unlock(); return string(f.certRead) == "DEVICE-CERT-v1" })
	got, err := os.ReadFile(filepath.Join(st.dir, "store", "cert.bin"))
	if err != nil || string(got) != "DEVICE-CERT-v1" {
		t.Fatalf("stored certificate %q (%v)", got, err)
	}
	f.mu.Lock()
	log := append([]string(nil), f.fileIOLog...)
	f.mu.Unlock()
	if !reflect.DeepEqual(log, []string{"open=0", "write=0", "read=0"}) {
		t.Fatalf("FileIO log %v", log)
	}
	// Host::SetTimer fired TimerExpired with the context pointer
	waitFor(t, "TimerExpired", func() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.timerCtx == 0xABCD })

	if ok, err := st.r.SetServerCertificate(ctx, []byte("c")); ok || err != nil {
		t.Fatalf("SetServerCertificate = %v, %v (NotSupportedError means \"false\")", ok, err)
	}

	kid, key := kidKey()
	sess, err := st.r.CreateSession(ctx, cdm.SessionTemporary)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := sess.GenerateRequest(ctx, "cenc", []byte("INITDATA"))
	if err != nil {
		t.Fatal(err)
	}
	if msg.Type != cdm.MessageLicenseRequest || string(msg.Data) != "LICENSE-REQUEST:"+hex.EncodeToString([]byte("INITDATA")) {
		t.Fatalf("message %q %q", msg.Type, msg.Data)
	}
	if sess.ID() != "wv-session-1" {
		t.Fatalf("session id %q", sess.ID())
	}
	// services the CDM asked the host for
	waitFor(t, "platform challenge answer", func() bool { return f.platformChallengeFailed.Load() })
	waitFor(t, "output protection answer", func() bool { return f.outputProtection.Load() != nil })
	if v := f.outputProtection.Load().([3]uint32); v != [3]uint32{queryResultSucceeded, linkTypeUnknown, protectionNone} {
		t.Fatalf("output protection answer %v: the host must not claim protection it does not have", v)
	}
	waitFor(t, "storage id answer", func() bool { return f.storageIDNull.Load() })

	// a rejected license surfaces the exception, system code and message
	var ce *cdm.Error
	if err := sess.Update(ctx, []byte("BAD")); !errors.As(err, &ce) || ce.Exception != cdm.ExceptionTypeError || ce.SystemCode != 77 || ce.Message != "bad license" {
		t.Fatalf("rejected update: %v", err)
	}
	// a good license: key statuses and expiration are visible when Update returns
	if err := sess.Update(ctx, license(kid, key)); err != nil {
		t.Fatal(err)
	}
	ks := sess.KeyStatuses()
	if len(ks) != 2 || ks[0].Status != cdm.KeyUsable || !bytes.Equal(ks[0].KeyID, kid) ||
		ks[1].Status != cdm.KeyOutputRestricted || ks[1].SystemCode != 9 || string(ks[1].KeyID) != "0123456789abcdef" {
		t.Fatalf("key statuses %+v", ks)
	}
	if !sess.Expiration().Equal(time.Unix(4102444800, 0)) {
		t.Fatalf("expiration %v", sess.Expiration())
	}

	// decrypt through every layer
	plain := randBytes(4000)
	subs := []cdm.Subsample{{ClearBytes: 20, CipherBytes: 1000}, {ClearBytes: 0, CipherBytes: 1600}, {ClearBytes: 3, CipherBytes: 1377}}
	iv8 := randBytes(8)
	encCenc := encryptCenc(t, key, iv8, subs, plain)
	out, err := st.r.Decrypt(ctx, &cdm.Sample{Data: encCenc, KeyID: kid, IV: iv8, Subsamples: subs, Scheme: cdm.SchemeCenc, TimestampUS: 1234})
	if err != nil || !bytes.Equal(out, plain) {
		t.Fatalf("cenc decrypt: %v (equal=%v)", err, bytes.Equal(out, plain))
	}
	iv16 := randBytes(16)
	encCbcs := encryptCbcsWhole(t, key, iv16, cdm.Pattern{CryptBlocks: 1, SkipBlocks: 9}, plain)
	out, err = st.r.Decrypt(ctx, &cdm.Sample{Data: encCbcs, KeyID: kid, IV: iv16, Scheme: cdm.SchemeCbcs, Pattern: cdm.Pattern{CryptBlocks: 1, SkipBlocks: 9}})
	if err != nil || !bytes.Equal(out, plain) {
		t.Fatalf("cbcs decrypt: %v (equal=%v)", err, bytes.Equal(out, plain))
	}
	// a clear sample never needs the CDM
	if out, err := st.r.Decrypt(ctx, &cdm.Sample{Data: plain}); err != nil || !bytes.Equal(out, plain) {
		t.Fatalf("clear sample: %v", err)
	}
	// CDM status codes map to the typed errors
	if _, err := st.r.Decrypt(ctx, &cdm.Sample{Data: encCenc, KeyID: randBytes(16), IV: iv8, Scheme: cdm.SchemeCenc}); !errors.Is(err, cdm.ErrNoKey) {
		t.Fatalf("kNoKey: %v", err)
	}
	if _, err := st.r.Decrypt(ctx, &cdm.Sample{Data: encCenc, KeyID: kid, IV: randBytes(5), Scheme: cdm.SchemeCenc}); !errors.Is(err, cdm.ErrDecrypt) {
		t.Fatalf("kDecryptError: %v", err)
	}
	if _, err := st.r.Decrypt(ctx, &cdm.Sample{Data: encCenc, KeyID: kid, IV: iv8, Scheme: cdm.SchemeCens}); !errors.Is(err, cdm.ErrUnsupported) {
		t.Fatalf("cens is not in the CDM API: %v", err)
	}
	// all the buffers the CDM allocated were released
	n := 0
	pins.Range(func(k, v any) bool {
		if _, ok := v.(*bufferObj); ok {
			n++
		}
		return true
	})
	if n != 0 {
		t.Fatalf("%d cdm::Buffer objects leaked", n)
	}

	// LoadSession: known and unknown ids (NULL session id means "not found")
	s2, _ := st.r.CreateSession(ctx, cdm.SessionPersistentLicense)
	if found, err := s2.Load(ctx, "wv-session-1"); err != nil || !found {
		t.Fatalf("load known: %v %v", found, err)
	}
	s3, _ := st.r.CreateSession(ctx, cdm.SessionPersistentLicense)
	if found, err := s3.Load(ctx, "nope"); err != nil || found {
		t.Fatalf("load unknown: %v %v", found, err)
	}
	// Remove emits a license-release message
	if err := s2.Remove(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-s2.Events():
		if ev.Type != cdm.EventMessage || ev.Message.Type != cdm.MessageLicenseRelease || string(ev.Message.Data) != "RELEASE" {
			t.Fatalf("%+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no release message")
	}
	// unsupported init data types are refused before reaching the CDM
	s4, _ := st.r.CreateSession(ctx, cdm.SessionTemporary)
	if _, err := s4.GenerateRequest(ctx, "skd", []byte("x")); !errors.As(err, &ce) || ce.Exception != cdm.ExceptionNotSupportedError {
		t.Fatalf("skd: %v", err)
	}

	if err := sess.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.r.Close(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "Destroy and DeinitializeCdmModule", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		st.mod.mu.Lock()
		defer st.mod.mu.Unlock()
		return f.destroyed && st.mod.deinits == 1
	})
}

func TestEngineFallsBackToInterfaceVersion10(t *testing.T) {
	st := newStack(t, []int{10}, nil, false)
	if st.r.Info().InterfaceVersion != 10 {
		t.Fatalf("%+v", st.r.Info())
	}
	if !reflect.DeepEqual(st.mod.asked, []int{11, 10}) {
		t.Fatalf("CreateCdmInstance was asked for %v", st.mod.asked)
	}
	// persistent state off: the CDM got no FileIO and was told so
	f := st.fake
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.initArgs[1] || f.fileio != 0 {
		t.Fatalf("persistent state must be off: %v fileio=%x", f.initArgs, f.fileio)
	}
}

func TestEngineRejectsUnknownVersionsAndHonoursPreference(t *testing.T) {
	if got := wantedVersions([]int{10, 12, 9, 11, 12, 13}); !reflect.DeepEqual(got, []int{12, 11, 10}) {
		t.Fatalf("%v", got)
	}
	if got := wantedVersions(nil); !reflect.DeepEqual(got, []int{11, 10}) {
		t.Fatalf("%v", got)
	}
	mod := &fakeModule{versions: []int{9}}
	if _, err := Open(Config{Module: mod, HostFiles: []HostFileSpec{}}); err == nil {
		t.Fatal("a CDM without interface 10..12 must be refused")
	}
	// the failed Open leaves nothing behind
	if _, err := Open(Config{}); err == nil {
		t.Fatal("no library")
	}
	if _, err := Open(Config{Library: filepath.Join(t.TempDir(), "missing.so")}); err == nil {
		t.Fatal("missing library")
	}
	if _, err := Open(Config{Module: &fakeModule{versions: []int{12}}, HostFiles: []HostFileSpec{}, InterfaceVersions: []int{12}}); err != nil {
		t.Fatalf("interface 12: %v", err)
	}
}

func TestEngineVerifiesHostFiles(t *testing.T) {
	dir := t.TempDir()
	lib := filepath.Join(dir, "libwidevinecdm.so")
	_ = os.WriteFile(lib, []byte("lib"), 0o755)
	_ = os.WriteFile(lib+".sig", []byte("sig"), 0o644)
	exe, _ := os.Executable()
	mod := &fakeModule{versions: []int{11}}
	// default host files: this process' executable (no .sig next to the test binary) and the library with its .sig
	eng, err := Open(Config{Module: mod, Library: lib})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	if len(mod.verified) != 1 || len(mod.verified[0]) != 2 {
		t.Fatalf("verified %+v", mod.verified)
	}
	h := mod.verified[0]
	if h[0].Path != exe || h[0].Sig != "" || h[1].Path != lib || h[1].Sig != lib+".sig" {
		t.Fatalf("%+v", h)
	}
	if eng.Info().VMPSigned {
		t.Fatal("no .sig next to the executable: the engine must not claim a VMP signature")
	}
}

func TestBuildHostFilesLayoutAsTheCDMReadsIt(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "host.bin")
	b := filepath.Join(dir, "lib.so")
	_ = os.WriteFile(a, []byte("AAAA"), 0o755)
	_ = os.WriteFile(a+".sig", []byte("SIGSIG"), 0o644)
	_ = os.WriteFile(b, []byte("BBBBB"), 0o755)
	arr, n, release, err := buildHostFiles([]HostFileSpec{{Path: a, Sig: a + ".sig"}, {Path: b}})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if n != 2 || unsafe.Sizeof(hostFile{}) != 16 {
		t.Fatalf("n=%d size=%d", n, unsafe.Sizeof(hostFile{}))
	}
	files := unsafe.Slice((*hostFile)(ptrAt(arr)), n)
	read := func(fd int32) string {
		var buf [16]byte
		m, err := syscall.Read(int(fd), buf[:])
		if err != nil {
			t.Fatal(err)
		}
		syscall.Close(int(fd))
		return string(buf[:m])
	}
	nul := func(p uintptr) string {
		var s []byte
		for c := *(*byte)(ptrAt(p)); c != 0; c = *(*byte)(ptrAt(p + uintptr(len(s)))) {
			s = append(s, c)
		}
		return string(s)
	}
	if nul(files[0].path) != a || read(files[0].file) != "AAAA" || read(files[0].sig) != "SIGSIG" {
		t.Fatalf("first host file wrong: %+v", files[0])
	}
	if nul(files[1].path) != b || read(files[1].file) != "BBBBB" || files[1].sig != -1 {
		t.Fatalf("second host file wrong: %+v", files[1])
	}
	if _, _, _, err := buildHostFiles([]HostFileSpec{{Path: filepath.Join(dir, "missing")}}); err == nil {
		t.Fatal("a missing host file must be an error")
	}
}

// ---- ABI layout assertions ----

func TestABILayouts(t *testing.T) {
	type off struct {
		name string
		got  uintptr
		want uintptr
	}
	var in inputBuffer2
	var ki keyInformation
	var pc platformChallengeResponse
	checks := []off{
		{"InputBuffer_2 size", unsafe.Sizeof(in), 80},
		{"data", unsafe.Offsetof(in.data), 0},
		{"data_size", unsafe.Offsetof(in.dataSize), 8},
		{"encryption_scheme", unsafe.Offsetof(in.scheme), 12},
		{"key_id", unsafe.Offsetof(in.keyID), 16},
		{"key_id_size", unsafe.Offsetof(in.keyIDSize), 24},
		{"iv", unsafe.Offsetof(in.iv), 32},
		{"iv_size", unsafe.Offsetof(in.ivSize), 40},
		{"subsamples", unsafe.Offsetof(in.subsamples), 48},
		{"num_subsamples", unsafe.Offsetof(in.numSubsamples), 56},
		{"pattern.crypt", unsafe.Offsetof(in.cryptBlocks), 64},
		{"pattern.skip", unsafe.Offsetof(in.skipBlocks), 68},
		{"timestamp", unsafe.Offsetof(in.timestamp), 72},
		{"SubsampleEntry size", unsafe.Sizeof(subsampleEntry{}), 8},
		{"KeyInformation size", unsafe.Sizeof(ki), 24},
		{"key_id_size", unsafe.Offsetof(ki.keyIDSize), 8},
		{"status", unsafe.Offsetof(ki.status), 12},
		{"system_code", unsafe.Offsetof(ki.systemCode), 16},
		{"PlatformChallengeResponse size", unsafe.Sizeof(pc), 48},
		{"signed_data_signature", unsafe.Offsetof(pc.signature), 16},
		{"platform_key_certificate", unsafe.Offsetof(pc.platformCertificate), 32},
		{"AudioDecoderConfig_2 size", unsafe.Sizeof(audioDecoderConfig2{}), 32},
		{"VideoDecoderConfig_2 size", unsafe.Sizeof(videoDecoderConfig2{}), 40},
		{"VideoDecoderConfig_3 size", unsafe.Sizeof(videoDecoderConfig3{}), 40},
		{"VideoDecoderConfig_2.coded_size", unsafe.Offsetof(videoDecoderConfig2{}.width), 16},
		{"VideoDecoderConfig_3.coded_size", unsafe.Offsetof(videoDecoderConfig3{}.width), 16},
		{"VideoDecoderConfig_3.extra_data", unsafe.Offsetof(videoDecoderConfig3{}.extraData), 24},
		{"VideoDecoderConfig_3.encryption_scheme", unsafe.Offsetof(videoDecoderConfig3{}.scheme), 36},
		{"AudioDecoderConfig_2.extra_data", unsafe.Offsetof(audioDecoderConfig2{}.extraData), 16},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
	if unsafe.Sizeof(uintptr(0)) != 8 {
		t.Fatal("64-bit only")
	}
	// vtable slot counts: Host_10 has 18 methods, Host_11/12 19; CDM_10/11/12 have 20; Buffer 5; DecryptedBlock 4; FileIO 4
	for name, p := range map[string][2]int{
		"Host_11 ReportMetrics is the last slot": {hostReportMetrics, hostSlots - 1},
		"Host_10 RequestStorageId precedes it":   {hostRequestStorageID, hostReportMetrics - 1},
		"CDM Destroy is the last method":         {cdmDestroy, cdmSlots - 1},
		"CDM OnStorageId":                        {cdmOnStorageID, cdmDestroy - 1},
		"CDM Decrypt after TimerExpired":         {cdmDecrypt, cdmTimerExpired + 1},
		"Buffer::Size is the last slot":          {bufSize, bufSlots - 1},
		"DecryptedBlock::Timestamp":              {blockTimestamp, blockSlots - 1},
		"FileIO::Close":                          {fileClose, fileSlots - 1},
	} {
		if p[0] != p[1] {
			t.Errorf("%s: %d != %d", name, p[0], p[1])
		}
	}
	// spot-check the numeric slots against the header's declaration order
	want := map[string]int{
		"Initialize": cdmInitialize, "GetStatusForPolicy": cdmGetStatusForPolicy, "SetServerCertificate": cdmSetServerCertificate,
		"CreateSessionAndGenerateRequest": cdmCreateSessionAndGenerateReq, "LoadSession": cdmLoadSession, "UpdateSession": cdmUpdateSession,
		"CloseSession": cdmCloseSession, "RemoveSession": cdmRemoveSession, "TimerExpired": cdmTimerExpired, "Decrypt": cdmDecrypt,
	}
	order := []string{"Initialize", "GetStatusForPolicy", "SetServerCertificate", "CreateSessionAndGenerateRequest", "LoadSession",
		"UpdateSession", "CloseSession", "RemoveSession", "TimerExpired", "Decrypt"}
	for i, n := range order {
		if want[n] != i {
			t.Errorf("CDM slot of %s = %d, want %d", n, want[n], i)
		}
	}
	hostOrder := []int{hostAllocate, hostSetTimer, hostGetCurrentWallTime, hostOnInitialized, hostOnResolveKeyStatusPromise, hostOnResolveNewSession,
		hostOnResolvePromise, hostOnRejectPromise, hostOnSessionMessage, hostOnSessionKeysChange, hostOnExpirationChange, hostOnSessionClosed,
		hostSendPlatformChallenge, hostEnableOutputProtection, hostQueryOutputProtection, hostOnDeferredInitDone, hostCreateFileIO, hostRequestStorageID, hostReportMetrics}
	for i, s := range hostOrder {
		if s != i {
			t.Errorf("host slot %d = %d", i, s)
		}
	}
}

func TestCallbackTablesAreFullyPopulated(t *testing.T) {
	c, err := theCallbacks()
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range c.host {
		if p == 0 {
			t.Errorf("Host slot %d has no callback", i)
		}
	}
	for i, p := range c.buffer {
		if p == 0 {
			t.Errorf("Buffer slot %d has no callback", i)
		}
	}
	for i, p := range c.block {
		if p == 0 {
			t.Errorf("DecryptedBlock slot %d has no callback", i)
		}
	}
	for i, p := range c.file {
		if p == 0 {
			t.Errorf("FileIO slot %d has no callback", i)
		}
	}
	if c.getHost == 0 {
		t.Error("no GetCdmHostFunc")
	}
}

// ---- host services called through the vtable like a CDM would ----

func TestHostWallTimeAndBuffers(t *testing.T) {
	st := newStack(t, []int{11}, nil, false)
	host := st.fake.host

	// Host::GetCurrentWallTime returns a double in the floating point return register
	var wall func(this uintptr) cdmTime
	purego.RegisterFunc(&wall, st.fake.hostSlotAddr(hostGetCurrentWallTime))
	now := float64(time.Now().UnixNano()) / 1e9
	if got := wall(host).T; got < now-2 || got > now+2 {
		t.Fatalf("GetCurrentWallTime = %v, want about %v", got, now)
	}

	buf := vcall(host, hostAllocate, 100)
	if buf == 0 {
		t.Fatal("Allocate returned NULL")
	}
	if vcall(buf, bufCapacity) != 100 || vcall(buf, bufSize) != 0 {
		t.Fatal("capacity/size of a new buffer")
	}
	data := vcall(buf, bufData)
	if data == 0 {
		t.Fatal("Data() is NULL")
	}
	copy(unsafe.Slice((*byte)(ptrAt(data)), 100), bytes.Repeat([]byte{7}, 100))
	vcall(buf, bufSetSize, 60)
	if vcall(buf, bufSize) != 60 {
		t.Fatal("SetSize")
	}
	vcall(buf, bufSetSize, 5000) // clamped to the capacity
	if vcall(buf, bufSize) != 100 {
		t.Fatal("SetSize beyond capacity must clamp")
	}
	vcall(buf, bufDestroy)
	if _, ok := lookup[*bufferObj](buf); ok {
		t.Fatal("Destroy must release the buffer")
	}
	zero := vcall(host, hostAllocate, 0)
	if zero == 0 {
		t.Fatal("a zero-capacity buffer is still a valid buffer")
	}
	vcall(zero, bufDestroy)
	if vcall(host, hostAllocate, 2<<30) != 0 {
		t.Fatal("absurd allocation must fail with NULL")
	}
}

func TestFileIOSemantics(t *testing.T) {
	st := newStack(t, []int{11}, nil, true)
	host := st.fake.host
	dir := filepath.Join(st.dir, "store")

	// a client object whose three methods record what the host reports
	type rec struct {
		open, write []uintptr
		read        [][]byte
		readStatus  []uintptr
	}
	var r rec
	var vt [3]uintptr
	vt[clientOnOpenComplete] = purego.NewCallback(func(this, status uintptr) uintptr { r.open = append(r.open, status); return 0 })
	vt[clientOnReadComplete] = purego.NewCallback(func(this, status, data, size uintptr) uintptr {
		r.readStatus = append(r.readStatus, status)
		r.read = append(r.read, cBytes(data, uint32(size)))
		return 0
	})
	vt[clientOnWriteComplete] = purego.NewCallback(func(this, status uintptr) uintptr { r.write = append(r.write, status); return 0 })
	client := &struct{ vtbl uintptr }{vtbl: uintptr(unsafe.Pointer(&vt))}
	clientAddr := uintptr(unsafe.Pointer(client))
	pin(clientAddr, client)
	defer unpin(clientAddr)

	a := &arena{}
	defer a.release()
	call := func(f func()) { // run on the CDM thread, as the CDM would; the replies are posted to the same thread
		if err := st.eng.thr.do(f); err != nil {
			t.Fatal(err)
		}
		st.eng.thr.do(func() {}) // flush posted completions
		st.eng.thr.do(func() {})
	}
	open := func(io uintptr, name string) {
		p, n := strPtr(a, name)
		call(func() { vcall(io, fileOpen, p, n) })
	}

	io1 := vcall(host, hostCreateFileIO, clientAddr)
	io2 := vcall(host, hostCreateFileIO, clientAddr)
	if io1 == 0 || io2 == 0 {
		t.Fatal("CreateFileIO returned NULL with persistent state on")
	}
	open(io1, "state.dat")
	open(io2, "state.dat") // already open elsewhere
	open(io2, "../escape") // not a valid name
	open(io2, "_hidden")
	open(io2, "")
	if !reflect.DeepEqual(r.open, []uintptr{fileIOStatusSuccess, fileIOStatusInUse, fileIOStatusError, fileIOStatusError, fileIOStatusError}) {
		t.Fatalf("open statuses %v", r.open)
	}
	// reading an empty new file succeeds with zero bytes
	call(func() { vcall(io1, fileRead) })
	if len(r.read) != 1 || len(r.read[0]) != 0 || r.readStatus[0] != fileIOStatusSuccess {
		t.Fatalf("empty read: %v %v", r.read, r.readStatus)
	}
	p, n := strPtr(a, "hello widevine")
	call(func() { vcall(io1, fileWrite, p, n) })
	call(func() { vcall(io1, fileRead) })
	if !reflect.DeepEqual(r.write, []uintptr{fileIOStatusSuccess}) || string(r.read[1]) != "hello widevine" {
		t.Fatalf("write %v read %q", r.write, r.read[1])
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "state.dat")); string(b) != "hello widevine" {
		t.Fatalf("on disk: %q", b)
	}
	if _, err := os.Stat(filepath.Join(dir, "state.dat.tmp")); err == nil {
		t.Fatal("temporary file left behind")
	}
	// operations on a FileIO that was never opened fail
	call(func() { vcall(io2, fileRead) })
	if r.readStatus[len(r.readStatus)-1] != fileIOStatusError {
		t.Fatalf("read before open: %v", r.readStatus)
	}
	// after Close the name is free again
	call(func() { vcall(io1, fileClose) })
	open(io2, "state.dat")
	if r.open[len(r.open)-1] != fileIOStatusSuccess {
		t.Fatalf("open after close: %v", r.open)
	}
	call(func() { vcall(io2, fileClose) })
	if entries, _ := os.ReadDir(dir); len(entries) != 2 { // cert.bin from the fake and state.dat
		t.Fatalf("store contents: %v", entries)
	}

	// without persistent state the CDM gets NULL
	st2 := newStack(t, []int{11}, nil, false)
	if io := vcall(st2.fake.host, hostCreateFileIO, clientAddr); io != 0 {
		t.Fatal("CreateFileIO must return NULL when persistent state is off")
	}
}

func TestEngineDoubleCloseAndUseAfterClose(t *testing.T) {
	st := newStack(t, []int{11}, nil, false)
	if err := st.eng.Close(); err != nil {
		t.Fatal(err)
	}
	if err := st.eng.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := st.eng.CreateSession(context.Background(), cdm.SessionTemporary, "cenc", nil); err == nil {
		t.Fatal("CreateSession after Close must fail")
	}
	if _, err := st.eng.Decrypt(context.Background(), &cdm.Sample{Data: []byte{1}, Scheme: cdm.SchemeCenc}); err == nil {
		t.Fatal("Decrypt after Close must fail")
	}
	if engineByHandle(st.eng.handle) != nil {
		t.Fatal("engine still registered")
	}
}

func TestAwaitRunsQueuedJobsAndTimesOut(t *testing.T) {
	th := newCDMThread()
	defer th.close()
	ch := make(chan int, 1)
	ran := false
	var got int
	var err error
	if e := th.do(func() {
		th.post(func() { ran = true; ch <- 42 }) // the answer depends on a job queued behind us
		got, err = await(context.Background(), th, ch, 2*time.Second)
	}); e != nil {
		t.Fatal(e)
	}
	if err != nil || got != 42 || !ran {
		t.Fatalf("%v %v %v", got, err, ran)
	}
	var terr error
	_ = th.do(func() { _, terr = await(context.Background(), th, make(chan int), 20*time.Millisecond) })
	if terr == nil {
		t.Fatal("await must time out")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = th.do(func() { _, terr = await(ctx, th, make(chan int), time.Second) })
	if !errors.Is(terr, context.Canceled) {
		t.Fatalf("%v", terr)
	}
}

// ---- reference encryptors (independent of cdm.DecryptSample) ----

func encryptCenc(t *testing.T, key, iv8 []byte, subs []cdm.Subsample, plain []byte) []byte {
	t.Helper()
	blk, _ := aes.NewCipher(key)
	iv := make([]byte, 16)
	copy(iv, iv8)
	var clear, protected []byte
	_ = clear
	pos := 0
	var idx [][2]int
	for _, s := range subs {
		pos += int(s.ClearBytes)
		protected = append(protected, plain[pos:pos+int(s.CipherBytes)]...)
		idx = append(idx, [2]int{pos, int(s.CipherBytes)})
		pos += int(s.CipherBytes)
	}
	cipher.NewCTR(blk, iv).XORKeyStream(protected, protected)
	out := append([]byte(nil), plain...)
	off := 0
	for _, r := range idx {
		copy(out[r[0]:r[0]+r[1]], protected[off:off+r[1]])
		off += r[1]
	}
	return out
}

func encryptCbcsWhole(t *testing.T, key, iv []byte, p cdm.Pattern, plain []byte) []byte {
	t.Helper()
	blk, _ := aes.NewCipher(key)
	out := append([]byte(nil), plain...)
	prev := append([]byte(nil), iv...)
	for b := 0; b < len(out)/16; b++ {
		if uint32(b)%(p.CryptBlocks+p.SkipBlocks) >= p.CryptBlocks {
			continue
		}
		cur := out[b*16 : b*16+16]
		for i := range cur {
			cur[i] ^= prev[i]
		}
		blk.Encrypt(cur, cur)
		copy(prev, cur)
	}
	return out
}
