//go:build windows

package main

import (
	"io"
	"os"

	"golang.org/x/sys/windows"
)

// UNVERIFIED: compiled for Windows but never run there.

// protocolStreams returns stdin and a duplicate of the original stdout handle, and points the process' standard
// output handle at stderr so a CDM's console output cannot corrupt the frames.
func protocolStreams() (io.Reader, io.Writer, error) {
	proc := windows.CurrentProcess()
	var dup windows.Handle
	if err := windows.DuplicateHandle(proc, windows.Handle(os.Stdout.Fd()), proc, &dup, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
		return nil, nil, err
	}
	if err := windows.SetStdHandle(windows.STD_OUTPUT_HANDLE, windows.Handle(os.Stderr.Fd())); err != nil {
		return nil, nil, err
	}
	return os.Stdin, os.NewFile(uintptr(dup), "sweef-cdm protocol"), nil
}
