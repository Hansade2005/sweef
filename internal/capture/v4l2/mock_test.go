//go:build linux && (amd64 || arm64)

package v4l2

import (
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

// mockSize / mockFormat describe what the fake camera offers.
type mockSize struct {
	w, h      uint32
	intervals [][2]uint32 // discrete {numerator, denominator}; nil with stepwise set
	stepwise  *[6]uint32  // {min, max, step} fractions {num,den,num,den,num,den}
}

type mockFormat struct {
	fourcc uint32
	desc   string
	sizes  []mockSize
	// stepwise sizes: {min_w, max_w, step_w, min_h, max_h, step_h}
	stepSizes *[6]uint32
	stepIvals [][2]uint32 // intervals offered for stepwise sizes
}

// mockCam implements Device and the part of the V4L2 ioctl protocol the provider uses. It is a faithful state
// machine for the streaming I/O sequence (REQBUFS -> QUERYBUF -> mmap -> QBUF -> STREAMON -> poll/DQBUF/QBUF ->
// STREAMOFF -> munmap -> REQBUFS(0)) and counts every resource so the tests can assert nothing leaks.
type mockCam struct {
	mu sync.Mutex
	// identity
	driver, card, bus string
	capabilities      uint32
	deviceCaps        uint32
	formats           []mockFormat
	// behaviour switches
	busyOnSFmt, busyOnReqBufs, busyOnStreamOn bool
	adjust                                    func(*pixFormat) // the driver "fixes up" the requested format
	stride                                    int              // bytesperline to report (0 = natural)
	noSParm                                   bool
	devFPS                                    uint32
	// state
	cur       pixFormat
	mem       [][]byte
	mapped    map[int]bool
	queued    []uint32
	pending   [][]byte
	pendingEr []bool // deliver this frame with V4L2_BUF_FLAG_ERROR
	streaming bool
	bufCount  uint32
	seq       uint32
	removed   bool
	woke      bool
	notify    chan struct{}
	// counters / log
	closed       atomic.Bool
	mmaps, unmap atomic.Int32
	streamOffs   atomic.Int32
	freeBufs     atomic.Int32 // REQBUFS(0)
	sfmt         atomic.Int32
	dqErr        syscall.Errno // returned by the next DQBUF when set
}

func newMockCam() *mockCam {
	return &mockCam{driver: "uvcvideo", card: "Mock Webcam", bus: "usb-0000:00:14.0-1", capabilities: capVideoCapture | capStreaming | capDeviceCaps,
		deviceCaps: capVideoCapture | capStreaming, notify: make(chan struct{}, 1), mapped: map[int]bool{}, devFPS: 30}
}

func (m *mockCam) signal() {
	select {
	case m.notify <- struct{}{}:
	default:
	}
}

// push queues one raw frame as the camera would produce it.
func (m *mockCam) push(frame []byte) { m.pushFlag(frame, false) }

func (m *mockCam) pushFlag(frame []byte, bad bool) {
	m.mu.Lock()
	m.pending = append(m.pending, frame)
	m.pendingEr = append(m.pendingEr, bad)
	m.mu.Unlock()
	m.signal()
}

// unplug makes the device disappear: poll reports an error, ioctls return ENODEV.
func (m *mockCam) unplug() {
	m.mu.Lock()
	m.removed = true
	m.mu.Unlock()
	m.signal()
}

func (m *mockCam) Ioctl(req uintptr, arg unsafe.Pointer) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.removed && req != vidiocStreamOff {
		return syscall.ENODEV
	}
	switch req {
	case vidiocQueryCap:
		c := (*capability)(arg)
		copy(c.Driver[:], m.driver)
		copy(c.Card[:], m.card)
		copy(c.BusInfo[:], m.bus)
		c.Capabilities, c.DeviceCaps = m.capabilities, m.deviceCaps
		return nil
	case vidiocEnumFmt:
		d := (*fmtDesc)(arg)
		if d.Type != bufTypeVideoCapture || int(d.Index) >= len(m.formats) {
			return syscall.EINVAL
		}
		f := m.formats[d.Index]
		d.PixelFormat = f.fourcc
		copy(d.Description[:], f.desc)
		return nil
	case vidiocEnumFrameSizes:
		s := (*frmSizeEnum)(arg)
		f := m.fmtOf(s.PixelFormat)
		if f == nil {
			return syscall.EINVAL
		}
		if f.stepSizes != nil {
			if s.Index != 0 {
				return syscall.EINVAL
			}
			s.Type = frmSizeStepwise
			s.U = *f.stepSizes
			return nil
		}
		if int(s.Index) >= len(f.sizes) {
			return syscall.EINVAL
		}
		s.Type = frmSizeDiscrete
		s.U[0], s.U[1] = f.sizes[s.Index].w, f.sizes[s.Index].h
		return nil
	case vidiocEnumFrameIntervs:
		v := (*frmIvalEnum)(arg)
		f := m.fmtOf(v.PixelFormat)
		if f == nil {
			return syscall.EINVAL
		}
		var iv [][2]uint32
		if f.stepSizes != nil {
			iv = f.stepIvals
		} else {
			for _, s := range f.sizes {
				if s.w == v.Width && s.h == v.Height {
					iv = s.intervals
				}
			}
		}
		if int(v.Index) >= len(iv) {
			return syscall.EINVAL
		}
		v.Type = frmSizeDiscrete
		v.U[0], v.U[1] = iv[v.Index][0], iv[v.Index][1]
		return nil
	case vidiocSFmt:
		m.sfmt.Add(1)
		if m.busyOnSFmt {
			return syscall.EBUSY
		}
		f := (*format)(arg)
		p := f.Pix
		if m.adjust != nil {
			m.adjust(&p)
		}
		m.fill(&p)
		m.cur = p
		f.Pix = p
		return nil
	case vidiocSParm:
		if m.noSParm {
			return syscall.ENOTTY
		}
		sp := (*streamParm)(arg)
		// the driver picks the nearest supported rate; the mock supports exactly devFPS
		sp.Capture.TimePerFrame = fract{Numerator: 1, Denominator: m.devFPS}
		return nil
	case vidiocReqBufs:
		rb := (*requestBuffers)(arg)
		if rb.Count == 0 {
			m.freeBufs.Add(1)
			m.mem, m.queued, m.bufCount = nil, nil, 0
			return nil
		}
		if m.busyOnReqBufs {
			return syscall.EBUSY
		}
		n := rb.Count
		m.bufCount = n
		size := int(m.cur.SizeImage)
		m.mem = make([][]byte, n)
		for i := range m.mem {
			m.mem[i] = make([]byte, size)
		}
		return nil
	case vidiocQueryBuf:
		b := (*buffer)(arg)
		if int(b.Index) >= len(m.mem) {
			return syscall.EINVAL
		}
		b.Length = uint32(len(m.mem[b.Index]))
		b.M = uint64(b.Index) * 0x10000
		return nil
	case vidiocQBuf:
		b := (*buffer)(arg)
		if int(b.Index) >= len(m.mem) {
			return syscall.EINVAL
		}
		m.queued = append(m.queued, b.Index)
		m.signal()
		return nil
	case vidiocStreamOn:
		if m.busyOnStreamOn {
			return syscall.EBUSY
		}
		m.streaming = true
		return nil
	case vidiocStreamOff:
		m.streamOffs.Add(1)
		m.streaming = false
		m.queued = nil
		return nil
	case vidiocDQBuf:
		b := (*buffer)(arg)
		if m.dqErr != 0 {
			e := m.dqErr
			m.dqErr = 0
			return e
		}
		if !m.streaming || len(m.queued) == 0 || len(m.pending) == 0 {
			return syscall.EAGAIN
		}
		idx := m.queued[0]
		m.queued = m.queued[1:]
		fr, bad := m.pending[0], m.pendingEr[0]
		m.pending, m.pendingEr = m.pending[1:], m.pendingEr[1:]
		n := copy(m.mem[idx], fr)
		b.Index, b.BytesUsed = idx, uint32(n)
		b.Flags = bufFlagTimestampMonotone
		if bad {
			b.Flags |= bufFlagError
		}
		b.Sequence = m.seq
		// timestamps advance by the device frame interval, regardless of wall-clock time
		us := int64(m.seq) * 1_000_000 / int64(m.devFPS)
		b.Timestamp.Sec, b.Timestamp.Usec = 1000+us/1_000_000, us%1_000_000
		m.seq++
		return nil
	}
	return syscall.ENOTTY
}

func (m *mockCam) fmtOf(fourcc uint32) *mockFormat {
	for i := range m.formats {
		if m.formats[i].fourcc == fourcc {
			return &m.formats[i]
		}
	}
	return nil
}

// fill sets the derived fields the driver reports.
func (m *mockCam) fill(p *pixFormat) {
	w := int(p.Width)
	var bpl, size int
	switch p.PixelFormat {
	case pixYUYV, pixUYVY:
		bpl = w * 2
		size = bpl * int(p.Height)
	case pixNV12, pixNV21, pixYU12, pixYV12:
		bpl = w
		size = w * int(p.Height) * 3 / 2
	case pixRGB3, pixBGR3:
		bpl = w * 3
		size = bpl * int(p.Height)
	case pixGREY:
		bpl = w
		size = w * int(p.Height)
	default: // compressed
		bpl = 0
		size = w * int(p.Height) * 2
	}
	if m.stride > 0 && bpl > 0 {
		bpl = m.stride
		if p.PixelFormat == pixYUYV || p.PixelFormat == pixUYVY || p.PixelFormat == pixRGB3 || p.PixelFormat == pixBGR3 || p.PixelFormat == pixGREY {
			size = bpl * int(p.Height)
		} else {
			size = bpl*int(p.Height) + bpl*((int(p.Height)+1)/2)
		}
	}
	p.BytesPerLine, p.SizeImage = uint32(bpl), uint32(size)
}

func (m *mockCam) Mmap(offset int64, length int) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	idx := int(offset / 0x10000)
	if idx >= len(m.mem) || length != len(m.mem[idx]) {
		return nil, syscall.EINVAL
	}
	m.mmaps.Add(1)
	m.mapped[idx] = true
	return m.mem[idx], nil
}

func (m *mockCam) Munmap(b []byte) error {
	m.unmap.Add(1)
	return nil
}

func (m *mockCam) Poll(timeout time.Duration) (PollResult, error) {
	deadline := time.After(timeout)
	for {
		m.mu.Lock()
		switch {
		case m.removed:
			m.mu.Unlock()
			return PollResult{Hup: true}, nil
		case m.woke:
			m.woke = false
			m.mu.Unlock()
			return PollResult{Woken: true}, nil
		case m.streaming && len(m.queued) > 0 && len(m.pending) > 0:
			m.mu.Unlock()
			return PollResult{Ready: true}, nil
		}
		m.mu.Unlock()
		select {
		case <-m.notify:
		case <-deadline:
			return PollResult{}, nil
		}
	}
}

func (m *mockCam) Wake() {
	m.mu.Lock()
	m.woke = true
	m.mu.Unlock()
	m.signal()
}

func (m *mockCam) Close() error { m.closed.Store(true); return nil }

// ---- test fixtures -----------------------------------------------------------------------------------------

// discrete builds a format with the given sizes, each offered at the given fps list.
func discrete(fourcc uint32, name string, fps []uint32, sizes ...[2]uint32) mockFormat {
	f := mockFormat{fourcc: fourcc, desc: name}
	for _, s := range sizes {
		ms := mockSize{w: s[0], h: s[1]}
		for _, r := range fps {
			ms.intervals = append(ms.intervals, [2]uint32{1, r})
		}
		f.sizes = append(f.sizes, ms)
	}
	return f
}

// fakeFS is an in-memory /dev: path -> camera, with stamps that change when a node is replaced.
type fakeFS struct {
	mu    sync.Mutex
	nodes map[string]*mockCam
	stamp map[string]string
	opens atomic.Int32
	busy  map[string]bool
}

func newFS() *fakeFS {
	return &fakeFS{nodes: map[string]*mockCam{}, stamp: map[string]string{}, busy: map[string]bool{}}
}

func (f *fakeFS) add(path string, c *mockCam) {
	f.mu.Lock()
	f.nodes[path] = c
	f.stamp[path] += "+"
	f.mu.Unlock()
}

func (f *fakeFS) remove(path string) {
	f.mu.Lock()
	delete(f.nodes, path)
	f.mu.Unlock()
}

func (f *fakeFS) provider() *Provider {
	return &Provider{
		Glob: func() ([]string, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			var out []string
			for p := range f.nodes {
				out = append(out, p)
			}
			return out, nil
		},
		Open: func(path string) (Device, error) {
			f.mu.Lock()
			c, ok := f.nodes[path]
			busy := f.busy[path]
			f.mu.Unlock()
			if !ok {
				return nil, syscall.ENOENT
			}
			if busy {
				return nil, syscall.EBUSY
			}
			f.opens.Add(1)
			c.closed.Store(false)
			return c, nil
		},
		Stamp: func(path string) string {
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.stamp[path]
		},
		entries: map[string]*entry{},
	}
}
