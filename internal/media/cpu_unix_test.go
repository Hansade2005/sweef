//go:build !windows

package media

import "syscall"

// cpuSeconds is the user plus system CPU time of the process so far.
func cpuSeconds() float64 {
	var r syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &r)
	return float64(r.Utime.Sec) + float64(r.Utime.Usec)/1e6 + float64(r.Stime.Sec) + float64(r.Stime.Usec)/1e6
}
