package engine

// media_mse.go: host functions behind MediaSource/SourceBuffer
// (media_prelude.js) and the per-element player (mediaplayer.go).

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"

	"github.com/sweefbrowser/sweef/internal/media"
)

// mseHost owns the Go side of every SourceBuffer of one VM.
type mseHost struct {
	mu   sync.Mutex
	bufs map[int]*media.MSEBuffer
	next int
}

func newMSEHost() *mseHost { return &mseHost{bufs: map[int]*media.MSEBuffer{}, next: 1} }

func (h *mseHost) get(id int) *media.MSEBuffer {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.bufs[id]
}

// mseTracks is the trackSource of a MediaSource: the stores of its
// SourceBuffers, looked up on every call.
type mseTracks struct {
	h   *mseHost
	mu  sync.Mutex
	ids []int
	eos atomic.Bool
}

func (t *mseTracks) find(kind string) *media.TrackStore {
	t.mu.Lock()
	ids := append([]int(nil), t.ids...)
	t.mu.Unlock()
	for _, id := range ids {
		if b := t.h.get(id); b != nil {
			if st := b.FirstStore(kind); st != nil {
				return st
			}
		}
	}
	return nil
}

func (t *mseTracks) Video() *media.TrackStore { return t.find("video") }
func (t *mseTracks) Audio() *media.TrackStore { return t.find("audio") }
func (t *mseTracks) EOS() bool                { return t.eos.Load() }

func rangesAny(r [][2]float64) []any {
	out := make([]any, len(r))
	for i, x := range r {
		out[i] = []any{x[0], x[1]}
	}
	return out
}

func trackAny(t media.Track) map[string]any {
	return map[string]any{"id": t.ID, "kind": t.Kind, "codec": t.Codec, "codecName": t.CodecName, "width": t.Width, "height": t.Height,
		"frameRate": t.FrameRate, "sampleRate": t.SampleRate, "channels": t.Channels, "language": t.Language, "name": t.Name}
}

func (m *mediaHost) mseFns(f map[string]hostFn) {
	ms := m.mse
	f["mseNew"] = func(a []any) (any, error) {
		ms.mu.Lock()
		defer ms.mu.Unlock()
		id := ms.next
		ms.next++
		b := media.NewMSEBuffer(argS(a, 0))
		if q := os.Getenv("SWEEF_MSE_QUOTA"); q != "" { // diagnostics: shrink the buffer limit
			var n int
			if _, err := fmt.Sscan(q, &n); err == nil && n > 0 {
				b.SetQuota(n)
			}
		}
		ms.bufs[id] = b
		return id, nil
	}
	f["mseClose"] = func(a []any) (any, error) {
		ms.mu.Lock()
		delete(ms.bufs, int(argI(a, 0)))
		ms.mu.Unlock()
		return nil, nil
	}
	// mseAppend(id, base64, currentTime)
	f["mseAppend"] = func(a []any) (any, error) {
		b := ms.get(int(argI(a, 0)))
		if b == nil {
			return map[string]any{"ok": false, "error": "removed"}, nil
		}
		data, err := base64.StdEncoding.DecodeString(argS(a, 1))
		if err != nil {
			return map[string]any{"ok": false, "error": "decode", "message": "bad data"}, nil
		}
		r, err := b.Append(data, argF(a, 2))
		if err != nil {
			code := "decode"
			if errors.Is(err, media.ErrQuotaExceeded) {
				code = "quota"
			}
			return map[string]any{"ok": false, "error": code, "message": err.Error(), "buffered": rangesAny(b.Buffered())}, nil
		}
		out := map[string]any{"ok": true, "frames": r.Frames, "dropped": r.Dropped, "evicted": r.Evicted, "buffered": rangesAny(r.Buffered)}
		if r.Init != nil {
			tracks := make([]any, 0, len(r.Init.Tracks))
			for _, t := range r.Init.Tracks {
				tracks = append(tracks, trackAny(t))
			}
			out["init"] = map[string]any{"duration": r.Init.Duration, "tracks": tracks, "first": r.Init.First}
		}
		return out, nil
	}
	f["mseRemove"] = func(a []any) (any, error) {
		b := ms.get(int(argI(a, 0)))
		if b == nil {
			return []any{}, nil
		}
		b.Remove(argF(a, 1), argF(a, 2))
		return rangesAny(b.Buffered()), nil
	}
	f["mseTruncate"] = func(a []any) (any, error) {
		b := ms.get(int(argI(a, 0)))
		if b == nil {
			return []any{}, nil
		}
		b.Truncate(argF(a, 1))
		return rangesAny(b.Buffered()), nil
	}
	f["mseAbort"] = func(a []any) (any, error) {
		if b := ms.get(int(argI(a, 0))); b != nil {
			b.Abort()
		}
		return nil, nil
	}
	f["mseBuffered"] = func(a []any) (any, error) {
		b := ms.get(int(argI(a, 0)))
		if b == nil {
			return []any{}, nil
		}
		return rangesAny(b.Buffered()), nil
	}
	// mseConfig(id, key, v1, v2): mode (v1 "segments"|"sequence"), tso, window, type, quota
	f["mseConfig"] = func(a []any) (any, error) {
		b := ms.get(int(argI(a, 0)))
		if b == nil {
			return nil, nil
		}
		switch argS(a, 1) {
		case "mode":
			b.SetMode(argS(a, 2) == "sequence")
		case "tso":
			b.SetTimestampOffset(argF(a, 2))
		case "window":
			b.SetAppendWindow(argF(a, 2), argF(a, 3))
		case "type":
			b.ChangeType(argS(a, 2))
		case "quota":
			b.SetQuota(int(argI(a, 2)))
		}
		return b.TimestampOffset(), nil
	}
	f["mseBytes"] = func(a []any) (any, error) {
		b := ms.get(int(argI(a, 0)))
		if b == nil {
			return 0, nil
		}
		return b.Bytes(), nil
	}
	f["mediaFakeDevices"] = func(a []any) (any, error) { return os.Getenv("SWEEF_FAKE_MEDIA") != "", nil }
	f["mediaReset"] = func(a []any) (any, error) {
		m.shutdown()
		m.mu.Lock()
		for _, r := range m.res {
			r.fsMu.Lock()
			if r.fs != nil {
				r.fs.Close()
			}
			r.fsMu.Unlock()
		}
		m.res = map[int]*mediaRes{}
		m.vsrcs = map[int64]*videoSource{}
		m.pcm = map[int]*media.PCM{}
		m.mu.Unlock()
		ms.mu.Lock()
		ms.bufs = map[int]*media.MSEBuffer{}
		ms.mu.Unlock()
		m.waReset()
		return nil, nil
	}
	m.playerFns(f)
}

// ---- player host functions ----------------------------------------------------------------------

func (m *mediaHost) playerFor(node int64) *mediaPlayer {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.players[node]
}

func (m *mediaHost) setPlayer(node int64, p *mediaPlayer) {
	m.mu.Lock()
	old := m.players[node]
	if p == nil {
		delete(m.players, node)
	} else {
		m.players[node] = p
	}
	m.mu.Unlock()
	if old != nil {
		old.Close()
	}
}

func (m *mediaHost) playerFns(f map[string]hostFn) {
	// playerOpen(node, h, isVideo): a player over the tracks of progressive resource h
	f["playerOpen"] = func(a []any) (any, error) {
		node, h := argI(a, 0), int(argI(a, 1))
		r := m.get(h)
		if r == nil || m.j.tab == nil {
			return map[string]any{"ok": false}, nil
		}
		video, audio, err := media.OpenTracks(r.src, r.info)
		if err != nil {
			return map[string]any{"ok": false, "error": err.Error()}, nil
		}
		if video != nil && !media.HasNativeVideo(video.Config().CodecName) {
			video = nil
		}
		if audio != nil {
			if d, err := media.NewAudioDecoder(audio.Config()); err != nil {
				audio = nil
			} else {
				_ = d
			}
		}
		if video == nil && audio == nil {
			return map[string]any{"ok": false, "error": "no native tracks"}, nil
		}
		vs := m.vsFor(node)
		if video != nil && argB(a, 2) {
			c := video.Config()
			vs.mu.Lock()
			vs.frame, vs.hasPic = nil, false
			vs.w, vs.h = c.Width, c.Height
			vs.mu.Unlock()
			m.publish(node, vs)
		}
		m.setPlayer(node, newMediaPlayer(m.j.tab, node, fixedTracks{video, audio}, vs))
		return map[string]any{"ok": true, "video": video != nil, "audio": audio != nil}, nil
	}
	// playerMSE(node, idsJSON, isVideo, w, h): a player over the SourceBuffers ids of a MediaSource
	f["playerMSE"] = func(a []any) (any, error) {
		node := argI(a, 0)
		var ids []int
		_ = json.Unmarshal([]byte(argS(a, 1)), &ids)
		if p := m.playerFor(node); p != nil {
			if t, ok := p.src.(*mseTracks); ok {
				t.mu.Lock()
				t.ids = ids
				t.mu.Unlock()
				p.hasAudio.Store(t.Audio() != nil)
				return map[string]any{"ok": true}, nil
			}
		}
		if m.j.tab == nil {
			return map[string]any{"ok": false}, nil
		}
		vs := m.vsFor(node)
		if argB(a, 2) {
			vs.mu.Lock()
			vs.frame, vs.hasPic = nil, false
			vs.w, vs.h = int(argI(a, 3)), int(argI(a, 4))
			vs.mu.Unlock()
			m.publish(node, vs)
		}
		m.setPlayer(node, newMediaPlayer(m.j.tab, node, &mseTracks{h: m.mse, ids: ids}, vs))
		return map[string]any{"ok": true}, nil
	}
	f["playerEOS"] = func(a []any) (any, error) {
		if p := m.playerFor(argI(a, 0)); p != nil {
			if t, ok := p.src.(*mseTracks); ok {
				t.eos.Store(argB(a, 1))
			}
		}
		return nil, nil
	}
	f["playerPlay"] = func(a []any) (any, error) {
		if p := m.playerFor(argI(a, 0)); p != nil {
			p.Play(argF(a, 1), argF(a, 2))
		}
		return nil, nil
	}
	f["playerPause"] = func(a []any) (any, error) {
		if p := m.playerFor(argI(a, 0)); p != nil {
			return p.Pause(), nil
		}
		return nil, nil
	}
	f["playerSeek"] = func(a []any) (any, error) {
		if p := m.playerFor(argI(a, 0)); p != nil {
			p.Seek(argF(a, 1))
		}
		return nil, nil
	}
	f["playerRate"] = func(a []any) (any, error) {
		if p := m.playerFor(argI(a, 0)); p != nil {
			p.SetRate(argF(a, 1))
		}
		return nil, nil
	}
	f["playerVolume"] = func(a []any) (any, error) {
		if p := m.playerFor(argI(a, 0)); p != nil {
			p.SetVolume(argF(a, 1), argB(a, 2))
		}
		return nil, nil
	}
	f["playerHold"] = func(a []any) (any, error) {
		if p := m.playerFor(argI(a, 0)); p != nil {
			p.SetHold(argB(a, 1))
		}
		return nil, nil
	}
	f["playerRoute"] = func(a []any) (any, error) {
		if p := m.playerFor(argI(a, 0)); p != nil {
			p.SetRouted(argB(a, 1))
		}
		return nil, nil
	}
	f["playerClock"] = func(a []any) (any, error) {
		p := m.playerFor(argI(a, 0))
		if p == nil {
			return nil, nil
		}
		t, playing, audio := p.State()
		return map[string]any{"t": t, "playing": playing, "audio": audio}, nil
	}
	f["playerStats"] = func(a []any) (any, error) {
		p := m.playerFor(argI(a, 0))
		if p == nil {
			return map[string]any{"total": 0, "dropped": 0, "corrupted": 0}, nil
		}
		s := p.Stats()
		return map[string]any{"total": s.Total, "dropped": s.Dropped, "corrupted": s.Corrupted}, nil
	}
	f["playerClose"] = func(a []any) (any, error) {
		m.setPlayer(argI(a, 0), nil)
		return nil, nil
	}
	f["audioState"] = func(a []any) (any, error) {
		if m.j.tab == nil {
			return map[string]any{"audible": false, "muted": false}, nil
		}
		s := m.j.tab.AudioState()
		return map[string]any{"audible": s.Audible, "muted": s.Muted}, nil
	}
}
