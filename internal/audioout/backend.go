package audioout

import "time"

// Device format. Every backend receives interleaved float32 frames in this
// format, so the mixer converts exactly once.
const (
	DeviceRate     = 48000
	DeviceChannels = 2
)

// Backend is an audio device (or a stand-in) driven by the mixer.
type Backend interface {
	// Name is a short label for logs and diagnostics ("oto", "null", "test").
	Name() string
	// Start begins pulling audio. pull fills dst (interleaved stereo float32
	// at DeviceRate) with exactly len(dst) samples and is called from one
	// goroutine at a time. It does not block for long.
	Start(pull func(dst []float32)) error
	// Latency is the estimated time between pull returning a frame and the
	// listener hearing it.
	Latency() time.Duration
	// Now is the backend's clock. Real backends return time.Now(); the test
	// sink returns its virtual time so tests are deterministic.
	Now() time.Time
	// Close stops the device. The mixer calls it at most once.
	Close() error
	// Real reports whether sound reaches a device (false for null and test).
	Real() bool
}
