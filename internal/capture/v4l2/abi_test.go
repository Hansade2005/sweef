//go:build linux && (amd64 || arm64)

package v4l2

import (
	"testing"
	"unsafe"
)

// The expected values are the kernel's: sizeof() of the structs in videodev2.h on a 64-bit target, and the request
// numbers as printed by `printf '%#x\n' $(( ... ))` of the _IOR/_IOW/_IOWR macros (also what strace shows).
func TestKernelABISizes(t *testing.T) {
	cases := []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"v4l2_capability", unsafe.Sizeof(capability{}), 104},
		{"v4l2_pix_format", unsafe.Sizeof(pixFormat{}), 48},
		{"v4l2_format", unsafe.Sizeof(format{}), 208},
		{"v4l2_fmtdesc", unsafe.Sizeof(fmtDesc{}), 64},
		{"v4l2_frmsizeenum", unsafe.Sizeof(frmSizeEnum{}), 44},
		{"v4l2_frmivalenum", unsafe.Sizeof(frmIvalEnum{}), 52},
		{"v4l2_fract", unsafe.Sizeof(fract{}), 8},
		{"v4l2_captureparm", unsafe.Sizeof(captureParm{}), 40},
		{"v4l2_streamparm", unsafe.Sizeof(streamParm{}), 204},
		{"v4l2_requestbuffers", unsafe.Sizeof(requestBuffers{}), 20},
		{"v4l2_timecode", unsafe.Sizeof(timecode{}), 16},
		{"v4l2_buffer", unsafe.Sizeof(buffer{}), 88},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("sizeof(%s) = %d, kernel ABI says %d", c.name, c.got, c.want)
		}
	}
}

func TestKernelABIOffsets(t *testing.T) {
	var f format
	var b buffer
	var fs frmSizeEnum
	var fi frmIvalEnum
	var sp streamParm
	var cp captureParm
	var c capability
	cases := []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"format.fmt.pix", unsafe.Offsetof(f.Pix), 8},
		{"buffer.timestamp", unsafe.Offsetof(b.Timestamp), 24},
		{"buffer.timecode", unsafe.Offsetof(b.Timecode), 40},
		{"buffer.sequence", unsafe.Offsetof(b.Sequence), 56},
		{"buffer.memory", unsafe.Offsetof(b.Memory), 60},
		{"buffer.m", unsafe.Offsetof(b.M), 64},
		{"buffer.length", unsafe.Offsetof(b.Length), 72},
		{"buffer.reserved2", unsafe.Offsetof(b.Reserved2), 76},
		{"buffer.request_fd", unsafe.Offsetof(b.RequestFD), 80},
		{"frmsizeenum.discrete", unsafe.Offsetof(fs.U), 12},
		{"frmsizeenum.reserved", unsafe.Offsetof(fs.Reserved), 36},
		{"frmivalenum.discrete", unsafe.Offsetof(fi.U), 20},
		{"frmivalenum.reserved", unsafe.Offsetof(fi.Reserved), 44},
		{"streamparm.parm.capture", unsafe.Offsetof(sp.Capture), 4},
		{"captureparm.timeperframe", unsafe.Offsetof(cp.TimePerFrame), 8},
		{"capability.card", unsafe.Offsetof(c.Card), 16},
		{"capability.bus_info", unsafe.Offsetof(c.BusInfo), 48},
		{"capability.version", unsafe.Offsetof(c.Version), 80},
		{"capability.capabilities", unsafe.Offsetof(c.Capabilities), 84},
		{"capability.device_caps", unsafe.Offsetof(c.DeviceCaps), 88},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("offsetof(%s) = %d, kernel ABI says %d", c.name, c.got, c.want)
		}
	}
	if unsafe.Sizeof(b.Timestamp) != 16 {
		t.Errorf("struct timeval is %d bytes, v4l2_buffer needs 16", unsafe.Sizeof(b.Timestamp))
	}
}

func TestIoctlNumbers(t *testing.T) {
	cases := []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"VIDIOC_QUERYCAP", vidiocQueryCap, 0x80685600},
		{"VIDIOC_ENUM_FMT", vidiocEnumFmt, 0xC0405602},
		{"VIDIOC_G_FMT", vidiocGFmt, 0xC0D05604},
		{"VIDIOC_S_FMT", vidiocSFmt, 0xC0D05605},
		{"VIDIOC_REQBUFS", vidiocReqBufs, 0xC0145608},
		{"VIDIOC_QUERYBUF", vidiocQueryBuf, 0xC0585609},
		{"VIDIOC_QBUF", vidiocQBuf, 0xC058560F},
		{"VIDIOC_DQBUF", vidiocDQBuf, 0xC0585611},
		{"VIDIOC_STREAMON", vidiocStreamOn, 0x40045612},
		{"VIDIOC_STREAMOFF", vidiocStreamOff, 0x40045613},
		{"VIDIOC_G_PARM", vidiocGParm, 0xC0CC5615},
		{"VIDIOC_S_PARM", vidiocSParm, 0xC0CC5616},
		{"VIDIOC_ENUM_FRAMESIZES", vidiocEnumFrameSizes, 0xC02C564A},
		{"VIDIOC_ENUM_FRAMEINTERVALS", vidiocEnumFrameIntervs, 0xC034564B},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %#x, want %#x", c.name, c.got, c.want)
		}
	}
}

func TestFourcc(t *testing.T) {
	// v4l2_fourcc('Y','U','Y','V') == 0x56595559 and friends, as printed in videodev2.h / v4l2-ctl
	for name, c := range map[string][2]uint32{
		"YUYV": {pixYUYV, 0x56595559}, "MJPG": {pixMJPG, 0x47504A4D}, "NV12": {pixNV12, 0x3231564E},
		"YU12": {pixYU12, 0x32315559}, "RGB3": {pixRGB3, 0x33424752}, "BGR3": {pixBGR3, 0x33524742},
	} {
		if c[0] != c[1] {
			t.Errorf("%s = %#x, want %#x", name, c[0], c[1])
		}
	}
}
