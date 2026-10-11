package engine

import (
	"encoding/base64"
	"math"
	"os"
	"sync"
	"sync/atomic"

	"github.com/sweefbrowser/sweef/internal/media"
)

// mseHost keeps one parser per SourceBuffer.
type mseHost struct {
	bufs map[int]*media.MSEBuffer
	next int
}

func (m *mediaHost) mseFns(f map[string]hostFn) {
	ms := &mseHost{bufs: map[int]*media.MSEBuffer{}, next: 1}
	f["mseNew"] = func(a []any) (any, error) {
		id := ms.next
		ms.next++
		ms.bufs[id] = media.NewMSEBuffer(argS(a, 0))
		return id, nil
	}
	// frames for MSE video: the appended bytes form a playable stream
	type mseVid struct {
		vs       *videoSource
		inflight atomic.Bool
		want     atomic.Uint64
		mu       sync.Mutex
	}
	vids := map[int]*mseVid{}
	f["mseBind"] = func(a []any) (any, error) { // node, bufId
		node := argI(a, 0)
		vs := m.vsFor(node)
		vs.mu.Lock()
		vs.frame = nil
		vs.w, vs.h = int(argI(a, 2)), int(argI(a, 3))
		vs.mu.Unlock()
		vids[int(argI(a, 1))] = &mseVid{vs: vs}
		m.publish(node, vs)
		return nil, nil
	}
	f["mseFrame"] = func(a []any) (any, error) { // bufId, t
		id := int(argI(a, 0))
		v, b := vids[id], ms.bufs[id]
		if v == nil || b == nil {
			return false, nil
		}
		v.want.Store(math.Float64bits(argF(a, 1)))
		if v.inflight.CompareAndSwap(false, true) {
			go func() {
				defer v.inflight.Store(false)
				for {
					t := math.Float64frombits(v.want.Load())
					data := b.Snapshot()
					if info, err := media.ProbeBytes(data); err == nil {
						if fs, err := media.OpenFrames(media.BytesSource(data), info, ""); err == nil {
							if img, err := fs.Frame(t); err == nil && img != nil {
								v.vs.mu.Lock()
								v.vs.frame = img
								if bd := img.Bounds(); v.vs.w == 0 {
									v.vs.w, v.vs.h = bd.Dx(), bd.Dy()
								}
								v.vs.seq++
								v.vs.mu.Unlock()
							}
							fs.Close()
						}
					}
					if math.Float64bits(t) == v.want.Load() {
						return
					}
				}
			}()
		}
		return true, nil
	}
	f["mseClose"] = func(a []any) (any, error) { delete(ms.bufs, int(argI(a, 0))); return nil, nil }
	f["mseAppend"] = func(a []any) (any, error) {
		b := ms.bufs[int(argI(a, 0))]
		if b == nil {
			return map[string]any{"ok": false, "error": "removed"}, nil
		}
		data, err := base64.StdEncoding.DecodeString(argS(a, 1))
		if err != nil {
			return map[string]any{"ok": false, "error": "bad data"}, nil
		}
		r, err := b.Append(data)
		if err != nil {
			return map[string]any{"ok": false, "error": err.Error()}, nil
		}
		out := map[string]any{"ok": true}
		if r.Init != nil {
			tracks := make([]any, 0, len(r.Init.Tracks))
			for _, t := range r.Init.Tracks {
				tracks = append(tracks, map[string]any{"id": t.ID, "kind": t.Kind, "codec": t.Codec, "codecName": t.CodecName, "width": t.Width, "height": t.Height, "frameRate": t.FrameRate, "sampleRate": t.SampleRate, "channels": t.Channels, "language": t.Language, "name": t.Name})
			}
			out["init"] = map[string]any{"duration": r.Init.Duration, "tracks": tracks}
		}
		spans := make([]any, 0, len(r.Spans))
		for _, s := range r.Spans {
			spans = append(spans, []any{s.Track, s.Start, s.End})
		}
		out["spans"] = spans
		return out, nil
	}
	f["mediaReset"] = func(a []any) (any, error) {
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
		ms.bufs = map[int]*media.MSEBuffer{}
		m.waReset()
		return nil, nil
	}
	f["mediaFakeDevices"] = func(a []any) (any, error) { return os.Getenv("SWEEF_FAKE_MEDIA") != "", nil }
}
