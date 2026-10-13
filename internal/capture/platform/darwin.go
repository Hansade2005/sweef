//go:build darwin

package platform

import "github.com/sweefbrowser/sweef/internal/capture/avf"

// UNVERIFIED: the macOS backend has never run on real hardware (docs/capture.md).
func init() { register(avf.NewProvider()) }
