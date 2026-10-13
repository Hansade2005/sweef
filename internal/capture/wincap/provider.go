//go:build windows && (amd64 || arm64)

// UNVERIFIED: never run on real hardware (see com.go).

package wincap

import (
	"sync"
	"time"

	"github.com/sweefbrowser/sweef/internal/capture"
)

// Provider lists Media Foundation cameras and WASAPI microphones. The lists are cached for two seconds because
// the engine asks often and each enumeration costs a COM round trip.
type Provider struct {
	mu   sync.Mutex
	at   time.Time
	cams []camInfo
	mics []micInfo
}

// NewProvider returns the Windows provider.
func NewProvider() *Provider { return &Provider{} }

func (p *Provider) Name() string { return "wincap" }

func (p *Provider) refresh() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.at.IsZero() && time.Since(p.at) < 2*time.Second {
		return
	}
	p.at = time.Now()
	p.cams, _ = camList()
	p.mics, _ = micList()
}

func (p *Provider) Devices() []capture.DeviceInfo {
	p.refresh()
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []capture.DeviceInfo
	for _, c := range p.cams {
		out = append(out, capture.DeviceInfo{ID: c.link, GroupID: c.link, Label: c.name, Kind: capture.VideoInput})
	}
	for _, m := range p.mics {
		out = append(out, capture.DeviceInfo{ID: m.id, GroupID: m.id, Label: m.name, Kind: capture.AudioInput})
	}
	return out
}

func (p *Provider) OpenVideo(id string) (capture.VideoSource, error) {
	p.refresh()
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.cams {
		if c.link == id {
			return &camSource{info: capture.DeviceInfo{ID: id, GroupID: id, Label: c.name, Kind: capture.VideoInput}, link: id}, nil
		}
	}
	return nil, capture.ErrNotFound
}

func (p *Provider) OpenAudio(id string) (capture.AudioSource, error) {
	p.refresh()
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, m := range p.mics {
		if m.id == id {
			return &micSource{info: capture.DeviceInfo{ID: id, GroupID: id, Label: m.name, Kind: capture.AudioInput}, id: id}, nil
		}
	}
	return nil, capture.ErrNotFound
}

// AudioCaps reports mono or stereo: the shared-mode mix format decides the rate when the stream starts, so the
// rate list is the common one and the track settings report what the stream really delivers.
func (p *Provider) AudioCaps(string) (capture.AudioCaps, bool) {
	return capture.AudioCaps{Rates: []int{48000}, MinChannels: 1, MaxChannels: 2, DefaultRate: 48000, DefaultChanns: 1}, true
}

// VideoCaps: the native formats are only known once the camera is opened, and opening a camera to list its modes
// would light its indicator before the user has agreed to anything. The generic ladder is used and the real mode
// is whatever the nearest native one is; getSettings() reports it.
func (p *Provider) VideoCaps(string) (capture.VideoCaps, bool) { return capture.VideoCaps{}, false }
