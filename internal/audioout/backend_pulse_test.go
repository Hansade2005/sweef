//go:build linux || freebsd || netbsd || openbsd || dragonfly || solaris

package audioout

import (
	"encoding/binary"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/capture/pulse"
)

// fakePlay answers the backend like a sound server would: it asks for an amount, records what is written and
// shrinks the request by it.
type fakePlay struct {
	mu      sync.Mutex
	ask     int
	written []byte
	corks   []bool
	err     error
	closed  bool
	lat     time.Duration
}

func (f *fakePlay) Wait(timeout time.Duration) (int, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		n, err := f.ask, f.err
		f.mu.Unlock()
		if err != nil {
			return 0, err
		}
		if n > 0 {
			return n, nil
		}
		time.Sleep(time.Millisecond)
	}
	return 0, nil
}

func (f *fakePlay) Write(p []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.written = append(f.written, p...)
	f.ask -= len(p)
	return nil
}

func (f *fakePlay) Timing() (pulse.Timing, error) {
	return pulse.Timing{Latency: f.lat, Playing: true}, nil
}

func (f *fakePlay) Cork(on bool) error {
	f.mu.Lock()
	f.corks = append(f.corks, on)
	f.mu.Unlock()
	return nil
}

func (f *fakePlay) Err() error { f.mu.Lock(); defer f.mu.Unlock(); return f.err }
func (f *fakePlay) Close()     { f.mu.Lock(); f.closed = true; f.mu.Unlock() }

func (f *fakePlay) request(n int) { f.mu.Lock(); f.ask += n; f.mu.Unlock() }
func (f *fakePlay) got() int      { f.mu.Lock(); defer f.mu.Unlock(); return len(f.written) }

func TestPulseBackendAnswersRequestsWithMixedAudio(t *testing.T) {
	fp := &fakePlay{}
	b := newPulseBackend(fp)
	var pulls, frames int
	var mu sync.Mutex
	err := b.Start(func(dst []float32) {
		mu.Lock()
		pulls++
		frames += len(dst) / DeviceChannels
		mu.Unlock()
		for i := range dst {
			dst[i] = 0.25
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Start(func([]float32) {}); err == nil {
		t.Fatal("a second Start must fail")
	}
	fp.request(2880 * 8) // 60 ms
	deadline := time.Now().Add(10 * time.Second)
	for fp.got() < 2880*8 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if fp.got() != 2880*8 {
		t.Fatalf("wrote %d bytes, want %d", fp.got(), 2880*8)
	}
	mu.Lock()
	p, fr := pulls, frames
	mu.Unlock()
	if fr != 2880 || p < 3 { // pulled in chunks of at most 20 ms
		t.Fatalf("pulls %d frames %d", p, fr)
	}
	fp.mu.Lock()
	v := math.Float32frombits(binary.LittleEndian.Uint32(fp.written[:4]))
	fp.mu.Unlock()
	if v != 0.25 {
		t.Fatalf("first sample %v, want 0.25 (float32 little endian)", v)
	}
	if !b.Real() || b.Name() != "pulse" {
		t.Fatal("backend identity")
	}

	// suspend and resume cork the stream, once each
	b.Suspend()
	b.Suspend()
	b.Resume()
	fp.mu.Lock()
	c := append([]bool(nil), fp.corks...)
	fp.mu.Unlock()
	if len(c) != 2 || !c[0] || c[1] {
		t.Fatalf("corks %v", c)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	fp.mu.Lock()
	closed := fp.closed
	fp.mu.Unlock()
	if !closed {
		t.Fatal("stream not closed")
	}
}

func TestPulseBackendKeepsTheClockWhenTheServerGoesAway(t *testing.T) {
	fp := &fakePlay{}
	b := newPulseBackend(fp)
	var mu sync.Mutex
	frames := 0
	_ = b.Start(func(dst []float32) { mu.Lock(); frames += len(dst) / DeviceChannels; mu.Unlock() })
	fp.mu.Lock()
	fp.err = errors.New("connection reset")
	fp.mu.Unlock()
	time.Sleep(600 * time.Millisecond)
	mu.Lock()
	got := frames
	mu.Unlock()
	// the clock goes on in real time (about half a second of frames); a loaded machine may be slower, and it is
	// never much faster than real time
	if got < DeviceRate/20 || got > DeviceRate {
		t.Fatalf("after the server failed the clock pulled %d frames in 0.6 s", got)
	}
	if b.Real() {
		t.Fatal("a backend whose stream failed must not claim to be a real device")
	}
	b.Close()
}

func TestPulseBackendLatencyFollowsServerTiming(t *testing.T) {
	fp := &fakePlay{lat: 90 * time.Millisecond}
	b := newPulseBackend(fp)
	_ = b.Start(func(dst []float32) {})
	defer b.Close()
	deadline := time.Now().Add(10 * time.Second)
	for b.Latency() != 90*time.Millisecond && time.Now().Before(deadline) {
		fp.request(8 * 480)
		time.Sleep(20 * time.Millisecond)
	}
	if b.Latency() != 90*time.Millisecond {
		t.Fatalf("latency %v, want the server's 90 ms", b.Latency())
	}
}
