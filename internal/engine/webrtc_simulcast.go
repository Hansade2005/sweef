package engine

// webrtc_simulcast.go: sending several encodings of one video track (RTCRtpTransceiverInit.sendEncodings with
// rid, RTCRtpSender.setParameters per encoding). Each layer is a pion encoding of the sender with its own
// RTP stream id, SSRC, VP8 encoder (rtcmedia.VideoSender) and RTCP loop; one capture source is fanned out to all
// of them. The congestion estimate of the connection is shared: the allocator below gives the lowest layers
// their bitrate first and pauses a layer that would get less than it needs, as browsers do.

import (
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"

	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"

	"github.com/sweefbrowser/sweef/internal/capture"
	"github.com/sweefbrowser/sweef/internal/rtcmedia"
)

// rtcEnc is one RTCRtpEncodingParameters as the page set it.
type rtcEnc struct {
	RID          string   `json:"rid"`
	Active       *bool    `json:"active"`
	MaxBitrate   *float64 `json:"maxBitrate"`
	MaxFramerate *float64 `json:"maxFramerate"`
	Scale        *float64 `json:"scaleResolutionDownBy"`
}

func (e rtcEnc) active() bool { return e.Active == nil || *e.Active }

func (e rtcEnc) sendParams(live bool) rtcmedia.SendParams {
	sp := rtcmedia.DefaultSendParams()
	if e.MaxBitrate != nil {
		sp.MaxBitrate = int(*e.MaxBitrate)
	}
	if e.MaxFramerate != nil {
		sp.MaxFramerate = *e.MaxFramerate
	}
	if e.Scale != nil && *e.Scale >= 1 {
		sp.ScaleDownBy = *e.Scale
	}
	sp.Active = live && e.active()
	return sp
}

// rtcLayer is one simulcast layer beyond the first (the first is rtcTR.local / rtcTR.vs).
type rtcLayer struct {
	rid   string
	local *rtcmedia.LocalTrack
	vs    *rtcmedia.VideoSender
}

// simulcast reports whether the transceiver sends more than one encoding.
func (x *rtcTR) simulcast() bool { return len(x.encs) > 1 }

// videoLayers returns the video pipelines of the transceiver, the first layer first (smu held).
func (x *rtcTR) videoLayersLocked() []layerRef {
	var out []layerRef
	if x.vs != nil || x.local != nil {
		rid := ""
		if len(x.encs) > 0 {
			rid = x.encs[0].RID
		}
		out = append(out, layerRef{rid: rid, local: x.local, vs: x.vs})
	}
	for _, l := range x.extras {
		out = append(out, layerRef{rid: l.rid, local: l.local, vs: l.vs})
	}
	return out
}

type layerRef struct {
	rid   string
	local *rtcmedia.LocalTrack
	vs    *rtcmedia.VideoSender
}

func (x *rtcTR) videoLayers() []layerRef {
	x.smu.Lock()
	defer x.smu.Unlock()
	return x.videoLayersLocked()
}

// parseEncodings decodes the encodings JSON of the page. One object (the legacy form of rtcSetParams) is accepted.
func parseEncodings(s string) []rtcEnc {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var list []rtcEnc
	if s[0] == '{' {
		var e rtcEnc
		if json.Unmarshal([]byte(s), &e) == nil {
			return []rtcEnc{e}
		}
		return nil
	}
	_ = json.Unmarshal([]byte(s), &list)
	return list
}

// newSimulcastSender adds a video transceiver whose sender carries one layer per encoding.
func (p *rtcPeer) newSimulcastSender(init webrtc.RTPTransceiverInit, encs []rtcEnc, trackID, streamID string) (*webrtc.RTPTransceiver, []*rtcmedia.LocalTrack, error) {
	if trackID == "" {
		trackID = "track"
	}
	if streamID == "" {
		streamID = "-"
	}
	locals := make([]*rtcmedia.LocalTrack, len(encs))
	for i, e := range encs {
		lt := rtcmedia.NewLocalTrackRID(webrtc.RTPCodecTypeVideo, trackID, streamID, e.RID)
		lt.SetRetransmitLimiter(p.rtx)
		locals[i] = lt
	}
	t, err := p.pc.AddTransceiverFromTrack(locals[0], init)
	if err != nil {
		return nil, nil, rtcErr(err)
	}
	for _, lt := range locals[1:] {
		if err := t.Sender().AddEncoding(lt); err != nil {
			_ = p.pc.RemoveTrack(t.Sender())
			return nil, nil, rtcErr(err)
		}
	}
	return t, locals, nil
}

// attachSimulcast registers the layers on the transceiver (called once, right after creation).
func (p *rtcPeer) attachSimulcast(x *rtcTR, encs []rtcEnc, locals []*rtcmedia.LocalTrack) {
	s := x.t.Sender()
	x.smu.Lock()
	x.encs = encs
	x.local = locals[0]
	for i, lt := range locals {
		lt.SetMidFunc(x.t.Mid)
		lt.ReadyWhenConnected(s)
		if i > 0 {
			x.extras = append(x.extras, &rtcLayer{rid: encs[i].RID, local: lt})
		}
	}
	x.smu.Unlock()
}

// ensureExtraLayers creates the encoders of the layers beyond the first (smu held).
func (p *rtcPeer) ensureExtraLayersLocked(x *rtcTR) {
	s := x.t.Sender()
	for i, l := range x.extras {
		if l.vs != nil {
			continue
		}
		l.vs = rtcmedia.NewVideoSender(l.local, p.est)
		idx := i + 1
		x.layerShare(idx)
		vs, rid := l.vs, l.rid
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			rtcmedia.RunRTCPRID(s, rid, vs.HandleRTCP)
		}()
	}
	if x.vs != nil {
		x.layerShare(0)
	}
	x.applyEncodingsLocked()
}

// applyEncodingsLocked pushes the configured encodings to the layer senders (smu held).
func (x *rtcTR) applyEncodingsLocked() {
	live := x.live.Load()
	for i, l := range x.videoLayersLocked() {
		if l.vs == nil || i >= len(x.encs) {
			continue
		}
		sp := x.encs[i].sendParams(live)
		if x.simFallback && i > 0 {
			sp.Active = false
		}
		if t := x.peer.h.j.tab; t != nil && t.policy().Level == SaverStrict && (sp.MaxBitrate == 0 || sp.MaxBitrate > 300_000) {
			sp.MaxBitrate = 300_000
		}
		l.vs.SetParams(sp)
	}
}

// layerShare installs the allocator on layer idx (smu held).
func (x *rtcTR) layerShare(idx int) {
	if !x.simulcast() {
		return
	}
	layers := x.videoLayersLocked()
	if idx >= len(layers) || layers[idx].vs == nil {
		return
	}
	layers[idx].vs.SetShare(func(total int) int { return x.allocate(total, idx) })
}

// allocate divides the connection's bitrate over the active layers: the smallest picture first, each up to what
// it wants, and a layer that cannot get its minimum gets nothing.
func (x *rtcTR) allocate(total, idx int) int {
	x.smu.Lock()
	layers := x.videoLayersLocked()
	encs := append([]rtcEnc(nil), x.encs...)
	fallback := x.simFallback
	x.smu.Unlock()
	type cand struct {
		i              int
		px             int
		want, min, max int
	}
	var cs []cand
	for i, l := range layers {
		if l.vs == nil || i >= len(encs) {
			continue
		}
		e := encs[i]
		if !e.active() || (fallback && i > 0) {
			continue
		}
		w, h := l.vs.SourceSize()
		if w == 0 {
			w, h = 640, 360
		}
		scale := 1.0
		if e.Scale != nil && *e.Scale >= 1 {
			scale = *e.Scale
		}
		px := int(float64(w*h) / (scale * scale))
		want, minb := rtcmedia.LayerBitrates(px)
		if e.MaxBitrate != nil && *e.MaxBitrate > 0 {
			if int(*e.MaxBitrate) < want {
				want = int(*e.MaxBitrate)
			}
			if minb > want {
				minb = want
			}
		}
		cs = append(cs, cand{i: i, px: px, want: want, min: minb})
	}
	sort.SliceStable(cs, func(a, b int) bool { return cs[a].px < cs[b].px })
	left := total
	for _, c := range cs {
		give := 0
		if left >= c.min {
			give = c.want
			if give > left {
				give = left
			}
		}
		if c.i == idx {
			return give
		}
		left -= give
	}
	return 0
}

// setEncodings applies the page's encodings (rtcSetParams with an array).
func (p *rtcPeer) setEncodings(x *rtcTR, encs []rtcEnc) {
	x.smu.Lock()
	if len(encs) == len(x.encs) && len(encs) > 1 {
		// rid and layer count are fixed after creation; take the mutable fields
		for i := range encs {
			encs[i].RID = x.encs[i].RID
		}
		x.encs = encs
	}
	x.applyEncodingsLocked()
	x.smu.Unlock()
}

// simulcastNegotiated looks at the remote answer and, when it does not accept simulcast for a transceiver, sends
// the first layer only (what a browser does with a remote end that ignores the rid / simulcast lines).
func (p *rtcPeer) checkSimulcastAnswer(sdp string) {
	p.mu.Lock()
	trs := append([]*rtcTR(nil), p.trs...)
	p.mu.Unlock()
	for _, x := range trs {
		if !x.simulcast() {
			continue
		}
		mid := x.t.Mid()
		if mid == "" {
			continue
		}
		ok := sdpSectionAccepts(sdp, mid)
		x.smu.Lock()
		if x.simFallback == ok { // changed
			x.simFallback = !ok
			x.applyEncodingsLocked()
		}
		x.smu.Unlock()
	}
}

// sdpSectionAccepts reports whether the media section with this mid has a simulcast recv line.
func sdpSectionAccepts(sdp, mid string) bool {
	secs := strings.Split(sdp, "\nm=")
	for _, s := range secs[1:] {
		if !strings.Contains(s, "\na=mid:"+mid+"\r") && !strings.Contains(s, "\na=mid:"+mid+"\n") {
			continue
		}
		for _, ln := range strings.Split(s, "\n") {
			ln = strings.TrimSpace(ln)
			if strings.HasPrefix(ln, "a=simulcast:") && strings.Contains(ln, "recv") {
				return true
			}
		}
		return false
	}
	return true
}

// closeLayers tears down the extra layers (smu held by the caller of closeSender after it detached them).
func closeLayerPipelines(extras []*rtcLayer) {
	for _, l := range extras {
		if l.vs != nil {
			l.vs.Close()
		}
	}
}

var _ = errors.New
var _ = math.Abs
var _ capture.VideoSource
var _ rtcp.Packet
