//go:build windows && (amd64 || arm64)

// UNVERIFIED: never run on real hardware. This package is compiled and vetted for windows/amd64 and
// windows/arm64 only; nobody has run it against a real camera or microphone. docs/capture.md lists what to check.

// Package wincap is the Windows capture backend: Media Foundation (IMFSourceReader) for cameras and WASAPI
// (IAudioCaptureClient, shared mode) for microphones, driven through raw COM vtable calls with
// golang.org/x/sys/windows: no cgo, no third-party COM library.
//
// COM rules this file keeps: every thread that touches an interface initialises COM first and stays the same OS
// thread for the life of the objects (runtime.LockOSThread), and every interface pointer is released.
package wincap

import (
	"errors"
	"fmt"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// obj is a COM interface pointer.
type obj struct{ p unsafe.Pointer }

func (o obj) nil() bool { return o.p == nil }

// call invokes vtable method idx. Every method takes the object as its first argument. Arguments that are
// pointers must point into heap memory the caller keeps alive (see scratch): a pointer to a stack variable could
// move while the call is in flight.
func (o obj) call(idx uintptr, args ...uintptr) windows.Handle {
	vtbl := *(**[256]uintptr)(o.p)
	a := make([]uintptr, 0, len(args)+1)
	a = append(a, uintptr(o.p))
	a = append(a, args...)
	r, _, _ := syscall.SyscallN(vtbl[idx], a...)
	return windows.Handle(r)
}

func (o obj) release() {
	if o.p != nil {
		o.call(2)
	}
}

// hr turns an HRESULT into an error.
func hr(op string, r windows.Handle) error {
	if int32(r) >= 0 {
		return nil
	}
	return &hresult{op: op, code: uint32(r)}
}

type hresult struct {
	op   string
	code uint32
}

func (e *hresult) Error() string {
	return fmt.Sprintf("wincap: %s failed: HRESULT 0x%08X", e.op, e.code)
}

// HRESULTs the backend maps to the capture package's errors.
const (
	eAccessDenied           = 0x80070005
	audclntEDeviceInUse     = 0x8889000A
	audclntEDeviceInvalid   = 0x88890004
	audclntEExclusiveInUse  = 0x8889000B
	mfENoMoreTypes          = 0xC00D36B9
	mfEHardwareMFTFailed    = 0xC00D3EA2
	mfEVideoRecordingDevice = 0xC00D3EA3
	mfEDeviceInvalidated    = 0xC00D3E84
	mfEDeviceInUse          = 0xC00D3EA5
	mfENotAccepting         = 0xC00D36B2
)

func isHR(err error, codes ...uint32) bool {
	var h *hresult
	if errors.As(err, &h) {
		for _, c := range codes {
			if h.code == c {
				return true
			}
		}
	}
	return false
}

// scratch is heap memory for out parameters.
type scratch struct {
	ptr [4]unsafe.Pointer
	u32 [4]uint32
	u64 [2]uint64
	i64 [2]int64
}

func (s *scratch) pptr(i int) uintptr { return uintptr(unsafe.Pointer(&s.ptr[i])) }
func (s *scratch) pu32(i int) uintptr { return uintptr(unsafe.Pointer(&s.u32[i])) }
func (s *scratch) pu64(i int) uintptr { return uintptr(unsafe.Pointer(&s.u64[i])) }
func (s *scratch) pi64(i int) uintptr { return uintptr(unsafe.Pointer(&s.i64[i])) }

func newScratch() *scratch { return &scratch{} }

func guid(g *windows.GUID) uintptr { return uintptr(unsafe.Pointer(g)) }

// onCOMThread runs f on a dedicated, COM-initialised OS thread and waits for it.
func onCOMThread(f func() error) error {
	errc := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if err := windows.CoInitializeEx(0, windows.COINIT_MULTITHREADED); err != nil {
			errc <- err
			return
		}
		defer windows.CoUninitialize()
		errc <- f()
	}()
	return <-errc
}

func utf16(p *uint16) string { return windows.UTF16PtrToString(p) }

// coString reads a CoTaskMemAlloc'ed wide string and frees it.
func coString(p unsafe.Pointer) string {
	if p == nil {
		return ""
	}
	s := utf16((*uint16)(p))
	windows.CoTaskMemFree(p)
	return s
}
