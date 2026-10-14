//go:build (amd64 || arm64) && !windows

package widevinehost

import (
	"time"

	"github.com/ebitengine/purego"
)

// Host::GetCurrentWallTime returns a double (cdm::Time, seconds since the Unix epoch) and
// Host::OnExpirationChange takes one as its last argument. On Linux and macOS purego callbacks can do both:
//
//   - a callback returning a struct with one float64 field returns it in xmm0 (SysV x86-64) or d0 (AAPCS64), which is
//     exactly where the C++ ABI puts a double return value;
//   - float64 parameters are read from the floating point argument registers (the fourth argument of
//     OnExpirationChange is the first floating point argument, so xmm0/d0).
type cdmTime struct{ T float64 }

func nowSeconds() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func newWallTimeCallback() (uintptr, error) {
	return purego.NewCallback(func(this uintptr) cdmTime { return cdmTime{T: nowSeconds()} }), nil
}

func newExpirationCallback(h func(this, sid, size uintptr, t float64)) (uintptr, error) {
	return purego.NewCallback(func(this, sid, size uintptr, t float64) uintptr {
		h(this, sid, size, t)
		return 0
	}), nil
}

// touchWallTime and startWallClock only matter on Windows, where the wall time lives in a memory cell.
func touchWallTime()                      {}
func startWallClock(stop <-chan struct{}) {}
