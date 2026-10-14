package zpack

import (
	"bytes"
	"math/rand"
	"strings"
	"testing"
	"testing/fstest"
)

func sample() map[string][]byte {
	rnd := rand.New(rand.NewSource(1))
	noise := make([]byte, 3000)
	rnd.Read(noise)
	return map[string][]byte{
		"a.txt":         []byte(strings.Repeat("hello zpack ", 500)),
		"dir/b.js":      []byte("function f(){return 1}\n" + strings.Repeat("var x = f();\n", 100)),
		"dir/sub/c.bin": noise, // incompressible: stored
		"dir/sub/d.bin": noise, // identical to c.bin: one blob
		"dir/empty":     {},
		"top":           []byte("x"),
	}
}

func TestBlobRoundTrip(t *testing.T) {
	for name, b := range sample() {
		z := Deflate(b)
		if got := Inflate(string(z)); !bytes.Equal(got, b) {
			t.Errorf("%s: round trip differs", name)
		}
	}
	var l Lazy
	z := string(Deflate([]byte("lazy text")))
	if l.Get(z) != "lazy text" || l.Get(z) != "lazy text" {
		t.Error("Lazy.Get")
	}
}

func TestArchiveRoundTrip(t *testing.T) {
	files := sample()
	z := Build(files)
	a, err := Open(string(z))
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Names()) != len(files) {
		t.Fatalf("%d names, want %d", len(a.Names()), len(files))
	}
	for n, want := range files {
		got, ok := a.Read(n)
		if !ok || !bytes.Equal(got, want) {
			t.Errorf("%s: read back %d bytes, ok=%v", n, len(got), ok)
		}
		if sz, _ := a.Size(n); sz != len(want) {
			t.Errorf("%s: Size %d, want %d", n, sz, len(want))
		}
	}
	if _, ok := a.Read("missing"); ok {
		t.Error("read a missing entry")
	}
	// Entries with identical contents share a blob: c.bin and d.bin cost the bytes of one.
	one := Build(map[string][]byte{"dir/sub/c.bin": files["dir/sub/c.bin"]})
	two := Build(map[string][]byte{"dir/sub/c.bin": files["dir/sub/c.bin"], "dir/sub/d.bin": files["dir/sub/d.bin"]})
	if len(two)-len(one) > 20 {
		t.Errorf("a duplicate entry cost %d bytes", len(two)-len(one))
	}
	// Reads return copies: scribbling on one must not change the next.
	b1, _ := a.Read("top")
	b1[0] = 'y'
	if b2, _ := a.Read("top"); string(b2) != "x" {
		t.Error("Read returned shared memory")
	}
}

func TestArchiveRejectsGarbage(t *testing.T) {
	good := string(Build(sample()))
	for _, bad := range []string{"", "ZAR", "ZAR2" + good[4:], good[:len(good)/2], good[:10]} {
		if _, err := Open(bad); err == nil {
			t.Errorf("Open accepted %d garbage bytes", len(bad))
		}
	}
}

func TestFS(t *testing.T) {
	fsys := &FS{Z: string(Build(sample()))}
	if err := fstest.TestFS(fsys, "a.txt", "dir/b.js", "dir/sub/c.bin", "dir/empty", "top"); err != nil {
		t.Fatal(err)
	}
	b, err := fsys.ReadFile("dir/b.js")
	if err != nil || !strings.HasPrefix(string(b), "function f()") {
		t.Fatalf("ReadFile: %v", err)
	}
	if _, err := fsys.ReadFile("dir"); err == nil {
		t.Error("ReadFile of a directory succeeded")
	}
	if _, err := (&FS{Z: "junk"}).Open("a"); err == nil {
		t.Error("Open on a broken archive succeeded")
	}
}
