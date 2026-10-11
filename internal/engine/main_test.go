package engine

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMain(m *testing.M) {
	dir, _ := os.MkdirTemp("", "sweef-test-cache-")
	os.Setenv("SWEEF_CACHE_DIR", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func TestCodeCachePersistsAcrossEngines(t *testing.T) {
	dir := t.TempDir()
	srv := testServer(t)
	o := DefaultOptions()
	o.CacheDir = dir
	e1 := New(o)
	tab, _ := e1.NewTab(srv.URL + "/react")
	if y, _ := tab.AriaSnapshotYAML(); y == "" {
		t.Fatal("no snapshot")
	}
	e1.Close()
	files, _ := filepath.Glob(filepath.Join(dir, "code-*", "*.qjsbc"))
	if len(files) < 2 { // react.js and react-dom.js
		t.Fatalf("expected bytecode files on disk, got %v", files)
	}
	e2 := New(o)
	defer e2.Close()
	key := codeKey(mustRead(t, "testdata/vendor/react-dom.js"))
	if e2.code.get(key) == nil {
		t.Fatal("second engine did not load react-dom bytecode from disk")
	}
	tab2, _ := e2.NewTab(srv.URL + "/react")
	if err := tab2.Click("#inc"); err != nil {
		t.Fatal(err)
	}
	if txt, _ := tab2.Text("h1"); txt != "Count: 1" {
		t.Fatalf("react from cached bytecode: %q\n%s", txt, consoleText(tab2))
	}
}

func mustRead(t *testing.T, p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
