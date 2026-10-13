package capture

import (
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeVideo is a scriptable VideoSource.
type fakeVideo struct {
	info    DeviceInfo
	starts  *atomic.Int32
	stops   *atomic.Int32
	failErr error // Start fails with this
	mu      sync.Mutex
	ch      chan *Frame
	stop    chan struct{}
	err     error
	w, h    int
}

func (f *fakeVideo) Info() DeviceInfo { return f.info }
func (f *fakeVideo) Start(c VideoConstraints) (<-chan *Frame, error) {
	if f.failErr != nil {
		return nil, f.failErr
	}
	f.starts.Add(1)
	f.ch = make(chan *Frame, 2)
	f.stop = make(chan struct{})
	w, h := c.Width, c.Height
	if w == 0 {
		w, h = 64, 48
	}
	f.w, f.h = w, h
	go func() {
		defer close(f.ch)
		for i := 0; ; i++ {
			fr := NewBlack(w, h)
			fr.PTS = time.Duration(i) * time.Millisecond
			fr.Y[0] = byte(i)
			select {
			case f.ch <- fr:
			case <-f.stop:
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	return f.ch, nil
}
func (f *fakeVideo) Stop() error {
	f.stops.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stop != nil {
		select {
		case <-f.stop:
		default:
			close(f.stop)
		}
	}
	return nil
}
func (f *fakeVideo) Err() error { f.mu.Lock(); defer f.mu.Unlock(); return f.err }
func (f *fakeVideo) Mode() (int, int, float64) {
	return f.w, f.h, 30
}

// kill simulates unplugging: the channel closes and Err reports why.
func (f *fakeVideo) kill(err error) {
	f.mu.Lock()
	f.err = err
	f.mu.Unlock()
	close(f.stop)
}

func newFake() (*fakeVideo, *atomic.Int32, *atomic.Int32) {
	var s, p atomic.Int32
	return &fakeVideo{info: DeviceInfo{ID: "fake", Kind: VideoInput, Label: "Fake"}, starts: &s, stops: &p}, &s, &p
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 400; i++ {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func TestLeaseSharesOneDevice(t *testing.T) {
	src, starts, stops := newFake()
	open := func() (VideoSource, error) { return src, nil }
	a, err := AcquireVideoFrom("k1", open, VideoConstraints{Width: 320, Height: 240}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	b, err := AcquireVideoFrom("k1", open, VideoConstraints{Width: 640, Height: 480}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if starts.Load() != 1 {
		t.Fatalf("device started %d times", starts.Load())
	}
	if m := b.Mode(); m.W != 320 || m.H != 240 {
		t.Fatalf("joined lease reports %+v, want the running 320x240", m)
	}
	sa, sb := a.Subscribe(), b.Subscribe()
	for _, s := range []*Sub[Frame]{sa, sb} {
		select {
		case f := <-s.C:
			if f.W != 320 {
				t.Fatalf("frame %dx%d", f.W, f.H)
			}
		case <-time.After(time.Second):
			t.Fatal("no frame")
		}
	}
	a.Release()
	if stops.Load() != 0 {
		t.Fatal("device stopped while a lease is still held")
	}
	b.Release()
	waitFor(t, "device stop", func() bool { return stops.Load() == 1 })
	b.Release() // idempotent
	if stops.Load() != 1 {
		t.Fatal("double release stopped twice")
	}
	liveMu.Lock()
	n := len(videoShares)
	liveMu.Unlock()
	if n != 0 {
		t.Fatalf("%d shares leaked", n)
	}
}

func TestLeaseStartFailure(t *testing.T) {
	src, _, _ := newFake()
	src.failErr = ErrInUse
	_, err := AcquireVideoFrom("k2", func() (VideoSource, error) { return src, nil }, VideoConstraints{}, time.Second)
	if !errors.Is(err, ErrInUse) {
		t.Fatalf("got %v, want ErrInUse", err)
	}
	if _, err = AcquireVideoFrom("k3", func() (VideoSource, error) { return nil, ErrNotFound }, VideoConstraints{}, time.Second); !errors.Is(err, ErrNotFound) {
		t.Fatalf("open error: %v", err)
	}
	liveMu.Lock()
	n := len(videoShares)
	liveMu.Unlock()
	if n != 0 {
		t.Fatalf("failed starts left %d shares", n)
	}
}

func TestLeaseTimeoutWithoutFrames(t *testing.T) {
	var s, p atomic.Int32
	mute := &silentVideo{starts: &s, stops: &p}
	_, err := AcquireVideoFrom("k4", func() (VideoSource, error) { return mute, nil }, VideoConstraints{}, 50*time.Millisecond)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("got %v, want ErrTimeout", err)
	}
	waitFor(t, "stop after timeout", func() bool { return p.Load() == 1 })
}

type silentVideo struct {
	starts, stops *atomic.Int32
	ch            chan *Frame
}

func (s *silentVideo) Info() DeviceInfo { return DeviceInfo{} }
func (s *silentVideo) Start(VideoConstraints) (<-chan *Frame, error) {
	s.starts.Add(1)
	s.ch = make(chan *Frame)
	return s.ch, nil
}
func (s *silentVideo) Stop() error { s.stops.Add(1); close(s.ch); return nil }
func (s *silentVideo) Err() error  { return nil }

func TestTrackLifecycleAndUnplug(t *testing.T) {
	src, _, stops := newFake()
	l, err := AcquireVideoFrom("k5", func() (VideoSource, error) { return src, nil }, VideoConstraints{Width: 64, Height: 48}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var ended atomic.Int32
	tr := NewVideoTrack(l, src.info, "https://x.example", func(*Track) { ended.Add(1) })
	if LookupTrack(tr.ID) != tr {
		t.Fatal("track not registered")
	}
	clone := tr.Clone()
	frames, cancel := tr.SubscribeVideo()
	defer cancel()
	if f := <-frames; f.W != 64 {
		t.Fatal("frame size")
	}
	// disabled -> black, same size, still flowing
	tr.SetEnabled(false)
	var f *Frame
	waitFor(t, "black frame", func() bool {
		select {
		case f = <-frames:
			return f.Y[0] == 16 && f.W == 64
		default:
			return false
		}
	})
	tr.SetEnabled(true)

	// unplug: every track on the device ends with EndDevice and an error
	src.kill(errors.New("no such device"))
	for _, x := range []*Track{tr, clone} {
		select {
		case <-x.Ended():
		case <-time.After(time.Second):
			t.Fatal("track did not end on unplug")
		}
		if r, err := x.EndInfo(); r != EndDevice || err == nil {
			t.Fatalf("end info %q %v", r, err)
		}
	}
	waitFor(t, "frames channel closed", func() bool { _, ok := <-frames; return !ok })
	if ended.Load() != 2 {
		t.Fatalf("onEnd ran %d times", ended.Load())
	}
	if LookupTrack(tr.ID) != nil || LiveTracks() != 0 {
		t.Fatal("ended tracks still registered")
	}
	_ = stops
}

func TestTrackStopReleasesDevice(t *testing.T) {
	src, _, stops := newFake()
	l, err := AcquireVideoFrom("k6", func() (VideoSource, error) { return src, nil }, VideoConstraints{}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	tr := NewVideoTrack(l, src.info, "", nil)
	c := tr.Clone()
	tr.End(EndStopped, nil)
	if stops.Load() != 0 {
		t.Fatal("device stopped while a clone is live")
	}
	c.End(EndUser, nil)
	waitFor(t, "device stopped after both tracks ended", func() bool { return stops.Load() == 1 })
	if r, _ := c.EndInfo(); r != EndUser {
		t.Fatalf("reason %q", r)
	}
	tr.End(EndDevice, nil) // later calls cannot change the reason
	if r, _ := tr.EndInfo(); r != EndStopped {
		t.Fatalf("reason changed to %q", r)
	}
}

func TestReopenKeepsSubscribers(t *testing.T) {
	var starts, stops atomic.Int32
	mk := func() (VideoSource, error) {
		return &fakeVideo{info: DeviceInfo{ID: "r"}, starts: &starts, stops: &stops}, nil
	}
	l, err := AcquireVideoFrom("k7", mk, VideoConstraints{Width: 64, Height: 48}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	sub := l.Subscribe()
	defer sub.Cancel()
	<-sub.C
	if err := l.Reopen(VideoConstraints{Width: 128, Height: 96}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "frames at the new size", func() bool {
		select {
		case f, ok := <-sub.C:
			return ok && f.W == 128
		default:
			return false
		}
	})
	if starts.Load() != 2 {
		t.Fatalf("starts=%d", starts.Load())
	}
	select {
	case <-l.Ended():
		t.Fatal("share ended by a deliberate reopen")
	default:
	}
}

func TestSlowSubscriberNeverBlocksSource(t *testing.T) {
	src, _, _ := newFake()
	l, err := AcquireVideoFrom("k8", func() (VideoSource, error) { return src, nil }, VideoConstraints{}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	slow := l.Subscribe() // never read
	fast := l.Subscribe()
	got := 0
	deadline := time.After(2 * time.Second)
	for got < 50 {
		select {
		case <-fast.C:
			got++
		case <-deadline:
			t.Fatalf("fast subscriber starved behind a slow one after %d frames", got)
		}
	}
	slow.Cancel()
	fast.Cancel()
	fast.Cancel() // idempotent
}

func TestOpenCloseNoLeaks(t *testing.T) {
	runtime.GC()
	before := runtime.NumGoroutine()
	for i := 0; i < 100; i++ {
		src, _, _ := newFake()
		l, err := AcquireVideoFrom("leak", func() (VideoSource, error) { return src, nil }, VideoConstraints{}, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		tr := NewVideoTrack(l, src.info, "", nil)
		ch, cancel := tr.SubscribeVideo()
		<-ch
		cancel()
		tr.End(EndStopped, nil)
	}
	waitFor(t, "goroutines to wind down", func() bool { return runtime.NumGoroutine() <= before+2 })
	if LiveTracks() != 0 {
		t.Fatalf("%d tracks leaked", LiveTracks())
	}
	waitFor(t, "shares to be freed", func() bool {
		liveMu.Lock()
		defer liveMu.Unlock()
		return len(videoShares) == 0
	})
}

type regProvider struct {
	name string
	devs []DeviceInfo
	vcap VideoCaps
}

func (r *regProvider) Name() string                          { return r.name }
func (r *regProvider) Devices() []DeviceInfo                 { return r.devs }
func (r *regProvider) OpenAudio(string) (AudioSource, error) { return nil, ErrUnsupported }
func (r *regProvider) OpenVideo(id string) (VideoSource, error) {
	s, _, _ := newFake()
	s.info.ID = id
	return s, nil
}
func (r *regProvider) VideoCaps(id string) (VideoCaps, bool) { return r.vcap, true }
func (r *regProvider) AudioCaps(string) (AudioCaps, bool)    { return AudioCaps{}, false }

func TestRegistryAndCaps(t *testing.T) {
	p := &regProvider{name: "zz-test", devs: []DeviceInfo{{ID: "cam1", Kind: VideoInput, Label: "C1"}, {ID: "mic1", Kind: AudioInput}},
		vcap: VideoCaps{Modes: []Mode{{W: 320, H: 240, FPSMin: 5, FPSMax: 30}}}}
	Register(p)
	defer Unregister("zz-test")
	if d := Devices(VideoInput); len(d) != 1 || d[0].ID != "cam1" {
		t.Fatalf("devices %+v", d)
	}
	if c := VideoCapsOf("cam1"); len(c.Modes) != 1 || c.Modes[0].W != 320 {
		t.Fatalf("caps %+v", c)
	}
	if c := VideoCapsOf("unknown"); len(c.Modes) < 3 {
		t.Fatal("default caps expected")
	}
	if _, err := OpenVideo("cam1"); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenVideo("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatal("unknown device should be ErrNotFound")
	}
	if c := AudioCapsOf("mic1"); c.DefaultRate != 48000 {
		t.Fatalf("audio default caps %+v", c)
	}
	minW, maxW, _, maxH, minF, maxF := p.vcap.Bounds()
	if minW != 320 || maxW != 320 || maxH != 240 || minF != 5 || maxF != 30 {
		t.Fatal("bounds")
	}
}

func TestClosestMode(t *testing.T) {
	modes := []Mode{{640, 480, 1, 15}, {640, 480, 1, 30}, {1280, 720, 1, 30}, {1920, 1080, 1, 5}}
	m, _ := ClosestMode(modes, 1280, 720, 30)
	if m.W != 1280 {
		t.Errorf("got %+v", m)
	}
	m, _ = ClosestMode(modes, 640, 480, 30)
	if m.FPSMax != 30 {
		t.Errorf("same size should prefer the mode that reaches 30 fps: %+v", m)
	}
	m, _ = ClosestMode(modes, 1920, 1080, 30)
	if m.W != 1920 {
		t.Errorf("exact size wins over frame rate: %+v", m)
	}
	if _, ok := ClosestMode(nil, 1, 1, 1); ok {
		t.Error("empty mode list")
	}
}
