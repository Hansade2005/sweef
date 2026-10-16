package engine

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// Clicking a submit button and pressing Enter in a field submit the form through a cancelable submit event, once.
// Pages that handle the submit themselves (React/Vue logins, TodoMVC) cancel it; the engine must not then post
// the form anyway (saucedemo went out as a GET and reloaded; TodoMVC reloaded on Enter).
func TestSubmitGoesThroughTheSubmitEvent(t *testing.T) {
	var gets atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			gets.Add(1)
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><body><form id="f" action="/"><input id="q" name="q"><button id="go">Go</button></form>
<p id="out"></p><script>
var n = 0;
document.getElementById('f').addEventListener('submit', function (e) { e.preventDefault(); n++; document.getElementById('out').textContent = 'submitted ' + n + ':' + document.getElementById('q').value; });
</script>`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	tab := newTabForLoad(t)
	if err := tab.Navigate(srv.URL+"/", nil); err != nil {
		t.Fatal(err)
	}
	if err := tab.Fill("#q", "milk"); err != nil {
		t.Fatal(err)
	}
	if err := tab.Press("#q", "Enter"); err != nil {
		t.Fatal(err)
	}
	if v := mustEval(t, tab, `document.getElementById('out').textContent`); v != `"submitted 1:milk"` {
		t.Fatalf("after Enter: %s", v)
	}
	if err := tab.Click("#go"); err != nil {
		t.Fatal(err)
	}
	tab.Settle(50 * time.Millisecond)
	if v := mustEval(t, tab, `document.getElementById('out').textContent`); v != `"submitted 2:milk"` {
		t.Fatalf("after click: %s", v)
	}
	if n := gets.Load(); n != 0 {
		t.Fatalf("the cancelled form was still sent %d time(s)", n)
	}
}
