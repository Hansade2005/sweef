package engine

// webcodecs_streams.go: the insertable-stream half of the raw-media APIs, on top of the VideoFrame/AudioData storage of
// webcodecs_data.go:
//
//   - MediaStreamTrackProcessor: a goroutine reads a live track (capture, generated, remote WebRTC, canvas) and tells
//     the page about each frame with an event; the page's readable stream hands it out as a VideoFrame / AudioData.
//   - MediaStreamTrackGenerator and VideoTrackGenerator: a capture.Track whose source the page feeds, so previews,
//     WebRTC senders and Web Audio see it like any other track (capture_host.go).

import (
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sweefbrowser/sweef/internal/capture"
)

type wsHost struct {
	j      *jsVM
	mu     sync.Mutex
	subs   map[int64]func()
	nsub   int64
	closed bool
}

func init() {
	registerAsyncHandler("wcs", func(j *jsVM, e asyncEvent) {
		if _, err := j.call("__swWcsEvent", e.id, e.typ, e.data); err != nil && j.tab != nil {
			j.tab.LogConsole("error", jsErrorText(err, "track processor "+e.typ))
		}
		j.jobs()
	})
	registerHostExtension("wcstreams", func(j *jsVM) map[string]hostFn {
		h := &wsHost{j: j, subs: map[int64]func(){}}
		j.onClose(h.closeAll)
		f := map[string]hostFn{}
		h.processorFns(f)
		h.generatorFns(f)
		return f
	})
}

func (h *wsHost) closeAll() {
	h.mu.Lock()
	h.closed = true
	subs := h.subs
	h.subs = map[int64]func(){}
	h.mu.Unlock()
	for _, c := range subs {
		c()
	}
}

func (h *wsHost) push(typ string, id int64, v any) {
	h.mu.Lock()
	closed := h.closed
	h.mu.Unlock()
	if closed {
		return
	}
	var data string
	if v != nil {
		b, _ := json.Marshal(v)
		data = string(b)
	}
	h.j.rt().push(asyncEvent{kind: "wcs", id: id, typ: typ, data: data})
	if t := h.j.tab; t != nil {
		t.wake()
	}
}

// video hands a captured I420 frame to the page as a VideoFrame payload.
func (h *wsHost) video(sub int64, c *capture.Frame, start time.Time) {
	if c == nil || c.W <= 0 || c.H <= 0 {
		return
	}
	cw, chh := (c.W+1)/2, (c.H+1)/2
	d := &wcData{format: "I420", w: c.W, h: c.H, planes: [][]byte{c.Y, c.U, c.V}, strides: []int{c.YStride, c.UVStride, c.UVStride}, bytes: int64(c.W*c.H + 2*cw*chh)}
	hd := wcNewHandle(h.j.wcState(), d, 0, 0, c.W, c.H)
	if hd == nil {
		return // over the frame memory budget: drop the frame
	}
	h.push("video", sub, map[string]any{"i": hd.info(), "ts": time.Since(start).Microseconds()})
}

// audio hands an interleaved s16 chunk to the page as an AudioData payload.
func (h *wsHost) audio(sub int64, pcm []int16, rate, ch int, frameIndex int64) {
	if ch < 1 || len(pcm) < ch {
		return
	}
	n := len(pcm) / ch
	planes := make([][]float32, ch)
	for c := range planes {
		planes[c] = make([]float32, n)
		for i := 0; i < n; i++ {
			planes[c][i] = float32(pcm[i*ch+c]) / 32768
		}
	}
	hd := wcNewHandle(h.j.wcState(), wcAudioFromPlanar(planes, rate), 0, 0, 0, 0)
	if hd == nil {
		return
	}
	ts := frameIndex * 1_000_000 / int64(max(rate, 1))
	h.push("audio", sub, map[string]any{"i": hd.info(), "ts": ts})
}

// wsVideoSink receives the decoded pictures of a remote WebRTC track.
type wsVideoSink struct {
	h     *wsHost
	sub   int64
	start time.Time
}

func (s *wsVideoSink) PushFrame(img *image.YCbCr) {
	b := img.Bounds()
	if img.SubsampleRatio != image.YCbCrSubsampleRatio420 || b.Min != (image.Point{}) {
		return
	}
	s.h.video(s.sub, &capture.Frame{W: b.Dx(), H: b.Dy(), Y: img.Y, U: img.Cb, V: img.Cr, YStride: img.YStride, UVStride: img.CStride}, s.start)
}

func (h *wsHost) processorFns(f map[string]hostFn) {
	// wcSubscribe(kind, specJSON) -> subscription id. spec: {cap: trackId} or {rtc: [pc, tr]} or {canvas, fps}.
	f["wcSubscribe"] = func(a []any) (any, error) {
		kind := argS(a, 0)
		var spec struct {
			Cap string  `json:"cap"`
			RTC []int64 `json:"rtc"`
			Cvs int64   `json:"canvas"`
			Fps float64 `json:"fps"`
		}
		_ = json.Unmarshal([]byte(argS(a, 1)), &spec)
		h.mu.Lock()
		if h.closed {
			h.mu.Unlock()
			return nil, errors.New("InvalidStateError: the document is gone")
		}
		h.nsub++
		sub := h.nsub
		h.mu.Unlock()
		stop := make(chan struct{})
		var once sync.Once
		cancel := func() { once.Do(func() { close(stop) }) }
		var detach func()
		start := time.Now()
		pump := func(ch <-chan *capture.Frame, endOnClose bool) {
			go func() {
				for {
					select {
					case fr, ok := <-ch:
						if !ok {
							if endOnClose {
								h.push("end", sub, nil)
							}
							return
						}
						h.video(sub, fr, start)
					case <-stop:
						return
					}
				}
			}()
		}
		switch {
		case spec.Cap != "":
			ct := capture.LookupTrack(spec.Cap)
			if ct == nil {
				return nil, errors.New("InvalidStateError: the track is not live")
			}
			if kind == "video" {
				ch, c := ct.SubscribeVideo()
				detach = c
				pump(ch, true)
			} else {
				ch, c := ct.SubscribeAudio()
				detach = c
				go func() {
					var played int64
					for {
						select {
						case ck, ok := <-ch:
							if !ok {
								h.push("end", sub, nil)
								return
							}
							h.audio(sub, ck.PCM, ck.Rate, ck.Channels, played)
							played += int64(len(ck.PCM) / max(ck.Channels, 1))
						case <-stop:
							return
						}
					}
				}()
			}
		case len(spec.RTC) == 2:
			rh := h.j.rtc
			if rh == nil {
				return nil, errors.New("InvalidStateError: no peer connection")
			}
			rh.mu.Lock()
			p := rh.peers[spec.RTC[0]]
			rh.mu.Unlock()
			x := (*rtcTR)(nil)
			if p != nil {
				x = p.tr(spec.RTC[1])
			}
			if x == nil {
				return nil, errors.New("InvalidStateError: unknown receiver")
			}
			if kind == "video" {
				detach = x.tapVideo(&wsVideoSink{h: h, sub: sub, start: start})
			} else {
				var played int64
				var mu sync.Mutex
				detach = x.tapAudio(func(pcm []int16, rate, ch int) {
					mu.Lock()
					at := played
					played += int64(len(pcm) / max(ch, 1))
					mu.Unlock()
					h.audio(sub, pcm, rate, ch, at)
				})
			}
		case spec.Cvs != 0 && kind == "video":
			t := h.j.tab
			if t == nil {
				return nil, errors.New("InvalidStateError: no document")
			}
			fps := spec.Fps
			if fps <= 0 {
				fps = 30
			}
			cv := newCanvasVideo(t, spec.Cvs, fps)
			ch, err := cv.Start(capture.VideoConstraints{FrameRate: fps})
			if err != nil {
				return nil, errors.New("InvalidStateError: " + err.Error())
			}
			detach = func() { _ = cv.Stop() }
			pump(ch, false)
		default:
			return nil, errors.New("InvalidStateError: this track cannot be read by a processor")
		}
		h.mu.Lock()
		h.subs[sub] = func() {
			cancel()
			if detach != nil {
				detach()
			}
		}
		h.mu.Unlock()
		return sub, nil
	}
	f["wcUnsubscribe"] = func(a []any) (any, error) {
		h.mu.Lock()
		c := h.subs[argI(a, 0)]
		delete(h.subs, argI(a, 0))
		h.mu.Unlock()
		if c != nil {
			c()
		}
		return nil, nil
	}
}

// ---- generators --------------------------------------------------------------------------------------------------

// genVideo is the source behind a generated video track: the page pushes frames.
type genVideo struct {
	mu   sync.Mutex
	ch   chan *capture.Frame
	done bool
	info capture.DeviceInfo
}

func (g *genVideo) Info() capture.DeviceInfo { return g.info }
func (g *genVideo) Start(capture.VideoConstraints) (<-chan *capture.Frame, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.ch = make(chan *capture.Frame, 3)
	return g.ch, nil
}
func (g *genVideo) Stop() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.done && g.ch != nil {
		g.done = true
		close(g.ch)
	}
	return nil
}
func (g *genVideo) Err() error { return nil }
func (g *genVideo) push(f *capture.Frame) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.done || g.ch == nil {
		return
	}
	for {
		select {
		case g.ch <- f:
			return
		default:
		}
		select {
		case <-g.ch:
		default:
		}
	}
}

type genAudio struct {
	mu   sync.Mutex
	ch   chan *capture.AudioChunk
	done bool
	info capture.DeviceInfo
}

func (g *genAudio) Info() capture.DeviceInfo { return g.info }
func (g *genAudio) Start(capture.AudioConstraints) (<-chan *capture.AudioChunk, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.ch = make(chan *capture.AudioChunk, 32)
	return g.ch, nil
}
func (g *genAudio) Stop() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.done && g.ch != nil {
		g.done = true
		close(g.ch)
	}
	return nil
}
func (g *genAudio) Err() error { return nil }
func (g *genAudio) push(c *capture.AudioChunk) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.done || g.ch == nil {
		return
	}
	for {
		select {
		case g.ch <- c:
			return
		default:
		}
		select {
		case <-g.ch:
		default:
		}
	}
}

var genSeq atomic.Int64

// newGenerated creates a live track the page feeds. kind is "video" or "audio".
func (h *capHost) newGenerated(kind, label string) (*capTrack, any, error) {
	n := genSeq.Add(1)
	key := fmt.Sprintf("generated-%d", n)
	origin := ""
	if t := h.tab(); t != nil {
		origin = t.URL()
	}
	if kind == "video" {
		src := &genVideo{info: capture.DeviceInfo{ID: key, Label: label, Kind: capture.VideoInput}}
		lease, err := capture.AcquireVideoFrom(key, func() (capture.VideoSource, error) { return src, nil }, capture.VideoConstraints{}, -1)
		if err != nil {
			return nil, nil, err
		}
		ct := &capTrack{kind: "generated-video", label: label, settings: map[string]any{"deviceId": "", "groupId": ""}, caps: map[string]any{}}
		ct.t = capture.NewVideoTrack(lease, src.info, origin, nil)
		h.add(ct)
		return ct, src, nil
	}
	src := &genAudio{info: capture.DeviceInfo{ID: key, Label: label, Kind: capture.AudioInput}}
	lease, err := capture.AcquireAudioFrom(key, func() (capture.AudioSource, error) { return src, nil }, capture.AudioConstraints{}, -1)
	if err != nil {
		return nil, nil, err
	}
	ct := &capTrack{kind: "generated-audio", label: label, settings: map[string]any{"deviceId": "", "groupId": "", "sampleRate": 48000, "channelCount": 2}, caps: map[string]any{}}
	ct.t = capture.NewAudioTrack(lease, src.info, origin, nil)
	h.add(ct)
	return ct, src, nil
}

var (
	genMu  sync.Mutex
	genReg = map[string]any{} // track id -> *genVideo | *genAudio
)

func (h *wsHost) generatorFns(f map[string]hostFn) {
	// wcGenNew(kind, label) -> the track as getUserMedia describes one
	f["wcGenNew"] = func(a []any) (any, error) {
		ch := h.j.capture
		if ch == nil {
			return nil, errors.New("InvalidStateError: no capture support")
		}
		ct, src, err := ch.newGenerated(argS(a, 0), argS(a, 1))
		if err != nil {
			return nil, errors.New("OperationError: " + err.Error())
		}
		genMu.Lock()
		genReg[ct.t.ID] = src
		genMu.Unlock()
		go func() {
			<-ct.t.Ended()
			genMu.Lock()
			delete(genReg, ct.t.ID)
			genMu.Unlock()
		}()
		b, _ := json.Marshal(ch.trackJSON(ct))
		return string(b), nil
	}
	// wcGenVideo(track, handle): the frame's visible rectangle becomes the next picture of the track
	f["wcGenVideo"] = func(a []any) (any, error) {
		genMu.Lock()
		g, _ := genReg[argS(a, 0)].(*genVideo)
		genMu.Unlock()
		hd := wcLookup(argI(a, 1))
		if g == nil || hd == nil || hd.d.audio {
			return false, nil
		}
		w, hh := hd.vw, hd.vh
		y, u, v := hd.d.toI420(hd.vx, hd.vy, w, hh)
		g.push(&capture.Frame{W: w, H: hh, Y: y, U: u, V: v, YStride: w, UVStride: ceilDiv(w, 2)})
		return true, nil
	}
	// wcGenAudio(track, handle): the AudioData's samples become the next chunk of the track
	f["wcGenAudio"] = func(a []any) (any, error) {
		genMu.Lock()
		g, _ := genReg[argS(a, 0)].(*genAudio)
		genMu.Unlock()
		hd := wcLookup(argI(a, 1))
		if g == nil || hd == nil || !hd.d.audio || hd.d.nch < 1 || hd.d.frames < 1 {
			return false, nil
		}
		d := hd.d
		pcm := make([]int16, d.frames*d.nch)
		for c := 0; c < d.nch; c++ {
			for i := 0; i < d.frames; i++ {
				v := d.pcm[c][i] * 32767
				if v > 32767 {
					v = 32767
				} else if v < -32768 {
					v = -32768
				}
				pcm[i*d.nch+c] = int16(v)
			}
		}
		g.push(&capture.AudioChunk{Rate: d.rate, Channels: d.nch, PCM: pcm})
		return true, nil
	}
}
