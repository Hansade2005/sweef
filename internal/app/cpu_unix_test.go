//go:build !windows

package app

import (
	"syscall"
	"time"
)

// processCPU is the user+system CPU time this process has used so far.
func processCPU() time.Duration {
	var r syscall.Rusage
	syscall.Getrusage(syscall.RUSAGE_SELF, &r)
	return time.Duration(r.Utime.Nano() + r.Stime.Nano())
}
