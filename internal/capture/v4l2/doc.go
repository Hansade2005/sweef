// Package v4l2 is a pure-Go (no cgo, no libv4l) Video4Linux2 capture provider: it enumerates /dev/video*, lists
// formats, sizes and frame intervals with the VIDIOC_ENUM_* ioctls, and streams with memory-mapped buffers.
//
// The implementation is built for linux/amd64 and linux/arm64 only: the ioctl numbers and struct layouts in abi.go
// are the 64-bit kernel ABI, which is identical on those two architectures. On every other target the package is
// empty and the platform registry (internal/capture/platform) simply registers no camera.
//
// All kernel access goes through the Device interface, so the whole capture loop is tested against an in-memory
// device (see mock_test.go) without any hardware.
package v4l2
