// Package screen turns "something that can hand over its current pixels" into a capture.VideoSource: the whole
// browser window or one tab for getDisplayMedia. It knows nothing about the browser; the app supplies a Grabber.
//
// Capturing the operating system's screen or other applications' windows is out of scope: it needs OS APIs
// (X11 XShm/XDamage, Wayland portals, DXGI, ScreenCaptureKit) that this package deliberately does not use.
package screen

import (
	"errors"
	"fmt"
	"image"
	"sync"
	"time"

	"github.com/sweefbrowser/sweef/internal/capture"
)

// Grabber returns the current pixels and a generation number. When gen equals the previous call's the pixels are
// unchanged and may be nil; the source then repeats its last frame. The returned image may be reused by the next
// call: the source converts it before asking again. An error ends the source (the tab is gone).
type Grabber func(prev int) (img *image.RGBA, gen int, err error)

// Source is the VideoSource.
type Source struct {
	info capture.DeviceInfo
	grab Grabber
	// W, H is the native size of the surface.
	W, H int

	mu      sync.Mutex
	started bool
	stop    chan struct{}
	done    chan struct{}
	err     error
	w, h    int
	fps     float64
}

// New makes a source for a surface of native size w x h.
func New(info capture.DeviceInfo, w, h int, grab Grabber) *Source {
	return &Source{info: info, grab: grab, W: w, H: h}
}

func (s *Source) Info() capture.DeviceInfo { return s.info }

func (s *Source) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// Mode implements capture.Moder.
func (s *Source) Mode() (int, int, float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w, s.h, s.fps
}

// Start delivers frames of c.Width x c.Height (the surface is scaled down to fit; 0 = native size) at
// c.FrameRate (default 30, at most 60).
func (s *Source) Start(c capture.VideoConstraints) (<-chan *capture.Frame, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return nil, errors.New("screen: source already started")
	}
	s.started = true
	w, h := c.Width, c.Height
	if w <= 0 || h <= 0 || w > s.W || h > s.H {
		w, h = s.W, s.H
	}
	w, h = max(w&^1, 2), max(h&^1, 2)
	fps := c.FrameRate
	if fps <= 0 {
		fps = 30
	}
	fps = min(fps, 60)
	s.w, s.h, s.fps = w, h, fps
	s.stop, s.done = make(chan struct{}), make(chan struct{})
	out := make(chan *capture.Frame, 2)
	go s.run(out, w, h, fps, s.stop, s.done)
	return out, nil
}

func (s *Source) run(out chan *capture.Frame, w, h int, fps float64, stop, done chan struct{}) {
	defer close(done)
	defer close(out)
	tk := time.NewTicker(time.Duration(float64(time.Second) / fps))
	defer tk.Stop()
	start := time.Now()
	var last *capture.Frame
	gen := -1
	for {
		select {
		case <-stop:
			return
		case <-tk.C:
		}
		img, g, err := s.grab(gen)
		if err != nil {
			s.mu.Lock()
			s.err = fmt.Errorf("%w: %v", capture.ErrNotFound, err)
			s.mu.Unlock()
			return
		}
		if img != nil && (g != gen || last == nil) {
			gen = g
			b := img.Bounds()
			full := capture.NewFrame(b.Dx(), b.Dy())
			capture.FromRGBA(full, img)
			if b.Dx() == w && b.Dy() == h {
				last = full
			} else {
				last = capture.NewFrame(w, h)
				capture.Scale(last, full)
			}
		}
		if last == nil {
			continue
		}
		f := *last // the planes are shared and immutable once sent; only the timestamp differs
		f.PTS = time.Since(start)
		select {
		case out <- &f:
		default:
			select {
			case <-out:
			default:
			}
			select {
			case out <- &f:
			default:
			}
		}
	}
}

func (s *Source) Stop() error {
	s.mu.Lock()
	stop, done := s.stop, s.done
	s.stop = nil
	s.mu.Unlock()
	if stop != nil {
		close(stop)
	}
	if done != nil {
		<-done
	}
	return nil
}
