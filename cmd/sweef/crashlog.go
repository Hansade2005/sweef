package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"time"
)

// crashLogPath is where a crash of the browser window is recorded: the Windows launcher has no console, so a
// panic or a fatal runtime error would otherwise vanish.
func crashLogPath() string {
	d, err := os.UserCacheDir()
	if err != nil || d == "" {
		d = os.TempDir()
	}
	return filepath.Join(d, "Sweef", "crash.log")
}

// installCrashLog sends fatal runtime errors (and goroutine traces) to crash.log. It returns a function for
// deferred use that records a recovered panic the same way and exits with status 2.
func installCrashLog() (recoverAndLog func()) {
	p := crashLogPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return func() {}
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return func() {}
	}
	fmt.Fprintf(f, "\n=== sweef %s started %s ===\n", version, time.Now().Format(time.RFC3339))
	_ = debug.SetCrashOutput(f, debug.CrashOptions{})
	return func() {
		if r := recover(); r != nil {
			msg := fmt.Sprintf("panic: %v\n\n%s", r, debug.Stack())
			fmt.Fprintf(f, "%s\n", msg)
			_ = f.Sync()
			fmt.Fprintf(os.Stderr, "sweef crashed; details saved to %s\n%s\n", p, msg)
			os.Exit(2)
		}
	}
}
