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

	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/cc"
	"github.com/pion/interceptor/pkg/gcc"
	"github.com/pion/interceptor/pkg/nack"
	"github.com/pion/logging"
	"github.com/pion/sdp/v3"
	"github.com/pion/webrtc/v4"
	"time"
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
const InitialBitrate = 700_000

// API is a pion API configured for browser-style calls.
type API struct {
	*webrtc.API
	// Estimator receives the peer's bandwidth estimator when the PeerConnection is created.
	Estimator <-chan cc.BandwidthEstimator
	me        *webrtc.MediaEngine
}

// Codecs returns the codecs the media engine offers for a kind, in preference order.
func Codecs(kind webrtc.RTPCodecType) []webrtc.RTPCodecParameters {
	switch kind {
	case webrtc.RTPCodecTypeAudio:
		return []webrtc.RTPCodecParameters{
			{RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2, SDPFmtpLine: "minptime=10;useinbandfec=1"}, PayloadType: PTOpus},
			{RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: mimeTelephoneEv, ClockRate: 8000}, PayloadType: PTTelEv},
		}
	default:
		return []webrtc.RTPCodecParameters{
			{RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000}, PayloadType: PTVP8},
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
	// feedback: nack + nack pli (Configure below), transport-cc (Configure below), plus remb and fir
	me.RegisterFeedback(webrtc.RTCPFeedback{Type: "goog-remb"}, webrtc.RTPCodecTypeVideo)
	me.RegisterFeedback(webrtc.RTCPFeedback{Type: "ccm", Parameter: "fir"}, webrtc.RTPCodecTypeVideo)

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
			gcc.SendSideBWEPacer(gcc.NewNoOpPacer()), // pacing is done by the senders, which know frame boundaries
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
	if err := webrtc.ConfigureNackWithOptions(me, reg,
		[]nack.GeneratorOption{nack.GeneratorInterval(30 * time.Millisecond), nack.GeneratorSize(1024), nack.GeneratorSkipLastN(2), nack.WithGeneratorLoggerFactory(lf)},
		nack.ResponderSize(1024), nack.WithResponderLoggerFactory(lf)); err != nil {
		return nil, err
	}
	if err := webrtc.ConfigureRTCPReports(reg); err != nil {
		return nil, err
	}
	if err := webrtc.ConfigureStatsInterceptor(reg); err != nil {
		return nil, err
	}
	if err := webrtc.ConfigureTWCCSender(me, reg); err != nil {
		return nil, err
	}
	api := webrtc.NewAPI(webrtc.WithSettingEngine(se), webrtc.WithMediaEngine(me), webrtc.WithInterceptorRegistry(reg))
	if api == nil {
		return nil, errors.New("rtcmedia: no API")
	}
	return &API{API: api, Estimator: est, me: me}, nil
}
