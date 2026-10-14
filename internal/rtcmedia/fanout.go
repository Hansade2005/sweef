package rtcmedia

import (
	"sync"

	"github.com/sweefbrowser/sweef/internal/capture"
)

// FanOut shares one capture source between several consumers (the layers of a simulcast sender). A
// capture.VideoSource can only be started once, so each consumer gets a Branch: a VideoSource of its own that
// receives every frame of the root. The root starts with the first branch and stops when the last one stops. A
// slow consumer never delays the others: a branch whose channel is full drops its oldest frame. Frames are
// shared, read-only.
type FanOut struct {
	root capture.VideoSource

	mu       sync.Mutex
	branches map[*Branch]struct{}
	started  bool
	stopped  bool
	err      error
}

// NewFanOut wraps src (not yet started).
func NewFanOut(src capture.VideoSource) *FanOut {
	return &FanOut{root: src, branches: map[*Branch]struct{}{}}
}

// Branch is one consumer's view of a FanOut.
type Branch struct {
	f    *FanOut
	ch   chan *capture.Frame
	done bool
}

// Branch returns a new consumer. Start it like any capture.VideoSource.
func (f *FanOut) Branch() *Branch { return &Branch{f: f} }

func (b *Branch) Info() capture.DeviceInfo { return b.f.root.Info() }

func (b *Branch) Start(c capture.VideoConstraints) (<-chan *capture.Frame, error) {
	f := b.f
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stopped {
		return nil, capture.ErrNotFound
	}
	b.ch = make(chan *capture.Frame, 4)
	if !f.started {
		frames, err := f.root.Start(c)
		if err != nil {
			return nil, err
		}
		f.started = true
		go f.pump(frames)
	}
	f.branches[b] = struct{}{}
	return b.ch, nil
}

func (f *FanOut) pump(frames <-chan *capture.Frame) {
	for fr := range frames {
		f.mu.Lock()
		for b := range f.branches {
			select {
			case b.ch <- fr:
			default:
				select { // full: drop the oldest frame of this branch
				case <-b.ch:
				default:
				}
				select {
				case b.ch <- fr:
				default:
				}
			}
		}
		f.mu.Unlock()
	}
	f.mu.Lock()
	f.err = f.root.Err()
	f.stopped = true
	for b := range f.branches {
		if !b.done {
			b.done = true
			close(b.ch)
		}
	}
	f.branches = map[*Branch]struct{}{}
	f.mu.Unlock()
}

func (b *Branch) Stop() error {
	f := b.f
	f.mu.Lock()
	if _, ok := f.branches[b]; ok {
		delete(f.branches, b)
	}
	if !b.done && b.ch != nil {
		b.done = true
		close(b.ch)
	}
	last := f.started && !f.stopped && len(f.branches) == 0
	if last {
		f.stopped = true
	}
	f.mu.Unlock()
	if last {
		return f.root.Stop()
	}
	return nil
}

func (b *Branch) Err() error {
	b.f.mu.Lock()
	defer b.f.mu.Unlock()
	return b.f.err
}
