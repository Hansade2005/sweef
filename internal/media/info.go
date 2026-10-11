// Package media holds Sweef's pure-Go media stack: container parsers,
// audio decoders, frame providers and (in subpackage webaudio) the Web Audio
// DSP graph. It has no dependency on the engine so it can be tested against
// ffprobe/ffmpeg directly.
package media

import (
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
)

// Track describes one elementary stream of a container.
type Track struct {
	ID            int     `json:"id"`
	Kind          string  `json:"kind"`      // "audio", "video", "text"
	Codec         string  `json:"codec"`     // RFC 6381 style: avc1.64001e, mp4a.40.2, vp8, vorbis, opus, flac, mp3, 1 (pcm)
	CodecName     string  `json:"codecName"` // ffprobe style: h264, aac, vp8, vorbis, opus, flac, mp3, pcm_s16le
	Width         int     `json:"width,omitempty"`
	Height        int     `json:"height,omitempty"`
	FrameRate     float64 `json:"frameRate,omitempty"`
	SampleRate    int     `json:"sampleRate,omitempty"`
	Channels      int     `json:"channels,omitempty"`
	BitsPerSample int     `json:"bitsPerSample,omitempty"`
	BitRate       int     `json:"bitRate,omitempty"`
	Duration      float64 `json:"duration,omitempty"`
	Language      string  `json:"language,omitempty"`
	Name          string  `json:"name,omitempty"`
	Default       bool    `json:"default,omitempty"`
	Frames        int     `json:"frames,omitempty"`
}

// SeekPoint maps a presentation time to a byte offset (a keyframe/cue/page).
type SeekPoint struct {
	Time   float64 `json:"t"`
	Offset int64   `json:"o"`
}

// Info is the result of probing a media resource.
type Info struct {
	Container  string      `json:"container"` // mp4 webm matroska ogg wav mp3 flac aac avi gif apng
	Brand      string      `json:"brand,omitempty"`
	Duration   float64     `json:"duration"` // seconds; <0 when unknown
	Size       int64       `json:"size"`
	Tracks     []Track     `json:"tracks"`
	Seek       []SeekPoint `json:"-"`
	Fragmented bool        `json:"fragmented,omitempty"`
	DataStart  int64       `json:"dataStart"` // first byte of media payload
	DataEnd    int64       `json:"dataEnd"`
	BitRate    int         `json:"bitRate,omitempty"` // average bytes*8/duration of the payload
	Title      string      `json:"title,omitempty"`
	XingFrame  bool        `json:"-"` // mp3: first frame is a Xing/Info header (decodes as silence)
	SkipStart  int         `json:"-"` // mp3: LAME encoder delay (samples)
	SkipEnd    int         `json:"-"` // mp3: LAME padding (samples)
}

// ErrUnknownFormat is returned when no parser recognises the data.
var ErrUnknownFormat = errors.New("media: unrecognised container")

// ErrTruncated means the resource ended inside a structure.
var ErrTruncated = errors.New("media: truncated data")

// Video returns the first video track or nil.
func (i *Info) Video() *Track { return i.first("video") }

// Audio returns the first audio track or nil.
func (i *Info) Audio() *Track { return i.first("audio") }

func (i *Info) first(kind string) *Track {
	for k := range i.Tracks {
		if i.Tracks[k].Kind == kind {
			return &i.Tracks[k]
		}
	}
	return nil
}

// HasVideo reports whether any video track exists.
func (i *Info) HasVideo() bool { return i.Video() != nil }

// MIME is the canonical MIME type including a codecs parameter.
func (i *Info) MIME() string {
	base := ""
	switch i.Container {
	case "mp4":
		if i.HasVideo() {
			base = "video/mp4"
		} else if strings.HasPrefix(i.Brand, "M4A") {
			base = "audio/mp4"
		} else {
			base = "audio/mp4"
		}
	case "webm":
		if i.HasVideo() {
			base = "video/webm"
		} else {
			base = "audio/webm"
		}
	case "matroska":
		base = "video/x-matroska"
	case "ogg":
		if i.HasVideo() {
			base = "video/ogg"
		} else {
			base = "audio/ogg"
		}
	case "wav":
		base = "audio/wav"
	case "mp3":
		base = "audio/mpeg"
	case "flac":
		base = "audio/flac"
	case "aac":
		base = "audio/aac"
	case "avi":
		base = "video/x-msvideo"
	case "gif":
		base = "image/gif"
	default:
		return ""
	}
	var cs []string
	for _, t := range i.Tracks {
		if t.Codec != "" && t.Kind != "text" {
			cs = append(cs, t.Codec)
		}
	}
	if len(cs) > 0 && i.Container != "wav" && i.Container != "mp3" && i.Container != "aac" && i.Container != "flac" {
		return base + `; codecs="` + strings.Join(cs, ", ") + `"`
	}
	return base
}

// ByteForTime returns the byte offset to start reading from to play at time
// t (nearest preceding seek point, or a linear estimate).
func (i *Info) ByteForTime(t float64) int64 {
	if t <= 0 || i.Duration <= 0 {
		return i.DataStart
	}
	if len(i.Seek) > 0 {
		best := i.Seek[0]
		for _, s := range i.Seek {
			if s.Time <= t {
				best = s
			} else {
				break
			}
		}
		if best.Time <= t {
			return best.Offset
		}
		return i.DataStart
	}
	if t >= i.Duration {
		return i.DataEnd
	}
	return i.DataStart + int64(float64(i.DataEnd-i.DataStart)*t/i.Duration)
}

// TimeForByte is the inverse of ByteForTime.
func (i *Info) TimeForByte(b int64) float64 {
	if b <= i.DataStart {
		return 0
	}
	if b >= i.DataEnd {
		if i.Duration < 0 {
			return 0
		}
		return i.Duration
	}
	if len(i.Seek) > 0 {
		t := 0.0
		for k, s := range i.Seek {
			if s.Offset > b {
				// interpolate between previous and this point
				if k == 0 {
					return 0
				}
				p := i.Seek[k-1]
				if s.Offset > p.Offset {
					return p.Time + (s.Time-p.Time)*float64(b-p.Offset)/float64(s.Offset-p.Offset)
				}
				return p.Time
			}
			t = s.Time
		}
		// after the last seek point
		last := i.Seek[len(i.Seek)-1]
		if i.Duration > last.Time && i.DataEnd > last.Offset {
			return last.Time + (i.Duration-last.Time)*float64(b-last.Offset)/float64(i.DataEnd-last.Offset)
		}
		return t
	}
	if i.Duration <= 0 || i.DataEnd <= i.DataStart {
		return 0
	}
	return i.Duration * float64(b-i.DataStart) / float64(i.DataEnd-i.DataStart)
}

// Probe sniffs and parses the container of r.
func Probe(r io.ReaderAt, size int64) (*Info, error) {
	head := make([]byte, 64)
	n, _ := r.ReadAt(head, 0)
	head = head[:n]
	if len(head) < 4 {
		return nil, ErrUnknownFormat
	}
	var info *Info
	var err error
	switch {
	case len(head) >= 12 && string(head[4:8]) == "ftyp", len(head) >= 12 && (string(head[4:8]) == "moov" || string(head[4:8]) == "mdat" || string(head[4:8]) == "free" || string(head[4:8]) == "styp" || string(head[4:8]) == "moof"):
		info, err = probeMP4(r, size)
	case head[0] == 0x1A && head[1] == 0x45 && head[2] == 0xDF && head[3] == 0xA3:
		info, err = probeEBML(r, size)
	case string(head[:4]) == "OggS":
		info, err = probeOgg(r, size)
	case string(head[:4]) == "RIFF" && len(head) >= 12 && string(head[8:12]) == "WAVE":
		info, err = probeWAV(r, size)
	case string(head[:4]) == "RIFF" && len(head) >= 12 && string(head[8:12]) == "AVI ":
		info, err = probeAVI(r, size)
	case string(head[:4]) == "fLaC":
		info, err = probeFLAC(r, size)
	case string(head[:3]) == "GIF":
		info, err = probeGIF(r, size)
	case string(head[:3]) == "ID3" || isMP3Sync(head) || isADTS(head):
		info, err = probeMPEGAudio(r, size)
	default:
		// last chance: MP3 without ID3 but with junk before sync
		if i, e := probeMPEGAudio(r, size); e == nil && len(i.Tracks) > 0 {
			return i, nil
		}
		return nil, ErrUnknownFormat
	}
	if err != nil {
		return nil, err
	}
	info.Size = size
	if info.DataEnd == 0 || info.DataEnd > size {
		info.DataEnd = size
	}
	if info.Duration > 0 && info.BitRate == 0 && info.DataEnd > info.DataStart {
		info.BitRate = int(float64(info.DataEnd-info.DataStart) * 8 / info.Duration)
	}
	return info, nil
}

// ProbeBytes is Probe over an in-memory buffer.
func ProbeBytes(b []byte) (*Info, error) { return Probe(bytesReaderAt(b), int64(len(b))) }

type bytesReaderAt []byte

func (b bytesReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(b)) {
		return 0, io.EOF
	}
	n := copy(p, b[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// readFull reads exactly n bytes at off.
func readFull(r io.ReaderAt, off int64, n int) ([]byte, error) {
	if n < 0 || n > 1<<30 {
		return nil, fmt.Errorf("media: bad read length %d", n)
	}
	b := make([]byte, n)
	m, err := r.ReadAt(b, off)
	if m == n {
		return b, nil
	}
	if err == nil || err == io.EOF {
		err = ErrTruncated
	}
	return b[:m], err
}

func be16(b []byte) int { return int(b[0])<<8 | int(b[1]) }
func be24(b []byte) int { return int(b[0])<<16 | int(b[1])<<8 | int(b[2]) }
func be32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}
func be64(b []byte) uint64 { return uint64(be32(b))<<32 | uint64(be32(b[4:])) }
func le16(b []byte) int    { return int(b[0]) | int(b[1])<<8 }
func le32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}
func le64(b []byte) uint64 { return uint64(le32(b)) | uint64(le32(b[4:]))<<32 }

func round3(f float64) float64 { return math.Round(f*1000) / 1000 }
