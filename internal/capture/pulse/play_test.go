package pulse

import (
	"math"
	"testing"
	"time"
)

func dialFake(t *testing.T, f *fakeServer) *Conn {
	t.Helper()
	c, err := Dial(f.opts())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestPlaybackStreamRoundTrip(t *testing.T) {
	f := newFake(t)
	f.play.sinkUsec = 20000
	c := dialFake(t, f)
	s, err := c.OpenPlayback("Sweef test", 48000, 2, 60*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	tlen := f.play.tlen
	f.mu.Unlock()
	if want := uint32(2880 * 8); tlen != want { // 60 ms of stereo float32
		t.Fatalf("tlength %d, want %d", tlen, want)
	}
	n, err := s.Wait(2 * time.Second)
	if err != nil || n != int(tlen) {
		t.Fatalf("first request: %d, %v", n, err)
	}
	// write a tone in pieces that are not a whole request; the server asks again for what it received
	pcm := make([]byte, 0, 4000*8)
	for i := 0; i < 4000; i++ {
		v := math.Float32bits(float32(0.5 * math.Sin(2*math.Pi*440*float64(i)/48000)))
		for k := 0; k < 2; k++ {
			pcm = append(pcm, byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
		}
	}
	for off := 0; off < len(pcm); off += 7000 {
		end := min(off+7000, len(pcm))
		if err := s.Write(pcm[off:end]); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		f.mu.Lock()
		got := len(f.play.playData)
		f.mu.Unlock()
		if got == len(pcm)/8*8 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server received %d of %d bytes", got, len(pcm))
		}
		time.Sleep(5 * time.Millisecond)
	}
	f.mu.Lock()
	same := string(f.play.playData) == string(pcm)
	f.mu.Unlock()
	if !same {
		t.Fatal("the bytes the server received differ from what was written")
	}

	// requests keep coming as data is consumed
	if n, err := s.Wait(time.Second); err != nil || n < 1000 {
		t.Fatalf("no new request after writing: %d, %v", n, err)
	}

	// timing: the server holds half of what was written
	tm, err := s.Timing()
	if err != nil {
		t.Fatal(err)
	}
	held := float64(len(pcm)/2) / 8 / 48000
	want := 0.020 + held
	if math.Abs(tm.Latency.Seconds()-want) > 0.002 || !tm.Playing || tm.Written != int64(len(pcm)) {
		t.Fatalf("timing %+v, want latency %.4f s written %d", tm, want, len(pcm))
	}
}

func TestPlaybackControls(t *testing.T) {
	f := newFake(t)
	c := dialFake(t, f)
	s, err := c.OpenPlayback("Sweef test", 44100, 1, 40*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Cork(true); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	corked := f.play.corked
	f.mu.Unlock()
	if !corked {
		t.Fatal("cork not sent")
	}
	if err := s.Cork(false); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := s.Drain(time.Second); err != nil {
		t.Fatal(err)
	}
	if err := s.SetVolume(0.5); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	p := f.play
	f.mu.Unlock()
	if p.corked || p.flushes != 1 || p.drains != 1 {
		t.Fatalf("server state %+v", p)
	}
	if want := uint32(math.Cbrt(0.5)*0x10000 + 0.5); p.volume != want {
		t.Fatalf("volume %d, want %d (cubic scale)", p.volume, want)
	}
	s.Close()
	f.mu.Lock()
	del := f.play.deleted
	f.mu.Unlock()
	if del != 1 {
		t.Fatalf("stream deleted %d times", del)
	}
	if _, err := s.Wait(50 * time.Millisecond); err == nil {
		t.Fatal("Wait on a closed stream must fail")
	}
}

func TestPlaybackKilledAndServerGone(t *testing.T) {
	f := newFake(t)
	c := dialFake(t, f)
	s, err := c.OpenPlayback("Sweef test", 48000, 2, 40*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.play.killOnWrite = true
	f.mu.Unlock()
	if _, err := s.Wait(time.Second); err != nil {
		t.Fatal(err)
	}
	_ = s.Write(make([]byte, 800))
	deadline := time.Now().Add(2 * time.Second)
	for s.Err() == nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if s.Err() == nil {
		t.Fatal("a killed playback stream must report an error")
	}

	c2 := dialFake(t, f)
	s2, err := c2.OpenPlayback("Sweef test", 48000, 2, 40*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	f.killAll()
	for deadline := time.Now().Add(2 * time.Second); s2.Err() == nil && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := s2.Wait(2 * time.Second); err == nil {
		t.Fatal("Wait after the server went away must fail")
	}
}

func TestPlaybackRejectsUnsupportedChannels(t *testing.T) {
	f := newFake(t)
	c := dialFake(t, f)
	if _, err := c.OpenPlayback("x", 48000, 6, 40*time.Millisecond); err == nil {
		t.Fatal("6 channels must be refused")
	}
}
