package screen

import (
	"errors"
	"image"
	"image/color"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/capture"
)

func solid(w, h int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, 255
	}
	return img
}

func TestScaledFramesAndRepeatOnUnchanged(t *testing.T) {
	var grabs, conv atomic.Int32
	img := solid(640, 360, color.RGBA{200, 30, 30, 255})
	src := New(capture.DeviceInfo{ID: "x", Kind: capture.Screen}, 640, 360, func(prev int) (*image.RGBA, int, error) {
		grabs.Add(1)
		if prev == 7 {
			return nil, 7, nil // unchanged
		}
		conv.Add(1)
		return img, 7, nil
	})
	ch, err := src.Start(capture.VideoConstraints{Width: 320, Height: 180, FrameRate: 50})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := src.Start(capture.VideoConstraints{}); err == nil {
		t.Fatal("second Start accepted")
	}
	var first *capture.Frame
	for i := 0; i < 6; i++ {
		f := <-ch
		if f.W != 320 || f.H != 180 {
			t.Fatalf("frame %dx%d, want the constrained 320x180", f.W, f.H)
		}
		if first == nil {
			first = f
		} else if f.PTS <= first.PTS {
			t.Fatal("timestamps do not advance")
		}
		if r, g, b := capture.YUVToRGB(f.Y[0], f.U[0], f.V[0]); r < 180 || g > 60 || b > 60 {
			t.Fatalf("pixel (%d,%d,%d)", r, g, b)
		}
	}
	if w, h, fps := src.Mode(); w != 320 || h != 180 || fps != 50 {
		t.Fatalf("Mode %d %d %v", w, h, fps)
	}
	src.Stop()
	if conv.Load() != 1 || grabs.Load() < 5 {
		t.Fatalf("converted %d times for %d grabs: unchanged frames must not be converted again", conv.Load(), grabs.Load())
	}
	for range ch {
	}
}

func TestSizeChangesAndErrors(t *testing.T) {
	n := 0
	src := New(capture.DeviceInfo{ID: "x"}, 100, 100, func(prev int) (*image.RGBA, int, error) {
		n++
		switch {
		case n < 3:
			return solid(100, 100, color.RGBA{1, 2, 3, 255}), n, nil
		case n < 6:
			return solid(50, 80, color.RGBA{9, 9, 9, 255}), n, nil // the window was resized: still delivered at the negotiated size
		}
		return nil, 0, errors.New("tab gone")
	})
	ch, _ := src.Start(capture.VideoConstraints{FrameRate: 60})
	for f := range ch {
		if f.W != 100 || f.H != 100 {
			t.Fatalf("frame %dx%d", f.W, f.H)
		}
	}
	if err := src.Err(); !errors.Is(err, capture.ErrNotFound) {
		t.Fatalf("Err = %v", err)
	}
	// a request larger than the surface is ignored; odd sizes are made even
	s2 := New(capture.DeviceInfo{}, 101, 99, func(int) (*image.RGBA, int, error) { return solid(101, 99, color.RGBA{}), 1, nil })
	ch2, _ := s2.Start(capture.VideoConstraints{Width: 4000, Height: 4000})
	f := <-ch2
	if f.W != 100 || f.H != 98 {
		t.Fatalf("%dx%d", f.W, f.H)
	}
	s2.Stop()
}

func TestStopPromptAndNoLeak(t *testing.T) {
	runtime.GC()
	before := runtime.NumGoroutine()
	for i := 0; i < 100; i++ {
		src := New(capture.DeviceInfo{}, 64, 64, func(int) (*image.RGBA, int, error) { return solid(64, 64, color.RGBA{}), 1, nil })
		ch, _ := src.Start(capture.VideoConstraints{FrameRate: 60})
		<-ch
		start := time.Now()
		src.Stop()
		if time.Since(start) > 200*time.Millisecond {
			t.Fatal("Stop is slow")
		}
	}
	for i := 0; i < 100 && runtime.NumGoroutine() > before+1; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > before+1 {
		t.Fatalf("goroutines %d -> %d", before, n)
	}
}
