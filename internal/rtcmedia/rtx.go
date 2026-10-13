package rtcmedia

import (
	"sync"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
)

// RTXCounter is an interceptor, placed just above the network, that sees every retransmission the NACK
// responder sends (packets on the RTX stream). It counts them per media SSRC for the statistics and holds
// them to a bit rate budget: without one a lossy path makes retransmissions of retransmissions that fill
// the link they are meant to repair.
type RTXCounter struct {
	mu      sync.Mutex
	streams map[uint32]*rtxStream
}

type rtxStream struct {
	packets, bytes, dropped uint64
	tokens                  float64 // bytes that may be sent now
	rate                    float64 // bytes per second added to the bucket
	last                    time.Time
}

// NewRTXCounter returns an empty counter; register it with the interceptor registry.
func NewRTXCounter() *RTXCounter { return &RTXCounter{streams: map[uint32]*rtxStream{}} }

// NewInterceptor implements interceptor.Factory.
func (c *RTXCounter) NewInterceptor(string) (interceptor.Interceptor, error) {
	return &rtxInterceptor{c: c}, nil
}

type rtxInterceptor struct {
	interceptor.NoOp
	c *RTXCounter
}

func (r *rtxInterceptor) BindLocalStream(info *interceptor.StreamInfo, w interceptor.RTPWriter) interceptor.RTPWriter {
	if info.SSRCRetransmission == 0 {
		return w
	}
	st := &rtxStream{rate: 100_000, tokens: 20_000, last: time.Now()} // 800 kbit/s until the sender says otherwise
	r.c.mu.Lock()
	r.c.streams[info.SSRC] = st
	r.c.mu.Unlock()
	rtx := info.SSRCRetransmission
	return interceptor.RTPWriterFunc(func(h *rtp.Header, p []byte, a interceptor.Attributes) (int, error) {
		if h.SSRC != rtx {
			return w.Write(h, p, a)
		}
		r.c.mu.Lock()
		now := time.Now()
		st.tokens += now.Sub(st.last).Seconds() * st.rate
		st.last = now
		if max := st.rate/2 + 8000; st.tokens > max {
			st.tokens = max
		}
		size := float64(len(p) + 12)
		if st.tokens < size {
			st.dropped++
			r.c.mu.Unlock()
			return len(p), nil // over budget: the receiver will ask again or ask for a key frame
		}
		st.tokens -= size
		st.packets++
		st.bytes += uint64(len(p))
		r.c.mu.Unlock()
		return w.Write(h, p, a)
	})
}

// SetBudget limits the retransmission rate of a media SSRC to bps bits per second.
func (c *RTXCounter) SetBudget(ssrc uint32, bps int) {
	c.mu.Lock()
	if st := c.streams[ssrc]; st != nil {
		st.rate = float64(bps) / 8
	}
	c.mu.Unlock()
}

// Get returns the retransmitted packets and payload bytes sent for a media SSRC and the number held back.
func (c *RTXCounter) Get(ssrc uint32) (packets, bytes, dropped uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if st := c.streams[ssrc]; st != nil {
		return st.packets, st.bytes, st.dropped
	}
	return 0, 0, 0
}
