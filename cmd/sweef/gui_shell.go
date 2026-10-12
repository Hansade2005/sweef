package main

import (
	"github.com/sweefbrowser/sweef/internal/app"
	"github.com/sweefbrowser/sweef/internal/shell/auto"
	"github.com/sweefbrowser/sweef/internal/shell/mainthread"
)

// The native window comes from shell/auto (X11, Win32 or Cocoa by GOOS).
// internal/app only knows the registration hook, so it builds without any
// native code. mainthread.Run gives every OS the same program shape (it is
// mandatory on macOS, where AppKit owns the process main thread).
func init() {
	app.RegisterShell(auto.Open)
	guiRun = mainthread.Run
}
