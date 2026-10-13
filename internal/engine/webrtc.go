package engine

// webrtc.go: RTCPeerConnection on top of github.com/pion/webrtc/v4 (pure Go).
//
// Layout: webrtc_prelude.js implements the page-facing classes (events,
// promises, validation, RTCRtpTransceiver bookkeeping); this file owns the
// peer connections. Host calls are synchronous and return JSON strings; pion's
// asynchronous callbacks (candidates, state changes, data channel traffic) are
// pushed onto the page's realtime queue (kind "rtc") and reach JavaScript on
// the page thread via __swRtcEvent, exactly like WebSocket frames. docs/webrtc.md
// has the supported surface and the privacy rules.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/pion/ice/v4"
	"github.com/pion/interceptor/pkg/cc"
	"github.com/pion/interceptor/pkg/stats"
	"github.com/pion/logging"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/rtcerr"

	"github.com/sweefbrowser/sweef/internal/rtcmedia"
)

//go:embed webrtc_prelude.js
var webrtcPreludeJS string

func init() {
	registerPrelude("webrtc", webrtcPreludeJS)
	registerHostExtension("webrtc", func(j *jsVM) map[string]hostFn { return newRTCHost(j).fns() })
	registerAsyncHandler("rtc", func(j *jsVM, e asyncEvent) {
		if _, err := j.call("__swRtcEvent", e.id, e.typ, e.data); err != nil {
			j.tab.LogConsole("error", jsErrorText(err, "webrtc "+e.typ))
		}
		j.jobs() // a microtask checkpoint after each event task: awaiting code continues before the next event
	})
	switch strings.ToLower(os.Getenv("SWEEF_WEBRTC_HOST_CANDIDATES")) {
	case "raw", "allow":
		rtcHostMode.Store(int32(WebRTCHostRaw))
	case "none", "off":
		rtcHostMode.Store(int32(WebRTCHostNone))
	}
	if v := strings.ToLower(os.Getenv("SWEEF_WEBRTC")); v == "off" || v == "0" || v == "disabled" {
		rtcEnabled.Store(false)
	}
	if v := strings.ToLower(os.Getenv("SWEEF_WEBRTC_ALLOW_WITH_PROXY")); v == "1" || v == "on" || v == "true" {
		rtcAllowProxy.Store(true)
	}
}

// WebRTCHostCandidates says how a page may learn the machine's own addresses
// through ICE host candidates.
type WebRTCHostCandidates int32

const (
	// WebRTCHostMDNS (default) gathers host candidates but publishes them under
	// a random <uuid>.local name resolved by multicast DNS, as Chrome and Firefox
	// do. When the platform cannot run mDNS the host candidates are dropped, never
	// published as raw addresses.
	WebRTCHostMDNS WebRTCHostCandidates = iota
	// WebRTCHostNone publishes no host candidate at all: connections need a
	// server reflexive or relayed candidate (iceServers configured by the page).
	WebRTCHostNone
	// WebRTCHostRaw publishes raw local addresses. This leaks the local IP to the
	// page and to every peer it talks to; it exists for diagnostics and tests.
	WebRTCHostRaw
)

var (
	rtcHostMode   atomic.Int32 // WebRTCHostCandidates
	rtcEnabled    atomic.Bool
	rtcAllowProxy atomic.Bool
	// rtcTestLoopback restricts raw host candidates to the loopback interface
	// (tests: no dependence on the machine's network).
	rtcTestLoopback atomic.Bool
	rtcLive         atomic.Int64 // open peer connections, process wide (tests, diagnostics)
)

func init() { rtcEnabled.Store(true) }

// SetWebRTCHostCandidates sets the host candidate policy for peer connections
// created from now on. Also settable with SWEEF_WEBRTC_HOST_CANDIDATES=mdns|none|raw.
func SetWebRTCHostCandidates(m WebRTCHostCandidates) { rtcHostMode.Store(int32(m)) }

// WebRTCHostCandidatePolicy returns the current policy.
func WebRTCHostCandidatePolicy() WebRTCHostCandidates {
	return WebRTCHostCandidates(rtcHostMode.Load())
}

// SetWebRTCEnabled turns the RTCPeerConnection API on or off for pages loaded
// from now on (SWEEF_WEBRTC=off at start-up). Off removes the constructors, as
// in browsers that ship without WebRTC.
func SetWebRTCEnabled(on bool) { rtcEnabled.Store(on) }

// SetWebRTCAllowWithProxy lets peer connections be created while an HTTP or SOCKS
// proxy is configured. WebRTC is UDP and cannot use the proxy, so by default the
// constructor throws NotSupportedError instead of silently bypassing it.
func SetWebRTCAllowWithProxy(on bool) { rtcAllowProxy.Store(on) }

// ---- candidate and SDP sanitising -----------------------------------------------

// rtcSanitizeCandidate applies the host candidate policy to one candidate line
// (with or without the "a=" prefix). ok=false means the candidate must not be
// shown to the page. Server reflexive and relayed candidates keep their public
// address (the page configured the server) but never the private related address.
func rtcSanitizeCandidate(line string, mode WebRTCHostCandidates) (string, bool) {
	if mode == WebRTCHostRaw {
		return line, true
	}
	f := strings.Fields(line)
	typIdx := -1
	for i, w := range f {
		if w == "typ" && i+1 < len(f) {
			typIdx = i
			break
		}
	}
	if typIdx < 0 || len(f) < 8 {
		return "", false // unparseable: never let it through
	}
	addr := f[4]
	switch f[typIdx+1] {
	case "host":
		if mode == WebRTCHostNone || !strings.HasSuffix(addr, ".local") {
			return "", false
		}
	default:
		for i := typIdx + 2; i+1 < len(f); i++ {
			switch f[i] {
			case "raddr":
				f[i+1] = "0.0.0.0"
			case "rport":
				f[i+1] = "0"
			}
		}
	}
	return strings.Join(f, " "), true
}

// rtcSanitizeSDP removes candidates the policy forbids from a session description.
func rtcSanitizeSDP(sdp string, mode WebRTCHostCandidates) string {
	if mode == WebRTCHostRaw || !strings.Contains(sdp, "a=candidate:") {
		return sdp
	}
	lines := strings.Split(sdp, "\r\n")
	out := lines[:0:0]
	for _, l := range lines {
		if strings.HasPrefix(l, "a=candidate:") {
			s, ok := rtcSanitizeCandidate(l, mode)
			if !ok {
				continue
			}
			l = s
		}
		out = append(out, l)
	}
	return strings.Join(out, "\r\n")
}

var (
	ipv6Once sync.Once
	ipv6Ok   bool
)

func rtcHasIPv6() bool {
	ipv6Once.Do(func() {
		c, err := net.ListenPacket("udp6", "[::1]:0")
		if err == nil {
			c.Close()
			ipv6Ok = true
		}
	})
	return ipv6Ok
}

// ---- host ---------------------------------------------------------------------------

type rtcHost struct {
	j     *jsVM
	mu    sync.Mutex
	peers map[int64]*rtcPeer
	certs map[int64]*webrtc.Certificate
	next  int64
	reg   sync.Once
}

func newRTCHost(j *jsVM) *rtcHost {
	return &rtcHost{j: j, peers: map[int64]*rtcPeer{}, certs: map[int64]*webrtc.Certificate{}}
}

func (h *rtcHost) closeAll() {
	h.mu.Lock()
	ps := make([]*rtcPeer, 0, len(h.peers))
	for _, p := range h.peers {
		ps = append(ps, p)
	}
	h.peers = map[int64]*rtcPeer{}
	h.mu.Unlock()
	for _, p := range ps {
		p.close()
	}
}

func (h *rtcHost) peer(a []any) (*rtcPeer, error) {
	h.mu.Lock()
	p := h.peers[argI(a, 0)]
	h.mu.Unlock()
	if p == nil {
		return nil, errors.New("InvalidStateError: the peer connection is closed")
	}
	return p, nil
}

type rtcDC struct {
	id int64
	d  *webrtc.DataChannel
}

type rtcTR struct {
	id int64
	t  *webrtc.RTPTransceiver

	// send side (smu): the local track stays bound to the sender for its whole life, so replaceTrack only
	// swaps the source behind the pipeline
	smu       sync.Mutex
	local     *rtcmedia.LocalTrack
	vs        *rtcmedia.VideoSender
	as        *rtcmedia.AudioSender
	desc      RTCTrackDesc
	params    rtcmedia.SendParams
	live      atomic.Bool // a live track is attached and the sender is active
	encActive atomic.Bool // RTCRtpEncodingParameters.active
	rtcpH     atomic.Pointer[func([]rtcp.Packet)]
	drained   sync.Once

	// receive side (rmu)
	rmu     sync.Mutex
	vr      *rtcmedia.VideoReceiver
	ar      *rtcmedia.AudioReceiver
	nodes   map[int64]*rtcBitmap
	audioOn bool
}

type rtcPeer struct {
	h       *rtcHost
	id      int64
	pc      *webrtc.PeerConnection
	mode    WebRTCHostCandidates
	mu      sync.Mutex
	dcs     map[int64]*rtcDC
	nextDC  int64
	trs     []*rtcTR
	nextTR  int64
	lastOff string // raw offer/answer as pion produced them (pion requires the exact text back)
	lastAns string
	closed  atomic.Bool
	done    chan struct{}
	wg      sync.WaitGroup
	est     cc.BandwidthEstimator
	sg      stats.Getter
	rtx     *rtcmedia.RTXCounter
}

func (p *rtcPeer) emit(typ string, v any) {
	if p.closed.Load() {
		return
	}
	var data string
	if v != nil {
		b, _ := json.Marshal(v)
		data = string(b)
	}
	p.h.j.rt().push(asyncEvent{kind: "rtc", id: p.id, typ: typ, data: data})
	if t := p.h.j.tab; t != nil {
		t.wake() // a window gives the page's event loop a slice, as for finished images
	}
}

func rtcErr(err error) error {
	if err == nil {
		return nil
	}
	var (
		is  *rtcerr.InvalidStateError
		ia  *rtcerr.InvalidAccessError
		ns  *rtcerr.NotSupportedError
		im  *rtcerr.InvalidModificationError
		sy  *rtcerr.SyntaxError
		ty  *rtcerr.TypeError
		op  *rtcerr.OperationError
		rng *rtcerr.RangeError
	)
	msg := err.Error()
	switch {
	case errors.As(err, &is):
		return errors.New("InvalidStateError: " + msg)
	case errors.As(err, &ia):
		return errors.New("InvalidAccessError: " + msg)
	case errors.As(err, &ns):
		return errors.New("NotSupportedError: " + msg)
	case errors.As(err, &im):
		if strings.Contains(msg, "signaling state") {
			return errors.New("InvalidStateError: " + msg) // a description of the wrong type for this signalingState
		}
		return errors.New("InvalidModificationError: " + msg)
	case errors.As(err, &sy):
		return errors.New("SyntaxError: " + msg)
	case errors.As(err, &ty):
		return errors.New("TypeError: " + msg)
	case errors.As(err, &rng):
		return errors.New("RangeError: " + msg)
	case errors.As(err, &op):
		return errors.New("OperationError: " + msg)
	}
	return errors.New("OperationError: " + msg)
}

func jsonOut(v any) (any, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

type rtcICEServerJS struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username"`
	Credential string   `json:"credential"`
}

type rtcConfigJS struct {
	ICEServers         []rtcICEServerJS `json:"iceServers"`
	ICETransportPolicy string           `json:"iceTransportPolicy"`
	BundlePolicy       string           `json:"bundlePolicy"`
	RTCPMuxPolicy      string           `json:"rtcpMuxPolicy"`
	PoolSize           int              `json:"iceCandidatePoolSize"`
	Certificates       []int64          `json:"certificates"`
}

func (h *rtcHost) config(c rtcConfigJS) (webrtc.Configuration, error) {
	cfg := webrtc.Configuration{ICEServers: []webrtc.ICEServer{}}
	for _, s := range c.ICEServers {
		cfg.ICEServers = append(cfg.ICEServers, webrtc.ICEServer{URLs: s.URLs, Username: s.Username, Credential: s.Credential})
	}
	switch c.ICETransportPolicy {
	case "relay":
		cfg.ICETransportPolicy = webrtc.ICETransportPolicyRelay
	default:
		cfg.ICETransportPolicy = webrtc.ICETransportPolicyAll
	}
	switch c.BundlePolicy {
	case "max-compat":
		cfg.BundlePolicy = webrtc.BundlePolicyMaxCompat
	case "max-bundle":
		cfg.BundlePolicy = webrtc.BundlePolicyMaxBundle
	default:
		cfg.BundlePolicy = webrtc.BundlePolicyBalanced
	}
	cfg.RTCPMuxPolicy = webrtc.RTCPMuxPolicyRequire
	cfg.ICECandidatePoolSize = uint8(min(c.PoolSize, 255)) //nolint:gosec // bounded above
	h.mu.Lock()
	for _, id := range c.Certificates {
		cert := h.certs[id]
		if cert == nil {
			h.mu.Unlock()
			return cfg, errors.New("InvalidAccessError: unknown certificate")
		}
		cfg.Certificates = append(cfg.Certificates, *cert)
	}
	h.mu.Unlock()
	return cfg, nil
}

func (h *rtcHost) newAPI(mode WebRTCHostCandidates) (*rtcmedia.API, error) {
	se := webrtc.SettingEngine{}
	se.LoggerFactory = &logging.DefaultLoggerFactory{Writer: io.Discard, DefaultLogLevel: logging.LogLevelDisabled}
	if os.Getenv("SWEEF_WEBRTC_DEBUG") != "" { // pion's own log to stderr, for diagnosing connectivity
		lf := logging.NewDefaultLoggerFactory()
		lf.DefaultLogLevel = logging.LogLevelDebug
		se.LoggerFactory = lf
	}
	switch mode {
	case WebRTCHostMDNS:
		se.SetICEMulticastDNSMode(ice.MulticastDNSModeQueryAndGather)
	case WebRTCHostNone:
		se.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
	default:
		se.SetICEMulticastDNSMode(ice.MulticastDNSModeQueryOnly) // resolve the peer's .local names only
	}
	se.DisableActiveTCP(true)
	if !rtcHasIPv6() {
		// pion turns mDNS off altogether when the IPv6 multicast socket cannot be opened
		se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4, webrtc.NetworkTypeTCP4})
	}
	if rtcTestLoopback.Load() {
		se.SetIncludeLoopbackCandidate(true)
		se.SetInterfaceFilter(func(name string) bool { return strings.HasPrefix(name, "lo") })
	}
	var w *wireCounters
	if t := h.j.tab; t != nil {
		if c := t.Context(); c != nil {
			w = &c.data().wire
		}
	}
	n, err := newRTCNet(w)
	if err != nil {
		return nil, err
	}
	se.SetNet(n)
	return rtcmedia.NewAPI(se, true)
}

func (h *rtcHost) fns() map[string]hostFn {
	f := map[string]hostFn{}
	f["rtcEnabled"] = func(a []any) (any, error) { return rtcEnabled.Load(), nil }
	f["rtcNew"] = h.hostNew
	f["rtcClose"] = func(a []any) (any, error) {
		id := argI(a, 0)
		h.mu.Lock()
		p := h.peers[id]
		delete(h.peers, id)
		h.mu.Unlock()
		if p != nil {
			p.close()
		}
		return nil, nil
	}
	f["rtcSetConfig"] = func(a []any) (any, error) {
		p, err := h.peer(a)
		if err != nil {
			return nil, err
		}
		var c rtcConfigJS
		if err := json.Unmarshal([]byte(argS(a, 1)), &c); err != nil {
			return nil, errors.New("TypeError: " + err.Error())
		}
		cfg, err := h.config(c)
		if err != nil {
			return nil, err
		}
		return nil, rtcErr(p.pc.SetConfiguration(cfg))
	}
	f["rtcTicket"] = func(a []any) (any, error) {
		p, err := h.peer(a)
		if err != nil {
			return nil, err
		}
		p.emit("ticket", argI(a, 1)) // answered after every event queued so far: promises resolve after the events they cause
		return nil, nil
	}
	f["rtcCreateOffer"] = func(a []any) (any, error) {
		p, err := h.peer(a)
		if err != nil {
			return nil, err
		}
		var o struct {
			ICERestart bool `json:"iceRestart"`
		}
		_ = json.Unmarshal([]byte(argS(a, 1)), &o)
		d, err := p.pc.CreateOffer(&webrtc.OfferOptions{ICERestart: o.ICERestart})
		if err != nil {
			return nil, rtcErr(err)
		}
		p.mu.Lock()
		p.lastOff = d.SDP
		p.mu.Unlock()
		return rtcSanitizeSDP(d.SDP, p.mode), nil
	}
	f["rtcCreateAnswer"] = func(a []any) (any, error) {
		p, err := h.peer(a)
		if err != nil {
			return nil, err
		}
		d, err := p.pc.CreateAnswer(nil)
		if err != nil {
			return nil, rtcErr(err)
		}
		p.mu.Lock()
		p.lastAns = d.SDP
		p.mu.Unlock()
		return rtcSanitizeSDP(d.SDP, p.mode), nil
	}
	f["rtcSetLocal"] = func(a []any) (any, error) {
		p, err := h.peer(a)
		if err != nil {
			return nil, err
		}
		typ, sdp := argS(a, 1), argS(a, 2)
		return nil, p.setDescription(true, typ, sdp)
	}
	f["rtcSetRemote"] = func(a []any) (any, error) {
		p, err := h.peer(a)
		if err != nil {
			return nil, err
		}
		typ, sdp := argS(a, 1), argS(a, 2)
		return nil, p.setDescription(false, typ, sdp)
	}
	f["rtcAddIce"] = func(a []any) (any, error) {
		p, err := h.peer(a)
		if err != nil {
			return nil, err
		}
		var ci webrtc.ICECandidateInit
		if err := json.Unmarshal([]byte(argS(a, 1)), &ci); err != nil {
			return nil, errors.New("TypeError: " + err.Error())
		}
		return nil, rtcErr(p.pc.AddICECandidate(ci))
	}
	f["rtcState"] = func(a []any) (any, error) {
		p, err := h.peer(a)
		if err != nil {
			return nil, err
		}
		return jsonOut(p.state())
	}
	f["rtcTransceivers"] = func(a []any) (any, error) {
		p, err := h.peer(a)
		if err != nil {
			return nil, err
		}
		return jsonOut(p.snapshot())
	}
	f["rtcAddTrack"] = func(a []any) (any, error) {
		p, err := h.peer(a)
		if err != nil {
			return nil, err
		}
		return p.addTrack(argS(a, 1), argS(a, 2), argS(a, 3))
	}
	f["rtcAddTransceiver"] = func(a []any) (any, error) {
		p, err := h.peer(a)
		if err != nil {
			return nil, err
		}
		return p.addTransceiver(argS(a, 1), argS(a, 2), argS(a, 3), argS(a, 4))
	}
	f["rtcSetSender"] = func(a []any) (any, error) {
		p, err := h.peer(a)
		if err != nil {
			return nil, err
		}
		return nil, p.setSender(argI(a, 1), argS(a, 2), argS(a, 3), argS(a, 4), argB(a, 5))
	}
	f["rtcSenderLive"] = func(a []any) (any, error) {
		p, err := h.peer(a)
		if err != nil {
			return nil, err
		}
		if x := p.tr(argI(a, 1)); x != nil {
			x.setLive(argB(a, 2))
		}
		return nil, nil
	}
	f["rtcTrackStopped"] = func(a []any) (any, error) {
		p, err := h.peer(a)
		if err != nil {
			return nil, nil
		}
		if x := p.tr(argI(a, 1)); x != nil {
			x.live.Store(false)
			p.detachSource(x) // closes our capture source: the camera is released
		}
		return nil, nil
	}
	f["rtcStates"] = func(a []any) (any, error) {
		p, err := h.peer(a)
		if err != nil {
			return nil, err
		}
		pc := p.pc
		return pc.SignalingState().String() + "|" + pc.ICEGatheringState().String() + "|" + pc.ICEConnectionState().String() + "|" + pc.ConnectionState().String(), nil
	}
	f["rtcRemoveTrack"] = func(a []any) (any, error) {
		p, err := h.peer(a)
		if err != nil {
			return nil, err
		}
		return nil, p.removeTrack(argI(a, 1))
	}
	f["rtcSetDirection"] = func(a []any) (any, error) {
		p, err := h.peer(a)
		if err != nil {
			return nil, err
		}
		return nil, p.setDirection(argI(a, 1), argS(a, 2))
	}
	f["rtcStopTransceiver"] = func(a []any) (any, error) {
		p, err := h.peer(a)
		if err != nil {
			return nil, err
		}
		tr := p.tr(argI(a, 1))
		if tr == nil {
			return nil, nil
		}
		tr.live.Store(false)
		err = rtcErr(tr.t.Stop())
		tr.closeSender()
		tr.closeReceiver()
		return nil, err
	}
	f["rtcCreateDC"] = func(a []any) (any, error) {
		p, err := h.peer(a)
		if err != nil {
			return nil, err
		}
		return p.createDC(argS(a, 1), argS(a, 2))
	}
	f["rtcDCSend"] = func(a []any) (any, error) {
		p, err := h.peer(a)
		if err != nil {
			return nil, err
		}
		d := p.dc(argI(a, 1))
		if d == nil {
			return nil, errors.New("InvalidStateError: RTCDataChannel.readyState is not 'open'")
		}
		if argB(a, 3) {
			b, err := base64.StdEncoding.DecodeString(argS(a, 2))
			if err != nil {
				return nil, errors.New("TypeError: bad binary payload")
			}
			if err := d.d.Send(b); err != nil {
				return nil, rtcSendErr(err)
			}
			return nil, nil
		}
		if err := d.d.SendText(argS(a, 2)); err != nil {
			return nil, rtcSendErr(err)
		}
		return nil, nil
	}
	f["rtcDCClose"] = func(a []any) (any, error) {
		p, err := h.peer(a)
		if err != nil {
			return nil, err
		}
		if d := p.dc(argI(a, 1)); d != nil {
			_ = d.d.Close()
		}
		return nil, nil
	}
	f["rtcDCInfo"] = func(a []any) (any, error) {
		p, err := h.peer(a)
		if err != nil {
			return nil, err
		}
		d := p.dc(argI(a, 1))
		if d == nil {
			return jsonOut(map[string]any{"state": "closed", "buffered": 0})
		}
		return jsonOut(map[string]any{"state": d.d.ReadyState().String(), "buffered": d.d.BufferedAmount(), "low": d.d.BufferedAmountLowThreshold(), "id": dcID(d.d)})
	}
	f["rtcDCThreshold"] = func(a []any) (any, error) {
		p, err := h.peer(a)
		if err != nil {
			return nil, err
		}
		if d := p.dc(argI(a, 1)); d != nil {
			d.d.SetBufferedAmountLowThreshold(uint64(max(argI(a, 2), 0)))
		}
		return nil, nil
	}
	f["rtcStats"] = func(a []any) (any, error) {
		p, err := h.peer(a)
		if err != nil {
			return nil, err
		}
		return p.stats()
	}
	f["rtcTransport"] = func(a []any) (any, error) {
		p, err := h.peer(a)
		if err != nil {
			return nil, err
		}
		return jsonOut(p.transport())
	}
	f["rtcGenCert"] = h.hostGenCert
	h.mediaFns(f)
	return f
}

func rtcSendErr(err error) error {
	if errors.Is(err, io.ErrClosedPipe) || strings.Contains(err.Error(), "not open") {
		return errors.New("InvalidStateError: RTCDataChannel.readyState is not 'open'")
	}
	if strings.Contains(err.Error(), "larger than") || strings.Contains(err.Error(), "too large") {
		return errors.New("TypeError: " + err.Error())
	}
	return errors.New("OperationError: " + err.Error())
}

func dcID(d *webrtc.DataChannel) any {
	if id := d.ID(); id != nil {
		return int(*id)
	}
	return nil
}

func (h *rtcHost) hostGenCert(a []any) (any, error) {
	var key any
	var err error
	switch argS(a, 0) {
	case "RSA":
		bits := int(argI(a, 1))
		if bits < 1024 || bits > 4096 {
			bits = 2048
		}
		key, err = rsa.GenerateKey(rand.Reader, bits)
	default:
		key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	}
	if err != nil {
		return nil, errors.New("OperationError: " + err.Error())
	}
	cert, err := webrtc.GenerateCertificate(key)
	if err != nil {
		return nil, errors.New("OperationError: " + err.Error())
	}
	h.mu.Lock()
	h.next++
	id := h.next
	h.certs[id] = cert
	h.mu.Unlock()
	fps := []map[string]string{}
	if fp, err := cert.GetFingerprints(); err == nil {
		for _, x := range fp {
			fps = append(fps, map[string]string{"algorithm": x.Algorithm, "value": x.Value})
		}
	}
	return jsonOut(map[string]any{"id": id, "expires": cert.Expires().UnixMilli(), "fingerprints": fps})
}

func (h *rtcHost) hostNew(a []any) (any, error) {
	if !rtcEnabled.Load() {
		return nil, errors.New("NotSupportedError: WebRTC is disabled")
	}
	t := h.j.tab
	if t != nil && t.eng != nil && !rtcAllowProxy.Load() && t.eng.proxyFor(t.Context()) != "" {
		return nil, errors.New("NotSupportedError: WebRTC is UDP and cannot go through the configured proxy")
	}
	var c rtcConfigJS
	if err := json.Unmarshal([]byte(argS(a, 0)), &c); err != nil {
		return nil, errors.New("TypeError: " + err.Error())
	}
	cfg, err := h.config(c)
	if err != nil {
		return nil, err
	}
	if t != nil && t.IsOfflineMode() {
		cfg.ICEServers = nil // offline emulation: no STUN/TURN traffic, local candidates only
	}
	mode := WebRTCHostCandidatePolicy()
	api, err := h.newAPI(mode)
	if err != nil {
		return nil, errors.New("OperationError: " + err.Error())
	}
	pc, err := api.NewPeerConnection(cfg)
	if err != nil {
		return nil, rtcErr(err)
	}
	r := h.j.rt()
	h.reg.Do(func() { r.addCloser(h.closeAll) })
	h.mu.Lock()
	id := r.id()
	p := &rtcPeer{h: h, id: id, pc: pc, mode: mode, dcs: map[int64]*rtcDC{}, done: make(chan struct{})}
	select {
	case p.est = <-api.Estimator:
	default:
	}
	select {
	case p.sg = <-api.Stats:
	default:
	}
	p.rtx = api.Retransmits
	h.peers[id] = p
	h.mu.Unlock()
	rtcLive.Add(1)
	p.wire()
	return id, nil
}

func (p *rtcPeer) wire() {
	pc := p.pc
	pc.OnSignalingStateChange(func(s webrtc.SignalingState) { p.emit("signalingstatechange", s.String()) })
	pc.OnICEGatheringStateChange(func(s webrtc.ICEGatheringState) { p.emit("icegatheringstatechange", s.String()) })
	pc.OnICEConnectionStateChange(func(s webrtc.ICEConnectionState) { p.emit("iceconnectionstatechange", s.String()) })
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) { p.emit("connectionstatechange", s.String()) })
	pc.OnNegotiationNeeded(func() { p.emit("negotiationneeded", nil) })
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			p.emit("icecandidate", nil)
			return
		}
		init := c.ToJSON()
		line, ok := rtcSanitizeCandidate(init.Candidate, p.mode)
		if !ok {
			return
		}
		mid, idx := p.firstMid()
		p.emit("icecandidate", map[string]any{"candidate": line, "sdpMid": mid, "sdpMLineIndex": idx, "usernameFragment": derefStr(init.UsernameFragment)})
	})
	pc.OnTrack(p.onRemoteTrack)
	pc.OnDataChannel(func(d *webrtc.DataChannel) {
		p.mu.Lock()
		p.nextDC++
		id := p.nextDC
		p.dcs[id] = &rtcDC{id: id, d: d}
		p.mu.Unlock()
		p.emit("datachannel", p.dcInfo(id, d))
		p.wireDC(id, d)
	})
	if s := pc.SCTP(); s != nil {
		if t := s.Transport(); t != nil {
			t.OnStateChange(func(st webrtc.DTLSTransportState) { p.emit("dtlsstate", st.String()) })
			if it := t.ICETransport(); it != nil {
				it.OnConnectionStateChange(func(st webrtc.ICETransportState) { p.emit("icetstate", st.String()) })
				it.OnSelectedCandidatePairChange(func(*webrtc.ICECandidatePair) { p.emit("icetpair", nil) })
			}
		}
	}
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (p *rtcPeer) firstMid() (any, int) {
	for _, d := range []*webrtc.SessionDescription{p.pc.LocalDescription()} {
		if d == nil {
			continue
		}
		if pd, err := d.Unmarshal(); err == nil && len(pd.MediaDescriptions) > 0 {
			if mid, ok := pd.MediaDescriptions[0].Attribute("mid"); ok {
				return mid, 0
			}
		}
	}
	return "0", 0
}

func (p *rtcPeer) close() {
	if !p.closed.CompareAndSwap(false, true) {
		return
	}
	close(p.done)
	_ = p.pc.Close()
	p.mu.Lock()
	trs := append([]*rtcTR(nil), p.trs...)
	p.mu.Unlock()
	for _, x := range trs {
		x.closeSender()
		x.closeReceiver()
	}
	p.wg.Wait()
	rtcLive.Add(-1)
}

func (p *rtcPeer) dc(id int64) *rtcDC {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dcs[id]
}

func (p *rtcPeer) dcInfo(id int64, d *webrtc.DataChannel) map[string]any {
	m := map[string]any{"dc": id, "label": d.Label(), "ordered": d.Ordered(), "protocol": d.Protocol(), "negotiated": d.Negotiated(), "id": dcID(d)}
	if v := d.MaxPacketLifeTime(); v != nil {
		m["maxPacketLifeTime"] = int(*v)
	}
	if v := d.MaxRetransmits(); v != nil {
		m["maxRetransmits"] = int(*v)
	}
	return m
}

func (p *rtcPeer) wireDC(id int64, d *webrtc.DataChannel) {
	d.OnOpen(func() { p.emit("dcopen", map[string]any{"dc": id, "id": dcID(d)}) })
	d.OnClose(func() { p.emit("dcclose", map[string]any{"dc": id}) })
	d.OnError(func(err error) { p.emit("dcerror", map[string]any{"dc": id, "message": err.Error()}) })
	d.OnBufferedAmountLow(func() { p.emit("dcbufferedlow", map[string]any{"dc": id}) })
	d.OnMessage(func(m webrtc.DataChannelMessage) {
		if m.IsString {
			p.emit("dcmessage", map[string]any{"dc": id, "text": string(m.Data)})
			return
		}
		p.emit("dcmessage", map[string]any{"dc": id, "bin": base64.StdEncoding.EncodeToString(m.Data)})
	})
}

func (p *rtcPeer) createDC(label, optsJSON string) (any, error) {
	var o struct {
		Ordered           *bool   `json:"ordered"`
		MaxPacketLifeTime *uint16 `json:"maxPacketLifeTime"`
		MaxRetransmits    *uint16 `json:"maxRetransmits"`
		Protocol          string  `json:"protocol"`
		Negotiated        bool    `json:"negotiated"`
		ID                *uint16 `json:"id"`
	}
	if err := json.Unmarshal([]byte(optsJSON), &o); err != nil {
		return nil, errors.New("TypeError: " + err.Error())
	}
	init := &webrtc.DataChannelInit{Ordered: o.Ordered, MaxPacketLifeTime: o.MaxPacketLifeTime, MaxRetransmits: o.MaxRetransmits}
	if o.Protocol != "" {
		init.Protocol = &o.Protocol
	}
	if o.Negotiated {
		t := true
		init.Negotiated = &t
		init.ID = o.ID
	}
	d, err := p.pc.CreateDataChannel(label, init)
	if err != nil {
		return nil, rtcErr(err)
	}
	p.mu.Lock()
	p.nextDC++
	id := p.nextDC
	p.dcs[id] = &rtcDC{id: id, d: d}
	p.mu.Unlock()
	p.wireDC(id, d)
	return jsonOut(p.dcInfo(id, d))
}

// setDescription applies a description given by the page. Our own offers and
// answers are matched back to pion's raw text (the page only ever saw the
// sanitised form); anything else is passed through and pion rejects munging.
func (p *rtcPeer) setDescription(local bool, typ, sdp string) error {
	st := webrtc.NewSDPType(typ)
	var err error
	if st == webrtc.SDPType(0) {
		return errors.New("TypeError: bad description type")
	}
	d := webrtc.SessionDescription{Type: st, SDP: sdp}
	if st == webrtc.SDPTypeRollback {
		return p.rollback(local)
	} else if local {
		p.mu.Lock()
		raw := map[webrtc.SDPType]string{webrtc.SDPTypeOffer: p.lastOff, webrtc.SDPTypeAnswer: p.lastAns, webrtc.SDPTypePranswer: p.lastAns}[st]
		p.mu.Unlock()
		if raw != "" && (sdp == raw || sdp == rtcSanitizeSDP(raw, p.mode)) {
			d.SDP = raw
		}
	}
	if local {
		err = p.pc.SetLocalDescription(d)
	} else {
		err = p.pc.SetRemoteDescription(d)
	}
	return rtcErr(err)
}

// rollback abandons the pending local or remote description. pion v4 accepts the
// rollback type but has no valid transition for it, so the signaling state and
// the pending description are reset directly (TestWebRTCRollbackAndImplicitDescriptions
// fails if a pion upgrade renames the fields). Transceivers that a rolled back
// remote offer created stay in the connection; they are reused by the next offer.
func (p *rtcPeer) rollback(local bool) error {
	st := p.pc.SignalingState()
	if local && st != webrtc.SignalingStateHaveLocalOffer && st != webrtc.SignalingStateHaveLocalPranswer ||
		!local && st != webrtc.SignalingStateHaveRemoteOffer && st != webrtc.SignalingStateHaveRemotePranswer {
		return errors.New("InvalidStateError: nothing to roll back in signalingState " + st.String())
	}
	v := reflect.ValueOf(p.pc).Elem()
	sig, pend := v.FieldByName("signalingState"), v.FieldByName("pendingLocalDescription")
	if !local {
		pend = v.FieldByName("pendingRemoteDescription")
	}
	if !sig.IsValid() || !pend.IsValid() || sig.Type() != reflect.TypeOf(webrtc.SignalingState(0)) || pend.Type() != reflect.TypeOf((*webrtc.SessionDescription)(nil)) {
		return errors.New("NotSupportedError: rollback is not supported")
	}
	reflect.NewAt(pend.Type(), unsafe.Pointer(pend.UnsafeAddr())).Elem().Set(reflect.Zero(pend.Type())) //nolint:gosec // see above
	(*webrtc.SignalingState)(unsafe.Pointer(sig.UnsafeAddr())).Set(webrtc.SignalingStateStable)         //nolint:gosec
	p.emit("signalingstatechange", "stable")
	return nil
}

type rtcDesc struct {
	Type string `json:"type"`
	SDP  string `json:"sdp"`
}

func (p *rtcPeer) desc(d *webrtc.SessionDescription) *rtcDesc {
	if d == nil {
		return nil
	}
	return &rtcDesc{Type: d.Type.String(), SDP: rtcSanitizeSDP(d.SDP, p.mode)}
}

func (p *rtcPeer) state() map[string]any {
	pc := p.pc
	return map[string]any{
		"signaling":     pc.SignalingState().String(),
		"gathering":     pc.ICEGatheringState().String(),
		"iceConnection": pc.ICEConnectionState().String(),
		"connection":    pc.ConnectionState().String(),
		"local":         p.desc(pc.LocalDescription()),
		"currentLocal":  p.desc(pc.CurrentLocalDescription()),
		"pendingLocal":  p.desc(pc.PendingLocalDescription()),
		"remote":        p.desc(pc.RemoteDescription()),
		"currentRemote": p.desc(pc.CurrentRemoteDescription()),
		"pendingRemote": p.desc(pc.PendingRemoteDescription()),
		"trickle":       pc.CanTrickleICECandidates() == webrtc.ICETrickleCapabilitySupported,
	}
}

// ---- transceivers ---------------------------------------------------------------------

func (p *rtcPeer) trFor(t *webrtc.RTPTransceiver) *rtcTR {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, x := range p.trs {
		if x.t == t {
			return x
		}
	}
	p.nextTR++
	x := &rtcTR{id: p.nextTR, t: t}
	x.encActive.Store(true)
	x.params = rtcmedia.DefaultSendParams()
	p.trs = append(p.trs, x)
	if s := t.Sender(); s != nil {
		p.drainSender(x, s)
	}
	return x
}

func (p *rtcPeer) tr(id int64) *rtcTR {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, x := range p.trs {
		if x.id == id {
			return x
		}
	}
	return nil
}

func (p *rtcPeer) snapshot() []map[string]any {
	out := []map[string]any{}
	for _, t := range p.pc.GetTransceivers() {
		x := p.trFor(t)
		out = append(out, map[string]any{"id": x.id, "mid": t.Mid(), "kind": t.Kind().String(), "direction": t.Direction().String()})
	}
	return out
}

func (p *rtcPeer) newLocal(x *rtcTR, kind, trackID, streamID string) *rtcmedia.LocalTrack {
	if trackID == "" {
		trackID = "track"
	}
	if streamID == "" {
		streamID = "-"
	}
	lt := rtcmedia.NewLocalTrack(kindOf(kind), trackID, streamID)
	lt.SetRetransmitLimiter(p.rtx)
	if x != nil {
		lt.SetMidFunc(x.t.Mid)
	}
	return lt
}

// drainSender reads the RTCP of a sender (the interceptors need that to see feedback) and hands it to the
// pipeline that is currently attached.
func (p *rtcPeer) drainSender(x *rtcTR, s *webrtc.RTPSender) {
	x.drained.Do(func() {
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			rtcmedia.RunRTCP(s, func(pkts []rtcp.Packet) {
				if h := x.rtcpH.Load(); h != nil {
					(*h)(pkts)
				}
			})
		}()
	})
}

func (p *rtcPeer) trOfSender(s *webrtc.RTPSender) *rtcTR {
	for _, t := range p.pc.GetTransceivers() {
		if t.Sender() == s {
			return p.trFor(t)
		}
	}
	return nil
}

func (p *rtcPeer) addTrack(kind, descJSON, streamID string) (any, error) {
	d := parseTrackDesc(descJSON)
	lt := p.newLocal(nil, kind, d.ID, streamID)
	s, err := p.pc.AddTrack(lt)
	if err != nil {
		return nil, rtcErr(err)
	}
	x := p.trOfSender(s)
	if x == nil {
		return nil, errors.New("OperationError: sender has no transceiver")
	}
	lt.SetMidFunc(x.t.Mid)
	lt.ReadyWhenConnected(s)
	x.smu.Lock()
	x.local = lt
	x.smu.Unlock()
	if err := p.ensurePipeline(x); err != nil {
		return nil, rtcErr(err)
	}
	p.bindSource(x, d)
	x.setLive(true)
	return jsonOut(map[string]any{"id": x.id, "all": p.snapshot()})
}

func (p *rtcPeer) addTransceiver(kind, dir, descJSON, streamID string) (any, error) {
	dd := webrtc.RTPTransceiverDirectionSendrecv
	switch dir {
	case "sendonly":
		dd = webrtc.RTPTransceiverDirectionSendonly
	case "recvonly":
		dd = webrtc.RTPTransceiverDirectionRecvonly
	case "inactive":
		dd = webrtc.RTPTransceiverDirectionInactive
	}
	init := webrtc.RTPTransceiverInit{Direction: dd}
	var t *webrtc.RTPTransceiver
	var err error
	var lt *rtcmedia.LocalTrack
	d := parseTrackDesc(descJSON)
	if d.ID != "" {
		lt = p.newLocal(nil, kind, d.ID, streamID)
		t, err = p.pc.AddTransceiverFromTrack(lt, init)
	} else {
		ck := webrtc.RTPCodecTypeVideo
		if kind == "audio" {
			ck = webrtc.RTPCodecTypeAudio
		}
		t, err = p.pc.AddTransceiverFromKind(ck, init)
	}
	if err != nil {
		return nil, rtcErr(err)
	}
	x := p.trFor(t)
	if lt != nil {
		lt.SetMidFunc(t.Mid)
		lt.ReadyWhenConnected(t.Sender())
		x.smu.Lock()
		x.local = lt
		x.smu.Unlock()
		if err := p.ensurePipeline(x); err != nil {
			return nil, rtcErr(err)
		}
		p.bindSource(x, d)
		x.setLive(true)
	}
	return jsonOut(map[string]any{"id": x.id, "all": p.snapshot()})
}

// setSender attaches (a track description) or detaches the page's track to a sender: replaceTrack.
func (p *rtcPeer) setSender(trID int64, kind, descJSON, streamID string, live bool) error {
	x := p.tr(trID)
	if x == nil {
		return errors.New("InvalidStateError: unknown transceiver")
	}
	s := x.t.Sender()
	if s == nil {
		return errors.New("InvalidStateError: no sender")
	}
	d := parseTrackDesc(descJSON)
	if d.ID == "" {
		x.setLive(false)
		p.detachSource(x)
		return nil
	}
	x.smu.Lock()
	have := x.local != nil
	x.smu.Unlock()
	if !have {
		lt := p.newLocal(x, kind, d.ID, streamID)
		lt.ReadyWhenConnected(s)
		if err := s.ReplaceTrack(lt); err != nil {
			return rtcErr(err)
		}
		x.smu.Lock()
		x.local = lt
		x.smu.Unlock()
	}
	if err := p.ensurePipeline(x); err != nil {
		return rtcErr(err)
	}
	p.bindSource(x, d)
	x.setLive(live)
	return nil
}

func (p *rtcPeer) removeTrack(trID int64) error {
	x := p.tr(trID)
	if x == nil {
		return nil
	}
	x.live.Store(false)
	var err error
	if s := x.t.Sender(); s != nil {
		err = rtcErr(p.pc.RemoveTrack(s))
	}
	x.closeSender()
	return err
}

// setDirection changes a transceiver's direction. pion v4 only accepts a
// direction when the transceiver is created and has no setter, so the field is
// written directly; TestWebRTCDirectionHook fails if a pion upgrade renames it.
func (p *rtcPeer) setDirection(trID int64, dir string) error {
	x := p.tr(trID)
	if x == nil {
		return errors.New("InvalidStateError: unknown transceiver")
	}
	var d webrtc.RTPTransceiverDirection
	switch dir {
	case "sendrecv":
		d = webrtc.RTPTransceiverDirectionSendrecv
	case "sendonly":
		d = webrtc.RTPTransceiverDirectionSendonly
	case "recvonly":
		d = webrtc.RTPTransceiverDirectionRecvonly
	case "inactive":
		d = webrtc.RTPTransceiverDirectionInactive
	default:
		return errors.New("TypeError: bad direction")
	}
	return rtcStoreDirection(x.t, d)
}

func rtcStoreDirection(t *webrtc.RTPTransceiver, d webrtc.RTPTransceiverDirection) error {
	f := reflect.ValueOf(t).Elem().FieldByName("direction")
	if !f.IsValid() || f.Type() != reflect.TypeOf(atomic.Value{}) {
		return errors.New("NotSupportedError: transceiver direction cannot be changed")
	}
	v := (*atomic.Value)(unsafe.Pointer(f.UnsafeAddr())) //nolint:gosec // see setDirection
	v.Store(d)
	return nil
}

// ---- stats and transports -----------------------------------------------------------

func (p *rtcPeer) stats() (any, error) {
	rep := p.pc.GetStats()
	b, err := json.Marshal(rep)
	if err != nil {
		return nil, errors.New("OperationError: " + err.Error())
	}
	var m map[string]map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, errors.New("OperationError: " + err.Error())
	}
	p.addMediaStats(m)
	for _, s := range m {
		if s["type"] == "local-candidate" {
			p.scrubCandidateStat(s)
		}
		if ip, ok := s["ip"]; ok {
			if _, has := s["address"]; !has {
				s["address"] = ip
			}
		}
	}
	return jsonOut(m)
}

// scrubCandidateStat hides raw local addresses in a local-candidate stat.
func (p *rtcPeer) scrubCandidateStat(s map[string]any) {
	if p.mode == WebRTCHostRaw {
		return
	}
	delete(s, "networkType")
	for _, k := range []string{"ip", "address"} {
		if v, ok := s[k].(string); ok && net.ParseIP(v) != nil && s["candidateType"] == "host" {
			s[k] = ""
		}
	}
	for _, k := range []string{"relatedAddress"} {
		if _, ok := s[k]; ok {
			s[k] = "0.0.0.0"
		}
	}
}

func (p *rtcPeer) candJSON(c *webrtc.ICECandidate) map[string]any {
	if c == nil {
		return nil
	}
	line, ok := rtcSanitizeCandidate(c.ToJSON().Candidate, p.mode)
	if !ok {
		return nil
	}
	return map[string]any{"candidate": line}
}

func (p *rtcPeer) transport() map[string]any {
	m := map[string]any{}
	s := p.pc.SCTP()
	if s == nil {
		return m
	}
	m["sctp"] = map[string]any{"state": s.State().String(), "maxMessageSize": s.GetCapabilities().MaxMessageSize, "maxChannels": s.MaxChannels()}
	t := s.Transport()
	if t == nil {
		return m
	}
	m["dtls"] = map[string]any{"state": t.State().String()}
	it := t.ICETransport()
	if it == nil {
		return m
	}
	ice := map[string]any{"state": it.State().String(), "role": it.Role().String(), "gathering": p.pc.ICEGatheringState().String()}
	if pair, err := it.GetSelectedCandidatePair(); err == nil && pair != nil {
		ice["local"] = p.candJSON(pair.Local)
		ice["remote"] = map[string]any{"candidate": pair.Remote.ToJSON().Candidate}
	}
	if lp, err := it.GetLocalParameters(); err == nil {
		ice["localParams"] = map[string]any{"usernameFragment": lp.UsernameFragment, "password": lp.Password}
	}
	if rp, err := it.GetRemoteParameters(); err == nil {
		ice["remoteParams"] = map[string]any{"usernameFragment": rp.UsernameFragment, "password": rp.Password}
	}
	m["ice"] = ice
	return m
}
