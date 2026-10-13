package rtcmedia

import (
	"io"
	"math"
	"sync"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/thesyncim/gopus"
)

// AudioRecvStats is the receiver's contribution to getStats().
type AudioRecvStats struct {
	PacketsReceived, PacketsDiscarded, BytesReceived                             uint64
	SamplesReceived, ConcealedSamples, SilentConcealedSamples, ConcealmentEvents uint64
	InsertedSamplesForDeceleration, RemovedSamplesForAcceleration                uint64
	TotalSamplesDuration, TotalAudioEnergy, AudioLevel                           float64
	JitterBufferDelay                                                            float64
	JitterBufferEmittedCount                                                     uint64
	JitterBufferTarget                                                           float64
	FECRecovered                                                                 uint64
	LastPacketTime                                                               time.Time
	LastRTPTimestamp                                                             uint32
	SSRC                                                                         uint32
	Muted                                                                        bool
	DecodeErrors                                                                 uint64
}

type apkt struct {
	data    []byte
	ts      uint32
	arrival time.Time
}

// AudioReceiver reorders and decodes a received Opus stream and plays it into an AudioSink: a small
// adaptive jitter buffer, packet loss concealment (in-band FEC when the next packet carries it, else
// the decoder's PLC) and level metering.
type AudioReceiver struct {
	track *webrtc.TrackRemote
	rcv   *webrtc.RTPReceiver
	ssrc  uint32

	mu      sync.Mutex
	sink    AudioSink
	proc    AudioProcessor
	pkts    map[uint32]*apkt
	highest uint32
	haveSeq bool
	started bool
	playSeq uint32
	jitter  float64 // seconds, RFC 3550 style
	prevTr  float64
	haveTr  bool
	t0      time.Time
	tsBase  uint32
	target  int // packets to hold before starting / steady depth
	plcRun  int
	owed    int
	st      AudioRecvStats
	lastPkt time.Time
	muted   bool

	dec *gopus.Decoder
	pcm []int16
	f32 []float32
	out []int16

	done chan struct{}
	wg   sync.WaitGroup
	rwg  sync.WaitGroup
	once sync.Once

	onMute func(muted bool)
}

// NewAudioReceiver starts receiving a remote audio track into sink (nil: discard).
// onMute is called when packets stop or resume (from a receiver goroutine).
func NewAudioReceiver(track *webrtc.TrackRemote, rcv *webrtc.RTPReceiver, sink AudioSink, onMute func(muted bool)) (*AudioReceiver, error) {
	dec, err := gopus.NewDecoder(gopus.DefaultDecoderConfig(opusRate, 2))
	if err != nil {
		return nil, err
	}
	if sink == nil {
		sink = NullSink{}
	}
	a := &AudioReceiver{track: track, rcv: rcv, ssrc: uint32(track.SSRC()), sink: sink, pkts: map[uint32]*apkt{}, dec: dec,
		done: make(chan struct{}), target: 3, muted: true, t0: time.Now(), onMute: onMute}
	a.st.SSRC = a.ssrc
	a.st.Muted = true
	a.pcm = make([]int16, 5760*2)
	a.f32 = make([]float32, 5760*2)
	a.rwg.Add(2)
	go func() { defer a.rwg.Done(); a.readRTP() }()
	go func() { defer a.rwg.Done(); a.readRTCP() }()
	a.wg.Add(1)
	go func() { defer a.wg.Done(); a.play() }()
	return a, nil
}

// SetSink changes where audio goes (nil: discard).
func (a *AudioReceiver) SetSink(s AudioSink) {
	if s == nil {
		s = NullSink{}
	}
	a.mu.Lock()
	old := a.sink
	a.sink = s
	a.mu.Unlock()
	if c, ok := old.(io.Closer); ok && old != s {
		c.Close() // a sink that holds a device stream releases it
	}
}

// SetProcessor installs the echo cancellation hook, which sees the far-end signal before playback.
func (a *AudioReceiver) SetProcessor(p AudioProcessor) { a.mu.Lock(); a.proc = p; a.mu.Unlock() }

// Close stops playout; Wait waits for the RTP and RTCP readers, which end when the transport closes.
func (a *AudioReceiver) Close() {
	a.once.Do(func() { close(a.done) })
	a.wg.Wait()
	a.mu.Lock()
	sink := a.sink
	a.mu.Unlock()
	if c, ok := sink.(io.Closer); ok {
		c.Close()
	}
}

// Wait blocks until the readers have ended (after the peer connection was closed).
func (a *AudioReceiver) Wait() { a.rwg.Wait() }

// Stats returns a snapshot.
func (a *AudioReceiver) Stats() AudioRecvStats {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.st
	s.Muted = a.muted
	s.JitterBufferTarget = float64(a.target) * 0.02
	return s
}

// Sources returns what getSynchronizationSources reports.
func (a *AudioReceiver) Sources() (ssrc uint32, last time.Time, rtpTS uint32, level float64, ok bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.ssrc, a.st.LastPacketTime, a.st.LastRTPTimestamp, a.st.AudioLevel, !a.st.LastPacketTime.IsZero()
}

func (a *AudioReceiver) readRTCP() {
	for {
		if _, _, err := a.rcv.ReadRTCP(); err != nil {
			return
		}
	}
}

func (a *AudioReceiver) readRTP() {
	for {
		p, _, err := a.track.ReadRTP()
		if err != nil {
			return
		}
		select {
		case <-a.done:
			return
		default:
		}
		a.insert(p, time.Now())
	}
}

func (a *AudioReceiver) setMuted(m bool) {
	if a.muted == m {
		return
	}
	a.muted = m
	if f := a.onMute; f != nil {
		go f(m)
	}
}

func (a *AudioReceiver) insert(p *rtp.Packet, now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.st.PacketsReceived++
	a.st.BytesReceived += uint64(len(p.Payload) + p.Header.MarshalSize())
	a.st.LastPacketTime, a.st.LastRTPTimestamp = now, p.Timestamp
	a.lastPkt = now
	a.setMuted(false)
	if len(p.Payload) < 1 {
		a.st.PacketsDiscarded++
		return
	}
	var e uint32
	if !a.haveSeq {
		a.haveSeq = true
		e = uint32(p.SequenceNumber) + 1<<16
		a.highest = e
		a.tsBase = p.Timestamp
	} else {
		e = a.highest&^0xffff | uint32(p.SequenceNumber)
		if d := int32(e - a.highest); d > 0x8000 || d < -0x8000 {
			if d > 0 {
				e -= 0x10000
			} else {
				e += 0x10000
			}
		}
		if int32(e-a.highest) > 0 {
			a.highest = e
		}
	}
	if a.started && int32(e-a.playSeq) < 0 {
		a.st.PacketsDiscarded++ // too late to play
		return
	}
	if _, dup := a.pkts[e]; dup {
		a.st.PacketsDiscarded++
		return
	}
	// interarrival jitter on the 48 kHz timestamps
	tr := now.Sub(a.t0).Seconds() - float64(int32(p.Timestamp-a.tsBase))/opusRate
	if a.haveTr {
		d := math.Abs(tr - a.prevTr)
		a.jitter += (d - a.jitter) / 16
	}
	a.prevTr, a.haveTr = tr, true
	// adaptive depth: about two jitters plus one packet, between 2 and 12 packets
	t := int(math.Ceil((2*a.jitter+0.02)/0.02)) + 1
	if t < 2 {
		t = 2
	}
	if t > 12 {
		t = 12
	}
	if t > a.target {
		a.target = t
	} else if t < a.target && a.st.PacketsReceived%100 == 0 {
		a.target--
	}
	a.pkts[e] = &apkt{data: append([]byte(nil), p.Payload...), ts: p.Timestamp, arrival: now}
}

func (a *AudioReceiver) play() {
	tk := time.NewTicker(20 * time.Millisecond)
	defer tk.Stop()
	for {
		select {
		case <-a.done:
			return
		case now := <-tk.C:
			a.tick(now)
		}
	}
}

func (a *AudioReceiver) tick(now time.Time) {
	a.mu.Lock()
	if !a.lastPkt.IsZero() && now.Sub(a.lastPkt) > 2*time.Second {
		a.setMuted(true)
	}
	if !a.started {
		if len(a.pkts) < a.target {
			a.mu.Unlock()
			return
		}
		first := a.highest
		for k := range a.pkts {
			if int32(k-first) < 0 {
				first = k
			}
		}
		a.playSeq, a.started, a.plcRun, a.owed = first, true, 0, 0
	}
	// the buffer is too deep (a burst arrived or the clocks drift): drop the oldest packets
	if depth := int(int32(a.highest - a.playSeq)); depth > a.target+10 {
		skip := depth - a.target - 2
		for i := 0; i < skip; i++ {
			delete(a.pkts, a.playSeq)
			a.playSeq++
			a.st.RemovedSamplesForAcceleration += opusFrame
		}
	}
	a.owed += opusFrame
	var frames [][]int16
	for a.owed > 0 {
		n, pcm := a.step(now)
		if n <= 0 {
			break
		}
		a.owed -= n
		frames = append(frames, pcm)
	}
	sink, proc := a.sink, a.proc
	a.mu.Unlock()
	for _, f := range frames {
		if proc != nil {
			proc.AnalyzeFarEnd(f, opusRate, 2)
		}
		sink.Write(f, opusRate, 2)
	}
}

// step produces the next piece of audio (decoded, concealed or silence) and returns its length in
// samples per channel. a.mu is held.
func (a *AudioReceiver) step(now time.Time) (int, []int16) {
	pk, ok := a.pkts[a.playSeq]
	var n int
	var err error
	switch {
	case ok:
		delete(a.pkts, a.playSeq)
		a.playSeq++
		a.plcRun = 0
		n, err = a.dec.DecodeInt16(pk.data, a.pcm)
		if err != nil {
			a.st.DecodeErrors++
			n = 0
		}
		a.st.JitterBufferDelay += now.Sub(pk.arrival).Seconds() * float64(n)
		a.st.JitterBufferEmittedCount += uint64(n)
	case int32(a.highest-a.playSeq) > 0:
		// a hole with newer packets behind it: recover with the next packet's FEC when possible, else PLC
		a.st.ConcealmentEvents++
		if next, have := a.pkts[a.playSeq+1]; have {
			a.playSeq++
			n, err = a.dec.DecodeWithFEC(next.data, a.f32, true)
			if err == nil && n > 0 {
				a.st.FECRecovered++
				for i := 0; i < n*2 && i < len(a.f32); i++ {
					a.pcm[i] = f32to16(a.f32[i])
				}
			} else {
				n, err = a.dec.DecodeInt16(nil, a.pcm)
			}
		} else {
			a.playSeq++
			n, err = a.dec.DecodeInt16(nil, a.pcm)
		}
		a.st.ConcealedSamples += uint64(opusFrame)
		if err != nil {
			n = 0
		}
	default:
		// nothing to play yet: conceal up to 100 ms, then fall silent and re-buffer
		a.plcRun++
		if a.plcRun > 5 {
			a.started = false
			return 0, nil
		}
		n, err = a.dec.DecodeInt16(nil, a.pcm)
		if err != nil {
			n = 0
		}
		a.st.ConcealedSamples += uint64(n)
		a.st.InsertedSamplesForDeceleration += uint64(n)
	}
	if n <= 0 {
		return 0, nil
	}
	out := append([]int16(nil), a.pcm[:n*2]...)
	a.st.SamplesReceived += uint64(n)
	dur := float64(n) / opusRate
	lvl := rmsLevel(out)
	a.st.TotalSamplesDuration += dur
	a.st.TotalAudioEnergy += lvl * lvl * dur
	a.st.AudioLevel = lvl
	return n, out
}

func f32to16(v float32) int16 {
	v *= 32767
	if v > 32767 {
		return 32767
	}
	if v < -32768 {
		return -32768
	}
	return int16(v)
}
