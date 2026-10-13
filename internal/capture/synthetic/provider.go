// Package synthetic provides deterministic fake devices for tests, CI and demos: a test-pattern camera, a tone or
// noise microphone and a placeholder speaker. They exist only when the environment variable SWEEF_FAKE_DEVICES=1
// is set (SWEEF_FAKE_MEDIA=1, the older spelling, works too), the way Chrome's --use-fake-device-for-media-stream
// is opt-in. SWEEF_FAKE_AUDIO=noise|silence picks the microphone signal, SWEEF_FAKE_CAMERA=WxH@FPS the default
// camera mode.
package synthetic

import (
	"fmt"
	"os"
	"strings"

	"github.com/sweefbrowser/sweef/internal/capture"
)

// Device ids.
const (
	CameraID  = "synthetic-camera"
	MicID     = "synthetic-mic"
	SpeakerID = "synthetic-speaker"
)

// Enabled reports whether the fake devices are on.
func Enabled() bool {
	return os.Getenv("SWEEF_FAKE_DEVICES") == "1" || os.Getenv("SWEEF_FAKE_MEDIA") == "1"
}

type provider struct{}

func init() { capture.Register(provider{}) }

func (provider) Name() string { return "synthetic" }

func (provider) Devices() []capture.DeviceInfo {
	if !Enabled() {
		return nil
	}
	return []capture.DeviceInfo{
		{ID: CameraID, GroupID: "synthetic", Label: "Fake Camera", Kind: capture.VideoInput, Facing: "user"},
		{ID: MicID, GroupID: "synthetic", Label: "Fake Microphone", Kind: capture.AudioInput},
		{ID: SpeakerID, GroupID: "synthetic", Label: "Fake Speaker", Kind: capture.AudioOutput},
	}
}

func (provider) VideoCaps(id string) (capture.VideoCaps, bool) {
	if id != CameraID {
		return capture.VideoCaps{}, false
	}
	return capture.VideoCaps{Modes: Ladder}, true
}

func (provider) AudioCaps(id string) (capture.AudioCaps, bool) {
	if id != MicID {
		return capture.AudioCaps{}, false
	}
	return capture.AudioCaps{Rates: []int{48000}, MinChannels: 1, MaxChannels: 1, DefaultRate: 48000, DefaultChanns: 1}, true
}

func (p provider) OpenVideo(id string) (capture.VideoSource, error) {
	if !Enabled() || id != CameraID {
		return nil, capture.ErrNotFound
	}
	w, h, fps := 640, 480, 30.0
	if s := os.Getenv("SWEEF_FAKE_CAMERA"); s != "" {
		var ww, hh int
		var ff float64
		if n, _ := fmt.Sscanf(strings.ToLower(s), "%dx%d@%g", &ww, &hh, &ff); n >= 2 && ww > 0 && hh > 0 {
			w, h = ww, hh
			if n == 3 && ff > 0 {
				fps = ff
			}
		}
	}
	return NewCamera(capture.DeviceInfo{ID: CameraID, GroupID: "synthetic", Label: "Fake Camera", Kind: capture.VideoInput, Facing: "user"}, w, h, fps), nil
}

func (p provider) OpenAudio(id string) (capture.AudioSource, error) {
	if !Enabled() || id != MicID {
		return nil, capture.ErrNotFound
	}
	m := NewMic(capture.DeviceInfo{ID: MicID, GroupID: "synthetic", Label: "Fake Microphone", Kind: capture.AudioInput})
	switch os.Getenv("SWEEF_FAKE_AUDIO") {
	case "noise":
		m.Wave = Noise
	case "silence":
		m.Wave = Silence
	}
	return m, nil
}
