package engine

import (
	"encoding/base64"
	"sync"
	"time"

	"github.com/sweefbrowser/sweef/internal/capture"
)

// waTap carries what a MediaStreamAudioDestinationNode renders to the MediaRecorders that record its track. The
// graph renders on the JS thread (waRender), so push never blocks: a recorder that cannot keep up loses chunks.
type waTap struct {
	rate int
	mu   sync.Mutex
	subs map[chan *capture.AudioChunk]struct{}
	acc  []int16
}

func (t *waTap) nch() int { return 2 }

func (t *waTap) push(ch [][]float32) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.subs) == 0 || len(ch) == 0 {
		return
	}
	l, r := ch[0], ch[0]
	if len(ch) > 1 {
		r = ch[1]
	}
	for i := range l {
		t.acc = append(t.acc, pcm16(l[i]), pcm16(r[i]))
	}
	if len(t.acc) < 2*(t.rate/100) {
		return
	}
	pcm := t.acc
	t.acc = nil
	c := &capture.AudioChunk{Rate: t.rate, Channels: 2, PCM: pcm}
	for s := range t.subs {
		select {
		case s <- c:
		default:
		}
	}
}

func pcm16(v float32) int16 {
	switch {
	case v >= 1:
		return 32767
	case v <= -1:
		return -32768
	}
	return int16(v * 32767)
}

func (t *waTap) subscribe() (<-chan *capture.AudioChunk, func()) {
	ch := make(chan *capture.AudioChunk, 64)
	t.mu.Lock()
	if t.subs == nil {
		t.subs = map[chan *capture.AudioChunk]struct{}{}
	}
	t.subs[ch] = struct{}{}
	t.mu.Unlock()
	return ch, func() {
		t.mu.Lock()
		if _, ok := t.subs[ch]; ok {
			delete(t.subs, ch)
			close(ch)
		}
		t.mu.Unlock()
	}
}

func (t *waTap) close() {
	t.mu.Lock()
	for s := range t.subs {
		close(s)
	}
	t.subs = nil
	t.mu.Unlock()
}

func (h *recHost) addTap(track string, t *waTap) {
	h.mu.Lock()
	if !h.closed {
		h.taps[track] = t
	}
	h.mu.Unlock()
}

func b64s(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func durMs(ms int) time.Duration { return time.Duration(ms) * time.Millisecond }
