package engine

// capture_preview.go: showing a live camera or screen track in a <video> element (video.srcObject = stream).
// The element's bitmap source is replaced by one that follows the track; frames are converted to RGBA only when
// the painter asks for them, so a page with a hidden preview costs a channel read per frame.

import (
	"image"
	"sync"
	"time"

	"github.com/sweefbrowser/sweef/internal/capture"
)

type liveVideoSource struct {
	mu     sync.Mutex
	frame  *capture.Frame
	img    *image.RGBA
	imgFor *capture.Frame
}

func (s *liveVideoSource) set(f *capture.Frame) {
	s.mu.Lock()
	s.frame = f
	s.mu.Unlock()
}

func (s *liveVideoSource) Bitmap() image.Image {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.frame == nil {
		return nil
	}
	if s.imgFor != s.frame {
		s.img, s.imgFor = s.frame.ToRGBA(), s.frame
	}
	return s.img
}

func (s *liveVideoSource) Size() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.frame == nil {
		return 0, 0
	}
	return s.frame.W, s.frame.H
}

// previewRepaintEvery bounds how often a preview asks the window to paint.
const previewRepaintEvery = 66 * time.Millisecond

// hostBindPreview(node, trackId) points the <video> node at a live track.
func (h *capHost) hostBindPreview(a []any) (any, error) {
	node, id := argI(a, 0), argS(a, 1)
	ct := h.track(id)
	t := h.tab()
	if ct == nil || t == nil {
		return false, nil
	}
	h.unbind(node)
	frames, cancel := ct.t.SubscribeVideo()
	src := &liveVideoSource{}
	t.SetBitmapSource(node, src)
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		cancel()
		return false, nil
	}
	if h.previews == nil {
		h.previews = map[int64]func(){}
	}
	h.previews[node] = cancel
	h.mu.Unlock()
	go func() {
		var last time.Time
		for f := range frames {
			src.set(f)
			if time.Since(last) >= previewRepaintEvery {
				last = time.Now()
				t.requestRepaint()
			}
		}
	}()
	return true, nil
}

func (h *capHost) hostUnbindPreview(a []any) (any, error) {
	h.unbind(argI(a, 0))
	return nil, nil
}

func (h *capHost) unbind(node int64) {
	h.mu.Lock()
	c := h.previews[node]
	delete(h.previews, node)
	h.mu.Unlock()
	if c != nil {
		c()
	}
}

// requestRepaint marks the document changed and asks the window to paint (the same path late images take).
func (t *Tab) requestRepaint() {
	if t.closed.Load() {
		return
	}
	t.mu.RLock()
	d := t.dom
	t.mu.RUnlock()
	if d == nil {
		return
	}
	d.Touch()
	if f := t.onUpdate.Load(); f != nil {
		(*f)()
	}
}
