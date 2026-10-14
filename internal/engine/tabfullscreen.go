package engine

import "sync/atomic"

// tabfullscreen.go: element fullscreen (the Fullscreen API) as the embedding app sees it. The page's JavaScript
// (media_prelude.js) decides whether a request is allowed (transient user activation) and marks the element; the
// app answers the EvFullscreen event by giving the page the whole window.

// EvFullscreen is published when the page enters or leaves element fullscreen. Data is FullscreenState.
const EvFullscreen EventKind = "Page.fullscreen"

// FullscreenState is the payload of EvFullscreen and the result of Tab.Fullscreen.
type FullscreenState struct {
	Active bool `json:"active"`
}

type tabFullscreen struct{ on atomic.Bool }

func (t *Tab) setFullscreen(on bool) {
	if t.fs.on.Swap(on) == on || t.closed.Load() {
		return
	}
	t.emit(EvFullscreen, FullscreenState{Active: on})
	t.notifyUpdate()
}

// Fullscreen reports whether an element of the page is fullscreen.
func (t *Tab) Fullscreen() FullscreenState { return FullscreenState{Active: t.fs.on.Load()} }

// ExitFullscreen leaves element fullscreen as the user did (Esc, F11, a tab switch): the page sees
// fullscreenchange. It is a no-op when nothing is fullscreen.
func (t *Tab) ExitFullscreen() {
	if !t.fs.on.Load() {
		return
	}
	t.fs.on.Store(false)
	_, _ = t.Evaluate("__swFsExit()")
}
