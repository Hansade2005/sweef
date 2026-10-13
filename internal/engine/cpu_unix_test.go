//go:build !windows

package engine

import (
	"syscall"
	"time"
)

// procCPU is the process CPU time (user + system), for the call CPU measurements.
func procCPU() time.Duration {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}
