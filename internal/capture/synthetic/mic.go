package synthetic

import (
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/sweefbrowser/sweef/internal/capture"
)

// Waveform selects what the synthetic microphone plays.
type Waveform int

const (
	Sine Waveform = iota
	Noise
	Silence
)

// Mic is the deterministic test microphone: a sine tone, white noise (a fixed-seed xorshift generator, so two runs
// produce the same samples) or silence, as mono 16-bit PCM in 10 ms chunks (one Opus frame at 48 kHz).
type Mic struct {
	info capture.DeviceInfo
	// Rate and Freq configure the tone; Amp is the peak as a fraction of full scale.
	Rate int
	Freq float64
	Amp  float64
	Wave Waveform
	// Unthrottled delivers chunks as fast as they are read instead of in real time.
	Unthrottled bool

	mu   sync.Mutex
	stop chan struct{}
	done chan struct{}
	used bool
}

// NewMic makes a 48 kHz, 440 Hz, -12 dBFS sine microphone.
func NewMic(info capture.DeviceInfo) *Mic {
	return &Mic{info: info, Rate: 48000, Freq: 440, Amp: 0.25, Wave: Sine}
}

func (m *Mic) Info() capture.DeviceInfo { return m.info }
func (m *Mic) Err() error               { return nil }

// ChunkSamples is the number of samples per channel in one chunk.
func (m *Mic) ChunkSamples() int { return m.Rate / 100 }

// Fill writes chunk number n (n >= 0) of the signal; it is a pure function of n and the configuration.
func (m *Mic) Fill(pcm []int16, n int) {
	base := n * len(pcm)
	switch m.Wave {
	case Sine:
		for i := range pcm {
			pcm[i] = int16(math.Round(m.Amp * 32767 * math.Sin(2*math.Pi*m.Freq*float64(base+i)/float64(m.Rate))))
		}
	case Noise:
		// per-sample hash of the sample index: random access, so chunk n never depends on earlier chunks
		for i := range pcm {
			x := uint64(base+i)*0x9E3779B97F4A7C15 + 0x1234567
			x ^= x >> 30
			x *= 0xBF58476D1CE4E5B9
			x ^= x >> 27
			x *= 0x94D049BB133111EB
			x ^= x >> 31
			pcm[i] = int16((float64(int64(x>>11))/float64(1<<53)*2 - 1) * m.Amp * 32767)
		}
	default:
		for i := range pcm {
			pcm[i] = 0
		}
	}
}

func (m *Mic) Start(capture.AudioConstraints) (<-chan *capture.AudioChunk, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.used {
		return nil, fmt.Errorf("synthetic: microphone already started")
	}
	m.used = true
	m.stop, m.done = make(chan struct{}), make(chan struct{})
	out := make(chan *capture.AudioChunk, 8)
	go m.run(out, m.stop, m.done)
	return out, nil
}

func (m *Mic) run(out chan *capture.AudioChunk, stop, done chan struct{}) {
	defer close(done)
	defer close(out)
	var tick *time.Ticker
	if !m.Unthrottled {
		tick = time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
	}
	n := m.ChunkSamples()
	for i := 0; ; i++ {
		if tick != nil {
			select {
			case <-tick.C:
			case <-stop:
				return
			}
		}
		c := &capture.AudioChunk{Rate: m.Rate, Channels: 1, PCM: make([]int16, n), PTS: time.Duration(i) * 10 * time.Millisecond}
		m.Fill(c.PCM, i)
		select {
		case out <- c:
		case <-stop:
			return
		default:
			if tick == nil { // unthrottled: wait for the reader
				select {
				case out <- c:
				case <-stop:
					return
				}
				continue
			}
			select { // real time: drop the oldest chunk
			case <-out:
			default:
			}
			select {
			case out <- c:
			default:
			}
		}
	}
}

func (m *Mic) Stop() error {
	m.mu.Lock()
	stop, done := m.stop, m.done
	m.stop = nil
	m.mu.Unlock()
	if stop != nil {
		close(stop)
	}
	if done != nil {
		<-done
	}
	return nil
}
