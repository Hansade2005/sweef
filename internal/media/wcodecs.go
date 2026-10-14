package media

// wcodecs.go: the codec back end of WebCodecs (internal/engine/webcodecs*.go). It wraps the pure-Go decoders and
// encoders of this tree behind small, JavaScript-free interfaces:
//
//	video decode   VP8, VP9, H.264 (AVC with a description, or Annex B), HEVC, AV1
//	video encode   VP8 (internal/media/vp8enc)
//	audio decode   Opus, AAC (ASC description or ADTS), MP3, Vorbis, PCM (u8, s16, s24, s32, f32), A-law, mu-law
//	audio encode   Opus
//
// Codec strings are the ones WebCodecs registers (https://www.w3.org/TR/webcodecs-codec-registry/): "vp8",
// "vp09.00.10.08", "avc1.64001f", "hev1.1.6.L93.B0", "av01.0.04M.08", "opus", "mp4a.40.2", "mp3", "vorbis", "pcm-s16",
// "ulaw", "alaw".

import (
	"encoding/binary"
	"errors"
	"image"
	"math"
	"strings"

	"github.com/thesyncim/gopus"

	"github.com/sweefbrowser/sweef/internal/media/h264"
	"github.com/sweefbrowser/sweef/internal/media/hevc"
	"github.com/sweefbrowser/sweef/internal/media/vp8enc"
)

// ---- video decoding --------------------------------------------------------------------------------------

// WCPicture is a decoded picture in the layout WebCodecs hands out: tightly packed planes.
type WCPicture struct {
	Width, Height int
	Format        string   // "I420", "I422", "I444" or "RGBA"
	Planes        [][]byte // one per plane; for RGBA a single interleaved plane
	Strides       []int
	FullRange     bool
	Matrix        string // "bt709" or "smpte170m" for YUV, "rgb" for RGBA
}

// WCVideoName maps a WebCodecs video codec string to the decoder name used here ("" when it is not a video codec
// this tree knows).
func WCVideoName(codec string) string {
	switch {
	case codec == "vp8":
		return "vp8"
	case codec == "vp9" || strings.HasPrefix(codec, "vp09."):
		return "vp9"
	case strings.HasPrefix(codec, "avc1.") || strings.HasPrefix(codec, "avc3."):
		return "h264"
	case strings.HasPrefix(codec, "hev1.") || strings.HasPrefix(codec, "hvc1."):
		return "hevc"
	case strings.HasPrefix(codec, "av01."):
		return "av1"
	}
	return ""
}

// WCVideoDecoder decodes one video elementary stream.
type WCVideoDecoder struct {
	name string
	dec  vdecoder
	pd   picDecoder
	h264 *h264.Decoder
	hevc *hevc.Decoder
}

// DecodeOrder reports how pictures relate to chunks: true when the picture Decode returns is the one the chunk
// carries (the caller reorders by timestamp, ReorderDepth pictures deep); false for HEVC, whose decoder outputs
// in display order with its own delay (the caller gives the pictures the smallest outstanding timestamps).
func (d *WCVideoDecoder) DecodeOrder() bool { return d.hevc == nil }

// ReorderDepth is how many pictures a consumer ordering by timestamp has to hold back (H.264 only).
func (d *WCVideoDecoder) ReorderDepth() int {
	if d.h264 != nil {
		return d.h264.ReorderDepth()
	}
	return 0
}

// NewWCVideoDecoder creates a decoder. desc is the codec description (avcC, hvcC, av1C) or nil: H.264 without a
// description is read as Annex B, as WebCodecs defines.
func NewWCVideoDecoder(codec string, desc []byte) (*WCVideoDecoder, error) {
	name := WCVideoName(codec)
	if name == "" {
		return nil, ErrUnsupported
	}
	var d vdecoder
	w := &WCVideoDecoder{name: name}
	switch name {
	case "h264":
		h := h264.NewDecoder()
		if len(desc) > 0 {
			if err := h.SetAVCC(desc); err != nil {
				return nil, err
			}
		}
		d = &h264Adapter{h}
		w.h264 = h
	case "hevc":
		h := hevc.NewDecoder()
		if len(desc) > 0 {
			if err := h.SetHVCC(desc); err != nil {
				return nil, err
			}
		}
		d = &hevcAdapter{h}
		w.hevc = h
	default:
		d = newNativeDecoder(&vstream{codec: name, cfg: desc})
		if d == nil {
			return nil, ErrUnsupported
		}
	}
	w.dec = d
	w.pd, _ = d.(picDecoder)
	return w, nil
}

// Decode decodes one access unit (one chunk) and returns the pictures it completes: none for a hidden frame or a
// parameter-set-only unit, usually one. A picture that came with a recoverable error is returned without the error.
func (d *WCVideoDecoder) Decode(data []byte) ([]*WCPicture, error) {
	if d.hevc != nil {
		frames, err := d.hevc.Push(data)
		var out []*WCPicture
		for _, f := range frames {
			out = append(out, wcPicture(framePic(f)))
		}
		if len(out) == 0 {
			return nil, err
		}
		return out, nil
	}
	pic, err := d.decodeOne(data)
	if pic == nil {
		return nil, err
	}
	return []*WCPicture{pic}, nil
}

// Flush returns the pictures still held back by the decoder's own output delay (HEVC).
func (d *WCVideoDecoder) Flush() []*WCPicture {
	if d.hevc == nil {
		return nil
	}
	var out []*WCPicture
	for _, f := range d.hevc.Flush() {
		out = append(out, wcPicture(framePic(f)))
	}
	return out
}

func (d *WCVideoDecoder) decodeOne(data []byte) (*WCPicture, error) {
	var p vpic
	var err error
	if d.pd != nil {
		p, err = d.pd.DecodePic(data)
		// The player keeps plain 8-bit 4:2:0 pictures lazy (converted to RGBA straight from the decoder's planes
		// when shown). WebCodecs hands out the sample planes themselves, so wait for the picture and narrow it.
		if p.lazy && p.frm != nil {
			p.frm.Wait()
			if img, ok := p.frm.ToYCbCr(); ok {
				p = vpic{ycc: img}
			} else {
				p.lazy = false
			}
		}
	} else {
		var ycc *image.YCbCr
		ycc, err = d.dec.Decode(data)
		p = vpic{ycc: ycc}
	}
	if !p.valid() {
		return nil, err
	}
	return wcPicture(p), nil
}

// Reset drops the reference pictures: the next chunk must be a key frame.
func (d *WCVideoDecoder) Reset() { d.dec.Reset() }

func wcPicture(p vpic) *WCPicture {
	w, h := pictureSize(p)
	if p.ycc != nil {
		y := p.ycc
		var format string
		switch y.SubsampleRatio {
		case image.YCbCrSubsampleRatio420:
			format = "I420"
		case image.YCbCrSubsampleRatio422:
			format = "I422"
		case image.YCbCrSubsampleRatio444:
			format = "I444"
		}
		if format != "" {
			cw, ch := w, h
			switch format {
			case "I420":
				cw, ch = (w+1)/2, (h+1)/2
			case "I422":
				cw = (w + 1) / 2
			}
			out := &WCPicture{Width: w, Height: h, Format: format, Matrix: "smpte170m"}
			if h >= 720 {
				out.Matrix = "bt709"
			}
			cp := func(src []byte, stride, pw, ph int) []byte {
				b := make([]byte, pw*ph)
				for r := 0; r < ph; r++ {
					copy(b[r*pw:(r+1)*pw], src[r*stride:r*stride+pw])
				}
				return b
			}
			out.Planes = [][]byte{cp(y.Y, y.YStride, w, h), cp(y.Cb, y.CStride, cw, ch), cp(y.Cr, y.CStride, cw, ch)}
			out.Strides = []int{w, cw, cw}
			return out
		}
	}
	rgba := p.rgba(h >= 720)
	return &WCPicture{Width: w, Height: h, Format: "RGBA", Planes: [][]byte{rgba.Pix}, Strides: []int{rgba.Stride}, Matrix: "rgb"}
}

// ---- video encoding (VP8) -----------------------------------------------------------------------------------

// WCVP8Encoder is a VP8 encoder with the knobs WebCodecs has.
type WCVP8Encoder struct {
	e    *vp8enc.Encoder
	w, h int
}

// NewWCVP8Encoder creates an encoder for w x h frames (at least 16 x 16).
func NewWCVP8Encoder(w, h, bitrate int, fps float64, keyInterval int) (*WCVP8Encoder, error) {
	e, err := vp8enc.New(vp8enc.Config{Width: w, Height: h, Bitrate: bitrate, FrameRate: fps, KeyInterval: keyInterval, Speed: -1})
	if err != nil {
		return nil, err
	}
	return &WCVP8Encoder{e: e, w: w, h: h}, nil
}

// SetBitrate and SetFrameRate reconfigure rate control without a new key frame.
func (e *WCVP8Encoder) SetBitrate(bps int)       { e.e.SetBitrate(bps) }
func (e *WCVP8Encoder) SetFrameRate(fps float64) { e.e.SetFrameRate(fps) }

// Encode encodes one I420 picture. A different size than before resizes the encoder (the output is a key frame).
// data is nil when rate control dropped the frame.
func (e *WCVP8Encoder) Encode(w, h int, y, u, v []byte, ys, cs int, key bool) (data []byte, isKey bool, err error) {
	if w != e.w || h != e.h {
		if err = e.e.Resize(w, h); err != nil {
			return nil, false, err
		}
		e.w, e.h = w, h
	}
	if key {
		e.e.RequestKeyFrame()
	}
	pk, err := e.e.Encode(&vp8enc.Image{W: w, H: h, Y: y, U: u, V: v, YStride: ys, UVStride: cs})
	if err != nil || pk == nil {
		return nil, false, err
	}
	return pk.Data, pk.Key, nil
}

// VP8Keyframe is a convenience for tests: whether a raw VP8 frame is a key frame.
func VP8Keyframe(b []byte) bool { return len(b) > 0 && b[0]&1 == 0 }

// ---- audio decoding ------------------------------------------------------------------------------------------

// WCAudioDecoder decodes one audio stream to planar float32.
type WCAudioDecoder struct {
	dec  AudioDecoder
	pcm  string // "pcm-*", "ulaw", "alaw" for the raw formats
	rate int
	ch   int
	adts bool
	out  [][]float32
}

// WCAudioName maps a codec string to the decoder family ("" if unknown).
func WCAudioName(codec string) string {
	switch {
	case codec == "opus":
		return "opus"
	case strings.HasPrefix(codec, "mp4a.40.") || strings.HasPrefix(codec, "mp4a.67"):
		return "aac"
	case codec == "mp3" || codec == "mp4a.6b" || codec == "mp4a.69":
		return "mp3"
	case codec == "vorbis":
		return "vorbis"
	case codec == "pcm-u8", codec == "pcm-s16", codec == "pcm-s24", codec == "pcm-s32", codec == "pcm-f32", codec == "ulaw", codec == "alaw":
		return codec
	}
	return ""
}

// NewWCAudioDecoder creates a decoder. desc is the codec description (the AudioSpecificConfig, OpusHead, or the
// Vorbis headers in Xiph lacing); adts says that AAC chunks carry ADTS headers (and no description is needed).
func NewWCAudioDecoder(codec string, desc []byte, rate, ch int, adts bool) (*WCAudioDecoder, error) {
	name := WCAudioName(codec)
	if name == "" {
		return nil, ErrUnsupported
	}
	if rate <= 0 || ch <= 0 || ch > 8 {
		return nil, errors.New("invalid sample rate or channel count")
	}
	w := &WCAudioDecoder{rate: rate, ch: ch}
	switch name {
	case "opus":
		if len(desc) == 0 {
			desc = OpusHeadBytes(ch, rate, 0)
		}
		d, err := NewAudioDecoder(TrackConfig{CodecName: "opus", Cfg: desc, CfgKind: "opushead", Channels: ch, SampleRate: rate})
		if err != nil {
			return nil, err
		}
		w.dec = d
	case "aac":
		if adts {
			w.adts = true
			return w, nil // the decoder is created from the first header
		}
		d, err := NewAudioDecoder(TrackConfig{CodecName: "aac", Cfg: desc, CfgKind: "asc", Channels: ch, SampleRate: rate})
		if err != nil {
			return nil, err
		}
		w.dec = d
	case "mp3":
		d, err := NewAudioDecoder(TrackConfig{CodecName: "mp3", Channels: ch, SampleRate: rate})
		if err != nil {
			return nil, err
		}
		w.dec = d
	case "vorbis":
		d, err := NewAudioDecoder(TrackConfig{CodecName: "vorbis", Cfg: desc, Channels: ch, SampleRate: rate})
		if err != nil {
			return nil, err
		}
		w.dec = d
	default:
		w.pcm = name
	}
	return w, nil
}

// Decode decodes one chunk. The result is valid until the next call; it may be empty (priming).
func (d *WCAudioDecoder) Decode(pkt []byte) (planar [][]float32, rate int, err error) {
	if d.pcm != "" {
		out, err := decodeRawAudio(d.pcm, pkt, d.ch, &d.out)
		return out, d.rate, err
	}
	if d.adts {
		hdr, body, err := splitADTS(pkt)
		if err != nil {
			return nil, d.rate, err
		}
		if d.dec == nil {
			dd, err := NewAudioDecoder(TrackConfig{CodecName: "aac", Cfg: hdr, CfgKind: "asc"})
			if err != nil {
				return nil, d.rate, err
			}
			d.dec = dd
		}
		pkt = body
	}
	out, err := d.dec.Decode(pkt)
	if err != nil {
		return nil, d.rate, err
	}
	return out, d.dec.Rate(), nil
}

// Reset drops decoder history.
func (d *WCAudioDecoder) Reset() {
	if d.dec != nil {
		d.dec.Reset()
	}
}

// Channels is the decoder's output channel count.
func (d *WCAudioDecoder) Channels() int {
	if d.dec != nil {
		return d.dec.Channels()
	}
	return d.ch
}

func decodeRawAudio(kind string, b []byte, ch int, scratch *[][]float32) ([][]float32, error) {
	var width int
	switch kind {
	case "pcm-u8", "ulaw", "alaw":
		width = 1
	case "pcm-s16":
		width = 2
	case "pcm-s24":
		width = 3
	default:
		width = 4
	}
	fr := len(b) / (width * ch)
	if len(*scratch) != ch {
		*scratch = make([][]float32, ch)
	}
	out := *scratch
	for c := range out {
		if cap(out[c]) < fr {
			out[c] = make([]float32, fr)
		}
		out[c] = out[c][:fr]
	}
	for i := 0; i < fr; i++ {
		for c := 0; c < ch; c++ {
			p := b[(i*ch+c)*width:]
			var v float32
			switch kind {
			case "pcm-u8":
				v = (float32(p[0]) - 128) / 128
			case "pcm-s16":
				v = float32(int16(binary.LittleEndian.Uint16(p))) / 32768
			case "pcm-s24":
				x := int32(p[0]) | int32(p[1])<<8 | int32(p[2])<<16
				x = x << 8 >> 8
				v = float32(x) / 8388608
			case "pcm-s32":
				v = float32(int32(binary.LittleEndian.Uint32(p))) / 2147483648
			case "pcm-f32":
				v = math.Float32frombits(binary.LittleEndian.Uint32(p))
			case "ulaw":
				v = float32(ulawToLinear(p[0])) / 32768
			case "alaw":
				v = float32(alawToLinear(p[0])) / 32768
			}
			out[c][i] = v
		}
	}
	return out, nil
}

func ulawToLinear(u byte) int16 {
	u = ^u
	t := (int(u&0x0f) << 3) + 0x84
	t <<= (u & 0x70) >> 4
	if u&0x80 != 0 {
		return int16(0x84 - t)
	}
	return int16(t - 0x84)
}

func alawToLinear(a byte) int16 {
	a ^= 0x55
	t := int(a&0x0f) << 4
	seg := (a & 0x70) >> 4
	switch seg {
	case 0:
		t += 8
	case 1:
		t += 0x108
	default:
		t += 0x108
		t <<= seg - 1
	}
	if a&0x80 != 0 {
		return int16(t)
	}
	return int16(-t)
}

// splitADTS returns the AudioSpecificConfig implied by an ADTS header and the raw data block that follows it.
func splitADTS(p []byte) (asc, body []byte, err error) {
	if len(p) < 7 || p[0] != 0xff || p[1]&0xf0 != 0xf0 {
		return nil, nil, errors.New("not an ADTS frame")
	}
	profile := int(p[2]>>6) & 3 // audio object type - 1
	freq := int(p[2]>>2) & 15
	chans := int(p[2]&1)<<2 | int(p[3]>>6)
	hlen := 7
	if p[1]&1 == 0 { // protection_absent == 0: a CRC follows
		hlen = 9
	}
	if len(p) < hlen {
		return nil, nil, errors.New("truncated ADTS frame")
	}
	aot := profile + 1
	asc = []byte{byte(aot<<3 | freq>>1), byte(freq<<7 | chans<<3)}
	return asc, p[hlen:], nil
}

// ---- audio encoding (Opus) ------------------------------------------------------------------------------------

// WCOpusEncoder encodes interleaved float32 PCM to Opus packets of a fixed duration.
type WCOpusEncoder struct {
	enc          *gopus.Encoder
	rate, ch     int
	frameSamples int
	buf          []byte
}

// NewWCOpusEncoder creates an encoder. frameUs is the packet duration in microseconds (2500, 5000, 10000, 20000,
// 40000 or 60000; 0 means 20000); application is "audio" (default), "voip" or "lowdelay".
func NewWCOpusEncoder(rate, ch, bitrate, frameUs int, application string) (*WCOpusEncoder, error) {
	app := gopus.ApplicationAudio
	switch application {
	case "voip":
		app = gopus.ApplicationVoIP
	case "lowdelay":
		app = gopus.ApplicationLowDelay
	}
	enc, err := gopus.NewEncoder(gopus.EncoderConfig{SampleRate: rate, Channels: ch, Application: app})
	if err != nil {
		return nil, err
	}
	if frameUs == 0 {
		frameUs = 20000
	}
	switch frameUs {
	case 2500, 5000, 10000, 20000, 40000, 60000:
	default:
		return nil, errors.New("unsupported Opus frame duration")
	}
	n := rate * frameUs / 1000000
	if bitrate > 0 {
		if err := enc.SetBitrate(bitrate); err != nil {
			return nil, err
		}
	}
	return &WCOpusEncoder{enc: enc, rate: rate, ch: ch, frameSamples: n, buf: make([]byte, 4000)}, nil
}

// FrameSamples is the number of samples per channel in one packet.
func (e *WCOpusEncoder) FrameSamples() int { return e.frameSamples }

// SetBitrate changes the target bitrate.
func (e *WCOpusEncoder) SetBitrate(bps int) { _ = e.enc.SetBitrate(bps) }

// Encode encodes exactly FrameSamples samples per channel (interleaved).
func (e *WCOpusEncoder) Encode(pcm []float32) ([]byte, error) {
	n, err := e.enc.Encode(pcm, e.buf)
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), e.buf[:n]...), nil
}

// OpusHeadBytes builds the identification header (RFC 7845) WebCodecs uses as the Opus decoder description.
func OpusHeadBytes(ch, inputRate, preSkip int) []byte {
	b := make([]byte, 19)
	copy(b, "OpusHead")
	b[8] = 1
	b[9] = byte(ch)
	binary.LittleEndian.PutUint16(b[10:], uint16(preSkip))
	binary.LittleEndian.PutUint32(b[12:], uint32(inputRate))
	return b
}

// AnimatedImageFrames decodes an animated GIF or APNG into composited frames and their display durations in
// seconds; ok is false for still images and other formats.
func AnimatedImageFrames(data []byte) (frames []image.Image, seconds []float64, loops bool, ok bool) {
	fs, anim := OpenAnimatedImage(data)
	if !anim {
		return nil, nil, false, false
	}
	a, isAnim := fs.(*animFrames)
	if !isAnim || len(a.frames) == 0 {
		return nil, nil, false, false
	}
	prev := 0.0
	for _, e := range a.ends {
		seconds = append(seconds, e-prev)
		prev = e
	}
	return a.frames, seconds, a.loop, true
}

// VideoSamples is the first video track of a file as WebCodecs sees it: the codec string, the decoder
// description and the access units in decode order with their presentation times in microseconds.
type VideoSamples struct {
	Codec       string
	Description []byte
	Width       int
	Height      int
	Chunks      []VideoChunk
}

// VideoChunk is one access unit.
type VideoChunk struct {
	Data     []byte
	PTS, Dur int64 // microseconds
	Key      bool
}

// ExtractVideoSamples demuxes the first video track of an MP4 or WebM file (used by the tests of the WebCodecs
// layer, and by tools that need the access units of a clip).
func ExtractVideoSamples(data []byte) (*VideoSamples, error) {
	info, err := ProbeBytes(data)
	if err != nil {
		return nil, err
	}
	v := info.Video()
	if v == nil {
		return nil, errors.New("no video track")
	}
	src := BytesSource(data)
	vs, err := loadVideoStream(src, info)
	if err != nil {
		return nil, err
	}
	out := &VideoSamples{Codec: v.Codec, Description: vs.cfg, Width: v.Width, Height: v.Height}
	for i := range vs.samples {
		b, err := vs.payload(src, i)
		if err != nil {
			return nil, err
		}
		s := vs.samples[i]
		dur := int64(0)
		if v.FrameRate > 0 {
			dur = int64(1e6 / v.FrameRate)
		}
		out.Chunks = append(out.Chunks, VideoChunk{Data: b, PTS: int64(math.Round(s.pts * 1e6)), Dur: dur, Key: s.key})
	}
	return out, nil
}
