package mediarec

import (
	"runtime"
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/capture"
	"github.com/sweefbrowser/sweef/internal/media"
)

func (r *Recorder) pending() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.il)
}

func TestIdleVideoDoesNotHoldBackAudio(t *testing.T) {
	ain, _ := toneSource(48000, 1, 440)
	silent := make(chan *capture.Frame)
	vin := &VideoIn{C: silent, Cancel: func() {}, W: 160, H: 120, FPS: 30}
	col := &collector{}
	r, err := New(Config{Container: "webm", Timeslice: 250 * time.Millisecond}, []*AudioIn{ain}, vin, col.add)
	if err != nil {
		t.Fatal(err)
	}
	r.Start()
	time.Sleep(1700 * time.Millisecond)
	col.mu.Lock()
	n := len(col.chunks)
	col.mu.Unlock()
	r.Stop()
	<-r.Done()
	if n < 3 {
		t.Errorf("a video track that sends nothing held the audio back: %d chunks in 1.7 s at a 250 ms timeslice", n)
	}
	if p := decode(t, col.all()); len(p.Data[0]) < 48000 {
		t.Errorf("only %d samples decoded", len(p.Data[0]))
	}
}

func TestQueuesStayBoundedWhenSourcesRunFast(t *testing.T) {
	c := make(chan *capture.Frame, 3)
	stop := make(chan struct{})
	go func() { // a camera at 300 fps
		defer close(c)
		for n := 0; ; n++ {
			select {
			case <-stop:
				return
			default:
			}
			select {
			case c <- moving(160, 120, n):
			default:
			}
			time.Sleep(3 * time.Millisecond)
		}
	}()
	ain, _ := toneSource(48000, 1, 440)
	col := &collector{}
	r, err := New(Config{Container: "webm", Timeslice: 200 * time.Millisecond}, []*AudioIn{ain}, &VideoIn{C: c, Cancel: func() {}, W: 160, H: 120, FPS: 300}, col.add)
	if err != nil {
		t.Fatal(err)
	}
	r.Start()
	peak := 0
	for i := 0; i < 150; i++ {
		time.Sleep(10 * time.Millisecond)
		peak = max(peak, r.pending())
	}
	r.Stop()
	<-r.Done()
	close(stop)
	if peak > 60 {
		t.Errorf("%d packets waiting in the interleaver at the peak", peak)
	}
	vs, err := media.ExtractVideoSamples(col.all())
	if err != nil {
		t.Fatal(err)
	}
	if len(vs.Chunks) > 60 { // 30 fps cap for 1.5 s, with slack
		t.Errorf("%d video frames in 1.5 s: the frame rate cap did not apply", len(vs.Chunks))
	}
}

func TestNoGoroutinesLeftBehind(t *testing.T) {
	before := runtime.NumGoroutine()
	for i := 0; i < 5; i++ {
		ain, cancel := toneSource(48000, 1, 440)
		vin, _ := newFrames(160, 120, 20)
		col := &collector{}
		r, err := New(Config{Container: "webm"}, []*AudioIn{ain}, vin, col.add)
		if err != nil {
			t.Fatal(err)
		}
		r.Start()
		time.Sleep(150 * time.Millisecond)
		r.Stop()
		<-r.Done()
		cancel()
	}
	deadline := time.Now().Add(3 * time.Second)
	for runtime.NumGoroutine() > before+2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > before+2 {
		t.Errorf("%d goroutines before, %d after five recordings", before, n)
	}
}
