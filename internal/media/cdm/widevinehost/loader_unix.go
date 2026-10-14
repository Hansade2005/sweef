//go:build (amd64 || arm64) && !windows

package widevinehost

import "github.com/ebitengine/purego"

func openLibrary(path string) (uintptr, error) {
	return purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_LOCAL)
}

func lookupSymbol(h uintptr, name string) (uintptr, bool) {
	p, err := purego.Dlsym(h, name)
	return p, err == nil && p != 0
}

func closeLibrary(h uintptr) { _ = purego.Dlclose(h) }
