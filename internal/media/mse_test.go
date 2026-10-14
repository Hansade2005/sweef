package media

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
)

const mseFixtures = "../engine/testdata/media/mse"

func fx(t *testing.T, parts ...string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(append([]string{mseFixtures}, parts...)...))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func seg(rep int, n int, ext string) string { return fmt.Sprintf("chunk-%d-%05d.%s", rep, n, ext) }

func near(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

func wantRanges(t *testing.T, got [][2]float64, want ...[2]float64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("ranges %v, want %v", got, want)
	}
	for i := range got {
		if !near(got[i][0], want[i][0], 0.03) || !near(got[i][1], want[i][1], 0.03) {
			t.Fatalf("ranges %v, want %v", got, want)
		}
	}
}

func TestMSEFragmentedMP4SegmentsInOddChunks(t *testing.T) {
	b := NewMSEBuffer(`video/mp4; codecs="avc1.4d400c"`)
	data := fx(t, "h264", "init-0.m4s")
	r, err := b.Append(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	if r.Init == nil || len(r.Init.Tracks) != 1 || r.Init.Tracks[0].CodecName != "h264" || r.Init.Tracks[0].Width != 320 || r.Init.Tracks[0].Height != 180 {
		t.Fatalf("init %+v", r.Init)
	}
	if len(b.Tracks()[0].Cfg) < 7 {
		t.Fatal("avcC missing from the track configuration")
	}
	// segments arrive in 777-byte pieces
	for n := 1; n <= 3; n++ {
		s := fx(t, "h264", seg(0, n, "m4s"))
		for i := 0; i < len(s); i += 777 {
			e := min(i+777, len(s))
			if _, err := b.Append(s[i:e], 0); err != nil {
				t.Fatal(err)
			}
		}
	}
	wantRanges(t, b.Buffered(), [2]float64{0, 6})
	vs := b.FirstStore("video")
	if vs.Len() != 150 {
		t.Fatalf("%d video frames, want 150", vs.Len())
	}
	// first picture shown at 0 thanks to the edit list; decode order starts before it (B-frames)
	s, ok := vs.ShownAt(0)
	if !ok || !near(s.PTS, 0, 1e-6) || !s.Key {
		t.Fatalf("shown at 0: %+v %v", s, ok)
	}
	s, ok = vs.ShownAt(3.01)
	if !ok || s.PTS > 3.01 || s.PTS < 2.96 {
		t.Fatalf("shown at 3.01: %+v %v", s, ok)
	}
	k, ok := vs.KeyAtOrBefore(s.DTS)
	if !ok || !near(k.PTS, 2, 0.1) {
		t.Fatalf("key before: %+v", k)
	}
}

func TestMSEAudioPrimingAndBufferedRange(t *testing.T) {
	b := NewMSEBuffer(`audio/mp4; codecs="mp4a.40.2"`)
	b.Append(fx(t, "h264", "init-2.m4s"), 0)
	for n := 1; n <= 4; n++ {
		if _, err := b.Append(fx(t, "h264", seg(2, n, "m4s")), 0); err != nil {
			t.Fatal(err)
		}
	}
	// the priming frame (pts < 0) is kept for the decoder but the buffered range starts at 0
	r := b.Buffered()
	if len(r) != 1 || r[0][0] != 0 || !near(r[0][1], 6.0, 0.03) {
		t.Fatalf("audio buffered %v", r)
	}
	if as := b.FirstStore("audio"); as.Config().CodecName != "aac" || as.Config().CfgKind != "asc" || len(as.Config().Cfg) < 2 {
		t.Fatalf("audio config %+v", as.Config())
	}
}

func TestMSEMediaSegmentBeforeInit(t *testing.T) {
	b := NewMSEBuffer(`video/mp4; codecs="avc1.4d400c"`)
	_, err := b.Append(fx(t, "h264", seg(0, 1, "m4s")), 0)
	if !errors.Is(err, ErrBadStream) {
		t.Fatalf("got %v, want ErrBadStream", err)
	}
}

func TestMSEGarbage(t *testing.T) {
	b := NewMSEBuffer(`video/mp4; codecs="avc1.4d400c"`)
	if _, err := b.Append([]byte("this is not an mp4 file at all, not even close...."), 0); err == nil {
		t.Fatal("garbage accepted")
	}
}

func TestMSEWebMClusters(t *testing.T) {
	b := NewMSEBuffer(`video/webm; codecs="vp9"`)
	r, err := b.Append(fx(t, "vp9", "init-0.webm"), 0)
	if err != nil || r.Init == nil || r.Init.Tracks[0].CodecName != "vp9" || r.Init.Tracks[0].Width != 320 {
		t.Fatalf("init: %v %+v", err, r)
	}
	for n := 1; n <= 3; n++ {
		s := fx(t, "vp9", seg(0, n, "webm"))
		for i := 0; i < len(s); i += 999 { // clusters split anywhere
			if _, err := b.Append(s[i:min(i+999, len(s))], 0); err != nil {
				t.Fatal(err)
			}
		}
	}
	wantRanges(t, b.Buffered(), [2]float64{0, 6})
	if n := b.FirstStore("video").Len(); n != 150 {
		t.Fatalf("%d frames", n)
	}
	// audio, Opus
	a := NewMSEBuffer(`audio/webm; codecs="opus"`)
	a.Append(fx(t, "vp9", "init-1.webm"), 0)
	for n := 1; n <= 4; n++ {
		if _, err := a.Append(fx(t, "vp9", seg(1, n, "webm")), 0); err != nil {
			t.Fatal(err)
		}
	}
	rg := a.Buffered()
	if len(rg) != 1 || rg[0][1] < 5.9 {
		t.Fatalf("opus buffered %v", rg)
	}
	if c := a.FirstStore("audio").Config(); c.CodecName != "opus" || c.CfgKind != "opushead" || len(c.Cfg) < 19 {
		t.Fatalf("opus config %+v", c)
	}
}

func TestMSETimestampOffsetAndSequence(t *testing.T) {
	b := NewMSEBuffer(`video/mp4; codecs="avc1.4d400c"`)
	b.Append(fx(t, "h264", "init-0.m4s"), 0)
	b.SetTimestampOffset(10)
	b.Append(fx(t, "h264", seg(0, 1, "m4s")), 0)
	wantRanges(t, b.Buffered(), [2]float64{10, 12})

	// sequence mode places successive segments end to end whatever their timestamps
	s := NewMSEBuffer(`video/mp4; codecs="avc1.4d400c"`)
	s.Append(fx(t, "h264", "init-0.m4s"), 0)
	s.SetMode(true)
	s.Append(fx(t, "h264", seg(0, 3, "m4s")), 0) // timestamps 4..6
	s.Append(fx(t, "h264", seg(0, 1, "m4s")), 0) // timestamps 0..2
	wantRanges(t, s.Buffered(), [2]float64{0, 4})
}

func TestMSEAppendWindow(t *testing.T) {
	b := NewMSEBuffer(`video/mp4; codecs="avc1.4d400c"`)
	b.Append(fx(t, "h264", "init-0.m4s"), 0)
	b.SetAppendWindow(2, 4)
	for n := 1; n <= 3; n++ {
		b.Append(fx(t, "h264", seg(0, n, "m4s")), 0)
	}
	wantRanges(t, b.Buffered(), [2]float64{2, 4})
}

func TestMSERemoveAndOverlap(t *testing.T) {
	b := NewMSEBuffer(`video/mp4; codecs="avc1.4d400c"`)
	b.Append(fx(t, "h264", "init-0.m4s"), 0)
	for n := 1; n <= 3; n++ {
		b.Append(fx(t, "h264", seg(0, n, "m4s")), 0)
	}
	b.Remove(2, 4)
	wantRanges(t, b.Buffered(), [2]float64{0, 2}, [2]float64{4, 6})
	// the removed middle can be filled again with the other quality (new init, same track id)
	b.Append(fx(t, "h264", "init-1.m4s"), 0)
	if _, err := b.Append(fx(t, "h264", seg(1, 2, "m4s")), 0); err != nil {
		t.Fatal(err)
	}
	wantRanges(t, b.Buffered(), [2]float64{0, 6})
	st := b.FirstStore("video")
	hi, _ := st.ShownAt(1)
	lo, _ := st.ShownAt(3)
	if hi.Cfg == nil || lo.Cfg == nil || hi.Cfg.Width != 320 || lo.Cfg.Width != 160 {
		t.Fatalf("samples must remember their init segment: %+v / %+v", hi.Cfg, lo.Cfg)
	}
	// removal from the middle of a GOP also drops the dependents
	b.Remove(0.5, 0.6)
	r := b.Buffered()
	if len(r) < 2 || r[0][1] > 0.6 {
		t.Fatalf("after cutting a GOP: %v", r)
	}
}

func TestMSEQuotaEviction(t *testing.T) {
	b := NewMSEBuffer(`video/mp4; codecs="avc1.4d400c"`)
	b.Append(fx(t, "h264", "init-0.m4s"), 0)
	b.SetQuota(len(fx(t, "h264", seg(0, 1, "m4s"))) + len(fx(t, "h264", seg(0, 2, "m4s"))) + 100)
	for n := 1; n <= 2; n++ {
		if _, err := b.Append(fx(t, "h264", seg(0, n, "m4s")), 0); err != nil {
			t.Fatal(err)
		}
	}
	// full, playhead at 0: nothing before it, so the next append is refused
	if _, err := b.Append(fx(t, "h264", seg(0, 3, "m4s")), 0); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("got %v, want ErrQuotaExceeded", err)
	}
	// playhead at 3.5: the first GOP is behind it and is evicted to make room
	r, err := b.Append(fx(t, "h264", seg(0, 3, "m4s")), 3.5)
	if err != nil {
		t.Fatal(err)
	}
	if r.Evicted == 0 {
		t.Fatal("expected eviction")
	}
	got := b.Buffered()
	if len(got) != 1 || got[0][0] < 1.9 || !near(got[0][1], 6, 0.03) {
		t.Fatalf("buffered after eviction: %v", got)
	}
}

func TestMSEAV1(t *testing.T) {
	b := NewMSEBuffer(`video/mp4; codecs="av01.0.00M.08"`)
	r, err := b.Append(fx(t, "av1", "init-0.m4s"), 0)
	if err != nil || r.Init.Tracks[0].CodecName != "av1" {
		t.Fatalf("init %v %+v", err, r)
	}
	for n := 1; n <= 3; n++ {
		if _, err := b.Append(fx(t, "av1", seg(0, n, "m4s")), 0); err != nil {
			t.Fatal(err)
		}
	}
	wantRanges(t, b.Buffered(), [2]float64{0, 6})
}

func TestTrackStoreGapAndShownAt(t *testing.T) {
	st := NewTrackStore(TrackConfig{Kind: "video"})
	mk := func(t0 float64, n int) []Sample {
		var o []Sample
		for i := 0; i < n; i++ {
			o = append(o, NewSample(t0+float64(i)*0.04, t0+float64(i)*0.04, 0.04, i == 0, []byte{1}))
		}
		return o
	}
	st.Insert(mk(0, 25))
	st.Insert(mk(5, 25))
	wantRanges(t, st.Ranges(), [2]float64{0, 1}, [2]float64{5, 6})
	if _, ok := st.ShownAt(3); ok {
		t.Fatal("a time in the gap must not map to a sample")
	}
	if s, ok := st.ShownAt(5.5); !ok || !near(s.PTS, 5.48, 1e-6) {
		t.Fatalf("5.5 -> %+v %v", s, ok)
	}
	if _, _, gap := st.Next(0.92); gap {
		t.Fatal("no gap inside a range")
	}
	if s, ok, gap := st.Next(0.96 + 0.04); !ok || !gap || s.DTS != 5 {
		t.Fatalf("crossing the gap: %+v %v %v", s, ok, gap)
	}
}
