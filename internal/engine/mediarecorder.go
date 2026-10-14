package engine

// mediarecorder.go: MediaRecorder, Go side. The page-facing API (state machine, events, Blob/BlobEvent) is
// platformjs/recorder.js; the encoders and containers are internal/mediarec; docs/mediarecorder.md has the overview.
//
// A recording is a mediarec.Recorder with its own goroutines, so nothing is encoded on the JS thread. Chunks and the
// final stop travel back as events on the VM's event queue ("rec"), in the order the recorder produced them, and
// the bytes of a chunk are taken by the page through the WebCodecs blob store.
//
// Which tracks can be recorded is decided here, from what the page already holds: getUserMedia / getDisplayMedia
// tracks of this page (the same capture.Track the page owns, so a track that was never granted, or whose permission
// was revoked, has nothing to record), canvas.captureStream() tracks of this page's canvases, and the track of a
// MediaStreamAudioDestinationNode of this page's AudioContext.

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/sweefbrowser/sweef/internal/capture"
	"github.com/sweefbrowser/sweef/internal/mediarec"
)

type recHost struct {
	j      *jsVM
	mu     sync.Mutex
	next   int64
	recs   map[int64]*recSession
	taps   map[string]*waTap
	closed bool
}

type recSession struct {
	id    int64
	r     *mediarec.Recorder
	audio map[string]int // track id -> audio source index
	video string
}

func (j *jsVM) recState() *recHost {
	j.clMu.Lock()
	defer j.clMu.Unlock()
	if j.rech == nil {
		h := &recHost{j: j, recs: map[int64]*recSession{}, taps: map[string]*waTap{}}
		j.rech = h
		j.onClose(h.shutdown)
	}
	return j.rech
}

func (h *recHost) shutdown() {
	h.mu.Lock()
	h.closed = true
	recs := h.recs
	h.recs = map[int64]*recSession{}
	taps := h.taps
	h.taps = map[string]*waTap{}
	h.mu.Unlock()
	for _, s := range recs {
		s.r.Stop()
	}
	for _, t := range taps {
		t.close()
	}
}

func (h *recHost) push(m map[string]any) {
	h.mu.Lock()
	closed := h.closed
	h.mu.Unlock()
	if closed {
		return
	}
	h.j.rt().push(asyncEvent{kind: "rec", data: jsonStr(m)})
	if h.j.tab != nil {
		h.j.tab.wake()
	}
}

type recTrackDesc struct {
	ID     string  `json:"id"`
	Kind   string  `json:"kind"`
	Canvas int64   `json:"canvas"`
	W      int     `json:"w"`
	H      int     `json:"h"`
	FPS    float64 `json:"fps"`
}

type recOpts struct {
	Mime   string         `json:"mime"`
	ABps   int            `json:"abps"`
	VBps   int            `json:"vbps"`
	CBR    bool           `json:"cbr"`
	Slice  int            `json:"slice"`
	Tracks []recTrackDesc `json:"tracks"`
}

// start resolves the tracks, builds the recorder and starts it. The result is {id, mime}; a problem the page must
// report as an error event comes back as {err, name}.
func (h *recHost) start(o *recOpts) map[string]any {
	fail := func(name, f string, a ...any) map[string]any {
		return map[string]any{"err": fmt.Sprintf(f, a...), "name": name}
	}
	spec, ok := mediarec.ParseMime(o.Mime)
	if !ok {
		return fail("NotSupportedError", "Unsupported MIME type '%s'", o.Mime)
	}
	wantAudio := o.Mime == "" || spec.Audio
	wantVideo := (o.Mime == "" || spec.Video) && spec.Container != "ogg"
	var ain []*mediarec.AudioIn
	var aids []string
	var vin *mediarec.VideoIn
	var vid string
	var cancels []func()
	for _, d := range o.Tracks {
		switch {
		case d.Kind == "audio" && wantAudio:
			if a, cancel := h.audioSource(d); a != nil {
				ain = append(ain, a)
				aids = append(aids, d.ID)
				cancels = append(cancels, cancel)
			}
		case d.Kind == "video" && wantVideo && vin == nil:
			if v, cancel := h.videoSource(d); v != nil {
				vin, vid = v, d.ID
				cancels = append(cancels, cancel)
			}
		}
	}
	release := func() {
		for _, c := range cancels {
			c()
		}
	}
	if len(ain) == 0 && vin == nil {
		return fail("NotSupportedError", "There is nothing to record: no live track of this page can be recorded as %s", orDefault(o.Mime, "the default type"))
	}
	mime := o.Mime
	if mime == "" {
		mime = mediarec.DefaultMime(len(ain) > 0, vin != nil)
		spec, _ = mediarec.ParseMime(mime)
	}
	if spec.Container == "ogg" && len(ain) == 0 {
		release()
		return fail("NotSupportedError", "Ogg records audio, and the stream has no audio track that can be recorded")
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		release()
		return fail("InvalidStateError", "the document is closing")
	}
	h.next++
	id := h.next
	h.mu.Unlock()
	cfg := mediarec.Config{Container: spec.Container, AudioBPS: o.ABps, VideoBPS: o.VBps, CBR: o.CBR}
	if o.Slice > 0 {
		cfg.Timeslice = durMs(o.Slice)
	}
	r, err := mediarec.New(cfg, ain, vin, func(c mediarec.Chunk) {
		tok := h.j.wcState().putBlob(c.Data)
		h.push(map[string]any{"r": id, "t": "data", "b": tok, "n": len(c.Data), "tc": c.TimeMs})
	})
	if err != nil {
		release()
		return fail("NotSupportedError", "%v", err)
	}
	s := &recSession{id: id, r: r, audio: map[string]int{}, video: vid}
	for i, t := range aids {
		s.audio[t] = i
	}
	h.mu.Lock()
	h.recs[id] = s
	h.mu.Unlock()
	r.Start()
	go func() {
		<-r.Done()
		h.mu.Lock()
		delete(h.recs, id)
		h.mu.Unlock()
		h.push(map[string]any{"r": id, "t": "stop"})
	}()
	return map[string]any{"id": id, "mime": mime, "audio": len(ain), "video": vin != nil}
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// audioSource subscribes to the audio behind a track of this page.
func (h *recHost) audioSource(d recTrackDesc) (*mediarec.AudioIn, func()) {
	if c := h.j.capture; c != nil {
		if ct := c.track(d.ID); ct != nil {
			if ct.t.Kind != capture.AudioInput {
				return nil, nil
			}
			ch, cancel := ct.t.SubscribeAudio()
			m := ct.t.AudioMode()
			if m.Rate <= 0 {
				m = capture.AudioMode{Rate: 48000, Channels: 1}
			}
			return &mediarec.AudioIn{C: ch, Cancel: cancel, Rate: m.Rate, Channels: max(m.Channels, 1)}, cancel
		}
	}
	h.mu.Lock()
	tap := h.taps[d.ID]
	h.mu.Unlock()
	if tap != nil {
		ch, cancel := tap.subscribe()
		return &mediarec.AudioIn{C: ch, Cancel: cancel, Rate: tap.rate, Channels: tap.nch(), OwnClock: true}, cancel
	}
	return nil, nil
}

// videoSource subscribes to the pictures behind a track of this page.
func (h *recHost) videoSource(d recTrackDesc) (*mediarec.VideoIn, func()) {
	if c := h.j.capture; c != nil {
		if ct := c.track(d.ID); ct != nil {
			if ct.t.Kind != capture.VideoInput && ct.t.Kind != capture.Screen {
				return nil, nil
			}
			m := ct.t.VideoMode()
			w, hh, fps := m.W, m.H, m.FPS
			if w <= 0 {
				w, hh = d.W, d.H
			}
			if fps <= 0 {
				fps = d.FPS
			}
			if w <= 0 || hh <= 0 {
				return nil, nil
			}
			ch, cancel := ct.t.SubscribeVideo()
			return &mediarec.VideoIn{C: ch, Cancel: cancel, W: w, H: hh, FPS: fps}, cancel
		}
	}
	if d.Canvas != 0 && h.j.tab != nil && d.W > 0 && d.H > 0 {
		fps := d.FPS
		if fps <= 0 {
			fps = 30
		}
		src := newCanvasVideo(h.j.tab, d.Canvas, fps)
		ch, err := src.Start(capture.VideoConstraints{})
		if err != nil {
			return nil, nil
		}
		cancel := func() { _ = src.Stop() }
		return &mediarec.VideoIn{C: ch, Cancel: cancel, W: d.W, H: d.H, FPS: fps}, cancel
	}
	return nil, nil
}

func (h *recHost) session(id int64) *recSession {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.recs[id]
}

func (j *jsVM) recHostFns(f map[string]hostFn) {
	h := func() *recHost { return j.recState() } // created on first use
	f["recMime"] = func(a []any) (any, error) {
		sp, ok := mediarec.ParseMime(argS(a, 0))
		if !ok {
			return "", nil
		}
		return jsonStr(map[string]any{"c": sp.Container, "a": sp.Audio, "v": sp.Video, "t": sp.Type}), nil
	}
	f["recStart"] = func(a []any) (any, error) {
		var o recOpts
		if err := json.Unmarshal([]byte(argS(a, 0)), &o); err != nil {
			return nil, fmt.Errorf("TypeError: %v", err)
		}
		return jsonStr(h().start(&o)), nil
	}
	f["recCtl"] = func(a []any) (any, error) {
		s := h().session(argI(a, 0))
		if s == nil {
			return nil, nil
		}
		switch argS(a, 1) {
		case "pause":
			s.r.Pause()
		case "resume":
			s.r.Resume()
		case "data":
			s.r.RequestData()
		case "stop":
			s.r.Stop()
		}
		return nil, nil
	}
	f["recEnd"] = func(a []any) (any, error) {
		s := h().session(argI(a, 0))
		if s == nil {
			return nil, nil
		}
		t := argS(a, 1)
		if i, ok := s.audio[t]; ok {
			s.r.AudioEnded(i)
		}
		if s.video == t {
			s.r.VideoEnded()
		}
		return nil, nil
	}
	// the bytes of a chunk for pages whose VM has no typed-array natives
	f["recTake"] = func(a []any) (any, error) {
		wc := j.wcState()
		wc.mu.Lock()
		b := wc.blobs[argI(a, 0)]
		delete(wc.blobs, argI(a, 0))
		wc.mu.Unlock()
		return b64s(b), nil
	}
}

func init() {
	registerAsyncHandler("rec", func(j *jsVM, e asyncEvent) {
		if _, err := j.call("__swRecEvent", e.data); err != nil && j.tab != nil && !strings.Contains(err.Error(), "not defined") {
			j.tab.LogConsole("error", jsErrorText(err, "MediaRecorder callback"))
		}
		j.jobs()
	})
}
