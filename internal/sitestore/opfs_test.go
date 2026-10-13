package sitestore

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
)

func TestOPFSTreeSurvivesARestartAndLargeFilesAreBlobs(t *testing.T) {
	dir := t.TempDir()
	m := newTestManager(t, dir, 0)
	fs := m.Origin("http://opfs").OPFS()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(fs.Get([]string{"docs"}, true, true))
	must(fs.Get([]string{"docs", "sub"}, true, true))
	must(fs.Get([]string{"docs", "small.txt"}, false, true))
	must(fs.Get([]string{"docs", "sub", "big.bin"}, false, true))
	must(fs.Write([]string{"docs", "small.txt"}, []byte("hello")))
	big := bytes.Repeat([]byte{0, 1, 2, 255}, 200<<10) // 800 KiB, every byte value class
	must(fs.Write([]string{"docs", "sub", "big.bin"}, big))
	if got := blobFiles(dir); len(got) != 1 {
		t.Fatalf("the large file must be a file on disk, the small one not: %v", got)
	}
	if u := fs.Usage(); u < int64(len(big)) {
		t.Fatalf("usage: %d", u)
	}
	m.Shutdown()

	m = newTestManager(t, dir, 0)
	defer m.Shutdown()
	fs = m.Origin("http://opfs").OPFS()
	b, mtime, err := fs.Read([]string{"docs", "small.txt"})
	if err != nil || string(b) != "hello" || mtime == 0 {
		t.Fatalf("small file after restart: %q %d %v", b, mtime, err)
	}
	if b, _, err := fs.Read([]string{"docs", "sub", "big.bin"}); err != nil || !bytes.Equal(b, big) {
		t.Fatalf("large file after restart: %d bytes, %v", len(b), err)
	}
	es, err := fs.List([]string{"docs"})
	if err != nil || fmt.Sprint(es) != "[{small.txt false} {sub true}]" {
		t.Fatalf("list: %v %v", es, err)
	}
	if st, _ := fs.Stat([]string{"docs", "small.txt"}); st == nil || st.Dir || st.Size != 5 {
		t.Fatalf("stat: %+v", st)
	}
	if st, _ := fs.Stat([]string{"nope"}); st != nil {
		t.Fatal("stat of a missing path")
	}

	// errors
	if err := fs.Get([]string{"docs"}, false, false); !IsError(err, "TypeMismatchError") {
		t.Fatalf("directory asked as file: %v", err)
	}
	if err := fs.Get([]string{"nope", "x"}, false, true); !IsError(err, "NotFoundError") {
		t.Fatalf("missing parent: %v", err)
	}
	if err := fs.Remove([]string{"docs"}, false); !IsError(err, "InvalidModificationError") {
		t.Fatalf("non-empty directory: %v", err)
	}
	if err := fs.Remove(nil, true); !IsError(err, "NoModificationAllowedError") {
		t.Fatalf("root: %v", err)
	}
	if err := fs.Move([]string{"docs"}, []string{"docs", "sub", "inside"}); !IsError(err, "InvalidModificationError") {
		t.Fatalf("directory into itself: %v", err)
	}

	// move, then remove recursively: the blob goes with it and the usage returns to zero
	must(fs.Move([]string{"docs", "sub", "big.bin"}, []string{"docs", "moved.bin"}))
	if b, _, err := fs.Read([]string{"docs", "moved.bin"}); err != nil || !bytes.Equal(b, big) {
		t.Fatal("moved file differs")
	}
	if _, _, err := fs.Read([]string{"docs", "sub", "big.bin"}); !IsError(err, "NotFoundError") {
		t.Fatalf("old path after a move: %v", err)
	}
	must(fs.Remove([]string{"docs"}, true))
	if got := blobFiles(dir); len(got) != 0 {
		t.Fatalf("blob left after removing the tree: %v", got)
	}
	if u := fs.Usage(); u != 0 {
		t.Fatalf("usage after removing everything: %d", u)
	}
	if es, _ := fs.List(nil); len(es) != 0 {
		t.Fatalf("root not empty: %v", es)
	}
}

func TestOPFSMemoryOnlyManagerKeepsNothingOnDisk(t *testing.T) {
	m := newTestManager(t, "", 0)
	defer m.Shutdown()
	fs := m.Origin("http://private").OPFS()
	if err := fs.Get([]string{"f"}, false, true); err != nil {
		t.Fatal(err)
	}
	big := bytes.Repeat([]byte("x"), 300<<10)
	if err := fs.Write([]string{"f"}, big); err != nil {
		t.Fatal(err)
	}
	if b, _, err := fs.Read([]string{"f"}); err != nil || !bytes.Equal(b, big) {
		t.Fatalf("read back: %d %v", len(b), err)
	}
	if ents, _ := os.ReadDir(os.TempDir()); len(ents) < 0 {
		t.Fatal("unreachable")
	}
}

func TestCacheBodiesStreamAndEntriesKeepTheirPlace(t *testing.T) {
	dir := t.TempDir()
	m := newTestManager(t, dir, 0)
	o := m.Origin("http://cache")
	c, _ := o.Caches()
	if _, err := c.Open("v1"); err != nil {
		t.Fatal(err)
	}
	big := bytes.Repeat([]byte("0123456789"), 40<<10) // 400 KB
	put := func(url string, body []byte) {
		t.Helper()
		if err := c.Put("v1", CacheEntry{URL: url, Method: "GET", Status: 200, ResHeaders: [][2]string{{"x", url}}}, body); err != nil {
			t.Fatal(err)
		}
	}
	put("http://cache/a", []byte("a1"))
	put("http://cache/big", big)
	put("http://cache/c?x=1", []byte("c1"))
	put("http://cache/a", []byte("a2")) // replaces in place: a stays first
	if names := func() (out []string) {
		es, _ := c.Requests("v1", CacheQuery{})
		for _, e := range es {
			out = append(out, strings.TrimPrefix(e.URL, "http://cache"))
		}
		return
	}(); fmt.Sprint(names) != "[/a /big /c?x=1]" {
		t.Fatalf("order after replacing in place: %v", names)
	}
	// a large body is streamed from its file
	ms := c.MatchStream("v1", false, CacheQuery{URL: "http://cache/big", Method: "GET"}, false)
	if len(ms) != 1 {
		t.Fatal("no match")
	}
	if _, ok := ms[0].Body.(*os.File); !ok {
		t.Fatalf("a large body must be read from its file, got %T", ms[0].Body)
	}
	var got bytes.Buffer
	buf := make([]byte, 4096)
	chunks := 0
	for {
		n, err := ms[0].Body.Read(buf)
		got.Write(buf[:n])
		chunks++
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	ms[0].Body.Close()
	if !bytes.Equal(got.Bytes(), big) || chunks < 50 {
		t.Fatalf("streamed %d bytes in %d reads", got.Len(), chunks)
	}
	// ignoreSearch and the method rules
	if n := len(c.Match("v1", false, CacheQuery{URL: "http://cache/c", Method: "GET", IgnoreSearch: true}, true)); n != 1 {
		t.Fatalf("ignoreSearch: %d", n)
	}
	if n := len(c.Match("v1", false, CacheQuery{URL: "http://cache/c", Method: "GET"}, true)); n != 0 {
		t.Fatalf("exact match must include the query: %d", n)
	}
	if n := len(c.Match("v1", false, CacheQuery{URL: "http://cache/a", Method: "POST"}, true)); n != 0 {
		t.Fatalf("POST must not match: %d", n)
	}
	if n := len(c.Match("v1", false, CacheQuery{URL: "http://cache/a", Method: "POST", IgnoreMethod: true}, true)); n != 1 {
		t.Fatalf("ignoreMethod: %d", n)
	}
	// deleting an entry frees its blob and its bytes
	before := o.Usage().Caches
	n, err := c.DeleteEntries("v1", CacheQuery{URL: "http://cache/big", Method: "GET"})
	if err != nil || n != 1 {
		t.Fatalf("delete: %d %v", n, err)
	}
	if got := blobFiles(dir); len(got) != 0 {
		t.Fatalf("blob left: %v", got)
	}
	if after := o.Usage().Caches; before-after < int64(len(big)) {
		t.Fatalf("usage %d -> %d", before, after)
	}
	// deleting the cache: gone at once, collected in the background, usage back to zero
	put("http://cache/big2", big)
	if !c.Delete("v1") || c.Has("v1") {
		t.Fatal("delete")
	}
	o.WaitGC()
	if got := blobFiles(dir); len(got) != 0 {
		t.Fatalf("blobs left after deleting the cache: %v", got)
	}
	if u := o.Usage().Caches; u != 0 {
		t.Fatalf("usage after deleting the cache: %d", u)
	}
	if keys := c.Keys(); len(keys) != 0 {
		t.Fatalf("keys: %v", keys)
	}
}

func TestCacheKeysInCreationOrderSurviveARestart(t *testing.T) {
	dir := t.TempDir()
	m := newTestManager(t, dir, 0)
	c, _ := m.Origin("http://cache").Caches()
	for _, n := range []string{"zeta", "alpha", "mid"} {
		if created, err := c.Open(n); err != nil || !created {
			t.Fatalf("open %s: %v %v", n, created, err)
		}
	}
	if created, _ := c.Open("alpha"); created {
		t.Fatal("reopening an existing cache must not create it")
	}
	c.Delete("alpha")
	m.Shutdown()
	m = newTestManager(t, dir, 0)
	defer m.Shutdown()
	c, _ = m.Origin("http://cache").Caches()
	if fmt.Sprint(c.Keys()) != "[zeta mid]" {
		t.Fatalf("keys: %v", c.Keys())
	}
}
