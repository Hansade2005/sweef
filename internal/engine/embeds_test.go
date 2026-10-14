package engine

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sweefbrowser/sweef/internal/zpack"
)

// TestEmbeddedJSIsCurrent: every JavaScript source has a deflated twin that decodes to exactly the source, and no
// twin outlives its source. Fails with the fix when someone edits a .js file and forgets `go generate`.
func TestEmbeddedJSIsCurrent(t *testing.T) {
	for _, pat := range []string{"*.js", "platformjs/*.js"} {
		srcs, err := filepath.Glob(pat)
		if err != nil || len(srcs) == 0 {
			t.Fatalf("glob %s: %v (%d files)", pat, err, len(srcs))
		}
		for _, src := range srcs {
			want, err := os.ReadFile(src)
			if err != nil {
				t.Fatal(err)
			}
			z, err := os.ReadFile(src + ".z")
			if err != nil {
				t.Errorf("%s has no %s.z: run `go generate ./internal/engine`", src, src)
				continue
			}
			if got := zpack.Inflate(string(z)); !bytes.Equal(got, want) {
				t.Errorf("%s.z is stale: run `go generate ./internal/engine`", src)
			}
		}
	}
	zs, _ := filepath.Glob("*.js.z")
	pz, _ := filepath.Glob("platformjs/*.js.z")
	for _, z := range append(zs, pz...) {
		if _, err := os.Stat(strings.TrimSuffix(z, ".z")); err != nil {
			t.Errorf("%s has no source: delete it", z)
		}
	}
}

// TestPreludeKeyIgnoresInflation: the bytecode cache key is computed from the embedded deflated forms, so a warm
// start never inflates the 1.2 MB of source. It must still change when any prelude changes and be stable otherwise.
func TestPreludeKeyIgnoresInflation(t *testing.T) {
	a, b := preludeKey(), preludeKey()
	if a != b {
		t.Fatal("preludeKey is not stable")
	}
	registerPrelude("zz_key_test", "var x = 1;")
	defer func() { extMu.Lock(); delete(preludeExtras, "zz_key_test"); extMu.Unlock() }()
	if preludeKey() == a {
		t.Fatal("preludeKey ignores a registered prelude")
	}
	if !strings.Contains(fullPrelude(), "var x = 1;") {
		t.Fatal("fullPrelude lacks a registered raw prelude")
	}
}
