package rtcmedia

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

// Negotiated describes what the remote end agreed to for a sender.
type Negotiated struct {
	Bound   bool
	PT      uint8
	RTXPT   uint8
	SSRC    uint32
	RTXSSRC uint32
	TWCC    bool // transport-wide congestion control feedback (extension and feedback type)
	REMB    bool
	NACK    bool
	PLI     bool
	FIR     bool
	MidID   uint8
}

// LocalTrack is the webrtc.TrackLocal the senders write RTP into. It stays bound to one RTPSender for
// its whole life, so replaceTrack only swaps the media source and the RTP stream (SSRC, sequence
// numbers, timestamps) continues without a gap.
type LocalTrack struct {
	kind     webrtc.RTPCodecType
	id       string
	streamID string
	mime     string

	mu     sync.RWMutex
	w      webrtc.TrackLocalWriter
	ctxID  string
	neg    Negotiated
	audID  uint8
	absID  uint8
	midFn  func() string
	onBind []func(Negotiated)

	pkts atomic.Uint64
}

// NewLocalTrack creates a track of a kind ("audio" or "video") with its msid track and stream ids.
func NewLocalTrack(kind webrtc.RTPCodecType, id, streamID string) *LocalTrack {
	mime := webrtc.MimeTypeVP8
	if kind == webrtc.RTPCodecTypeAudio {
		mime = webrtc.MimeTypeOpus
	}
	return &LocalTrack{kind: kind, id: id, streamID: streamID, mime: mime}
}

func (t *LocalTrack) ID() string                 { return t.id }
func (t *LocalTrack) RID() string                { return "" }
func (t *LocalTrack) StreamID() string           { return t.streamID }
func (t *LocalTrack) Kind() webrtc.RTPCodecType  { return t.kind }
func (t *LocalTrack) SetMidFunc(f func() string) { t.mu.Lock(); t.midFn = f; t.mu.Unlock() }

// OnBind registers f to be called whenever the track is (re)bound to a transport.
func (t *LocalTrack) OnBind(f func(Negotiated)) {
	t.mu.Lock()
	t.onBind = append(t.onBind, f)
	n := t.neg
	t.mu.Unlock()
	if n.Bound {
		f(n)
	}
}

// Negotiated returns the current negotiation result.
func (t *LocalTrack) Negotiated() Negotiated {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.neg
}

// Bind implements webrtc.TrackLocal.
func (t *LocalTrack) Bind(ctx webrtc.TrackLocalContext) (webrtc.RTPCodecParameters, error) {
	var codec webrtc.RTPCodecParameters
	found := false
	for _, c := range ctx.CodecParameters() {
		if strings.EqualFold(c.MimeType, t.mime) {
			codec, found = c, true
			break
		}
	}
	if !found {
		return webrtc.RTPCodecParameters{}, webrtc.ErrUnsupportedCodec
	}
	n := Negotiated{Bound: true, PT: uint8(codec.PayloadType), SSRC: uint32(ctx.SSRC()), RTXSSRC: uint32(ctx.SSRCRetransmission())}
	for _, c := range ctx.CodecParameters() {
		if strings.EqualFold(c.MimeType, webrtc.MimeTypeRTX) && strings.Contains(c.SDPFmtpLine, "apt=") {
			if strings.HasSuffix(c.SDPFmtpLine, itoa(int(codec.PayloadType))) {
				n.RTXPT = uint8(c.PayloadType)
			}
		}
	}
	for _, fb := range codec.RTCPFeedback {
		switch {
		case fb.Type == "nack" && fb.Parameter == "":
			n.NACK = true
		case fb.Type == "nack" && fb.Parameter == "pli":
			n.PLI = true
		case fb.Type == "ccm" && fb.Parameter == "fir":
			n.FIR = true
		case fb.Type == "goog-remb":
			n.REMB = true
		}
	}
	var aud, abs, mid uint8
	for _, e := range ctx.HeaderExtensions() {
		switch e.URI {
		case ExtTransportCC:
			n.TWCC = true
		case ExtAudioLevel:
			aud = uint8(e.ID)
		case ExtAbsSendTime:
			abs = uint8(e.ID)
		case ExtMid:
			mid = uint8(e.ID)
		}
	}
	n.MidID = mid
	t.mu.Lock()
	t.w = ctx.WriteStream()
	t.ctxID = ctx.ID()
	t.neg = n
	t.audID, t.absID = aud, abs
	cbs := append([]func(Negotiated){}, t.onBind...)
	t.mu.Unlock()
	for _, f := range cbs {
		f(n)
	}
	return codec, nil
}

// Unbind implements webrtc.TrackLocal.
func (t *LocalTrack) Unbind(ctx webrtc.TrackLocalContext) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ctxID != ctx.ID() {
		return webrtc.ErrUnbindFailed
	}
	t.w = nil
	t.neg = Negotiated{}
	return nil
}

// ErrNotBound is returned by Write before negotiation is complete.
var ErrNotBound = errors.New("rtcmedia: track is not bound to a transport")

// WriteOpts carries the per-packet header extension values.
type WriteOpts struct {
	AudioLevel int  // dBov, 0 (loudest) to 127; used when Audio is set
	Voice      bool // voice activity flag
	Audio      bool
	WithMid    bool
}

// Write sends one RTP packet (payload type and SSRC are filled in).
func (t *LocalTrack) Write(h *rtp.Header, payload []byte, o WriteOpts) (int, error) {
	t.mu.RLock()
	w, n, audID, absID, midFn := t.w, t.neg, t.audID, t.absID, t.midFn
	t.mu.RUnlock()
	if w == nil {
		return 0, ErrNotBound
	}
	h.SSRC = n.SSRC
	h.PayloadType = n.PT
	if absID != 0 {
		now := time.Now()
		ns := uint64(now.UnixNano())
		ts := uint32(((ns%64_000_000_000)<<18)/1_000_000_000) & 0xffffff
		_ = h.SetExtension(absID, []byte{byte(ts >> 16), byte(ts >> 8), byte(ts)})
	}
	if o.Audio && audID != 0 {
		lvl := byte(clampInt(o.AudioLevel, 0, 127))
		if o.Voice {
			lvl |= 0x80
		}
		_ = h.SetExtension(audID, []byte{lvl})
	}
	if o.WithMid && n.MidID != 0 && midFn != nil {
		if mid := midFn(); mid != "" {
			_ = h.SetExtension(n.MidID, []byte(mid))
		}
	}
	t.pkts.Add(1)
	return w.WriteRTP(h, payload)
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
