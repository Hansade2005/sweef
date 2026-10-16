//go:build !linux

package engine

// ProcessRSS is not read from the OS here; callers fall back to the Go
// runtime's view (MemStats.Sys).
func ProcessRSS() (uint64, bool) { return 0, false }
