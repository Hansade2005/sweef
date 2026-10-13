// Package rtcmedia is the media half of WebRTC for Sweef: it turns raw capture frames and PCM into RTP
// (VP8 and Opus, with the feedback machinery real services expect) and RTP back into decoded frames and
// PCM. It sits on pion/webrtc but does not know about the browser engine; internal/engine binds it to
// MediaStreamTracks and to <video> elements.
//
// Send path: capture.VideoSource -> scale -> internal/media/vp8enc -> VP8 payload descriptor -> pacer ->
// pion (NACK responder, TWCC, RTCP reports). Congestion control is pion's GCC estimator plus REMB and RTCP
// loss, driving the encoder bitrate, resolution and frame rate.
// Receive path: pion (NACK generator, TWCC feedback) -> reorder/jitter buffer -> VP8 depacketiser ->
// internal/media/vp8 -> FrameSink; Opus with a small adaptive jitter buffer and concealment -> AudioSink.
package rtcmedia

import (
	"errors"
	"io"
	"sync"

	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/cc"
	"github.com/pion/interceptor/pkg/gcc"
	"github.com/pion/interceptor/pkg/nack"
	"github.com/pion/interceptor/pkg/stats"
	"github.com/pion/interceptor/pkg/twcc"
	"github.com/pion/logging"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/sdp/v3"
	"github.com/pion/webrtc/v4"
)

// Header extension URIs offered in SDP.
const (
	ExtMid          = sdp.SDESMidURI
	ExtRID          = sdp.SDESRTPStreamIDURI
	ExtRepairedRID  = sdp.SDESRepairRTPStreamIDURI
	ExtTransportCC  = sdp.TransportCCURI
	ExtAbsSendTime  = "http://www.webrtc.org/experiments/rtp-hdrext/abs-send-time"
	ExtAudioLevel   = "urn:ietf:params:rtp-hdrext:ssrc-audio-level"
	ExtVideoOrient  = "urn:3gpp:video-orientation"
	mimeTelephoneEv = "audio/telephone-event"
)

// Payload types offered (the numbers Chrome uses for the same codecs).
const (
	PTVP8   = 96
	PTRTX   = 97
	PTOpus  = 111
	PTTelEv = 126
)

// InitialBitrate is where the congestion controller starts before feedback arrives.
const InitialBitrate = 1_000_000

// API is a pion API configured for browser-style calls.
type API struct {
	*webrtc.API
	// Estimator receives the peer's bandwidth estimator when the PeerConnection is created.
	Estimator <-chan cc.BandwidthEstimator
	// Stats gives the per-SSRC RTP/RTCP statistics of the peer (outbound and remote-inbound included,
	// which pion's own getStats does not report), once the PeerConnection exists.
	Stats <-chan stats.Getter
	// Retransmits counts what the NACK responder resent.
	Retransmits *RTXCounter
	me          *webrtc.MediaEngine
}

// Codecs returns the codecs the media engine offers for a kind, in preference order.
func Codecs(kind webrtc.RTPCodecType) []webrtc.RTPCodecParameters {
	switch kind {
	case webrtc.RTPCodecTypeAudio:
		return []webrtc.RTPCodecParameters{
			{RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2, SDPFmtpLine: "minptime=10;useinbandfec=1",
				RTCPFeedback: []webrtc.RTCPFeedback{{Type: webrtc.TypeRTCPFBTransportCC}}}, PayloadType: PTOpus},
			{RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: mimeTelephoneEv, ClockRate: 8000}, PayloadType: PTTelEv},
		}
	default:
		return []webrtc.RTPCodecParameters{
			{RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000, RTCPFeedback: []webrtc.RTCPFeedback{
				{Type: "goog-remb"}, {Type: webrtc.TypeRTCPFBTransportCC}, {Type: "ccm", Parameter: "fir"}, {Type: "nack"}, {Type: "nack", Parameter: "pli"}}}, PayloadType: PTVP8},
			{RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeRTX, ClockRate: 90000, SDPFmtpLine: "apt=96"}, PayloadType: PTRTX},
		}
	}
}

// NewAPI builds the media engine (VP8 + RTX, Opus + telephone-event, browser header extensions and
// feedback) and the interceptor chain (GCC congestion control, TWCC, NACK, RTCP reports, stats).
func NewAPI(se webrtc.SettingEngine, quiet bool) (*API, error) {
	me := &webrtc.MediaEngine{}
	for _, k := range []webrtc.RTPCodecType{webrtc.RTPCodecTypeAudio, webrtc.RTPCodecTypeVideo} {
		for _, c := range Codecs(k) {
			if err := me.RegisterCodec(c, k); err != nil {
				return nil, err
			}
		}
	}

	for _, ext := range []struct {
		uri  string
		kind webrtc.RTPCodecType
	}{
		{ExtMid, webrtc.RTPCodecTypeAudio}, {ExtMid, webrtc.RTPCodecTypeVideo},
		{ExtRID, webrtc.RTPCodecTypeVideo}, {ExtRepairedRID, webrtc.RTPCodecTypeVideo},
		{ExtAbsSendTime, webrtc.RTPCodecTypeAudio}, {ExtAbsSendTime, webrtc.RTPCodecTypeVideo},
		{ExtAudioLevel, webrtc.RTPCodecTypeAudio},
		{ExtVideoOrient, webrtc.RTPCodecTypeVideo},
	} {
		if err := me.RegisterHeaderExtension(webrtc.RTPHeaderExtensionCapability{URI: ext.uri}, ext.kind); err != nil {
			return nil, err
		}
	}

	reg := &interceptor.Registry{}
	lf := logging.LoggerFactory(&logging.DefaultLoggerFactory{Writer: io.Discard, DefaultLogLevel: logging.LogLevelDisabled})
	_ = quiet

	// congestion control first: it must see the packets after the TWCC extension was added
	est := make(chan cc.BandwidthEstimator, 1)
	ccf, err := cc.NewInterceptor(func() (cc.BandwidthEstimator, error) {
		return gcc.NewSendSideBWE(
			gcc.SendSideBWEInitialBitrate(InitialBitrate),
			gcc.SendSideBWEMinBitrate(30_000),
			gcc.SendSideBWEMaxBitrate(6_000_000),
			gcc.SendSideBWEPacer(&passPacer{writers: map[uint32]interceptor.RTPWriter{}}), // pacing is done by the senders, which know frame boundaries
			gcc.WithLoggerFactory(lf),
		)
	})
	if err != nil {
		return nil, err
	}
	ccf.OnNewPeerConnection(func(_ string, e cc.BandwidthEstimator) {
		select {
		case est <- e:
		default:
		}
	})
	reg.Add(ccf)
	if err := webrtc.ConfigureTWCCHeaderExtensionSender(me, reg); err != nil {
		return nil, err
	}
	rtxc := NewRTXCounter()
	reg.Add(rtxc)
	// NACK: the responder is pion's; requests are generated by the receivers (videorecv.go), which know
	// what they still need and when to give up and ask for a key frame instead
	responder, err := nack.NewResponderInterceptor(nack.ResponderSize(1024), nack.WithResponderLoggerFactory(lf))
	if err != nil {
		return nil, err
	}
	reg.Add(responder)
	if err := webrtc.ConfigureRTCPReports(reg); err != nil {
		return nil, err
	}
	if err := webrtc.ConfigureStatsInterceptor(reg); err != nil {
		return nil, err
	}
	// a second statistics interceptor of our own, because pion keeps its getter private and its report has
	// no outbound-rtp or remote-inbound-rtp entries
	sg := make(chan stats.Getter, 1)
	sf, err := stats.NewInterceptor(stats.WithLoggerFactory(lf))
	if err != nil {
		return nil, err
	}
	sf.OnNewPeerConnection(func(_ string, g stats.Getter) {
		select {
		case sg <- g:
		default:
		}
	})
	reg.Add(sf)
	// transport-wide feedback for what we receive (the sender side is in the header extension interceptor above)
	twccGen, err := twcc.NewSenderInterceptor(twcc.WithLoggerFactory(lf))
	if err != nil {
		return nil, err
	}
	reg.Add(twccGen)
	api := webrtc.NewAPI(webrtc.WithSettingEngine(se), webrtc.WithMediaEngine(me), webrtc.WithInterceptorRegistry(reg))
	if api == nil {
		return nil, errors.New("rtcmedia: no API")
	}
	return &API{API: api, Estimator: est, Stats: sg, Retransmits: rtxc, me: me}, nil
}

// RunRTCP reads the RTCP of an RTPSender until it ends and passes it to h (nil: just drain it, which
// the interceptors need to see feedback). Run it in a goroutine.
func RunRTCP(rs *webrtc.RTPSender, h func([]rtcp.Packet)) {
	for {
		pkts, _, err := rs.ReadRTCP()
		if err != nil {
			return
		}
		if h != nil {
			h(pkts)
		}
	}
}

// HeaderExtensions lists the header extension URIs offered for a kind.
func HeaderExtensions(kind webrtc.RTPCodecType) []string {
	if kind == webrtc.RTPCodecTypeAudio {
		return []string{ExtMid, ExtAbsSendTime, ExtAudioLevel, ExtTransportCC}
	}
	return []string{ExtMid, ExtRID, ExtRepairedRID, ExtAbsSendTime, ExtTransportCC, ExtVideoOrient}
}

// passPacer is a gcc.Pacer that sends at once (the senders pace themselves). Unlike gcc's own no-op pacer
// it also forwards packets of streams it was not told about, which is what the NACK responder's
// retransmissions on the RTX SSRC are: they take the writer of a registered stream, which writes any SSRC.
type passPacer struct {
	mu      sync.Mutex
	writers map[uint32]interceptor.RTPWriter
	any     interceptor.RTPWriter
}

func (p *passPacer) AddStream(ssrc uint32, w interceptor.RTPWriter) {
	p.mu.Lock()
	p.writers[ssrc] = w
	if p.any == nil {
		p.any = w
	}
	p.mu.Unlock()
}

func (p *passPacer) SetTargetBitrate(int) {}
func (p *passPacer) Close() error         { return nil }

func (p *passPacer) Write(h *rtp.Header, payload []byte, a interceptor.Attributes) (int, error) {
	p.mu.Lock()
	w := p.writers[h.SSRC]
	if w == nil {
		w = p.any
	}
	p.mu.Unlock()
	if w == nil {
		return 0, errors.New("rtcmedia: no stream to write to")
	}
	return w.Write(h, payload, a)
}
