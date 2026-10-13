//go:build linux || freebsd || openbsd || netbsd || dragonfly

package notify

// Native opens the platform's notification service: org.freedesktop.Notifications on the session bus.
func Native(appName string) (Sink, error) {
	f, err := OpenFreedesktop(appName)
	if err != nil {
		return nil, err
	}
	return f, nil
}
