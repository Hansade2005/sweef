package media

import (
	"crypto/md5"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNativeVideoFixtures decodes the committed clips with the pure-Go
// decoders (ffmpeg disabled) and compares every frame, in presentation
// order, with the MD5 that ffmpeg's decoder produced for it.
func TestNativeVideoFixtures(t *testing.T) {
	SetFFmpegEnabled(false)
	defer SetFFmpegEnabled(true)
	files, _ := filepath.Glob("testdata/*.md5")
	if len(files) == 0 {
		t.Fatal("no fixtures")
	}
	for _, f := range files {
		base := strings.TrimSuffix(f, ".md5")
		var clip string
		for _, ext := range []string{".mp4", ".webm"} {
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
			frames, _, _ := decodeFramesNative(t, data)
			if len(frames) != len(wl) {
				t.Fatalf("decoded %d frames, reference has %d", len(frames), len(wl))
			}
			for i, fr := range frames {
				sum := md5.Sum(fr)
				if got := hex.EncodeToString(sum[:]); got != wl[i] {
					t.Fatalf("frame %d: md5 %s want %s", i, got, wl[i])
				}
			}
			// the FrameSource API must pick the native tier
			info, err := ProbeBytes(data)
			if err != nil {
				t.Fatal(err)
			}
			fs, err := OpenFrames(BytesSource(data), info, "")
			if err != nil {
				t.Fatal(err)
			}
			defer fs.Close()
			if fs.Mode() != "native" {
				t.Fatalf("mode %q", fs.Mode())
			}
			if _, err := fs.Frame(0.5); err != nil {
				t.Fatal(err)
			}
		})
	}
}
