//go:build darwin

package auto

import (
	"github.com/sweefbrowser/sweef/internal/shell"
	"github.com/sweefbrowser/sweef/internal/shell/cocoa"
)

// Open opens a window with the Cocoa shell. It must be called from inside
// mainthread.Run.
func Open(cfg shell.Config) (shell.Window, error) { return cocoa.Open(cfg) }

// Backend names the implementation in use (for --stats and bug reports).
const Backend = "cocoa"
