//go:build !linux && !freebsd && !openbsd && !netbsd && !dragonfly && !windows && !darwin

package notify

// Native has no notification service on this platform.
func Native(appName string) (Sink, error) { return nil, ErrUnavailable }
