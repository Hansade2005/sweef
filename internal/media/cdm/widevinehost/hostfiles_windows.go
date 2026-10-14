//go:build (amd64 || arm64) && windows

package widevinehost

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// UNVERIFIED: compiled for Windows but never run there.

// hostFile is cdm::HostFile on Windows: 24 bytes. file_path is a wchar_t* and the PlatformFile members are HANDLEs
// (INVALID_HANDLE_VALUE when absent). The CDM closes the handles after VerifyCdmHost_0.
//
//	0 file_path (const wchar_t*), 8 file (HANDLE), 16 sig_file (HANDLE)
type hostFile struct {
	path uintptr
	file uintptr
	sig  uintptr
}

const (
	_ = uint(unsafe.Sizeof(hostFile{}) - 24)
	_ = uint(24 - unsafe.Sizeof(hostFile{}))
)

func buildHostFiles(specs []HostFileSpec) (addr uintptr, n int, release func(), err error) {
	a := &arena{}
	arr := make([]hostFile, len(specs))
	var opened []windows.Handle
	cleanup := func() {
		for _, h := range opened {
			_ = windows.CloseHandle(h)
		}
	}
	open := func(p string) (windows.Handle, error) {
		pp, err := windows.UTF16PtrFromString(p)
		if err != nil {
			return windows.InvalidHandle, err
		}
		h, err := windows.CreateFile(pp, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_DELETE, nil,
			windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
		if err == nil {
			opened = append(opened, h)
		}
		return h, err
	}
	for i, s := range specs {
		u16, err := windows.UTF16FromString(s.Path)
		if err != nil {
			cleanup()
			a.release()
			return 0, 0, nil, err
		}
		buf := make([]byte, len(u16)*2)
		for j, c := range u16 {
			buf[2*j], buf[2*j+1] = byte(c), byte(c>>8)
		}
		arr[i].path = a.bytes(buf)
		h, err := open(s.Path)
		if err != nil {
			cleanup()
			a.release()
			return 0, 0, nil, fmt.Errorf("opening host file: %w", err)
		}
		arr[i].file, arr[i].sig = uintptr(h), uintptr(windows.InvalidHandle)
		if s.Sig != "" {
			if sh, err := open(s.Sig); err == nil {
				arr[i].sig = uintptr(sh)
			}
		}
	}
	if len(arr) == 0 {
		return 0, 0, func() {}, nil
	}
	base := uintptr(unsafe.Pointer(&arr[0]))
	a.keep(base, arr)
	return base, len(arr), a.release, nil
}
