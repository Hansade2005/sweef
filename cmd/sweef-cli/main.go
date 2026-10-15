// Command sweef-cli is the session client of sweef without the browser engine linked in.
//
// Every sweef command is a new process, and the sweef binary initializes the whole engine before main (some
// 190 packages: codecs, WebAssembly, QuickJS, fonts) even when the command only forwards to the session
// daemon. sweef-cli serves the session commands (open, snapshot, click, eval ... see sweef help) with the
// same code (internal/cliclient) and starts the same daemon, the sweef binary next to it, but starts in about
// half the time. Any other command is handed to that sweef binary.
//
// The daemon binary is $SWEEF_BIN when set, else sweef(.exe) in sweef-cli's directory, else sweef on PATH.
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/sweefbrowser/sweef/internal/cliclient"
)

func sweefBinary() (string, error) {
	if p := os.Getenv("SWEEF_BIN"); p != "" {
		return p, nil
	}
	name := "sweef"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if self, err := os.Executable(); err == nil {
		if p := filepath.Join(filepath.Dir(self), name); fileExists(p) {
			return p, nil
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	return "", errors.New("sweef-cli: the sweef binary was not found next to sweef-cli, on PATH or in $SWEEF_BIN")
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func main() {
	cliclient.DaemonExe = sweefBinary
	cmd, args := cliclient.LeadingGlobalFlags(os.Args[1:])
	if cliclient.IsSessionCommand(cmd, args) {
		cliclient.Run(cmd, args)
		return
	}
	bin, err := sweefBinary()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	c := exec.Command(bin, os.Args[1:]...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := c.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			os.Exit(ee.ExitCode())
		}
		fmt.Fprintln(os.Stderr, "sweef-cli:", err)
		os.Exit(1)
	}
}
