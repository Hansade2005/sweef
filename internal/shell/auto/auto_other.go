//go:build !linux && !windows && !darwin

package auto

import (
	"errors"

	"github.com/sweefbrowser/sweef/internal/shell"
)

// Open reports that this OS has no native shell.
func Open(cfg shell.Config) (shell.Window, error) {
	return nil, errors.New("shell: no native window implementation for this OS")
}

// Backend names the implementation in use.
const Backend = "none"
