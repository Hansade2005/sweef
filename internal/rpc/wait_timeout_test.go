package rpc

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/engine"
)

// Waits honour the caller's "timeoutMs" (the CLI, the batch runner and the SDKs send it); they ran for the
// 30 s default instead and the CLI gave up first with a socket i/o timeout.
func TestWaitHonoursTimeoutMs(t *testing.T) {
	e := engine.New(engine.DefaultOptions())
	defer e.Close()
	s := NewServer(e)
	if _, err := s.Call("Page.navigate", json.RawMessage(`{"url":"data:text/html,<p>hello</p>"}`)); err != nil {
		t.Fatalf("Page.navigate: %v", err)
	}
	t0 := time.Now()
	_, err := s.Call("Page.waitForSelector", json.RawMessage(`{"selector":"text=never there","state":"visible","timeoutMs":300}`))
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if d := time.Since(t0); d > 3*time.Second {
		t.Fatalf("waited %v for a 300 ms timeout", d)
	}
}

// A timed wait runs the page meanwhile: a timer due during the wait has fired when it returns.
func TestWaitForTimeoutRunsThePage(t *testing.T) {
	e := engine.New(engine.DefaultOptions())
	defer e.Close()
	s := NewServer(e)
	if _, err := s.Call("Page.navigate", json.RawMessage(`{"url":"data:text/html,<p id=p>a</p><script>setTimeout(function(){document.getElementById('p').textContent='b'},700)</script>"}`)); err != nil {
		t.Fatalf("Page.navigate: %v", err)
	}
	t0 := time.Now()
	if _, err := s.Call("Page.waitForTimeout", json.RawMessage(`{"ms":1200}`)); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(t0); d < time.Second || d > 5*time.Second {
		t.Fatalf("waited %v for 1200 ms", d)
	}
	raw, err := s.Call("Runtime.evaluate", json.RawMessage(`{"expression":"document.getElementById('p').textContent"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"b"`) {
		t.Fatalf("timer did not run during the wait: %s", raw)
	}
}

// Between calls the session daemon keeps the page running for a while: a timer the page set fires without the
// client calling anything.
func TestBackgroundPumpRunsPagesBetweenCalls(t *testing.T) {
	e := engine.New(engine.DefaultOptions())
	defer e.Close()
	s := NewServer(e)
	s.BackgroundPump = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.backgroundPump(ctx)
	if _, err := s.Call("Page.navigate", json.RawMessage(`{"url":"data:text/html,<p id=p>a</p><script>setTimeout(function(){window.firedAt=Date.now()},300)</script>"}`)); err != nil {
		t.Fatalf("Page.navigate: %v", err)
	}
	time.Sleep(1500 * time.Millisecond) // no calls meanwhile
	tabs := e.ListTabs()
	if len(tabs) == 0 {
		t.Fatal("no tab")
	}
	raw, err := tabs[0].Evaluate("window.firedAt ? Date.now() - window.firedAt > 600 : false") // fired well before this call
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `true` {
		t.Fatalf("the timer did not run between calls: %s", raw)
	}
}
