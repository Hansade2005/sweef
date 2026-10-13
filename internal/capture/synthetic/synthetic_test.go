package synthetic

import (
	"bytes"
	"math"
	"runtime"
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/capture"
)

func TestRenderDeterministicAndMoving(t *testing.T) {
	a, b, c := capture.NewFrame(320, 240), capture.NewFrame(320, 240), capture.NewFrame(320, 240)
	Render(a, 5, 30)
	Render(b, 5, 30)
	Render(c, 6, 30)
	if !bytes.Equal(a.Y, b.Y) || !bytes.Equal(a.U, b.U) || !bytes.Equal(a.V, b.V) {
		t.Fatal("same frame number rendered differently")
	}
	if bytes.Equal(a.Y, c.Y) {
		t.Fatal("consecutive frames identical: the bars do not move")
	}
	// the bars move left by four pixels a frame: below the text plate, row 200 of frame 6 is frame 5 shifted by 4
	for x := 0; x < 316; x++ {
		if c.Y[200*320+x] != a.Y[200*320+x+4] {
			t.Fatalf("bar edge not shifted at x=%d", x)
		}
	}
	d := capture.NewFrame(320, 240)
	Render(d, 123456, 30)
	if bytes.Equal(a.Y[:20*320], d.Y[:20*320]) {
		t.Fatal("frame counter text did not change")
	}
}

func TestRenderSizes(t *testing.T) {
	for _, s := range [][2]int{{16, 16}, {2, 2}, {1920, 1080}, {33, 17}} {
		f := capture.NewFrame(s[0], s[1])
		Render(f, 99, 60) // must not panic on tiny or odd sizes
	}
}

func TestCameraStreamUnthrottled(t *testing.T) {
	cam := NewCamera(capture.DeviceInfo{ID: "x"}, 160, 120, 30)
	cam.Unthrottled = true
	ch, err := cam.Start(capture.VideoConstraints{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cam.Start(capture.VideoConstraints{}); err == nil {
		t.Fatal("second Start accepted")
	}
	var prev time.Duration = -1
	for i := 0; i < 10; i++ {
		f := <-ch
		if f.W != 160 || f.H != 120 || f.PTS <= prev {
			t.Fatalf("frame %d: %dx%d pts=%v prev=%v", i, f.W, f.H, f.PTS, prev)
		}
		ref := capture.NewFrame(160, 120)
		Render(ref, i, 30)
		if !bytes.Equal(ref.Y, f.Y) {
			t.Fatalf("frame %d differs from Render", i)
		}
		prev = f.PTS
	}
	if err := cam.Stop(); err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
	cam.Stop() // idempotent
}

func TestCameraHonoursConstraints(t *testing.T) {
	cam := NewCamera(capture.DeviceInfo{ID: "x"}, 0, 0, 0)
	cam.Unthrottled = true
	ch, _ := cam.Start(capture.VideoConstraints{Width: 1280, Height: 720, FrameRate: 15})
	f := <-ch
	if f.W != 1280 || f.H != 720 {
		t.Fatalf("%dx%d", f.W, f.H)
	}
	if w, h, fps := cam.Mode(); w != 1280 || h != 720 || fps != 15 {
		t.Fatalf("mode %d %d %v", w, h, fps)
	}
	cam.Stop()
}

func TestCameraRealtimePacing(t *testing.T) {
	cam := NewCamera(capture.DeviceInfo{ID: "x"}, 64, 48, 50)
	ch, _ := cam.Start(capture.VideoConstraints{})
	start := time.Now()
	for i := 0; i < 6; i++ {
		<-ch
	}
	cam.Stop()
	// six frames at 50 fps take at least ~100 ms; this only guards against free-running, not against jitter
	if d := time.Since(start); d < 80*time.Millisecond {
		t.Fatalf("6 frames in %v: not paced", d)
	}
}

func TestMicSignals(t *testing.T) {
	m := NewMic(capture.DeviceInfo{ID: "m"})
	a, b := make([]int16, 480), make([]int16, 480)
	m.Fill(a, 3)
	m.Fill(b, 3)
	for i := range a {
		if a[i] != b[i] {
			t.Fatal("not deterministic")
		}
	}
	// the sine has the configured frequency: count rising zero crossings over 10 chunks (100 ms of 440 Hz = 44)
	cross, prev := 0, int16(0)
	buf := make([]int16, 480)
	for n := 0; n < 10; n++ {
		m.Fill(buf, n)
		for _, s := range buf {
			if prev < 0 && s >= 0 {
				cross++
			}
			prev = s
		}
	}
	if cross < 43 || cross > 45 {
		t.Fatalf("%d rising crossings in 100 ms, want about 44", cross)
	}
	peak := 0.0
	for _, s := range a {
		peak = math.Max(peak, math.Abs(float64(s)))
	}
	if peak < 0.2*32767 || peak > 0.26*32767 {
		t.Fatalf("peak %v", peak)
	}
	m.Wave = Noise
	m.Fill(a, 0)
	m.Fill(b, 1)
	same := 0
	for i := range a {
		if a[i] == b[i] {
			same++
		}
	}
	if same > 20 {
		t.Fatalf("noise chunks look alike (%d equal samples)", same)
	}
	m.Wave = Silence
	m.Fill(a, 0)
	for _, s := range a {
		if s != 0 {
			t.Fatal("silence is not silent")
		}
	}
}

func TestMicStream(t *testing.T) {
	m := NewMic(capture.DeviceInfo{ID: "m"})
	m.Unthrottled = true
	ch, err := m.Start(capture.AudioConstraints{})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		c := <-ch
		if c.Rate != 48000 || c.Channels != 1 || len(c.PCM) != 480 || c.PTS != time.Duration(i)*10*time.Millisecond {
			t.Fatalf("chunk %d: %+v", i, c)
		}
	}
	m.Stop()
	for range ch {
	}
}

func TestProviderGatedByEnv(t *testing.T) {
	t.Setenv("SWEEF_FAKE_DEVICES", "")
	t.Setenv("SWEEF_FAKE_MEDIA", "")
	if len(capture.Devices(capture.VideoInput)) != 0 || len(capture.Devices(capture.AudioInput)) != 0 {
		t.Fatal("fake devices present without SWEEF_FAKE_DEVICES")
	}
	if _, err := capture.OpenVideo(CameraID); err == nil {
		t.Fatal("opened a fake camera without the env var")
	}
	t.Setenv("SWEEF_FAKE_DEVICES", "1")
	if len(capture.Devices(capture.VideoInput)) != 1 || len(capture.Devices(capture.AudioInput)) != 1 || len(capture.Devices(capture.AudioOutput)) != 1 {
		t.Fatalf("fake devices missing: %+v", capture.Devices(capture.VideoInput))
	}
	t.Setenv("SWEEF_FAKE_CAMERA", "320x180@10")
	l, err := capture.AcquireVideo(CameraID, capture.VideoConstraints{}, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if m := l.Mode(); m.W != 320 || m.H != 180 || m.FPS != 10 {
		t.Fatalf("env camera mode %+v", m)
	}
	l.Release()
	t.Setenv("SWEEF_FAKE_AUDIO", "noise")
	al, err := capture.AcquireAudio(MicID, capture.AudioConstraints{}, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if m := al.Mode(); m.Rate != 48000 || m.Channels != 1 {
		t.Fatalf("mic mode %+v", m)
	}
	al.Release()
}

func TestNoGoroutineLeak(t *testing.T) {
	t.Setenv("SWEEF_FAKE_DEVICES", "1")
	runtime.GC()
	before := runtime.NumGoroutine()
	for i := 0; i < 100; i++ {
		l, err := capture.AcquireVideo(CameraID, capture.VideoConstraints{FrameRate: 60}, 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		a, err := capture.AcquireAudio(MicID, capture.AudioConstraints{}, 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		vt := capture.NewVideoTrack(l, capture.DeviceInfo{ID: CameraID, Kind: capture.VideoInput}, "", nil)
		at := capture.NewAudioTrack(a, capture.DeviceInfo{ID: MicID, Kind: capture.AudioInput}, "", nil)
		vt.End(capture.EndStopped, nil)
		at.End(capture.EndStopped, nil)
	}
	for i := 0; i < 200 && runtime.NumGoroutine() > before+2; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > before+2 {
		t.Fatalf("goroutines %d -> %d after 100 open/close cycles", before, n)
	}
	if capture.LiveTracks() != 0 {
		t.Fatal("tracks leaked")
	}
}
