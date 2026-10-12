//go:build windows

package auto

import (
	"github.com/sweefbrowser/sweef/internal/shell"
	"github.com/sweefbrowser/sweef/internal/shell/win32"
)

// Open opens a window with the Win32 shell.
func Open(cfg shell.Config) (shell.Window, error) { return win32.Open(cfg) }

// Backend names the implementation in use (for --stats and bug reports).
const Backend = "win32"
