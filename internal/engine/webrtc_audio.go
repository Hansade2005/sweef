package engine

// webrtc_audio.go: remote WebRTC audio goes to the tab's audio group of internal/audioout, so call audio is
// heard through the mixer and the tab's mute button silences it.

import (
	"sync"
	"time"

	"github.com/sweefbrowser/sweef/internal/audioout"
	"github.com/sweefbrowser/sweef/internal/rtcmedia"
)

// rtcOutSink plays decoded PCM into an audioout stream of the tab's group. It never blocks the receiver: audio that
// would pile up (the device is slower than the network, or the mixer is stopped) is dropped to keep the delay low.
type rtcOutSink struct {
	t *Tab

	mu       sync.Mutex
	st       *audioout.Stream
	rate, ch int
	buf      []float32
	started  bool
	closed   bool
}

const (
	rtcOutMaxBuffer = 300 * time.Millisecond // above this, incoming audio is dropped
	rtcOutStart     = 40 * time.Millisecond  // buffered before playback starts
)

func (s *rtcOutSink) Write(pcm []int16, rate, channels int) {
	if len(pcm) == 0 || rate <= 0 || channels <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	if s.st == nil || s.rate != rate || s.ch != channels {
		if s.st != nil {
			s.st.Close()
		}
		s.st = audioout.Default().Open(rate, channels, s.t.audioGroup())
		s.rate, s.ch, s.started = rate, channels, false
	}
	if s.st.Buffered() > rtcOutMaxBuffer {
		return
	}
	if cap(s.buf) < len(pcm) {
		s.buf = make([]float32, len(pcm))
	}
	b := s.buf[:len(pcm)]
	for i, v := range pcm {
		b[i] = float32(v) / 32768
	}
	// the stream accepts two seconds, so with the check above this does not block
	if _, err := s.st.Write(b); err != nil {
		return
	}
	if !s.started && s.st.Buffered() >= rtcOutStart {
		s.st.Play()
		s.started = true
	}
}

// Close releases the stream.
func (s *rtcOutSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.st != nil {
		s.st.Close()
		s.st = nil
	}
	return nil
}

// audioSink returns the sink for received audio of this peer's tab: the factory installed with
// SetRTCAudioSinkFactory if there is one (tests, embedders), otherwise the audio output.
func (p *rtcPeer) audioSink() rtcmedia.AudioSink {
	rtcSinkMu.Lock()
	f := rtcSinkMake
	rtcSinkMu.Unlock()
	if f != nil {
		return f()
	}
	if p.h == nil || p.h.j == nil || p.h.j.tab == nil {
		return rtcmedia.NullSink{}
	}
	return &rtcOutSink{t: p.h.j.tab}
}
