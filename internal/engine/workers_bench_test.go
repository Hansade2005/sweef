package engine

import (
	"testing"
	"time"
)

// TestWorkerThroughput prints what a worker costs on this machine (go test -run WorkerThroughput -v ./internal/engine).
func TestWorkerThroughput(t *testing.T) {
	if testing.Short() {
		t.Skip("measurement")
	}
	srv := newWKServer(t, map[string]string{"/echo.js": `onmessage = (e) => postMessage(e.data);`})
	e := newFeatureEngine(t)
	tab := openTab(t, e, srv.URL+"/a.html")
	// warm the VM pool and the script cache
	js(t, tab, `const w = new Worker('/echo.js'); await new Promise((r) => { w.onmessage = r; w.postMessage(1); }); w.terminate(); return 1;`)

	t0 := time.Now()
	js(t, tab, `for (let i = 0; i < 10; i++) { const w = new Worker('/echo.js'); await new Promise((r) => { w.onmessage = r; w.postMessage(1); }); w.terminate(); } return 1;`)
	t.Logf("10 workers, each created, one message round trip, terminated: %v (%.1f ms each)", time.Since(t0), float64(time.Since(t0).Milliseconds())/10)

	js(t, tab, `window.w = new Worker('/echo.js'); await new Promise((r) => { window.w.onmessage = r; window.w.postMessage(1); }); return 1;`)
	t0 = time.Now()
	js(t, tab, `const w = window.w; for (let i = 0; i < 500; i++) await new Promise((r) => { w.onmessage = r; w.postMessage({ i, s: 'x'.repeat(20) }); }); return 1;`)
	d := time.Since(t0)
	t.Logf("500 object round trips to a running worker: %v (%.2f ms each)", d, float64(d.Microseconds())/500/1000)

	t0 = time.Now()
	js(t, tab, `const w = window.w; const buf = new Uint8Array(1 << 20); for (let i = 0; i < 4; i++) { const b = buf.buffer.slice(0); await new Promise((r) => { w.onmessage = r; w.postMessage(b, [b]); }); } return 1;`)
	d = time.Since(t0)
	t.Logf("4 round trips of a 1 MiB ArrayBuffer (transferred both ways): %v (%.0f ms each)", d, float64(d.Milliseconds())/4)
}
