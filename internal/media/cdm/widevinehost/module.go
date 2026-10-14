//go:build amd64 || arm64

package widevinehost

import (
	"errors"
	"fmt"
	"unsafe"

	"github.com/ebitengine/purego"
)

// HostFileSpec names a file that is part of the CDM's host for Verified Media Path: the helper executable and its
// castLabs EVS signature (<exe>.sig), and the CDM library with its own .sig if one exists. Sig may be empty.
type HostFileSpec struct {
	Path string
	Sig  string
}

// Module is the exported C surface of a CDM library (content_decryption_module.h, module version 4, and
// content_decryption_module_ext.h). The production implementation is the dlopen'ed library; tests substitute a
// fake that implements the C++ side in Go.
type Module interface {
	// Initialize calls InitializeCdmModule_4.
	Initialize() error
	// Deinitialize calls DeinitializeCdmModule.
	Deinitialize()
	// Version is GetCdmVersion().
	Version() string
	// VerifyHost calls VerifyCdmHost_0 and reports whether the library exports it. The result is the CDM's
	// immediate answer: false means it rejected the files outright.
	VerifyHost(files []HostFileSpec) (called, accepted bool, err error)
	// CreateInstance calls CreateCdmInstance(version, key_system, size, get_cdm_host_func, user_data) and returns the
	// cdm::ContentDecryptionModule_<version>* (0 if the version or key system is not supported).
	CreateInstance(version int, keySystem string, getHost, userData uintptr) uintptr
	Close()
}

// dlModule is a Module backed by a shared library.
type dlModule struct {
	handle uintptr
	path   string

	initialize, deinit, createInstance, getVersion, verifyHost uintptr
}

// loadModule opens the library at path and resolves the entry points. InitializeCdmModule_4, CreateCdmInstance
// and GetCdmVersion are required; DeinitializeCdmModule and VerifyCdmHost_0 are optional.
func loadModule(path string) (Module, error) {
	h, err := openLibrary(path)
	if err != nil {
		return nil, fmt.Errorf("loading %s: %w", path, err)
	}
	m := &dlModule{handle: h, path: path}
	var ok bool
	if m.initialize, ok = lookupSymbol(h, "InitializeCdmModule_4"); !ok {
		closeLibrary(h)
		return nil, errors.New("the library does not export InitializeCdmModule_4: not a module-version-4 CDM")
	}
	if m.createInstance, ok = lookupSymbol(h, "CreateCdmInstance"); !ok {
		closeLibrary(h)
		return nil, errors.New("the library does not export CreateCdmInstance")
	}
	if m.getVersion, ok = lookupSymbol(h, "GetCdmVersion"); !ok {
		closeLibrary(h)
		return nil, errors.New("the library does not export GetCdmVersion")
	}
	m.deinit, _ = lookupSymbol(h, "DeinitializeCdmModule")
	m.verifyHost, _ = lookupSymbol(h, "VerifyCdmHost_0")
	return m, nil
}

func (m *dlModule) Initialize() error {
	purego.SyscallN(m.initialize)
	return nil
}

func (m *dlModule) Deinitialize() {
	if m.deinit != 0 {
		purego.SyscallN(m.deinit)
	}
}

func (m *dlModule) Version() string {
	r, _, _ := purego.SyscallN(m.getVersion)
	if r == 0 {
		return ""
	}
	// NUL-terminated C string owned by the library
	var n int
	for *(*byte)(ptrAt(r + uintptr(n))) != 0 && n < 256 {
		n++
	}
	return string(unsafe.Slice((*byte)(ptrAt(r)), n))
}

func (m *dlModule) VerifyHost(files []HostFileSpec) (called, accepted bool, err error) {
	if m.verifyHost == 0 {
		return false, false, nil
	}
	arr, n, release, err := buildHostFiles(files)
	if err != nil {
		return false, false, err
	}
	defer release()
	r, _, _ := purego.SyscallN(m.verifyHost, arr, uintptr(n))
	return true, r&0xff != 0, nil
}

func (m *dlModule) CreateInstance(version int, keySystem string, getHost, userData uintptr) uintptr {
	a := &arena{}
	defer a.release()
	ks := a.bytes([]byte(keySystem))
	r, _, _ := purego.SyscallN(m.createInstance, uintptr(version), ks, uintptr(len(keySystem)), getHost, userData)
	return r
}

func (m *dlModule) Close() {
	// Unloading a CDM library is not safe while its threads may still run; the helper process exits right after,
	// so the library is left mapped.
}
