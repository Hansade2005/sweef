package ui_test

import (
	"bytes"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"testing/fstest"

	"github.com/sweefbrowser/sweef/internal/ui"
)

// TestArchiveIsCurrent: the embedded deflated archive serves exactly the files below web/. Fails with the fix when
// someone edits a UI asset and forgets `go generate ./internal/ui`.
func TestArchiveIsCurrent(t *testing.T) {
	disk := os.DirFS("web")
	var want []string
	err := fs.WalkDir(disk, ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			want = append(want, p)
		}
		return err
	})
	if err != nil || len(want) < 40 {
		t.Fatalf("walk web/: %v (%d files)", err, len(want))
	}
	for _, p := range want {
		a, err := fs.ReadFile(disk, p)
		if err != nil {
			t.Fatal(err)
		}
		b, err := fs.ReadFile(ui.FS, p)
		if err != nil {
			t.Errorf("%s is missing from the archive: run `go generate ./internal/ui`", p)
			continue
		}
		if !bytes.Equal(a, b) {
			t.Errorf("%s differs from the archive: run `go generate ./internal/ui`", p)
		}
	}
	var got []string
	_ = fs.WalkDir(ui.FS, ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			got = append(got, p)
		}
		return err
	})
	if len(got) != len(want) {
		t.Errorf("archive has %d files, web/ has %d: run `go generate ./internal/ui`", len(got), len(want))
	}
}

// TestArchiveFSBehavesLikeADirectory: the same questions answered the same way as by the file system it replaces
// (embed.FS over web/): listings, stat, errors, io.Seeker and net/http.
func TestArchiveFSBehavesLikeADirectory(t *testing.T) {
	disk := os.DirFS("web")
	if err := fstest.TestFS(ui.FS, "chrome/index.html", "shared/base.css", "newtab/index.html"); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{".", "chrome", "shared"} {
		a, err1 := fs.ReadDir(disk, d)
		b, err2 := fs.ReadDir(ui.FS, d)
		if err1 != nil || err2 != nil || len(a) != len(b) {
			t.Fatalf("ReadDir(%s): %v %v, %d vs %d entries", d, err1, err2, len(a), len(b))
		}
		for i := range a {
			ai, _ := a[i].Info()
			bi, _ := b[i].Info()
			if a[i].Name() != b[i].Name() || a[i].IsDir() != b[i].IsDir() || (!a[i].IsDir() && ai.Size() != bi.Size()) {
				t.Errorf("ReadDir(%s)[%d]: %s/%v/%d vs %s/%v/%d", d, i, a[i].Name(), a[i].IsDir(), ai.Size(), b[i].Name(), b[i].IsDir(), bi.Size())
			}
		}
	}
	for _, p := range []string{"nope.html", "chrome/nope.js", "../etc/passwd", "/chrome/index.html", "chrome/"} {
		_, e1 := fs.ReadFile(disk, p)
		_, e2 := fs.ReadFile(ui.FS, p)
		if (e1 == nil) != (e2 == nil) {
			t.Errorf("ReadFile(%q): disk err %v, archive err %v", p, e1, e2)
		}
	}
	if _, err := fs.ReadFile(ui.FS, "chrome"); err == nil {
		t.Error("reading a directory as a file succeeded")
	}
	srv := httptest.NewServer(http.FileServer(http.FS(ui.FS)))
	defer srv.Close()
	for _, p := range []string{"/chrome/index.html", "/shared/base.css"} {
		want, _ := fs.ReadFile(disk, p[1:])
		res, err := http.Get(srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != 200 || !bytes.Equal(got, want) {
			t.Errorf("GET %s: %d, %d bytes (want %d)", p, res.StatusCode, len(got), len(want))
		}
	}
	req, _ := http.NewRequest("GET", srv.URL+"/shared/base.css", nil)
	req.Header.Set("Range", "bytes=2-9")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	part, _ := io.ReadAll(res.Body)
	res.Body.Close()
	full, _ := fs.ReadFile(disk, "shared/base.css")
	if res.StatusCode != 206 || !bytes.Equal(part, full[2:10]) {
		t.Errorf("range request: %d %q", res.StatusCode, part)
	}
}
