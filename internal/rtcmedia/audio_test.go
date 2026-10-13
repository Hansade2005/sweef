package rtcmedia

import (
	"math"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/sweefbrowser/sweef/internal/capture"
	"github.com/sweefbrowser/sweef/internal/rtcmedia/testsrc"
)

func TestResampler(t *testing.T) {
	for _, c := range []struct{ in, out int }{{44100, 48000}, {16000, 48000}, {96000, 48000}, {48000, 48000}, {8000, 48000}} {
		r := NewResampler(c.in, c.out)
		const hz = 1000.0
		n := c.in / 2
		src := make([]float32, n)
		for i := range src {
			src[i] = float32(0.5 * math.Sin(2*math.Pi*hz*float64(i)/float64(c.in)))
		}
		var out []float32
		for i := 0; i < n; i += 441 { // arbitrary chunking
			end := i + 441
			if end > n {
				end = n
			}
			out = r.Process(out, src[i:end])
		}
		wantN := n * c.out / c.in
		if d := len(out) - wantN; d < -64 || d > 64 {
			t.Errorf("%d->%d: %d samples, want about %d", c.in, c.out, len(out), wantN)
		}
		pcm := make([]int16, len(out))
		for i, v := range out {
			pcm[i] = int16(v * 32767)
		}
		skip := 400
		if lvl := testsrc.ToneLevel(pcm[skip:], 1, c.out, hz); lvl < 0.98 {
			t.Errorf("%d->%d: tone purity %.3f", c.in, c.out, lvl)
		}
	}
}

func TestAudioLoopback(t *testing.T) {
	p := newPair(t, nil)
	defer p.close()
	lt := NewLocalTrack(webrtc.RTPCodecTypeAudio, "a0", "s0")
	rs, err := p.a.AddTrack(lt)
	if err != nil {
		t.Fatal(err)
	}
	as, err := NewAudioSender(lt, rs)
	if err != nil {
		t.Fatal(err)
	}
	defer as.Close()
	tone := testsrc.NewTone(440)
	tone.Rate = 44100 // exercises the resampler
	if err := as.SetSource(tone, capture.AudioConstraints{}); err != nil {
		t.Fatal(err)
	}
	sink := &RecordSink{}
	var ar *AudioReceiver
	got := make(chan struct{})
	p.b.OnTrack(func(tr *webrtc.TrackRemote, rcv *webrtc.RTPReceiver) {
		ar, _ = NewAudioReceiver(tr, rcv, sink)
		close(got)
	})
	p.connect(t)
	<-got
	defer ar.Close()
	time.Sleep(3 * time.Second)
	pcm, rate, ch := sink.Snapshot()
	if rate != 48000 || ch != 2 || len(pcm) < 48000*2*2 {
		t.Fatalf("received %d samples at %d Hz, %d ch", len(pcm), rate, ch)
	}
	pcm = pcm[len(pcm)/2:] // skip the start-up
	lvl := testsrc.ToneLevel(pcm, 2, 48000, 440)
	t.Logf("tone purity at 440 Hz: %.3f over %d samples", lvl, len(pcm)/2)
	if lvl < 0.9 {
		t.Errorf("tone not received: %.3f", lvl)
	}
	st := ar.Stats()
	t.Logf("receiver: %d packets, %d concealed samples, %d FEC, audio level %.3f, jitter buffer target %.0f ms", st.PacketsReceived, st.ConcealedSamples, st.FECRecovered, st.AudioLevel, st.JitterBufferTarget*1000)
	if st.AudioLevel < 0.05 {
		t.Errorf("audio level %.3f", st.AudioLevel)
	}
}
