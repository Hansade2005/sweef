//go:build linux && (amd64 || arm64)

package v4l2

import (
	"errors"
	"fmt"
	"math"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/sweefbrowser/sweef/internal/capture"
)

const (
	numBuffers     = 4
	pollSlice      = 250 * time.Millisecond // upper bound between wake-up checks; Stop interrupts it immediately
	staleAfter     = 5 * time.Second        // no frame for this long: the camera is dead (unplugged hub, hung firmware)
	maxBadInARow   = 60                     // consecutive undecodable frames before giving up
	frameDropRatio = 0.5                    // emit a frame up to this fraction of a device interval early (see pace)
)

// Source streams one camera. It implements capture.VideoSource and capture.Moder.
type Source struct {
	info  capture.DeviceInfo
	path  string
	open  func(path string) (Device, error)
	modes []fmtMode

	mu      sync.Mutex
	started bool
	stopped bool
	dev     Device
	done    chan struct{}
	err     error
	w, h    int
	fps     float64 // the rate frames are delivered at
	pix     uint32
	stride  int
	bufs    [][]byte
}

func (s *Source) Info() capture.DeviceInfo { return s.info }

// Mode reports the negotiated size and delivery rate.
func (s *Source) Mode() (int, int, float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w, s.h, s.fps
}

func (s *Source) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// mapErr turns a kernel error into the capture package's vocabulary.
func mapErr(op string, err error) error {
	switch errno(err) {
	case syscall.EBUSY:
		return fmt.Errorf("%w (%s: %v)", capture.ErrInUse, op, err)
	case syscall.ENODEV, syscall.ENOENT, syscall.ENXIO, syscall.EPIPE, syscall.EIO:
		return fmt.Errorf("%w (%s: %v)", capture.ErrNotFound, op, err)
	case syscall.EACCES, syscall.EPERM:
		return fmt.Errorf("capture: permission denied opening the camera (%s: %v)", op, err)
	}
	return fmt.Errorf("v4l2: %s: %w", op, err)
}

// Start negotiates the mode closest to c, maps the buffers and begins streaming.
func (s *Source) Start(c capture.VideoConstraints) (<-chan *capture.Frame, error) {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return nil, errors.New("v4l2: source already started")
	}
	s.started = true
	s.mu.Unlock()

	dev, err := s.open(s.path)
	if err != nil {
		return nil, mapErr("open "+s.path, err)
	}
	ch, err := s.start(dev, c)
	if err != nil {
		s.teardown(dev, false)
		return nil, err
	}
	return ch, nil
}

func (s *Source) start(dev Device, c capture.VideoConstraints) (<-chan *capture.Frame, error) {
	want, ok := chooseMode(s.modes, c)
	if !ok {
		return nil, fmt.Errorf("v4l2: %s offers no usable video format", s.info.Label)
	}
	f := format{Type: bufTypeVideoCapture}
	f.Pix = pixFormat{Width: uint32(want.w), Height: uint32(want.h), PixelFormat: want.fourcc, Field: fieldAny}
	if err := dev.Ioctl(vidiocSFmt, unsafe.Pointer(&f)); err != nil {
		return nil, mapErr("VIDIOC_S_FMT", err)
	}
	// the driver may adjust anything: believe what came back
	if _, ok := supported[f.Pix.PixelFormat]; !ok {
		return nil, fmt.Errorf("v4l2: driver switched to unsupported pixel format %08x", f.Pix.PixelFormat)
	}
	w, h := int(f.Pix.Width), int(f.Pix.Height)
	stride := int(f.Pix.BytesPerLine)
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("v4l2: driver returned a %dx%d format", w, h)
	}

	// frame interval: ask for the requested rate within what the mode supports
	fps := c.FrameRate
	if fps <= 0 {
		fps = 30
	}
	devFPS := math.Min(fps, want.fpsMax)
	devFPS = math.Max(devFPS, math.Max(want.fpsMin, 1))
	sp := streamParm{Type: bufTypeVideoCapture}
	sp.Capture.TimePerFrame = fract{Numerator: 1000, Denominator: uint32(math.Round(devFPS * 1000))}
	if err := dev.Ioctl(vidiocSParm, unsafe.Pointer(&sp)); err == nil {
		if n, d := sp.Capture.TimePerFrame.Numerator, sp.Capture.TimePerFrame.Denominator; n > 0 && d > 0 {
			devFPS = float64(d) / float64(n) // what the driver actually chose
		}
	} // a driver without S_PARM runs at its own rate; devFPS stays a hint, the pacing below measures the truth
	deliver := math.Min(fps, devFPS)

	rb := requestBuffers{Count: numBuffers, Type: bufTypeVideoCapture, Memory: memoryMMap}
	if err := dev.Ioctl(vidiocReqBufs, unsafe.Pointer(&rb)); err != nil {
		return nil, mapErr("VIDIOC_REQBUFS", err)
	}
	if rb.Count < 2 {
		return nil, errors.New("v4l2: driver granted fewer than two buffers")
	}
	bufs := make([][]byte, 0, rb.Count)
	for i := uint32(0); i < rb.Count; i++ {
		b := buffer{Index: i, Type: bufTypeVideoCapture, Memory: memoryMMap}
		if err := dev.Ioctl(vidiocQueryBuf, unsafe.Pointer(&b)); err != nil {
			return nil, mapErr("VIDIOC_QUERYBUF", err)
		}
		mem, err := dev.Mmap(int64(uint32(b.M)), int(b.Length))
		if err != nil {
			return nil, mapErr("mmap", err)
		}
		bufs = append(bufs, mem)
		s.mu.Lock()
		s.bufs = bufs
		s.mu.Unlock()
		if err := dev.Ioctl(vidiocQBuf, unsafe.Pointer(&b)); err != nil {
			return nil, mapErr("VIDIOC_QBUF", err)
		}
	}
	typ := int32(bufTypeVideoCapture)
	if err := dev.Ioctl(vidiocStreamOn, unsafe.Pointer(&typ)); err != nil {
		return nil, mapErr("VIDIOC_STREAMON", err)
	}

	s.mu.Lock()
	s.dev, s.w, s.h, s.fps, s.pix, s.stride = dev, w, h, deliver, f.Pix.PixelFormat, stride
	s.done = make(chan struct{})
	done := s.done
	s.mu.Unlock()
	out := make(chan *capture.Frame, 2)
	go s.loop(dev, out, done, devFPS, deliver)
	return out, nil
}

// teardown releases everything the kernel handed out. It is safe to call after a partial start.
func (s *Source) teardown(dev Device, streaming bool) {
	if streaming {
		typ := int32(bufTypeVideoCapture)
		_ = dev.Ioctl(vidiocStreamOff, unsafe.Pointer(&typ))
	}
	s.mu.Lock()
	bufs := s.bufs
	s.bufs = nil
	s.mu.Unlock()
	for _, b := range bufs {
		_ = dev.Munmap(b)
	}
	if len(bufs) > 0 {
		rb := requestBuffers{Count: 0, Type: bufTypeVideoCapture, Memory: memoryMMap}
		_ = dev.Ioctl(vidiocReqBufs, unsafe.Pointer(&rb)) // frees the driver's buffers so the next opener can allocate
	}
	_ = dev.Close()
}

// pace decides which device frames to deliver when the page asked for fewer frames per second than the camera
// runs at. Targets are spaced 1/deliver apart; a device frame within half a device interval of the next target is
// taken, so 24 fps from a 30 fps camera yields 24 on average instead of 15.
type pace struct {
	interval, devInterval float64
	next                  float64
	started               bool
}

func newPace(devFPS, deliver float64) *pace {
	if deliver <= 0 || devFPS <= 0 {
		return &pace{}
	}
	return &pace{interval: 1 / deliver, devInterval: 1 / devFPS}
}

func (p *pace) take(t float64) bool {
	if p.interval == 0 || p.interval <= p.devInterval*1.05 {
		return true // delivering the camera's own rate
	}
	if !p.started {
		p.started, p.next = true, t+p.interval
		return true
	}
	if t >= p.next-p.devInterval*frameDropRatio {
		p.next += p.interval
		if p.next < t { // fell behind (a long stall): do not burst
			p.next = t + p.interval
		}
		return true
	}
	return false
}

func (s *Source) fail(err error) {
	s.mu.Lock()
	if s.err == nil {
		s.err = err
	}
	s.mu.Unlock()
}

func (s *Source) stopRequested() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopped
}

func (s *Source) loop(dev Device, out chan *capture.Frame, done chan struct{}, devFPS, deliver float64) {
	defer close(done)
	defer close(out)
	defer func() { s.teardown(dev, true) }()

	s.mu.Lock()
	w, h, pix, stride, bufs := s.w, s.h, s.pix, s.stride, s.bufs
	s.mu.Unlock()
	info := supported[pix]
	pc := newPace(devFPS, deliver)
	start := time.Now()
	var firstTS float64 = -1
	lastGood := time.Now()
	bad := 0

	for {
		if s.stopRequested() {
			return
		}
		r, err := dev.Poll(pollSlice)
		if err != nil {
			s.fail(mapErr("poll", err))
			return
		}
		if s.stopRequested() {
			return
		}
		if r.Hup {
			s.fail(fmt.Errorf("%w (device error or removal)", capture.ErrNotFound))
			return
		}
		if !r.Ready {
			if time.Since(lastGood) > staleAfter {
				s.fail(fmt.Errorf("%w (no frames for %v)", capture.ErrNotFound, staleAfter))
				return
			}
			continue
		}
		// drain: more than one buffer can be ready after a stall; deliver the newest of a burst
		for {
			b := buffer{Type: bufTypeVideoCapture, Memory: memoryMMap}
			if err := dev.Ioctl(vidiocDQBuf, unsafe.Pointer(&b)); err != nil {
				switch errno(err) {
				case syscall.EAGAIN:
				case syscall.EIO: // transient "bad frame" some drivers report; the buffer was not dequeued
				default:
					s.fail(mapErr("VIDIOC_DQBUF", err))
					return
				}
				break
			}
			lastGood = time.Now()
			now := time.Since(start).Seconds()
			if b.Flags&bufFlagTimestampMask == bufFlagTimestampMonotone {
				ts := float64(b.Timestamp.Sec) + float64(b.Timestamp.Usec)/1e6
				if firstTS < 0 {
					firstTS = ts
				}
				now = ts - firstTS
			}
			var frame *capture.Frame
			if b.Flags&bufFlagError == 0 && int(b.Index) < len(bufs) && pc.take(now) {
				n := int(b.BytesUsed)
				if n == 0 || n > len(bufs[b.Index]) {
					n = len(bufs[b.Index])
				}
				frame = capture.NewFrame(w, h)
				var cerr error
				if info.mjpg {
					cerr = capture.DecodeMJPEG(frame, bufs[b.Index][:n])
				} else {
					cerr = capture.Convert(frame, info.pix, bufs[b.Index][:n], stride)
				}
				if cerr != nil {
					frame = nil
					if bad++; bad >= maxBadInARow {
						s.fail(fmt.Errorf("v4l2: %d undecodable frames in a row, last: %w", bad, cerr))
						_ = dev.Ioctl(vidiocQBuf, unsafe.Pointer(&b))
						return
					}
				} else {
					bad = 0
					frame.PTS = time.Duration(now * float64(time.Second))
				}
			}
			if err := dev.Ioctl(vidiocQBuf, unsafe.Pointer(&b)); err != nil {
				s.fail(mapErr("VIDIOC_QBUF", err))
				return
			}
			if frame != nil {
				select { // never wait for a slow consumer: drop the oldest frame
				case out <- frame:
				default:
					select {
					case <-out:
					default:
					}
					select {
					case out <- frame:
					default:
					}
				}
			}
		}
	}
}

// Stop interrupts the poll, stops streaming and releases the device. It returns once the kernel resources are
// gone, so a following Start (here or in another process) can succeed.
func (s *Source) Stop() error {
	s.mu.Lock()
	s.stopped = true
	dev, done := s.dev, s.done
	s.mu.Unlock()
	if dev != nil {
		dev.Wake()
	}
	if done != nil {
		<-done
	}
	return nil
}
