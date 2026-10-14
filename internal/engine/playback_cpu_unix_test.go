//go:build !windows

package engine

import (
	"syscall"
	"time"
)

// cpuTime is the user plus system CPU time of the process so far.
func cpuTime() time.Duration {
	var ru syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &ru) != nil {
		return 0
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}
