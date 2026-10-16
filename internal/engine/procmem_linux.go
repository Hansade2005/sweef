//go:build linux

package engine

import (
	"bytes"
	"os"
	"strconv"
)

// ProcessRSS is the resident set size of this process in bytes (VmRSS); ok
// is false when the OS does not say. It counts what the Go runtime does not
// see: the QuickJS heaps live in libc-allocated memory outside the Go heap.
func ProcessRSS() (uint64, bool) {
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0, false
	}
	i := bytes.Index(data, []byte("VmRSS:"))
	if i < 0 {
		return 0, false
	}
	f := bytes.Fields(data[i+6:])
	if len(f) < 1 {
		return 0, false
	}
	kb, err := strconv.ParseUint(string(f[0]), 10, 64)
	if err != nil {
		return 0, false
	}
	return kb << 10, true
}
