//go:build !(linux || darwin || windows || freebsd || netbsd || openbsd || dragonfly || solaris)

package audioout

import "errors"

func newDeviceBackend() (Backend, error) {
	return nil, errors.New("audioout: no audio device backend on this platform")
}
