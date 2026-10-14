//go:build !windows

package vp8enc

import (
	"syscall"
	"time"
)

// cpuTime returns the process CPU time (user + system).
func cpuTime() time.Duration {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}
