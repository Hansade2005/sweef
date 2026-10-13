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

var (
	ole32               = windows.NewLazySystemDLL("ole32.dll")
	procCoCreateInstace = ole32.NewProc("CoCreateInstance")
	procPropVariantClr  = ole32.NewProc("PropVariantClear")

	clsidMMDeviceEnumerator = windows.GUID{Data1: 0xBCDE0395, Data2: 0xE52F, Data3: 0x467C, Data4: [8]byte{0x8E, 0x3D, 0xC4, 0x57, 0x92, 0x91, 0x69, 0x2E}}
	iidIMMDeviceEnumerator  = windows.GUID{Data1: 0xA95664D2, Data2: 0x9614, Data3: 0x4F35, Data4: [8]byte{0xA7, 0x46, 0xDE, 0x8D, 0xB6, 0x36, 0x17, 0xE6}}
	iidIAudioClient         = windows.GUID{Data1: 0x1CB9AD4C, Data2: 0xDBFA, Data3: 0x4C32, Data4: [8]byte{0xB1, 0x78, 0xC2, 0xF5, 0x68, 0xA7, 0x03, 0xB2}}
	iidIAudioCaptureClient  = windows.GUID{Data1: 0xC8ADBD64, Data2: 0xE71E, Data3: 0x48A0, Data4: [8]byte{0xA4, 0xDE, 0x18, 0x5C, 0x39, 0x5C, 0xD3, 0x17}}
	// PKEY_Device_FriendlyName: {a45c254e-df1c-4efd-8020-67d146a850e0}, 14
	pkeyFriendlyName = propertyKey{fmtid: windows.GUID{Data1: 0xA45C254E, Data2: 0xDF1C, Data3: 0x4EFD, Data4: [8]byte{0x80, 0x20, 0x67, 0xD1, 0x46, 0xA8, 0x50, 0xE0}}, pid: 14}
)

// propVariant is PROPVARIANT: 16 bytes on 32-bit and 24 on 64-bit; the type tag is at +0, the value at +8.
type propVariant struct {
	vt    uint16
	_     [3]uint16
	ptr   unsafe.Pointer
	extra unsafe.Pointer
}

type propertyKey struct {
	fmtid windows.GUID
	pid   uint32
}

const (
	clsctxAll             = 0x17
	eCapture              = 1
	eConsole              = 0
	deviceStateActive     = 1
	stgmRead              = 0
	audclntShareShared    = 0
	autoConvertPCM        = 0x80000000
	srcDefaultQuality     = 0x08000000
	bufferFlagsSilent     = 0x2
	waveFormatPCM         = 1
	vtLPWSTR              = 31
	idxEnumEnumEndpoints  = 3
	idxEnumDefaultEnd     = 4
	idxEnumGetDevice      = 5
	idxCollCount          = 3
	idxCollItem           = 4
	idxDevActivate        = 3
	idxDevOpenProps       = 4
	idxDevGetID           = 5
	idxPropsGetValue      = 5
	idxACInitialize       = 3
	idxACGetMixFormat     = 8
	idxACStart            = 10
	idxACStop             = 11
	idxACGetService       = 14
	idxCapGetBuffer       = 3
	idxCapReleaseBuffer   = 4
	idxCapGetNextPacket   = 5
	reftimesPerMillis     = 10000
	captureBufferDuration = 100 * reftimesPerMillis // 100 ms of buffer
)

// waveFormatEx is WAVEFORMATEX (packed layout: Go's natural alignment equals the C one here).
type waveFormatEx struct {
	FormatTag      uint16
	Channels       uint16
	SamplesPerSec  uint32
	AvgBytesPerSec uint32
	BlockAlign     uint16
	BitsPerSample  uint16
	Size           uint16
}

func coCreate(clsid, iid *windows.GUID) (obj, error) {
	s := newScratch()
	r, _, _ := procCoCreateInstace.Call(guid(clsid), 0, clsctxAll, guid(iid), s.pptr(0))
	if err := hr("CoCreateInstance", windows.Handle(r)); err != nil {
		return obj{}, err
	}
	return obj{s.ptr[0]}, nil
}

type micInfo struct {
	id, name string
}

// micList enumerates active capture endpoints. The default one comes first.
func micList() ([]micInfo, error) {
	var out []micInfo
	err := onCOMThread(func() error {
		en, err := coCreate(&clsidMMDeviceEnumerator, &iidIMMDeviceEnumerator)
		if err != nil {
			return err
		}
		defer en.release()
		s := newScratch()
		defID := ""
		if hr("GetDefaultAudioEndpoint", en.call(idxEnumDefaultEnd, eCapture, eConsole, s.pptr(0))) == nil {
			dev := obj{s.ptr[0]}
			defID, _, _ = deviceNames(dev)
			dev.release()
		}
		if err := hr("EnumAudioEndpoints", en.call(idxEnumEnumEndpoints, eCapture, deviceStateActive, s.pptr(1))); err != nil {
			return err
		}
		coll := obj{s.ptr[1]}
		defer coll.release()
		if err := hr("IMMDeviceCollection::GetCount", coll.call(idxCollCount, s.pu32(0))); err != nil {
			return err
		}
		n := s.u32[0]
		for i := uint32(0); i < n; i++ {
			if hr("Item", coll.call(idxCollItem, uintptr(i), s.pptr(2))) != nil {
				continue
			}
			dev := obj{s.ptr[2]}
			id, name, err := deviceNames(dev)
			dev.release()
			if err == nil && id != "" {
				m := micInfo{id: id, name: name}
				if id == defID {
					out = append([]micInfo{m}, out...)
				} else {
					out = append(out, m)
				}
			}
		}
		return nil
	})
	return out, err
}

// deviceNames returns an endpoint's id and friendly name.
func deviceNames(dev obj) (id, name string, err error) {
	s := newScratch()
	if err = hr("IMMDevice::GetId", dev.call(idxDevGetID, s.pptr(0))); err != nil {
		return
	}
	id = coString(s.ptr[0])
	if err = hr("OpenPropertyStore", dev.call(idxDevOpenProps, stgmRead, s.pptr(1))); err != nil {
		return id, id, nil
	}
	props := obj{s.ptr[1]}
	defer props.release()
	pv := new(propVariant)
	if hr("GetValue", props.call(idxPropsGetValue, uintptr(unsafe.Pointer(&pkeyFriendlyName)), uintptr(unsafe.Pointer(pv)))) == nil {
		if pv.vt == vtLPWSTR {
			name = utf16((*uint16)(pv.ptr))
		}
		procPropVariantClr.Call(uintptr(unsafe.Pointer(pv)))
	}
	if name == "" {
		name = id
	}
	return id, name, nil
}

// micSource implements capture.AudioSource over WASAPI shared mode.
type micSource struct {
	info capture.DeviceInfo
	id   string

	mu      sync.Mutex
	started bool
	stop    chan struct{}
	done    chan struct{}
	err     error
}

func (m *micSource) Info() capture.DeviceInfo { return m.info }
func (m *micSource) Err() error               { m.mu.Lock(); defer m.mu.Unlock(); return m.err }
func (m *micSource) fail(err error)           { m.mu.Lock(); m.err = err; m.mu.Unlock() }

type startResult struct {
	rate, ch int
	err      error
}

// Start opens the endpoint, asks for 16-bit PCM at the mix rate (AUTOCONVERTPCM lets the audio engine convert from
// whatever the device produces) and delivers 10 ms chunks. It returns once the stream runs or has failed.
func (m *micSource) Start(c capture.AudioConstraints) (<-chan *capture.AudioChunk, error) {
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return nil, fmt.Errorf("wincap: microphone already started")
	}
	m.started = true
	m.stop, m.done = make(chan struct{}), make(chan struct{})
	stop, done := m.stop, m.done
	m.mu.Unlock()
	out := make(chan *capture.AudioChunk, 16)
	res := make(chan startResult, 1)
	go m.capture(c, out, stop, done, res)
	if r := <-res; r.err != nil {
		<-done
		return nil, r.err
	}
	return out, nil
}

func (m *micSource) Stop() error {
	m.mu.Lock()
	stop, done := m.stop, m.done
	m.stop = nil
	m.mu.Unlock()
	if stop != nil {
		close(stop)
	}
	if done != nil {
		<-done
	}
	return nil
}

// mapAudioErr turns the HRESULTs a capture start can return into the capture package's errors.
func mapAudioErr(op string, err error) error {
	switch {
	case isHR(err, eAccessDenied):
		return fmt.Errorf("wincap: %s: access to the microphone is blocked (Windows Settings > Privacy & security > Microphone): %w", op, err)
	case isHR(err, audclntEDeviceInUse, audclntEExclusiveInUse):
		return fmt.Errorf("%w: %v", capture.ErrInUse, err)
	case isHR(err, audclntEDeviceInvalid):
		return fmt.Errorf("%w: %v", capture.ErrNotFound, err)
	}
	return err
}

func (m *micSource) capture(c capture.AudioConstraints, out chan *capture.AudioChunk, stop, done chan struct{}, res chan startResult) {
	defer close(done)
	defer close(out)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := windows.CoInitializeEx(0, windows.COINIT_MULTITHREADED); err != nil {
		res <- startResult{err: err}
		return
	}
	defer windows.CoUninitialize()
	reported := false
	report := func(rate, ch int, err error) {
		if !reported {
			reported = true
			res <- startResult{rate, ch, err}
		}
	}
	defer report(0, 0, fmt.Errorf("wincap: microphone stopped while starting"))

	s := newScratch()
	en, err := coCreate(&clsidMMDeviceEnumerator, &iidIMMDeviceEnumerator)
	if err != nil {
		report(0, 0, err)
		return
	}
	defer en.release()
	idp := windows.StringToUTF16Ptr(m.id)
	r := en.call(idxEnumGetDevice, uintptr(unsafe.Pointer(idp)), s.pptr(0))
	runtime.KeepAlive(idp)
	if err := hr("IMMDeviceEnumerator::GetDevice", r); err != nil {
		report(0, 0, fmt.Errorf("%w: %v", capture.ErrNotFound, err))
		return
	}
	dev := obj{s.ptr[0]}
	defer dev.release()
	if err := hr("IMMDevice::Activate", dev.call(idxDevActivate, guid(&iidIAudioClient), clsctxAll, 0, s.pptr(1))); err != nil {
		report(0, 0, mapAudioErr("Activate", err))
		return
	}
	ac := obj{s.ptr[1]}
	defer ac.release()
	if err := hr("GetMixFormat", ac.call(idxACGetMixFormat, s.pptr(2))); err != nil {
		report(0, 0, err)
		return
	}
	mix := (*waveFormatEx)(s.ptr[2])
	rate, mixCh := int(mix.SamplesPerSec), int(mix.Channels)
	windows.CoTaskMemFree(s.ptr[2])
	ch := min(max(c.Channels, 1), 2, max(mixCh, 1))
	wf := &waveFormatEx{FormatTag: waveFormatPCM, Channels: uint16(ch), SamplesPerSec: uint32(rate), BitsPerSample: 16}
	wf.BlockAlign = uint16(ch * 2)
	wf.AvgBytesPerSec = uint32(rate * ch * 2)
	if err := hr("IAudioClient::Initialize", ac.call(idxACInitialize, audclntShareShared, autoConvertPCM|srcDefaultQuality, captureBufferDuration, 0, uintptr(unsafe.Pointer(wf)), 0)); err != nil {
		runtime.KeepAlive(wf)
		report(0, 0, mapAudioErr("Initialize", err))
		return
	}
	runtime.KeepAlive(wf)
	if err := hr("GetService(IAudioCaptureClient)", ac.call(idxACGetService, guid(&iidIAudioCaptureClient), s.pptr(3))); err != nil {
		report(0, 0, err)
		return
	}
	cap := obj{s.ptr[3]}
	defer cap.release()
	if err := hr("IAudioClient::Start", ac.call(idxACStart)); err != nil {
		report(0, 0, mapAudioErr("Start", err))
		return
	}
	defer ac.call(idxACStop)
	report(rate, ch, nil)

	chunk := rate / 100 * ch
	var buf []int16
	n := 0
	tk := time.NewTicker(5 * time.Millisecond)
	defer tk.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tk.C:
		}
		for {
			if err := hr("GetNextPacketSize", cap.call(idxCapGetNextPacket, s.pu32(0))); err != nil {
				m.fail(mapAudioErr("GetNextPacketSize", err))
				return
			}
			if s.u32[0] == 0 {
				break
			}
			if err := hr("GetBuffer", cap.call(idxCapGetBuffer, s.pptr(0), s.pu32(1), s.pu32(2), 0, 0)); err != nil {
				m.fail(mapAudioErr("GetBuffer", err))
				return
			}
			frames, flags := int(s.u32[1]), s.u32[2]
			if flags&bufferFlagsSilent != 0 {
				buf = append(buf, make([]int16, frames*ch)...)
			} else {
				buf = append(buf, unsafe.Slice((*int16)(s.ptr[0]), frames*ch)...)
			}
			cap.call(idxCapReleaseBuffer, uintptr(frames))
		}
		for len(buf) >= chunk {
			pcm := append([]int16(nil), buf[:chunk]...)
			buf = buf[chunk:]
			a := &capture.AudioChunk{Rate: rate, Channels: ch, PCM: pcm, PTS: time.Duration(n) * 10 * time.Millisecond}
			n++
			select {
			case out <- a:
			default:
				select {
				case <-out:
				default:
				}
				select {
				case out <- a:
				default:
				}
			}
		}
	}
}
