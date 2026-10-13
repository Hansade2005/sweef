//go:build linux && (amd64 || arm64)

package v4l2

import (
	"syscall"
	"unsafe"
)

// Kernel ABI, from include/uapi/linux/videodev2.h and asm-generic/ioctl.h (64-bit layout). Struct sizes are checked
// at compile time against the values the kernel's headers give; abi_test.go also checks field offsets and the
// resulting ioctl request numbers against literals.

// capability is struct v4l2_capability.
type capability struct {
	Driver       [16]byte
	Card         [32]byte
	BusInfo      [32]byte
	Version      uint32
	Capabilities uint32
	DeviceCaps   uint32
	Reserved     [3]uint32
}

// pixFormat is struct v4l2_pix_format.
type pixFormat struct {
	Width        uint32
	Height       uint32
	PixelFormat  uint32
	Field        uint32
	BytesPerLine uint32
	SizeImage    uint32
	ColorSpace   uint32
	Priv         uint32
	Flags        uint32
	YCbCrEnc     uint32 // union with hsv_enc
	Quantization uint32
	XferFunc     uint32
}

// format is struct v4l2_format: a type and a 200-byte union that is 8-byte aligned on 64-bit because one of its
// members (struct v4l2_window) holds pointers; hence the explicit pad.
type format struct {
	Type uint32
	_    uint32
	Pix  pixFormat
	_    [200 - unsafe.Sizeof(pixFormat{})]byte
}

// fmtDesc is struct v4l2_fmtdesc.
type fmtDesc struct {
	Index       uint32
	Type        uint32
	Flags       uint32
	Description [32]byte
	PixelFormat uint32
	MbusCode    uint32
	Reserved    [3]uint32
}

// frmSizeEnum is struct v4l2_frmsizeenum; U holds either {width, height} (discrete) or
// {min_width, max_width, step_width, min_height, max_height, step_height} (stepwise/continuous).
type frmSizeEnum struct {
	Index       uint32
	PixelFormat uint32
	Type        uint32
	U           [6]uint32
	Reserved    [2]uint32
}

// frmIvalEnum is struct v4l2_frmivalenum; U holds {numerator, denominator} (discrete) or
// {min, max, step} fractions (stepwise/continuous).
type frmIvalEnum struct {
	Index       uint32
	PixelFormat uint32
	Width       uint32
	Height      uint32
	Type        uint32
	U           [6]uint32
	Reserved    [2]uint32
}

// fract is struct v4l2_fract.
type fract struct{ Numerator, Denominator uint32 }

// captureParm is struct v4l2_captureparm.
type captureParm struct {
	Capability   uint32
	CaptureMode  uint32
	TimePerFrame fract
	ExtendedMode uint32
	ReadBuffers  uint32
	Reserved     [4]uint32
}

// streamParm is struct v4l2_streamparm (only the capture member of the union is used).
type streamParm struct {
	Type    uint32
	Capture captureParm
	_       [200 - unsafe.Sizeof(captureParm{})]byte
}

// requestBuffers is struct v4l2_requestbuffers.
type requestBuffers struct {
	Count        uint32
	Type         uint32
	Memory       uint32
	Capabilities uint32
	Flags        uint8
	Reserved     [3]uint8
}

// timecode is struct v4l2_timecode.
type timecode struct {
	Type     uint32
	Flags    uint32
	Frames   uint8
	Seconds  uint8
	Minutes  uint8
	Hours    uint8
	Userbits [4]uint8
}

// buffer is struct v4l2_buffer. M is the union {offset, userptr, planes, fd}; for V4L2_MEMORY_MMAP its low 32 bits
// are the mmap offset. Go inserts the pad before Timestamp (8-byte aligned) the same way the C compiler does.
type buffer struct {
	Index     uint32
	Type      uint32
	BytesUsed uint32
	Flags     uint32
	Field     uint32
	Timestamp syscall.Timeval
	Timecode  timecode
	Sequence  uint32
	Memory    uint32
	M         uint64
	Length    uint32
	Reserved2 uint32
	RequestFD int32 // union with reserved
}

// Compile-time size checks: a wrong layout is a build error, not a corrupted ioctl.
var (
	_ [104 - unsafe.Sizeof(capability{})]byte
	_ [unsafe.Sizeof(capability{}) - 104]byte
	_ [48 - unsafe.Sizeof(pixFormat{})]byte
	_ [unsafe.Sizeof(pixFormat{}) - 48]byte
	_ [208 - unsafe.Sizeof(format{})]byte
	_ [unsafe.Sizeof(format{}) - 208]byte
	_ [64 - unsafe.Sizeof(fmtDesc{})]byte
	_ [unsafe.Sizeof(fmtDesc{}) - 64]byte
	_ [44 - unsafe.Sizeof(frmSizeEnum{})]byte
	_ [unsafe.Sizeof(frmSizeEnum{}) - 44]byte
	_ [52 - unsafe.Sizeof(frmIvalEnum{})]byte
	_ [unsafe.Sizeof(frmIvalEnum{}) - 52]byte
	_ [204 - unsafe.Sizeof(streamParm{})]byte
	_ [unsafe.Sizeof(streamParm{}) - 204]byte
	_ [20 - unsafe.Sizeof(requestBuffers{})]byte
	_ [unsafe.Sizeof(requestBuffers{}) - 20]byte
	_ [88 - unsafe.Sizeof(buffer{})]byte
	_ [unsafe.Sizeof(buffer{}) - 88]byte
)

// ioctl request encoding (asm-generic/ioctl.h, used by amd64 and arm64): dir(2) | size(14) | type(8) | nr(8).
const (
	iocNone  = 0
	iocWrite = 1
	iocRead  = 2
)

func ioc(dir, nr, size uintptr) uintptr { return dir<<30 | size<<16 | 'V'<<8 | nr }

// ioctl requests.
var (
	vidiocQueryCap         = ioc(iocRead, 0, unsafe.Sizeof(capability{}))
	vidiocEnumFmt          = ioc(iocRead|iocWrite, 2, unsafe.Sizeof(fmtDesc{}))
	vidiocGFmt             = ioc(iocRead|iocWrite, 4, unsafe.Sizeof(format{}))
	vidiocSFmt             = ioc(iocRead|iocWrite, 5, unsafe.Sizeof(format{}))
	vidiocReqBufs          = ioc(iocRead|iocWrite, 8, unsafe.Sizeof(requestBuffers{}))
	vidiocQueryBuf         = ioc(iocRead|iocWrite, 9, unsafe.Sizeof(buffer{}))
	vidiocQBuf             = ioc(iocRead|iocWrite, 15, unsafe.Sizeof(buffer{}))
	vidiocDQBuf            = ioc(iocRead|iocWrite, 17, unsafe.Sizeof(buffer{}))
	vidiocStreamOn         = ioc(iocWrite, 18, 4) // int
	vidiocStreamOff        = ioc(iocWrite, 19, 4) // int
	vidiocGParm            = ioc(iocRead|iocWrite, 21, unsafe.Sizeof(streamParm{}))
	vidiocSParm            = ioc(iocRead|iocWrite, 22, unsafe.Sizeof(streamParm{}))
	vidiocEnumFrameSizes   = ioc(iocRead|iocWrite, 74, unsafe.Sizeof(frmSizeEnum{}))
	vidiocEnumFrameIntervs = ioc(iocRead|iocWrite, 75, unsafe.Sizeof(frmIvalEnum{}))
)

// Constants.
const (
	bufTypeVideoCapture = 1
	memoryMMap          = 1
	fieldAny            = 0

	capVideoCapture      = 0x00000001
	capVideoCaptureMPlan = 0x00001000
	capMetaCapture       = 0x00800000
	capStreaming         = 0x04000000
	capDeviceCaps        = 0x80000000

	bufFlagError             = 0x00000040
	bufFlagTimestampMask     = 0x0000e000
	bufFlagTimestampMonotone = 0x00002000

	frmSizeDiscrete   = 1
	frmSizeContinuous = 2
	frmSizeStepwise   = 3
)

func fourcc(a, b, c, d byte) uint32 {
	return uint32(a) | uint32(b)<<8 | uint32(c)<<16 | uint32(d)<<24
}

var (
	pixYUYV = fourcc('Y', 'U', 'Y', 'V')
	pixUYVY = fourcc('U', 'Y', 'V', 'Y')
	pixNV12 = fourcc('N', 'V', '1', '2')
	pixNV21 = fourcc('N', 'V', '2', '1')
	pixYU12 = fourcc('Y', 'U', '1', '2')
	pixYV12 = fourcc('Y', 'V', '1', '2')
	pixMJPG = fourcc('M', 'J', 'P', 'G')
	pixJPEG = fourcc('J', 'P', 'E', 'G')
	pixRGB3 = fourcc('R', 'G', 'B', '3')
	pixBGR3 = fourcc('B', 'G', 'R', '3')
	pixGREY = fourcc('G', 'R', 'E', 'Y')
)

func cstr(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}
