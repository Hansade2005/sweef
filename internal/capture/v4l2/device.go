//go:build linux && (amd64 || arm64)

package v4l2

import (
	"errors"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// pollRdNorm is POLLRDNORM (golang.org/x/sys/unix does not export it).
const pollRdNorm = 0x40

// PollResult is what Poll saw.
type PollResult struct {
	Ready bool // a buffer can be dequeued
	Hup   bool // the device went away (POLLERR, POLLHUP or POLLNVAL)
	Woken bool // Wake was called
}

// Device is everything the capture loop needs from the kernel. The real implementation wraps a file descriptor;
// tests substitute an in-memory device that implements the ioctl protocol, so every code path below runs without
// hardware.
type Device interface {
	// Ioctl issues a request; arg points to the request's struct (or an int for STREAMON/OFF). It returns a
	// syscall.Errno on failure. EINTR is handled inside.
	Ioctl(req uintptr, arg unsafe.Pointer) error
	// Mmap maps length bytes of the device at the offset reported by VIDIOC_QUERYBUF.
	Mmap(offset int64, length int) ([]byte, error)
	Munmap(b []byte) error
	// Poll waits up to timeout for a frame, a device error or Wake.
	Poll(timeout time.Duration) (PollResult, error)
	// Wake makes a blocked or the next Poll return with Woken set. Safe from any goroutine.
	Wake()
	Close() error
}

// fdDevice is the real /dev/videoN.
type fdDevice struct {
	fd     int
	wr, rd int // wake pipe
	mu     sync.Mutex
	closed bool
}

// OpenDevice opens a V4L2 character device non-blocking. EBUSY maps to the caller's ErrInUse handling.
func OpenDevice(path string) (Device, error) {
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	var p [2]int
	if err := unix.Pipe2(p[:], unix.O_NONBLOCK|unix.O_CLOEXEC); err != nil {
		syscall.Close(fd)
		return nil, err
	}
	return &fdDevice{fd: fd, rd: p[0], wr: p[1]}, nil
}

func (d *fdDevice) Ioctl(req uintptr, arg unsafe.Pointer) error {
	for {
		_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(d.fd), req, uintptr(arg))
		if errno == syscall.EINTR {
			continue
		}
		if errno != 0 {
			return errno
		}
		return nil
	}
}

func (d *fdDevice) Mmap(offset int64, length int) ([]byte, error) {
	return unix.Mmap(d.fd, offset, length, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
}

func (d *fdDevice) Munmap(b []byte) error { return unix.Munmap(b) }

func (d *fdDevice) Poll(timeout time.Duration) (PollResult, error) {
	fds := []unix.PollFd{
		{Fd: int32(d.fd), Events: unix.POLLIN | pollRdNorm | unix.POLLPRI},
		{Fd: int32(d.rd), Events: unix.POLLIN},
	}
	ms := int(timeout / time.Millisecond)
	if timeout > 0 && ms == 0 {
		ms = 1
	}
	n, err := unix.Poll(fds, ms)
	if err != nil {
		if errors.Is(err, syscall.EINTR) {
			return PollResult{}, nil
		}
		return PollResult{}, err
	}
	var r PollResult
	if n == 0 {
		return r, nil
	}
	if fds[1].Revents&unix.POLLIN != 0 {
		var b [64]byte
		_, _ = syscall.Read(d.rd, b[:])
		r.Woken = true
	}
	if e := fds[0].Revents; e != 0 {
		if e&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
			r.Hup = true
		}
		if e&(unix.POLLIN|pollRdNorm) != 0 {
			r.Ready = true
		}
	}
	return r, nil
}

func (d *fdDevice) Wake() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.closed {
		_, _ = syscall.Write(d.wr, []byte{1})
	}
}

func (d *fdDevice) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil
	}
	d.closed = true
	syscall.Close(d.rd)
	syscall.Close(d.wr)
	return syscall.Close(d.fd)
}
