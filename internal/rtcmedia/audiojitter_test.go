package rtcmedia

import (
	"math"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/thesyncim/gopus"

	"github.com/sweefbrowser/sweef/internal/rtcmedia/testsrc"
)

func newTestAudioReceiver(t *testing.T, sink AudioSink) *AudioReceiver {
	dec, err := gopus.NewDecoder(gopus.DefaultDecoderConfig(opusRate, 2))
	if err != nil {
		t.Fatal(err)
	}
	return &AudioReceiver{ssrc: 7, sink: sink, pkts: map[uint32]*apkt{}, dec: dec, target: 3, muted: true, t0: time.Now(),
		pcm: make([]int16, 5760*2), f32: make([]float32, 5760*2), done: make(chan struct{})}
}

func toneFrames(t *testing.T, n int, lossPct int) [][]byte {
	enc, err := gopus.NewEncoder(gopus.EncoderConfig{SampleRate: opusRate, Channels: 1, Application: gopus.ApplicationVoIP})
	if err != nil {
		t.Fatal(err)
	}
	_ = enc.SetBitrate(32000)
	enc.SetFEC(true)
	_ = enc.SetPacketLoss(lossPct)
	var out [][]byte
	phase := 0.0
	for i := 0; i < n; i++ {
		pcm := make([]int16, opusFrame)
		for k := range pcm {
			pcm[k] = int16(0.4 * 32767 * math.Sin(phase))
			phase += 2 * math.Pi * 440 / opusRate
		}
		buf := make([]byte, 400)
		m, err := enc.EncodeInt16(pcm, buf)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, buf[:m])
	}
	return out
}

// runAudio plays frames through the receiver with a controlled clock; lost lists frame indexes that never
// arrive, jitterMs delays every third packet.
func runAudio(t *testing.T, frames [][]byte, lost map[int]bool, jitterMs int) (*AudioReceiver, *RecordSink) {
	sink := &RecordSink{}
	a := newTestAudioReceiver(t, sink)
	now := time.Now()
	type arrival struct {
		at  time.Time
		idx int
	}
	var arrivals []arrival
	for i := range frames {
		if lost[i] {
			continue
		}
		at := now.Add(time.Duration(i) * 20 * time.Millisecond)
		if jitterMs > 0 && i%3 == 0 {
			at = at.Add(time.Duration(jitterMs) * time.Millisecond)
		}
		arrivals = append(arrivals, arrival{at, i})
	}
	next := 0
	for tick := 0; tick < len(frames)+10; tick++ {
		tnow := now.Add(time.Duration(tick) * 20 * time.Millisecond)
		for next < len(arrivals) {
			// deliver in arrival-time order
			best := next
			for k := next; k < len(arrivals); k++ {
				if arrivals[k].at.Before(arrivals[best].at) {
					best = k
				}
			}
			if arrivals[best].at.After(tnow) {
				break
			}
			arrivals[next], arrivals[best] = arrivals[best], arrivals[next]
			p := &rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: uint16(65500 + arrivals[next].idx), Timestamp: uint32(arrivals[next].idx) * opusFrame}, Payload: frames[arrivals[next].idx]}
			a.insert(p, arrivals[next].at)
			next++
		}
		a.tick(tnow)
	}
	return a, sink
}

func TestAudioReceiverClean(t *testing.T) {
	frames := toneFrames(t, 150, 5)
	a, sink := runAudio(t, frames, nil, 0)
	pcm, rate, ch := sink.Snapshot()
	st := a.Stats()
	if rate != 48000 || ch != 2 {
		t.Fatalf("format %d Hz %d ch", rate, ch)
	}
	got := len(pcm) / 2
	if played := got - int(st.ConcealedSamples); played != 150*opusFrame {
		t.Errorf("%d samples of real audio out for 150 frames in (wanted %d)", played, 150*opusFrame)
	}
	if lvl := testsrc.ToneLevel(pcm[len(pcm)/4:], 2, 48000, 440); lvl < 0.99 {
		t.Errorf("tone purity %.3f", lvl)
	}
	if st.ConcealedSamples > 5*opusFrame { // only the run-out after the last packet, at most 100 ms
		t.Errorf("%d concealed samples on a clean stream", st.ConcealedSamples)
	}
	if a.target > 4 {
		t.Errorf("jitter buffer target %d packets on a jitter-free stream", a.target)
	}
}

func TestAudioReceiverReordersAndConceals(t *testing.T) {
	frames := toneFrames(t, 200, 20)
	// jitter of 30 ms on every third packet: reordering the buffer has to absorb; 1 isolated loss (FEC) and 3 in a row (PLC)
	lost := map[int]bool{60: true, 120: true, 121: true, 122: true}
	a, sink := runAudio(t, frames, lost, 30)
	pcm, _, _ := sink.Snapshot()
	st := a.Stats()
	t.Logf("jitter estimate %.1f ms; %d samples out; concealment events %d, concealed samples %d, FEC recovered %d, discarded %d, target %d packets",
		a.jitter*1000, len(pcm)/2, st.ConcealmentEvents, st.ConcealedSamples, st.FECRecovered, st.PacketsDiscarded, a.target)
	if st.ConcealmentEvents < 4 {
		t.Errorf("concealment events %d, want at least 4 (4 lost packets)", st.ConcealmentEvents)
	}
	if st.FECRecovered == 0 {
		t.Errorf("the isolated loss was not recovered from the next packet's in-band FEC")
	}
	if got := len(pcm) / 2; got < 190*opusFrame {
		t.Errorf("only %d samples played of %d: the output stalled", got, 200*opusFrame)
	}
	if lvl := testsrc.ToneLevel(pcm[len(pcm)/4:], 2, 48000, 440); lvl < 0.9 {
		t.Errorf("tone purity %.3f with concealment", lvl)
	}
}
