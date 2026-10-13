package rtcmedia

import (
	"image"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"

	"github.com/sweefbrowser/sweef/internal/media/vp8"
)

// FrameSink receives decoded video frames at their playout time.
type FrameSink interface {
	// PushFrame delivers a frame. img is never modified afterwards, so a sink may keep it.
	PushFrame(img *image.YCbCr)
}

// VideoRecvStats is the receiver's contribution to getStats().
type VideoRecvStats struct {
	FramesReceived, FramesDecoded, FramesDropped, KeyFramesDecoded uint64
	TotalDecodeTime                                                float64
	FrameWidth, FrameHeight                                        int
	FramesPerSecond                                                float64
	JitterBufferDelay                                              float64
	JitterBufferEmittedCount                                       uint64
	JitterBufferTarget                                             float64
	FreezeCount                                                    uint32
	TotalFreezesDuration                                           float64
	PLICount, NACKCount, FIRCount                                  uint32
	PacketsDiscarded                                               uint64
	BytesReceived                                                  uint64
	PacketsReceived                                                uint64
	LastPacketTime                                                 time.Time
	Muted                                                          bool
	SSRC                                                           uint32
	LastRTPTimestamp                                               uint32
	DecodeErrors                                                   uint64
}

type vpkt struct {
	seq     uint32 // extended
	ts      uint32
	marker  bool
	start   bool // first packet of a frame (S=1, PID=0)
	payload []byte
	arrival time.Time
}

type vframe struct {
	first, last uint32
	ts          uint32
	key         bool
	data        []byte
	arrival     time.Time // arrival of the last packet
	firstArr    time.Time
}

type outFrame struct {
	img  *image.YCbCr
	at   time.Time
	arr  time.Time
	size int
}

// VideoReceiver reassembles, decodes and plays out a received VP8 stream.
type VideoReceiver struct {
	track     *webrtc.TrackRemote
	rcv       *webrtc.RTPReceiver
	writeRTCP func([]rtcp.Packet) error
	ssrc      uint32
	usePLI    bool

	mu    sync.Mutex
	sinks []FrameSink
	st    VideoRecvStats
	fps   fpsMeter

	// jitter buffer (guarded by mu)
	pkts       map[uint32]*vpkt
	ready      []*vframe
	highest    uint32
	haveSeq    bool
	delivered  uint32 // last sequence number of the last delivered frame
	haveRef    bool
	needKey    bool
	waitSince  time.Time
	lastPLI    time.Time
	lastKeyAt  time.Time
	lastPktAt  time.Time
	muted      bool
	deliverSig chan struct{}

	// playout timing
	havePlay  bool
	baseTr    float64
	prevTr    float64
	jitter    float64
	delay     float64
	firstTS   uint32
	lastTS    uint32
	tsExt     int64
	firstArr  time.Time
	outQ      []*outFrame
	outSig    chan struct{}
	lastShown time.Time
	freezing  bool
	lastW     int
	lastH     int
	firing    bool

	dec *vp8.Decoder

	done chan struct{}
	wg   sync.WaitGroup // decode, playout and monitor loops
	rwg  sync.WaitGroup // readers; they end when the transport closes
	once sync.Once

	// Callbacks (called from receiver goroutines; keep them quick).
	OnResize func(w, h int)
	OnMute   func(muted bool)
	OnFrame  func() // after a frame was pushed to the sinks
}

// NewVideoReceiver starts receiving the remote track. writeRTCP sends RTCP to the remote (PLI).
func NewVideoReceiver(track *webrtc.TrackRemote, rcv *webrtc.RTPReceiver, writeRTCP func([]rtcp.Packet) error) *VideoReceiver {
	r := &VideoReceiver{
		track: track, rcv: rcv, writeRTCP: writeRTCP, ssrc: uint32(track.SSRC()),
		pkts: map[uint32]*vpkt{}, dec: vp8.NewDecoder(), muted: true, needKey: true,
		deliverSig: make(chan struct{}, 1), outSig: make(chan struct{}, 1), done: make(chan struct{}),
		delay: 0.03,
	}
	r.st.SSRC = r.ssrc
	r.st.Muted = true
	r.usePLI = true
	for _, fb := range track.Codec().RTCPFeedback {
		if fb.Type == "nack" && fb.Parameter == "pli" {
			r.usePLI = true
			break
		}
	}
	r.rwg.Add(2)
	go func() { defer r.rwg.Done(); r.readRTP() }()
	go func() { defer r.rwg.Done(); r.readRTCP() }()
	r.wg.Add(3)
	go func() { defer r.wg.Done(); r.decodeLoop() }()
	go func() { defer r.wg.Done(); r.playLoop() }()
	go func() { defer r.wg.Done(); r.monitor() }()
	return r
}

// AddSink adds a frame consumer; the most recent frame (if any) is delivered at once.
func (r *VideoReceiver) AddSink(s FrameSink) {
	r.mu.Lock()
	r.sinks = append(r.sinks, s)
	r.mu.Unlock()
}

// RemoveSink removes a consumer.
func (r *VideoReceiver) RemoveSink(s FrameSink) {
	r.mu.Lock()
	for i, x := range r.sinks {
		if x == s {
			r.sinks = append(r.sinks[:i], r.sinks[i+1:]...)
			break
		}
	}
	r.mu.Unlock()
}

// Size returns the size of the last decoded frame (0, 0 before the first).
func (r *VideoReceiver) Size() (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastW, r.lastH
}

// Muted reports whether packets are currently arriving.
func (r *VideoReceiver) Muted() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.muted
}

// Stats returns a snapshot.
func (r *VideoReceiver) Stats() VideoRecvStats {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.st
	s.FramesPerSecond = r.fps.rate()
	s.Muted = r.muted
	s.FrameWidth, s.FrameHeight = r.lastW, r.lastH
	s.JitterBufferTarget = r.delay
	return s
}

// Close stops decoding and playout. The two reader goroutines end when the peer connection (or the
// transceiver) closes; Wait waits for them.
func (r *VideoReceiver) Close() {
	r.once.Do(func() { close(r.done) })
	r.wg.Wait()
}

// Wait blocks until the RTP and RTCP readers have ended (after the peer connection was closed).
func (r *VideoReceiver) Wait() { r.rwg.Wait() }

// ---- RTP in -------------------------------------------------------------------------------------

func (r *VideoReceiver) readRTP() {
	for {
		p, _, err := r.track.ReadRTP()
		if err != nil {
			return
		}
		select {
		case <-r.done:
			return
		default:
		}
		r.insert(p, time.Now())
	}
}

func (r *VideoReceiver) readRTCP() {
	for {
		if _, _, err := r.rcv.ReadRTCP(); err != nil {
			return
		}
	}
}

func (r *VideoReceiver) setMuted(m bool) {
	if r.muted == m {
		return
	}
	r.muted = m
	if f := r.OnMute; f != nil {
		go f(m)
	}
}

func (r *VideoReceiver) extend(seq uint16) uint32 {
	if !r.haveSeq {
		r.haveSeq = true
		r.highest = uint32(seq) + 1<<16 // leave room below for late packets
		return r.highest
	}
	cycles := r.highest &^ 0xffff
	e := cycles | uint32(seq)
	switch {
	case int32(e-r.highest) > 0 && e-r.highest > 0x8000:
		e -= 0x10000
	case int32(r.highest-e) > 0 && r.highest-e > 0x8000:
		e += 0x10000
	}
	if int32(e-r.highest) > 0 {
		r.highest = e
	}
	return e
}

func (r *VideoReceiver) insert(p *rtp.Packet, now time.Time) {
	d, err := parseVP8Desc(p.Payload)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.st.PacketsReceived++
	r.st.BytesReceived += uint64(len(p.Payload) + p.Header.MarshalSize())
	r.st.LastPacketTime = now
	r.st.LastRTPTimestamp = p.Timestamp
	r.lastPktAt = now
	r.setMuted(false)
	if err != nil {
		r.st.PacketsDiscarded++
		return
	}
	e := r.extend(p.SequenceNumber)
	if _, dup := r.pkts[e]; dup || (r.haveRef && int32(e-r.delivered) <= 0) {
		r.st.PacketsDiscarded++
		return
	}
	pk := &vpkt{seq: e, ts: p.Timestamp, marker: p.Marker, start: d.start && d.pid == 0, payload: append([]byte(nil), p.Payload[d.hdrLen:]...), arrival: now}
	r.pkts[e] = pk
	// is a frame completed by this packet?
	s := e
	for {
		cur := r.pkts[s]
		if cur.start {
			break
		}
		prev, ok := r.pkts[s-1]
		if !ok || prev.ts != cur.ts {
			s = 0
			break
		}
		s--
	}
	if s == 0 {
		return
	}
	t := e
	for {
		cur := r.pkts[t]
		if cur.marker {
			break
		}
		next, ok := r.pkts[t+1]
		if !ok || next.ts != cur.ts {
			t = 0
			break
		}
		t++
	}
	if t == 0 {
		return
	}
	for _, f := range r.ready {
		if f.first == s {
			return
		}
	}
	fr := &vframe{first: s, last: t, ts: pk.ts, firstArr: r.pkts[s].arrival, arrival: now}
	size := 0
	for q := s; q <= t; q++ {
		size += len(r.pkts[q].payload)
	}
	fr.data = make([]byte, 0, size)
	for q := s; q <= t; q++ {
		pq := r.pkts[q]
		fr.data = append(fr.data, pq.payload...)
		if pq.arrival.After(fr.arrival) {
			fr.arrival = pq.arrival
		}
	}
	fr.key = len(fr.data) > 0 && fr.data[0]&1 == 0
	r.ready = append(r.ready, fr)
	sort.Slice(r.ready, func(i, j int) bool { return int32(r.ready[i].first-r.ready[j].first) < 0 })
	r.st.FramesReceived++
	select {
	case r.deliverSig <- struct{}{}:
	default:
	}
}

// ---- reassembly order and loss handling ---------------------------------------------------------

// next returns the next frame to decode, or nil. It applies the loss policy: frames are decoded in order;
// a gap that is not repaired within the wait budget makes the receiver drop frames and ask for a key
// frame, and frames that cannot be decoded without their reference are not shown (the last picture stays).
func (r *VideoReceiver) next(now time.Time) *vframe {
	r.mu.Lock()
	defer r.mu.Unlock()
	for len(r.ready) > 0 {
		f := r.ready[0]
		switch {
		case f.key:
			r.dropBefore(f.first)
			r.ready = r.ready[1:]
			r.consume(f)
			r.haveRef, r.needKey, r.waitSince = true, false, time.Time{}
			return f
		case r.haveRef && !r.needKey && f.first == r.delivered+1:
			r.ready = r.ready[1:]
			r.consume(f)
			r.waitSince = time.Time{}
			return f
		case r.haveRef && !r.needKey && int32(f.first-(r.delivered+1)) > 0:
			// a gap before this frame: give retransmissions a chance
			if r.waitSince.IsZero() {
				r.waitSince = now
			}
			budget := 180*time.Millisecond + time.Duration(r.delay*float64(time.Second))
			if now.Sub(r.waitSince) < budget {
				return nil
			}
			r.needKey = true
			r.st.FramesDropped++
			r.consume(f)
			r.ready = r.ready[1:]
			r.waitSince = time.Time{}
			r.requestKeyLocked(now)
		default:
			// no usable reference: discard until a key frame arrives
			r.st.FramesDropped++
			r.consume(f)
			r.ready = r.ready[1:]
			r.requestKeyLocked(now)
		}
	}
	return nil
}

func (r *VideoReceiver) consume(f *vframe) {
	for q := f.first; q != f.last+1; q++ {
		delete(r.pkts, q)
	}
	if int32(f.last-r.delivered) > 0 || !r.haveRef {
		r.delivered = f.last
	}
}

// dropBefore discards every stored packet and ready frame older than seq.
func (r *VideoReceiver) dropBefore(seq uint32) {
	for k := range r.pkts {
		if int32(k-seq) < 0 {
			delete(r.pkts, k)
		}
	}
	keep := r.ready[:0]
	for _, f := range r.ready {
		if int32(f.first-seq) >= 0 {
			keep = append(keep, f)
		} else {
			r.st.FramesDropped++
		}
	}
	r.ready = keep
}

func (r *VideoReceiver) requestKeyLocked(now time.Time) {
	if now.Sub(r.lastPLI) < 500*time.Millisecond {
		return
	}
	r.lastPLI = now
	r.st.PLICount++
	go r.sendPLI()
}

func (r *VideoReceiver) sendPLI() {
	var pkt rtcp.Packet = &rtcp.PictureLossIndication{MediaSSRC: r.ssrc}
	if !r.usePLI {
		pkt = &rtcp.FullIntraRequest{MediaSSRC: r.ssrc, FIR: []rtcp.FIREntry{{SSRC: r.ssrc, SequenceNumber: uint8(r.st.PLICount)}}}
	}
	if r.writeRTCP != nil {
		_ = r.writeRTCP([]rtcp.Packet{pkt})
	}
}

// ---- decode and playout ---------------------------------------------------------------------------

func (r *VideoReceiver) decodeLoop() {
	tk := time.NewTicker(25 * time.Millisecond)
	defer tk.Stop()
	for {
		select {
		case <-r.done:
			return
		case <-r.deliverSig:
		case <-tk.C:
		}
		for {
			now := time.Now()
			f := r.next(now)
			if f == nil {
				break
			}
			r.decode(f, now)
		}
		r.gc()
	}
}

// gc keeps the packet store bounded when a stream never completes frames.
func (r *VideoReceiver) gc() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.pkts) > 2000 {
		var old uint32 = r.highest
		for k := range r.pkts {
			if int32(k-old) < 0 {
				old = k
			}
		}
		cut := old + 1000
		for k := range r.pkts {
			if int32(k-cut) < 0 {
				delete(r.pkts, k)
			}
		}
		r.needKey = true
		r.requestKeyLocked(time.Now())
	}
}

func (r *VideoReceiver) decode(f *vframe, now time.Time) {
	t0 := time.Now()
	img, err := r.dec.Decode(f.data)
	dt := time.Since(t0)
	r.mu.Lock()
	if err != nil {
		r.st.DecodeErrors++
		r.needKey = true
		r.requestKeyLocked(now)
		r.mu.Unlock()
		return
	}
	r.st.FramesDecoded++
	r.st.TotalDecodeTime += dt.Seconds()
	if f.key {
		r.st.KeyFramesDecoded++
	}
	if img == nil {
		r.mu.Unlock()
		return
	}
	// playout time from the arrival jitter of whole frames: transit = arrival - RTP time, both relative to the first frame
	if !r.havePlay {
		r.havePlay = true
		r.firstTS, r.lastTS, r.tsExt, r.firstArr = f.ts, f.ts, 0, f.arrival
	}
	r.tsExt += int64(int32(f.ts - r.lastTS))
	r.lastTS = f.ts
	tsSec := float64(r.tsExt) / 90000
	tr := f.arrival.Sub(r.firstArr).Seconds() - tsSec
	if r.st.FramesDecoded == 1 {
		r.baseTr, r.prevTr = tr, tr
	}
	d := math.Abs(tr - r.prevTr)
	r.prevTr = tr
	r.jitter += (d - r.jitter) / 16
	if tr < r.baseTr {
		r.baseTr = tr
	} else {
		r.baseTr += (tr - r.baseTr) * 0.003
	}
	want := math.Max(0.02, math.Min(0.4, 3*r.jitter+0.02))
	if want > r.delay {
		r.delay += (want - r.delay) * 0.3
	} else {
		r.delay += (want - r.delay) * 0.02
	}
	playAt := r.firstArr.Add(time.Duration((r.baseTr + tsSec + r.delay) * float64(time.Second)))
	if playAt.Before(now) {
		playAt = now
	}
	if playAt.After(now.Add(600 * time.Millisecond)) {
		playAt = now.Add(600 * time.Millisecond)
	}
	r.outQ = append(r.outQ, &outFrame{img: img, at: playAt, arr: f.firstArr, size: len(f.data)})
	r.mu.Unlock()
	select {
	case r.outSig <- struct{}{}:
	default:
	}
}

func (r *VideoReceiver) playLoop() {
	t := time.NewTimer(time.Hour)
	defer t.Stop()
	for {
		r.mu.Lock()
		var wait time.Duration = time.Hour
		var due *outFrame
		now := time.Now()
		// pick the newest frame that is due; older due frames are skipped
		cut := 0
		for i, f := range r.outQ {
			if !f.at.After(now) {
				due = f
				cut = i + 1
			}
		}
		if due != nil {
			r.st.FramesDropped += uint64(cut - 1)
			r.outQ = r.outQ[cut:]
		}
		if len(r.outQ) > 0 {
			wait = r.outQ[0].at.Sub(now)
		}
		var sinks []FrameSink
		resize := false
		if due != nil {
			sinks = append(sinks, r.sinks...)
			b := due.img.Bounds()
			if b.Dx() != r.lastW || b.Dy() != r.lastH {
				r.lastW, r.lastH = b.Dx(), b.Dy()
				resize = true
			}
			r.st.JitterBufferDelay += now.Sub(due.arr).Seconds()
			r.st.JitterBufferEmittedCount++
			if !r.lastShown.IsZero() && now.Sub(r.lastShown) > 400*time.Millisecond {
				r.st.FreezeCount++
				r.st.TotalFreezesDuration += now.Sub(r.lastShown).Seconds()
			}
			r.lastShown = now
			r.fps.add(now)
		}
		r.mu.Unlock()
		if due != nil {
			if resize && r.OnResize != nil {
				r.OnResize(due.img.Bounds().Dx(), due.img.Bounds().Dy())
			}
			for _, s := range sinks {
				s.PushFrame(due.img)
			}
			if r.OnFrame != nil {
				r.OnFrame()
			}
			continue
		}
		if !t.Stop() {
			select {
			case <-t.C:
			default:
			}
		}
		t.Reset(wait)
		select {
		case <-r.done:
			return
		case <-r.outSig:
		case <-t.C:
		}
	}
}

// monitor handles mute detection and periodic key frame requests while no picture can be decoded.
func (r *VideoReceiver) monitor() {
	tk := time.NewTicker(100 * time.Millisecond)
	defer tk.Stop()
	start := time.Now()
	for {
		select {
		case <-r.done:
			return
		case now := <-tk.C:
			r.mu.Lock()
			if !r.lastPktAt.IsZero() && now.Sub(r.lastPktAt) > 2*time.Second {
				r.setMuted(true)
			}
			if r.needKey && !r.lastPktAt.IsZero() && now.Sub(r.lastPktAt) < time.Second && now.Sub(start) > 300*time.Millisecond {
				r.requestKeyLocked(now)
			}
			r.mu.Unlock()
		}
	}
}

// Sources returns what getSynchronizationSources reports for this stream.
func (r *VideoReceiver) Sources() (ssrc uint32, last time.Time, rtpTS uint32, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ssrc, r.st.LastPacketTime, r.st.LastRTPTimestamp, !r.st.LastPacketTime.IsZero()
}
