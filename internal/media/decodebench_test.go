package media

import (
	"os"
	"sort"
	"strconv"
	"testing"
	"time"
)

// TestDecodeSpeed measures how fast the pure-Go video decoders run on a real file:
//
//	SWEEF_BENCH_VIDEO=/path/clip.mp4 go test -run TestDecodeSpeed -v ./internal/media
//
// It decodes every frame in order (as playback does) and reports the median and the 95th percentile of
// the decode time per frame, the frames per second, and the cost of converting a picture to RGBA.
func TestDecodeSpeed(t *testing.T) {
	path := os.Getenv("SWEEF_BENCH_VIDEO")
	if path == "" {
		t.Skip("set SWEEF_BENCH_VIDEO to a video file")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := ProbeBytes(b)
	if err != nil {
		t.Fatal(err)
	}
	v, _, err := OpenTracks(BytesSource(b), info)
	if err != nil || v == nil {
		t.Fatalf("open: %v", err)
	}
	dec, err := NewVideoDecoder(v.Config())
	if err != nil {
		t.Fatal(err)
	}
	ss := v.Snapshot()
	if n, _ := strconv.Atoi(os.Getenv("SWEEF_BENCH_FRAMES")); n > 0 && n < len(ss) {
		ss = ss[:n]
	}
	var per []float64
	var conv []float64
	var disp, dispN int // frames nothing refers to (a late player skips them): count and decode time
	var dispMS float64
	t0 := time.Now()
	cpu0 := cpuSeconds()
	var lastPic Picture
	for _, s := range ss {
		data, _ := s.Bytes()
		d := dec.Disposable(data)
		t1 := time.Now()
		pic, err := dec.Decode(data)
		if err != nil {
			t.Fatal(err)
		}
		per = append(per, float64(time.Since(t1).Microseconds())/1000)
		if d {
			disp++
			dispMS += per[len(per)-1]
		}
		dispN++
		if pic.Valid() {
			lastPic = pic
		}
	}
	total := time.Since(t0)
	cpu := cpuSeconds() - cpu0
	for i := 0; i < 10; i++ {
		t1 := time.Now()
		_ = lastPic.RGBA()
		conv = append(conv, float64(time.Since(t1).Microseconds())/1000)
	}
	sort.Float64s(per)
	sort.Float64s(conv)
	c := v.Config()
	t.Logf("%s %dx%d %s: %d frames in %.2fs = %.1f fps; decode ms/frame median %.1f p95 %.1f max %.1f; CPU %.1f ms/frame; RGBA conversion median %.1f ms",
		path, c.Width, c.Height, c.CodecName, len(ss), total.Seconds(), float64(len(ss))/total.Seconds(),
		per[len(per)/2], per[len(per)*95/100], per[len(per)-1], cpu*1000/float64(len(ss)), conv[len(conv)/2])
	t.Logf("non-reference frames (a late player skips these): %d of %d, %.0f%% of the decode time", disp, dispN, 100*dispMS/total.Seconds()/1000)
}
