//go:build windows

package app

import "time"

// processCPU is unavailable here; the idle CPU test skips on non-Linux anyway.
func processCPU() time.Duration { return 0 }
