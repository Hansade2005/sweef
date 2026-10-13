//go:build linux || freebsd || netbsd || openbsd || dragonfly || solaris

package audioout

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sweefbrowser/sweef/internal/capture/pulse"
)

// The Linux (and BSD) device backend speaks the PulseAudio native protocol over the server's unix socket, in pure
// Go: no libpulse, no ALSA library, no dlopen (the Sweef binary stays statically linked). PipeWire's pulse server
// speaks the same protocol. A machine with only bare ALSA (no sound server) gets the null sink.

// pulseLatency is the buffer asked from the server (the delay between a pull and the sound, before the device's
// own latency, which the timing queries add).
const pulseLatency = 60 * time.Millisecond

// playStream is the part of pulse.PlayStream the backend uses (tests substitute a fake).
type playStream interface {
	Wait(timeout time.Duration) (int, error)
	Write(p []byte) error
	Timing() (pulse.Timing, error)
	Cork(on bool) error
	Err() error
	Close()
}

type pulseBackend struct {
	conn *pulse.Conn
	st   playStream

	latency atomic.Int64 // nanoseconds, refreshed from the server's timing
	started atomic.Bool
	stop    chan struct{}
	done    chan struct{}
	once    sync.Once
	corked  atomic.Bool
	failed  atomic.Bool
}

var (
	pulseMu     sync.Mutex
	pulseFailed error
)

// newDeviceBackend connects to the sound server and creates the playback stream. A failure is remembered, so a
// machine without a server pays for the attempt once.
func newDeviceBackend() (Backend, error) {
	pulseMu.Lock()
	defer pulseMu.Unlock()
	if pulseFailed != nil {
		return nil, pulseFailed
	}
	c, err := pulse.Dial(pulse.Options{AppName: "Sweef", DialTimeout: 500 * time.Millisecond})
	if err != nil {
		pulseFailed = fmt.Errorf("no PulseAudio/PipeWire server: %w", err)
		return nil, pulseFailed
	}
	st, err := c.OpenPlayback("Sweef", DeviceRate, DeviceChannels, pulseLatency)
	if err != nil {
		c.Close()
		pulseFailed = fmt.Errorf("audio playback stream: %w", err)
		return nil, pulseFailed
	}
	b := newPulseBackend(st)
	b.conn = c
	return b, nil
}

func newPulseBackend(st playStream) *pulseBackend {
	b := &pulseBackend{st: st, stop: make(chan struct{}), done: make(chan struct{})}
	b.latency.Store(int64(pulseLatency + 20*time.Millisecond))
	return b
}

func (b *pulseBackend) Name() string   { return "pulse" }
func (b *pulseBackend) Real() bool     { return !b.failed.Load() }
func (b *pulseBackend) Now() time.Time { return time.Now() }
func (b *pulseBackend) Latency() time.Duration {
	return time.Duration(b.latency.Load())
}

func (b *pulseBackend) Start(pull func([]float32)) error {
	if !b.started.CompareAndSwap(false, true) {
		return errors.New("audioout: pulse backend already started")
	}
	go b.run(pull)
	return nil
}

// Suspend and Resume cork the stream while nothing is playing so the desktop stops listing the browser as an
// audio client.
func (b *pulseBackend) Suspend() {
	if b.corked.CompareAndSwap(false, true) {
		_ = b.st.Cork(true)
	}
}

func (b *pulseBackend) Resume() {
	if b.corked.CompareAndSwap(true, false) {
		_ = b.st.Cork(false)
	}
}

// run answers the server's requests with mixed audio. Each pull is at most 20 ms so the audio clock stays fine
// grained. If the server goes away the loop keeps the clock running in real time and discards the audio (the null
// sink's behaviour), so media keeps its timing.
func (b *pulseBackend) run(pull func([]float32)) {
	defer close(b.done)
	const frameBytes = DeviceChannels * 4
	maxFrames := chunkFrames * 2
	buf := make([]float32, maxFrames*DeviceChannels)
	raw := make([]byte, len(buf)*4)
	lastTiming := time.Now()
	for {
		select {
		case <-b.stop:
			return
		default:
		}
		n, err := b.st.Wait(100 * time.Millisecond)
		if err != nil {
			b.failed.Store(true)
			b.freeRun(pull)
			return
		}
		if n > 0 && n < frameBytes {
			time.Sleep(5 * time.Millisecond) // a partial frame is outstanding: wait for the server to ask for more
			continue
		}
		for n >= frameBytes {
			frames := min(n/frameBytes, maxFrames)
			dst := buf[:frames*DeviceChannels]
			pull(dst)
			for i, v := range dst {
				binary.LittleEndian.PutUint32(raw[i*4:], math.Float32bits(v))
			}
			if err := b.st.Write(raw[:frames*frameBytes]); err != nil {
				b.failed.Store(true)
				b.freeRun(pull)
				return
			}
			n -= frames * frameBytes
		}
		if time.Since(lastTiming) > 250*time.Millisecond && !b.corked.Load() {
			lastTiming = time.Now()
			if tm, err := b.st.Timing(); err == nil && tm.Latency > 0 && tm.Latency < 2*time.Second {
				b.latency.Store(int64(tm.Latency))
			}
		}
	}
}

// freeRun pulls in real time and drops the audio: the stream to the server is gone.
func (b *pulseBackend) freeRun(pull func([]float32)) {
	b.latency.Store(0)
	chunk := time.Duration(chunkFrames) * time.Second / DeviceRate
	dst := make([]float32, chunkFrames*DeviceChannels)
	start := time.Now()
	for i := int64(1); ; i++ {
		select {
		case <-b.stop:
			return
		case <-time.After(time.Until(start.Add(time.Duration(i) * chunk))):
		}
		pull(dst)
	}
}

func (b *pulseBackend) Close() error {
	b.once.Do(func() {
		close(b.stop)
		if b.started.Load() {
			<-b.done
		}
		b.st.Close()
		if b.conn != nil {
			b.conn.Close()
		}
	})
	return nil
}
