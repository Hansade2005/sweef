package engine

// tabaudio.go: the tab's audio state as the browser chrome sees it. Every
// stream a page opens (media elements, Web Audio) belongs to the tab's mix
// group, which carries the tab-level mute and the "tab is audible" indicator.

import (
	"sync"

	"github.com/sweefbrowser/sweef/internal/audioout"
)

// EvAudio is published when a tab starts or stops making sound and when its
// mute state changes. Data is AudioState.
const EvAudio EventKind = "Media.audio"

// AudioState is the payload of EvAudio and the result of Tab.AudioState.
type AudioState struct {
	Audible bool `json:"audible"` // the tab played non-silent audio in the last 1.5 s (true while muted too, like a browser tab)
	Muted   bool `json:"muted"`   // the tab is muted by the browser (not by the page)
}

type tabAudio struct {
	mu    sync.Mutex
	group *audioout.Group
	muted bool
	state AudioState
}

// audioGroup returns the tab's mix group, creating it on first use.
func (t *Tab) audioGroup() *audioout.Group {
	a := &t.audio
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.group == nil {
		g := audioout.Default().NewGroup(t.ID)
		g.SetMuted(a.muted)
		g.OnAudible(func(aud bool) { t.audioChanged(aud) })
		a.group = g
	}
	return a.group
}

func (t *Tab) audioChanged(audible bool) {
	a := &t.audio
	a.mu.Lock()
	if a.state.Audible == audible {
		a.mu.Unlock()
		return
	}
	a.state.Audible = audible
	st := a.state
	a.mu.Unlock()
	if !t.closed.Load() {
		t.emit(EvAudio, st)
		t.notifyUpdate()
	}
}

// SetAudioMuted mutes or unmutes everything the tab plays. The media clocks
// keep running, so a muted video stays in sync.
func (t *Tab) SetAudioMuted(m bool) {
	a := &t.audio
	a.mu.Lock()
	a.muted = m
	a.state.Muted = m
	g := a.group
	st := a.state
	a.mu.Unlock()
	if g != nil {
		g.SetMuted(m)
	}
	if !t.closed.Load() {
		t.emit(EvAudio, st)
	}
}

// AudioState reports whether the tab is audible and whether it is muted.
func (t *Tab) AudioState() AudioState {
	a := &t.audio
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.state
}

func (t *Tab) closeAudio() {
	a := &t.audio
	a.mu.Lock()
	g := a.group
	a.group = nil
	a.mu.Unlock()
	if g != nil {
		g.Close()
	}
}

// notifyUpdate asks the window to repaint (any goroutine).
func (t *Tab) notifyUpdate() {
	if t.closed.Load() {
		return
	}
	if f := t.onUpdate.Load(); f != nil {
		(*f)()
	}
}

// videoFrameReady marks a new video picture: the document's paint counter
// moves (layout stays cached) and the window is asked to repaint. relayout
// is set when the picture's size changed, which can change the element's
// intrinsic size and so the layout.
func (t *Tab) videoFrameReady(relayout bool) {
	t.mu.RLock()
	d := t.dom
	t.mu.RUnlock()
	if d != nil {
		if relayout {
			d.Touch()
		}
		d.TouchPaint()
	}
	t.notifyUpdate()
}
