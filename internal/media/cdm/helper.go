package cdm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

// HelperEnv names the environment variable that overrides the helper binary location.
const HelperEnv = "SWEEF_CDM_HELPER"

// HelperName is the file name of the CDM host helper next to the browser binary.
func HelperName() string {
	if runtime.GOOS == "windows" {
		return "sweef-cdm.exe"
	}
	return "sweef-cdm"
}

// FindHelper locates the helper: $SWEEF_CDM_HELPER, else next to the running executable. The helper is a
// separate binary because it must be dynamically linked (it dlopen's the CDM) while the Linux browser binary is
// fully static.
func FindHelper() (string, error) {
	if p := os.Getenv(HelperEnv); p != "" {
		if st, err := os.Stat(p); err != nil || st.IsDir() {
			return "", fmt.Errorf("%s=%q is not a file", HelperEnv, p)
		}
		return p, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	p := filepath.Join(filepath.Dir(exe), HelperName())
	if st, err := os.Stat(p); err != nil || st.IsDir() {
		return "", fmt.Errorf("the CDM host helper %s was not found next to %s (or set %s)", HelperName(), exe, HelperEnv)
	}
	return p, nil
}

// HelperSignaturePath is where the helper's VMP signature (castLabs EVS output) is looked for: the helper path with
// ".sig" appended, as `evs-vmp sign <exe>` writes it. See docs/drm.md.
func HelperSignaturePath(helper string) string { return helper + ".sig" }

// StartHelper starts the helper process and connects a Remote to its stdin/stdout. args are passed through (for
// Widevine: -cdm <library>). The helper's stderr goes to stderr (nil: discarded).
func StartHelper(ctx context.Context, path string, args []string, env []string, stderr io.Writer, init InitParams) (*Remote, error) {
	cmd := exec.Command(path, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stderr = stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("cdm: starting %s: %w", path, err)
	}
	conn := &pipeConn{r: stdout, w: stdin}
	var once sync.Once
	reap := func() error {
		var werr error
		once.Do(func() {
			waited := make(chan error, 1)
			go func() { waited <- cmd.Wait() }()
			select {
			case werr = <-waited:
			case <-time.After(3 * time.Second):
				_ = cmd.Process.Kill()
				werr = <-waited
			}
		})
		var ee *exec.ExitError
		if errors.As(werr, &ee) {
			return nil // a killed or crashed helper is not an error of Close
		}
		return werr
	}
	rem, err := NewRemote(ctx, conn, init, reap)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = reap()
		return nil, err
	}
	return rem, nil
}

type pipeConn struct {
	r io.ReadCloser
	w io.WriteCloser
}

func (p *pipeConn) Read(b []byte) (int, error)  { return p.r.Read(b) }
func (p *pipeConn) Write(b []byte) (int, error) { return p.w.Write(b) }
func (p *pipeConn) Close() error {
	// Closing stdin tells the helper to exit; the read side is closed by Wait.
	e1 := p.w.Close()
	_ = p.r.Close()
	return e1
}
