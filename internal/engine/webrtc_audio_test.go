package engine

import (
	"math"
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/audioout"
)

// Remote call audio is heard through the audio output, in the tab's group: the tab's mute silences it. The test
// output has a virtual clock, so nothing here depends on timing.
func TestRTCAudioSinkPlaysThroughTabGroup(t *testing.T) {
	out, sink := audioout.NewTestOutput()
	audioout.SetDefault(out)
	t.Cleanup(func() { audioout.SetDefault(nil) })
	tab := newMediaTab(t)
	s := &rtcOutSink{t: tab}
	defer s.Close()

	pcm := make([]int16, 48000/10*2) // 100 ms stereo
	write := func() {
		for i := 0; i < len(pcm)/2; i++ {
			v := int16(12000 * math.Sin(2*math.Pi*440*float64(i)/48000))
			pcm[2*i], pcm[2*i+1] = v, v
		}
		s.Write(pcm, 48000, 2)
	}
	write()
	write()
	sink.Advance(150 * time.Millisecond)
	if pk := sink.Peak(0, sink.Frames()); pk < 0.2 {
		t.Fatalf("call audio did not reach the output: peak %v", pk)
	}

	tab.SetAudioMuted(true)
	from := sink.Frames()
	write()
	sink.Advance(100 * time.Millisecond)
	if pk := sink.Peak(from+960, sink.Frames()); pk != 0 { // after the first 20 ms (a gain ramp)
		t.Fatalf("muted tab still plays call audio: peak %v", pk)
	}
	tab.SetAudioMuted(false)

	// never blocks: a flood of audio with the device not pulling is dropped, not queued without limit
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			write()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("writing call audio blocked")
	}
}
