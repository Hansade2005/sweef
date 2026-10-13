package engine

import (
	"runtime"
	"testing"
	"time"
)

func TestServiceWorkerEngineCloseLeavesNoGoroutines(t *testing.T) {
	srv := newSWServer(t, basicSW)
	srv.setExtra("/asset.js", "x")
	cycle := func() {
		o := DefaultOptions()
		o.StorageDir = t.TempDir()
		e := New(o)
		tab, err := e.NewTab(srv.URL + "/")
		if err != nil {
			t.Fatal(err)
		}
		js(t, tab, swHelpers+`await regReady('/sw.js'); await fetch('/api/hello'); await new Promise((res) => { const q = indexedDB.open('x', 1); q.onsuccess = () => res(q.result); }); return 1;`)
		e.Close()
	}
	settle := func() int {
		var n int
		for i := 0; i < 40; i++ {
			runtime.GC()
			n = runtime.NumGoroutine()
			time.Sleep(25 * time.Millisecond)
		}
		return n
	}
	cycle() // warm up pools and transports
	base := settle()
	for i := 0; i < 3; i++ {
		cycle()
	}
	after := settle()
	if after > base+2 {
		buf := make([]byte, 1<<20)
		buf = buf[:runtime.Stack(buf, true)]
		t.Fatalf("goroutines grew %d -> %d over 3 engine lifetimes with a service worker\n%s", base, after, buf)
	}
	t.Logf("goroutines: %d before, %d after", base, after)
}
