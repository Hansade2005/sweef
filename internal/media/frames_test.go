package media

import (
	"image"
	"math"
	"path/filepath"
	"testing"
)

func imgDiff(a, b image.Image) float64 {
	ab, bb := a.Bounds(), b.Bounds()
	if ab.Dx() != bb.Dx() || ab.Dy() != bb.Dy() {
		return math.Inf(1)
	}
	var sum float64
	n := 0
	for y := 0; y < ab.Dy(); y++ {
		for x := 0; x < ab.Dx(); x++ {
			r1, g1, b1, _ := a.At(ab.Min.X+x, ab.Min.Y+y).RGBA()
			r2, g2, b2, _ := b.At(bb.Min.X+x, bb.Min.Y+y).RGBA()
			sum += math.Abs(float64(r1>>8)-float64(r2>>8)) + math.Abs(float64(g1>>8)-float64(g2>>8)) + math.Abs(float64(b1>>8)-float64(b2>>8))
			n += 3
		}
	}
	return sum / float64(n)
}

func openFrames(t *testing.T, name string, withFF bool) (FrameSource, *Info) {
	data := load(t, name)
	info, err := ProbeBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	if !withFF {
		SetFFmpegEnabled(false)
		t.Cleanup(func() { SetFFmpegEnabled(true) })
	}
	fs, err := OpenFrames(BytesSource(data), info, filepath.Join(testMediaDir, name))
	if err != nil {
		t.Fatalf("OpenFrames %s: %v", name, err)
	}
	return fs, info
}

func TestVP8KeyframeVsFFmpeg(t *testing.T) {
	if FFmpegPath() == "" {
		t.Skip("no ffmpeg")
	}
	nat, _ := openFrames(t, "clip.webm", false)
	if nat.Mode() != "native" {
		t.Fatalf("mode %s", nat.Mode())
	}
	a, err := nat.Frame(0)
	if err != nil {
		t.Fatal(err)
	}
	SetFFmpegEnabled(true)
	ff, _ := openFrames(t, "clip.webm", true)
	// force the ffmpeg path: ask for a later frame so native is bypassed
	b, err := ff.Frame(0.2)
	if err != nil {
		t.Fatal(err)
	}
	c, err := nat.Frame(0.2)
	if err != nil {
		t.Fatal(err)
	}
	if d := imgDiff(c, b); d > 3 {
		t.Errorf("VP8 inter frame native vs ffmpeg @0.2 mean abs diff %.2f", d)
	}
	ff0, err := ff.Frame(0)
	if err != nil {
		t.Fatal(err)
	}
	if d := imgDiff(a, ff0); d > 3 {
		t.Errorf("VP8 native vs ffmpeg frame 0 mean abs diff %.2f", d)
	}
}

func TestMJPEGAndGIF(t *testing.T) {
	fs, info := openFrames(t, "mjpeg.avi", false)
	if fs.Mode() != "mjpeg" {
		t.Fatal(fs.Mode())
	}
	img, err := fs.Frame(0.5)
	if err != nil || img.Bounds().Dx() != 64 {
		t.Fatalf("mjpeg frame: %v %v", img, err)
	}
	_ = info
	g, _ := openFrames(t, "anim.gif", false)
	i0, _ := g.Frame(0.05)
	i1, _ := g.Frame(0.45)
	if imgDiff(i0, i1) < 1 {
		t.Error("gif frames should differ over time")
	}
	SetFFmpegEnabled(true)
	if FFmpegPath() != "" {
		ff, _ := openFrames(t, "mjpeg.avi", true)
		a, err := ff.Frame(0.4)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := fs.Frame(0.4)
		if d := imgDiff(a, b); d > 4 {
			t.Errorf("mjpeg native vs ffmpeg diff %.2f", d)
		} else {
			t.Logf("mjpeg native vs ffmpeg mean abs diff %.2f", d)
		}
	}
}
