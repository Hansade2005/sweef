package agentbridge

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A fake Anthropic Messages API (SSE), adapted from internal/agent/fake_test.go.

type sw struct {
	w       http.ResponseWriter
	f       http.Flusher
	r       *http.Request
	started bool
	n       int
}

func (s *sw) begin() {
	if !s.started {
		s.w.Header().Set("content-type", "text/event-stream")
		s.w.Header().Set("request-id", fmt.Sprintf("req_fake_%d", s.n))
		s.w.WriteHeader(200)
		s.started = true
	}
}

func (s *sw) raw(name, data string) {
	s.begin()
	fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", name, data)
	s.f.Flush()
}

func jstr(v string) string { b, _ := json.Marshal(v); return string(b) }

func (s *sw) start(in, cc, cr int) {
	s.raw("message_start", fmt.Sprintf(`{"type":"message_start","message":{"id":"msg_%d","type":"message","role":"assistant","model":"claude-haiku-5-5","content":[],"stop_reason":null,"usage":{"input_tokens":%d,"cache_creation_input_tokens":%d,"cache_read_input_tokens":%d,"output_tokens":1}}}`, s.n, in, cc, cr))
	s.raw("ping", `{"type":"ping"}`)
}

func (s *sw) text(idx int, delay time.Duration, parts ...string) {
	s.raw("content_block_start", fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"text","text":""}}`, idx))
	for _, p := range parts {
		if delay > 0 {
			time.Sleep(delay)
		}
		s.raw("content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"text_delta","text":%s}}`, idx, jstr(p)))
	}
	s.raw("content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, idx))
}

func (s *sw) tool(idx int, id, name, doc string) {
	a, b := len(doc)/3, 2*len(doc)/3
	s.raw("content_block_start", fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"tool_use","id":%s,"name":%s,"input":{}}}`, idx, jstr(id), jstr(name)))
	for _, p := range []string{doc[:a], doc[a:b], doc[b:]} {
		s.raw("content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"input_json_delta","partial_json":%s}}`, idx, jstr(p)))
	}
	s.raw("content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, idx))
}

func (s *sw) end(stop string, out int) {
	s.raw("message_delta", fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":%s,"stop_sequence":null},"usage":{"output_tokens":%d}}`, jstr(stop), out))
	s.raw("message_stop", `{"type":"message_stop"}`)
}

func (s *sw) std() {
	if s.n == 1 {
		s.start(60, 7000, 0)
	} else {
		s.start(60+s.n, 400, 7000+300*s.n)
	}
}

type fakeAPI struct {
	t      testing.TB
	srv    *httptest.Server
	mu     sync.Mutex
	bodies [][]byte
	heads  atomic.Int32
	keys   []string
	turns  func(n int, s *sw, body []byte)
}

func newFakeAPI(t testing.TB, h func(n int, s *sw, body []byte)) *fakeAPI {
	t.Helper()
	f := &fakeAPI{t: t, turns: h}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			f.heads.Add(1)
			w.WriteHeader(404)
			return
		}
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.bodies = append(f.bodies, body)
		f.keys = append(f.keys, r.Header.Get("x-api-key"))
		n := len(f.bodies)
		f.mu.Unlock()
		fl, _ := w.(http.Flusher)
		f.mu.Lock()
		h := f.turns
		f.mu.Unlock()
		h(n, &sw{w: w, f: fl, r: r, n: n}, body)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) requests() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]byte(nil), f.bodies...)
}

// set replaces the script (and forgets earlier requests).
func (f *fakeAPI) set(h func(n int, s *sw, body []byte)) {
	f.mu.Lock()
	f.turns, f.bodies, f.keys = h, nil, nil
	f.mu.Unlock()
}

func (f *fakeAPI) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.bodies) }

type scriptTurn func(s *sw, body []byte)

func scripted(turns ...scriptTurn) func(n int, s *sw, body []byte) {
	return func(n int, s *sw, body []byte) {
		if n > len(turns) {
			turns[len(turns)-1](s, body)
			return
		}
		turns[n-1](s, body)
	}
}

type tc struct{ id, name, input string }

func toolTurn(text string, calls ...tc) scriptTurn {
	return func(s *sw, body []byte) {
		s.std()
		idx := 0
		if text != "" {
			s.text(idx, 0, text)
			idx++
		}
		for _, c := range calls {
			s.tool(idx, c.id, c.name, c.input)
			idx++
		}
		s.end("tool_use", 50)
	}
}

// dyn builds a tool turn from the request body (to look refs up in snapshots).
func dyn(f func(body []byte) (string, []tc)) scriptTurn {
	return func(s *sw, body []byte) {
		text, calls := f(body)
		toolTurn(text, calls...)(s, body)
	}
}

func textTurn(delay time.Duration, parts ...string) scriptTurn {
	return func(s *sw, body []byte) { s.std(); s.text(0, delay, parts...); s.end("end_turn", 20+len(parts)*4) }
}

func doneTurn(summary string) scriptTurn {
	return toolTurn("", tc{"toolu_done", "done", fmt.Sprintf(`{"summary":%q,"success":true}`, summary)})
}

func actJSON(steps string) string { return `{"steps":` + steps + `}` }

// toolResultText concatenates the text of every tool_result in the request.
func toolResultText(body []byte) string {
	var p struct {
		Messages []struct {
			Content []map[string]any `json:"content"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(body, &p)
	var sb strings.Builder
	for _, m := range p.Messages {
		for _, b := range m.Content {
			if b["type"] != "tool_result" {
				continue
			}
			if arr, ok := b["content"].([]any); ok {
				for _, e := range arr {
					if mm, ok := e.(map[string]any); ok {
						if s, ok := mm["text"].(string); ok {
							sb.WriteString(s)
							sb.WriteString("\n")
						}
					}
				}
			}
		}
	}
	return sb.String()
}

var refRe = func(role, name string) *regexp.Regexp {
	return regexp.MustCompile(`(?m)` + role + `\s+"` + regexp.QuoteMeta(name) + `"[^\n]*?\[(?:[^\]]*\s)?ref=(e\d+)`)
}

// findRef returns the ref of `role "name"` in the snapshots of the request.
// userText is the text blocks of the user messages: the starting page's snapshot rides with the task there.
func userText(body []byte) string {
	var p struct {
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(body, &p)
	var sb strings.Builder
	for _, m := range p.Messages {
		if m.Role != "user" {
			continue
		}
		switch c := m.Content.(type) {
		case string:
			sb.WriteString(c + "\n")
		case []any:
			for _, e := range c {
				if mm, ok := e.(map[string]any); ok && mm["type"] == "text" {
					if s, ok := mm["text"].(string); ok {
						sb.WriteString(s + "\n")
					}
				}
			}
		}
	}
	return sb.String()
}

// findRef finds the newest ref of a node: in the tool results, or else in the starting page that came with the task.
func findRef(body []byte, role, name string) string {
	m := refRe(role, name).FindAllStringSubmatch(toolResultText(body), -1)
	if len(m) == 0 {
		m = refRe(role, name).FindAllStringSubmatch(userText(body), -1)
	}
	if len(m) == 0 {
		return ""
	}
	return m[len(m)-1][1]
}
