package media

import (
	"encoding/binary"
	"fmt"
	"math"
	"sync"

	mp3 "github.com/hajimehoshi/go-mp3"
)

// TrackSet is what a player needs from a progressive resource: the video and
// audio sample stores and a way to know when they are complete.
type TrackSet struct {
	Video, Audio *TrackStore
	// Done is nil for a resource that is indexed completely when it is
	// returned; otherwise it is closed when a background indexer or decoder has
	// added the last sample (until then the stores grow, like a SourceBuffer).
	Done <-chan struct{}
	// Err is the reason a background job gave up (read after Done is closed).
	mu  sync.Mutex
	err error
}

// Failed returns the error of a background job, if any.
func (t *TrackSet) Failed() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.err
}

func (t *TrackSet) fail(err error) {
	t.mu.Lock()
	t.err = err
	t.mu.Unlock()
}

// maxPCMSeconds bounds the audio decoded up front for formats without a
// frame-level index (WAV, FLAC, Ogg): longer files are left silent.
const maxPCMSeconds = 12 * 60

// OpenTrackSet opens any resource the player can handle: MP4/WebM through
// OpenTracks, MP3 with a background frame indexer, and WAV, FLAC, Ogg and ADTS
// AAC through a background decode into 16-bit PCM chunks.
func OpenTrackSet(src Source, info *Info) (*TrackSet, error) {
	switch info.Container {
	case "mp4", "webm", "matroska":
		v, a, err := OpenTracks(src, info)
		if err != nil {
			return nil, err
		}
		return &TrackSet{Video: v, Audio: a}, nil
	case "mp3":
		return openMP3Set(src, info)
	case "wav", "flac", "ogg", "aac":
		return openPCMSet(src, info)
	}
	return nil, ErrUnsupported
}

// ---- MP3 ----------------------------------------------------------------------------

func openMP3Set(src Source, info *Info) (*TrackSet, error) {
	a := info.Audio()
	if a == nil {
		return nil, fmt.Errorf("media: no audio track")
	}
	rate := a.SampleRate
	cfg := TrackConfig{ID: 1, Kind: "audio", Codec: "mp3", CodecName: "mp3", SampleRate: rate, Channels: 2}
	st := NewTrackStore(cfg)
	done := make(chan struct{})
	ts := &TrackSet{Audio: st, Done: done}
	go func() {
		defer close(done)
		if err := indexMP3(src, info, st); err != nil {
			ts.fail(err)
		}
	}()
	return ts, nil
}

// indexMP3 walks the frame headers of the file and appends one sample per
// frame. The encoder delay (and the Xing frame) are put on the negative part
// of the timeline, where playback trims them.
func indexMP3(src Source, info *Info, st *TrackStore) error {
	size := src.Size()
	pos := info.DataStart
	if pos < 0 {
		pos = 0
	}
	if info.DataEnd > 0 && info.DataEnd < size {
		size = info.DataEnd
	}
	skip := 0
	if info.SkipStart > 0 {
		skip = info.SkipStart + 529
	}
	rate := 0
	var t float64
	first := true
	var batch []Sample
	flush := func() {
		if len(batch) > 0 {
			st.Append(batch)
			batch = nil
		}
	}
	defer flush()
	buf := make([]byte, 0, 1<<16)
	var bufStart int64
	for pos+4 <= size {
		// refill when fewer than a header's worth of frame is buffered
		if pos < bufStart || pos+2048 > bufStart+int64(len(buf)) {
			n := int64(1 << 16)
			if pos+n > size {
				n = size - pos
			}
			b, err := readFull(src, pos, int(n))
			if len(b) == 0 && err != nil {
				return err
			}
			buf, bufStart = b, pos
		}
		off := int(pos - bufStart)
		h, ok := parseMP3Header(buf[off:])
		if !ok || h.layer != 3 || h.frameLen <= 4 {
			pos++
			continue
		}
		if pos+int64(h.frameLen) > size {
			break
		}
		if rate == 0 {
			rate = h.rate
		}
		if h.rate != rate {
			pos++ // a stream that changes rate mid-file: stop at the change
			continue
		}
		dur := float64(h.spf) / float64(rate)
		if first {
			first = false
			if info.XingFrame {
				pos += int64(h.frameLen) // the Info/Xing frame decodes to silence: not part of the timeline
				continue
			}
			t = -float64(skip) / float64(rate)
		}
		p, n := pos, h.frameLen
		batch = append(batch, NewLazySample(t, t, dur, true, n, func() ([]byte, error) { return readFull(src, p, n) }))
		t += dur
		pos += int64(h.frameLen)
		if len(batch) >= 256 {
			flush()
		}
	}
	return nil
}

// mp3Stream decodes single MP3 frames.
type mp3Stream struct {
	d    *mp3.FrameDecoder
	rate int
	out  [][]float32
}

func newMP3Stream(c TrackConfig) *mp3Stream {
	return &mp3Stream{d: mp3.NewFrameDecoder(), rate: c.SampleRate, out: make([][]float32, 2)}
}

func (m *mp3Stream) Rate() int     { return m.rate }
func (m *mp3Stream) Channels() int { return 2 }
func (m *mp3Stream) Preroll() int  { return 3 }
func (m *mp3Stream) Reset()        { m.d.Reset() }

func (m *mp3Stream) Decode(pkt []byte) ([][]float32, error) {
	raw, sr, err := m.d.Decode(pkt)
	if err != nil {
		if err == mp3.ErrNoData {
			return nil, nil // the reservoir is not primed yet: no audio from this frame
		}
		return nil, err
	}
	m.rate = sr
	n := len(raw) / 4
	for c := range m.out {
		if cap(m.out[c]) < n {
			m.out[c] = make([]float32, n)
		}
		m.out[c] = m.out[c][:n]
	}
	for i := 0; i < n; i++ {
		m.out[0][i] = float32(int16(binary.LittleEndian.Uint16(raw[i*4:]))) / 32768
		m.out[1][i] = float32(int16(binary.LittleEndian.Uint16(raw[i*4+2:]))) / 32768
	}
	return m.out, nil
}

// ---- decoded PCM -----------------------------------------------------------------------

const pcmChunk = 4096 // frames per sample

func openPCMSet(src Source, info *Info) (*TrackSet, error) {
	a := info.Audio()
	if a == nil {
		return nil, fmt.Errorf("media: no audio track")
	}
	if info.Duration > maxPCMSeconds {
		return nil, fmt.Errorf("media: %s of %s audio is too long to decode up front", fmtDur(info.Duration), info.Container)
	}
	if src.Size() > 256<<20 {
		return nil, fmt.Errorf("media: file too large")
	}
	done := make(chan struct{})
	ts := &TrackSet{Done: done}
	cfg := TrackConfig{ID: 1, Kind: "audio", Codec: "pcm", CodecName: "pcm_s16", SampleRate: a.SampleRate, Channels: a.Channels}
	if cfg.Channels <= 0 {
		cfg.Channels = 2
	}
	st := NewTrackStore(cfg)
	ts.Audio = st
	go func() {
		defer close(done)
		data, err := readFull(src, 0, int(src.Size()))
		if len(data) == 0 {
			ts.fail(err)
			return
		}
		pcm, err := DecodeAudio(data)
		if err != nil || pcm == nil || pcm.Frames() == 0 {
			ts.fail(fmt.Errorf("media: decode: %v", err))
			return
		}
		data = nil
		c := cfg
		c.SampleRate, c.Channels = pcm.SampleRate, len(pcm.Data)
		st.SetConfig(c)
		var batch []Sample
		n := pcm.Frames()
		ch := len(pcm.Data)
		for i := 0; i < n; i += pcmChunk {
			e := min(i+pcmChunk, n)
			b := make([]byte, (e-i)*ch*2)
			for f := i; f < e; f++ {
				for k := 0; k < ch; k++ {
					v := pcm.Data[k][f]
					if v > 1 {
						v = 1
					} else if v < -1 {
						v = -1
					}
					binary.LittleEndian.PutUint16(b[((f-i)*ch+k)*2:], uint16(int16(math.Round(float64(v)*32767))))
				}
			}
			t0 := float64(i) / float64(pcm.SampleRate)
			batch = append(batch, NewSample(t0, t0, float64(e-i)/float64(pcm.SampleRate), true, b))
			if len(batch) >= 64 {
				st.Append(batch)
				batch = nil
			}
		}
		if len(batch) > 0 {
			st.Append(batch)
		}
	}()
	return ts, nil
}

func fmtDur(s float64) string { return fmt.Sprintf("%.0f s", s) }

// pcmStream turns PCM chunk samples back into planar float32.
type pcmStream struct {
	rate, ch int
	out      [][]float32
}

func newPCMStream(c TrackConfig) *pcmStream {
	return &pcmStream{rate: c.SampleRate, ch: c.Channels, out: make([][]float32, c.Channels)}
}

func (p *pcmStream) Rate() int     { return p.rate }
func (p *pcmStream) Channels() int { return p.ch }
func (p *pcmStream) Preroll() int  { return 0 }
func (p *pcmStream) Reset()        {}

func (p *pcmStream) Decode(pkt []byte) ([][]float32, error) {
	n := len(pkt) / 2 / p.ch
	for c := range p.out {
		if cap(p.out[c]) < n {
			p.out[c] = make([]float32, n)
		}
		p.out[c] = p.out[c][:n]
	}
	for i := 0; i < n; i++ {
		for c := 0; c < p.ch; c++ {
			p.out[c][i] = float32(int16(binary.LittleEndian.Uint16(pkt[(i*p.ch+c)*2:]))) / 32768
		}
	}
	return p.out, nil
}
