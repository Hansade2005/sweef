package engine

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A worker whose script is slow and then throws holds its own lock while it pushes the error to the page's
// event queue; the page's event loop asks whether workers have work while holding the queue lock. Taking the
// two in opposite orders hung the navigation forever (bbc.com/news did exactly this).
func TestWorkerStartupErrorDoesNotDeadlockThePageLoop(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<html><body><p id=t>x</p><script>
var ws=[];for (var i=0;i<6;i++){ ws.push(new Worker('/w.js')); }
setTimeout(function(){ document.getElementById('t').textContent='done'; }, 50);
</script></body></html>`)
		case "/w.js":
			w.Header().Set("Content-Type", "text/javascript")
			fmt.Fprint(w, `var s=0; for (var i=0;i<3000000;i++){ s+=i; } throw new Error("worker boom "+s);`)
		}
	}))
	defer srv.Close()
	e := newDSEngine(t)
	done := make(chan *Tab, 1)
	go func() {
		tab, _ := e.NewTab(srv.URL + "/")
		if tab != nil {
			tab.Settle(500 * time.Millisecond)
		}
		done <- tab
	}()
	select {
	case tab := <-done:
		if txt, _ := tab.Text("#t"); txt != "done" {
			t.Fatalf("page text %q", txt)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("navigation hangs: worker start vs hasLiveWork lock order")
	}
}

// The lock order itself, deterministically: a worker busy in its script holds its VM lock and then pushes an
// event; the page loop polling for live work must not wait for that VM lock while holding the queue lock.
func TestHasLiveWorkDoesNotWaitForABusyWorker(t *testing.T) {
	w := &workerVM{}
	r := &realtime{workers: map[int64]*workerVM{1: w}}
	j := &jsVM{realtime: r}
	w.mu.Lock() // the worker is evaluating its script
	go func() {
		time.Sleep(100 * time.Millisecond)
		r.push(asyncEvent{kind: "worker", id: 1, typ: "error"}) // the script threw
		w.mu.Unlock()
	}()
	done := make(chan struct{})
	go func() {
		j.hasLiveWork()
		time.Sleep(200 * time.Millisecond)
		j.hasLiveWork()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("hasLiveWork deadlocked against a worker that pushes while holding its VM lock")
	}
	if len(r.take()) != 1 {
		t.Fatal("the worker's event was lost")
	}
}
