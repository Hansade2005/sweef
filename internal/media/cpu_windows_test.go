//go:build windows

package media

// cpuSeconds is not measured on Windows.
func cpuSeconds() float64 { return 0 }
