package media

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func load(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(testMediaDir, name))
	if err != nil {
		t.Skip(err)
	}
	return b
}

// bestLagError returns the minimal mean abs error over lags in [-maxLag, maxLag].
func bestLagError(a, b []float32, maxLag int) (float64, int) {
	best, bl := math.Inf(1), 0
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	n -= 2 * maxLag
	for lag := -maxLag; lag <= maxLag; lag++ {
		var e float64
		for i := maxLag; i < maxLag+n; i += 3 {
			e += math.Abs(float64(a[i]) - float64(b[i+lag]))
		}
		e /= float64(n / 3)
		if e < best {
			best, bl = e, lag
		}
	}
	return best, bl
}

func TestNativeDecodersMatchFFmpeg(t *testing.T) {
	if FFmpegPath() == "" {
		t.Skip("no ffmpeg")
	}
	cases := []struct {
		file string
		tol  float64
		lag  int
	}{
		{"tone.wav", 1e-6, 0},
		{"tone.flac", 1e-5, 0},
		{"tone.ogg", 2e-4, 0},
		{"tone.mp3", 2e-2, 2400},
	}
	for _, c := range cases {
		data := load(t, c.file)
		info, err := ProbeBytes(data)
		if err != nil {
			t.Fatal(err)
		}
		if !NativeAudio(info) {
			t.Errorf("%s should be native", c.file)
		}
		got, err := DecodeAudio(data)
		if err != nil {
			t.Errorf("%s: %v", c.file, err)
			continue
		}
		ref, err := DecodeAudioFFmpeg(data, got.SampleRate, len(got.Data))
		if err != nil {
			t.Fatal(err)
		}
		e, lag := bestLagError(got.Data[0], ref.Data[0], c.lag)
		t.Logf("%s: frames %d (ffmpeg %d) rate %d ch %d meanAbsErr=%.2e lag=%d", c.file, got.Frames(), ref.Frames(), got.SampleRate, len(got.Data), e, lag)
		if e > c.tol {
			t.Errorf("%s mean abs error %.3e > %.3e", c.file, e, c.tol)
		}
	}
}

func TestFFmpegAudioFallback(t *testing.T) {
	if FFmpegPath() == "" {
		t.Skip("no ffmpeg")
	}
	for _, f := range []string{"tone.m4a", "tone.opus", "clip.mp4", "clip.webm", "tone.aac"} {
		p, err := DecodeAudio(load(t, f))
		if err != nil || p.Frames() < 10000 {
			t.Errorf("%s: %v frames=%v", f, err, p)
		}
	}
	SetFFmpegEnabled(false)
	defer SetFFmpegEnabled(true)
	if _, err := DecodeAudio(load(t, "tone.m4a")); err != nil {
		t.Errorf("AAC must decode natively without ffmpeg: %v", err)
	}
	if _, err := DecodeAudio(load(t, "tone.mp3")); err != nil {
		t.Errorf("mp3 must stay native: %v", err)
	}
}

func TestResample(t *testing.T) {
	n := 4410
	src := make([]float32, n)
	for i := range src {
		src[i] = float32(math.Sin(2 * math.Pi * 440 * float64(i) / 44100))
	}
	out := Resample(&PCM{SampleRate: 44100, Data: [][]float32{src}}, 48000)
	var maxErr float64
	for i := 100; i < len(out.Data[0])-100; i++ {
		want := math.Sin(2 * math.Pi * 440 * float64(i) / 48000)
		maxErr = math.Max(maxErr, math.Abs(want-float64(out.Data[0][i])))
	}
	if maxErr > 5e-3 {
		t.Fatalf("resample error %g", maxErr)
	}
}
