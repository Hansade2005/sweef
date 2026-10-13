package sitestore

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestCacheStoreReplaceCompactAndReload(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, 0)
	o := m.Origin("http://a")
	c, err := o.Caches()
	if err != nil {
		t.Fatal(err)
	}
	if created, _ := c.Open("v1"); !created {
		t.Fatal("not created")
	}
	for i := 0; i < 600; i++ { // the same request again and again: one live entry, hundreds of dead records
		e := CacheEntry{URL: "http://a/x#frag", Method: "GET", Status: 200, ResHeaders: [][2]string{{"content-type", "text/plain"}}}
		if err := c.Put("v1", e, []byte(fmt.Sprintf("body %d", i))); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Put("v1", CacheEntry{URL: "http://a/y", Method: "GET", Status: 200}, []byte("y")); err != nil {
		t.Fatal(err)
	}
	if err := c.Put("nope", CacheEntry{URL: "http://a/z", Method: "GET"}, nil); !IsError(err, "NotFoundError") {
		t.Fatalf("put into a missing cache: %v", err)
	}
	bodies, _ := filepath.Glob(filepath.Join(dir, "*", "caches", "bodies", "*"))
	if len(bodies) != 2 {
		t.Fatalf("superseded bodies were not removed: %d files", len(bodies))
	}
	logs, _ := filepath.Glob(filepath.Join(dir, "*", "caches", "index.log"))
	if len(logs) != 1 {
		t.Fatalf("index logs: %v", logs)
	}
	fi, _ := os.Stat(logs[0])
	if fi.Size() > 20_000 {
		t.Fatalf("index log not compacted: %d bytes", fi.Size())
	}
	m.Close()

	m2 := NewManager(dir, 0)
	c2, _ := m2.Origin("http://a").Caches()
	got := c2.Match("v1", false, CacheQuery{URL: "http://a/x", Method: "GET"}, false)
	if len(got) != 1 || string(got[0].Body) != "body 599" || got[0].Entry.Status != 200 || got[0].Entry.ResHeaders[0][1] != "text/plain" {
		t.Fatalf("after reload: %+v", got)
	}
	if n := len(c2.Match("", true, CacheQuery{URL: "http://a/y", Method: "GET"}, true)); n != 1 {
		t.Fatalf("match across caches: %d", n)
	}
	if got := c2.Match("v1", false, CacheQuery{URL: "http://a/x", Method: "POST"}, false); len(got) != 0 {
		t.Fatal("POST must not match")
	}
	if got := c2.Match("v1", false, CacheQuery{URL: "http://a/x?q=1", Method: "GET", IgnoreSearch: true}, false); len(got) != 1 {
		t.Fatal("ignoreSearch")
	}
	if !c2.Delete("v1") {
		t.Fatal("delete")
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*", "caches", "bodies", "*")); len(left) != 0 {
		t.Fatalf("bodies left: %v", left)
	}
	if u := m2.Origin("http://a").Usage(); u.Caches != 0 {
		t.Fatalf("usage after delete: %+v", u)
	}
}
