package engine

import (
	"runtime"
	"testing"
	"time"
)

// Dedicated workers, shared workers, channels and locks all end with the document or engine that owns them.
func TestWorkersEngineCloseLeavesNoGoroutines(t *testing.T) {
	srv := newWKServer(t, map[string]string{
		"/w.js":  `onmessage = (e) => postMessage(e.data); const c = new BroadcastChannel('x'); setInterval(() => {}, 20);`,
		"/sh.js": `onconnect = (e) => { e.ports[0].onmessage = (m) => e.ports[0].postMessage(m.data); };`,
	})
	cycle := func(closeTab bool) {
		o := DefaultOptions()
		e := New(o)
		tab, err := e.NewTab(srv.URL + "/a.html")
		if err != nil {
			t.Fatal(err)
		}
		js(t, tab, `
const ws = [new Worker('/w.js'), new Worker('/w.js')];
await Promise.all(ws.map((w) => new Promise((res) => { w.onmessage = () => res(); w.postMessage(1); })));
const sw = new SharedWorker('/sh.js'); await new Promise((res) => { sw.port.onmessage = () => res(); sw.port.postMessage(1); });
const bc = new BroadcastChannel('x'); navigator.locks.request('l', () => new Promise(() => {}));
await new Promise((r) => setTimeout(r, 30));
ws[1].terminate();
return 1;`)
		if closeTab {
			e.CloseTab(tab.ID)
		}
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
	cycle(false) // warm up pools and transports
	cycle(true)
	base := settle()
	for i := 0; i < 3; i++ {
		cycle(i%2 == 0)
	}
	after := settle()
	if after > base+2 {
		buf := make([]byte, 1<<20)
		buf = buf[:runtime.Stack(buf, true)]
		t.Fatalf("goroutines grew %d -> %d over 3 engine lifetimes with workers\n%s", base, after, buf)
	}
	t.Logf("goroutines: %d before, %d after", base, after)
}
