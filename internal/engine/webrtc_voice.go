package engine

import (
	"github.com/sweefbrowser/sweef/internal/rtcmedia"
	"github.com/sweefbrowser/sweef/internal/rtcmedia/audioproc"
)

// Voice processing (echo cancellation, noise suppression, automatic gain) runs on the WebRTC send path: the
// processor of a peer connection hears what the peer plays (the far end, the remote WebRTC audio) and cleans what
// the microphone track sends. Audio that does not go through a peer connection (MediaRecorder, Web Audio) is not
// processed, which the track settings do not claim to change: they report what the page asked for.

func init() {
	rtcProcMake = func() rtcmedia.AudioProcessor { return audioproc.New() }
}

// optioner is implemented by processors whose stages can be switched.
type optioner interface{ SetOptions(audioproc.Options) }

// setVoiceProcessing applies the track's echoCancellation / noiseSuppression / autoGainControl settings to the
// processor of the peer. A setting the track does not carry counts as on, as in Chrome.
func (p *rtcPeer) setVoiceProcessing(d RTCTrackDesc) {
	pr := p.audioProcessor()
	o, ok := pr.(optioner)
	if !ok {
		return
	}
	on := func(b *bool) bool { return b == nil || *b }
	o.SetOptions(audioproc.Options{EchoCancellation: on(d.EchoCancellation), NoiseSuppression: on(d.NoiseSuppression), AutoGain: on(d.AutoGainControl)})
}
