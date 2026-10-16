//go:build !unix

package font

import "os"

// readFontFile reads a system font file (mapfile_unix.go maps it instead).
func readFontFile(path string) ([]byte, error) { return os.ReadFile(path) }
