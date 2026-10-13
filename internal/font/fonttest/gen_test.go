package fonttest

import (
	"bytes"
	"os"
	"os/exec"
	"testing"
)

// TestGenerateFixtures rewrites testdata/*.woff2 with a real brotli encoder:
//
//	BROTLIC=/path/to/brotlic go test ./internal/font/fonttest -run Generate
//
// where brotlic compresses stdin to stdout (any brotli CLI works, e.g. one
// built from github.com/andybalholm/brotli, MIT). The fixtures are committed so
// the ordinary tests need no encoder.
func TestGenerateFixtures(t *testing.T) {
	bin := os.Getenv("BROTLIC")
	if bin == "" {
		t.Skip("BROTLIC not set")
	}
	br := func(b []byte) []byte {
		cmd := exec.Command(bin)
		cmd.Stdin = bytes.NewReader(b)
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	write := func(name string, b []byte) {
		if err := os.WriteFile("testdata/"+name, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("boxes.woff2", WOFF2(Boxes("Boxy", "Regular", 400, false).SFNT(), Options{Transform: true, Brotli: br}))
	write("icons.woff2", WOFF2(Icons("Icony").SFNT(), Options{Transform: true, Brotli: br}))
}
