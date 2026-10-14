//go:build (amd64 || arm64) && windows

package widevinehost

import "golang.org/x/sys/windows"

// UNVERIFIED: compiled for Windows but never run there.

func openLibrary(path string) (uintptr, error) {
	// The CDM sits next to its own dependencies; search that directory first.
	h, err := windows.LoadLibraryEx(path, 0, windows.LOAD_LIBRARY_SEARCH_DLL_LOAD_DIR|windows.LOAD_LIBRARY_SEARCH_DEFAULT_DIRS)
	if err != nil {
		h, err = windows.LoadLibrary(path)
	}
	return uintptr(h), err
}

func lookupSymbol(h uintptr, name string) (uintptr, bool) {
	p, err := windows.GetProcAddress(windows.Handle(h), name)
	return p, err == nil && p != 0
}

func closeLibrary(h uintptr) { _ = windows.FreeLibrary(windows.Handle(h)) }
