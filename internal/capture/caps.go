package capture

import "math"

// Mode is one resolution a camera can deliver and the frame-rate range it can deliver it at. It deliberately knows
// nothing about the pixel format: the provider picks the format when the source starts.
type Mode struct {
	W, H           int
	FPSMin, FPSMax float64
}

// VideoCaps is what the JS constraint solver and getCapabilities() work from.
type VideoCaps struct {
	Modes []Mode
}

// Bounds returns the extreme width, height and frame rate over all modes (zeroes when empty).
func (c VideoCaps) Bounds() (minW, maxW, minH, maxH int, minF, maxF float64) {
	for i, m := range c.Modes {
		if i == 0 {
			minW, maxW, minH, maxH, minF, maxF = m.W, m.W, m.H, m.H, m.FPSMin, m.FPSMax
			continue
		}
		minW, maxW = min(minW, m.W), max(maxW, m.W)
		minH, maxH = min(minH, m.H), max(maxH, m.H)
		minF, maxF = math.Min(minF, m.FPSMin), math.Max(maxF, m.FPSMax)
	}
	return
}

// AudioCaps describes a microphone.
type AudioCaps struct {
	Rates                      []int // sample rates the source delivers (usually just its native rate)
	MinChannels, MaxChannels   int
	DefaultRate, DefaultChanns int
}

// CapsProvider is implemented by providers that know the capabilities of their devices. Providers that do not are
// treated as able to deliver anything within DefaultVideoCaps / DefaultAudioCaps.
type CapsProvider interface {
	VideoCaps(id string) (VideoCaps, bool)
	AudioCaps(id string) (AudioCaps, bool)
}

// DefaultVideoCaps is the capability of a source that did not say: the usual webcam ladder at 1..60 fps.
func DefaultVideoCaps() VideoCaps {
	var c VideoCaps
	for _, s := range [][2]int{{160, 120}, {320, 240}, {640, 360}, {640, 480}, {800, 600}, {1280, 720}, {1920, 1080}} {
		c.Modes = append(c.Modes, Mode{W: s[0], H: s[1], FPSMin: 1, FPSMax: 60})
	}
	return c
}

// DefaultAudioCaps is the capability of a microphone that did not say.
func DefaultAudioCaps() AudioCaps {
	return AudioCaps{Rates: []int{48000}, MinChannels: 1, MaxChannels: 2, DefaultRate: 48000, DefaultChanns: 1}
}

// VideoCapsOf returns the capabilities of the camera or screen source with this id.
func VideoCapsOf(id string) VideoCaps {
	if p, ok := find(id, VideoInput); ok {
		if cp, ok := p.(CapsProvider); ok {
			if c, ok := cp.VideoCaps(id); ok && len(c.Modes) > 0 {
				return c
			}
		}
	}
	return DefaultVideoCaps()
}

// AudioCapsOf returns the capabilities of the microphone with this id.
func AudioCapsOf(id string) AudioCaps {
	if p, ok := find(id, AudioInput); ok {
		if cp, ok := p.(CapsProvider); ok {
			if c, ok := cp.AudioCaps(id); ok && len(c.Rates) > 0 {
				return c
			}
		}
	}
	return DefaultAudioCaps()
}

// ClosestMode picks the mode nearest to a requested size, preferring (in order) a mode that can reach the
// requested frame rate and, among equals, the larger frame rate. A zero request component means "don't care".
func ClosestMode(modes []Mode, w, h int, fps float64) (Mode, bool) {
	best, bestScore, ok := Mode{}, math.Inf(1), false
	for _, m := range modes {
		s := 0.0
		if w > 0 {
			s += math.Abs(float64(m.W-w)) / float64(max(m.W, w))
		}
		if h > 0 {
			s += math.Abs(float64(m.H-h)) / float64(max(m.H, h))
		}
		if fps > 0 && m.FPSMax < fps {
			s += 0.001 + (fps-m.FPSMax)/fps*0.0005 // can't reach the rate: lose to modes that can
		}
		s -= m.FPSMax * 1e-7
		if s < bestScore {
			best, bestScore, ok = m, s, true
		}
	}
	return best, ok
}
