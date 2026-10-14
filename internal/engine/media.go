package engine

// media.go: host functions behind the HTMLMediaElement / MediaSource /
// Web Audio preludes (media_prelude.js, media_audio_prelude.js). Containers,
// decoders and DSP live in internal/media; this file only adapts them to the
// per-VM host object and publishes video frames through the bitmap registry.

import (
	"bytes"
	_ "embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"math"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	_ "golang.org/x/image/webp"

	"github.com/sweefbrowser/sweef/internal/media"
)

//go:embed media_prelude.js
var mediaPreludeJS string

func init() {
	registerPrelude("media", mediaPreludeJS)
	registerHostExtension("media", func(j *jsVM) map[string]hostFn { return newMediaHost(j).fns() })
}

const mediaChunk = 1 << 20

// netSource is a media.Source over HTTP Range requests with a chunk cache.
type netSource struct {
	j       *jsVM
	url     string
	mu      sync.Mutex
	size    int64
	chunks  map[int64][]byte
	order   []int64
	noRange bool
	whole   []byte
	fetched int64
}

func (n *netSource) Size() int64 { return n.size }

func (n *netSource) get(idx int64) ([]byte, error) {
	n.mu.Lock()
	if n.whole != nil {
		n.mu.Unlock()
		return n.whole, nil
	}
	if c, ok := n.chunks[idx]; ok {
		n.mu.Unlock()
		return c, nil
	}
	n.mu.Unlock()
	start := idx * mediaChunk
	end := start + mediaChunk - 1
	if end >= n.size && n.size > 0 {
		end = n.size - 1
	}
	r, err := n.j.tab.httpForScriptKind(KindMedia, "GET", n.url, map[string]string{"Range": fmt.Sprintf("bytes=%d-%d", start, end)}, "")
	if err != nil {
		return nil, err
	}
	status, _ := r["status"].(int)
	body, _ := r["body"].(string)
	if status == 200 {
		n.mu.Lock()
		n.whole = []byte(body)
		n.size = int64(len(body))
		n.noRange = true
		n.mu.Unlock()
		return n.whole, nil
	}
	if status != 206 {
		return nil, fmt.Errorf("http %d", status)
	}
	n.mu.Lock()
	n.chunks[idx] = []byte(body)
	n.order = append(n.order, idx)
	n.fetched += int64(len(body))
	for len(n.order) > 32 {
		delete(n.chunks, n.order[0])
		n.order = n.order[1:]
	}
	n.mu.Unlock()
	return []byte(body), nil
}

func (n *netSource) ReadAt(p []byte, off int64) (int, error) {
	total := 0
	for total < len(p) {
		pos := off + int64(total)
		if pos >= n.size {
			return total, errEOF
		}
		idx := pos / mediaChunk
		c, err := n.get(idx)
		if err != nil {
			return total, err
		}
		var rel int64
		if n.whole != nil {
			rel = pos
		} else {
			rel = pos - idx*mediaChunk
		}
		if rel >= int64(len(c)) {
			return total, errEOF
		}
		total += copy(p[total:], c[rel:])
	}
	return total, nil
}

var errEOF = fmt.Errorf("EOF")

// have reports the byte ranges present in the cache.
func (n *netSource) have() [][2]int64 {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.whole != nil {
		return [][2]int64{{0, n.size}}
	}
	var out [][2]int64
	for i := int64(0); i*mediaChunk < n.size; i++ {
		if c, ok := n.chunks[i]; ok {
			s, e := i*mediaChunk, i*mediaChunk+int64(len(c))
			if l := len(out); l > 0 && out[l-1][1] == s {
				out[l-1][1] = e
			} else {
				out = append(out, [2]int64{s, e})
			}
		}
	}
	return out
}

// opaqueVideoFrame is a converted video picture: an opaque RGBA image that nothing changes afterwards, which lets
// the painter draw it without copying it (raster.Painter looks for OpaqueFrame).
type opaqueVideoFrame struct{ *image.RGBA }

// OpaqueFrame returns the image.
func (f opaqueVideoFrame) OpaqueFrame() *image.RGBA { return f.RGBA }

// videoSource is the BitmapSource for one <video> node. Frames arrive either
// as decoded pictures from a player (converted to RGBA only when something
// paints or reads them) or as ready images from the legacy frame provider.
type videoSource struct {
	mu     sync.Mutex
	poster image.Image
	frame  image.Image   // RGBA of the current frame, nil until converted
	pic    media.Picture // current decoded picture (player path)
	hasPic bool
	w, h   int
	seq    int64
}

func (v *videoSource) Bitmap() image.Image {
	v.mu.Lock()
	if v.hasPic && v.frame == nil {
		pic, seq := v.pic, v.seq
		v.mu.Unlock()
		img := pic.RGBA() // the conversion runs outside the lock
		v.mu.Lock()
		if v.seq == seq && v.frame == nil && img != nil {
			v.frame = opaqueVideoFrame{img}
		}
		f := v.frame
		v.mu.Unlock()
		if f != nil {
			return f
		}
		if img != nil {
			return opaqueVideoFrame{img}
		}
		v.mu.Lock()
	}
	defer v.mu.Unlock()
	if v.frame != nil {
		return v.frame
	}
	return v.poster
}

// setPicture shows a decoded picture and reports whether its size differs from the previous one.
func (v *videoSource) setPicture(p media.Picture) (resized bool) {
	v.mu.Lock()
	v.pic, v.hasPic, v.frame = p, true, nil
	if w, h := p.Size(); w > 0 {
		resized = w != v.w || h != v.h
		v.w, v.h = w, h
	}
	v.seq++
	v.mu.Unlock()
	return resized
}

func (v *videoSource) Size() (int, int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.w > 0 && v.h > 0 {
		return v.w, v.h
	}
	if v.poster != nil {
		b := v.poster.Bounds()
		return b.Dx(), b.Dy()
	}
	return 0, 0
}

type mediaRes struct {
	src      media.Source
	net      *netSource
	info     *media.Info
	fs       media.FrameSource
	fsErr    error
	fsMu     sync.Mutex
	vs       *videoSource
	inflight atomic.Bool
	wantT    atomic.Uint64
	mode     string
}

type mediaHost struct {
	j       *jsVM
	mu      sync.Mutex
	res     map[int]*mediaRes
	next    int
	vsrcs   map[int64]*videoSource
	pcm     map[int]*media.PCM
	nextP   int
	wa      *waState
	players map[int64]*mediaPlayer
	mse     *mseHost
	eme     *emeKeys // Clear Key content keys (eme.go)
}

func newMediaHost(j *jsVM) *mediaHost {
	m := &mediaHost{j: j, res: map[int]*mediaRes{}, vsrcs: map[int64]*videoSource{}, pcm: map[int]*media.PCM{}, next: 1, nextP: 1,
		players: map[int64]*mediaPlayer{}, mse: newMSEHost(), eme: &emeKeys{stores: map[int]*media.KeyStore{}}}
	j.onClose(m.shutdown)
	return m
}

// shutdown stops every player and Web Audio output when the VM goes away.
func (m *mediaHost) shutdown() {
	m.mu.Lock()
	pl := m.players
	m.players = map[int64]*mediaPlayer{}
	m.mu.Unlock()
	for _, p := range pl {
		p.Close()
	}
	m.waShutdown()
}

func (m *mediaHost) vsFor(node int64) *videoSource {
	m.mu.Lock()
	defer m.mu.Unlock()
	v := m.vsrcs[node]
	if v == nil {
		v = &videoSource{}
		m.vsrcs[node] = v
	}
	return v
}

func infoMap(info *media.Info) map[string]any {
	tracks := make([]any, 0, len(info.Tracks))
	for _, t := range info.Tracks {
		tracks = append(tracks, map[string]any{
			"id": t.ID, "kind": t.Kind, "codec": t.Codec, "codecName": t.CodecName,
			"width": t.Width, "height": t.Height, "frameRate": t.FrameRate,
			"sampleRate": t.SampleRate, "channels": t.Channels, "language": t.Language,
			"name": t.Name, "default": t.Default,
		})
	}
	return map[string]any{"container": info.Container, "duration": info.Duration, "size": info.Size, "tracks": tracks, "mime": info.MIME(), "fragmented": info.Fragmented}
}

func (m *mediaHost) openSource(src media.Source, net *netSource) (any, error) {
	info, err := media.Probe(src, src.Size())
	if err != nil {
		return map[string]any{"ok": false, "error": "decode", "message": err.Error()}, nil
	}
	ok, reason := media.Playable(info)
	if !ok {
		return map[string]any{"ok": false, "error": "unsupported", "message": reason, "info": infoMap(info)}, nil
	}
	mode := ""
	v := info.Video()
	if v != nil {
		if media.HasNativeVideo(v.CodecName) {
			mode = "native"
		} else if media.FFmpegPath() != "" {
			mode = "ffmpeg"
		}
	}
	m.mu.Lock()
	id := m.next
	m.next++
	r := &mediaRes{src: src, net: net, info: info, mode: mode}
	m.res[id] = r
	m.mu.Unlock()
	out := map[string]any{"ok": true, "h": id, "info": infoMap(info), "frameMode": mode}
	return out, nil
}

func (m *mediaHost) fns() map[string]hostFn {
	f := map[string]hostFn{}
	// binary-safe HTTP for fetch()/XHR wrappers: the body comes back base64-encoded
	f["mediaHttpBytes"] = func(a []any) (any, error) {
		if m.j.tab == nil {
			return nil, fmt.Errorf("TypeError: Failed to fetch")
		}
		hdr := map[string]string{}
		if hs := argS(a, 2); hs != "" {
			_ = json.Unmarshal([]byte(hs), &hdr)
		}
		abs, err := resolveURL(m.j.tab.URL(), argS(a, 1))
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(abs, "data:") {
			b, err := mediaDataURL(abs)
			if err != nil {
				return nil, err
			}
			return map[string]any{"status": 200, "statusText": "OK", "url": abs, "headers": "{}", "b64": base64.StdEncoding.EncodeToString(b)}, nil
		}
		r, err := m.j.tab.httpForScript(argS(a, 0), abs, hdr, "")
		if err != nil {
			return nil, err
		}
		body, _ := r["body"].(string)
		hj, _ := json.Marshal(r["headers"])
		return map[string]any{"status": r["status"], "statusText": r["statusText"], "url": r["url"], "headers": string(hj), "b64": base64.StdEncoding.EncodeToString([]byte(body))}, nil
	}
	// Data Saver: true when media must not preload or autoplay (the page's play() still loads it)
	f["mediaLite"] = func(a []any) (any, error) {
		if m.j.tab == nil {
			return false, nil
		}
		return m.j.tab.policy().BlockMediaLoad, nil
	}
	f["mediaCanPlay"] = func(a []any) (any, error) { return media.CanPlayType(argS(a, 0)), nil }
	f["mediaIsTypeSupported"] = func(a []any) (any, error) { return media.IsTypeSupportedMSE(argS(a, 0)), nil }
	f["mediaFFmpeg"] = func(a []any) (any, error) { return media.FFmpegPath() != "", nil }
	f["mediaOpen"] = func(a []any) (any, error) {
		raw := argS(a, 0)
		if j := m.j; j.tab == nil {
			return map[string]any{"ok": false, "error": "network"}, nil
		}
		abs, err := resolveURL(m.j.tab.URL(), raw)
		if err != nil {
			return map[string]any{"ok": false, "error": "network", "message": err.Error()}, nil
		}
		if strings.HasPrefix(abs, "data:") {
			b, err := mediaDataURL(abs)
			if err != nil {
				return map[string]any{"ok": false, "error": "network", "message": err.Error()}, nil
			}
			return m.openSource(media.BytesSource(b), nil)
		}
		ns := &netSource{j: m.j, url: abs, chunks: map[int64][]byte{}, size: 1 << 40}
		r, err := m.j.tab.httpForScriptKind(KindMedia, "GET", abs, map[string]string{"Range": "bytes=0-" + strconv.Itoa(mediaChunk-1)}, "")
		if err != nil {
			return map[string]any{"ok": false, "error": "network", "message": err.Error()}, nil
		}
		status, _ := r["status"].(int)
		body, _ := r["body"].(string)
		hdr, _ := r["headers"].(map[string]any)
		switch {
		case status == 206:
			cr, _ := hdr["content-range"].(string)
			if i := strings.LastIndex(cr, "/"); i >= 0 {
				if tot, err := strconv.ParseInt(cr[i+1:], 10, 64); err == nil {
					ns.size = tot
				}
			}
			if ns.size == 1<<40 {
				ns.size = int64(len(body))
			}
			ns.chunks[0] = []byte(body)
			ns.order = []int64{0}
		case status == 200:
			ns.whole = []byte(body)
			ns.size = int64(len(body))
		default:
			return map[string]any{"ok": false, "error": "network", "status": status}, nil
		}
		ct, _ := hdr["content-type"].(string)
		if ns.size == 0 {
			return map[string]any{"ok": false, "error": "network", "message": "empty"}, nil
		}
		out, err := m.openSource(ns, ns)
		if mm, ok := out.(map[string]any); ok {
			mm["contentType"] = ct
			mm["status"] = status
		}
		return out, err
	}
	f["mediaOpenBytes"] = func(a []any) (any, error) {
		b, err := base64.StdEncoding.DecodeString(argS(a, 0))
		if err != nil {
			return map[string]any{"ok": false, "error": "decode"}, nil
		}
		return m.openSource(media.BytesSource(b), nil)
	}
	f["mediaClose"] = func(a []any) (any, error) {
		id := int(argI(a, 0))
		m.mu.Lock()
		r := m.res[id]
		delete(m.res, id)
		m.mu.Unlock()
		if r != nil {
			r.fsMu.Lock()
			if r.fs != nil {
				r.fs.Close()
			}
			r.fsMu.Unlock()
		}
		return nil, nil
	}
	f["mediaBuffered"] = func(a []any) (any, error) {
		r := m.get(int(argI(a, 0)))
		if r == nil {
			return []any{}, nil
		}
		return bufferedRanges(r), nil
	}
	// mediaFetch(h, fromTime, seconds): make sure the bytes for the window are cached.
	f["mediaFetch"] = func(a []any) (any, error) {
		r := m.get(int(argI(a, 0)))
		if r == nil {
			return nil, nil
		}
		if r.net != nil && r.net.whole == nil {
			from := r.info.ByteForTime(argF(a, 1))
			to := r.info.ByteForTime(argF(a, 1) + argF(a, 2))
			if to <= from {
				to = from + mediaChunk
			}
			if to > r.net.size {
				to = r.net.size
			}
			for c := from / mediaChunk; c*mediaChunk < to; c++ {
				if _, err := r.net.get(c); err != nil {
					break
				}
			}
		}
		return bufferedRanges(r), nil
	}
	f["mediaPoster"] = func(a []any) (any, error) {
		node := argI(a, 0)
		vs := m.vsFor(node)
		raw := argS(a, 1)
		if raw == "" {
			vs.mu.Lock()
			vs.poster = nil
			vs.mu.Unlock()
			m.publish(node, vs)
			return false, nil
		}
		abs, err := resolveURL(m.j.tab.URL(), raw)
		if err != nil {
			return false, nil
		}
		var data []byte
		if strings.HasPrefix(abs, "data:") {
			data, err = mediaDataURL(abs)
		} else if pol := m.j.tab.policy(); pol.NoImages {
			// strict: the poster is an image like any other
			m.j.tab.noteSkipped(KindImage, abs, -1, -1, "poster", 0)
			return false, nil
		} else {
			var body string
			var status int
			o := fetchOpts{kind: KindImage}
			if pol.ImageMaxBytes > 0 {
				o.maxBody = int64(pol.ImageMaxBytes)
			}
			body, status, err = m.j.tab.fetchRes(abs, o)
			if err == nil && status != 200 {
				err = fmt.Errorf("status %d", status)
			}
			data = []byte(body)
		}
		if err != nil {
			return false, nil
		}
		img, _, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			return false, nil
		}
		vs.mu.Lock()
		vs.poster = img
		vs.mu.Unlock()
		m.publish(node, vs)
		return true, nil
	}
	// mediaBind(node, h): the node shows frames of resource h (0 unbinds).
	f["mediaBind"] = func(a []any) (any, error) {
		node := argI(a, 0)
		vs := m.vsFor(node)
		vs.mu.Lock()
		vs.frame, vs.hasPic = nil, false
		vs.w, vs.h = 0, 0
		if r := m.get(int(argI(a, 1))); r != nil {
			if v := r.info.Video(); v != nil {
				vs.w, vs.h = v.Width, v.Height
			}
			r.vs = vs
		}
		vs.mu.Unlock()
		m.publish(node, vs)
		return nil, nil
	}
	f["mediaFrame"] = func(a []any) (any, error) {
		r := m.get(int(argI(a, 0)))
		if r == nil || r.mode == "" || r.vs == nil {
			return false, nil
		}
		r.wantT.Store(math.Float64bits(argF(a, 1)))
		if r.inflight.CompareAndSwap(false, true) {
			go m.frameWorker(r)
		}
		return true, nil
	}
	f["mediaFrameSeq"] = func(a []any) (any, error) {
		r := m.get(int(argI(a, 0)))
		if r == nil || r.vs == nil {
			return 0, nil
		}
		r.vs.mu.Lock()
		defer r.vs.mu.Unlock()
		return r.vs.seq, nil
	}
	f["mediaFramePNG"] = func(a []any) (any, error) { // for tests / drawImage fallbacks
		r := m.get(int(argI(a, 0)))
		if r == nil || r.vs == nil {
			return nil, nil
		}
		img := r.vs.Bitmap()
		if img == nil {
			return nil, nil
		}
		return encodePNGBase64(img), nil
	}
	// decode whole audio of a resource for Web Audio: returns pcm id
	f["mediaDecodeAudio"] = func(a []any) (any, error) {
		r := m.get(int(argI(a, 0)))
		if r == nil {
			return nil, fmt.Errorf("EncodingError")
		}
		data := make([]byte, r.src.Size())
		if _, err := r.src.ReadAt(data, 0); err != nil && int64(len(data)) != r.src.Size() {
			return nil, fmt.Errorf("EncodingError")
		}
		p, err := media.DecodeAudio(data)
		if err != nil {
			return nil, fmt.Errorf("EncodingError: %v", err)
		}
		return m.storePCM(p, int(argI(a, 1))), nil
	}
	f["mediaDecodeBytes"] = func(a []any) (any, error) {
		b, err := base64.StdEncoding.DecodeString(argS(a, 0))
		if err != nil {
			return nil, fmt.Errorf("EncodingError")
		}
		p, err := media.DecodeAudio(b)
		if err != nil || p.Frames() == 0 {
			return nil, fmt.Errorf("EncodingError")
		}
		return m.storePCM(p, int(argI(a, 1))), nil
	}
	f["mediaPCMData"] = func(a []any) (any, error) {
		m.mu.Lock()
		p := m.pcm[int(argI(a, 0))]
		m.mu.Unlock()
		ch := int(argI(a, 1))
		if p == nil || ch >= len(p.Data) {
			return "", nil
		}
		return f32b64(p.Data[ch]), nil
	}
	f["mediaStoreF32"] = func(a []any) (any, error) { // channels uploaded from JS AudioBuffers
		chs, _ := a[1].([]any)
		p := &media.PCM{SampleRate: int(argI(a, 0))}
		for _, c := range chs {
			s, _ := c.(string)
			p.Data = append(p.Data, b64f32(s))
		}
		return m.storePCM(p, 0), nil
	}
	f["mediaParseVTT"] = nil
	delete(f, "mediaParseVTT")
	m.waHost(f)
	return f
}

func (m *mediaHost) get(id int) *mediaRes {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.res[id]
}

func (m *mediaHost) storePCMData(d [][]float32, rate int) map[string]any {
	return m.storePCM(&media.PCM{SampleRate: rate, Data: d}, 0)
}

func (m *mediaHost) storePCM(p *media.PCM, rate int) map[string]any {
	if rate > 0 && p.SampleRate != rate {
		p = media.Resample(p, rate)
	}
	m.mu.Lock()
	id := m.nextP
	m.nextP++
	m.pcm[id] = p
	m.mu.Unlock()
	return map[string]any{"id": id, "sampleRate": p.SampleRate, "channels": len(p.Data), "length": p.Frames()}
}

func (m *mediaHost) publish(node int64, vs *videoSource) {
	if m.j.tab != nil {
		m.j.tab.SetBitmapSource(node, vs)
	}
}

func (m *mediaHost) frameWorker(r *mediaRes) {
	defer r.inflight.Store(false)
	for {
		t := math.Float64frombits(r.wantT.Load())
		r.fsMu.Lock()
		if r.fs == nil && r.fsErr == nil {
			path := ""
			r.fs, r.fsErr = media.OpenFrames(r.src, r.info, path)
		}
		fs := r.fs
		r.fsMu.Unlock()
		if fs == nil {
			return
		}
		img, err := fs.Frame(t)
		if err == nil && img != nil {
			r.vs.mu.Lock()
			r.vs.frame, r.vs.hasPic = img, false
			if b := img.Bounds(); r.vs.w == 0 {
				r.vs.w, r.vs.h = b.Dx(), b.Dy()
			}
			r.vs.seq++
			r.vs.mu.Unlock()
		}
		if math.Float64bits(t) == r.wantT.Load() {
			return
		}
	}
}

func bufferedRanges(r *mediaRes) []any {
	if r.net == nil {
		return []any{[]any{0.0, math.Max(r.info.Duration, 0)}}
	}
	var out []any
	for _, br := range r.net.have() {
		s, e := r.info.TimeForByte(br[0]), r.info.TimeForByte(br[1])
		if br[0] <= r.info.DataStart {
			s = 0
		}
		if br[1] >= r.net.size {
			e = r.info.Duration
		}
		if e > s {
			if l := len(out); l > 0 {
				if prev := out[l-1].([]any); s <= prev[1].(float64)+0.001 {
					prev[1] = e
					continue
				}
			}
			out = append(out, []any{s, e})
		}
	}
	return out
}

func argF(a []any, i int) float64 {
	if i >= len(a) {
		return 0
	}
	switch v := a[i].(type) {
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case float64:
		return v
	}
	return 0
}

func mediaDataURL(u string) ([]byte, error) {
	i := strings.IndexByte(u, ',')
	if i < 0 {
		return nil, fmt.Errorf("bad data URL")
	}
	meta, body := u[5:i], u[i+1:]
	if strings.HasSuffix(meta, ";base64") {
		b, err := base64.StdEncoding.DecodeString(body)
		if err != nil {
			b, err = base64.RawStdEncoding.DecodeString(body)
		}
		return b, err
	}
	s, err := url.PathUnescape(body)
	return []byte(s), err
}

func f32b64(s []float32) string {
	b := make([]byte, len(s)*4)
	for i, v := range s {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(v))
	}
	return base64.StdEncoding.EncodeToString(b)
}

func b64f32(s string) []float32 {
	b, _ := base64.StdEncoding.DecodeString(s)
	out := make([]float32, len(b)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return out
}

func encodePNGBase64(img image.Image) string {
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return base64.StdEncoding.EncodeToString(b.Bytes())
}
