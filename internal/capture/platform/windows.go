//go:build windows && (amd64 || arm64)

package platform

import "github.com/sweefbrowser/sweef/internal/capture/wincap"

// UNVERIFIED: the Windows backend has never run on real hardware (docs/capture.md).
func init() { register(wincap.NewProvider()) }
