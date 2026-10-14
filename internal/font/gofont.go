package font

import (
	_ "embed"
	"sync"

	"github.com/sweefbrowser/sweef/internal/zpack"
)

// The eight Go fonts (golang.org/x/image/font/gofont, 1.3 MB as Go byte slices) are the last-resort faces. They are
// embedded deflated in gofont.zar (internal/zpack archive, about half the size) and inflated when a face is first
// used, which on a machine with system fonts is usually never. Regenerate with
//
//	go generate ./internal/font
//
// TestGoFontArchive checks the archive against the x/image packages byte for byte.
//
//go:generate go run ../../tools/zpack gofont -o gofont.zar
//go:embed gofont.zar
var goFontZ string

var (
	goFontOnce sync.Once
	goFontArc  *zpack.Archive
)

// goFont returns a fresh copy of the named Go font's TTF bytes, or nil.
func goFont(name string) []byte {
	goFontOnce.Do(func() { goFontArc, _ = zpack.Open(goFontZ) })
	if goFontArc == nil {
		return nil
	}
	b, _ := goFontArc.Read(name)
	return b
}
