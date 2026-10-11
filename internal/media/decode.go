package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"github.com/hajimehoshi/go-mp3"
	"github.com/jfreymuth/oggvorbis"
)

// PCM is decoded audio: planar float32 in [-1, 1].
type PCM struct {
	SampleRate int
	Data       [][]float32
}

// Frames is the sample-frame count.
func (p *PCM) Frames() int {
	if len(p.Data) == 0 {
		return 0
	}
	return len(p.Data[0])
}

// Duration in seconds.
func (p *PCM) Duration() float64 {
	if p.SampleRate == 0 {
		return 0
	}
	return float64(p.Frames()) / float64(p.SampleRate)
}

// ErrUnsupported means no decoder is available for the stream.
var ErrUnsupported = errors.New("media: unsupported codec")

var (
	ffOnce     sync.Once
	ffPath     string
	ffDisabled bool
)

// FFmpegPath returns the ffmpeg binary or "" when unavailable (or disabled
// through SWEEF_NO_FFMPEG=1 / SetFFmpegEnabled(false)).
func FFmpegPath() string {
	ffOnce.Do(func() {
		if os.Getenv("SWEEF_NO_FFMPEG") != "" {
			ffDisabled = true
			return
		}
		if p, err := exec.LookPath("ffmpeg"); err == nil {
			ffPath = p
		} else if _, err := os.Stat("/usr/bin/ffmpeg"); err == nil {
			ffPath = "/usr/bin/ffmpeg"
		}
	})
	if ffDisabled {
		return ""
	}
	return ffPath
}

var ffMu sync.Mutex

// SetFFmpegEnabled toggles the use of ffmpeg process-wide (tests).
func SetFFmpegEnabled(on bool) {
	FFmpegPath()
	ffMu.Lock()
	defer ffMu.Unlock()
	if on {
		ffDisabled = os.Getenv("SWEEF_NO_FFMPEG") != ""
		if ffPath == "" {
			if p, err := exec.LookPath("ffmpeg"); err == nil {
				ffPath = p
			}
		}
	} else {
		ffDisabled = true
	}
}

// NativeAudio reports whether the codec of a container is decoded by pure Go.
func NativeAudio(info *Info) bool {
	a := info.Audio()
	if a == nil {
		return false
	}
	switch info.Container {
	case "wav":
		switch a.CodecName {
		case "pcm_u8", "pcm_s16le", "pcm_s24le", "pcm_s32le", "pcm_f32le", "pcm_f64le", "pcm_alaw", "pcm_mulaw":
			return true
		}
	case "mp3":
		return a.CodecName == "mp3"
	case "ogg":
		if a.CodecName == "opus" {
			return true
		}
		return a.CodecName == "vorbis" && !info.HasVideo()
	case "webm", "matroska", "mp4":
		return a.CodecName == "opus" || nativeAAC(a)
	case "aac":
		return nativeAAC(a)
	case "flac":
		return true
	}
	return false
}

// DecodeAudio decodes a complete resource to PCM at its native rate.
func DecodeAudio(data []byte) (*PCM, error) {
	info, err := ProbeBytes(data)
	if err != nil {
		return nil, err
	}
	if info.Audio() == nil {
		return nil, fmt.Errorf("media: no audio track")
	}
	if NativeAudio(info) {
		if a := info.Audio(); a.CodecName == "opus" || a.CodecName == "aac" {
			return decodeNativeCodec(data, info)
		}
		switch info.Container {
		case "wav":
			return decodeWAV(data)
		case "mp3":
			return decodeMP3(data)
		case "ogg":
			return decodeVorbis(data)
		case "flac":
			return DecodeFLAC(data)
		}
	}
	if FFmpegPath() != "" {
		a := info.Audio()
		return DecodeAudioFFmpeg(data, a.SampleRate, a.Channels)
	}
	return nil, ErrUnsupported
}

func decodeWAV(data []byte) (*PCM, error) {
	f, err := parseWAV(bytesReaderAt(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	bytesPer := f.Bits / 8
	if f.Tag == 6 || f.Tag == 7 {
		bytesPer = 1
	}
	if bytesPer == 0 || f.Channels == 0 {
		return nil, ErrUnsupported
	}
	end := f.DataOff + f.DataLen
	if end > int64(len(data)) {
		end = int64(len(data))
	}
	d := data[f.DataOff:end]
	frames := len(d) / (bytesPer * f.Channels)
	out := &PCM{SampleRate: f.SampleRate, Data: make([][]float32, f.Channels)}
	for c := range out.Data {
		out.Data[c] = make([]float32, frames)
	}
	p := 0
	for i := 0; i < frames; i++ {
		for c := 0; c < f.Channels; c++ {
			var v float32
			switch {
			case f.Tag == 1 && f.Bits == 8:
				v = (float32(d[p]) - 128) / 128
			case f.Tag == 1 && f.Bits == 16:
				v = float32(int16(binary.LittleEndian.Uint16(d[p:]))) / 32768
			case f.Tag == 1 && f.Bits == 24:
				x := int32(d[p]) | int32(d[p+1])<<8 | int32(int8(d[p+2]))<<16
				v = float32(x) / 8388608
			case f.Tag == 1 && f.Bits == 32:
				v = float32(int32(binary.LittleEndian.Uint32(d[p:]))) / 2147483648
			case f.Tag == 3 && f.Bits == 32:
				v = math.Float32frombits(binary.LittleEndian.Uint32(d[p:]))
			case f.Tag == 3 && f.Bits == 64:
				v = float32(math.Float64frombits(binary.LittleEndian.Uint64(d[p:])))
			case f.Tag == 6:
				v = float32(alaw(d[p])) / 32768
			case f.Tag == 7:
				v = float32(mulaw(d[p])) / 32768
			default:
				return nil, ErrUnsupported
			}
			out.Data[c][i] = v
			p += bytesPer
		}
	}
	return out, nil
}

func alaw(a byte) int {
	a ^= 0x55
	t := int(a&0x0f) << 4
	seg := int(a&0x70) >> 4
	switch seg {
	case 0:
		t += 8
	case 1:
		t += 0x108
	default:
		t += 0x108
		t <<= uint(seg - 1)
	}
	if a&0x80 != 0 {
		return t
	}
	return -t
}

func mulaw(u byte) int {
	u = ^u
	t := (int(u&0x0f)<<3 + 0x84) << (uint(u&0x70) >> 4)
	if u&0x80 != 0 {
		return 0x84 - t
	}
	return t - 0x84
}

func decodeMP3(data []byte) (*PCM, error) {
	d, err := mp3.NewDecoder(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(d)
	if err != nil {
		return nil, err
	}
	frames := len(raw) / 4
	out := &PCM{SampleRate: d.SampleRate(), Data: [][]float32{make([]float32, frames), make([]float32, frames)}}
	for i := 0; i < frames; i++ {
		out.Data[0][i] = float32(int16(binary.LittleEndian.Uint16(raw[i*4:]))) / 32768
		out.Data[1][i] = float32(int16(binary.LittleEndian.Uint16(raw[i*4+2:]))) / 32768
	}
	// go-mp3 always emits stereo; fold back to mono sources. Trim the
	// encoder delay/padding and the Xing frame the way ffmpeg does so the
	// length matches Chromium's decodeAudioData.
	if info, err := ProbeBytes(data); err == nil {
		if a := info.Audio(); a != nil && a.Channels == 1 {
			out.Data = out.Data[:1]
		}
		start, end := 0, 0
		if info.XingFrame {
			start += 1152
			if a := info.Audio(); a != nil && a.SampleRate < 32000 {
				start = 576
			}
		}
		if info.SkipStart > 0 {
			start += info.SkipStart + 529
		}
		if info.SkipEnd > 529 {
			end = info.SkipEnd - 529
		}
		n := out.Frames()
		if start+end < n {
			for c := range out.Data {
				out.Data[c] = out.Data[c][start : n-end]
			}
		}
	}
	return out, nil
}

func decodeVorbis(data []byte) (*PCM, error) {
	samples, f, err := oggvorbis.ReadAll(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	ch := f.Channels
	if ch <= 0 {
		return nil, fmt.Errorf("media: vorbis channels")
	}
	frames := len(samples) / ch
	out := &PCM{SampleRate: f.SampleRate, Data: make([][]float32, ch)}
	for c := range out.Data {
		out.Data[c] = make([]float32, frames)
	}
	for i := 0; i < frames; i++ {
		for c := 0; c < ch; c++ {
			out.Data[c][i] = samples[i*ch+c]
		}
	}
	return out, nil
}

// DecodeAudioFFmpeg decodes anything ffmpeg understands. rate/channels may
// be 0 to keep the source layout (then mono/44100 is assumed for the raw read).
func DecodeAudioFFmpeg(data []byte, rate, channels int) (*PCM, error) {
	ff := FFmpegPath()
	if ff == "" {
		return nil, ErrUnsupported
	}
	tmp, err := os.CreateTemp("", "sweef-audio-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return nil, err
	}
	tmp.Close()
	if channels <= 0 {
		channels = 2
	}
	if rate <= 0 {
		rate = 44100
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	args := []string{"-v", "error", "-nostdin", "-i", tmp.Name(), "-vn", "-sn", "-map", "0:a:0", "-f", "f32le", "-acodec", "pcm_f32le", "-ac", strconv.Itoa(channels), "-ar", strconv.Itoa(rate), "pipe:1"}
	cmd := exec.CommandContext(ctx, ff, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	raw, err := cmd.Output()
	if err != nil || len(raw) < 4 {
		return nil, fmt.Errorf("media: ffmpeg decode failed: %v %s", err, stderr.String())
	}
	frames := len(raw) / (4 * channels)
	out := &PCM{SampleRate: rate, Data: make([][]float32, channels)}
	for c := range out.Data {
		out.Data[c] = make([]float32, frames)
	}
	for i := 0; i < frames; i++ {
		for c := 0; c < channels; c++ {
			out.Data[c][i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[(i*channels+c)*4:]))
		}
	}
	return out, nil
}

// Resample converts PCM to another rate with a windowed-sinc kernel.
func Resample(p *PCM, to int) *PCM {
	if p.SampleRate == to || to <= 0 {
		return p
	}
	ratio := float64(to) / float64(p.SampleRate)
	n := int(math.Round(float64(p.Frames()) * ratio))
	out := &PCM{SampleRate: to, Data: make([][]float32, len(p.Data))}
	cut := math.Min(1, ratio)
	const half = 16
	for c, src := range p.Data {
		dst := make([]float32, n)
		for i := 0; i < n; i++ {
			pos := float64(i) / ratio
			ip := int(math.Floor(pos))
			var sum float64
			for k := ip - half + 1; k <= ip+half; k++ {
				if k < 0 || k >= len(src) {
					continue
				}
				x := pos - float64(k)
				w := 0.5 + 0.5*math.Cos(math.Pi*x/half)
				var s float64
				if x == 0 {
					s = 1
				} else {
					s = math.Sin(math.Pi*x*cut) / (math.Pi * x * cut)
				}
				sum += float64(src[k]) * s * w
			}
			dst[i] = float32(sum)
		}
		out.Data[c] = dst
	}
	return out
}

// decodeNativeCodec decodes the Opus or AAC track of any container natively.
func decodeNativeCodec(data []byte, info *Info) (*PCM, error) {
	as, err := loadAudioStream(data, info)
	if err != nil {
		return nil, err
	}
	switch as.codec {
	case "opus":
		return decodeOpusStream(as)
	case "aac":
		return decodeAACStream(data, as)
	}
	return nil, ErrUnsupported
}
