package media

import (
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const testMediaDir = "../engine/testdata/media"

type ffStream struct {
	CodecType  string `json:"codec_type"`
	CodecName  string `json:"codec_name"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	SampleRate string `json:"sample_rate"`
	Channels   int    `json:"channels"`
	RFrameRate string `json:"avg_frame_rate"`
	Duration   string `json:"duration"`
}
type ffProbe struct {
	Streams []ffStream `json:"streams"`
	Format  struct {
		Duration   string `json:"duration"`
		FormatName string `json:"format_name"`
	} `json:"format"`
}

func ffprobe(t *testing.T, path string) *ffProbe {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error", "-show_format", "-show_streams", "-of", "json", path).Output()
	if err != nil {
		t.Skipf("ffprobe unavailable: %v", err)
	}
	var p ffProbe
	if err := json.Unmarshal(out, &p); err != nil {
		t.Fatal(err)
	}
	return &p
}

func TestProbeAgainstFFprobe(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(testMediaDir, "*"))
	checked := 0
	for _, f := range files {
		ext := filepath.Ext(f)
		switch ext {
		case ".wav", ".mp3", ".ogg", ".flac", ".opus", ".m4a", ".aac", ".webm", ".mp4", ".avi", ".gif":
		default:
			continue
		}
		f := f
		if strings.HasPrefix(filepath.Base(f), "garbage") {
			b, _ := os.ReadFile(f)
			if _, err := ProbeBytes(b); err == nil {
				t.Errorf("garbage fixture must not probe")
			}
			continue
		}
		t.Run(filepath.Base(f), func(t *testing.T) {
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			info, err := ProbeBytes(b)
			if err != nil {
				t.Fatalf("probe: %v", err)
			}
			ref := ffprobe(t, f)
			fd, _ := strconv.ParseFloat(ref.Format.Duration, 64)
			tol := 0.06
			if math.Abs(info.Duration-fd) > tol {
				t.Errorf("duration %.4f vs ffprobe %.4f", info.Duration, fd)
			}
			for _, rs := range ref.Streams {
				if rs.CodecType != "audio" && rs.CodecType != "video" {
					continue
				}
				var got *Track
				for i := range info.Tracks {
					if info.Tracks[i].Kind == rs.CodecType {
						got = &info.Tracks[i]
						break
					}
				}
				if got == nil {
					t.Errorf("missing %s track", rs.CodecType)
					continue
				}
				name := got.CodecName
				if name != rs.CodecName && !(rs.CodecName[:3] == "pcm" && name[:3] == "pcm") {
					t.Errorf("%s codec %q vs ffprobe %q", rs.CodecType, name, rs.CodecName)
				}
				if rs.CodecType == "video" {
					if got.Width != rs.Width || got.Height != rs.Height {
						t.Errorf("size %dx%d vs %dx%d", got.Width, got.Height, rs.Width, rs.Height)
					}
					var n, d float64
					if _, err := fmtSscan(rs.RFrameRate, &n, &d); err == nil && d > 0 {
						if math.Abs(got.FrameRate-n/d) > 0.5 {
							t.Errorf("fps %.3f vs %.3f", got.FrameRate, n/d)
						}
					}
				} else {
					sr, _ := strconv.Atoi(rs.SampleRate)
					if got.SampleRate != sr {
						t.Errorf("sample rate %d vs %d", got.SampleRate, sr)
					}
					if got.Channels != rs.Channels {
						t.Errorf("channels %d vs %d", got.Channels, rs.Channels)
					}
				}
			}
			t.Logf("%s: %s dur=%.3f (ffprobe %.3f) tracks=%v mime=%s seek=%d", filepath.Base(f), info.Container, info.Duration, fd, trackSummary(info), info.MIME(), len(info.Seek))
			checked++
		})
	}
	if checked == 0 {
		t.Skip("no fixtures")
	}
}

func trackSummary(i *Info) []string {
	var o []string
	for _, t := range i.Tracks {
		o = append(o, t.Kind+":"+t.Codec)
	}
	return o
}

func fmtSscan(s string, n, d *float64) (int, error) {
	var a, b float64
	c, err := sscanRatio(s, &a, &b)
	*n, *d = a, b
	return c, err
}
