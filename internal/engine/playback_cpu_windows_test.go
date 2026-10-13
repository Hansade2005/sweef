//go:build windows

package engine

import "time"

// cpuTime is not measured on Windows (the playback report then shows 0% CPU).
func cpuTime() time.Duration { return 0 }
