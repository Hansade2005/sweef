//go:build !windows

package main

import (
	"io"
	"os"
	"syscall"
)

// protocolStreams returns the protocol input (stdin) and output. Output is a duplicate of the original stdout; file
// descriptor 1 is then pointed at stderr so that whatever a CDM library writes to stdout cannot corrupt the frames.
func protocolStreams() (io.Reader, io.Writer, error) {
	fd, err := syscall.Dup(1)
	if err != nil {
		return nil, nil, err
	}
	syscall.CloseOnExec(fd)
	if err := redirectStdoutToStderr(); err != nil {
		return nil, nil, err
	}
	return os.Stdin, os.NewFile(uintptr(fd), "sweef-cdm protocol"), nil
}
