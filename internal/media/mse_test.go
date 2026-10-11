package media

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestMSEWebM(t *testing.T) {
	b := load(t, "clip.webm")
	m := NewMSEBuffer(`video/webm; codecs="vp8, vorbis"`)
	// feed in odd chunk sizes
	var spans []MSESpan
	var init *MSEInit
	for i := 0; i < len(b); i += 4999 {
		e := i + 4999
		if e > len(b) {
			e = len(b)
		}
		r, err := m.Append(b[i:e])
		if err != nil {
			t.Fatal(err)
		}
		if r.Init != nil {
			init = r.Init
		}
		spans = append(spans, r.Spans...)
	}
	if init == nil || len(init.Tracks) != 2 {
		t.Fatalf("init %+v", init)
	}
	var maxEnd float64
	for _, s := range spans {
		if s.End > maxEnd {
			maxEnd = s.End
		}
	}
	if maxEnd < 1.9 || maxEnd > 2.15 {
		t.Fatalf("webm coverage end %.3f (spans %v)", maxEnd, spans)
	}
}

func TestMSEFragmentedMP4(t *testing.T) {
	if FFmpegPath() == "" {
		t.Skip("no ffmpeg")
	}
	out := filepath.Join(t.TempDir(), "frag.mp4")
	err := exec.Command("ffmpeg", "-v", "error", "-y", "-i", filepath.Join(testMediaDir, "clip.mp4"), "-c", "copy", "-movflags", "frag_keyframe+empty_moov+default_base_moof", out).Run()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out)
	m := NewMSEBuffer(`video/mp4; codecs="avc1.64000a, mp4a.40.2"`)
	var spans []MSESpan
	var init *MSEInit
	for i := 0; i < len(b); i += 3001 {
		e := i + 3001
		if e > len(b) {
			e = len(b)
		}
		r, err := m.Append(b[i:e])
		if err != nil {
			t.Fatal(err)
		}
		if r.Init != nil {
			init = r.Init
		}
		spans = append(spans, r.Spans...)
	}
	if init == nil || len(init.Tracks) != 2 {
		t.Fatalf("init %+v", init)
	}
	var maxEnd float64
	for _, s := range spans {
		if s.End > maxEnd {
			maxEnd = s.End
		}
	}
	if maxEnd < 1.9 {
		t.Fatalf("mp4 coverage end %.3f", maxEnd)
	}
	info, err := ProbeBytes(b)
	if err != nil || !info.Fragmented || info.Duration < 1.9 {
		t.Fatalf("fragmented probe: %v %+v", err, info)
	}
}
