package engine

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A worker whose script is slow and then throws reports the error to the page's event queue while the page's
// event loop runs. Taking the worker's and the queue's locks in opposite orders hung the navigation forever
// (bbc.com/news did exactly this); workers now run on their own goroutine and only post to the queue.
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
