package engine

// tabmediasession.go: navigator.mediaSession as the embedding app sees it. The page's metadata, playback state,
// position and the actions it handles are published as EvMediaSession (the chrome's "now playing" control); the
// control's buttons come back through Tab.MediaSessionAction, which runs the page's action handler.

import (
	"encoding/json"
	"strconv"
	"sync"
)

// EvMediaSession is published when the page changes its Media Session. Data is MediaSessionInfo.
const EvMediaSession EventKind = "Media.session"

// MediaArtwork is one image of the session's metadata.
type MediaArtwork struct {
	Src   string `json:"src"`
	Sizes string `json:"sizes,omitempty"`
	Type  string `json:"type,omitempty"`
}

// MediaSessionInfo is the payload of EvMediaSession and the result of Tab.MediaSession.
type MediaSessionInfo struct {
	Active        bool           `json:"active"` // the page set metadata, a playback state or an action handler
	Title         string         `json:"title,omitempty"`
	Artist        string         `json:"artist,omitempty"`
	Album         string         `json:"album,omitempty"`
	Artwork       []MediaArtwork `json:"artwork,omitempty"`
	PlaybackState string         `json:"playbackState"` // none | paused | playing
	Actions       []string       `json:"actions,omitempty"`
	Duration      float64        `json:"duration,omitempty"`
	Position      float64        `json:"position,omitempty"`
	Rate          float64        `json:"rate,omitempty"`
}

type tabMediaSession struct {
	mu   sync.Mutex
	info MediaSessionInfo
}

func (t *Tab) publishMediaSession(raw string) {
	var in struct {
		PlaybackState string   `json:"playbackState"`
		Actions       []string `json:"actions"`
		Position      *struct {
			Duration     float64 `json:"duration"`
			Position     float64 `json:"position"`
			PlaybackRate float64 `json:"playbackRate"`
		} `json:"position"`
		Metadata *struct {
			Title   string         `json:"title"`
			Artist  string         `json:"artist"`
			Album   string         `json:"album"`
			Artwork []MediaArtwork `json:"artwork"`
		} `json:"metadata"`
	}
	if json.Unmarshal([]byte(raw), &in) != nil {
		return
	}
	info := MediaSessionInfo{PlaybackState: in.PlaybackState, Actions: in.Actions}
	if info.PlaybackState != "playing" && info.PlaybackState != "paused" {
		info.PlaybackState = "none"
	}
	if m := in.Metadata; m != nil {
		info.Title, info.Artist, info.Album, info.Artwork = m.Title, m.Artist, m.Album, m.Artwork
	}
	if p := in.Position; p != nil {
		info.Duration, info.Position, info.Rate = p.Duration, p.Position, p.PlaybackRate
	}
	info.Active = in.Metadata != nil || info.PlaybackState != "none" || len(info.Actions) > 0
	t.ms.mu.Lock()
	t.ms.info = info
	t.ms.mu.Unlock()
	if !t.closed.Load() {
		t.emit(EvMediaSession, info)
		t.notifyUpdate()
	}
}

// MediaSession returns what the page last published.
func (t *Tab) MediaSession() MediaSessionInfo {
	t.ms.mu.Lock()
	defer t.ms.mu.Unlock()
	return t.ms.info
}

// MediaSessionAction presses a button of the browser's media controls: the page's handler for the action runs
// (play, pause and seeking fall back to the page's media element). arg is the seek offset or the seekto position.
func (t *Tab) MediaSessionAction(action string, arg float64) bool {
	switch action {
	case "play", "pause", "previoustrack", "nexttrack", "seekbackward", "seekforward", "seekto", "stop", "skipad", "hangup", "togglemicrophone", "togglecamera":
	default:
		return false
	}
	a, _ := json.Marshal(action)
	r, err := t.Evaluate("__swMediaAction(" + string(a) + "," + strconv.FormatFloat(arg, 'f', -1, 64) + ")")
	return err == nil && string(r) == "true"
}

func (t *Tab) resetMediaSession() {
	t.ms.mu.Lock()
	had := t.ms.info.Active
	t.ms.info = MediaSessionInfo{PlaybackState: "none"}
	info := t.ms.info
	t.ms.mu.Unlock()
	if had && !t.closed.Load() {
		t.emit(EvMediaSession, info)
	}
}

func init() {
	registerHostExtension("mediasession", func(j *jsVM) map[string]hostFn {
		return map[string]hostFn{"mediaSession": func(a []any) (any, error) {
			if j.tab != nil && len(a) > 0 {
				if s, ok := a[0].(string); ok {
					j.tab.publishMediaSession(s)
				}
			}
			return nil, nil
		}}
	})
}
