//go:build linux

package platform

import "github.com/sweefbrowser/sweef/internal/capture/pulse"

func init() {
	register(pulse.NewProvider())
	registerV4L2()
}
