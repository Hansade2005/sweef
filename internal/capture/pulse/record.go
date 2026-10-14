package pulse

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// recStream is a record stream: the server pushes PCM to it on its own channel.
type recStream struct {
	conn    *Conn
	channel uint32
	index   uint32 // source-output index (for DELETE)
	rate    uint32
	chans   int

	data chan []byte
	mu   sync.Mutex
	err  error
	done chan struct{}
	once sync.Once
}

// push is called by the read loop and must not block: when the consumer is slow the oldest block is dropped
// (a live microphone has no use for stale audio).
func (s *recStream) push(p []byte) {
	for {
		select {
		case s.data <- p:
			return
		default:
		}
		select {
		case <-s.data:
		default:
		}
	}
}

func (s *recStream) finish(err error) {
	s.once.Do(func() {
		s.mu.Lock()
		s.err = err
		s.mu.Unlock()
		close(s.done)
	})
}

func (s *recStream) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// openRecord creates a record stream of signed 16-bit little-endian PCM at the given rate and channel count.
// fragBytes is the requested fragment size (the latency): the server delivers about that much per packet.
func (c *Conn) openRecord(source string, rate uint32, channels int, fragBytes uint32) (*recStream, error) {
	var cm []byte
	switch channels {
	case 1:
		cm = []byte{posMono}
	case 2:
		cm = []byte{posFrontLeft, posFrontRight}
	default:
		return nil, fmt.Errorf("pulse: %d channels not supported", channels)
	}
	var w tagWriter
	w.sampleSpec(sampleS16LE, byte(channels), rate)
	w.channelMap(cm)
	w.u32(invalidIndex) // source index: select by name
	w.str(source)
	w.u32(invalidIndex) // max length: server default
	w.boolean(false)    // corked
	w.u32(fragBytes)
	// version 12 flags: no_remap, no_remix, fix_format, fix_rate, fix_channels, no_move, variable_rate
	for i := 0; i < 7; i++ {
		w.boolean(false)
	}
	// version 13: peak_detect, adjust_latency (so fragBytes is honoured), property list, direct_on_input
	w.boolean(false)
	w.boolean(true)
	w.proplist("media.name", "Sweef capture", "media.role", "phone")
	w.u32(invalidIndex)

	r, err := c.call(cmdCreateRecordStream, w.b, 5*time.Second)
	if err != nil {
		return nil, err
	}
	ch := r.u32()
	idx := r.u32()
	r.u32() // max length
	r.u32() // fragment size
	ss := r.sampleSpec()
	r.channelMap()
	r.u32() // source index
	r.str() // source name
	r.boolean()
	if r.err != nil {
		return nil, r.err
	}
	if ss.Format != sampleS16LE || ss.Rate == 0 {
		return nil, fmt.Errorf("pulse: server chose sample format %d at %d Hz", ss.Format, ss.Rate)
	}
	s := &recStream{conn: c, channel: ch, index: idx, rate: ss.Rate, chans: int(ss.Channels), data: make(chan []byte, 64), done: make(chan struct{})}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, errors.New("pulse: connection closed")
	}
	c.streams[ch] = s
	early := c.orphans[ch]
	delete(c.orphans, ch)
	c.mu.Unlock()
	for _, p := range early {
		s.push(p)
	}
	return s, nil
}

// close deletes the stream on the server and forgets it.
func (s *recStream) close() {
	var w tagWriter
	w.u32(s.channel)
	_, _ = s.conn.call(cmdDeleteRecordStream, w.b, time.Second)
	s.conn.mu.Lock()
	delete(s.conn.streams, s.channel)
	s.conn.mu.Unlock()
	s.finish(nil)
}
