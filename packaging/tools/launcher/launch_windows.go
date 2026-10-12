//go:build windows

package main

import (
	"os"
	"os/exec"
	"syscall"
	"unsafe"
)

const sweefName = "sweef.exe"

const createNoWindow = 0x08000000

func run(target string, argv []string) error {
	cmd := exec.Command(target, argv[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	cmd.Env = os.Environ()
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release() // do not wait: the browser outlives the launcher
}

// reportError shows a message box, since a windowsgui program has no console.
func reportError(err error) {
	user32 := syscall.NewLazyDLL("user32.dll")
	mb := user32.NewProc("MessageBoxW")
	text, _ := syscall.UTF16PtrFromString("Sweef could not start: " + err.Error())
	title, _ := syscall.UTF16PtrFromString("Sweef")
	mb.Call(0, uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(title)), 0x10)
}
