//go:build windows && (amd64 || arm64)

// UNVERIFIED: never run on real hardware (see com.go).

package wincap

import (
	"fmt"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/sweefbrowser/sweef/internal/capture"
)

// Media Foundation, as much of it as a synchronous IMFSourceReader capture needs.
var (
	mfplatDLL       = windows.NewLazySystemDLL("mfplat.dll")
	mfDLL           = windows.NewLazySystemDLL("mf.dll")
	mfreadwriteDLL  = windows.NewLazySystemDLL("mfreadwrite.dll")
	procMFStartup   = mfplatDLL.NewProc("MFStartup")
	procMFCreateAtt = mfplatDLL.NewProc("MFCreateAttributes")
	procMFCreateMT  = mfplatDLL.NewProc("MFCreateMediaType")
	procMFEnum      = mfDLL.NewProc("MFEnumDeviceSources")
	procMFReader    = mfreadwriteDLL.NewProc("MFCreateSourceReaderFromMediaSource")

	mfStartOnce sync.Once
	mfStartErr  error

	guidSourceType   = windows.GUID{Data1: 0xC60AC5FE, Data2: 0x252A, Data3: 0x478F, Data4: [8]byte{0xA0, 0xEF, 0xBC, 0x8F, 0xA5, 0xF7, 0xCA, 0xD3}}
	guidVidcap       = windows.GUID{Data1: 0x8AC3587A, Data2: 0x4AE7, Data3: 0x42D8, Data4: [8]byte{0x99, 0xE0, 0x0A, 0x60, 0x13, 0xEE, 0xF9, 0x0F}}
	guidFriendlyName = windows.GUID{Data1: 0x60D0E559, Data2: 0x52F8, Data3: 0x4FA2, Data4: [8]byte{0xBB, 0xCE, 0xAC, 0xDB, 0x34, 0xA8, 0xEC, 0x01}}
	guidSymLink      = windows.GUID{Data1: 0x58F0AAD8, Data2: 0x22BF, Data3: 0x4F8A, Data4: [8]byte{0xBB, 0x3D, 0xD2, 0xC4, 0x97, 0x8C, 0x6E, 0x2F}}
	guidMajorType    = windows.GUID{Data1: 0x48EBA18E, Data2: 0xF8C9, Data3: 0x4687, Data4: [8]byte{0xBF, 0x11, 0x0A, 0x74, 0xC9, 0xF9, 0x6A, 0x8F}}
	guidSubtype      = windows.GUID{Data1: 0xF7E34C9A, Data2: 0x42E8, Data3: 0x4714, Data4: [8]byte{0xB7, 0x4B, 0xCB, 0x29, 0xD7, 0x2C, 0x35, 0xE5}}
	guidVideo        = windows.GUID{Data1: 0x73646976, Data2: 0x0000, Data3: 0x0010, Data4: [8]byte{0x80, 0x00, 0x00, 0xAA, 0x00, 0x38, 0x9B, 0x71}}
	guidNV12         = windows.GUID{Data1: 0x3231564E, Data2: 0x0000, Data3: 0x0010, Data4: [8]byte{0x80, 0x00, 0x00, 0xAA, 0x00, 0x38, 0x9B, 0x71}}
	guidFrameSize    = windows.GUID{Data1: 0x1652C33D, Data2: 0xD6B2, Data3: 0x4012, Data4: [8]byte{0xB8, 0x34, 0x72, 0x03, 0x08, 0x49, 0xA3, 0x7D}}
	guidFrameRate    = windows.GUID{Data1: 0xC459A2E8, Data2: 0x3D2C, Data3: 0x4E44, Data4: [8]byte{0xB1, 0x32, 0xFE, 0xE5, 0x15, 0x6C, 0x7B, 0x30}}
	guidDefStride    = windows.GUID{Data1: 0x644B4E48, Data2: 0x1E02, Data3: 0x4516, Data4: [8]byte{0xB0, 0xEB, 0xC0, 0x1C, 0xA9, 0xD4, 0x9A, 0xC6}}
	guidVideoProc    = windows.GUID{Data1: 0xFB394F3D, Data2: 0xCCF1, Data3: 0x42EE, Data4: [8]byte{0xBB, 0xB3, 0xF9, 0xB8, 0x45, 0xD5, 0x68, 0x1D}}
	iidMediaSource   = windows.GUID{Data1: 0x279A808D, Data2: 0xAEC7, Data3: 0x40C8, Data4: [8]byte{0x9C, 0x6B, 0xA6, 0xB4, 0x92, 0xC7, 0x8A, 0x66}}
)

// IMFAttributes methods (IMFActivate, IMFMediaType, IMFSample and the source-reader attributes inherit them).
const (
	idxAttrGetUINT32     = 7
	idxAttrGetUINT64     = 8
	idxAttrGetAllocStr   = 13
	idxAttrSetUINT32     = 21
	idxAttrSetUINT64     = 22
	idxAttrSetGUID       = 24
	idxActActivateObject = 33
	idxActShutdown       = 34
	idxSRGetNativeType   = 5
	idxSRGetCurrentType  = 6
	idxSRSetCurrentType  = 7
	idxSRReadSample      = 9
	idxSampleToContig    = 41
	idxBufLock           = 3
	idxBufUnlock         = 4

	mfVersion              = 0x00020070
	firstVideoStream       = 0xFFFFFFFC
	srReaderfError         = 0x1
	srReaderfEndOfStream   = 0x2
	srReaderfCurrentChange = 0x20
)

func mfStartup() error {
	mfStartOnce.Do(func() {
		r, _, _ := procMFStartup.Call(mfVersion, 0)
		mfStartErr = hr("MFStartup", windows.Handle(r))
	})
	return mfStartErr
}

func mfAttrs(n int) (obj, error) {
	s := newScratch()
	r, _, _ := procMFCreateAtt.Call(s.pptr(0), uintptr(n))
	if err := hr("MFCreateAttributes", windows.Handle(r)); err != nil {
		return obj{}, err
	}
	return obj{s.ptr[0]}, nil
}

type camInfo struct {
	link, name string
}

// withActivates enumerates the video capture devices and calls f with each IMFActivate (released afterwards).
func withActivates(f func(act obj, link, name string) bool) error {
	if err := mfStartup(); err != nil {
		return err
	}
	attrs, err := mfAttrs(1)
	if err != nil {
		return err
	}
	defer attrs.release()
	if err := hr("SetGUID", attrs.call(idxAttrSetGUID, guid(&guidSourceType), guid(&guidVidcap))); err != nil {
		return err
	}
	s := newScratch()
	r, _, _ := procMFEnum.Call(uintptr(attrs.p), s.pptr(0), s.pu32(0))
	if err := hr("MFEnumDeviceSources", windows.Handle(r)); err != nil {
		return err
	}
	arr, n := s.ptr[0], int(s.u32[0])
	defer windows.CoTaskMemFree(arr)
	acts := make([]obj, n)
	for i := range acts {
		acts[i] = obj{*(*unsafe.Pointer)(unsafe.Add(arr, i*int(unsafe.Sizeof(arr))))}
	}
	defer func() {
		for _, a := range acts {
			a.release()
		}
	}()
	for _, a := range acts {
		name, link := allocString(a, &guidFriendlyName), allocString(a, &guidSymLink)
		if f(a, link, name) {
			return nil
		}
	}
	return nil
}

func allocString(a obj, key *windows.GUID) string {
	s := newScratch()
	if hr("GetAllocatedString", a.call(idxAttrGetAllocStr, guid(key), s.pptr(0), s.pu32(0))) != nil {
		return ""
	}
	return coString(s.ptr[0])
}

func camList() ([]camInfo, error) {
	var out []camInfo
	err := onCOMThread(func() error {
		return withActivates(func(_ obj, link, name string) bool {
			if link != "" {
				out = append(out, camInfo{link: link, name: name})
			}
			return false
		})
	})
	return out, err
}

// camSource implements capture.VideoSource with a synchronous IMFSourceReader.
type camSource struct {
	info capture.DeviceInfo
	link string

	mu      sync.Mutex
	started bool
	stop    chan struct{}
	done    chan struct{}
	err     error
	w, h    int
	fps     float64
}

func (c *camSource) Info() capture.DeviceInfo { return c.info }
func (c *camSource) Err() error               { c.mu.Lock(); defer c.mu.Unlock(); return c.err }
func (c *camSource) fail(err error)           { c.mu.Lock(); c.err = err; c.mu.Unlock() }
func (c *camSource) Mode() (int, int, float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.w, c.h, c.fps
}

type nativeMode struct {
	idx  uint32
	w, h int
	fps  float64
}

func mapVideoErr(op string, err error) error {
	switch {
	case isHR(err, eAccessDenied):
		return fmt.Errorf("wincap: %s: access to the camera is blocked (Windows Settings > Privacy & security > Camera): %w", op, err)
	case isHR(err, mfEDeviceInUse, mfEHardwareMFTFailed, mfEVideoRecordingDevice):
		return fmt.Errorf("%w: %v", capture.ErrInUse, err)
	case isHR(err, mfEDeviceInvalidated):
		return fmt.Errorf("%w: %v", capture.ErrNotFound, err)
	}
	return err
}

func (c *camSource) Start(vc capture.VideoConstraints) (<-chan *capture.Frame, error) {
	c.mu.Lock()
	if c.started {
		c.mu.Unlock()
		return nil, fmt.Errorf("wincap: camera already started")
	}
	c.started = true
	c.stop, c.done = make(chan struct{}), make(chan struct{})
	stop, done := c.stop, c.done
	c.mu.Unlock()
	out := make(chan *capture.Frame, 3)
	res := make(chan error, 1)
	go c.capture(vc, out, stop, done, res)
	if err := <-res; err != nil {
		<-done
		return nil, err
	}
	return out, nil
}

// Stop asks the read loop to end. ReadSample blocks until the next frame, so this returns within a frame interval
// for a working camera; a camera that stopped delivering is abandoned after three seconds.
func (c *camSource) Stop() error {
	c.mu.Lock()
	stop, done := c.stop, c.done
	c.stop = nil
	c.mu.Unlock()
	if stop != nil {
		close(stop)
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
	}
	return nil
}

func (c *camSource) capture(vc capture.VideoConstraints, out chan *capture.Frame, stop, done chan struct{}, res chan error) {
	defer close(done)
	defer close(out)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := windows.CoInitializeEx(0, windows.COINIT_MULTITHREADED); err != nil {
		res <- err
		return
	}
	defer windows.CoUninitialize()
	reported := false
	report := func(err error) {
		if !reported {
			reported = true
			res <- err
		}
	}
	defer report(fmt.Errorf("wincap: camera stopped while starting"))

	// 1. find the device again (an IMFActivate is only valid for the enumeration that made it) and activate it
	var src obj
	var actErr error
	found := false
	s := newScratch()
	err := withActivates(func(act obj, link, _ string) bool {
		if link != c.link {
			return false
		}
		found = true
		if actErr = hr("ActivateObject", act.call(idxActActivateObject, guid(&iidMediaSource), s.pptr(0))); actErr == nil {
			src = obj{s.ptr[0]}
		}
		return true
	})
	if err == nil && !found {
		err = capture.ErrNotFound
	}
	if err == nil {
		err = actErr
	}
	if err != nil {
		report(mapVideoErr("open the camera", err))
		return
	}
	defer src.release()
	defer func() { // release the device for other applications
		withActivates(func(act obj, link, _ string) bool {
			if link == c.link {
				act.call(idxActShutdown)
				return true
			}
			return false
		})
	}()

	// 2. a source reader that may convert (video processing on) so NV12 is available whatever the camera outputs
	attrs, err := mfAttrs(1)
	if err != nil {
		report(err)
		return
	}
	defer attrs.release()
	attrs.call(idxAttrSetUINT32, guid(&guidVideoProc), 1)
	r, _, _ := procMFReader.Call(uintptr(src.p), uintptr(attrs.p), s.pptr(1))
	if err := hr("MFCreateSourceReaderFromMediaSource", windows.Handle(r)); err != nil {
		report(mapVideoErr("create the source reader", err))
		return
	}
	reader := obj{s.ptr[1]}
	defer reader.release()

	// 3. pick the native mode closest to the request
	var modes []nativeMode
	for i := uint32(0); i < 512; i++ {
		if r := reader.call(idxSRGetNativeType, firstVideoStream, uintptr(i), s.pptr(2)); int32(r) < 0 {
			break
		}
		mt := obj{s.ptr[2]}
		var m nativeMode
		m.idx = i
		if hr("frame size", mt.call(idxAttrGetUINT64, guid(&guidFrameSize), s.pu64(0))) == nil {
			m.w, m.h = int(s.u64[0]>>32), int(uint32(s.u64[0]))
		}
		if hr("frame rate", mt.call(idxAttrGetUINT64, guid(&guidFrameRate), s.pu64(1))) == nil && uint32(s.u64[1]) != 0 {
			m.fps = float64(s.u64[1]>>32) / float64(uint32(s.u64[1]))
		}
		mt.release()
		if m.w > 0 && m.h > 0 {
			modes = append(modes, m)
		}
	}
	if len(modes) == 0 {
		report(fmt.Errorf("wincap: the camera reports no video formats"))
		return
	}
	cm := make([]capture.Mode, len(modes))
	for i, m := range modes {
		cm[i] = capture.Mode{W: m.w, H: m.h, FPSMin: m.fps, FPSMax: m.fps}
	}
	want := vc
	if want.Width <= 0 && want.Height <= 0 {
		want.Width, want.Height = 640, 480
	}
	best, _ := capture.ClosestMode(cm, want.Width, want.Height, want.FrameRate)
	pickFPS := best.FPSMax
	for _, m := range modes { // among equal sizes prefer the rate nearest the request
		if m.w == best.W && m.h == best.H && (want.FrameRate <= 0 && m.fps > pickFPS || want.FrameRate > 0 && absf(m.fps-want.FrameRate) < absf(pickFPS-want.FrameRate)) {
			pickFPS = m.fps
		}
	}
	// 4. ask for NV12 at that size and rate
	var mt obj
	if r, _, _ := procMFCreateMT.Call(s.pptr(2)); hr("MFCreateMediaType", windows.Handle(r)) != nil {
		report(fmt.Errorf("wincap: cannot create a media type"))
		return
	}
	mt = obj{s.ptr[2]}
	mt.call(idxAttrSetGUID, guid(&guidMajorType), guid(&guidVideo))
	mt.call(idxAttrSetGUID, guid(&guidSubtype), guid(&guidNV12))
	mt.call(idxAttrSetUINT64, guid(&guidFrameSize), uintptr(uint64(best.W)<<32|uint64(best.H)))
	den := uint64(1000)
	mt.call(idxAttrSetUINT64, guid(&guidFrameRate), uintptr(uint64(pickFPS*float64(den))<<32|den))
	rr := reader.call(idxSRSetCurrentType, firstVideoStream, 0, uintptr(mt.p))
	mt.release()
	if err := hr("SetCurrentMediaType(NV12)", rr); err != nil {
		report(mapVideoErr("select the video format", err))
		return
	}
	// what the reader will really deliver
	w, h, stride := best.W, best.H, best.W
	if hr("GetCurrentMediaType", reader.call(idxSRGetCurrentType, firstVideoStream, s.pptr(2))) == nil {
		cur := obj{s.ptr[2]}
		if hr("size", cur.call(idxAttrGetUINT64, guid(&guidFrameSize), s.pu64(0))) == nil {
			w, h = int(s.u64[0]>>32), int(uint32(s.u64[0]))
		}
		stride = w
		if hr("stride", cur.call(idxAttrGetUINT32, guid(&guidDefStride), s.pu32(0))) == nil && int32(s.u32[0]) > 0 {
			stride = int(int32(s.u32[0]))
		}
		cur.release()
	}
	c.mu.Lock()
	c.w, c.h, c.fps = w, h, pickFPS
	c.mu.Unlock()
	report(nil)

	// 5. read frames
	var first int64 = -1
	for {
		select {
		case <-stop:
			return
		default:
		}
		// ReadSample(stream, flags, *actualStream, *streamFlags, *timestamp, **sample)
		s.ptr[3] = nil
		r := reader.call(idxSRReadSample, firstVideoStream, 0, s.pu32(1), s.pu32(2), s.pi64(0), s.pptr(3))
		if err := hr("ReadSample", r); err != nil {
			c.fail(mapVideoErr("read a frame", err))
			return
		}
		flags := s.u32[2]
		if flags&(srReaderfError|srReaderfEndOfStream) != 0 {
			c.fail(fmt.Errorf("%w: the camera stopped delivering frames", capture.ErrNotFound))
			return
		}
		if s.ptr[3] == nil {
			continue // a stream tick or a gap
		}
		sample := obj{s.ptr[3]}
		ts := s.i64[0]
		if first < 0 {
			first = ts
		}
		frame := convertSample(sample, s, w, h, stride)
		sample.release()
		if frame == nil {
			continue
		}
		frame.PTS = time.Duration(ts-first) * 100 // 100 ns units
		select {
		case out <- frame:
		default:
			select {
			case <-out:
			default:
			}
			select {
			case out <- frame:
			default:
			}
		}
	}
}

func absf(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

// convertSample copies one NV12 sample into an I420 frame.
func convertSample(sample obj, s *scratch, w, h, stride int) *capture.Frame {
	if hr("ConvertToContiguousBuffer", sample.call(idxSampleToContig, s.pptr(0))) != nil {
		return nil
	}
	buf := obj{s.ptr[0]}
	defer buf.release()
	if hr("Lock", buf.call(idxBufLock, s.pptr(1), s.pu32(0), s.pu32(1))) != nil {
		return nil
	}
	defer buf.call(idxBufUnlock)
	n := int(s.u32[1])
	if n <= 0 {
		return nil
	}
	f := capture.NewFrame(w, h)
	if err := capture.Convert(f, capture.PixNV12, unsafe.Slice((*byte)(s.ptr[1]), n), stride); err != nil {
		return nil
	}
	return f
}
