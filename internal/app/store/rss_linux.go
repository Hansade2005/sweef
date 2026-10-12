//go:build linux

package store

import (
	"bytes"
	"os"
	"strconv"
)

// ReadRSS returns the resident set size of this process from /proc.
func ReadRSS() (uint64, bool) {
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
