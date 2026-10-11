package media

import (
	"fmt"

	"github.com/thesyncim/gopus"
)

// opusHead is the parsed Opus identification header (RFC 7845 section 5.1).
type opusHead struct {
	channels int
	preSkip  int
	gainQ8   int
	family   int
	streams  int
	coupled  int
	mapping  []byte
}

func parseOpusHead(b []byte) (opusHead, error) {
	var h opusHead
	if len(b) < 19 || string(b[:8]) != "OpusHead" {
		return h, fmt.Errorf("media: bad OpusHead")
	}
	h.channels = int(b[9])
	h.preSkip = le16(b[10:])
	h.gainQ8 = int(int16(le16(b[16:])))
	h.family = int(b[18])
	if h.family != 0 {
		if len(b) < 21+h.channels {
			return h, fmt.Errorf("media: short OpusHead mapping")
		}
		h.streams, h.coupled = int(b[19]), int(b[20])
		h.mapping = append([]byte(nil), b[21:21+h.channels]...)
	}
	return h, nil
}

// parseDOps parses the MP4 'dOps' box payload (big endian OpusHead).
func parseDOps(b []byte) (opusHead, error) {
	var h opusHead
	if len(b) < 11 {
		return h, fmt.Errorf("media: bad dOps")
	}
	h.channels = int(b[1])
	h.preSkip = be16(b[2:])
	h.gainQ8 = int(int16(be16(b[8:])))
	h.family = int(b[10])
	if h.family != 0 {
		if len(b) < 13+h.channels {
			return h, fmt.Errorf("media: short dOps mapping")
		}
		h.streams, h.coupled = int(b[11]), int(b[12])
		h.mapping = append([]byte(nil), b[13:13+h.channels]...)
	}
	return h, nil
}

// decodeOpus decodes Opus packets (SILK, CELT and hybrid, 2.5-60 ms frames,
// mono, stereo and RFC 7845 family 1 multichannel) to 48 kHz planar PCM,
// applying the pre-skip and the header output gain. total, when >= 0, is the
// number of frames to keep after the pre-skip (Ogg end trimming).
func decodeOpus(h opusHead, pkts [][]byte, total int) (*PCM, error) {
	if h.channels < 1 || h.channels > 8 {
		return nil, ErrUnsupported
	}
	ch := h.channels
	var dec func(pkt []byte, out []float32) (int, error)
	if h.family == 0 && ch <= 2 {
		d, err := gopus.NewDecoder(gopus.DefaultDecoderConfig(48000, ch))
		if err != nil {
			return nil, err
		}
		if h.gainQ8 != 0 {
			_ = d.SetGain(h.gainQ8)
		}
		dec = d.Decode
	} else {
		streams, coupled, mapping := h.streams, h.coupled, h.mapping
		if h.family == 0 {
			return nil, ErrUnsupported
		}
		d, err := gopus.NewMultistreamDecoder(48000, ch, streams, coupled, mapping)
		if err != nil {
			return nil, err
		}
		if h.gainQ8 != 0 {
			_ = d.SetGain(h.gainQ8)
		}
		dec = d.Decode
	}
	out := &PCM{SampleRate: 48000, Data: make([][]float32, ch)}
	buf := make([]float32, 5760*ch)
	skip := h.preSkip
	for _, p := range pkts {
		if len(p) == 0 {
			continue
		}
		n, err := dec(p, buf)
		if err != nil {
			// a corrupt packet becomes silence of unknown length: stop at the
			// last good frame like a decoder that gives up on the stream
			if len(out.Data[0]) == 0 {
				return nil, err
			}
			break
		}
		for i := 0; i < n; i++ {
			if skip > 0 {
				skip--
				continue
			}
			for c := 0; c < ch; c++ {
				out.Data[c] = append(out.Data[c], buf[i*ch+c])
			}
		}
	}
	if h.family == 1 {
		// Vorbis channel order -> the SMPTE/ffmpeg order Web Audio uses
		if perm, ok := vorbisToSMPTE[ch]; ok {
			re := make([][]float32, ch)
			for i, src := range perm {
				re[i] = out.Data[src]
			}
			out.Data = re
		}
	}
	if total >= 0 && total < len(out.Data[0]) {
		for c := range out.Data {
			out.Data[c] = out.Data[c][:total]
		}
	}
	return out, nil
}

// decodeOpusStream decodes the Opus track of any supported container.
func decodeOpusStream(as *audioStream) (*PCM, error) {
	var h opusHead
	var err error
	switch as.cfgKind {
	case "opushead":
		h, err = parseOpusHead(as.cfg)
	case "dops":
		h, err = parseDOps(as.cfg)
	default:
		err = ErrUnsupported
	}
	if err != nil {
		return nil, err
	}
	total := -1
	if as.lastGran >= 0 {
		total = int(as.lastGran) - h.preSkip
		if total < 0 {
			total = 0
		}
	}
	if as.endTrim > 0 {
		n := 0
		for _, p := range as.packets {
			if len(p) > 0 {
				n += opusPacketSamples(p)
			}
		}
		total = n - h.preSkip - as.endTrim
		if total < 0 {
			total = 0
		}
	}
	return decodeOpus(h, as.packets, total)
}

// vorbisToSMPTE maps an output channel index to the Vorbis-order channel
// holding it (RFC 7845 family 1 layouts).
var vorbisToSMPTE = map[int][]int{
	3: {0, 2, 1},
	5: {0, 2, 1, 3, 4},
	6: {0, 2, 1, 5, 3, 4},
	7: {0, 2, 1, 6, 5, 3, 4},
	8: {0, 2, 1, 7, 5, 6, 3, 4},
}

// opusPacketSamples is the number of 48 kHz samples per channel in a packet.
func opusPacketSamples(p []byte) int {
	toc := p[0]
	var fs int // in 1/400 s units... use samples at 48k
	cfg := int(toc >> 3)
	switch {
	case cfg >= 16: // CELT: 2.5/5/10/20 ms
		fs = 120 << uint(cfg&3)
	case cfg >= 12: // hybrid 10/20 ms
		fs = 480 << uint(cfg&1)
	case cfg&3 == 3: // SILK 60 ms
		fs = 2880
	default: // SILK 10/20/40 ms
		fs = 480 << uint(cfg&3)
	}
	frames := 1
	switch toc & 3 {
	case 1, 2:
		frames = 2
	case 3:
		if len(p) < 2 {
			return 0
		}
		frames = int(p[1] & 0x3f)
	}
	return fs * frames
}
