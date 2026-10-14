package webaudio

import (
	"fmt"
	"sync"
)

// Ring is a first-in first-out buffer of planar float32 audio between a producer that is not tied to the render
// clock (a microphone, a remote WebRTC track, an AudioData generator) and the render thread that consumes it a
// quantum at a time. When it is full the oldest audio is dropped (a live source never waits for its reader); when
// it runs dry the reader gets silence.
type Ring struct {
	mu   sync.Mutex
	ch   int
	capF int
	buf  [][]float32
	r, n int // read position and frames held
}

// NewRing creates a ring holding up to frames frames of channels channels.
func NewRing(frames, channels int) *Ring {
	if channels < 1 {
		channels = 1
	}
	r := &Ring{ch: channels, capF: frames, buf: make([][]float32, channels)}
	for i := range r.buf {
		r.buf[i] = make([]float32, frames)
	}
	return r
}

// Channels is the channel count of the audio in the ring.
func (r *Ring) Channels() int { return r.ch }

// Len is the number of frames waiting.
func (r *Ring) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n
}

// Push appends planar audio (one slice per channel, equal lengths). A source with fewer channels than the ring
// repeats its last channel; one with more drops the extra ones.
func (r *Ring) Push(p [][]float32) {
	if len(p) == 0 || len(p[0]) == 0 {
		return
	}
	frames := len(p[0])
	r.mu.Lock()
	defer r.mu.Unlock()
	if frames > r.capF {
		for c := range p {
			p[c] = p[c][frames-r.capF:]
		}
		frames = r.capF
	}
	if over := r.n + frames - r.capF; over > 0 { // drop the oldest
		r.r = (r.r + over) % r.capF
		r.n -= over
	}
	w := (r.r + r.n) % r.capF
	for c := 0; c < r.ch; c++ {
		src := p[min(c, len(p)-1)]
		first := min(frames, r.capF-w)
		copy(r.buf[c][w:w+first], src[:first])
		copy(r.buf[c][:frames-first], src[first:frames])
	}
	r.n += frames
}

// Pop fills dst (one slice per channel, each at least frames long) and returns how many frames were real audio;
// the rest of the block is zeroed.
func (r *Ring) Pop(dst [][]float32, frames int) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	got := min(frames, r.n)
	for c := range dst {
		if c >= r.ch {
			copy(dst[c][:got], dst[0][:got])
			clear(dst[c][got:frames])
			continue
		}
		first := min(got, r.capF-r.r)
		copy(dst[c][:first], r.buf[c][r.r:r.r+first])
		copy(dst[c][first:got], r.buf[c][:got-first])
		clear(dst[c][got:frames])
	}
	r.r = (r.r + got) % r.capF
	r.n -= got
	return got
}

type streamInProc struct{}

func (streamInProc) process(n *Node, c *Context, start int64, frames int, in [][][]float32) {
	r, _ := n.Props["ring"].(*Ring)
	if r == nil {
		out := n.setOut(0, 1)
		clear(out[0])
		return
	}
	out := n.setOut(0, r.Channels())
	r.Pop(out, frames)
}

// SetStreamIn gives a MediaStreamAudioSourceNode its audio: every quantum takes what the ring holds.
func (c *Context) SetStreamIn(id int, r *Ring) error {
	n := c.byID[id]
	if n == nil || n.Type != "mediastreamsource" {
		return fmt.Errorf("bad node")
	}
	n.Props["ring"] = r
	return nil
}

// SetTap installs a function called after every quantum with what a MediaStreamAudioDestinationNode received
// (the slices are only valid during the call).
func (c *Context) SetTap(id int, f func(ch [][]float32)) error {
	n := c.byID[id]
	if n == nil || n.Type != "mediastreamdest" {
		return fmt.Errorf("bad node")
	}
	n.Tap = f
	return nil
}

// Lock and Unlock serialise access to the graph between the page's host calls and a render goroutine.
func (c *Context) Lock()   { c.mu.Lock() }
func (c *Context) Unlock() { c.mu.Unlock() }
