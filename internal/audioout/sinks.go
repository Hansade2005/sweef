package audioout

import (
	"sync"
	"time"
)

// chunkFrames is the size of one mixer pull of the stand-in backends (10 ms).
const chunkFrames = DeviceRate / 100

// ---- null sink -----------------------------------------------------------------------

// nullBackend consumes audio at exactly the real-time rate and throws it
// away. It is what the browser uses without an audio device, so the audio
// clock, A/V sync and "audible" indicator still behave.
type nullBackend struct {
	mu     sync.Mutex
	stop   chan struct{}
	done   chan struct{}
	closed bool
}

// NewNullBackend returns the real-time null sink.
func NewNullBackend() Backend {
	return &nullBackend{stop: make(chan struct{}), done: make(chan struct{})}
}

func (n *nullBackend) Name() string           { return "null" }
func (n *nullBackend) Latency() time.Duration { return 0 }
func (n *nullBackend) Now() time.Time         { return time.Now() }
func (n *nullBackend) Real() bool             { return false }

func (n *nullBackend) Start(pull func([]float32)) error {
	go func() {
		defer close(n.done)
		buf := make([]float32, chunkFrames*DeviceChannels)
		start := time.Now()
		var sent int64
		t := time.NewTicker(10 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-n.stop:
				return
			case <-t.C:
			}
			// pull every chunk whose playout interval has begun (catches up after a stall)
			due := int64(time.Since(start).Seconds()*DeviceRate)/chunkFrames + 1
			for sent/chunkFrames < due {
				pull(buf)
				sent += chunkFrames
			}
		}
	}()
	return nil
}

func (n *nullBackend) Close() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.closed {
		n.closed = true
		close(n.stop)
		<-n.done
	}
	return nil
}

// ---- test sink ------------------------------------------------------------------------

// TestSink is a backend that records everything the mixer produces. In
// manual mode (the default) time is virtual and advances only through
// Advance, so tests are deterministic and as fast as the CPU allows. With
// Realtime it paces itself on the wall clock like the null sink.
type TestSink struct {
	mu       sync.Mutex
	pull     func([]float32)
	now      time.Time
	rec      []float32 // interleaved stereo, DeviceRate
	realtime bool
	lat      time.Duration
	stop     chan struct{}
	done     chan struct{}
	pulls    int
	started  time.Time // wall time of the first chunk (real-time mode)
}

// NewTestSink returns a manual-mode sink with a virtual clock and no
// latency. Use NewTestOutput for the common case.
func NewTestSink() *TestSink {
	return &TestSink{now: time.Unix(1_700_000_000, 0), stop: make(chan struct{}), done: make(chan struct{})}
}

// NewTestOutput returns an Output over a manual TestSink.
func NewTestOutput() (*Output, *TestSink) {
	s := NewTestSink()
	return mustOutput(s), s
}

// NewRealtimeTestOutput returns an Output over a recording sink that runs on
// the wall clock (for tests that exercise real timing, such as A/V sync).
func NewRealtimeTestOutput() (*Output, *TestSink) {
	s := NewTestSink()
	s.realtime = true
	return mustOutput(s), s
}

// SetLatency sets the latency the sink reports (call before NewOutput's
// first stream; it models a device buffer).
func (t *TestSink) SetLatency(d time.Duration) { t.mu.Lock(); t.lat = d; t.mu.Unlock() }

func (t *TestSink) Name() string { return "test" }
func (t *TestSink) Real() bool   { return false }
func (t *TestSink) Latency() time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lat
}
func (t *TestSink) Now() time.Time {
	if t.realtime {
		return time.Now()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.now
}

func (t *TestSink) Start(pull func([]float32)) error {
	t.mu.Lock()
	t.pull = pull
	t.mu.Unlock()
	if !t.realtime {
		close(t.done)
		return nil
	}
	go func() {
		defer close(t.done)
		start := time.Now()
		t.mu.Lock()
		t.started = start
		t.mu.Unlock()
		var sent int64
		tk := time.NewTicker(5 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-t.stop:
				return
			case <-tk.C:
			}
			due := int64(time.Since(start).Seconds()*DeviceRate)/chunkFrames + 1
			for sent/chunkFrames < due {
				t.chunk()
				sent += chunkFrames
			}
		}
	}()
	return nil
}

func (t *TestSink) chunk() {
	buf := make([]float32, chunkFrames*DeviceChannels)
	t.pull(buf)
	t.mu.Lock()
	t.rec = append(t.rec, buf...)
	t.pulls++
	t.mu.Unlock()
}

// Advance runs the device for d of virtual time (manual mode): the mixer is
// pulled in 10 ms chunks and the virtual clock advances with each. Streams
// that are playing consume audio as they would on a device.
func (t *TestSink) Advance(d time.Duration) {
	n := int(d / (10 * time.Millisecond))
	for i := 0; i < n; i++ {
		t.chunk() // a pull marks the start of the chunk's playout interval
		t.mu.Lock()
		t.now = t.now.Add(10 * time.Millisecond)
		t.mu.Unlock()
	}
}

// FrameTime is the wall time at which recorded device frame i was handed to
// the (virtual) speaker, in real-time mode: the sink's start plus i/DeviceRate.
func (t *TestSink) FrameTime(i int) time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.started.Add(time.Duration(i) * time.Second / DeviceRate)
}

// Frames is the number of device frames recorded so far.
func (t *TestSink) Frames() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.rec) / DeviceChannels
}

// Recorded returns a copy of the recorded interleaved stereo PCM.
func (t *TestSink) Recorded() []float32 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]float32(nil), t.rec...)
}

// Reset forgets the recording.
func (t *TestSink) Reset() {
	t.mu.Lock()
	t.rec = nil
	t.mu.Unlock()
}

// Peak returns the largest absolute sample in frames [from, to) of the
// recording (device frames).
func (t *TestSink) Peak(from, to int) float32 {
	t.mu.Lock()
	defer t.mu.Unlock()
	var pk float32
	for i := from * 2; i < to*2 && i < len(t.rec); i++ {
		if a := abs32(t.rec[i]); a > pk {
			pk = a
		}
	}
	return pk
}

// FirstSound returns the first recorded frame whose absolute value exceeds
// threshold, or -1.
func (t *TestSink) FirstSound(threshold float32) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := 0; i+1 < len(t.rec); i += 2 {
		if abs32(t.rec[i]) > threshold || abs32(t.rec[i+1]) > threshold {
			return i / 2
		}
	}
	return -1
}

func (t *TestSink) Close() error {
	select {
	case <-t.stop:
	default:
		close(t.stop)
	}
	<-t.done
	return nil
}
