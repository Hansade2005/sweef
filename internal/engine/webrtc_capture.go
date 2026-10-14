package engine

import (
	"sync"

	"github.com/sweefbrowser/sweef/internal/capture"
)

// A track made by getUserMedia/getDisplayMedia (internal/capture) is sent through WebRTC by subscribing to the
// live track: the camera is shared with previews and recorders, track.enabled=false yields black frames or silence
// (done by the track), and the sender ending does not stop the camera (only track.stop() does).

// captureTrackSource returns the RTC source for a page track id that belongs to a live capture track.
func captureTrackSource(id string) (RTCTrackSource, bool) {
	ct := capture.LookupTrack(id)
	if ct == nil {
		return RTCTrackSource{}, false
	}
	return RTCTrackSource{
		OpenVideo: func(capture.VideoConstraints) (capture.VideoSource, error) { return &subVideo{t: ct}, nil },
		OpenAudio: func(capture.AudioConstraints) (capture.AudioSource, error) { return &subAudio{t: ct}, nil },
	}, true
}

type subVideo struct {
	t      *capture.Track
	mu     sync.Mutex
	cancel func()
}

func (s *subVideo) Info() capture.DeviceInfo { return s.t.Device }
func (s *subVideo) Start(capture.VideoConstraints) (<-chan *capture.Frame, error) {
	ch, cancel := s.t.SubscribeVideo()
	s.mu.Lock()
	s.cancel = cancel
	s.mu.Unlock()
	return ch, nil
}
func (s *subVideo) Stop() error {
	s.mu.Lock()
	c := s.cancel
	s.cancel = nil
	s.mu.Unlock()
	if c != nil {
		c()
	}
	return nil
}
func (s *subVideo) Err() error { return nil }

type subAudio struct {
	t      *capture.Track
	mu     sync.Mutex
	cancel func()
}

func (s *subAudio) Info() capture.DeviceInfo { return s.t.Device }
func (s *subAudio) Start(capture.AudioConstraints) (<-chan *capture.AudioChunk, error) {
	ch, cancel := s.t.SubscribeAudio()
	s.mu.Lock()
	s.cancel = cancel
	s.mu.Unlock()
	return ch, nil
}
func (s *subAudio) Stop() error {
	s.mu.Lock()
	c := s.cancel
	s.cancel = nil
	s.mu.Unlock()
	if c != nil {
		c()
	}
	return nil
}
func (s *subAudio) Err() error { return nil }
