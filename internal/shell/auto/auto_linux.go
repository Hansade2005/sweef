//go:build linux

package auto

import (
	"github.com/sweefbrowser/sweef/internal/shell"
	"github.com/sweefbrowser/sweef/internal/shell/x11"
)

// Open opens a window with the X11 shell ($DISPLAY).
func Open(cfg shell.Config) (shell.Window, error) { return x11.Open(cfg) }

// Backend names the implementation in use (for --stats and bug reports).
const Backend = "x11"
