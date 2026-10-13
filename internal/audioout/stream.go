package audioout

import (
	"math"
	"sync"
	"time"
)

// Stream is one audio source in the mixer. Write feeds it; the mixer drains
// it at the device rate. All methods are safe for concurrent use.
//
// The stream keeps a timeline measured in device frames. Every frame ever
// accepted by Write, minus the frames discarded by Flush, has an index on
// that timeline; Written is the index the next frame will get and Position is
// the index of the frame the listener hears right now. A frame with index i
// is audible once Position reaches i. A player that restarts at media time T
// after a Flush therefore anchors (T, Written) and reads
// T + (Position - anchor) as its clock.
type Stream struct {
	o         *Output
	g         *Group
	inCh      int
	inRate    float64
	capFrames int

	// write side
	wmu     sync.Mutex
	conv    *converter
	convGen int
	scratch []float32
	tmp     []float32
	speed   float64
	newSpd  float64 // pending SetSpeed, applied by the next Write block

	mu      sync.Mutex
	cond    *sync.Cond
	ring    []float32 // stereo frames
	rpos    int       // frame index of the oldest buffered frame
	count   int       // buffered frames
	playing bool
	closed  bool
	gen     int // bumped by Flush

	tlBase       int64 // frames consumed (or dropped by Flush) before the last pull
	lastPullReal int   // real frames supplied by the last pull
	lastPullAt   time.Time
	underruns    int64
	vol          float64
	muted        bool
	curGain      float32 // gain at the end of the previous pull (ramped)
	ended        bool    // producer will write no more: the stream finishes on drain
	endedCh      chan struct{}
}

// Write converts interleaved PCM (the stream's channel count and rate) to the
// device format and queues it, blocking while the stream's buffer (two
// seconds) is full. It returns the number of input frames accepted. A Flush
// that happens while Write is blocked abandons the rest and returns
// ErrFlushed.
func (s *Stream) Write(pcm []float32) (int, error) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return 0, ErrClosed
	}
	gen := s.gen
	s.mu.Unlock()
	if s.convGen != gen {
		s.conv.reset()
		s.convGen = gen
	}

	frames := len(pcm) / s.inCh
	// convert in blocks so a long write does not need an equally long buffer
	const block = 4096
	done := 0
	for done < frames {
		n := frames - done
		if n > block {
			n = block
		}
		s.mu.Lock()
		if s.newSpd > 0 {
			s.speed = s.newSpd
			s.conv.setRate(s.inRate * s.speed)
			s.newSpd = 0
		}
		s.mu.Unlock()
		s.tmp = s.conv.convert(pcm[done*s.inCh:(done+n)*s.inCh], s.tmp[:0], &s.scratch)
		if err := s.push(s.tmp, gen); err != nil {
			return done, err
		}
		done += n
	}
	return frames, nil
}

// WritePlanar is Write for one slice per channel (the layout decoders
// produce).
func (s *Stream) WritePlanar(ch [][]float32) (int, error) {
	if len(ch) == 0 {
		return 0, nil
	}
	if len(ch) != s.inCh {
		return 0, ErrChannels
	}
	n := len(ch[0])
	for _, c := range ch {
		if len(c) < n {
			n = len(c)
		}
	}
	buf := make([]float32, n*len(ch))
	for c, src := range ch {
		for i := 0; i < n; i++ {
			buf[i*len(ch)+c] = src[i]
		}
	}
	return s.Write(buf)
}

func (s *Stream) push(st []float32, gen int) error {
	frames := len(st) / DeviceChannels
	off := 0
	s.mu.Lock()
	defer s.mu.Unlock()
	for off < frames {
		for !s.closed && s.gen == gen && s.count >= s.capFrames {
			s.cond.Wait()
		}
		if s.closed {
			return ErrClosed
		}
		if s.gen != gen {
			return ErrFlushed
		}
		n := frames - off
		if free := s.capFrames - s.count; n > free {
			n = free
		}
		w := (s.rpos + s.count) % s.capFrames
		first := n
		if w+first > s.capFrames {
			first = s.capFrames - w
		}
		copy(s.ring[w*2:], st[(off)*2:(off+first)*2])
		if first < n {
			copy(s.ring[0:], st[(off+first)*2:(off+n)*2])
		}
		s.count += n
		off += n
	}
	return nil
}

// Play starts (or resumes) consumption by the mixer.
func (s *Stream) Play() {
	s.mu.Lock()
	s.playing = true
	s.mu.Unlock()
	s.o.wake()
}

// Pause stops consumption; buffered audio is kept.
func (s *Stream) Pause() {
	s.mu.Lock()
	s.playing = false
	s.mu.Unlock()
}

// Playing reports whether the stream is being consumed.
func (s *Stream) Playing() bool { return s.isPlaying() }

func (s *Stream) isPlaying() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.playing && !s.closed
}

// Flush discards buffered audio. The discarded frames leave the timeline, so
// Written afterwards equals the frames consumed so far and the next frame
// written is the next one the listener hears.
func (s *Stream) Flush() {
	s.mu.Lock()
	s.rpos, s.count = 0, 0
	s.gen++
	s.ended = false
	s.cond.Broadcast()
	s.mu.Unlock()
}

// SetSpeed sets the playback speed: input is consumed speed times faster than
// real time with the pitch changing accordingly (no time stretching). It
// applies to audio written after the call; callers that need it to take effect
// immediately Flush and rewrite from the current position.
func (s *Stream) SetSpeed(v float64) {
	if !(v > 0) {
		v = 1
	}
	s.mu.Lock()
	s.newSpd = v
	s.mu.Unlock()
}

// SetVolume sets the stream gain (0..1).
func (s *Stream) SetVolume(v float64) {
	s.mu.Lock()
	s.vol = clamp01(v)
	s.mu.Unlock()
}

// SetMuted mutes or unmutes the stream. A muted stream keeps its clock
// running.
func (s *Stream) SetMuted(m bool) {
	s.mu.Lock()
	s.muted = m
	s.mu.Unlock()
}

// End marks the end of the input: once the buffer drains the stream stops
// consuming and Done is closed.
func (s *Stream) End() {
	s.mu.Lock()
	s.ended = true
	if s.endedCh == nil {
		s.endedCh = make(chan struct{})
	}
	if s.count == 0 {
		select {
		case <-s.endedCh:
		default:
			close(s.endedCh)
		}
	}
	s.mu.Unlock()
}

// Done is closed after End once every buffered frame has been consumed.
func (s *Stream) Done() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.endedCh == nil {
		s.endedCh = make(chan struct{})
	}
	return s.endedCh
}

// Close removes the stream from the mixer and releases blocked writers.
func (s *Stream) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.playing = false
	s.cond.Broadcast()
	s.mu.Unlock()
	s.o.remove(s)
}

// Buffered is the duration of audio queued but not yet consumed by the
// device.
func (s *Stream) Buffered() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return framesToDur(int64(s.count))
}

// Written is the timeline index the next written frame will get, as a
// duration since the stream opened.
func (s *Stream) Written() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return framesToDur(s.tlBase + int64(s.lastPullReal) + int64(s.count))
}

// Position is the timeline index of the frame the listener hears now: frames
// consumed by the device, interpolated between device callbacks and reduced
// by the output latency. It stands still while the stream is paused or
// starved and can be negative shortly after start (the first frame is not yet
// audible).
func (s *Stream) Position() time.Duration {
	now := s.o.be.Now()
	s.mu.Lock()
	c := s.tlBase
	if s.lastPullReal > 0 {
		f := int64(now.Sub(s.lastPullAt).Seconds() * DeviceRate)
		if f < 0 {
			f = 0
		}
		if f > int64(s.lastPullReal) {
			f = int64(s.lastPullReal)
		}
		c += f
	}
	s.mu.Unlock()
	return framesToDur(c) - s.o.be.Latency()
}

// Latency is the delay between a frame leaving the mixer and being heard.
func (s *Stream) Latency() time.Duration { return s.o.be.Latency() }

// Underruns counts mixer pulls that found the stream empty (or short) while
// it was playing and not ended.
func (s *Stream) Underruns() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.underruns
}

// Group returns the group the stream was opened in (may be nil).
func (s *Stream) Group() *Group { return s.g }

func framesToDur(f int64) time.Duration {
	return time.Duration(f) * time.Second / DeviceRate
}

// mix adds up to frames frames of this stream into dst. It reports whether
// any real audio was mixed.
func (s *Stream) mix(dst []float32, frames int, now time.Time) bool {
	s.mu.Lock()
	if s.closed || !s.playing {
		s.mu.Unlock()
		return false
	}
	// the previous pull's frames are now fully accounted
	s.tlBase += int64(s.lastPullReal)
	n := frames
	if n > s.count {
		n = s.count
	}
	if n < frames && !s.ended {
		s.underruns++
	}
	s.lastPullReal = n
	s.lastPullAt = now

	vol := s.vol
	target := float32(vol)
	grp := float32(1)
	if s.g != nil {
		grp = s.g.gain()
		if s.g.muted.Load() {
			grp = 0
		}
	}
	if s.muted {
		grp = 0
	}
	target *= grp
	g0 := s.curGain
	s.curGain = target

	var pk float32
	if n > 0 {
		step := (target - g0) / float32(n)
		g := g0
		r := s.rpos
		for i := 0; i < n; i++ {
			l, rr := s.ring[r*2], s.ring[r*2+1]
			if a := abs32(l); a > pk {
				pk = a
			}
			if a := abs32(rr); a > pk {
				pk = a
			}
			g += step
			dst[2*i] += l * g
			dst[2*i+1] += rr * g
			r++
			if r == s.capFrames {
				r = 0
			}
		}
		s.rpos = r
		s.count -= n
		s.cond.Broadcast()
	}
	if s.ended && s.count == 0 && s.endedCh != nil {
		select {
		case <-s.endedCh:
		default:
			close(s.endedCh)
		}
	}
	s.mu.Unlock()
	if float64(pk)*vol > loudThreshold {
		if s.g != nil {
			s.g.lastLoud.Store(now.UnixNano())
		}
	}
	return n > 0 && pk > 0
}

func abs32(v float32) float32 {
	return float32(math.Abs(float64(v)))
}
