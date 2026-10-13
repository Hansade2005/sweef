// Package capture defines the device-independent contract between media sources (cameras, microphones, screen and
// tab capture, synthetic test sources) and the code that consumes them (getUserMedia/getDisplayMedia tracks, WebRTC
// senders, MediaRecorder). Platform code registers devices here; the engine never talks to a device API directly.
//
// Frames are planar I420 (YUV 4:2:0 8-bit) and audio is interleaved signed 16-bit PCM, because that is what both the
// VP8/H.264 encoders and the Opus encoder take, and what the decoders on the receive side produce.
package capture

import (
	"errors"
	"sort"
	"sync"
	"time"
)

// Kind is the MediaDeviceInfo kind of a device.
type Kind string

const (
	VideoInput  Kind = "videoinput"
	AudioInput  Kind = "audioinput"
	AudioOutput Kind = "audiooutput"
	// Screen is not a MediaDeviceInfo kind: it is the source behind getDisplayMedia (a tab, a window or a monitor).
	Screen Kind = "screen"
)

// DeviceInfo describes one capture device.
type DeviceInfo struct {
	ID      string // stable per machine, opaque to pages (the engine hashes it per origin before exposing it)
	GroupID string
	Label   string
	Kind    Kind
	// Facing is "user", "environment" or "" (unknown); only meaningful for cameras.
	Facing string
}

// Frame is one I420 video frame. Y has YStride*H bytes, U and V have UVStride*((H+1)/2) bytes each.
type Frame struct {
	W, H              int
	Y, U, V           []byte
	YStride, UVStride int
	PTS               time.Duration // capture time since the source started
}

// AudioChunk is a block of interleaved 16-bit PCM.
type AudioChunk struct {
	Rate     int
	Channels int
	PCM      []int16
	PTS      time.Duration
}

// VideoConstraints is the already-resolved request (the JS constraint solver picks concrete numbers; a source
// returns the closest mode it supports and reports it in the first frame's W/H).
type VideoConstraints struct {
	Width, Height int
	FrameRate     float64
}

// AudioConstraints: Rate 0 means the source's preferred rate; consumers resample.
type AudioConstraints struct {
	Rate, Channels                               int
	EchoCancellation, NoiseSuppression, AutoGain bool
}

// VideoSource produces frames until Stop. Start may be called once per source instance; Open a new instance for another consumer.
type VideoSource interface {
	Info() DeviceInfo
	// Start begins capture and returns the frame channel, which is closed after Stop or on a device error.
	Start(c VideoConstraints) (<-chan *Frame, error)
	Stop() error
	// Err reports why the channel closed (nil after a clean Stop).
	Err() error
}

// AudioSource produces PCM chunks until Stop.
type AudioSource interface {
	Info() DeviceInfo
	Start(c AudioConstraints) (<-chan *AudioChunk, error)
	Stop() error
	Err() error
}

// Provider enumerates and opens devices for one platform or one synthetic backend.
type Provider interface {
	Name() string
	Devices() []DeviceInfo
	OpenVideo(id string) (VideoSource, error)
	OpenAudio(id string) (AudioSource, error)
}

// ErrNotFound is returned when a device id is unknown or the device has disappeared.
var ErrNotFound = errors.New("capture: device not found")

// ErrInUse is returned when another application holds the device exclusively.
var ErrInUse = errors.New("capture: device in use")

// ErrUnsupported is returned by a provider that does not implement a kind on this platform.
var ErrUnsupported = errors.New("capture: not supported on this platform")

var (
	mu        sync.Mutex
	providers = map[string]Provider{}
)

// Register adds a provider (call from init()). Registering the same name twice replaces it.
func Register(p Provider) {
	mu.Lock()
	providers[p.Name()] = p
	mu.Unlock()
}

// Unregister removes a provider by name (tests).
func Unregister(name string) {
	mu.Lock()
	delete(providers, name)
	mu.Unlock()
}

// Devices lists every registered device of a kind, ordered by provider name then device order.
func Devices(kind Kind) []DeviceInfo {
	mu.Lock()
	names := make([]string, 0, len(providers))
	for n := range providers {
		names = append(names, n)
	}
	sort.Strings(names)
	ps := make([]Provider, 0, len(names))
	for _, n := range names {
		ps = append(ps, providers[n])
	}
	mu.Unlock()
	var out []DeviceInfo
	for _, p := range ps {
		for _, d := range p.Devices() {
			if d.Kind == kind {
				out = append(out, d)
			}
		}
	}
	return out
}

func find(id string, kind Kind) (Provider, bool) {
	mu.Lock()
	defer mu.Unlock()
	for _, p := range providers {
		for _, d := range p.Devices() {
			if d.ID == id && d.Kind == kind {
				return p, true
			}
		}
	}
	return nil, false
}

// OpenVideo opens the camera or screen source with this id.
func OpenVideo(id string) (VideoSource, error) {
	p, ok := find(id, VideoInput)
	if !ok {
		if p, ok = find(id, Screen); !ok {
			return nil, ErrNotFound
		}
	}
	return p.OpenVideo(id)
}

// OpenAudio opens the microphone with this id.
func OpenAudio(id string) (AudioSource, error) {
	p, ok := find(id, AudioInput)
	if !ok {
		return nil, ErrNotFound
	}
	return p.OpenAudio(id)
}
