//go:build (amd64 || arm64) && !windows

package widevinehost

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// hostFile is cdm::HostFile on POSIX (content_decryption_module_ext.h): 16 bytes. file_path is a char*, the two
// PlatformFile members are int file descriptors (-1 when absent). The CDM owns the descriptors after
// VerifyCdmHost_0 and closes them.
//
//	0 file_path (const char*), 8 file (int), 12 sig_file (int)
type hostFile struct {
	path uintptr
	file int32
	sig  int32
}

const (
	_ = uint(unsafe.Sizeof(hostFile{}) - 16)
	_ = uint(16 - unsafe.Sizeof(hostFile{}))
)

// buildHostFiles opens every spec read-only and lays out the C array. Descriptors are duplicates so Go's os.File
// finalizers cannot close what the CDM now owns. release frees the memory (not the descriptors, see above).
func buildHostFiles(specs []HostFileSpec) (addr uintptr, n int, release func(), err error) {
	a := &arena{}
	arr := make([]hostFile, len(specs))
	var opened []int32
	cleanup := func() {
		for _, fd := range opened {
			_ = syscall.Close(int(fd))
		}
	}
	open := func(p string) (int32, error) {
		f, err := os.Open(p)
		if err != nil {
			return -1, err
		}
		defer f.Close()
		fd, err := syscall.Dup(int(f.Fd()))
		if err != nil {
			return -1, err
		}
		opened = append(opened, int32(fd))
		return int32(fd), nil
	}
	for i, s := range specs {
		arr[i].path = a.bytes(append([]byte(s.Path), 0))
		fd, err := open(s.Path)
		if err != nil {
			cleanup()
			a.release()
			return 0, 0, nil, fmt.Errorf("opening host file: %w", err)
		}
		arr[i].file, arr[i].sig = fd, -1
		if s.Sig != "" {
			if sfd, err := open(s.Sig); err == nil {
				arr[i].sig = sfd
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
