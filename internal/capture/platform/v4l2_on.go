//go:build linux && (amd64 || arm64)

package platform

import "github.com/sweefbrowser/sweef/internal/capture/v4l2"

func registerV4L2() { register(v4l2.NewProvider()) }
