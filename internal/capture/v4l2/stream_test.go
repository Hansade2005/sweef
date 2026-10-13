//go:build linux && (amd64 || arm64)

package v4l2

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/capture"
)

// smooth returns I420 planes that stay inside the RGB gamut, so a YUV -> RGB -> YUV round trip is within a level or
// two, with chroma that changes per 2x2 block (and rows, so packed 4:2:2 averaging is exercised only with
// duplicated rows below).
func smooth(w, h int) (y, u, v []byte) {
	cw, ch := (w+1)/2, (h+1)/2
	y, u, v = make([]byte, w*h), make([]byte, cw*ch), make([]byte, cw*ch)
	for r := 0; r < h; r++ {
		for c := 0; c < w; c++ {
			y[r*w+c] = byte(70 + (c*100/w+r*50/h)%120)
		}
	}
	for r := 0; r < ch; r++ {
		for c := 0; c < cw; c++ {
			u[r*cw+c] = byte(100 + (c*40/cw)%50)
			v[r*cw+c] = byte(110 + (r*40/ch)%50)
		}
	}
	return
}

// encodeRaw lays I420 planes out the way a camera delivers the given V4L2 format, with a padded row pitch.
func encodeRaw(fourccv uint32, w, h, stride int, y, u, v []byte) []byte {
	cw, ch := (w+1)/2, (h+1)/2
	switch fourccv {
	case pixYUYV, pixUYVY:
		buf := make([]byte, stride*h)
		for r := 0; r < h; r++ {
			for c := 0; c < w; c += 2 {
				o := r*stride + c*2
				uu, vv := u[(r/2)*cw+c/2], v[(r/2)*cw+c/2] // 4:2:2 with the chroma row repeated
				if fourccv == pixYUYV {
					buf[o], buf[o+1], buf[o+2], buf[o+3] = y[r*w+c], uu, y[r*w+c+1], vv
				} else {
					buf[o], buf[o+1], buf[o+2], buf[o+3] = uu, y[r*w+c], vv, y[r*w+c+1]
				}
			}
		}
		return buf
	case pixNV12, pixNV21:
		buf := make([]byte, stride*h+stride*ch)
		for r := 0; r < h; r++ {
			copy(buf[r*stride:], y[r*w:(r+1)*w])
		}
		for r := 0; r < ch; r++ {
			for c := 0; c < cw; c++ {
				a, b := u[r*cw+c], v[r*cw+c]
				if fourccv == pixNV21 {
					a, b = b, a
				}
				buf[stride*h+r*stride+2*c], buf[stride*h+r*stride+2*c+1] = a, b
			}
		}
		return buf
	case pixYU12, pixYV12:
		cs := (stride + 1) / 2
		buf := make([]byte, stride*h+2*cs*ch)
		for r := 0; r < h; r++ {
			copy(buf[r*stride:], y[r*w:(r+1)*w])
		}
		p1, p2 := u, v
		if fourccv == pixYV12 {
			p1, p2 = v, u
		}
		for r := 0; r < ch; r++ {
			copy(buf[stride*h+r*cs:], p1[r*cw:(r+1)*cw])
			copy(buf[stride*h+cs*ch+r*cs:], p2[r*cw:(r+1)*cw])
		}
		return buf
	case pixRGB3, pixBGR3:
		buf := make([]byte, stride*h)
		for r := 0; r < h; r++ {
			for c := 0; c < w; c++ {
				R, G, B := capture.YUVToRGB(y[r*w+c], u[(r/2)*cw+c/2], v[(r/2)*cw+c/2])
				o := r*stride + c*3
				if fourccv == pixRGB3 {
					buf[o], buf[o+1], buf[o+2] = R, G, B
				} else {
					buf[o], buf[o+1], buf[o+2] = B, G, R
				}
			}
		}
		return buf
	case pixGREY:
		buf := make([]byte, stride*h)
		for r := 0; r < h; r++ {
			for c := 0; c < w; c++ {
				buf[r*stride+c] = byte((int(y[r*w+c]) - 16) * 255 / 219)
			}
		}
		return buf
	}
	panic("unknown format")
}

func jpegFrame(t testing.TB, w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for r := 0; r < h; r++ {
		for c := 0; c < w; c++ {
			img.Set(c, r, color.RGBA{uint8(c * 255 / w), uint8(r * 255 / h), 128, 255})
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func startCam(t *testing.T, c *mockCam, vc capture.VideoConstraints) (*Source, <-chan *capture.Frame, *fakeFS) {
	t.Helper()
	fs := newFS()
	fs.add("/dev/video0", c)
	p := fs.provider()
	devs := p.Devices()
	if len(devs) != 1 {
		t.Fatalf("devices: %+v", devs)
	}
	vs, err := p.OpenVideo(devs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	ch, err := vs.Start(vc)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return vs.(*Source), ch, fs
}

func recv(t *testing.T, ch <-chan *capture.Frame) *capture.Frame {
	t.Helper()
	select {
	case f, ok := <-ch:
		if !ok {
			t.Fatal("frame channel closed")
		}
		return f
	case <-time.After(3 * time.Second):
		t.Fatal("no frame within 3s")
	}
	return nil
}

func TestEveryPixelFormatEndToEnd(t *testing.T) {
	const w, h = 64, 48
	y, u, v := smooth(w, h)
	for _, tc := range []struct {
		name   string
		fourcc uint32
		stride int
		tol    float64 // allowed deviation per sample from the source planes
	}{
		{"YUYV", pixYUYV, w*2 + 16, 0},
		{"UYVY", pixUYVY, w * 2, 0},
		{"NV12", pixNV12, w + 8, 0},
		{"NV21", pixNV21, w, 0},
		{"YU12", pixYU12, w + 4, 0},
		{"YV12", pixYV12, w, 0},
		{"RGB3", pixRGB3, w*3 + 6, 3},
		{"BGR3", pixBGR3, w * 3, 3},
		{"GREY", pixGREY, w, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newMockCam()
			c.formats = []mockFormat{discrete(tc.fourcc, tc.name, []uint32{30}, [2]uint32{w, h})}
			c.stride = tc.stride
			cam, ch, _ := startCam(t, c, capture.VideoConstraints{Width: w, Height: h, FrameRate: 30})
			defer cam.Stop()
			c.push(encodeRaw(tc.fourcc, w, h, tc.stride, y, u, v))
			f := recv(t, ch)
			if f.W != w || f.H != h {
				t.Fatalf("frame %dx%d", f.W, f.H)
			}
			maxd := 0.0
			cmp := func(got []byte, gs int, want []byte, ws, pw, ph int) {
				for r := 0; r < ph; r++ {
					for cc := 0; cc < pw; cc++ {
						maxd = math.Max(maxd, math.Abs(float64(got[r*gs+cc])-float64(want[r*ws+cc])))
					}
				}
			}
			cmp(f.Y, f.YStride, y, w, w, h)
			if tc.fourcc != pixGREY {
				cmp(f.U, f.UVStride, u, w/2, w/2, h/2)
				cmp(f.V, f.UVStride, v, w/2, w/2, h/2)
			} else if f.U[0] != 128 || f.V[0] != 128 {
				t.Errorf("grey chroma %d %d", f.U[0], f.V[0])
			}
			if maxd > tc.tol {
				t.Errorf("deviation from the source planes %.1f > %.1f", maxd, tc.tol)
			}
			if gw, gh, fps := cam.Mode(); gw != w || gh != h || fps != 30 {
				t.Errorf("Mode() = %d %d %v", gw, gh, fps)
			}
		})
	}
}

func TestMJPEGEndToEnd(t *testing.T) {
	const w, h = 64, 48
	c := newMockCam()
	c.formats = []mockFormat{discrete(pixMJPG, "MJPG", []uint32{30}, [2]uint32{w, h})}
	cam, ch, _ := startCam(t, c, capture.VideoConstraints{Width: w, Height: h})
	defer cam.Stop()
	data := jpegFrame(t, w, h)
	c.push(data)
	f := recv(t, ch)
	want := capture.NewFrame(w, h)
	if err := capture.DecodeMJPEG(want, data); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(f.Y, want.Y) || !bytes.Equal(f.U, want.U) || !bytes.Equal(f.V, want.V) {
		t.Fatal("MJPEG frame differs from the reference decode")
	}
}

func TestStopIsPrompt(t *testing.T) {
	c := newMockCam()
	c.formats = []mockFormat{discrete(pixYUYV, "YUYV", []uint32{30}, [2]uint32{64, 48})}
	cam, ch, _ := startCam(t, c, capture.VideoConstraints{})
	start := time.Now()
	if err := cam.Stop(); err != nil {
		t.Fatal(err)
	}
	// the poll slice is 250 ms: a prompt Stop must not wait for it
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Fatalf("Stop took %v with no frames flowing", d)
	}
	for range ch { // channel closes
	}
	if cam.Err() != nil {
		t.Fatalf("clean stop reported %v", cam.Err())
	}
	if !c.closed.Load() || c.streamOffs.Load() != 1 || c.mmaps.Load() != 4 || c.unmap.Load() != 4 || c.freeBufs.Load() != 1 {
		t.Fatalf("teardown: closed=%v streamoff=%d mmap=%d munmap=%d reqbufs0=%d", c.closed.Load(), c.streamOffs.Load(), c.mmaps.Load(), c.unmap.Load(), c.freeBufs.Load())
	}
	cam.Stop() // idempotent
}

func TestExclusiveAccess(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(c *mockCam)
	}{
		{"S_FMT", func(c *mockCam) { c.busyOnSFmt = true }},
		{"REQBUFS", func(c *mockCam) { c.busyOnReqBufs = true }},
		{"STREAMON", func(c *mockCam) { c.busyOnStreamOn = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newMockCam()
			c.formats = []mockFormat{discrete(pixYUYV, "YUYV", []uint32{30}, [2]uint32{64, 48})}
			fs := newFS()
			fs.add("/dev/video0", c)
			p := fs.provider()
			vs, _ := p.OpenVideo(p.Devices()[0].ID)
			tc.set(c)
			_, err := vs.Start(capture.VideoConstraints{})
			if !errors.Is(err, capture.ErrInUse) {
				t.Fatalf("got %v, want ErrInUse", err)
			}
			if !c.closed.Load() {
				t.Fatal("device left open after a failed start")
			}
			if c.mmaps.Load() != c.unmap.Load() {
				t.Fatalf("mapped %d, unmapped %d", c.mmaps.Load(), c.unmap.Load())
			}
		})
	}
	// open() itself refused (another process holds it exclusively)
	c := newMockCam()
	c.formats = []mockFormat{discrete(pixYUYV, "YUYV", []uint32{30}, [2]uint32{64, 48})}
	fs := newFS()
	fs.add("/dev/video0", c)
	p := fs.provider()
	vs, _ := p.OpenVideo(p.Devices()[0].ID)
	fs.busy["/dev/video0"] = true
	if _, err := vs.Start(capture.VideoConstraints{}); !errors.Is(err, capture.ErrInUse) {
		t.Fatalf("open EBUSY: %v", err)
	}
	// a second Start on the same Source is refused
	fs.busy["/dev/video0"] = false
	if _, err := vs.Start(capture.VideoConstraints{}); err == nil {
		t.Fatal("Start twice")
	}
}

func TestHotUnplug(t *testing.T) {
	c := newMockCam()
	c.formats = []mockFormat{discrete(pixYUYV, "YUYV", []uint32{30}, [2]uint32{64, 48})}
	y, u, v := smooth(64, 48)
	cam, ch, _ := startCam(t, c, capture.VideoConstraints{})
	c.push(encodeRaw(pixYUYV, 64, 48, 128, y, u, v))
	recv(t, ch)
	c.unplug()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if ok {
				continue
			}
			if err := cam.Err(); !errors.Is(err, capture.ErrNotFound) {
				t.Fatalf("Err() = %v, want ErrNotFound", err)
			}
			if !c.closed.Load() || c.mmaps.Load() != c.unmap.Load() {
				t.Fatal("resources not released after unplug")
			}
			return
		case <-deadline:
			t.Fatal("channel did not close after unplug")
		}
	}
}

func TestDQBufErrors(t *testing.T) {
	c := newMockCam()
	c.formats = []mockFormat{discrete(pixYUYV, "YUYV", []uint32{30}, [2]uint32{64, 48})}
	y, u, v := smooth(64, 48)
	raw := encodeRaw(pixYUYV, 64, 48, 128, y, u, v)
	cam, ch, _ := startCam(t, c, capture.VideoConstraints{})
	defer cam.Stop()
	// a buffer flagged ERROR is dropped and recycled; the next good one arrives
	c.pushFlag(raw, true)
	c.push(raw)
	recv(t, ch)
	c.mu.Lock()
	left := len(c.pending)
	c.mu.Unlock()
	if left != 0 {
		t.Fatalf("%d frames left pending", left)
	}
	// a transient EIO from DQBUF is survived
	c.mu.Lock()
	c.dqErr = syscall.EIO
	c.mu.Unlock()
	c.push(raw)
	c.push(raw)
	recv(t, ch)
	if cam.Err() != nil {
		t.Fatalf("transient error ended the stream: %v", cam.Err())
	}
}

func TestUndecodableFramesEndTheStream(t *testing.T) {
	c := newMockCam()
	c.formats = []mockFormat{discrete(pixMJPG, "MJPG", []uint32{30}, [2]uint32{64, 48})}
	cam, ch, _ := startCam(t, c, capture.VideoConstraints{})
	for i := 0; i < maxBadInARow+2; i++ {
		c.push([]byte("garbage"))
		time.Sleep(time.Millisecond)
	}
	for f := range ch {
		_ = f
	}
	if err := cam.Err(); err == nil {
		t.Fatal("persistent garbage should end the stream with an error")
	}
	if !c.closed.Load() {
		t.Fatal("device not closed")
	}
}

func TestDriverAdjustsFormat(t *testing.T) {
	c := newMockCam()
	c.formats = []mockFormat{discrete(pixYUYV, "YUYV", []uint32{30}, [2]uint32{1280, 720})}
	c.adjust = func(p *pixFormat) { p.Width, p.Height = 640, 480 } // driver only does VGA
	cam, ch, _ := startCam(t, c, capture.VideoConstraints{Width: 1280, Height: 720})
	defer cam.Stop()
	y, u, v := smooth(640, 480)
	c.push(encodeRaw(pixYUYV, 640, 480, 1280, y, u, v))
	f := recv(t, ch)
	if f.W != 640 || f.H != 480 {
		t.Fatalf("frame %dx%d: the negotiated format was not used", f.W, f.H)
	}
	if w, h, _ := cam.Mode(); w != 640 || h != 480 {
		t.Fatalf("Mode() %dx%d", w, h)
	}
}

func TestFrameRateControl(t *testing.T) {
	for _, tc := range []struct {
		fps      float64
		min, max int
	}{
		{30, 59, 60},
		{15, 29, 31},
		{24, 47, 49},
		{10, 19, 21},
	} {
		c := newMockCam()
		c.formats = []mockFormat{discrete(pixYUYV, "YUYV", []uint32{30}, [2]uint32{16, 16})}
		cam, ch, _ := startCam(t, c, capture.VideoConstraints{Width: 16, Height: 16, FrameRate: tc.fps})
		y, u, v := smooth(16, 16)
		raw := encodeRaw(pixYUYV, 16, 16, 32, y, u, v)
		// 60 device frames == 2 seconds of a 30 fps camera (timestamps come from the mock's frame counter)
		got := 0
		done := make(chan struct{})
		go func() {
			for range ch {
				got++
			}
			close(done)
		}()
		for i := 0; i < 60; i++ {
			c.push(raw)
			// let the loop consume at its own pace; the mock needs a queued buffer for each frame
			for {
				c.mu.Lock()
				n := len(c.pending)
				c.mu.Unlock()
				if n == 0 {
					break
				}
				time.Sleep(200 * time.Microsecond)
			}
		}
		time.Sleep(20 * time.Millisecond)
		cam.Stop()
		<-done
		if got < tc.min || got > tc.max {
			t.Errorf("requested %v fps from a 30 fps camera: delivered %d of 60 frames, want %d..%d", tc.fps, got, tc.min, tc.max)
		}
	}
}

func TestNoSParmDriver(t *testing.T) {
	c := newMockCam()
	c.noSParm = true
	c.formats = []mockFormat{discrete(pixYUYV, "YUYV", []uint32{30}, [2]uint32{16, 16})}
	cam, ch, _ := startCam(t, c, capture.VideoConstraints{Width: 16, Height: 16, FrameRate: 30})
	defer cam.Stop()
	y, u, v := smooth(16, 16)
	c.push(encodeRaw(pixYUYV, 16, 16, 32, y, u, v))
	recv(t, ch)
}

func TestOpenCloseLeaks(t *testing.T) {
	c := newMockCam()
	c.formats = []mockFormat{discrete(pixYUYV, "YUYV", []uint32{30}, [2]uint32{32, 16})}
	fs := newFS()
	fs.add("/dev/video0", c)
	p := fs.provider()
	id := p.Devices()[0].ID
	y, u, v := smooth(32, 16)
	raw := encodeRaw(pixYUYV, 32, 16, 64, y, u, v)
	runtime.GC()
	before := runtime.NumGoroutine()
	for i := 0; i < 100; i++ {
		vs, err := p.OpenVideo(id)
		if err != nil {
			t.Fatal(err)
		}
		ch, err := vs.Start(capture.VideoConstraints{})
		if err != nil {
			t.Fatalf("cycle %d: %v", i, err)
		}
		c.push(raw)
		recv(t, ch)
		if err := vs.Stop(); err != nil {
			t.Fatal(err)
		}
		if !c.closed.Load() {
			t.Fatalf("cycle %d: device not closed", i)
		}
		c.mu.Lock()
		c.pending, c.pendingEr = nil, nil
		c.mu.Unlock()
	}
	if c.mmaps.Load() != c.unmap.Load() || c.mmaps.Load() != 400 {
		t.Fatalf("mmap %d munmap %d, want 400 each", c.mmaps.Load(), c.unmap.Load())
	}
	if c.streamOffs.Load() != 100 || c.freeBufs.Load() != 100 {
		t.Fatalf("streamoff %d reqbufs(0) %d", c.streamOffs.Load(), c.freeBufs.Load())
	}
	for i := 0; i < 100 && runtime.NumGoroutine() > before+1; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > before+1 {
		t.Fatalf("goroutines %d -> %d", before, n)
	}
}

func TestPace(t *testing.T) {
	p := newPace(30, 30)
	for i := 0; i < 5; i++ {
		if !p.take(float64(i) / 30) {
			t.Fatal("full rate drops frames")
		}
	}
	p = newPace(30, 15)
	n := 0
	for i := 0; i < 90; i++ {
		if p.take(float64(i) / 30) {
			n++
		}
	}
	if n != 45 {
		t.Fatalf("15 of 30 over 3 s gave %d frames", n)
	}
	// a stall does not cause a burst
	p = newPace(30, 10)
	p.take(0)
	p.take(10)
	taken := 0
	for i := 1; i <= 6; i++ {
		if p.take(10 + float64(i)/30) {
			taken++
		}
	}
	if taken > 2 {
		t.Fatalf("burst after a stall: %d frames", taken)
	}
}
