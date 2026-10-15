package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/engine"
)

// client drives a real Server over its stdio loop (pipes), as a host would.
type client struct {
	t   *testing.T
	in  io.WriteCloser
	out *bufio.Reader
	id  int
	srv *Server
}

func newClient(t *testing.T) *client {
	t.Helper()
	e := engine.New(engine.DefaultOptions())
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	srv := New(e, outW)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { srv.Serve(ctx, inR); outW.Close(); close(done) }()
	t.Cleanup(func() {
		cancel()
		inW.Close()
		<-done
		e.Close()
	})
	return &client{t: t, in: inW, out: bufio.NewReaderSize(outR, 1<<20), srv: srv}
}

func (c *client) write(s string) {
	c.t.Helper()
	if _, err := io.WriteString(c.in, s+"\n"); err != nil {
		c.t.Fatal(err)
	}
}

// read returns the next message, failing after a timeout.
func (c *client) read() map[string]any {
	c.t.Helper()
	type res struct {
		line []byte
		err  error
	}
	ch := make(chan res, 1)
	go func() { l, err := c.out.ReadBytes('\n'); ch <- res{l, err} }()
	select {
	case r := <-ch:
		if r.err != nil {
			c.t.Fatalf("read: %v", r.err)
		}
		var m map[string]any
		if err := json.Unmarshal(r.line, &m); err != nil {
			c.t.Fatalf("bad JSON from server: %v: %s", err, r.line)
		}
		return m
	case <-time.After(20 * time.Second):
		c.t.Fatal("no reply from server")
	}
	return nil
}

func (c *client) rpc(method string, params any) map[string]any {
	c.t.Helper()
	c.id++
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": c.id, "method": method, "params": params})
	c.write(string(b))
	m := c.read()
	if id, _ := m["id"].(float64); int(id) != c.id {
		c.t.Fatalf("reply id %v, want %d", m["id"], c.id)
	}
	return m
}

// tool calls a tool and returns its text parts joined, its image parts, and isError.
func (c *client) tool(name string, args map[string]any) (string, []map[string]any, bool) {
	c.t.Helper()
	m := c.rpc("tools/call", map[string]any{"name": name, "arguments": args})
	r, ok := m["result"].(map[string]any)
	if !ok {
		c.t.Fatalf("%s: no result: %v", name, m)
	}
	var txt strings.Builder
	var imgs []map[string]any
	for _, p := range r["content"].([]any) {
		pm := p.(map[string]any)
		switch pm["type"] {
		case "text":
			txt.WriteString(pm["text"].(string))
		case "image":
			imgs = append(imgs, pm)
		}
	}
	isErr, _ := r["isError"].(bool)
	return txt.String(), imgs, isErr
}

func (c *client) mustTool(name string, args map[string]any) string {
	c.t.Helper()
	txt, _, isErr := c.tool(name, args)
	if isErr {
		c.t.Fatalf("%s(%v): error: %s", name, args, txt)
	}
	return txt
}

func TestHandshake(t *testing.T) {
	c := newClient(t)
	r := c.rpc("initialize", map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "test", "version": "0"}})["result"].(map[string]any)
	if r["protocolVersion"] != "2024-11-05" {
		t.Errorf("protocolVersion = %v, want the client's supported 2024-11-05", r["protocolVersion"])
	}
	if _, ok := r["capabilities"].(map[string]any)["tools"]; !ok {
		t.Errorf("capabilities lack tools: %v", r["capabilities"])
	}
	if si := r["serverInfo"].(map[string]any); si["name"] != SERVER_NAME || si["version"] != SERVER_VERSION {
		t.Errorf("serverInfo = %v", si)
	}
	r = c.rpc("initialize", map[string]any{"protocolVersion": "1999-01-01"})["result"].(map[string]any)
	if r["protocolVersion"] != PROTOCOL_VERSION {
		t.Errorf("unknown client version answered with %v, want %s", r["protocolVersion"], PROTOCOL_VERSION)
	}
	// notifications get no reply: the next reply must belong to the ping
	c.write(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	c.write(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}`)
	c.write(`{"jsonrpc":"2.0","method":"some/unknown/notification"}`)
	if m := c.rpc("ping", nil); m["result"] == nil {
		t.Errorf("ping: %v", m)
	}
	if e, _ := c.rpc("no/such/method", nil)["error"].(map[string]any); e == nil || e["code"].(float64) != -32601 {
		t.Errorf("unknown method: error = %v, want -32601", e)
	}
	c.write(`{not json`)
	if e, _ := c.read()["error"].(map[string]any); e == nil || e["code"].(float64) != -32700 {
		t.Errorf("parse error: %v", e)
	}
	if e, _ := c.rpc("tools/call", map[string]any{"arguments": map[string]any{}})["error"].(map[string]any); e == nil || e["code"].(float64) != -32602 {
		t.Errorf("tools/call without name: %v", e)
	}
}

var toolName = regexp.MustCompile(`^browser_[a-z_]+$`)

func TestToolsListSchemas(t *testing.T) {
	c := newClient(t)
	ts := c.rpc("tools/list", nil)["result"].(map[string]any)["tools"].([]any)
	if len(ts) < 50 {
		t.Fatalf("%d tools, want 50+", len(ts))
	}
	validType := map[string]bool{"string": true, "number": true, "integer": true, "boolean": true, "object": true, "array": true}
	seen := map[string]bool{}
	for _, x := range ts {
		td := x.(map[string]any)
		name, _ := td["name"].(string)
		if !toolName.MatchString(name) || seen[name] {
			t.Errorf("bad or duplicate tool name %q", name)
		}
		seen[name] = true
		if d, _ := td["description"].(string); len(d) < 8 {
			t.Errorf("%s: description %q", name, d)
		}
		s, _ := td["inputSchema"].(map[string]any)
		if s == nil || s["type"] != "object" {
			t.Errorf("%s: inputSchema must be an object schema: %v", name, s)
			continue
		}
		props, _ := s["properties"].(map[string]any)
		if props == nil {
			t.Errorf("%s: no properties map", name)
		}
		for pn, pv := range props {
			pt, _ := pv.(map[string]any)["type"].(string)
			if !validType[pt] {
				t.Errorf("%s.%s: type %q", name, pn, pt)
			}
			if pt == "array" && pv.(map[string]any)["items"] == nil {
				t.Errorf("%s.%s: array without items", name, pn)
			}
		}
		if r, ok := s["required"]; ok {
			for _, rn := range r.([]any) {
				if _, ok := props[rn.(string)]; !ok {
					t.Errorf("%s: required %q is not a property", name, rn)
				}
			}
		}
	}
	// every tool maps onto an RPC method the server knows
	for _, td := range tools {
		if td.method == "" {
			continue
		}
		_, rerr := c.srv.rpcS.Call(td.method, json.RawMessage(`{"tab":"none"}`))
		if rerr != nil && strings.Contains(rerr.Message, "unknown method") {
			t.Errorf("%s maps onto unknown RPC method %s", td.Name, td.method)
		}
	}
}

const fixtureLanding = `<!doctype html><html><head><title>Acme</title></head><body>
<nav><a href="/">Home</a> <a href="/login">Sign in</a></nav>
<h1>Ship faster</h1><p>%s</p>
<button id="cta" onclick="document.getElementById('out').textContent='clicked '+(++window.n||(window.n=1))">Start</button>
<p id="out"></p>
<button id="confirm" onclick="document.title = confirm('sure?') ? 'yes' : 'no'">Confirm</button>
<script>console.log('landing ready'); console.warn('careful')</script>
</body></html>`

const fixtureLogin = `<!doctype html><html><head><title>Sign in</title></head><body>
<h1>Sign in</h1><form action="/welcome" method="get">
<label for="email">Email</label><input id="email" name="email" type="email">
<label><input type="checkbox" name="remember" id="remember"> Remember me</label>
<select id="plan" name="plan"><option value="free">Free</option><option value="pro">Pro</option></select>
<button type="submit">Sign in</button></form></body></html>`

func fixture(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	long := strings.Repeat("All work and no play makes a dull page. ", 2000)
	serve := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			io.WriteString(w, body)
		}
	}
	mux.HandleFunc("/", serve(fmt.Sprintf(fixtureLanding, "Acme helps teams deploy.")))
	mux.HandleFunc("/long", serve(fmt.Sprintf(fixtureLanding, long)))
	mux.HandleFunc("/login", serve(fixtureLogin))
	mux.HandleFunc("/welcome", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, "<title>Welcome</title><h1>Welcome %s</h1><p>plan %s</p>", r.URL.Query().Get("email"), r.URL.Query().Get("plan"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

var refRe = regexp.MustCompile(`link "Sign in" \[ref=(e\d+)\]`)

func TestFlowOnLocalSite(t *testing.T) {
	site := fixture(t)
	c := newClient(t)
	c.rpc("initialize", map[string]any{"protocolVersion": PROTOCOL_VERSION})

	if out := c.mustTool("browser_navigate", map[string]any{"url": site.URL + "/"}); out != "Acme\n"+site.URL+"/" {
		t.Errorf("navigate = %q, want title and URL lines", out)
	}
	snap := c.mustTool("browser_snapshot", nil)
	m := refRe.FindStringSubmatch(snap)
	if m == nil {
		t.Fatalf("snapshot has no ref for the Sign in link:\n%s", snap)
	}
	// an action after a snapshot reports what changed
	out := c.mustTool("browser_click", map[string]any{"selector": "#cta"})
	if !strings.HasPrefix(out, "ok\n# changes since your last snapshot:") || !strings.Contains(out, "clicked 1") {
		t.Errorf("click reply lacks the delta: %q", out)
	}
	// the delta became the new baseline: an explicit delta now reports no change
	if out := c.mustTool("browser_snapshot", map[string]any{"delta": true}); out != "(no changes since your last snapshot)" {
		t.Errorf("delta after the auto-delta = %q", out)
	}
	// dialogs: arm accept, then a confirm() returns true
	c.mustTool("browser_dialog_respond", map[string]any{"action": "accept"})
	c.mustTool("browser_click", map[string]any{"selector": "#confirm"})
	if out := c.mustTool("browser_evaluate", map[string]any{"expression": "document.title"}); out != `"yes"` {
		t.Errorf("confirm after arming accept: title %s", out)
	}
	// console and network logs come back as lines with a cursor
	if out := c.mustTool("browser_console_log", nil); !strings.Contains(out, "log: landing ready") || !strings.Contains(out, "warn: careful") || !strings.Contains(out, "(next: since=") {
		t.Errorf("console log = %q", out)
	}
	if out := c.mustTool("browser_network_log", nil); !strings.Contains(out, "GET 200 "+site.URL+"/ (text/html)") {
		t.Errorf("network log = %q", out)
	}

	// click by ref navigates; the reply says so instead of diffing two pages
	out = c.mustTool("browser_click", map[string]any{"selector": m[1]})
	if !strings.Contains(out, "[navigated: Sign in "+site.URL+"/login") {
		t.Errorf("click on the Sign in ref = %q", out)
	}
	c.mustTool("browser_snapshot", map[string]any{"interactiveOnly": true})
	if out := c.mustTool("browser_fill", map[string]any{"selector": "#email", "value": "ada@example.com"}); !strings.Contains(out, "ada@example.com") {
		t.Errorf("fill reply lacks the changed textbox: %q", out)
	}
	c.mustTool("browser_check", map[string]any{"selector": "label=Remember me"})
	c.mustTool("browser_select_option", map[string]any{"selector": "#plan", "value": "Pro"})
	if out := c.mustTool("browser_evaluate", map[string]any{"expression": "[document.querySelector('#remember').checked, document.querySelector('#plan').value]"}); out != `[true,"pro"]` {
		t.Errorf("checkbox/select state = %s", out)
	}
	if out := c.mustTool("browser_get_attribute", map[string]any{"selector": "#email", "name": "type"}); out != "email" {
		t.Errorf("get_attribute type = %q", out)
	}
	c.mustTool("browser_press", map[string]any{"selector": "#email", "key": "Enter"})
	if out := c.mustTool("browser_get_text", map[string]any{"selector": "h1"}); out != "Welcome ada@example.com" {
		t.Errorf("after submitting: h1 = %q", out)
	}

	// screenshot: one PNG image part, no base64 in the text
	txt, imgs, isErr := c.tool("browser_screenshot", nil)
	if isErr || len(imgs) != 1 || txt != "" {
		t.Fatalf("screenshot: err=%v images=%d text=%q", isErr, len(imgs), txt)
	}
	b, err := base64.StdEncoding.DecodeString(imgs[0]["data"].(string))
	if err != nil || imgs[0]["mimeType"] != "image/png" {
		t.Fatalf("screenshot data: %v %v", err, imgs[0]["mimeType"])
	}
	if cfg, err := png.DecodeConfig(bytes.NewReader(b)); err != nil || cfg.Width < 100 || cfg.Height < 100 {
		t.Errorf("screenshot PNG: %v %dx%d", err, cfg.Width, cfg.Height)
	}
	c.mustTool("browser_emulate", map[string]any{"kind": "viewport", "value": "800x600"})
	if _, imgs, _ = c.tool("browser_screenshot", nil); len(imgs) == 1 {
		b, _ = base64.StdEncoding.DecodeString(imgs[0]["data"].(string))
		if cfg, _ := png.DecodeConfig(bytes.NewReader(b)); cfg.Width != 800 || cfg.Height != 600 {
			t.Errorf("screenshot after viewport 800x600 is %dx%d", cfg.Width, cfg.Height)
		}
	}
	if txt, _, _ := c.tool("browser_screenshot", map[string]any{"text": true}); !strings.Contains(txt, "Welcome") {
		t.Errorf("screenshot text=true lacks the text render: %q", txt)
	}

	if out := c.mustTool("browser_back", nil); !strings.HasPrefix(out, "ok") {
		t.Errorf("back = %q", out)
	}
	if out := c.mustTool("browser_evaluate", map[string]any{"expression": "location.pathname"}); out != `"/login"` {
		t.Errorf("after back: %s", out)
	}
	// any RPC method through browser_call
	if out := c.mustTool("browser_call", map[string]any{"method": "Page.title"}); out != `{"title":"Sign in"}` {
		t.Errorf("browser_call Page.title = %s", out)
	}
}

func TestBatchTool(t *testing.T) {
	site := fixture(t)
	c := newClient(t)
	out := c.mustTool("browser_batch", map[string]any{"steps": []any{
		map[string]any{"cmd": "open", "args": []any{site.URL + "/login"}},
		map[string]any{"cmd": "fill", "args": []any{"#email", "bo@example.com"}},
		map[string]any{"cmd": "click", "args": []any{"button[type=submit]"}},
		map[string]any{"cmd": "expect", "args": []any{"url", "/welcome"}},
		map[string]any{"method": "Page.title", "params": map[string]any{}},
	}})
	if !strings.Contains(out, "Welcome bo@example.com") {
		t.Errorf("batch observation lacks the final page: %.300s", out)
	}
	txt, _, isErr := c.tool("browser_batch", map[string]any{"steps": []any{
		map[string]any{"cmd": "click", "args": []any{"#nope"}},
		map[string]any{"cmd": "get", "args": []any{"title"}},
	}, "observe": "none"})
	if !isErr || !strings.Contains(txt, "✗ click #nope") || strings.Contains(txt, "get title") {
		t.Errorf("failing batch: isError=%v %.300s", isErr, txt)
	}
}

func TestErrorsAndTruncation(t *testing.T) {
	site := fixture(t)
	c := newClient(t)
	if txt, _, isErr := c.tool("browser_nope", nil); !isErr || !strings.Contains(txt, "unknown tool") {
		t.Errorf("unknown tool: %v %q", isErr, txt)
	}
	c.mustTool("browser_navigate", map[string]any{"url": site.URL + "/long"})
	t0 := time.Now()
	txt, _, isErr := c.tool("browser_click", map[string]any{"selector": "#does-not-exist"})
	if !isErr || !strings.Contains(txt, "#does-not-exist") || strings.HasPrefix(txt, "engine:") {
		t.Errorf("missing element: isError=%v %q", isErr, txt)
	}
	if d := time.Since(t0); d > 10*time.Second {
		t.Errorf("missing element took %v", d)
	}
	if txt, _, isErr := c.tool("browser_emulate", map[string]any{"kind": "viewport", "value": "wide"}); !isErr || !strings.Contains(txt, "WIDTHxHEIGHT") {
		t.Errorf("bad viewport value: %v %q", isErr, txt)
	}
	// large outputs are capped with a continuation offset
	html := c.mustTool("browser_get_html", nil)
	if len(html) > defaultMaxChars+200 || !strings.Contains(html, "call again with offset=20000") {
		t.Fatalf("get_html: %d chars, tail %q", len(html), html[len(html)-120:])
	}
	next := c.mustTool("browser_get_html", map[string]any{"offset": defaultMaxChars, "maxChars": 1000})
	if len(next) < 1000 || !strings.Contains(next, "chars 20000-21000 of ") {
		t.Errorf("continuation: %q", next[len(next)-100:])
	}
	if txt := c.mustTool("browser_get_text", map[string]any{"maxChars": 50}); !strings.Contains(txt, "[truncated: chars 0-50 of") {
		t.Errorf("get_text maxChars=50: %q", txt)
	}
	// snapshots keep their own cap
	if snap := c.mustTool("browser_snapshot", map[string]any{"maxChars": 2000}); len(snap) > 2300 {
		t.Errorf("snapshot maxChars=2000 returned %d chars", len(snap))
	}
	// page HTML is not escaped on the wire
	c.write(`{"jsonrpc":"2.0","id":99,"method":"tools/call","params":{"name":"browser_get_html","arguments":{"maxChars":200}}}`)
	line, _ := c.out.ReadBytes('\n')
	if bytes.Contains(line, []byte("\\u003c")) || !bytes.Contains(line, []byte("<html")) {
		t.Errorf("HTML is \\u003c-escaped on the wire: %.120s", line)
	}
}
