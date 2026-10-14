//go:build !(linux || freebsd || netbsd || openbsd || dragonfly || solaris || darwin || windows)

package audioout

import "errors"

func newDeviceBackend() (Backend, error) {
	return nil, errors.New("audioout: no audio device backend on this platform")
}
