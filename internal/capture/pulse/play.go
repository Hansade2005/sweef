package pulse

import (
	"errors"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"
)

// PlayStream is a playback stream to the server's default sink. The server asks for data (REQUEST) as its buffer
// drains; Wait blocks until it has asked, Write sends what was asked for.
type PlayStream struct {
	conn    *Conn
	channel uint32
	index   uint32 // sink-input index (volume)
	rate    uint32
	chans   int
	frame   int // bytes per frame

	mu        sync.Mutex
	cond      *sync.Cond
	requested int
	err       error
	done      chan struct{}
	once      sync.Once

	underflows atomic.Int64
}

func (c *Conn) playStream(ch uint32) *PlayStream {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.play[ch]
}

func (s *PlayStream) addRequest(n int) {
	s.mu.Lock()
	s.requested += n
	s.cond.Broadcast()
	s.mu.Unlock()
}

func (s *PlayStream) finish(err error) {
	s.once.Do(func() {
		if err == nil {
			err = errors.New("pulse: stream closed")
		}
		s.mu.Lock()
		s.err = err
		s.cond.Broadcast()
		s.mu.Unlock()
		close(s.done)
	})
}

// Err is why the stream ended (nil while it is alive).
func (s *PlayStream) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// Underflows counts the times the server ran out of data.
func (s *PlayStream) Underflows() int64 { return s.underflows.Load() }

// Frame is the size in bytes of one sample frame.
func (s *PlayStream) Frame() int { return s.frame }

// OpenPlayback creates a playback stream of 32-bit float little-endian PCM (the server converts it to what the
// device wants). latency is the buffer the server keeps (about the delay between Write and sound).
func (c *Conn) OpenPlayback(name string, rate uint32, channels int, latency time.Duration) (*PlayStream, error) {
	var cm []byte
	switch channels {
	case 1:
		cm = []byte{posMono}
	case 2:
		cm = []byte{posFrontLeft, posFrontRight}
	default:
		return nil, fmt.Errorf("pulse: %d channels not supported", channels)
	}
	frame := 4 * channels
	tlen := uint32(int(latency.Seconds()*float64(rate)) * frame)
	minreq := uint32(int(0.01*float64(rate)) * frame)
	var w tagWriter
	w.sampleSpec(sampleFloat32LE, byte(channels), rate)
	w.channelMap(cm)
	w.u32(invalidIndex) // sink index: the default sink
	w.nullStr()         // sink name
	w.u32(invalidIndex) // max length: server default
	w.boolean(false)    // corked
	w.u32(tlen)
	w.u32(0) // prebuf: start at once
	w.u32(minreq)
	w.u32(0) // sync id
	w.cvolume(channels, 0x10000)
	// version 12: no_remap, no_remix, fix_format, fix_rate, fix_channels, no_move, variable_rate
	for i := 0; i < 7; i++ {
		w.boolean(false)
	}
	// version 13: muted, adjust_latency (so tlength is honoured), property list
	w.boolean(false)
	w.boolean(true)
	w.proplist("media.name", name, "media.role", "video")

	r, err := c.call(cmdCreatePlaybackStream, w.b, 5*time.Second)
	if err != nil {
		return nil, err
	}
	ch := r.u32()
	idx := r.u32()
	req := r.u32()
	r.u32() // maxlength
	r.u32() // tlength
	r.u32() // prebuf
	r.u32() // minreq
	ss := r.sampleSpec()
	r.channelMap()
	r.u32() // sink index
	r.str() // sink name
	r.boolean()
	r.usec() // sink latency
	if r.err != nil {
		return nil, r.err
	}
	if ss.Format != sampleFloat32LE || int(ss.Channels) != channels || ss.Rate != rate {
		return nil, fmt.Errorf("pulse: server chose format %d, %d channels, %d Hz", ss.Format, ss.Channels, ss.Rate)
	}
	s := &PlayStream{conn: c, channel: ch, index: idx, rate: rate, chans: channels, frame: frame, requested: int(req), done: make(chan struct{})}
	s.cond = sync.NewCond(&s.mu)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, errors.New("pulse: connection closed")
	}
	c.play[ch] = s
	c.mu.Unlock()
	return s, nil
}

// Wait blocks until the server has asked for data, the stream ended or timeout passed. It returns the number of
// bytes the server wants (0 on timeout).
func (s *PlayStream) Wait(timeout time.Duration) (int, error) {
	deadline := time.Now().Add(timeout)
	timer := time.AfterFunc(timeout, func() { s.mu.Lock(); s.cond.Broadcast(); s.mu.Unlock() })
	defer timer.Stop()
	s.mu.Lock()
	defer s.mu.Unlock()
	for s.requested <= 0 && s.err == nil && time.Now().Before(deadline) {
		s.cond.Wait()
	}
	if s.err != nil {
		return 0, s.err
	}
	if s.requested < 0 {
		return 0, nil
	}
	return s.requested, nil
}

// Write sends whole frames of PCM (p is cut to a whole number of frames); the server's outstanding request shrinks
// by what was sent.
func (s *PlayStream) Write(p []byte) error {
	p = p[:len(p)/s.frame*s.frame]
	if len(p) == 0 {
		return nil
	}
	if err := s.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	s.requested -= len(p)
	s.mu.Unlock()
	return s.conn.send(s.channel, p)
}

// Timing is a snapshot of where the stream is.
type Timing struct {
	// Latency is the time from a byte being written to it being heard: what the server still holds of this stream
	// plus the device's own latency.
	Latency time.Duration
	Playing bool
	Written int64 // bytes the server received
	Read    int64 // bytes the device consumed
}

// Timing asks the server (GET_PLAYBACK_LATENCY).
func (s *PlayStream) Timing() (Timing, error) {
	var w tagWriter
	w.u32(s.channel)
	now := time.Now()
	w.timeval(uint32(now.Unix()), uint32(now.Nanosecond()/1000))
	r, err := s.conn.call(cmdGetPlaybackLatency, w.b, 2*time.Second)
	if err != nil {
		return Timing{}, err
	}
	sink := r.usec()
	r.usec() // source latency
	playing := r.boolean()
	r.timeval() // local
	r.timeval() // remote
	wr := r.s64()
	rd := r.s64()
	if r.err != nil {
		return Timing{}, r.err
	}
	held := wr - rd
	if held < 0 {
		held = 0
	}
	lat := time.Duration(sink)*time.Microsecond + time.Duration(float64(held)/float64(s.frame)/float64(s.rate)*float64(time.Second))
	return Timing{Latency: lat, Playing: playing, Written: wr, Read: rd}, nil
}

// Cork pauses (true) or resumes (false) playback; a corked stream receives no requests.
func (s *PlayStream) Cork(on bool) error {
	var w tagWriter
	w.u32(s.channel)
	w.boolean(on)
	_, err := s.conn.call(cmdCorkPlaybackStream, w.b, 2*time.Second)
	return err
}

// Flush drops what the server has buffered.
func (s *PlayStream) Flush() error {
	var w tagWriter
	w.u32(s.channel)
	_, err := s.conn.call(cmdFlushPlaybackStream, w.b, 2*time.Second)
	return err
}

// Drain returns when everything written has been played.
func (s *PlayStream) Drain(timeout time.Duration) error {
	var w tagWriter
	w.u32(s.channel)
	_, err := s.conn.call(cmdDrainPlaybackStream, w.b, timeout)
	return err
}

// SetVolume sets the stream's volume as a linear gain (0 to 1; the server's scale is cubic).
func (s *PlayStream) SetVolume(gain float64) error {
	if gain < 0 {
		gain = 0
	}
	if gain > 1 {
		gain = 1
	}
	var w tagWriter
	w.u32(s.index)
	w.cvolume(s.chans, uint32(math.Cbrt(gain)*0x10000+0.5))
	_, err := s.conn.call(cmdSetSinkInputVolume, w.b, 2*time.Second)
	return err
}

// Close deletes the stream on the server.
func (s *PlayStream) Close() {
	var w tagWriter
	w.u32(s.channel)
	_, _ = s.conn.call(cmdDeletePlaybackStream, w.b, time.Second)
	s.conn.mu.Lock()
	delete(s.conn.play, s.channel)
	s.conn.mu.Unlock()
	s.finish(nil)
}
