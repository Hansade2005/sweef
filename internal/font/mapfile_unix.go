//go:build unix

package font

import (
	"os"
	"syscall"
)

// readFontFile maps a system font file read-only instead of copying it into
// the Go heap: a face is kept for the life of the process, and of a font file
// (CJK and emoji fonts run to tens of MB) only the tables and glyphs actually
// used get paged in. The kernel can drop those pages again under pressure.
// The mapping is never unmapped (faces are never freed). Falls back to a read.
func readFontFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if n := st.Size(); n > 0 && n == int64(int(n)) && st.Mode().IsRegular() {
		if b, err := syscall.Mmap(int(f.Fd()), 0, int(n), syscall.PROT_READ, syscall.MAP_SHARED); err == nil {
			return b, nil
		}
	}
	return os.ReadFile(path)
}
