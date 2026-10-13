//go:build linux && !(amd64 || arm64)

package platform

// registerV4L2: the V4L2 struct layouts in internal/capture/v4l2 are the 64-bit ABI of amd64 and arm64 only.
func registerV4L2() {}
