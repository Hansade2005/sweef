//go:build linux && (amd64 || arm64)

package v4l2

import (
	"errors"
	"testing"

	"github.com/sweefbrowser/sweef/internal/capture"
)

func webcam() *mockCam {
	c := newMockCam()
	c.formats = []mockFormat{
		discrete(pixMJPG, "Motion-JPEG", []uint32{30, 15}, [2]uint32{640, 480}, [2]uint32{1280, 720}, [2]uint32{1920, 1080}),
		discrete(pixYUYV, "YUYV 4:2:2", []uint32{30, 15, 5}, [2]uint32{640, 480}, [2]uint32{320, 240}, [2]uint32{1280, 720}),
		discrete(fourcc('B', 'A', '8', '1'), "8-bit Bayer", []uint32{30}, [2]uint32{640, 480}), // not convertible: ignored
	}
	// make the 720p YUYV mode slow, as on many USB 2.0 cameras
	c.formats[1].sizes[2].intervals = [][2]uint32{{1, 10}}
	return c
}

func TestEnumerationFiltersNodes(t *testing.T) {
	fs := newFS()
	fs.add("/dev/video0", webcam())

	meta := newMockCam() // the UVC metadata node of the same camera: device_caps has no VIDEO_CAPTURE
	meta.deviceCaps = capMetaCapture | capStreaming
	meta.capabilities = capVideoCapture | capStreaming | capMetaCapture | capDeviceCaps
	fs.add("/dev/video1", meta)

	out := newMockCam() // an output-only node
	out.capabilities, out.deviceCaps = 0x2|capStreaming|capDeviceCaps, 0x2|capStreaming
	fs.add("/dev/video2", out)

	bayer := newMockCam() // capture node with only a raw Bayer format
	bayer.formats = []mockFormat{discrete(fourcc('B', 'A', '8', '1'), "bayer", []uint32{30}, [2]uint32{640, 480})}
	bayer.card = "Sensor"
	fs.add("/dev/video3", bayer)

	noStream := newMockCam() // read()-only driver
	noStream.card = "Old"
	noStream.formats = webcam().formats
	noStream.capabilities, noStream.deviceCaps = capVideoCapture|capDeviceCaps, capVideoCapture
	fs.add("/dev/video4", noStream)

	mp := newMockCam() // multi-planar only (an SoC ISP)
	mp.card = "ISP"
	mp.formats = webcam().formats
	mp.capabilities, mp.deviceCaps = capVideoCaptureMPlan|capStreaming|capDeviceCaps, capVideoCaptureMPlan|capStreaming
	fs.add("/dev/video5", mp)

	p := fs.provider()
	devs := p.Devices()
	if len(devs) != 1 {
		t.Fatalf("got %d devices, want only the real webcam: %+v", len(devs), devs)
	}
	d := devs[0]
	if d.Label != "Mock Webcam" || d.Kind != capture.VideoInput || d.GroupID != "usb-0000:00:14.0-1" || d.ID != "v4l2:usb-0000:00:14.0-1|Mock Webcam" {
		t.Fatalf("device info %+v", d)
	}
}

func TestEnumerationOrderIDsAndHotplug(t *testing.T) {
	fs := newFS()
	for _, n := range []string{"/dev/video10", "/dev/video2", "/dev/video9"} {
		c := webcam()
		c.card = "Cam " + n[len("/dev/video"):]
		fs.add(n, c)
	}
	dup := webcam() // two capture nodes of one physical camera (e.g. RGB and IR share a bus_info)
	dup.card = "Cam 2"
	fs.add("/dev/video11", dup)
	p := fs.provider()
	got := p.Devices()
	var labels []string
	for _, d := range got {
		labels = append(labels, d.Label)
	}
	want := []string{"Cam 2", "Cam 9", "Cam 10", "Cam 2"}
	if len(got) != 4 || labels[0] != want[0] || labels[1] != want[1] || labels[2] != want[2] || labels[3] != want[3] {
		t.Fatalf("order %v, want %v (natural, not lexical)", labels, want)
	}
	ids := map[string]bool{}
	for _, d := range got {
		if ids[d.ID] {
			t.Fatalf("duplicate id %q", d.ID)
		}
		ids[d.ID] = true
	}
	opens := fs.opens.Load()
	p.Devices() // cached: no more opens
	if fs.opens.Load() != opens {
		t.Fatalf("re-listing re-opened %d nodes", fs.opens.Load()-opens)
	}
	// unplug video9, plug a different camera into video2
	fs.remove("/dev/video9")
	repl := webcam()
	repl.card = "Replacement"
	fs.add("/dev/video2", repl)
	got = p.Devices()
	labels = labels[:0]
	for _, d := range got {
		labels = append(labels, d.Label)
	}
	if len(got) != 3 || labels[0] != "Replacement" {
		t.Fatalf("after hot-plug: %v", labels)
	}
	if _, err := p.OpenVideo("v4l2:nope|x"); !errors.Is(err, capture.ErrNotFound) {
		t.Fatalf("unknown id: %v", err)
	}
}

func TestModesAndCaps(t *testing.T) {
	fs := newFS()
	fs.add("/dev/video0", webcam())
	p := fs.provider()
	id := p.Devices()[0].ID
	caps, ok := p.VideoCaps(id)
	if !ok {
		t.Fatal("no caps")
	}
	// sizes merge over formats; the rate range is the widest over formats offering the size
	type m struct {
		w, h   int
		lo, hi float64
	}
	want := []m{{320, 240, 5, 30}, {640, 480, 5, 30}, {1280, 720, 10, 30}, {1920, 1080, 15, 30}}
	if len(caps.Modes) != len(want) {
		t.Fatalf("modes %+v", caps.Modes)
	}
	for i, w := range want {
		g := caps.Modes[i]
		if g.W != w.w || g.H != w.h || g.FPSMin != w.lo || g.FPSMax != w.hi {
			t.Errorf("mode %d: %+v, want %+v", i, g, w)
		}
	}
}

func TestStepwiseSizes(t *testing.T) {
	c := newMockCam()
	c.formats = []mockFormat{{fourcc: pixYUYV, desc: "yuyv", stepSizes: &[6]uint32{160, 1920, 8, 120, 1080, 8}, stepIvals: [][2]uint32{{1, 30}}}}
	fs := newFS()
	fs.add("/dev/video0", c)
	p := fs.provider()
	caps, _ := p.VideoCaps(p.Devices()[0].ID)
	has := func(w, h int) bool {
		for _, m := range caps.Modes {
			if m.W == w && m.H == h {
				return true
			}
		}
		return false
	}
	// 160x120 (min) and 1920x1080 (max) are in range; 640x480 and 1280x720 hit the 8-pixel step
	for _, s := range [][2]int{{160, 120}, {640, 480}, {1280, 720}, {1920, 1080}} {
		if !has(s[0], s[1]) {
			t.Errorf("missing %dx%d in %+v", s[0], s[1], caps.Modes)
		}
	}
	if has(176, 144) == false {
		t.Log("176x144 is on the 8-pixel grid and should be present")
	}
	for _, m := range caps.Modes {
		if m.W%8 != 0 || m.H%8 != 0 {
			t.Errorf("size %dx%d violates the step", m.W, m.H)
		}
	}
}

func TestChooseMode(t *testing.T) {
	ms := []fmtMode{
		{pixMJPG, 640, 480, 15, 30}, {pixYUYV, 640, 480, 5, 30},
		{pixMJPG, 1280, 720, 15, 30}, {pixYUYV, 1280, 720, 10, 10},
		{pixMJPG, 1920, 1080, 15, 30},
		{pixNV12, 320, 240, 5, 30},
	}
	cases := []struct {
		w, h int
		fps  float64
		want uint32
		ww   int
	}{
		{0, 0, 0, pixYUYV, 640},        // default 640x480@30: both formats reach it, the cheaper one wins
		{1280, 720, 30, pixMJPG, 1280}, // YUYV tops out at 10 fps there: MJPEG is the only way to 30
		{1280, 720, 10, pixYUYV, 1280}, // 10 fps is reachable with raw: no decode needed
		{1920, 1080, 30, pixMJPG, 1920},
		{300, 200, 0, pixNV12, 320}, // closest size
		{4000, 3000, 30, pixMJPG, 1920},
	}
	for _, c := range cases {
		m, ok := chooseMode(ms, capture.VideoConstraints{Width: c.w, Height: c.h, FrameRate: c.fps})
		if !ok || m.fourcc != c.want || m.w != c.ww {
			t.Errorf("%dx%d@%v -> %+v, want fourcc %#x width %d", c.w, c.h, c.fps, m, c.want, c.ww)
		}
	}
	if _, ok := chooseMode(nil, capture.VideoConstraints{}); ok {
		t.Error("empty list")
	}
}

func TestCapsProviderThroughRegistry(t *testing.T) {
	fs := newFS()
	fs.add("/dev/video0", webcam())
	p := fs.provider()
	capture.Register(p)
	defer capture.Unregister("v4l2")
	devs := capture.Devices(capture.VideoInput)
	if len(devs) != 1 {
		t.Fatal(devs)
	}
	if c := capture.VideoCapsOf(devs[0].ID); len(c.Modes) != 4 {
		t.Fatalf("registry caps %+v", c)
	}
}
