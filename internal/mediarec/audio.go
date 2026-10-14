package mediarec

import (
	"math"
	"sync"
	"time"

	"github.com/sweefbrowser/sweef/internal/capture"
	"github.com/sweefbrowser/sweef/internal/media"
)

// mixer turns the audio sources into Opus packets of 20 ms: each source is converted to 48 kHz and the channel
// count of the recording, queued in a FIFO, and the FIFOs are summed frame by frame. A source that falls behind
// the others by more than a few frames is padded with silence, so one stalled track cannot hold the rest.
type mixer struct {
	r   *Recorder
	nch int
	enc *media.WCOpusEncoder

	mu     sync.Mutex
	srcs   []*aSrc
	fresh  bool  // the next sample starts a new stretch of the timeline
	t0     int64 // timeline position (48 kHz samples) of the next frame to encode
	lastEn int64 // end of the previous stretch
	real   int64 // samples of real audio written (for the end trim in Ogg)
	buf    []float32
}

type aSrc struct {
	in    *AudioIn
	rs    *Resampler
	rsKey [2]int
	fifo  []float32
	ended bool
}

func newMixer(r *Recorder, in []*AudioIn) (*mixer, error) {
	nch := 1
	for _, a := range in {
		if a.Channels >= 2 {
			nch = 2
		}
	}
	bps := r.cfg.AudioBPS
	if bps <= 0 {
		bps = 128000
	}
	enc, err := media.NewWCOpusEncoder(opusRate, nch, bps, 20000, "audio")
	if err != nil {
		return nil, err
	}
	enc.SetCBR(r.cfg.CBR)
	m := &mixer{r: r, nch: nch, enc: enc, fresh: true, buf: make([]float32, opusFrame*nch)}
	for _, a := range in {
		m.srcs = append(m.srcs, &aSrc{in: a})
	}
	return m, nil
}

func (m *mixer) start() {
	for i, s := range m.srcs {
		m.r.wg.Add(1)
		go m.read(i, s)
	}
}

func (m *mixer) read(i int, s *aSrc) {
	defer m.r.wg.Done()
	for {
		select {
		case c, ok := <-s.in.C:
			if !ok {
				m.end(i)
				return
			}
			if m.r.paused.Load() || c == nil || len(c.PCM) == 0 {
				continue
			}
			m.push(s, c)
		case <-m.r.quit:
			return
		}
	}
}

// convert returns the chunk as interleaved float32 at 48 kHz with the recording's channel count.
func (m *mixer) convert(s *aSrc, c *capture.AudioChunk) []float32 {
	ch := c.Channels
	if ch < 1 {
		ch = 1
	}
	n := len(c.PCM) / ch
	// first make it mono or stereo, as float
	src := ch
	if src > 2 {
		src = 2
	}
	f := make([]float32, n*src)
	for i := 0; i < n; i++ {
		if ch <= 2 {
			for k := 0; k < src; k++ {
				f[i*src+k] = float32(c.PCM[i*ch+k]) / 32768
			}
			continue
		}
		// more than two channels: front left/right
		f[i*2] = float32(c.PCM[i*ch]) / 32768
		f[i*2+1] = float32(c.PCM[i*ch+1]) / 32768
	}
	rate := c.Rate
	if rate <= 0 {
		rate = s.in.Rate
	}
	if rate <= 0 {
		rate = opusRate
	}
	if s.rs == nil || s.rsKey != [2]int{rate, src} {
		s.rs, s.rsKey = NewResampler(rate, opusRate, src), [2]int{rate, src}
	}
	out := s.rs.Process(f)
	switch {
	case src == m.nch:
		return out
	case src == 1 && m.nch == 2:
		st := make([]float32, len(out)*2)
		for i, v := range out {
			st[2*i], st[2*i+1] = v, v
		}
		return st
	default: // stereo into a mono recording
		mo := make([]float32, len(out)/2)
		for i := range mo {
			mo[i] = (out[2*i] + out[2*i+1]) / 2
		}
		return mo
	}
}

func (m *mixer) push(s *aSrc, c *capture.AudioChunk) {
	pcm := m.convert(s, c)
	if len(pcm) == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if s.ended {
		return
	}
	if m.fresh {
		m.fresh = false
		now := int64(m.r.clk.Now()) * opusRate / int64(time.Second)
		m.t0 = now - int64(len(pcm)/m.nch)
		if m.t0 < m.lastEn {
			m.t0 = m.lastEn
		}
		for _, o := range m.srcs {
			o.fifo = o.fifo[:0]
		}
	}
	s.fifo = append(s.fifo, pcm...)
	m.drain(false)
}

// drain encodes every frame that is ready. final also encodes a last, partial frame (zero padded).
func (m *mixer) drain(final bool) {
	fs := opusFrame * m.nch
	for {
		minLen, maxLen, active := math.MaxInt, 0, 0
		for _, s := range m.srcs {
			if s.ended && len(s.fifo) == 0 {
				continue
			}
			active++
			if len(s.fifo) < minLen {
				minLen = len(s.fifo)
			}
			if len(s.fifo) > maxLen {
				maxLen = len(s.fifo)
			}
		}
		if active == 0 || maxLen == 0 {
			return
		}
		ready := minLen >= fs || maxLen >= fs+lagFrames*m.nch
		real := opusFrame
		if !ready {
			if !final {
				return
			}
			real = maxLen / m.nch
		}
		for i := range m.buf {
			m.buf[i] = 0
		}
		for _, s := range m.srcs {
			n := min(len(s.fifo), fs)
			for i := 0; i < n; i++ {
				m.buf[i] += s.fifo[i]
			}
			s.fifo = append(s.fifo[:0], s.fifo[n:]...)
		}
		if active > 1 {
			for i, v := range m.buf {
				if v > 1 {
					m.buf[i] = 1
				} else if v < -1 {
					m.buf[i] = -1
				}
			}
		}
		pkt, err := m.enc.Encode(m.buf)
		if err != nil {
			return
		}
		ts := m.t0
		m.t0 += opusFrame
		m.real += int64(real)
		m.r.marks[trkAudio].Store(m.t0 * 1000000 / opusRate)
		m.r.put(trkAudio, ts*1000000/opusRate, true, pkt, opusFrame)
	}
}

// tick pads clocked sources that the wall clock has left far behind.
func (m *mixer) tick(now time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fresh {
		return
	}
	want := int64(now)*opusRate/int64(time.Second) - m.t0 // samples that should exist by now
	changed := false
	for _, s := range m.srcs {
		if s.ended || s.in.OwnClock {
			continue
		}
		have := int64(len(s.fifo) / m.nch)
		if need := want - have - 2400; need > stallPadding {
			s.fifo = append(s.fifo, make([]float32, int(need)*m.nch)...)
			changed = true
		}
	}
	if changed {
		m.drain(false)
	}
}

func (m *mixer) end(i int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.srcs[i]
	if s.ended {
		return
	}
	s.ended = true
	if s.in.Cancel != nil {
		s.in.Cancel()
	}
	m.drain(false)
	for _, o := range m.srcs {
		if !o.ended {
			return
		}
	}
	m.drain(true)
	m.r.marks[trkAudio].Store(math.MaxInt64)
}

func (m *mixer) cancelAll() {
	for _, s := range m.srcs {
		if s.in.Cancel != nil {
			s.in.Cancel()
		}
	}
}

// final writes the partial frame left over at the end.
func (m *mixer) final() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.srcs {
		s.ended = true
	}
	m.drain(true)
	m.r.marks[trkAudio].Store(math.MaxInt64)
}

// pause pads out the frame being filled so nothing is lost, and starts a new stretch on resume.
func (m *mixer) pause() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fresh {
		return
	}
	saved := make([]bool, len(m.srcs))
	for i, s := range m.srcs {
		saved[i], s.ended = s.ended, true
	}
	m.drain(true)
	for i, s := range m.srcs {
		s.ended = saved[i]
		s.fifo = s.fifo[:0]
	}
	m.lastEn = m.t0
	m.fresh = true
}

func (m *mixer) resume() {}
