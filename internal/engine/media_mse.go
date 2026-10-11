package engine

import (
	"encoding/base64"
	"os"

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
