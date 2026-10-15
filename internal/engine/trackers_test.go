package engine

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// With BlockTrackers a third-party script from a listed host fails like a blocked request (its error event
// fires, it never runs); same-site requests and an engine without the option are unaffected.
func TestTrackerBlocking(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><body><p id="out">-</p><script>window.st = [];</script>
<script src="https://www.google-analytics.com/analytics.js" onerror="st.push('ga-error')" onload="st.push('ga-load')"></script>
<script src="/local.js"></script></body>`)
	}))
	defer srv.Close()
	for _, block := range []bool{true, false} {
		e := New(Options{CacheDir: "off", BlockTrackers: block})
		tab, _ := e.NewTab("about:blank")
		_ = tab.AddRoute(&RouteRule{Pattern: "https://www.google-analytics.com/**", Action: "fulfill", Status: 200, ContentType: "application/javascript", Body: "window.ga = 1;"})
		_ = tab.AddRoute(&RouteRule{Pattern: "**/local.js", Action: "fulfill", Status: 200, ContentType: "application/javascript", Body: "window.local = 1;"})
		if err := tab.Navigate(srv.URL+"/", nil); err != nil {
			t.Fatal(err)
		}
		tab.Settle(50 * time.Millisecond)
		got := mustEval(t, tab, `st.join(',') + '|' + (window.ga || 0) + '|' + (window.local || 0)`)
		want := `"ga-load|1|1"`
		if block {
			want = `"ga-error|0|1"`
		}
		if got != want {
			t.Errorf("block=%v: got %s want %s", block, got, want)
		}
		e.Close()
	}
}
