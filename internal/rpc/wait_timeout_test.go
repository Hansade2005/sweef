package rpc

import (
	"encoding/json"
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
