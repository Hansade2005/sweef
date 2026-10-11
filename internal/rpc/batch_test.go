package rpc

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sweefbrowser/sweef/internal/engine"
)

func TestBatchRunFlowObserveAndStop(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, `<html><head><title>Login</title></head><body><form action="/welcome"><label>Email <input name="email"></label><label><input type="checkbox" name="remember"> Remember me</label><button>Sign in</button></form></body></html>`)
	})
	mux.HandleFunc("/welcome", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, `<html><head><title>Welcome</title></head><body><h1>Hello `+r.URL.Query().Get("email")+`</h1></body></html>`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	e := engine.New(engine.DefaultOptions())
	defer e.Close()
	s := NewServer(e)

	call := func(params map[string]any) map[string]any {
		raw, _ := json.Marshal(params)
		res, rerr := s.Call("Batch.run", raw)
		if rerr != nil {
			t.Fatalf("Batch.run: %v", rerr)
		}
		var out map[string]any
		json.Unmarshal(res, &out)
		return out
	}

	out := call(map[string]any{
		"steps": []map[string]any{
			{"cmd": "open", "args": []string{srv.URL + "/"}},
			{"cmd": "fill", "args": []string{"label=Email", "ada@example.com"}},
			{"cmd": "check", "args": []string{"label=Remember me"}},
			{"cmd": "click", "args": []string{"text=Sign in"}},
			{"cmd": "expect", "args": []string{"title", "Welcome"}},
			{"cmd": "get", "args": []string{"text", "h1"}},
			{"method": "Page.url", "params": map[string]any{}},
		},
		"observe": "interactive",
	})
	if out["ok"] != true || out["ran"].(float64) != 7 {
		t.Fatalf("batch flow failed: %+v", out)
	}
	steps := out["steps"].([]any)
	h1 := steps[5].(map[string]any)["result"].(map[string]any)["text"]
	if h1 != "Hello ada@example.com" {
		t.Fatalf("get text result: %v", h1)
	}
	if !strings.Contains(out["url"].(string), "remember=on") {
		t.Fatalf("checkbox not submitted: %v", out["url"])
	}
	if obs, _ := out["observation"].(string); !strings.Contains(obs, "heading \"Hello ada@example.com\"") {
		t.Fatalf("observation: %q", obs)
	}

	// stop at the first failure, report it, keep going only for optional steps
	out = call(map[string]any{
		"steps": []map[string]any{
			{"cmd": "expect", "args": []string{"text", "does not exist"}, "optional": true},
			{"cmd": "click", "args": []string{"#missing"}},
			{"cmd": "get", "args": []string{"title"}},
		},
		"observe": "none",
	})
	if out["ok"] != false || out["ran"].(float64) != 2 || out["failed"].(float64) != 2 {
		t.Fatalf("stopOnError semantics: %+v", out)
	}
	if _, has := out["observation"]; has {
		t.Fatal("observe=none should not include an observation")
	}
}

func TestBatchDeltaObservation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, `<html><body><button id="b">0</button><script>let n=0;document.getElementById('b').onclick=e=>{e.target.textContent=String(++n)}</script></body></html>`)
	}))
	defer srv.Close()
	e := engine.New(engine.DefaultOptions())
	defer e.Close()
	s := NewServer(e)
	run := func(steps []map[string]any) string {
		raw, _ := json.Marshal(map[string]any{"steps": steps, "observe": "delta"})
		res, rerr := s.Call("Batch.run", raw)
		if rerr != nil {
			t.Fatal(rerr)
		}
		var out map[string]any
		json.Unmarshal(res, &out)
		o, _ := out["observation"].(string)
		return o
	}
	first := run([]map[string]any{{"cmd": "open", "args": []string{srv.URL}}})
	if !strings.Contains(first, `button "0"`) {
		t.Fatalf("first observation should be the full tree: %q", first)
	}
	second := run([]map[string]any{{"cmd": "click", "args": []string{"#b"}}, {"cmd": "click", "args": []string{"#b"}}})
	if !strings.Contains(second, `- - button "0"`) || !strings.Contains(second, `+ - button "2"`) {
		t.Fatalf("delta observation: %q", second)
	}
}
