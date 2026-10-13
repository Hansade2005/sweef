//go:build linux && (amd64 || arm64)

package v4l2

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"syscall"
	"unsafe"

	"github.com/sweefbrowser/sweef/internal/capture"
)

// fmtInfo says how a driver pixel format is converted.
type fmtInfo struct {
	pix  capture.PixelFormat // "" for MJPEG
	mjpg bool
	cost int // preference when several formats offer the same size: lower is cheaper to convert
}

// supported lists the pixel formats the converter understands. Anything else a camera offers (Bayer, 16-bit depth,
// H.264 elementary streams, ...) is ignored.
var supported = map[uint32]fmtInfo{
	pixYUYV: {pix: capture.PixYUYV, cost: 0},
	pixNV12: {pix: capture.PixNV12, cost: 0},
	pixYU12: {pix: capture.PixI420, cost: 0},
	pixYV12: {pix: capture.PixYV12, cost: 0},
	pixUYVY: {pix: capture.PixUYVY, cost: 1},
	pixNV21: {pix: capture.PixNV21, cost: 1},
	pixRGB3: {pix: capture.PixRGB24, cost: 2},
	pixBGR3: {pix: capture.PixBGR24, cost: 2},
	pixGREY: {pix: capture.PixGrey, cost: 3},
	pixMJPG: {mjpg: true, cost: 4},
	pixJPEG: {mjpg: true, cost: 4},
}

// fmtMode is one (pixel format, size) a camera can deliver and the frame-rate range it can do it at.
type fmtMode struct {
	fourcc         uint32
	w, h           int
	fpsMin, fpsMax float64
}

// devInfo is what probing a node yields.
type devInfo struct {
	card, bus, driver string
	modes             []fmtMode
}

func errno(err error) syscall.Errno {
	var e syscall.Errno
	if errors.As(err, &e) {
		return e
	}
	return 0
}

// probe reads the capabilities of an open node. It returns ok=false for nodes that are not usable single-plane
// capture devices (metadata, output, memory-to-memory, multi-planar-only, no streaming I/O, no format we convert).
func probe(dev Device) (devInfo, bool, error) {
	var cap capability
	if err := dev.Ioctl(vidiocQueryCap, unsafe.Pointer(&cap)); err != nil {
		return devInfo{}, false, fmt.Errorf("VIDIOC_QUERYCAP: %w", err)
	}
	caps := cap.Capabilities
	if caps&capDeviceCaps != 0 {
		caps = cap.DeviceCaps // the per-node capabilities: a webcam's metadata node has no VIDEO_CAPTURE here
	}
	info := devInfo{card: cstr(cap.Card[:]), bus: cstr(cap.BusInfo[:]), driver: cstr(cap.Driver[:])}
	if caps&capVideoCapture == 0 || caps&capMetaCapture != 0 || caps&capStreaming == 0 {
		return info, false, nil
	}
	modes, err := enumModes(dev)
	if err != nil {
		return info, false, err
	}
	info.modes = modes
	return info, len(modes) > 0, nil
}

// sizeLadder is used to sample continuous/stepwise size ranges.
var sizeLadder = [][2]int{{160, 120}, {176, 144}, {320, 180}, {320, 240}, {352, 288}, {424, 240}, {640, 360}, {640, 480},
	{800, 448}, {800, 600}, {960, 540}, {1024, 576}, {1280, 720}, {1280, 960}, {1600, 1200}, {1920, 1080}, {2560, 1440}, {3840, 2160}}

// enumModes lists every supported (format, size, rate range) with the ENUM ioctls.
func enumModes(dev Device) ([]fmtMode, error) {
	var out []fmtMode
	for i := uint32(0); ; i++ {
		fd := fmtDesc{Index: i, Type: bufTypeVideoCapture}
		if err := dev.Ioctl(vidiocEnumFmt, unsafe.Pointer(&fd)); err != nil {
			if errno(err) == syscall.EINVAL {
				break
			}
			if i == 0 {
				return nil, fmt.Errorf("VIDIOC_ENUM_FMT: %w", err)
			}
			break
		}
		if _, ok := supported[fd.PixelFormat]; !ok {
			continue
		}
		sizes := enumSizes(dev, fd.PixelFormat)
		for _, s := range sizes {
			lo, hi := enumRates(dev, fd.PixelFormat, s[0], s[1])
			out = append(out, fmtMode{fourcc: fd.PixelFormat, w: s[0], h: s[1], fpsMin: lo, fpsMax: hi})
		}
	}
	return out, nil
}

func enumSizes(dev Device, pix uint32) [][2]int {
	var out [][2]int
	for i := uint32(0); ; i++ {
		fs := frmSizeEnum{Index: i, PixelFormat: pix}
		if err := dev.Ioctl(vidiocEnumFrameSizes, unsafe.Pointer(&fs)); err != nil {
			break
		}
		switch fs.Type {
		case frmSizeDiscrete:
			out = append(out, [2]int{int(fs.U[0]), int(fs.U[1])})
		case frmSizeContinuous, frmSizeStepwise:
			// {min_w, max_w, step_w, min_h, max_h, step_h}: sample the usual sizes inside the range plus both ends
			minW, maxW, stepW := int(fs.U[0]), int(fs.U[1]), int(fs.U[2])
			minH, maxH, stepH := int(fs.U[3]), int(fs.U[4]), int(fs.U[5])
			stepW, stepH = max(stepW, 1), max(stepH, 1)
			ok := func(w, h int) bool {
				return w >= minW && w <= maxW && h >= minH && h <= maxH && (w-minW)%stepW == 0 && (h-minH)%stepH == 0
			}
			seen := map[[2]int]bool{}
			add := func(w, h int) {
				if ok(w, h) && !seen[[2]int{w, h}] {
					seen[[2]int{w, h}] = true
					out = append(out, [2]int{w, h})
				}
			}
			add(minW, minH)
			for _, l := range sizeLadder {
				add(l[0], l[1])
			}
			add(maxW, maxH)
			return out // a range is described by one entry
		}
	}
	return out
}

// enumRates returns the slowest and fastest frame rate for a size. A driver without ENUM_FRAMEINTERVALS is assumed
// to run 1..30 fps (the common default); that guess is later corrected by what VIDIOC_S_PARM reports.
func enumRates(dev Device, pix uint32, w, h int) (lo, hi float64) {
	lo, hi = math.Inf(1), 0
	for i := uint32(0); ; i++ {
		fi := frmIvalEnum{Index: i, PixelFormat: pix, Width: uint32(w), Height: uint32(h)}
		if err := dev.Ioctl(vidiocEnumFrameIntervs, unsafe.Pointer(&fi)); err != nil {
			break
		}
		rate := func(num, den uint32) float64 {
			if num == 0 {
				return 0
			}
			return float64(den) / float64(num)
		}
		switch fi.Type {
		case frmSizeDiscrete:
			r := rate(fi.U[0], fi.U[1])
			lo, hi = math.Min(lo, r), math.Max(hi, r)
		default: // stepwise / continuous: {min, max, step} intervals; the minimum interval is the maximum rate
			r1, r2 := rate(fi.U[0], fi.U[1]), rate(fi.U[2], fi.U[3])
			lo, hi = math.Min(lo, math.Min(r1, r2)), math.Max(hi, math.Max(r1, r2))
			return lo, hi
		}
	}
	if hi == 0 {
		return 1, 30
	}
	return lo, hi
}

// capsFromModes merges formats: a size's rate range is the widest over the formats offering it.
func capsFromModes(ms []fmtMode) capture.VideoCaps {
	type key struct{ w, h int }
	agg := map[key]*capture.Mode{}
	var order []key
	for _, m := range ms {
		k := key{m.w, m.h}
		if a, ok := agg[k]; ok {
			a.FPSMin, a.FPSMax = math.Min(a.FPSMin, m.fpsMin), math.Max(a.FPSMax, m.fpsMax)
			continue
		}
		agg[k] = &capture.Mode{W: m.w, H: m.h, FPSMin: m.fpsMin, FPSMax: m.fpsMax}
		order = append(order, k)
	}
	sort.Slice(order, func(i, j int) bool {
		if order[i].w != order[j].w {
			return order[i].w < order[j].w
		}
		return order[i].h < order[j].h
	})
	var c capture.VideoCaps
	for _, k := range order {
		c.Modes = append(c.Modes, *agg[k])
	}
	return c
}

// chooseMode picks the format and size for a request: the size closest to the request first, then among the
// formats offering that size one that reaches the requested rate, the cheapest to convert on a tie. Without a
// request it prefers 640x480 at 30 fps, the browser default.
func chooseMode(ms []fmtMode, c capture.VideoConstraints) (fmtMode, bool) {
	if len(ms) == 0 {
		return fmtMode{}, false
	}
	w, h, fps := c.Width, c.Height, c.FrameRate
	if w <= 0 && h <= 0 {
		w, h = 640, 480
	}
	if fps <= 0 {
		fps = 30
	}
	want, ok := capture.ClosestMode(capsFromModes(ms).Modes, w, h, fps)
	if !ok {
		return fmtMode{}, false
	}
	var best fmtMode
	found := false
	better := func(a, b fmtMode) bool { // is a better than b
		ar, br := a.fpsMax >= fps-0.01, b.fpsMax >= fps-0.01
		if ar != br {
			return ar
		}
		if !ar && a.fpsMax != b.fpsMax {
			return a.fpsMax > b.fpsMax
		}
		return supported[a.fourcc].cost < supported[b.fourcc].cost
	}
	for _, m := range ms {
		if m.w != want.W || m.h != want.H {
			continue
		}
		if !found || better(m, best) {
			best, found = m, true
		}
	}
	return best, found
}
