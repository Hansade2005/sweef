package app

import (
	"embed"
	"io/fs"
)

//go:embed testdata
var testdataFS embed.FS

// placeholderChrome is the tiny chrome page used until internal/ui lands (and
// in tests). It speaks the same protocol as the real one.
var placeholderChrome = mustRead(testdataFS, "testdata/chrome/index.html")

func mustRead(f embed.FS, name string) string {
	b, err := f.ReadFile(name)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// PlaceholderFS returns an fs.FS laid out like ui.FS (chrome/index.html,
// newtab/index.html) with minimal placeholder pages. Tests and previews use
// it; production passes ui.FS instead.
func PlaceholderFS() fs.FS {
	sub, err := fs.Sub(testdataFS, "testdata")
	if err != nil {
		panic(err)
	}
	return sub
}
