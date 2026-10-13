// Package platform registers the capture providers of the current operating system. Import it for its side effect
// (the engine does); the providers are cheap to register because they probe lazily.
//
//	linux:   V4L2 cameras (amd64, arm64) and PulseAudio / PipeWire microphones
//	windows: Media Foundation cameras and WASAPI microphones   (UNVERIFIED: never run on real hardware)
//	darwin:  AVFoundation cameras and microphones              (UNVERIFIED: never run on real hardware)
//
// See docs/capture.md for what each backend does and how it was checked.
package platform

import "github.com/sweefbrowser/sweef/internal/capture"

// Providers returns the providers that Register installed, for tools that list them.
func Providers() []capture.Provider { return registered }

var registered []capture.Provider

func register(p capture.Provider) {
	registered = append(registered, p)
	capture.Register(p)
}
