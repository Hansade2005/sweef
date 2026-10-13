package engine

// webrtc_media.go: the media half of RTCPeerConnection. It binds MediaStreamTracks to capture sources
// and RTP senders, received RTP to <video> elements and an audio sink, and adds the media statistics.
// The pipelines themselves (VP8 and Opus encoding, packetising, pacing, congestion control, jitter
// buffers, decoding) are in internal/rtcmedia; docs/webrtc.md has the overview.

import (
	"encoding/json"
	"errors"
	"image"
	"image/draw"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/sweefbrowser/sweef/internal/capture"
	"github.com/sweefbrowser/sweef/internal/rtcmedia"
	"github.com/sweefbrowser/sweef/internal/rtcmedia/testsrc"
)

// ---- what backs a MediaStreamTrack ------------------------------------------------------------

// RTCTrackDesc describes a page track as the engine sees it when it is attached to a sender.
type RTCTrackDesc struct {
	ID        string  `json:"id"`
	Kind      string  `json:"kind"`
	DeviceID  string  `json:"deviceId"`
	Label     string  `json:"label"`
	Width     int     `json:"width"`
	Height    int     `json:"height"`
	FrameRate float64 `json:"frameRate"`
	Canvas    int64   `json:"canvas"` // node id of the canvas behind canvas.captureStream()
	Enabled   bool    `json:"enabled"`
}

// RTCTrackSource opens fresh capture sources for one track (a sender needs its own instance, because a
// capture.VideoSource can only be started once).
type RTCTrackSource struct {
	OpenVideo func(c capture.VideoConstraints) (capture.VideoSource, error)
	OpenAudio func(c capture.AudioConstraints) (capture.AudioSource, error)
}

var (
	rtcSourceMu  sync.Mutex
	rtcSourceReg = map[string]RTCTrackSource{}
	rtcResolver  atomic.Pointer[func(t *Tab, d RTCTrackDesc) (RTCTrackSource, bool)]
	rtcSinkMu    sync.Mutex
	rtcSinkMake  func() rtcmedia.AudioSink
)

// RegisterRTCTrackSource says what feeds the track with this id (getUserMedia calls it for the tracks it
// creates). A sender created for the track opens its own source through it.
func RegisterRTCTrackSource(trackID string, s RTCTrackSource) {
	rtcSourceMu.Lock()
	rtcSourceReg[trackID] = s
	rtcSourceMu.Unlock()
}

// UnregisterRTCTrackSource forgets a track (call when the track is stopped or garbage).
func UnregisterRTCTrackSource(trackID string) {
	rtcSourceMu.Lock()
	delete(rtcSourceReg, trackID)
	rtcSourceMu.Unlock()
}

// SetRTCTrackResolver installs a function consulted for tracks that were not registered by id. The default
// handles canvas.captureStream() tracks, the fake devices (SWEEF_FAKE_MEDIA) and device ids known to
// internal/capture.
func SetRTCTrackResolver(f func(t *Tab, d RTCTrackDesc) (RTCTrackSource, bool)) {
	if f == nil {
		rtcResolver.Store(nil)
		return
	}
	rtcResolver.Store(&f)
}

// SetRTCAudioSinkFactory sets where received WebRTC audio goes: f is called for each remote audio track
// that is attached to a media element and returns the sink that plays it. The default discards audio;
// the audio output package supplies the real one.
func SetRTCAudioSinkFactory(f func() rtcmedia.AudioSink) {
	rtcSinkMu.Lock()
	rtcSinkMake = f
	rtcSinkMu.Unlock()
}

func rtcNewAudioSink() rtcmedia.AudioSink {
	rtcSinkMu.Lock()
	f := rtcSinkMake
	rtcSinkMu.Unlock()
	if f == nil {
		return rtcmedia.NullSink{}
	}
	return f()
}

func (p *rtcPeer) resolveSource(d RTCTrackDesc) (RTCTrackSource, bool) {
	rtcSourceMu.Lock()
	s, ok := rtcSourceReg[d.ID]
	rtcSourceMu.Unlock()
	if ok {
		return s, true
	}
	t := p.h.j.tab
	if f := rtcResolver.Load(); f != nil {
		if s, ok := (*f)(t, d); ok {
			return s, true
		}
	}
	switch {
	case d.Canvas != 0 && d.Kind == "video" && t != nil:
		node := d.Canvas
		return RTCTrackSource{OpenVideo: func(c capture.VideoConstraints) (capture.VideoSource, error) {
			fps := c.FrameRate
			if fps <= 0 {
				fps = d.FrameRate
			}
			return newCanvasVideo(t, node, fps), nil
		}}, true
	case d.DeviceID == "fake-cam":
		return RTCTrackSource{OpenVideo: func(c capture.VideoConstraints) (capture.VideoSource, error) {
			w, h, fps := c.Width, c.Height, c.FrameRate
			if w <= 0 {
				w, h = d.Width, d.Height
			}
			if w <= 0 {
				w, h = 640, 480
			}
			if fps <= 0 {
				fps = d.FrameRate
			}
			if fps <= 0 {
				fps = 30
			}
			return testsrc.NewVideo(w, h, fps), nil
		}}, true
	case d.DeviceID == "fake-mic":
		return RTCTrackSource{OpenAudio: func(capture.AudioConstraints) (capture.AudioSource, error) { return testsrc.NewTone(440), nil }}, true
	case d.DeviceID != "":
		kind := capture.VideoInput
		if d.Kind == "audio" {
			kind = capture.AudioInput
		}
		for _, di := range capture.Devices(kind) {
			if di.ID == d.DeviceID {
				id := di.ID
				return RTCTrackSource{
					OpenVideo: func(capture.VideoConstraints) (capture.VideoSource, error) { return capture.OpenVideo(id) },
					OpenAudio: func(capture.AudioConstraints) (capture.AudioSource, error) { return capture.OpenAudio(id) },
				}, true
			}
		}
	}
	return RTCTrackSource{}, false
}

// ---- canvas.captureStream() source -----------------------------------------------------------------

// canvasVideo samples a canvas (any registered bitmap source) at a frame rate.
type canvasVideo struct {
	tab  *Tab
	node int64
	fps  float64
	stop chan struct{}
	done chan struct{}
	once sync.Once
}

func newCanvasVideo(t *Tab, node int64, fps float64) *canvasVideo {
	if fps <= 0 {
		fps = 30
	}
	return &canvasVideo{tab: t, node: node, fps: fps}
}

func (c *canvasVideo) Info() capture.DeviceInfo {
	return capture.DeviceInfo{ID: "canvas", Label: "canvas", Kind: capture.VideoInput}
}

func (c *canvasVideo) source() BitmapSource {
	c.tab.mu.RLock()
	d := c.tab.dom
	c.tab.mu.RUnlock()
	bitmapMu.RLock()
	defer bitmapMu.RUnlock()
	return bitmapSources[bitmapKey{c.tab.ID, d, c.node}]
}

func (c *canvasVideo) Start(capture.VideoConstraints) (<-chan *capture.Frame, error) {
	c.stop, c.done = make(chan struct{}), make(chan struct{})
	ch := make(chan *capture.Frame, 2)
	go func() {
		defer close(c.done)
		defer close(ch)
		tk := time.NewTicker(time.Duration(float64(time.Second) / c.fps))
		defer tk.Stop()
		start := time.Now()
		for {
			select {
			case <-c.stop:
				return
			case <-tk.C:
			}
			src := c.source()
			if src == nil {
				continue
			}
			img := src.Bitmap()
			if img == nil {
				continue
			}
			f := imageToI420(img)
			if f == nil {
				continue
			}
			f.PTS = time.Since(start)
			select {
			case ch <- f:
			default:
			}
		}
	}()
	return ch, nil
}

func (c *canvasVideo) Stop() error {
	c.once.Do(func() {
		if c.stop != nil {
			close(c.stop)
			<-c.done
		}
	})
	return nil
}

func (c *canvasVideo) Err() error { return nil }

// imageToI420 converts any image to a (BT.601, limited range) I420 frame with even dimensions.
func imageToI420(img image.Image) *capture.Frame {
	b := img.Bounds()
	w, h := b.Dx()&^1, b.Dy()&^1
	if w < 16 || h < 16 {
		return nil
	}
	rgba, ok := img.(*image.RGBA)
	if !ok || rgba.Rect.Min != (image.Point{}) {
		rgba = image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
		draw.Draw(rgba, rgba.Bounds(), img, b.Min, draw.Src)
	}
	f := rtcmedia.NewFrame(w, h)
	for y := 0; y < h; y += 2 {
		r0 := rgba.Pix[y*rgba.Stride:]
		r1 := rgba.Pix[(y+1)*rgba.Stride:]
		for x := 0; x < w; x += 2 {
			var su, sv int
			for k := 0; k < 4; k++ {
				row := r0
				if k >= 2 {
					row = r1
				}
				i := (x + k&1) * 4
				r, g, bb := int(row[i]), int(row[i+1]), int(row[i+2])
				yy := (66*r+129*g+25*bb+128)>>8 + 16
				f.Y[(y+k>>1)*f.YStride+x+k&1] = byte(yy)
				su += (-38*r - 74*g + 112*bb)
				sv += (112*r - 94*g - 18*bb)
			}
			f.U[(y/2)*f.UVStride+x/2] = clampByte(((su>>2)+128)>>8 + 128)
			f.V[(y/2)*f.UVStride+x/2] = clampByte(((sv>>2)+128)>>8 + 128)
		}
	}
	return f
}

func clampByte(v int) byte {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return byte(v)
}

// nullAudio is the source of a track nothing is known about: it delivers no samples, so the sender
// transmits silence and the remote jitter buffer and the stats stay alive (the previous behaviour).
type nullAudio struct {
	stop chan struct{}
	once sync.Once
}

func (n *nullAudio) Info() capture.DeviceInfo {
	return capture.DeviceInfo{ID: "silence", Label: "silence", Kind: capture.AudioInput}
}
func (n *nullAudio) Start(capture.AudioConstraints) (<-chan *capture.AudioChunk, error) {
	n.stop = make(chan struct{})
	ch := make(chan *capture.AudioChunk)
	go func() { <-n.stop; close(ch) }()
	return ch, nil
}
func (n *nullAudio) Stop() error { n.once.Do(func() { close(n.stop) }); return nil }
func (n *nullAudio) Err() error  { return nil }

// ---- sender pipelines --------------------------------------------------------------------------------

func parseTrackDesc(s string) RTCTrackDesc {
	d := RTCTrackDesc{Enabled: true}
	if strings.HasPrefix(s, "{") {
		_ = json.Unmarshal([]byte(s), &d)
	} else {
		d.ID = s
	}
	return d
}

func kindOf(s string) webrtc.RTPCodecType {
	if s == "audio" {
		return webrtc.RTPCodecTypeAudio
	}
	return webrtc.RTPCodecTypeVideo
}

// ensurePipeline creates the media pipeline of a sender once it has a local track.
func (p *rtcPeer) ensurePipeline(x *rtcTR) error {
	x.smu.Lock()
	defer x.smu.Unlock()
	if x.local == nil {
		return errors.New("InvalidStateError: no local track")
	}
	if x.vs != nil || x.as != nil {
		return nil
	}
	if x.t.Kind() == webrtc.RTPCodecTypeVideo {
		x.vs = rtcmedia.NewVideoSender(x.local, p.est)
		h := x.vs.HandleRTCP
		x.rtcpH.Store(&h)
		if t := p.h.j.tab; t != nil && t.policy().Level == SaverStrict {
			sp := rtcmedia.DefaultSendParams()
			sp.MaxBitrate = 300_000 // Data Saver strict: a call still works, at a modest picture
			x.vs.SetParams(sp)
			x.params = sp
		}
		return nil
	}
	as, err := rtcmedia.NewAudioSender(x.local)
	if err != nil {
		return err
	}
	x.as = as
	h := as.HandleRTCP
	x.rtcpH.Store(&h)
	return nil
}

// bindSource opens the source behind the track (if known) and starts feeding the sender.
func (p *rtcPeer) bindSource(x *rtcTR, d RTCTrackDesc) {
	x.smu.Lock()
	defer x.smu.Unlock()
	x.desc = d
	src, known := p.resolveSource(d)
	if x.vs != nil {
		if known && src.OpenVideo != nil {
			if vsrc, err := src.OpenVideo(capture.VideoConstraints{Width: d.Width, Height: d.Height, FrameRate: d.FrameRate}); err == nil {
				_ = x.vs.SetSource(vsrc, capture.VideoConstraints{Width: d.Width, Height: d.Height, FrameRate: d.FrameRate})
			}
		} else {
			_ = x.vs.SetSource(nil, capture.VideoConstraints{})
		}
		x.vs.SetEnabled(d.Enabled)
		return
	}
	if x.as != nil {
		var asrc capture.AudioSource = &nullAudio{}
		if known && src.OpenAudio != nil {
			if a, err := src.OpenAudio(capture.AudioConstraints{Rate: 48000, Channels: 1}); err == nil {
				asrc = a
			}
		}
		_ = x.as.SetSource(asrc, capture.AudioConstraints{Rate: 48000, Channels: 1})
		x.as.SetEnabled(d.Enabled)
	}
}

// detachSource stops feeding the sender (replaceTrack(null)); the RTP stream stays negotiated.
func (p *rtcPeer) detachSource(x *rtcTR) {
	x.smu.Lock()
	defer x.smu.Unlock()
	if x.vs != nil {
		_ = x.vs.SetSource(nil, capture.VideoConstraints{})
	}
	if x.as != nil {
		_ = x.as.SetSource(nil, capture.AudioConstraints{})
	}
}

// closeSender tears down the pipeline of a sender.
func (x *rtcTR) closeSender() {
	x.smu.Lock()
	vs, as := x.vs, x.as
	x.vs, x.as, x.local = nil, nil, nil
	x.rtcpH.Store(nil)
	x.smu.Unlock()
	if vs != nil {
		vs.Close()
	}
	if as != nil {
		as.Close()
	}
}

func (x *rtcTR) setLive(live bool) {
	x.live.Store(live)
	x.smu.Lock()
	vs, as := x.vs, x.as
	x.smu.Unlock()
	if vs != nil {
		sp := vs.Params()
		sp.Active = live && x.encActive.Load()
		vs.SetParams(sp)
	}
	if as != nil {
		as.SetActive(live && x.encActive.Load())
	}
}

// ---- receiver pipelines --------------------------------------------------------------------------------

// rtcBitmap is the BitmapSource of a <video> showing a received track. The RGB conversion happens when
// something actually paints or reads the picture, not for every decoded frame.
type rtcBitmap struct {
	tab  *Tab
	mu   sync.Mutex
	yuv  *image.YCbCr
	rgba *image.RGBA
	seq  uint64
	conv uint64
	last time.Time
}

// rtcFrameHook, when set (tests), sees every frame pushed to a video element.
var rtcFrameHook atomic.Pointer[func(*image.YCbCr)]

func (b *rtcBitmap) PushFrame(img *image.YCbCr) {
	if h := rtcFrameHook.Load(); h != nil {
		(*h)(img)
	}
	b.mu.Lock()
	b.yuv = img
	b.seq++
	wake := time.Since(b.last) > 25*time.Millisecond
	if wake {
		b.last = time.Now()
	}
	b.mu.Unlock()
	if wake && b.tab != nil {
		b.tab.wake()
	}
}

func (b *rtcBitmap) Bitmap() image.Image {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.yuv == nil {
		return nil
	}
	if b.rgba == nil || b.conv != b.seq {
		r := b.yuv.Bounds()
		b.rgba = image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
		draw.Draw(b.rgba, b.rgba.Bounds(), b.yuv, r.Min, draw.Src)
		b.conv = b.seq
	}
	return b.rgba
}

func (b *rtcBitmap) Size() (int, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.yuv == nil {
		return 0, 0
	}
	r := b.yuv.Bounds()
	return r.Dx(), r.Dy()
}

// onRemoteTrack starts the receive pipeline of a track pion has found in the stream.
func (p *rtcPeer) onRemoteTrack(tr *webrtc.TrackRemote, rcv *webrtc.RTPReceiver) {
	var x *rtcTR
	for _, t := range p.pc.GetTransceivers() {
		if t.Receiver() == rcv {
			x = p.trFor(t)
			break
		}
	}
	drain := func() {
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			for {
				if _, _, err := tr.ReadRTP(); err != nil {
					return
				}
			}
		}()
	}
	p.wg.Add(1)
	go func() { // RTCP of the receiver must be read for the interceptors to work
		defer p.wg.Done()
		for {
			if _, _, err := rcv.ReadRTCP(); err != nil {
				return
			}
		}
	}()
	if x == nil || p.closed.Load() {
		drain()
		return
	}
	x.rmu.Lock()
	defer x.rmu.Unlock()
	if x.vr != nil || x.ar != nil { // a second simulcast layer: only the first is played
		drain()
		return
	}
	if tr.Kind() == webrtc.RTPCodecTypeVideo {
		vr := rtcmedia.NewVideoReceiver(tr, rcv, p.pc.WriteRTCP, rtcmedia.VideoCallbacks{
			OnResize: func(w, h int) { p.emit("vsize", map[string]any{"tr": x.id, "w": w, "h": h}) },
			OnMute:   func(m bool) { p.emit("trackmute", map[string]any{"tr": x.id, "muted": m}) },
		})
		x.vr = vr
		for _, s := range x.nodes {
			vr.AddSink(s)
		}
		return
	}
	var sink rtcmedia.AudioSink = rtcmedia.NullSink{}
	if x.audioOn {
		sink = rtcNewAudioSink()
	}
	ar, err := rtcmedia.NewAudioReceiver(tr, rcv, sink, func(m bool) { p.emit("trackmute", map[string]any{"tr": x.id, "muted": m}) })
	if err != nil {
		drain()
		return
	}
	x.ar = ar
}

// closeReceiver stops the receive pipelines of a transceiver (after the peer connection closed).
func (x *rtcTR) closeReceiver() {
	x.rmu.Lock()
	vr, ar := x.vr, x.ar
	x.vr, x.ar = nil, nil
	x.rmu.Unlock()
	if vr != nil {
		vr.Close()
		vr.Wait()
	}
	if ar != nil {
		ar.Close()
		ar.Wait()
	}
}

// ---- host functions ------------------------------------------------------------------------------------------

func (h *rtcHost) mediaFns(f map[string]hostFn) {
	f["rtcSetParams"] = func(a []any) (any, error) {
		p, err := h.peer(a)
		if err != nil {
			return nil, err
		}
		x := p.tr(argI(a, 1))
		if x == nil {
			return nil, nil
		}
		var e struct {
			Active       *bool    `json:"active"`
			MaxBitrate   *float64 `json:"maxBitrate"`
			MaxFramerate *float64 `json:"maxFramerate"`
			ScaleDown    *float64 `json:"scaleResolutionDownBy"`
		}
		_ = json.Unmarshal([]byte(argS(a, 2)), &e)
		x.encActive.Store(e.Active == nil || *e.Active)
		x.smu.Lock()
		sp := x.params
		if sp.ScaleDownBy < 1 {
			sp = rtcmedia.DefaultSendParams()
		}
		sp.MaxBitrate, sp.MaxFramerate, sp.ScaleDownBy = 0, 0, 1
		if e.MaxBitrate != nil {
			sp.MaxBitrate = int(*e.MaxBitrate)
		}
		if e.MaxFramerate != nil {
			sp.MaxFramerate = *e.MaxFramerate
		}
		if e.ScaleDown != nil && *e.ScaleDown >= 1 {
			sp.ScaleDownBy = *e.ScaleDown
		}
		if t := h.j.tab; t != nil && t.policy().Level == SaverStrict && (sp.MaxBitrate == 0 || sp.MaxBitrate > 300_000) {
			sp.MaxBitrate = 300_000
		}
		sp.Active = x.live.Load() && x.encActive.Load()
		x.params = sp
		vs, as := x.vs, x.as
		x.smu.Unlock()
		if vs != nil {
			vs.SetParams(sp)
		}
		if as != nil {
			as.SetActive(sp.Active)
			if e.MaxBitrate != nil && *e.MaxBitrate > 0 {
				as.SetBitrate(int(*e.MaxBitrate))
			}
		}
		return nil, nil
	}
	f["rtcTrackConstraints"] = func(a []any) (any, error) {
		p, err := h.peer(a)
		if err != nil {
			return nil, nil
		}
		if x := p.tr(argI(a, 1)); x != nil {
			p.bindSource(x, parseTrackDesc(argS(a, 2))) // reopen the source at the new size / frame rate
		}
		return nil, nil
	}
	f["rtcTrackEnabled"] = func(a []any) (any, error) {
		p, err := h.peer(a)
		if err != nil {
			return nil, err
		}
		if x := p.tr(argI(a, 1)); x != nil {
			x.smu.Lock()
			vs, as := x.vs, x.as
			x.desc.Enabled = argB(a, 2)
			x.smu.Unlock()
			if vs != nil {
				vs.SetEnabled(argB(a, 2))
			}
			if as != nil {
				as.SetEnabled(argB(a, 2))
			}
		}
		return nil, nil
	}
	f["rtcSenderParams"] = func(a []any) (any, error) {
		p, err := h.peer(a)
		if err != nil {
			return nil, err
		}
		x := p.tr(argI(a, 1))
		if x == nil || x.t.Sender() == nil {
			return jsonOut(map[string]any{})
		}
		sp := x.t.Sender().GetParameters()
		codecs := []map[string]any{}
		for _, c := range sp.Codecs {
			m := map[string]any{"payloadType": c.PayloadType, "mimeType": c.MimeType, "clockRate": c.ClockRate}
			if c.Channels > 0 {
				m["channels"] = c.Channels
			}
			if c.SDPFmtpLine != "" {
				m["sdpFmtpLine"] = c.SDPFmtpLine
			}
			codecs = append(codecs, m)
		}
		exts := []map[string]any{}
		for _, e := range sp.HeaderExtensions {
			exts = append(exts, map[string]any{"uri": e.URI, "id": e.ID})
		}
		return jsonOut(map[string]any{"codecs": codecs, "headerExtensions": exts})
	}
	f["rtcSetCodecPrefs"] = func(a []any) (any, error) {
		p, err := h.peer(a)
		if err != nil {
			return nil, err
		}
		x := p.tr(argI(a, 1))
		if x == nil {
			return nil, nil
		}
		var want []struct {
			MimeType    string `json:"mimeType"`
			ClockRate   int    `json:"clockRate"`
			Channels    int    `json:"channels"`
			SDPFmtpLine string `json:"sdpFmtpLine"`
		}
		_ = json.Unmarshal([]byte(argS(a, 2)), &want)
		var list []webrtc.RTPCodecParameters
		have := rtcmedia.Codecs(x.t.Kind())
		for _, w := range want {
			for _, c := range have {
				if strings.EqualFold(c.MimeType, w.MimeType) && (w.SDPFmtpLine == "" || c.SDPFmtpLine == w.SDPFmtpLine) {
					list = append(list, c)
					break
				}
			}
		}
		// RTX follows the codec it protects
		if len(want) > 0 && x.t.Kind() == webrtc.RTPCodecTypeVideo {
			for _, c := range list {
				if strings.EqualFold(c.MimeType, webrtc.MimeTypeVP8) {
					list = append(list, have[1])
					break
				}
			}
		}
		return nil, rtcErr(x.t.SetCodecPreferences(list))
	}
	f["rtcCaps"] = func(a []any) (any, error) {
		k := kindOf(argS(a, 0))
		codecs := []map[string]any{}
		for _, c := range rtcmedia.Codecs(k) {
			m := map[string]any{"mimeType": c.MimeType, "clockRate": c.ClockRate}
			if c.Channels > 0 {
				m["channels"] = c.Channels
			}
			if c.SDPFmtpLine != "" {
				m["sdpFmtpLine"] = c.SDPFmtpLine
			}
			codecs = append(codecs, m)
		}
		exts := []map[string]any{}
		for _, u := range rtcmedia.HeaderExtensions(k) {
			exts = append(exts, map[string]any{"uri": u})
		}
		return jsonOut(map[string]any{"codecs": codecs, "headerExtensions": exts})
	}
	f["rtcBindVideo"] = func(a []any) (any, error) {
		p, err := h.peer(a)
		if err != nil {
			return nil, err
		}
		x := p.tr(argI(a, 1))
		t := h.j.tab
		if x == nil || t == nil {
			return false, nil
		}
		node := argI(a, 2)
		x.rmu.Lock()
		if x.nodes == nil {
			x.nodes = map[int64]*rtcBitmap{}
		}
		bm := x.nodes[node]
		if bm == nil {
			bm = &rtcBitmap{tab: t}
			x.nodes[node] = bm
			if x.vr != nil {
				x.vr.AddSink(bm)
			}
		}
		w, hh := 0, 0
		if x.vr != nil {
			w, hh = x.vr.Size()
		}
		x.rmu.Unlock()
		t.SetBitmapSource(node, bm)
		return jsonOut(map[string]any{"w": w, "h": hh})
	}
	f["rtcUnbindVideo"] = func(a []any) (any, error) {
		p, err := h.peer(a)
		if err != nil {
			return nil, nil
		}
		x := p.tr(argI(a, 1))
		if x == nil {
			return nil, nil
		}
		node := argI(a, 2)
		x.rmu.Lock()
		if bm := x.nodes[node]; bm != nil {
			delete(x.nodes, node)
			if x.vr != nil {
				x.vr.RemoveSink(bm)
			}
		}
		x.rmu.Unlock()
		return nil, nil
	}
	f["rtcBindAudio"] = func(a []any) (any, error) {
		p, err := h.peer(a)
		if err != nil {
			return nil, nil
		}
		x := p.tr(argI(a, 1))
		if x == nil {
			return nil, nil
		}
		on := argB(a, 2)
		x.rmu.Lock()
		x.audioOn = on
		ar := x.ar
		x.rmu.Unlock()
		if ar != nil {
			if on {
				ar.SetSink(rtcNewAudioSink())
			} else {
				ar.SetSink(nil)
			}
		}
		return nil, nil
	}
	f["rtcSources"] = func(a []any) (any, error) {
		p, err := h.peer(a)
		if err != nil {
			return jsonOut([]any{})
		}
		x := p.tr(argI(a, 1))
		out := []map[string]any{}
		if x != nil {
			x.rmu.Lock()
			vr, ar := x.vr, x.ar
			x.rmu.Unlock()
			if vr != nil {
				if ssrc, at, ts, ok := vr.Sources(); ok {
					out = append(out, map[string]any{"source": ssrc, "timestamp": at.UnixMilli(), "rtpTimestamp": ts})
				}
			}
			if ar != nil {
				if ssrc, at, ts, lvl, ok := ar.Sources(); ok {
					out = append(out, map[string]any{"source": ssrc, "timestamp": at.UnixMilli(), "rtpTimestamp": ts, "audioLevel": lvl})
				}
			}
		}
		return jsonOut(out)
	}
}

// ---- statistics ---------------------------------------------------------------------------------------------

// addMediaStats merges the pipelines' counters into pion's report.
func (p *rtcPeer) addMediaStats(m map[string]map[string]any) {
	p.mu.Lock()
	trs := append([]*rtcTR(nil), p.trs...)
	p.mu.Unlock()
	now := float64(time.Now().UnixMilli())
	findRTP := func(typ string, ssrc uint32) map[string]any {
		for _, s := range m {
			if s["type"] == typ {
				if v, ok := s["ssrc"].(float64); ok && uint32(v) == ssrc {
					return s
				}
			}
		}
		return nil
	}
	// outbound-rtp and remote-inbound-rtp come from our own statistics interceptor (pion's report has
	// only the receive side)
	ensureOutbound := func(x *rtcTR, n rtcmedia.Negotiated, kind string) map[string]any {
		id := "outbound-rtp-" + itoa64(int64(n.SSRC))
		s := map[string]any{"type": "outbound-rtp", "id": id, "timestamp": now, "ssrc": float64(n.SSRC), "kind": kind, "transportId": "iceTransport", "mid": x.t.Mid()}
		if p.sg != nil {
			if st := p.sg.Get(n.SSRC); st != nil {
				o := st.OutboundRTPStreamStats
				s["packetsSent"] = o.PacketsSent
				s["bytesSent"] = o.BytesSent
				s["headerBytesSent"] = o.HeaderBytesSent
				s["nackCount"] = o.NACKCount
				s["pliCount"] = o.PLICount
				s["firCount"] = o.FIRCount
				ri := st.RemoteInboundRTPStreamStats
				if ri.RoundTripTimeMeasurements > 0 || ri.PacketsReceived > 0 {
					rid := "remote-inbound-rtp-" + itoa64(int64(n.SSRC))
					m[rid] = map[string]any{"type": "remote-inbound-rtp", "id": rid, "timestamp": now, "ssrc": float64(n.SSRC), "kind": kind, "transportId": "iceTransport",
						"localId": id, "packetsLost": ri.PacketsLost, "jitter": ri.Jitter, "fractionLost": ri.FractionLost,
						"roundTripTime": ri.RoundTripTime.Seconds(), "totalRoundTripTime": ri.TotalRoundTripTime.Seconds(), "roundTripTimeMeasurements": ri.RoundTripTimeMeasurements}
					s["remoteId"] = rid
				}
			}
		}
		if p.rtx != nil {
			pk, by, dr := p.rtx.Get(n.SSRC)
			s["retransmittedPacketsSent"] = pk
			s["retransmittedBytesSent"] = by
			s["packetsDiscardedOnSend"] = dr
		}
		m[id] = s
		return s
	}
	codecID := func(mime string) string {
		for id, s := range m {
			if s["type"] == "codec" && strings.EqualFold(s["mimeType"].(string), mime) {
				return id
			}
		}
		return ""
	}
	for _, x := range trs {
		x.smu.Lock()
		vs, as, local := x.vs, x.as, x.local
		desc := x.desc
		x.smu.Unlock()
		if local != nil {
			n := local.Negotiated()
			if vs != nil && n.Bound {
				st := vs.Stats()
				srcID := "SA" + itoa64(int64(n.SSRC))
				if s := ensureOutbound(x, n, "video"); s != nil {
					s["framesEncoded"] = st.FramesEncoded
					s["keyFramesEncoded"] = st.KeyFramesEncoded
					s["framesSent"] = st.FramesEncoded
					s["hugeFramesSent"] = st.HugeFramesSent
					s["totalEncodeTime"] = st.TotalEncodeTime
					s["frameWidth"] = st.FrameWidth
					s["frameHeight"] = st.FrameHeight
					s["framesPerSecond"] = st.FramesPerSecond
					s["qualityLimitationReason"] = st.QualityLimitationReason
					s["qualityLimitationDurations"] = st.QualityLimitationDurations
					s["qualityLimitationResolutionChanges"] = st.QualityLimitationResolutionChanges
					s["targetBitrate"] = st.TargetBitrate
					s["totalPacketSendDelay"] = st.PacerQueueDelay
					s["encoderImplementation"] = "Sweef VP8 (pure Go)"
					s["powerEfficientEncoder"] = false
					s["active"] = st.Active
					s["mediaSourceId"] = srcID
					if cid := codecID(webrtc.MimeTypeVP8); cid != "" {
						s["codecId"] = cid
					}
				}
				m[srcID] = map[string]any{"type": "media-source", "id": srcID, "timestamp": now, "kind": "video", "trackIdentifier": desc.ID,
					"width": st.FrameWidth, "height": st.FrameHeight, "frames": st.FramesCaptured, "framesPerSecond": st.FramesPerSecond}
			}
			if as != nil && n.Bound {
				st := as.Stats()
				srcID := "SA" + itoa64(int64(n.SSRC))
				if s := ensureOutbound(x, n, "audio"); s != nil {
					s["active"] = st.Active
					s["mediaSourceId"] = srcID
					s["targetBitrate"] = float64(32000)
					if cid := codecID(webrtc.MimeTypeOpus); cid != "" {
						s["codecId"] = cid
					}
				}
				m[srcID] = map[string]any{"type": "media-source", "id": srcID, "timestamp": now, "kind": "audio", "trackIdentifier": desc.ID,
					"audioLevel": st.AudioLevel, "totalAudioEnergy": st.TotalAudioEnergy, "totalSamplesDuration": st.TotalSamplesDuration}
			}
		}
		x.rmu.Lock()
		vr, ar := x.vr, x.ar
		x.rmu.Unlock()
		if vr != nil {
			st := vr.Stats()
			if s := findRTP("inbound-rtp", st.SSRC); s != nil {
				s["framesReceived"] = st.FramesReceived
				s["framesDecoded"] = st.FramesDecoded
				s["framesDropped"] = st.FramesDropped
				s["keyFramesDecoded"] = st.KeyFramesDecoded
				s["totalDecodeTime"] = st.TotalDecodeTime
				s["frameWidth"] = st.FrameWidth
				s["frameHeight"] = st.FrameHeight
				s["framesPerSecond"] = st.FramesPerSecond
				s["jitterBufferDelay"] = st.JitterBufferDelay
				s["jitterBufferEmittedCount"] = st.JitterBufferEmittedCount
				s["jitterBufferTargetDelay"] = st.JitterBufferTarget * float64(st.JitterBufferEmittedCount)
				s["freezeCount"] = st.FreezeCount
				s["totalFreezesDuration"] = st.TotalFreezesDuration
				s["pliCount"] = st.PLICount
				s["decoderImplementation"] = "Sweef VP8 (pure Go)"
				s["powerEfficientDecoder"] = false
				if cid := codecID(webrtc.MimeTypeVP8); cid != "" {
					s["codecId"] = cid
				}
			}
		}
		if ar != nil {
			st := ar.Stats()
			if s := findRTP("inbound-rtp", st.SSRC); s != nil {
				s["audioLevel"] = st.AudioLevel
				s["totalAudioEnergy"] = st.TotalAudioEnergy
				s["totalSamplesDuration"] = st.TotalSamplesDuration
				s["totalSamplesReceived"] = st.SamplesReceived
				s["concealedSamples"] = st.ConcealedSamples
				s["silentConcealedSamples"] = st.SilentConcealedSamples
				s["concealmentEvents"] = st.ConcealmentEvents
				s["insertedSamplesForDeceleration"] = st.InsertedSamplesForDeceleration
				s["removedSamplesForAcceleration"] = st.RemovedSamplesForAcceleration
				s["jitterBufferDelay"] = st.JitterBufferDelay / opusRateF
				s["jitterBufferEmittedCount"] = st.JitterBufferEmittedCount
				s["jitterBufferTargetDelay"] = st.JitterBufferTarget * float64(st.JitterBufferEmittedCount)
				s["packetsDiscarded"] = st.PacketsDiscarded
				s["fecPacketsReceived"] = st.FECRecovered
				s["decoderImplementation"] = "Sweef Opus (pure Go)"
				if cid := codecID(webrtc.MimeTypeOpus); cid != "" {
					s["codecId"] = cid
				}
			}
		}
	}
	// the available outgoing bitrate comes from the congestion controller
	if p.est != nil {
		for _, s := range m {
			if s["type"] == "candidate-pair" {
				if nom, _ := s["nominated"].(bool); nom {
					s["availableOutgoingBitrate"] = float64(p.est.GetTargetBitrate())
					s["sweefCongestionControl"] = p.est.GetStats() // non-standard: the GCC estimator's internals, for diagnostics
				}
			}
		}
	}
}

const opusRateF = 48000.0

func itoa64(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}
