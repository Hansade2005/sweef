package rpc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/sweefbrowser/sweef/internal/engine"
)

// The session CLI, batch and the Node and Python SDKs read Runtime.evaluate's .result,
// Log.getConsole's .messages and Network.getLogs's .entries. The first Page.navigate must
// load the page once, not create the tab at the url and then navigate it again.
func TestNavigateOnceAndWrappedResults(t *testing.T) {
	var loads atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		loads.Add(1)
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<title>T</title><body><script>console.log("hi")</script></body>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	e := engine.New(engine.DefaultOptions())
	defer e.Close()
	s := NewServer(e)
	call := func(method string, params map[string]any) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(params)
		res, rerr := s.Call(method, raw)
		if rerr != nil {
			t.Fatalf("%s: %v", method, rerr)
		}
		var m map[string]any
		if err := json.Unmarshal(res, &m); err != nil {
			t.Fatalf("%s: want an object, got %s", method, res)
		}
		return m
	}
	call("Page.navigate", map[string]any{"url": srv.URL + "/"})
	if n := loads.Load(); n != 1 {
		t.Errorf("first navigate loaded the page %d times, want 1", n)
	}
	if r := call("Runtime.evaluate", map[string]any{"expression": "document.title"}); r["result"] != "T" {
		t.Errorf("Runtime.evaluate = %v, want {result: T}", r)
	}
	msgs, _ := call("Log.getConsole", map[string]any{"since": 0})["messages"].([]any)
	if len(msgs) != 1 {
		t.Errorf("Log.getConsole messages = %v, want one entry", msgs)
	}
	if ents, _ := call("Network.getLogs", map[string]any{"since": 0})["entries"].([]any); len(ents) == 0 {
		t.Errorf("Network.getLogs has no entries")
	}
}
