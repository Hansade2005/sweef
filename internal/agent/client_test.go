package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newTestClient(t *testing.T, f *fakeAPI) *Client {
	t.Helper()
	c, err := NewClient(f.cfg())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func TestSSEReader(t *testing.T) {
	in := ": comment\r\nevent: a\r\ndata: {\"x\":1}\r\n\r\nevent: b\ndata: l1\ndata: l2\n\n\nevent: ping\n\ndata: only-data\n\nevent: tail\ndata: no-trailing-blank"
	r := newSSEReader(strings.NewReader(in))
	type ev struct{ n, d string }
	var got []ev
	for {
		n, d, err := r.next()
		if err != nil {
			break
		}
		got = append(got, ev{string(n), string(d)})
	}
	want := []ev{{"a", `{"x":1}`}, {"b", "l1\nl2"}, {"ping", ""}, {"", "only-data"}, {"tail", "no-trailing-blank"}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestSSEReaderLongLine(t *testing.T) {
	big := strings.Repeat("x", 300_000)
	r := newSSEReader(strings.NewReader("event: e\ndata: " + big + "\n\n"))
	n, d, err := r.next()
	if err != nil || string(n) != "e" || len(d) != len(big) {
		t.Fatalf("err=%v n=%q len=%d", err, n, len(d))
	}
}

func TestParseDelta(t *testing.T) {
	cases := []struct {
		data string
		idx  int
		kind deltaKind
		val  string
		ok   bool
	}{
		{`{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"Hello"}}`, 2, deltaText, "Hello", true},
		{`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"a\"b\\c\nd é 😀 /\/"}}`, 0, deltaText, "a\"b\\c\nd é \U0001F600 //", true},
		{`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"steps\":[{\"op\":\"click\""}}`, 1, deltaInputJSON, `{"steps":[{"op":"click"`, true},
		{`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hmm"}}`, 0, deltaThinking, "hmm", true},
		{`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"EqQB"}}`, 0, deltaSignature, "EqQB", true},
		{`{"type":"content_block_delta","index":3,"delta":{"type":"citations_delta","citation":{"a":1}}}`, 0, deltaOther, "", false}, // object value -> slow path
		{`{"type":"content_block_delta","index":4,"delta":{"type":"future_delta","x":"y"}}`, 4, deltaOther, "", true},
		{`{"type":"content_block_delta","delta":{"type":"text_delta","text":"x"}}`, 0, deltaOther, "", false},
	}
	var scratch []byte
	for i, c := range cases {
		idx, kind, val, ok := parseDelta([]byte(c.data), &scratch)
		if ok != c.ok || (ok && (idx != c.idx || kind != c.kind || string(val) != c.val)) {
			t.Errorf("case %d: got idx=%d kind=%d val=%q ok=%v", i, idx, kind, val, ok)
		}
	}
}

func TestParseDeltaMatchesEncodingJSON(t *testing.T) {
	// Property check: whatever text we send through the fast path must round-trip.
	var scratch []byte
	texts := []string{"plain", "tab\tnewline\nquote\"slash\\", "emoji \U0001F600 and é and 日本語", "</script><b>&amp;", "\x01\x1f control", strings.Repeat("ab\"", 50)}
	for _, s := range texts {
		b, _ := json.Marshal(s)
		data := fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%s}}`, b)
		_, kind, val, ok := parseDelta([]byte(data), &scratch)
		if !ok || kind != deltaText || string(val) != s {
			t.Errorf("%q -> %q ok=%v", s, val, ok)
		}
	}
}

func TestStreamAssemblesMessage(t *testing.T) {
	f := newFakeAPI(t, func(n int, s *sw, body []byte) {
		s.std()
		s.thinking(0, "plan", "sig==")
		s.text(1, "Hel", "lo ", "wor", "ld")
		s.toolJSON(2, "toolu_1", "act", `{"steps":[{"op":"click","ref":"e1"}]}`)
		s.raw("some_future_event", `{"type":"some_future_event","extra":{"deep":[1,2,3]}}`)
		s.end("tool_use", 77)
	})
	c := newTestClient(t, f)
	var texts []string
	var stops []string
	msg, m, err := c.Stream(context.Background(), []byte(`{}`), &Handler{
		OnText:      func(b []byte) { texts = append(texts, string(b)) },
		OnBlockStop: func(b *Block) { stops = append(stops, b.Type) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(texts, "|") != "Hel|lo |wor|ld" {
		t.Fatalf("text deltas %q", texts)
	}
	if strings.Join(stops, ",") != "thinking,text,tool_use" || len(msg.Blocks) != 3 {
		t.Fatalf("stops %v", stops)
	}
	tb := msg.Blocks[2]
	if !tb.InputOK || tb.ID != "toolu_1" || string(tb.Input) != `{"steps":[{"op":"click","ref":"e1"}]}` {
		t.Fatalf("tool block %+v", tb)
	}
	if string(msg.Blocks[0].Raw) != `{"type":"thinking","thinking":"plan","signature":"sig=="}` {
		t.Fatalf("thinking raw %s", msg.Blocks[0].Raw)
	}
	if msg.StopReason != "tool_use" || !msg.Complete || msg.Usage.OutputTokens != 77 || msg.Usage.CacheCreationTokens != 7000 {
		t.Fatalf("msg %+v", msg)
	}
	if m.FirstByte <= 0 || m.FirstToken <= 0 || m.FirstToolCall < m.FirstToken || m.Status != 200 || m.RequestID == "" {
		t.Fatalf("metrics %+v", m)
	}
	h := f.headers[0]
	if h.Get("x-api-key") == "" || h.Get("anthropic-version") != "2023-06-01" || h.Get("content-type") != "application/json" {
		t.Fatalf("headers %v", h)
	}
	if cs := c.CacheStats(); cs.Requests != 1 || cs.CacheCreation != 7000 || cs.HitRate() != 0 {
		t.Fatalf("cache stats %+v", cs)
	}
}

func TestTruncatedToolInputIsFlagged(t *testing.T) {
	f := newFakeAPI(t, func(n int, s *sw, body []byte) {
		s.std()
		s.tool(0, "toolu_x", "act", `{"steps":[{"op":"cli`) // cut off by max_tokens
		s.tool(1, "toolu_y", "done")                        // no input deltas at all
		s.end("max_tokens", 4096)
	})
	c := newTestClient(t, f)
	msg, _, err := c.Stream(context.Background(), []byte(`{}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Blocks[0].InputOK || string(msg.Blocks[0].Input) != "{}" {
		t.Fatalf("truncated input must be flagged and history-safe: %+v", msg.Blocks[0])
	}
	if !msg.Blocks[1].InputOK {
		t.Fatalf("empty input is a valid empty object")
	}
	if !json.Valid(msg.Blocks[0].Raw) {
		t.Fatal("raw block must stay valid JSON")
	}
}

func TestUnknownFieldsAndEventsTolerated(t *testing.T) {
	f := newFakeAPI(t, func(n int, s *sw, body []byte) {
		s.raw("message_start", `{"type":"message_start","message":{"id":"m","model":"x","usage":{"input_tokens":5,"output_tokens":1,"new_field":{"a":1},"service_tier":"standard"},"brand_new":true}}`)
		s.raw("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"","citations":null},"extra":1}`)
		s.raw("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"citations_delta","citation":{"type":"x"}}}`)
		s.raw("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok","extra":"x"}}`)
		s.raw("content_block_delta", `{"type":"content_block_delta","index":9,"delta":{"type":"text_delta","text":"orphan"}}`)
		s.raw("totally_new", `not even json`)
		s.blockStop(0)
		s.raw("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_details":null,"novel":1},"usage":{"output_tokens":3,"server_tool_use":{"web_search_requests":0}}}`)
		s.raw("message_stop", `{"type":"message_stop"}`)
	})
	c := newTestClient(t, f)
	msg, _, err := c.Stream(context.Background(), []byte(`{}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.Blocks) != 1 || msg.Blocks[0].Text != "ok" || msg.StopReason != "end_turn" || msg.Usage.OutputTokens != 3 || msg.Usage.InputTokens != 5 {
		t.Fatalf("%+v", msg)
	}
}

func TestRetryOn429HonoursRetryAfter(t *testing.T) {
	var hits atomic.Int32
	f := newFakeAPI(t, nil)
	f.handler = func(n int, s *sw, body []byte) {
		if hits.Add(1) <= 2 {
			s.w.Header().Set("retry-after", "2")
			s.w.WriteHeader(429)
			fmt.Fprint(s.w, `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`)
			return
		}
		s.std()
		s.text(0, "fine")
		s.end("end_turn", 1)
	}
	cfg := f.cfg()
	var waits []time.Duration
	cfg.Sleep = func(ctx context.Context, d time.Duration) error { waits = append(waits, d); return nil }
	c, _ := NewClient(cfg)
	defer c.Close()
	var retries int
	msg, m, err := c.Stream(context.Background(), []byte(`{}`), &Handler{OnRetry: func(int, time.Duration, error) { retries++ }})
	if err != nil {
		t.Fatal(err)
	}
	if m.Attempts != 3 || len(waits) != 2 || retries != 2 || msg.Blocks[0].Text != "fine" {
		t.Fatalf("attempts=%d waits=%v retries=%d", m.Attempts, waits, retries)
	}
	for _, w := range waits {
		if w < 2*time.Second {
			t.Fatalf("retry-after not honoured: %v", waits)
		}
	}
}

func TestRetryOverloadedAndServerErrors(t *testing.T) {
	for _, code := range []int{500, 502, 503, 529} {
		var hits atomic.Int32
		f := newFakeAPI(t, nil)
		code := code
		f.handler = func(n int, s *sw, body []byte) {
			if hits.Add(1) == 1 {
				s.w.WriteHeader(code)
				fmt.Fprint(s.w, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
				return
			}
			s.std()
			s.text(0, "x")
			s.end("end_turn", 1)
		}
		c := newTestClient(t, f)
		_, m, err := c.Stream(context.Background(), []byte(`{}`), nil)
		if err != nil || m.Attempts != 2 {
			t.Fatalf("code %d: err=%v attempts=%d", code, err, m.Attempts)
		}
	}
}

func TestNoRetryOnClientErrorAndNoRetryExhaustion(t *testing.T) {
	var hits atomic.Int32
	f := newFakeAPI(t, func(n int, s *sw, body []byte) {
		hits.Add(1)
		s.w.WriteHeader(400)
		fmt.Fprint(s.w, `{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`)
	})
	c := newTestClient(t, f)
	_, m, err := c.Stream(context.Background(), []byte(`{}`), nil)
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 400 || ae.Type != "invalid_request_error" || m.Attempts != 1 || ae.Retryable() {
		t.Fatalf("err=%v attempts=%d", err, m.Attempts)
	}
	// persistent 529: stops after MaxRetries+1 attempts
	hits.Store(0)
	f2 := newFakeAPI(t, func(n int, s *sw, body []byte) {
		hits.Add(1)
		s.w.WriteHeader(529)
		fmt.Fprint(s.w, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
	})
	c2 := newTestClient(t, f2)
	_, m2, err := c2.Stream(context.Background(), []byte(`{}`), nil)
	if err == nil || m2.Attempts != 5 || hits.Load() != 5 {
		t.Fatalf("err=%v attempts=%d hits=%d", err, m2.Attempts, hits.Load())
	}
	// retry-after beyond RetryMaxWait is not waited out
	f3 := newFakeAPI(t, func(n int, s *sw, body []byte) {
		s.w.Header().Set("retry-after", "3600")
		s.w.WriteHeader(429)
		fmt.Fprint(s.w, `{"type":"error","error":{"type":"rate_limit_error","message":"daily"}}`)
	})
	c3 := newTestClient(t, f3)
	_, m3, err := c3.Stream(context.Background(), []byte(`{}`), nil)
	if err == nil || m3.Attempts != 1 {
		t.Fatalf("expected immediate failure, attempts=%d err=%v", m3.Attempts, err)
	}
}

func TestSSEErrorEventBeforeContentRetriesAfterContentDoesNot(t *testing.T) {
	var hits atomic.Int32
	f := newFakeAPI(t, func(n int, s *sw, body []byte) {
		s.std()
		if hits.Add(1) == 1 {
			s.raw("error", `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
			return
		}
		s.text(0, "second try")
		s.end("end_turn", 1)
	})
	c := newTestClient(t, f)
	msg, m, err := c.Stream(context.Background(), []byte(`{}`), nil)
	if err != nil || m.Attempts != 2 || msg.Blocks[0].Text != "second try" {
		t.Fatalf("err=%v attempts=%d", err, m.Attempts)
	}

	hits.Store(0)
	f2 := newFakeAPI(t, func(n int, s *sw, body []byte) {
		s.std()
		hits.Add(1)
		s.text(0, "partial ")
		s.raw("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`)
		s.raw("error", `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
	})
	c2 := newTestClient(t, f2)
	msg2, m2, err := c2.Stream(context.Background(), []byte(`{}`), nil)
	var ae *APIError
	if !errors.As(err, &ae) || m2.Attempts != 1 || hits.Load() != 1 {
		t.Fatalf("mid-stream failure must not be retried: err=%v attempts=%d", err, m2.Attempts)
	}
	if len(msg2.Blocks) != 1 || msg2.Blocks[0].Text != "partial " {
		t.Fatalf("partial message should expose completed blocks: %+v", msg2)
	}
}

func TestRefusalAndMaxTokensStopReasons(t *testing.T) {
	for _, stop := range []string{"refusal", "max_tokens", "pause_turn"} {
		stop := stop
		f := newFakeAPI(t, func(n int, s *sw, body []byte) {
			s.std()
			s.text(0, "x")
			s.raw("message_delta", fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":%q,"stop_details":{"type":"refusal","category":"cyber","explanation":"e"}},"usage":{"output_tokens":4}}`, stop))
			s.raw("message_stop", `{"type":"message_stop"}`)
		})
		c := newTestClient(t, f)
		msg, _, err := c.Stream(context.Background(), []byte(`{}`), nil)
		if err != nil || msg.StopReason != stop || msg.StopCategory != "cyber" {
			t.Fatalf("%s: %v %+v", stop, err, msg)
		}
	}
}

func TestStreamEndsWithoutMessageStop(t *testing.T) {
	f := newFakeAPI(t, func(n int, s *sw, body []byte) {
		s.std()
		s.text(0, "cut")
	})
	cfg := f.cfg()
	cfg.MaxRetries = -1
	c, _ := NewClient(cfg)
	defer c.Close()
	msg, _, err := c.Stream(context.Background(), []byte(`{}`), nil)
	if err == nil || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("want unexpected EOF, got %v", err)
	}
	if len(msg.Blocks) != 1 {
		t.Fatalf("completed block should survive: %+v", msg)
	}
}

func goroutinesSettle(base int) int {
	var n int
	for i := 0; i < 100; i++ {
		n = runtime.NumGoroutine()
		if n <= base {
			return n
		}
		time.Sleep(20 * time.Millisecond)
	}
	return n
}

func TestCancelAbortsStreamImmediately(t *testing.T) {
	http.DefaultTransport.(*http.Transport).CloseIdleConnections()
	base := runtime.NumGoroutine()
	release := make(chan struct{})
	f := newFakeAPI(t, func(n int, s *sw, body []byte) {
		s.std()
		s.text(0, "first token")
		s.raw("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`)
		<-release // stall the stream; never finishes on its own
	})
	c := newTestClient(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan struct{}, 1)
	var once sync.Once
	done := make(chan error, 1)
	go func() {
		_, _, err := c.Stream(ctx, []byte(`{}`), &Handler{OnText: func([]byte) { once.Do(func() { got <- struct{}{} }) }})
		done <- err
	}()
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("no token")
	}
	t0 := time.Now()
	cancel()
	select {
	case err := <-done:
		lat := time.Since(t0)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("want context.Canceled, got %v", err)
		}
		if lat > 250*time.Millisecond {
			t.Fatalf("cancel took %v", lat)
		}
		t.Logf("cancel latency %v", lat)
	case <-time.After(3 * time.Second):
		t.Fatal("Stream did not return after cancel")
	}
	close(release)
	c.Close()
	f.srv.Close()
	if n := goroutinesSettle(base + 2); n > base+2 {
		buf := make([]byte, 1<<16)
		t.Fatalf("goroutine leak: %d > %d\n%s", n, base+2, buf[:runtime.Stack(buf, true)])
	}
}

func TestAPIKeyNeverInErrors(t *testing.T) {
	key := "sk-ant-test-key-0123456789"
	f := newFakeAPI(t, func(n int, s *sw, body []byte) {
		s.w.WriteHeader(401)
		fmt.Fprintf(s.w, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key %s"}}`, key)
	})
	c := newTestClient(t, f)
	_, _, err := c.Stream(context.Background(), []byte(`{}`), nil)
	if err == nil || strings.Contains(err.Error(), key) || !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("key leaked or not redacted: %v", err)
	}
	// connection errors
	cfg := f.cfg()
	cfg.BaseURL = "http://127.0.0.1:1"
	cfg.MaxRetries = -1
	c2, _ := NewClient(cfg)
	_, _, err = c2.Stream(context.Background(), []byte(`{}`), nil)
	if err == nil || strings.Contains(err.Error(), key) {
		t.Fatalf("err=%v", err)
	}
	// formatting a Config must not print the key
	for _, verb := range []string{"%v", "%+v", "%s"} {
		if s := fmt.Sprintf(verb, f.cfg()); strings.Contains(s, key) {
			t.Fatalf("Config printed with %s leaks the key", verb)
		}
	}
	b, _ := json.Marshal(f.cfg())
	if strings.Contains(string(b), key) {
		t.Fatal("Config JSON leaks the key")
	}
	if _, err := NewClient(Config{}); err == nil {
		t.Fatal("empty key must be rejected")
	}
}

func TestPrewarmSendsNoKeyAndReusesConnection(t *testing.T) {
	f := newFakeAPI(t, func(n int, s *sw, body []byte) {
		s.std()
		s.text(0, "x")
		s.end("end_turn", 1)
	})
	c := newTestClient(t, f)
	d, err := c.Prewarm(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("prewarm %v", d)
	if len(f.headers) != 1 || f.headers[0].Get("x-api-key") != "" {
		t.Fatalf("prewarm must be unauthenticated: %v", f.headers)
	}
	_, m, err := c.Stream(context.Background(), []byte(`{}`), nil)
	if err != nil || !m.ReusedConn {
		t.Fatalf("first real request should reuse the prewarmed connection: err=%v reused=%v", err, m != nil && m.ReusedConn)
	}
}

func TestCostAndHitRate(t *testing.T) {
	cs := CacheStats{InputTokens: 100, CacheRead: 900, CacheCreation: 0}
	if r := cs.HitRate(); r < 0.899 || r > 0.901 {
		t.Fatalf("hit rate %v", r)
	}
	if (CacheStats{}).HitRate() != 0 {
		t.Fatal("empty")
	}
	// 1M uncached input + 1M output below the 100K cliff per-request is 0.10+0.50 per MTok: use small numbers
	got := CostUSD(Usage{InputTokens: 50_000, OutputTokens: 10_000, CacheReadTokens: 40_000})
	want := (50_000*0.10 + 40_000*0.01 + 10_000*0.50) / 1e6
	if d := got - want; d > 1e-12 || d < -1e-12 {
		t.Fatalf("cost %v want %v", got, want)
	}
	big := CostUSD(Usage{InputTokens: 150_000})
	if d := big - 150_000*0.50/1e6; d > 1e-12 || d < -1e-12 {
		t.Fatalf("over-100K tier: %v", big)
	}
}
