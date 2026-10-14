//go:build linux || darwin

package main

import (
	"os"
	"runtime"
	"syscall"
)

// usage is the peak resident set size (MiB) and the user+system CPU time (ms) of a finished child.
func usage(ps *os.ProcessState) (rssMiB, cpuMs float64) {
	ru, _ := ps.SysUsage().(*syscall.Rusage)
	if ru == nil {
		return 0, 0
	}
	rss := float64(ru.Maxrss)
	if runtime.GOOS == "linux" {
		rss *= 1024 // ru_maxrss is in kilobytes on Linux and in bytes on macOS
	}
	return rss / (1 << 20), float64(ru.Utime.Nano()+ru.Stime.Nano()) / 1e6
}
