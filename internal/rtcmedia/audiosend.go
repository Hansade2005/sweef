package rtcmedia

import (
	"math"
	"sync"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/thesyncim/gopus"

	"github.com/sweefbrowser/sweef/internal/capture"
)

// opusSilence is a 20 ms Opus frame of silence (CELT, mono).
var opusSilence = []byte{0xf8, 0xff, 0xfe}

const (
	opusRate     = 48000
	opusFrame    = 960 // 20 ms
	audioBitrate = 32000
)

// AudioSendStats is the sender's contribution to getStats().
type AudioSendStats struct {
	FramesEncoded        uint64
	AudioLevel           float64
	TotalAudioEnergy     float64
	TotalSamplesDuration float64
	Underruns, Overruns  uint64
	PacketsSent          uint64
	BytesSent            uint64
	RTT                  float64
	FractionLost         float64
	Active               bool
}

// AudioSender encodes PCM from a capture.AudioSource to Opus (48 kHz, mono, 20 ms frames, in-band FEC)
// and sends it. Capture timing is decoupled from RTP timing by a small FIFO that a 20 ms clock drains.
type AudioSender struct {
	track *LocalTrack
	rs    *webrtc.RTPSender

	mu      sync.Mutex
	src     capture.AudioSource
	chunks  <-chan *capture.AudioChunk
	enabled bool
	active  bool
	hasSrc  bool
	closed  bool
	st      AudioSendStats
	bitrate int
	dtx     bool

	// Processor is the optional echo cancellation / noise suppression hook.
	Processor AudioProcessor

	enc     *gopus.Encoder
	res     *Resampler
	resRate int
	fifo    []float32
	tmp     []float32
	seq     uint16
	ts      uint32
	marker  bool
	lossPct int

	done chan struct{}
	wake chan struct{}
	wg   sync.WaitGroup
	rwg  sync.WaitGroup
}

// NewAudioSender starts the audio send machinery for a track.
func NewAudioSender(track *LocalTrack, rs *webrtc.RTPSender) (*AudioSender, error) {
	enc, err := gopus.NewEncoder(gopus.EncoderConfig{SampleRate: opusRate, Channels: 1, Application: gopus.ApplicationVoIP})
	if err != nil {
		return nil, err
	}
	_ = enc.SetBitrate(audioBitrate)
	enc.SetFEC(true)
	_ = enc.SetPacketLoss(5)
	_ = enc.SetComplexity(5)
	a := &AudioSender{track: track, rs: rs, enc: enc, enabled: true, active: true, bitrate: audioBitrate,
		done: make(chan struct{}), wake: make(chan struct{}, 1), seq: uint16(randUint32()), ts: randUint32(), marker: true}
	a.wg.Add(1)
	go func() { defer a.wg.Done(); a.run() }()
	a.rwg.Add(1)
	go func() { defer a.rwg.Done(); a.readRTCP() }()
	return a, nil
}

// SetSource attaches (or with nil detaches) the audio source; the previous one is stopped.
func (a *AudioSender) SetSource(src capture.AudioSource, c capture.AudioConstraints) error {
	var ch <-chan *capture.AudioChunk
	if src != nil {
		var err error
		if ch, err = src.Start(c); err != nil {
			return err
		}
	}
	a.mu.Lock()
	old := a.src
	a.src, a.chunks, a.hasSrc = src, ch, src != nil
	a.fifo = a.fifo[:0]
	a.res, a.resRate = nil, 0
	a.mu.Unlock()
	if old != nil {
		_ = old.Stop()
	}
	select {
	case a.wake <- struct{}{}:
	default:
	}
	return nil
}

// SetEnabled implements MediaStreamTrack.enabled: a disabled track sends silence.
func (a *AudioSender) SetEnabled(on bool) { a.mu.Lock(); a.enabled = on; a.mu.Unlock() }

// SetActive is RTCRtpEncodingParameters.active.
func (a *AudioSender) SetActive(on bool) { a.mu.Lock(); a.active = on; a.mu.Unlock() }

// SetBitrate sets the Opus bitrate (bits per second, 6000 to 128000).
func (a *AudioSender) SetBitrate(bps int) {
	if bps < 6000 {
		bps = 6000
	}
	if bps > 128000 {
		bps = 128000
	}
	a.mu.Lock()
	a.bitrate = bps
	_ = a.enc.SetBitrate(bps)
	a.mu.Unlock()
}

// SetDTX turns discontinuous transmission on or off.
func (a *AudioSender) SetDTX(on bool) { a.mu.Lock(); a.dtx = on; a.enc.SetDTX(on); a.mu.Unlock() }

// Close stops the sender and its source.
func (a *AudioSender) Close() {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return
	}
	a.closed = true
	src := a.src
	a.src, a.chunks = nil, nil
	close(a.done)
	a.mu.Unlock()
	if src != nil {
		_ = src.Stop()
	}
	a.wg.Wait()
}

// Wait blocks until the RTCP reader has ended (after the peer connection was closed).
func (a *AudioSender) Wait() { a.rwg.Wait() }

// Stats returns a snapshot.
func (a *AudioSender) Stats() AudioSendStats {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.st
	s.Active = a.active
	return s
}

func (a *AudioSender) readRTCP() {
	for {
		pkts, _, err := a.rs.ReadRTCP()
		if err != nil {
			return
		}
		for _, p := range pkts {
			rr, ok := p.(*rtcp.ReceiverReport)
			if !ok {
				continue
			}
			for _, r := range rr.Reports {
				if r.SSRC != a.track.Negotiated().SSRC {
					continue
				}
				frac := float64(r.FractionLost) / 256
				a.mu.Lock()
				a.st.FractionLost = frac
				if r.LastSenderReport != 0 {
					if d := ntpMid32(time.Now()) - r.LastSenderReport - r.Delay; d < 0x80000000 {
						a.st.RTT = float64(d) / 65536
					}
				}
				pct := int(math.Min(25, frac*100*1.5+3))
				if pct != a.lossPct {
					a.lossPct = pct
					_ = a.enc.SetPacketLoss(pct)
				}
				a.mu.Unlock()
			}
		}
	}
}

func (a *AudioSender) run() {
	tk := time.NewTicker(opusFrame * time.Second / opusRate)
	defer tk.Stop()
	for {
		a.mu.Lock()
		chunks := a.chunks
		a.mu.Unlock()
		select {
		case <-a.done:
			return
		case <-a.wake:
		case c, ok := <-chunks:
			if !ok {
				a.mu.Lock()
				if a.chunks == chunks {
					a.chunks = nil
				}
				a.mu.Unlock()
				continue
			}
			a.ingest(c)
		case <-tk.C:
			a.tick()
		}
	}
}

// ingest converts a captured chunk to mono 48 kHz and queues it.
func (a *AudioSender) ingest(c *capture.AudioChunk) {
	if c == nil || c.Channels < 1 || c.Rate < 8000 || len(c.PCM) == 0 {
		return
	}
	n := len(c.PCM) / c.Channels
	a.tmp = a.tmp[:0]
	for i := 0; i < n; i++ {
		var s float32
		for ch := 0; ch < c.Channels; ch++ {
			s += float32(c.PCM[i*c.Channels+ch])
		}
		a.tmp = append(a.tmp, s/float32(c.Channels)/32768)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.res == nil || a.resRate != c.Rate {
		a.res, a.resRate = NewResampler(c.Rate, opusRate), c.Rate
	}
	a.fifo = a.res.Process(a.fifo, a.tmp)
	// the source runs faster than the clock: keep the latency bounded (drop the oldest 20 ms steps)
	if over := len(a.fifo) - 6*opusFrame; over > 0 {
		over = (over + opusFrame - 1) / opusFrame * opusFrame
		a.fifo = append(a.fifo[:0], a.fifo[over:]...)
		a.st.Overruns++
	}
}

// tick sends one 20 ms frame.
func (a *AudioSender) tick() {
	a.mu.Lock()
	if !a.active || !a.hasSrc || a.closed {
		a.mu.Unlock()
		return
	}
	enabled := a.enabled
	var pcm [opusFrame]int16
	have := false
	if len(a.fifo) >= opusFrame {
		for i := 0; i < opusFrame; i++ {
			v := a.fifo[i] * 32767
			if v > 32767 {
				v = 32767
			} else if v < -32768 {
				v = -32768
			}
			pcm[i] = int16(v)
		}
		a.fifo = append(a.fifo[:0], a.fifo[opusFrame:]...)
		have = true
	} else if enabled {
		a.st.Underruns++
	}
	proc := a.Processor
	a.mu.Unlock()

	var data []byte
	level := 0.0
	if enabled && have {
		if proc != nil {
			proc.ProcessNearEnd(pcm[:])
		}
		level = rmsLevel(pcm[:])
		buf := make([]byte, 400)
		n, err := a.enc.EncodeInt16(pcm[:], buf)
		if err != nil {
			return
		}
		data = buf[:n]
	} else {
		data = opusSilence
	}
	a.mu.Lock()
	a.st.FramesEncoded++
	a.st.AudioLevel = level
	a.st.TotalAudioEnergy += level * level * 0.02
	a.st.TotalSamplesDuration += 0.02
	a.mu.Unlock()
	if len(data) <= 2 { // DTX: nothing to send for this frame, but time advances
		a.ts += opusFrame
		a.marker = true
		return
	}
	h := rtp.Header{Version: 2, SequenceNumber: a.seq, Timestamp: a.ts, Marker: a.marker}
	a.seq++
	a.ts += opusFrame
	a.marker = false
	d := dBov(level)
	n, _ := a.track.Write(&h, data, WriteOpts{Audio: true, AudioLevel: d, Voice: d < 70 && enabled, WithMid: a.seq < 40})
	a.mu.Lock()
	a.st.PacketsSent++
	a.st.BytesSent += uint64(n)
	a.mu.Unlock()
}
