package app

import (
	"errors"
	"sync"

	"github.com/sweefbrowser/sweef/internal/shell"
)

var (
	shellMu      sync.Mutex
	shellFactory func(shell.Config) (shell.Window, error)
)

// RegisterShell installs the native window factory. A tiny file in
// cmd/sweef calls it with shell/auto's Open once that package exists, so this
// package compiles without any native code.
func RegisterShell(open func(shell.Config) (shell.Window, error)) {
	shellMu.Lock()
	shellFactory = open
	shellMu.Unlock()
}

// ErrNoShell means no native window implementation was registered.
var ErrNoShell = errors.New("app: no native window available (shell/auto is not linked into this build); use --headless")

// OpenShell opens a window through the registered factory.
func OpenShell(cfg shell.Config) (shell.Window, error) {
	shellMu.Lock()
	f := shellFactory
	shellMu.Unlock()
	if f == nil {
		return nil, ErrNoShell
	}
	return f(cfg)
}

// HasShell reports whether a factory is registered.
func HasShell() bool {
	shellMu.Lock()
	defer shellMu.Unlock()
	return shellFactory != nil
}
