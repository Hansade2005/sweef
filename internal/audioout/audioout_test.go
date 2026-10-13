package audioout

import (
	"math"
	"testing"
	"time"
)

func sine(freq float64, rate, frames, ch int, amp float64) []float32 {
	out := make([]float32, frames*ch)
	for i := 0; i < frames; i++ {
		v := float32(amp * math.Sin(2*math.Pi*freq*float64(i)/float64(rate)))
		for c := 0; c < ch; c++ {
			out[i*ch+c] = v
		}
	}
	return out
}

func crossings(rec []float32, ch int) int {
	n := 0
	for i := 1; i < len(rec)/ch; i++ {
		if (rec[(i-1)*ch] < 0) != (rec[i*ch] < 0) {
			n++
		}
	}
	return n
}

func TestPassThroughAndVolume(t *testing.T) {
	o, sink := NewTestOutput()
	defer o.Close()
	s := o.Open(DeviceRate, 2, nil)
	in := sine(440, DeviceRate, DeviceRate/10, 2, 0.5)
	if n, err := s.Write(in); err != nil || n != DeviceRate/10 {
		t.Fatalf("write: %d %v", n, err)
	}
	s.SetVolume(0.5)
	s.Play()
	sink.Advance(100 * time.Millisecond)
	rec := sink.Recorded()
	if len(rec) != len(in) {
		t.Fatalf("recorded %d samples, want %d", len(rec), len(in))
	}
	// the gain ramps over the first chunk, so compare after it
	for i := chunkFrames * 2; i < len(in); i++ {
		if d := math.Abs(float64(rec[i] - in[i]*0.5)); d > 1e-6 {
			t.Fatalf("sample %d: got %v want %v", i, rec[i], in[i]*0.5)
		}
	}
}

func TestMuteKeepsClock(t *testing.T) {
	o, sink := NewTestOutput()
	defer o.Close()
	s := o.Open(DeviceRate, 2, nil)
	s.SetMuted(true)
	s.Write(sine(440, DeviceRate, DeviceRate/2, 2, 0.8))
	s.Play()
	sink.Advance(200 * time.Millisecond)
	if pk := sink.Peak(0, sink.Frames()); pk != 0 { // from the very first chunk: no fade-in from full volume
		t.Fatalf("muted stream leaked audio: peak %v", pk)
	}
	if p := s.Position(); p < 190*time.Millisecond || p > 210*time.Millisecond {
		t.Fatalf("clock should run while muted: %v", p)
	}
}

func TestResampleKeepsPitch(t *testing.T) {
	for _, rate := range []int{44100, 22050, 8000, 96000} {
		o, sink := NewTestOutput()
		s := o.Open(rate, 1, nil)
		s.Write(sine(1000, rate, rate, 1, 0.5)) // one second of 1 kHz
		s.Play()
		sink.Advance(900 * time.Millisecond)
		rec := sink.Recorded()
		// 900 ms of a 1 kHz tone: about 1800 zero crossings
		if c := crossings(rec, 2); c < 1780 || c > 1820 {
			t.Errorf("rate %d: %d zero crossings, want ~1800", rate, c)
		}
		var pk float32
		for _, v := range rec[200:] {
			if a := abs32(v); a > pk {
				pk = a
			}
		}
		if pk < 0.45 || pk > 0.52 {
			t.Errorf("rate %d: peak %v, want ~0.5", rate, pk)
		}
		o.Close()
	}
}

func TestChannelMapping(t *testing.T) {
	o, sink := NewTestOutput()
	defer o.Close()
	s := o.Open(DeviceRate, 6, nil)
	in := make([]float32, 6*4800)
	for i := 0; i < 4800; i++ {
		in[i*6+2] = 0.5 // centre only
	}
	s.Write(in)
	s.Play()
	sink.Advance(100 * time.Millisecond)
	rec := sink.Recorded()
	l, r := rec[2000], rec[2001]
	if l <= 0.1 || math.Abs(float64(l-r)) > 1e-6 {
		t.Fatalf("centre should fold equally into L and R, got %v %v", l, r)
	}
}

func TestPositionFlushAndUnderrun(t *testing.T) {
	o, sink := NewTestOutput()
	defer o.Close()
	s := o.Open(DeviceRate, 2, nil)
	s.Write(sine(300, DeviceRate, DeviceRate/5, 2, 0.3)) // 200 ms
	s.Play()
	sink.Advance(100 * time.Millisecond)
	if p := s.Position(); p != 100*time.Millisecond {
		t.Fatalf("position %v want 100ms", p)
	}
	if b := s.Buffered(); b != 100*time.Millisecond {
		t.Fatalf("buffered %v want 100ms", b)
	}
	w := s.Written()
	s.Flush()
	if s.Buffered() != 0 {
		t.Fatal("flush left data")
	}
	if got := s.Written(); got != w-100*time.Millisecond {
		t.Fatalf("written after flush %v want %v", got, w-100*time.Millisecond)
	}
	// starved: the clock must stand still and underruns are counted
	pos := s.Position()
	sink.Advance(100 * time.Millisecond)
	if s.Position() != pos {
		t.Fatalf("clock moved while starved: %v -> %v", pos, s.Position())
	}
	if s.Underruns() == 0 {
		t.Fatal("expected underruns")
	}
}

func TestLatencyShiftsPosition(t *testing.T) {
	sink := NewTestSink()
	sink.SetLatency(80 * time.Millisecond)
	o := mustOutput(sink)
	defer o.Close()
	s := o.Open(DeviceRate, 2, nil)
	s.Write(sine(300, DeviceRate, DeviceRate, 2, 0.3))
	s.Play()
	sink.Advance(200 * time.Millisecond)
	if p := s.Position(); p != 120*time.Millisecond {
		t.Fatalf("position %v want 120ms (200ms consumed - 80ms latency)", p)
	}
}

func TestBlockingWriteAndFlushRelease(t *testing.T) {
	o, sink := NewTestOutput()
	defer o.Close()
	s := o.Open(DeviceRate, 2, nil)
	big := sine(200, DeviceRate, DeviceRate*3, 2, 0.1) // more than the 2 s buffer
	done := make(chan error, 1)
	go func() { _, err := s.Write(big); done <- err }()
	select {
	case <-done:
		t.Fatal("write should block on a full, paused stream")
	case <-time.After(50 * time.Millisecond):
	}
	s.Play()
	sink.Advance(1500 * time.Millisecond) // frees space
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("write did not finish after the mixer drained")
	}
	// a blocked writer is released by Flush
	s2 := o.Open(DeviceRate, 2, nil)
	go func() { _, err := s2.Write(big); done <- err }()
	time.Sleep(50 * time.Millisecond)
	s2.Flush()
	select {
	case err := <-done:
		if err != ErrFlushed {
			t.Fatalf("got %v want ErrFlushed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("flush did not release the writer")
	}
}

func TestGroupAudibleAndMute(t *testing.T) {
	o, sink := NewTestOutput()
	defer o.Close()
	g := o.NewGroup("tab1")
	var events []bool
	g.OnAudible(func(a bool) { events = append(events, a) })
	s := o.Open(DeviceRate, 2, g)
	s.Write(sine(440, DeviceRate, DeviceRate, 2, 0.5))
	s.Play()
	sink.Advance(100 * time.Millisecond)
	o.Poll()
	if !g.Audible() || len(events) != 1 || !events[0] {
		t.Fatalf("group should be audible: %v %v", g.Audible(), events)
	}
	g.SetMuted(true)
	sink.Advance(100 * time.Millisecond)
	o.Poll()
	if !g.Audible() {
		t.Fatal("a muted tab stays marked audible")
	}
	if pk := sink.Peak(sink.Frames()-chunkFrames, sink.Frames()); pk != 0 {
		t.Fatalf("muted group still audible in the mix: %v", pk)
	}
	s.Pause()
	sink.Advance(2 * time.Second)
	o.Poll()
	if g.Audible() || len(events) != 2 || events[1] {
		t.Fatalf("group should go quiet: %v %v", g.Audible(), events)
	}
}

func TestSilentStreamIsNotAudible(t *testing.T) {
	o, sink := NewTestOutput()
	defer o.Close()
	g := o.NewGroup("t")
	s := o.Open(DeviceRate, 2, g)
	s.Write(make([]float32, DeviceRate*2))
	s.Play()
	sink.Advance(300 * time.Millisecond)
	o.Poll()
	if g.Audible() {
		t.Fatal("digital silence must not mark the tab audible")
	}
}

func TestSpeedChangesDuration(t *testing.T) {
	o, sink := NewTestOutput()
	defer o.Close()
	s := o.Open(DeviceRate, 2, nil)
	s.SetSpeed(2)
	s.Write(sine(500, DeviceRate, DeviceRate, 2, 0.4)) // 1 s of input at 2x = 0.5 s
	if w := s.Written(); w < 490*time.Millisecond || w > 510*time.Millisecond {
		t.Fatalf("written %v, want ~500ms for 1 s at 2x", w)
	}
	s.Play()
	sink.Advance(500 * time.Millisecond)
	if c := crossings(sink.Recorded(), 2); c < 960 || c > 1040 { // 1 kHz after pitch shift, 0.5 s
		t.Fatalf("crossings %d, want ~1000", c)
	}
}

func TestEndSignalsDone(t *testing.T) {
	o, sink := NewTestOutput()
	defer o.Close()
	s := o.Open(DeviceRate, 2, nil)
	s.Write(sine(500, DeviceRate, DeviceRate/10, 2, 0.4))
	s.End()
	s.Play()
	select {
	case <-s.Done():
		t.Fatal("done before drain")
	default:
	}
	sink.Advance(200 * time.Millisecond)
	select {
	case <-s.Done():
	default:
		t.Fatal("done not signalled after drain")
	}
	if s.Underruns() != 0 {
		t.Fatalf("an ended stream must not count underruns: %d", s.Underruns())
	}
}

func TestNullBackendRunsInRealTime(t *testing.T) {
	o := mustOutput(NewNullBackend())
	defer o.Close()
	if o.Real() {
		t.Fatal("null backend is not a real device")
	}
	s := o.Open(DeviceRate, 2, nil)
	s.Write(sine(440, DeviceRate, DeviceRate, 2, 0.3))
	s.Play()
	t0 := time.Now()
	time.Sleep(400 * time.Millisecond)
	p := s.Position()
	el := time.Since(t0)
	if d := p - (el - s.Latency()); d < -40*time.Millisecond || d > 40*time.Millisecond { // the heard position trails the wall clock by the device latency
		t.Fatalf("null sink clock %v vs wall %v (off by %v)", p, el, d)
	}
}

func TestDefaultFallsBackToNull(t *testing.T) {
	t.Setenv("SWEEF_AUDIO", "null")
	SetDefault(nil)
	o := Default()
	if o == nil || o.Real() {
		t.Fatal("expected null output")
	}
	SetDefault(nil)
}

func TestMaster(t *testing.T) {
	o, sink := NewTestOutput()
	defer o.Close()
	s := o.Open(DeviceRate, 2, nil)
	s.Write(sine(440, DeviceRate, DeviceRate/10, 2, 0.5))
	o.SetMasterVolume(0)
	s.Play()
	sink.Advance(100 * time.Millisecond)
	if sink.Peak(0, sink.Frames()) != 0 {
		t.Fatal("master volume 0 should silence the mix")
	}
}
