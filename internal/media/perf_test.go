package media

import (
	"crypto/md5"
	"encoding/hex"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/media/h264"
	"github.com/sweefbrowser/sweef/internal/media/yuv"
)

// TestFrameHashes prints one MD5 over the raw planes of every picture of a clip, in decode order. It is how
// the speed work on the decoders was checked to change no sample: run it on the old and the new code and
// compare the line it prints.
//
//	SWEEF_HASH_VIDEO=clip.mp4 go test -run TestFrameHashes -v ./internal/media
func TestFrameHashes(t *testing.T) {
	path := os.Getenv("SWEEF_HASH_VIDEO")
	if path == "" {
		t.Skip("set SWEEF_HASH_VIDEO to a video file")
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
	all := md5.New()
	n := 0
	limit, _ := strconv.Atoi(os.Getenv("SWEEF_HASH_FRAMES"))
	for _, s := range v.Snapshot() {
		data, _ := s.Bytes()
		pic, err := dec.Decode(data)
		if err != nil {
			t.Fatal(err)
		}
		if pic.Valid() {
			all.Write(pictureRaw(pic))
			n++
		}
		if limit > 0 && n >= limit {
			break
		}
	}
	t.Logf("HASH %s frames=%d md5=%s", path, n, hex.EncodeToString(all.Sum(nil)))
}

// BenchmarkH264Decode1080p decodes the committed 1080p High profile clip (60 frames, CABAC, B-frames, 8x8
// transform) from the first access unit and reports frames per second and CPU time per frame.
func BenchmarkH264Decode1080p(b *testing.B) {
	data, err := os.ReadFile("testdata/h264_1080p_high.mp4")
	if err != nil {
		b.Fatal(err)
	}
	info, err := ProbeBytes(data)
	if err != nil {
		b.Fatal(err)
	}
	v, _, err := OpenTracks(BytesSource(data), info)
	if err != nil || v == nil {
		b.Fatalf("open: %v", err)
	}
	ss := v.Snapshot()
	payload := make([][]byte, len(ss))
	for i, s := range ss {
		payload[i], _ = s.Bytes()
	}
	b.ResetTimer()
	frames := 0
	cpu0 := cpuSeconds()
	t0 := time.Now()
	for i := 0; i < b.N; i++ {
		dec, err := NewVideoDecoder(v.Config())
		if err != nil {
			b.Fatal(err)
		}
		var pics []Picture
		for _, p := range payload {
			pic, err := dec.Decode(p)
			if err != nil {
				b.Fatal(err)
			}
			pics = append(pics, pic)
			frames++
		}
		for _, p := range pics {
			p.Wait() // pictures may still be in flight when threads are on: count only finished ones
		}
	}
	el := time.Since(t0).Seconds()
	b.ReportMetric(float64(frames)/el, "fps")
	b.ReportMetric((cpuSeconds()-cpu0)*1000/float64(frames), "cpu-ms/frame")
}

// pictureRaw returns the planes of a picture as bytes (see vpicRaw).
func pictureRaw(p Picture) []byte {
	p.Wait()
	return vpicRaw(p.p)
}

// BenchmarkConvert1080p is the conversion of one decoded 1080p picture to RGBA for display: the narrowing copy
// to YCbCr followed by the YCbCr conversion (the former path) and the fused conversion.
func BenchmarkConvert1080p(b *testing.B) {
	data, err := os.ReadFile("testdata/h264_1080p_high.mp4")
	if err != nil {
		b.Fatal(err)
	}
	info, _ := ProbeBytes(data)
	v, _, err := OpenTracks(BytesSource(data), info)
	if err != nil || v == nil {
		b.Fatalf("open: %v", err)
	}
	d := h264.NewDecoder()
	d.SetAVCC(v.Config().Cfg)
	var fr *yuv.Frame
	for _, s := range v.Snapshot() {
		p, _ := s.Bytes()
		if f, _ := d.DecodeFrame(p); f != nil {
			fr = f
			break
		}
	}
	if fr == nil {
		b.Fatal("no picture")
	}
	b.Run("two-step", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			img, _ := fr.ToYCbCr()
			yuvToRGBA(img, true)
		}
	})
	b.Run("fused", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			frameToRGBA(fr, true)
		}
	})
}
