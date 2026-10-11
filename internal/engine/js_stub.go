//go:build !quickjs

package engine

import "sync/atomic"

// Pure-Go build: no QuickJS linked. All hooks below are inert; runtimeEval
// falls back to the tiny evaluator in js.go. This is what ships as the
// 4 MB binary for ultra-low-RAM targets that only need fetch+DOM+logs.

var cgoReady atomic.Bool

func cgoAvailable() bool        { return false }
func initCgoRuntime(o *Options) {}
func shutdownCgoRuntime()       {}

func createCgoContext(tabID string) any { return nil }
func destroyCgoContext(ctx any)         {}
func cgoEval(ctx any, src, name string) (string, []string) {
	return "", nil
}
