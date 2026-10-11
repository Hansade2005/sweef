package media

import (
	"crypto/md5"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFormatFixtures decodes the committed high bit depth, 4:2:2 / 4:4:4 /
// 4:4:0, monochrome and interlaced clips with the pure-Go decoders (no
// ffmpeg needed) and compares every frame, in presentation order, with the
// MD5 of ffmpeg's rawvideo output in the clip's native pixel format.
func TestFormatFixtures(t *testing.T) {
	files, _ := filepath.Glob("testdata/fmt/*.md5")
	if len(files) == 0 {
		t.Fatal("no fixtures")
	}
	for _, f := range files {
		base := strings.TrimSuffix(f, ".md5")
		var clip string
		for _, ext := range []string{".mp4", ".webm", ".mkv"} {
			if _, err := os.Stat(base + ext); err == nil {
				clip = base + ext
			}
		}
		if clip == "" {
			continue
		}
		t.Run(filepath.Base(base), func(t *testing.T) {
			data, err := os.ReadFile(clip)
			if err != nil {
				t.Fatal(err)
			}
			want, _ := os.ReadFile(f)
			wl := strings.Fields(string(want))
			frames := decodeRawNative(t, data)
			if len(frames) != len(wl) {
				t.Fatalf("decoded %d frames, reference has %d", len(frames), len(wl))
			}
			for i, fr := range frames {
				sum := md5.Sum(fr)
				if got := hex.EncodeToString(sum[:]); got != wl[i] {
					t.Fatalf("frame %d: md5 %s want %s", i, got, wl[i])
				}
			}
		})
	}
}

// TestCanPlayNativeVideoProfiles: without ffmpeg the pure-Go decoders back
// "probably" for every profile they decode, "maybe" for the Extended profile.
func TestCanPlayNativeVideoProfiles(t *testing.T) {
	SetFFmpegEnabled(false)
	defer SetFFmpegEnabled(true)
	for mime, want := range map[string]string{
		`video/mp4; codecs="avc1.42E01E"`:     "probably",
		`video/mp4; codecs="avc1.64001F"`:     "probably",
		`video/mp4; codecs="avc1.6E0033"`:     "probably", // High 10
		`video/mp4; codecs="avc1.7A0033"`:     "probably", // High 4:2:2
		`video/mp4; codecs="avc1.F4001F"`:     "probably", // High 4:4:4 Predictive
		`video/mp4; codecs="avc1.58A01E"`:     "probably", // Extended
		`video/webm; codecs="vp09.00.10.08"`:  "probably",
		`video/webm; codecs="vp09.01.20.08"`:  "probably",
		`video/webm; codecs="vp09.02.10.10"`:  "probably",
		`video/webm; codecs="vp09.03.10.12"`:  "probably",
		`video/mp4; codecs="hvc1.1.6.L93.B0"`: "",
	} {
		if got := CanPlayType(mime); got != want {
			t.Errorf("CanPlayType(%s) = %q, want %q", mime, got, want)
		}
	}
}
