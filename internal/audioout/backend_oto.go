//go:build windows || darwin

package audioout

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/ebitengine/oto/v3"
)

// Oto allows exactly one context per process and cannot recreate it, so the
// device is opened once and kept; a failure is remembered.
var (
	otoMu     sync.Mutex
	otoState  *otoBackend
	otoFailed error
)

// otoBuffer is the device buffer asked from the driver and the read-ahead of
// the oto player; together they are the output latency estimate.
const (
	otoDeviceBuffer = 40 * time.Millisecond
	otoPlayerBuffer = 40 * time.Millisecond
)

type otoBackend struct {
	ctx    *oto.Context
	player *oto.Player
	rd     *otoReader
	once   sync.Once
}

// otoReader adapts the mixer's pull callback to the io.Reader oto reads.
type otoReader struct {
	mu   sync.Mutex
	pull func([]float32)
	buf  []float32
}

func (r *otoReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pull == nil {
		for i := range p {
			p[i] = 0
		}
		return len(p), nil
	}
	const frameBytes = DeviceChannels * 4
	frames := len(p) / frameBytes
	if frames > chunkFrames*2 {
		frames = chunkFrames * 2 // at most 20 ms per pull: keeps the clock fine-grained
	}
	if frames == 0 {
		return 0, nil
	}
	if cap(r.buf) < frames*DeviceChannels {
		r.buf = make([]float32, frames*DeviceChannels)
	}
	b := r.buf[:frames*DeviceChannels]
	r.pull(b)
	for i, v := range b {
		binary.LittleEndian.PutUint32(p[i*4:], math.Float32bits(v))
	}
	return frames * frameBytes, nil
}

// newDeviceBackend opens the platform audio device through oto: PulseAudio
// (native protocol, pure Go) or ALSA (libasound loaded at run time) on Linux,
// WASAPI on Windows, AudioToolbox on macOS. No cgo is involved.
func newDeviceBackend() (Backend, error) {
	otoMu.Lock()
	defer otoMu.Unlock()
	if otoState != nil {
		return otoState, nil
	}
	if otoFailed != nil {
		return nil, otoFailed
	}
	ctx, ready, err := oto.NewContext(&oto.NewContextOptions{
		SampleRate:      DeviceRate,
		ChannelCount:    DeviceChannels,
		Format:          oto.FormatFloat32LE,
		BufferSize:      otoDeviceBuffer,
		ApplicationName: "Sweef",
	})
	if err != nil {
		otoFailed = err
		return nil, err
	}
	select {
	case <-ready:
	case <-time.After(4 * time.Second):
		otoFailed = errors.New("audio device did not become ready")
		return nil, otoFailed
	}
	if err := ctx.Err(); err != nil {
		otoFailed = fmt.Errorf("no audio device: %w", err)
		return nil, otoFailed
	}
	otoState = &otoBackend{ctx: ctx, rd: &otoReader{}}
	return otoState, nil
}

func (b *otoBackend) Name() string   { return "oto" }
func (b *otoBackend) Real() bool     { return true }
func (b *otoBackend) Now() time.Time { return time.Now() }
func (b *otoBackend) Latency() time.Duration {
	return otoDeviceBuffer + otoPlayerBuffer
}

func (b *otoBackend) Start(pull func([]float32)) error {
	b.rd.mu.Lock()
	b.rd.pull = pull
	b.rd.mu.Unlock()
	b.player = b.ctx.NewPlayer(b.rd)
	b.player.SetBufferSize(int(otoPlayerBuffer.Seconds() * DeviceRate * DeviceChannels * 4))
	b.player.Play()
	return nil
}

// Suspend and Resume pause the player while nothing is playing so the
// desktop stops listing the browser as an audio client.
func (b *otoBackend) Suspend() { b.player.Pause() }
func (b *otoBackend) Resume()  { b.player.Play() }

// Close detaches the mixer. The oto context itself lives for the process.
func (b *otoBackend) Close() error {
	b.once.Do(func() {
		if b.player != nil {
			b.player.Pause()
		}
		b.rd.mu.Lock()
		b.rd.pull = nil
		b.rd.mu.Unlock()
	})
	return nil
}
