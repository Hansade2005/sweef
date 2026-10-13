//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// xserver is one Xvfb instance. The harness starts a fresh one per measured run (same flags every time): 1920x1080x24,
// no compositor, no window manager, no auth, no TCP.
type xserver struct {
	display string
	cmd     *exec.Cmd
	logf    *os.File
}

func startXvfb(logDir string) (*xserver, error) {
	path, err := exec.LookPath("Xvfb")
	if err != nil {
		return nil, fmt.Errorf("Xvfb not installed")
	}
	for n := 120; n < 220; n++ {
		sock := fmt.Sprintf("/tmp/.X11-unix/X%d", n)
		lock := fmt.Sprintf("/tmp/.X%d-lock", n)
		if exists(sock) || exists(lock) {
			continue
		}
		lf, _ := os.Create(filepath.Join(logDir, fmt.Sprintf("xvfb-%d.log", n)))
		cmd := exec.Command(path, fmt.Sprintf(":%d", n), "-screen", "0", "1920x1080x24", "-ac", "-nolisten", "tcp", "-noreset")
		cmd.Stdout, cmd.Stderr = lf, lf
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		x := &xserver{display: fmt.Sprintf(":%d", n), cmd: cmd, logf: lf}
		ok := false
		for i := 0; i < 200; i++ {
			if exists(sock) {
				if c, err := dialX(x.display); err == nil {
					c.close()
					ok = true
					break
				}
			}
			time.Sleep(25 * time.Millisecond)
		}
		if !ok {
			x.stop()
			return nil, fmt.Errorf("Xvfb %s did not come up", x.display)
		}
		return x, nil
	}
	return nil, fmt.Errorf("no free X display")
}

func (x *xserver) stop() {
	if x.cmd.Process != nil {
		syscall.Kill(-x.cmd.Process.Pid, syscall.SIGKILL)
		x.cmd.Wait()
	}
	if x.logf != nil {
		x.logf.Close()
	}
	var n int
	fmt.Sscanf(x.display, ":%d", &n)
	os.Remove(fmt.Sprintf("/tmp/.X11-unix/X%d", n))
	os.Remove(fmt.Sprintf("/tmp/.X%d-lock", n))
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }
