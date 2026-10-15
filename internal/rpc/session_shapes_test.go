package rpc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

// Page.armDialog takes action "accept" (MCP, the session CLI and the SDKs send it);
// RPC results are not HTML-escaped; waitForTimeout honours timeoutMs.
func TestArmDialogActionAndCompactJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<title>D</title><button id=b onclick="document.title = confirm('ok?') ? 'yes' : 'no'">go</button>`))
	}))
	defer srv.Close()
	e := engine.New(engine.DefaultOptions())
	defer e.Close()
	s := NewServer(e)
	call := func(method string, params map[string]any) string {
		t.Helper()
		raw, _ := json.Marshal(params)
		res, rerr := s.Call(method, raw)
		if rerr != nil {
			t.Fatalf("%s: %v", method, rerr)
		}
		return string(res)
	}
	call("Page.navigate", map[string]any{"url": srv.URL})
	call("Page.armDialog", map[string]any{"action": "accept"})
	call("Input.click", map[string]any{"selector": "#b"})
	if got := call("Page.title", nil); got != `{"title":"yes"}` {
		t.Errorf("after arming accept, title = %s, want yes", got)
	}
	call("Page.armDialog", map[string]any{"action": "dismiss"})
	call("Input.click", map[string]any{"selector": "#b"})
	if got := call("Page.title", nil); got != `{"title":"no"}` {
		t.Errorf("after arming dismiss, title = %s, want no", got)
	}
	if h := call("Page.content", nil); !strings.Contains(h, "<button") {
		t.Errorf("Page.content is HTML-escaped: %.80s", h)
	}
	t0 := time.Now()
	call("Page.waitForTimeout", map[string]any{"timeoutMs": 50})
	if d := time.Since(t0); d > 700*time.Millisecond {
		t.Errorf("waitForTimeout{timeoutMs:50} took %v (the 1 s default?)", d)
	}
}

// Input.keyboard takes text (typed into the focused element) and key (one key or a
// combination); Input.scroll takes x/y as the CLI, batch and SDKs send them.
func TestKeyboardAndScrollParams(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<title>K</title><input id=q><div id=log></div><div style="height:5000px">tall</div>
<script>document.getElementById('q').addEventListener('keydown', e => {
  if (e.ctrlKey && e.key === 'b') document.getElementById('log').textContent = 'ctrl-b'; });</script>`))
	}))
	defer srv.Close()
	e := engine.New(engine.DefaultOptions())
	defer e.Close()
	s := NewServer(e)
	call := func(method string, params map[string]any) string {
		t.Helper()
		raw, _ := json.Marshal(params)
		res, rerr := s.Call(method, raw)
		if rerr != nil {
			t.Fatalf("%s: %v", method, rerr)
		}
		return string(res)
	}
	call("Page.navigate", map[string]any{"url": srv.URL})
	call("Input.focus", map[string]any{"selector": "#q"})
	call("Input.keyboard", map[string]any{"text": "hi"})
	call("Input.keyboard", map[string]any{"key": "Control+b"})
	if got := call("Runtime.evaluate", map[string]any{"expression": "document.getElementById('q').value + '|' + document.getElementById('log').textContent"}); got != `{"result":"hi|ctrl-b"}` {
		t.Errorf("after typing hi and pressing Control+b: %s, want hi|ctrl-b", got)
	}
	call("Input.scroll", map[string]any{"x": 0, "y": 600})
	if got := call("Runtime.evaluate", map[string]any{"expression": "window.scrollY > 0"}); got != `{"result":true}` {
		t.Errorf("Input.scroll{y:600} did not scroll: %s", got)
	}
}
