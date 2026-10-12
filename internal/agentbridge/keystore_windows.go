//go:build windows

package agentbridge

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

// protect encrypts the key with DPAPI (CryptProtectData): only the same
// Windows user can decrypt the file.
func protect(key string) ([]byte, error) {
	in := []byte(key)
	if len(in) == 0 {
		return nil, errors.New("empty key")
	}
	var out windows.DataBlob
	err := windows.CryptProtectData(&windows.DataBlob{Size: uint32(len(in)), Data: &in[0]}, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	if err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte("dpapi1\n"), unsafe.Slice(out.Data, out.Size)...), nil
}

func unprotect(b []byte) (string, error) {
	const hdr = "dpapi1\n"
	if len(b) <= len(hdr) || string(b[:len(hdr)]) != hdr {
		return "", errors.New("unrecognised key file")
	}
	raw := b[len(hdr):]
	var out windows.DataBlob
	err := windows.CryptUnprotectData(&windows.DataBlob{Size: uint32(len(raw)), Data: &raw[0]}, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	if err != nil {
		return "", err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return string(unsafe.Slice(out.Data, out.Size)), nil
}
