package rtcmedia

import (
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/interceptor/pkg/cc"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"

	"github.com/sweefbrowser/sweef/internal/capture"
	"github.com/sweefbrowser/sweef/internal/media/vp8enc"
)

// SendParams mirrors the fields of RTCRtpEncodingParameters that matter to a single-layer sender.
type SendParams struct {
	Active       bool
	MaxBitrate   int     // bits per second; 0 means the default ceiling
	MaxFramerate float64 // frames per second; 0 means the source rate
	ScaleDownBy  float64 // >= 1
}

// DefaultSendParams is an active, unconstrained encoding.
func DefaultSendParams() SendParams { return SendParams{Active: true, ScaleDownBy: 1} }

const (
	defaultMaxBitrate = 2_500_000
	minVideoBitrate   = 40_000
	maxRTPPayload     = 1180 // keeps packets plus header extensions and SRTP overhead under a 1280 byte MTU
)

// resolution tiers: pixel count and the bitrate from which that size is used (16:9 reference)
var videoTiers = []struct{ px, bps int }{
	{1280 * 720, 900_000},
	{960 * 540, 600_000},
	{640 * 360, 300_000},
	{480 * 270, 170_000},
	{320 * 180, 85_000},
	{240 * 136, 45_000},
	{160 * 90, 0},
}

// VideoSendStats is the sender's contribution to getStats().
type VideoSendStats struct {
	FramesEncoded, KeyFramesEncoded, FramesDroppedByEncoder uint64
	FramesCaptured                                          uint64
	TotalEncodeTime                                         float64
	TotalEncodedBytes                                       uint64
	FrameWidth, FrameHeight                                 int
	FramesPerSecond                                         float64
	TargetBitrate                                           float64
	QualityLimitationReason                                 string
	QualityLimitationDurations                              map[string]float64
	QualityLimitationResolutionChanges                      uint32
	PLICount, FIRCount, NACKCount                           uint32
	RTT                                                     float64
	FractionLost                                            float64
	Q                                                       int
	Active                                                  bool
	HugeFramesSent                                          uint64
	PacerQueueDelay                                         float64
	PacketsSent                                             uint64
	BytesSent                                               uint64
}

// VideoSender pulls frames from a capture.VideoSource, encodes them to VP8 at a rate the network
// allows and sends them as RTP through a LocalTrack.
type VideoSender struct {
	track *LocalTrack
	rs    *webrtc.RTPSender
	est   cc.BandwidthEstimator

	mu       sync.Mutex
	src      capture.VideoSource
	frames   <-chan *capture.Frame
	srcCons  capture.VideoConstraints
	enabled  bool
	params   SendParams
	srcW     int
	srcH     int
	lastW    int
	lastH    int
	closed   bool
	st       VideoSendStats
	limiting string
	limitAt  time.Time
	durs     map[string]float64
	tier     int // index into videoTiers currently used (0 = highest); -1 unlimited
	tierUp   time.Time
	cpuTier  int
	fps      fpsMeter
	lastEnc  time.Time

	wake    chan struct{}
	done    chan struct{}
	wg      sync.WaitGroup
	rwg     sync.WaitGroup // RTCP reader: ends when the transport closes
	keyReq  atomic.Bool
	lastKey time.Time
	remb    atomic.Int64
	lossBps atomic.Int64 // loss-based ceiling when no transport-cc feedback exists
	rttMs   atomic.Int64
	loss    atomic.Uint64 // fraction lost * 1e6

	enc     *vp8enc.Encoder
	scaler  Scaler
	scratch *capture.Frame
	black   *capture.Frame
	seq     uint16
	picID   uint16
	tsBase  uint32
	t0      time.Time
	nsent   uint64
	q       chan *paced
	qBytes  atomic.Int64
	target  atomic.Int64
	pacedTo atomic.Int64 // pacing rate

	// OnQuality is called (from the sender goroutine) when the quality limitation reason changes.
	OnQuality func(reason string)
}

type paced struct {
	hdr   rtp.Header
	data  []byte
	at    time.Time
	mid   bool
	frame bool
}

// NewVideoSender starts the send machinery for a track. rs is the RTPSender the track is attached to
// (its RTCP is read here); est may be nil.
func NewVideoSender(track *LocalTrack, rs *webrtc.RTPSender, est cc.BandwidthEstimator) *VideoSender {
	s := &VideoSender{
		track: track, rs: rs, est: est,
		enabled: true, params: DefaultSendParams(),
		wake: make(chan struct{}, 1), done: make(chan struct{}),
		durs: map[string]float64{"none": 0, "cpu": 0, "bandwidth": 0, "other": 0},
		q:    make(chan *paced, 1500),
		t0:   time.Now(), tier: -1, limiting: "none", limitAt: time.Now(),
	}
	s.tsBase = randUint32()
	s.seq = uint16(randUint32())
	s.picID = uint16(randUint32()) & 0x7fff
	s.lossBps.Store(InitialBitrate)
	s.target.Store(InitialBitrate)
	s.wg.Add(2)
	go func() { defer s.wg.Done(); s.run() }()
	go func() { defer s.wg.Done(); s.pace() }()
	s.rwg.Add(1)
	go func() { defer s.rwg.Done(); s.readRTCP() }()
	return s
}

// SetSource attaches (or with nil detaches) the media source. The previous source is stopped.
func (s *VideoSender) SetSource(src capture.VideoSource, c capture.VideoConstraints) error {
	var frames <-chan *capture.Frame
	if src != nil {
		var err error
		if frames, err = src.Start(c); err != nil {
			return err
		}
	}
	s.mu.Lock()
	old := s.src
	s.src, s.frames, s.srcCons = src, frames, c
	s.mu.Unlock()
	if old != nil {
		_ = old.Stop()
	}
	s.kick()
	s.keyReq.Store(true)
	return nil
}

// SetEnabled implements MediaStreamTrack.enabled: a disabled track sends black frames.
func (s *VideoSender) SetEnabled(on bool) {
	s.mu.Lock()
	changed := s.enabled != on
	s.enabled = on
	s.mu.Unlock()
	if changed {
		s.keyReq.Store(true)
		s.kick()
	}
}

// SetParams applies RTCRtpSender.setParameters values.
func (s *VideoSender) SetParams(p SendParams) {
	if p.ScaleDownBy < 1 {
		p.ScaleDownBy = 1
	}
	s.mu.Lock()
	s.params = p
	s.mu.Unlock()
	s.kick()
}

// Params returns the current parameters.
func (s *VideoSender) Params() SendParams {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.params
}

// RequestKeyFrame makes the next encoded frame a key frame (what a PLI or FIR does).
func (s *VideoSender) RequestKeyFrame() { s.keyReq.Store(true); s.kick() }

func (s *VideoSender) kick() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Close stops the sender; it does not close the source's owner, only stops the source.
func (s *VideoSender) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	src := s.src
	s.src, s.frames = nil, nil
	close(s.done)
	s.mu.Unlock()
	if src != nil {
		_ = src.Stop()
	}
	s.wg.Wait()
}

// Wait blocks until the RTCP reader has ended (after the peer connection was closed).
func (s *VideoSender) Wait() { s.rwg.Wait() }

// ---- main loop ----------------------------------------------------------------------------

func (s *VideoSender) run() {
	stat := time.NewTicker(250 * time.Millisecond)
	defer stat.Stop()
	blackT := time.NewTicker(200 * time.Millisecond)
	defer blackT.Stop()
	for {
		s.mu.Lock()
		frames := s.frames
		s.mu.Unlock()
		select {
		case <-s.done:
			return
		case <-s.wake:
		case f, ok := <-frames:
			if !ok {
				s.mu.Lock()
				if s.frames == frames {
					s.frames = nil
				}
				s.mu.Unlock()
				continue
			}
			s.handle(f)
		case <-stat.C:
			s.control()
		case <-blackT.C:
			s.mu.Lock()
			need := !s.enabled && s.src != nil && s.params.Active
			s.mu.Unlock()
			if need {
				s.sendBlack()
			}
		}
	}
}

func (s *VideoSender) sendBlack() {
	s.mu.Lock()
	w, h := s.srcW, s.srcH
	s.mu.Unlock()
	if w == 0 {
		w, h = 320, 180
	}
	if s.black == nil || s.black.W != w || s.black.H != h {
		s.black = NewFrame(w, h)
		FillBlack(s.black)
	}
	s.encodeSend(s.black, time.Now())
}

func (s *VideoSender) handle(f *capture.Frame) {
	now := time.Now()
	s.mu.Lock()
	enabled, p := s.enabled, s.params
	s.srcW, s.srcH = f.W, f.H
	s.st.FramesCaptured++
	last := s.st.FramesEncoded
	_ = last
	s.mu.Unlock()
	if !p.Active || !enabled {
		return
	}
	// frame rate limit
	maxFPS := p.MaxFramerate
	if cf := s.cpuFPS(); cf > 0 && (maxFPS == 0 || cf < maxFPS) {
		maxFPS = cf
	}
	if maxFPS > 0 && !s.lastEnc.IsZero() && now.Sub(s.lastEnc) < time.Duration(float64(time.Second)/maxFPS*0.85) {
		return
	}
	s.encodeSend(f, now)
}

// cpuFPS is the frame rate cap imposed by encoder load.
func (s *VideoSender) cpuFPS() float64 {
	switch s.cpuTier {
	case 1:
		return 20
	case 2, 3:
		return 15
	}
	return 0
}

// targetDims chooses the encode size for a source size from the bitrate, scaleResolutionDownBy and CPU load.
func (s *VideoSender) targetDims(sw, sh int, p SendParams) (int, int) {
	w, h := sw, sh
	if p.ScaleDownBy > 1 {
		w = int(float64(w) / p.ScaleDownBy)
		h = int(float64(h) / p.ScaleDownBy)
	}
	px := w * h
	if s.tier >= 0 && s.tier < len(videoTiers) {
		if t := videoTiers[s.tier].px; px > t {
			px = t
		}
	}
	if s.cpuTier >= 3 && px > 640*360 {
		px = 640 * 360
	}
	if px < w*h {
		f := math.Sqrt(float64(px) / float64(w*h))
		w, h = int(float64(w)*f), int(float64(h)*f)
	}
	w, h = w&^1, h&^1
	if w < 16 {
		w = 16
	}
	if h < 16 {
		h = 16
	}
	return w, h
}

// bitrateTarget combines the congestion estimate, REMB, the loss fallback and the application ceiling.
func (s *VideoSender) bitrateTarget(p SendParams) int {
	n := s.track.Negotiated()
	target := int(s.lossBps.Load())
	if s.est != nil && n.TWCC {
		target = s.est.GetTargetBitrate()
	}
	if n.REMB && !n.TWCC {
		if r := int(s.remb.Load()); r > 0 && r < target {
			target = r
		}
	}
	ceil := defaultMaxBitrate
	if p.MaxBitrate > 0 {
		ceil = p.MaxBitrate
	}
	if target > ceil {
		target = ceil
	}
	if target < minVideoBitrate {
		target = minVideoBitrate
	}
	return target
}

func (s *VideoSender) encodeSend(f *capture.Frame, now time.Time) {
	s.mu.Lock()
	p := s.params
	s.mu.Unlock()
	if f.W < 16 || f.H < 16 {
		return
	}
	tw, th := s.targetDims(f.W, f.H, p)
	img := f
	if tw != f.W || th != f.H {
		if s.scratch == nil || s.scratch.W != tw || s.scratch.H != th {
			s.scratch = NewFrame(tw, th)
		}
		s.scaler.Scale(s.scratch, f)
		img = s.scratch
	}
	target := s.bitrateTarget(p)
	s.target.Store(int64(target))
	s.pacedTo.Store(int64(float64(target) * 2.5))
	fps := s.fps.rate()
	if fps < 5 || fps > 60 {
		fps = 30
	}
	if s.enc == nil {
		enc, err := vp8enc.New(vp8enc.Config{Width: img.W, Height: img.H, FrameRate: fps, Bitrate: target, Speed: -1, MaxQ: 124})
		if err != nil {
			return
		}
		s.enc = enc
	} else if w, h := s.enc.Size(); w != img.W || h != img.H {
		if err := s.enc.Resize(img.W, img.H); err != nil {
			return
		}
		s.mu.Lock()
		s.st.QualityLimitationResolutionChanges++
		s.mu.Unlock()
	}
	s.enc.SetBitrate(target)
	s.enc.SetFrameRate(fps)
	if s.keyReq.Swap(false) && now.Sub(s.lastKey) > 150*time.Millisecond {
		s.enc.RequestKeyFrame()
		s.lastKey = now
	}
	// queue backlog: if the pacer is more than ~600 ms behind, skip this frame
	if rate := s.pacedTo.Load(); rate > 0 && float64(s.qBytes.Load()*8)/float64(rate) > 0.6 {
		s.mu.Lock()
		s.st.FramesDroppedByEncoder++
		s.mu.Unlock()
		s.keyReq.Store(true)
		return
	}
	t0 := time.Now()
	pk, err := s.enc.Encode(&vp8enc.Image{W: img.W, H: img.H, Y: img.Y, U: img.U, V: img.V, YStride: img.YStride, UVStride: img.UVStride})
	enc := time.Since(t0)
	s.lastEnc = now
	s.fps.add(now)
	if err != nil || pk == nil {
		s.mu.Lock()
		s.st.FramesDroppedByEncoder++
		s.mu.Unlock()
		return
	}
	ts := s.tsBase + uint32(now.Sub(s.t0).Seconds()*90000)
	payloads := vp8Packetize(pk.Data, pk.PartStarts, s.picID, false, maxRTPPayload)
	s.picID = (s.picID + 1) & 0x7fff
	if len(payloads) == 0 {
		return
	}
	for i, pl := range payloads {
		h := rtp.Header{Version: 2, SequenceNumber: s.seq, Timestamp: ts, Marker: i == len(payloads)-1}
		s.seq++
		pp := &paced{hdr: h, data: pl, at: now, mid: pk.Key || s.nsent < 30 || s.nsent%200 == 0, frame: i == 0}
		s.nsent++
		s.qBytes.Add(int64(len(pl)))
		select {
		case s.q <- pp:
		case <-s.done:
			return
		}
	}
	s.mu.Lock()
	s.st.FramesEncoded++
	if pk.Key {
		s.st.KeyFramesEncoded++
	}
	s.st.TotalEncodeTime += enc.Seconds()
	s.st.TotalEncodedBytes += uint64(len(pk.Data))
	s.st.FrameWidth, s.st.FrameHeight = img.W, img.H
	s.st.Q = pk.Q
	if len(pk.Data) > 20000 {
		s.st.HugeFramesSent++
	}
	s.mu.Unlock()
}

// ---- pacing ------------------------------------------------------------------------------------

func (s *VideoSender) pace() {
	next := time.Now()
	for {
		select {
		case <-s.done:
			return
		case p := <-s.q:
			rate := float64(s.pacedTo.Load())
			if rate < 300_000 {
				rate = 300_000
			}
			if d := time.Until(next); d > 1500*time.Microsecond {
				t := time.NewTimer(d)
				select {
				case <-t.C:
				case <-s.done:
					t.Stop()
					return
				}
			}
			now := time.Now()
			n, _ := s.track.Write(&p.hdr, p.data, WriteOpts{WithMid: p.mid})
			s.qBytes.Add(-int64(len(p.data)))
			s.mu.Lock()
			s.st.PacketsSent++
			s.st.BytesSent += uint64(n)
			s.st.PacerQueueDelay = now.Sub(p.at).Seconds()
			s.mu.Unlock()
			if next.Before(now) {
				next = now
			}
			next = next.Add(time.Duration(float64(len(p.data)+40) * 8 / rate * float64(time.Second)))
		}
	}
}

// ---- feedback ----------------------------------------------------------------------------------

func (s *VideoSender) readRTCP() {
	for {
		pkts, _, err := s.rs.ReadRTCP()
		if err != nil {
			return
		}
		for _, p := range pkts {
			switch r := p.(type) {
			case *rtcp.PictureLossIndication:
				s.mu.Lock()
				s.st.PLICount++
				s.mu.Unlock()
				s.RequestKeyFrame()
			case *rtcp.FullIntraRequest:
				s.mu.Lock()
				s.st.FIRCount++
				s.mu.Unlock()
				s.RequestKeyFrame()
			case *rtcp.ReceiverEstimatedMaximumBitrate:
				s.remb.Store(int64(r.Bitrate))
			case *rtcp.TransportLayerNack:
				s.mu.Lock()
				for _, n := range r.Nacks {
					s.st.NACKCount += uint32(len(n.PacketList()))
				}
				s.mu.Unlock()
			case *rtcp.ReceiverReport:
				for _, rr := range r.Reports {
					if rr.SSRC != s.track.Negotiated().SSRC {
						continue
					}
					s.onReport(rr)
				}
			}
		}
	}
}

func (s *VideoSender) onReport(rr rtcp.ReceptionReport) {
	frac := float64(rr.FractionLost) / 256
	s.loss.Store(uint64(frac * 1e6))
	if rr.LastSenderReport != 0 {
		now := ntpMid32(time.Now())
		rtt := now - rr.LastSenderReport - rr.Delay
		if rtt < 0x80000000 {
			s.rttMs.Store(int64(float64(rtt) / 65536 * 1000))
		}
	}
	// loss-based fallback ceiling, used when the remote gives no transport-wide feedback
	cur := float64(s.lossBps.Load())
	switch {
	case frac > 0.10:
		cur *= 1 - 0.5*frac
	case frac < 0.02:
		cur *= 1.06
	}
	cur = math.Max(float64(minVideoBitrate), math.Min(cur, 6_000_000))
	s.lossBps.Store(int64(cur))
}

// control runs every 250 ms: resolution tier, CPU limits, quality limitation bookkeeping.
func (s *VideoSender) control() {
	s.mu.Lock()
	p := s.params
	hasSrc := s.src != nil
	s.mu.Unlock()
	if !hasSrc || !p.Active {
		return
	}
	now := time.Now()
	target := s.bitrateTarget(p)
	// bandwidth ladder with hysteresis
	want := len(videoTiers) - 1
	for i, t := range videoTiers {
		if target >= t.bps {
			want = i
			break
		}
	}
	cur := s.tier
	if cur < 0 {
		cur = 0
	}
	switch {
	case want > cur: // need a smaller picture
		s.tier, s.tierUp = want, now
	case want < cur: // may use a bigger one once the estimate has been comfortably above its threshold for a while
		if target >= videoTiers[want].bps*5/4 {
			if s.tierUp.IsZero() {
				s.tierUp = now
			}
			if now.Sub(s.tierUp) > 3*time.Second {
				s.tier = cur - 1
				s.tierUp = time.Time{}
			}
		} else {
			s.tierUp = time.Time{}
		}
	default:
		s.tierUp = time.Time{}
	}
	// CPU: look at the encoder's own timing
	if s.enc != nil {
		st := s.enc.Stats()
		budget := 1 / math.Max(s.fps.rate(), 10)
		if st.Frames > 20 && st.Speed == 0 {
			avg := st.EncodeTime.Seconds() / float64(st.Frames)
			if avg > 0.9*budget && s.cpuTier < 3 {
				s.cpuTier++
			}
		}
	}
	// limitation reason for stats: only a picture or frame rate actually reduced counts
	reason := "none"
	s.mu.Lock()
	sw, sh := s.srcW, s.srcH
	s.mu.Unlock()
	tw, th := s.targetDims(sw, sh, p)
	switch {
	case sw*sh > 0 && tw*th < sw*sh:
		switch {
		case s.cpuTier >= 3:
			reason = "cpu"
		case s.tier >= 0 && videoTiers[s.tier].px < int(float64(sw*sh)/(p.ScaleDownBy*p.ScaleDownBy)):
			reason = "bandwidth"
		default:
			reason = "other"
		}
	case s.cpuTier > 0:
		reason = "cpu"
	}
	s.mu.Lock()
	if reason != s.limiting {
		s.durs[s.limiting] += now.Sub(s.limitAt).Seconds()
		s.limiting, s.limitAt = reason, now
		if s.OnQuality != nil {
			go s.OnQuality(reason)
		}
	}
	s.st.TargetBitrate = float64(target)
	s.mu.Unlock()
}

// Stats returns a snapshot of the counters.
func (s *VideoSender) Stats() VideoSendStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.st
	st.QualityLimitationReason = s.limiting
	st.QualityLimitationDurations = map[string]float64{}
	for k, v := range s.durs {
		st.QualityLimitationDurations[k] = v
	}
	st.QualityLimitationDurations[s.limiting] += time.Since(s.limitAt).Seconds()
	st.FramesPerSecond = s.fps.rate()
	st.RTT = float64(s.rttMs.Load()) / 1000
	st.FractionLost = float64(s.loss.Load()) / 1e6
	st.Active = s.params.Active
	return st
}

// Target returns the current target bitrate in bits per second.
func (s *VideoSender) Target() int { return int(s.target.Load()) }

// Source returns the active source (nil when none).
func (s *VideoSender) Source() capture.VideoSource {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.src
}

// ---- helpers -----------------------------------------------------------------------------------

// fpsMeter measures the frame rate over the last second or so.
type fpsMeter struct {
	mu sync.Mutex
	ts [64]time.Time
	n  int
}

func (m *fpsMeter) add(t time.Time) {
	m.mu.Lock()
	m.ts[m.n%len(m.ts)] = t
	m.n++
	m.mu.Unlock()
}

func (m *fpsMeter) rate() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.n < 2 {
		return 0
	}
	cnt := m.n
	if cnt > len(m.ts) {
		cnt = len(m.ts)
	}
	newest := m.ts[(m.n-1)%len(m.ts)]
	if time.Since(newest) > 2*time.Second {
		return 0
	}
	oldest := newest
	k := 0
	for i := 1; i < cnt; i++ {
		t := m.ts[(m.n-1-i)%len(m.ts)]
		if newest.Sub(t) > time.Second {
			break
		}
		oldest = t
		k++
	}
	if k == 0 || newest.Sub(oldest) <= 0 {
		return 0
	}
	return float64(k) / newest.Sub(oldest).Seconds()
}

func ntpMid32(t time.Time) uint32 {
	secs := uint64(t.Unix()) + 2208988800
	frac := uint64(t.Nanosecond()) * (1 << 32) / 1e9
	return uint32((secs&0xffff)<<16 | (frac >> 16))
}
