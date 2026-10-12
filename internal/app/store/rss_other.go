//go:build !linux

package store

// ReadRSS is only exact on Linux; other systems fall back to the Go
// runtime's view (callers see exact=false).
func ReadRSS() (uint64, bool) { return 0, false }
