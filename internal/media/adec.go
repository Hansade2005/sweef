package media

import (
	"fmt"

	"github.com/jfreymuth/vorbis"
	"github.com/thesyncim/gopus"
)

// AudioDecoder decodes compressed audio access units one at a time, as the
// player feeds them from a TrackStore.
type AudioDecoder interface {
	// Decode decodes one access unit to planar float32 PCM. The slices are
	// valid until the next call. Zero frames are fine (start-up packets).
	Decode(pkt []byte) ([][]float32, error)
	// Reset drops decoder history (after a seek or a gap).
	Reset()
	// Rate is the output sample rate; Channels the output channel count.
	Rate() int
	Channels() int
	// Preroll is the number of access units to decode and discard before
	// the first one that is wanted after Reset, so that overlap-add and
	// prediction state has converged.
	Preroll() int
}

// NewAudioDecoder returns a streaming decoder for an audio track.
func NewAudioDecoder(c TrackConfig) (AudioDecoder, error) {
	switch c.CodecName {
	case "aac":
		if isUSACConfig(c.Cfg) {
			return nil, ErrUnsupported
		}
		cfg, err := parseASC(c.Cfg)
		if err != nil {
			return nil, err
		}
		d, err := newAACDecoder(cfg)
		if err != nil {
			return nil, err
		}
		a := &aacStream{d: d, cfg: cfg}
		a.rate = cfg.rate
		if d.sbr && !d.down {
			a.rate = 2 * cfg.rate
		}
		a.nch = d.nch
		if d.ps {
			a.nch++
		}
		return a, nil
	case "opus":
		var h opusHead
		var err error
		switch c.CfgKind {
		case "dops":
			h, err = parseDOps(c.Cfg)
		default:
			h, err = parseOpusHead(c.Cfg)
		}
		if err != nil {
			return nil, err
		}
		return newOpusStream(h)
	case "vorbis":
		return newVorbisStream(c.Cfg)
	}
	return nil, ErrUnsupported
}

// ---- AAC -------------------------------------------------------------------------------

type aacStream struct {
	d         *aacDecoder
	cfg       aacConfig
	rate, nch int
	out       [][]float32
}

func (a *aacStream) Rate() int     { return a.rate }
func (a *aacStream) Channels() int { return a.nch }
func (a *aacStream) Preroll() int  { return 2 }

func (a *aacStream) Reset() {
	if d, err := newAACDecoder(a.cfg); err == nil {
		a.d = d
	}
}

func (a *aacStream) Decode(pkt []byte) ([][]float32, error) {
	d := a.d
	if len(a.out) != a.nch {
		a.out = make([][]float32, a.nch)
	}
	for i := range a.out {
		a.out[i] = a.out[i][:0]
	}
	d.pcm = a.out
	err := d.decodeFrameSafe(pkt)
	a.out = d.pcm
	if err != nil {
		return nil, err
	}
	return a.out, nil
}

// ---- Opus ------------------------------------------------------------------------------

type opusStream struct {
	h     opusHead
	dec   func(pkt []byte, out []float32) (int, error)
	reset func()
	buf   []float32
	out   [][]float32
	ch    int
	perm  []int // output channel i is decoded channel perm[i]
}

func newOpusStream(h opusHead) (*opusStream, error) {
	if h.channels < 1 || h.channels > 8 {
		return nil, ErrUnsupported
	}
	s := &opusStream{h: h, ch: h.channels, buf: make([]float32, 5760*h.channels), out: make([][]float32, h.channels)}
	if err := s.build(); err != nil {
		return nil, err
	}
	if h.family == 1 {
		if p, ok := vorbisToSMPTE[h.channels]; ok {
			s.perm = p
		}
	}
	return s, nil
}

func (s *opusStream) build() error {
	h := s.h
	if h.family == 0 && h.channels <= 2 {
		d, err := gopus.NewDecoder(gopus.DefaultDecoderConfig(48000, h.channels))
		if err != nil {
			return err
		}
		if h.gainQ8 != 0 {
			_ = d.SetGain(h.gainQ8)
		}
		s.dec = d.Decode
		return nil
	}
	if h.family == 0 {
		return ErrUnsupported
	}
	d, err := gopus.NewMultistreamDecoder(48000, h.channels, h.streams, h.coupled, h.mapping)
	if err != nil {
		return err
	}
	if h.gainQ8 != 0 {
		_ = d.SetGain(h.gainQ8)
	}
	s.dec = d.Decode
	return nil
}

func (s *opusStream) Rate() int     { return 48000 }
func (s *opusStream) Channels() int { return s.ch }
func (s *opusStream) Preroll() int  { return 4 } // 80 ms

func (s *opusStream) Reset() { _ = s.build() }

func (s *opusStream) Decode(pkt []byte) ([][]float32, error) {
	if len(pkt) == 0 {
		return nil, nil
	}
	n, err := s.dec(pkt, s.buf)
	if err != nil {
		return nil, err
	}
	for c := 0; c < s.ch; c++ {
		if cap(s.out[c]) < n {
			s.out[c] = make([]float32, n)
		}
		s.out[c] = s.out[c][:n]
		src := c
		if s.perm != nil {
			src = s.perm[c]
		}
		for i := 0; i < n; i++ {
			s.out[c][i] = s.buf[i*s.ch+src]
		}
	}
	return s.out, nil
}

// ---- Vorbis ----------------------------------------------------------------------------

type vorbisStream struct {
	d    *vorbis.Decoder
	hdrs [][]byte
	buf  []float32
	out  [][]float32
}

// xiphPackets splits Matroska's Xiph-laced CodecPrivate into the three Vorbis headers.
func xiphPackets(b []byte) ([][]byte, error) {
	if len(b) < 3 || b[0] != 2 {
		return nil, fmt.Errorf("media: bad Vorbis CodecPrivate")
	}
	p := 1
	var sizes [2]int
	for i := 0; i < 2; i++ {
		for p < len(b) {
			v := int(b[p])
			p++
			sizes[i] += v
			if v != 255 {
				break
			}
		}
	}
	if p+sizes[0]+sizes[1] > len(b) {
		return nil, fmt.Errorf("media: truncated Vorbis CodecPrivate")
	}
	h0 := b[p : p+sizes[0]]
	h1 := b[p+sizes[0] : p+sizes[0]+sizes[1]]
	h2 := b[p+sizes[0]+sizes[1]:]
	return [][]byte{h0, h1, h2}, nil
}

func newVorbisStream(cfg []byte) (*vorbisStream, error) {
	h, err := xiphPackets(cfg)
	if err != nil {
		return nil, err
	}
	s := &vorbisStream{hdrs: h}
	return s, s.build()
}

func (s *vorbisStream) build() error {
	s.d = &vorbis.Decoder{}
	for _, h := range s.hdrs {
		if err := s.d.ReadHeader(h); err != nil {
			return err
		}
	}
	s.buf = make([]float32, s.d.BufferSize())
	s.out = make([][]float32, s.d.Channels())
	return nil
}

func (s *vorbisStream) Rate() int     { return s.d.SampleRate() }
func (s *vorbisStream) Channels() int { return s.d.Channels() }
func (s *vorbisStream) Preroll() int  { return 1 }
func (s *vorbisStream) Reset()        { s.d.Clear() }

func (s *vorbisStream) Decode(pkt []byte) ([][]float32, error) {
	in, err := s.d.DecodeInto(pkt, s.buf)
	if err != nil {
		return nil, err
	}
	ch := s.d.Channels()
	n := len(in) / ch
	for c := 0; c < ch; c++ {
		if cap(s.out[c]) < n {
			s.out[c] = make([]float32, n)
		}
		s.out[c] = s.out[c][:n]
		for i := 0; i < n; i++ {
			s.out[c][i] = in[i*ch+c]
		}
	}
	return s.out, nil
}
