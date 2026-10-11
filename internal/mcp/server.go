// Package mcp implements a Model Context Protocol server (2024-11-05 spec,
// stdio transport) that exposes the Sweef engine as agent tools. This is
// what an AI client (Claude Desktop, OpenClaw agents, custom LLM loops) talks
// to: `sweef mcp` speaks JSON-RPC 2.0 over stdin/stdout with MCP framing —
// no browser process, no Electron, ~8 MB RSS total.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/sweefbrowser/sweef/internal/engine"
	"github.com/sweefbrowser/sweef/internal/rpc"
)

const (
	PROTOCOL_VERSION = "2024-11-05"
	SERVER_NAME      = "sweef-browser"
	SERVER_VERSION   = "0.1.0"
)

type rpcReq struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcResp struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcErr         `json:"error,omitempty"`
}

type rpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type toolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func objProps(props map[string]map[string]string, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

var tools = []toolDef{
	{"browser_navigate", "Navigate the active tab (or given tab) to a URL and wait for load.",
		objProps(map[string]map[string]string{"url": {"type": "string", "description": "absolute URL"}, "tab": {"type": "string", "description": "optional tab id"}}, "url")},
	{"browser_snapshot", "Return the accessibility tree (roles/names/states) of the page. Token-cheap; prefer this over HTML for reasoning.",
		objProps(map[string]map[string]string{"tab": {"type": "string"}})},
	{"browser_click", "Click the first element matching a CSS selector (links navigate, submit buttons post forms).",
		objProps(map[string]map[string]string{"selector": {"type": "string"}, "tab": {"type": "string"}}, "selector")},
	{"browser_fill", "Set the value of an input/textarea/select matched by CSS selector.",
		objProps(map[string]map[string]string{"selector": {"type": "string"}, "value": {"type": "string"}, "tab": {"type": "string"}}, "selector", "value")},
	{"browser_press", "Press a key on an element; 'Enter' submits the enclosing form.",
		objProps(map[string]map[string]string{"selector": {"type": "string"}, "key": {"type": "string"}, "tab": {"type": "string"}}, "selector")},
	{"browser_evaluate", "Run JavaScript in the page context (QuickJS ES2020) and return the JSON result.",
		objProps(map[string]map[string]string{"expression": {"type": "string"}, "tab": {"type": "string"}}, "expression")},
	{"browser_console_log", "Read buffered console messages (log/warn/error) since a cursor.",
		objProps(map[string]map[string]string{"since": {"type": "number", "description": "last seen event sid, 0 for all"}, "tab": {"type": "string"}})},
	{"browser_network_log", "Read buffered network events (requests/responses/failures) since a cursor.",
		objProps(map[string]map[string]string{"since": {"type": "number"}, "tab": {"type": "string"}})},
	{"browser_get_html", "Return the current page HTML.",
		objProps(map[string]map[string]string{"tab": {"type": "string"}})},
	{"browser_get_text", "Return normalised visible text of a selector (default body).",
		objProps(map[string]map[string]string{"selector": {"type": "string"}, "tab": {"type": "string"}})},
	{"browser_screenshot", "Render the page as structured text (fits low-res terminals); width optional.",
		objProps(map[string]map[string]string{"width": {"type": "number"}, "tab": {"type": "string"}})},
	{"browser_tab_new", "Open a new tab, optionally at a URL.",
		objProps(map[string]map[string]string{"url": {"type": "string"}})},
	{"browser_tab_list", "List open tabs with ids, urls, titles.", objProps(map[string]map[string]string{})},
	{"browser_tab_close", "Close a tab by id.",
		objProps(map[string]map[string]string{"tab": {"type": "string"}}, "tab")},
	{"browser_back", "History back.", objProps(map[string]map[string]string{"tab": {"type": "string"}})},
	{"browser_forward", "History forward.", objProps(map[string]map[string]string{"tab": {"type": "string"}})},
	{"browser_route_mock", "Intercept requests matching a glob and fulfill with a canned status/body (or abort).",
		objProps(map[string]map[string]string{"pattern": {"type": "string"}, "status": {"type": "number"}, "body": {"type": "string"}, "action": {"type": "string", "description": "fulfill|abort"}, "tab": {"type": "string"}}, "pattern")},
	{"browser_har_export", "Export captured network traffic as HAR 1.2 JSON.",
		objProps(map[string]map[string]string{"tab": {"type": "string"}})},
	{"browser_stats", "Engine memory/goroutine stats — useful for agents supervising their own footprint.",
		objProps(map[string]map[string]string{})},
}

// Server runs the MCP loop over rwc (stdin/stdout in production).
type Server struct {
	eng  *engine.Engine
	rpcS *rpc.Server
	w    io.Writer
}

func New(e *engine.Engine, w io.Writer) *Server {
	return &Server{eng: e, rpcS: rpc.NewServer(e), w: w}
}

func (s *Server) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = s.w.Write(b)
	return err
}

// Serve reads newline-delimited JSON-RPC until EOF.
func (s *Server) Serve(ctx context.Context, r io.Reader) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var req rpcReq
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			s.send(rpcResp{JSONRPC: "2.0", Error: &rpcErr{-32700, "parse error"}})
			continue
		}
		s.handle(ctx, &req)
	}
	return sc.Err()
}

func (s *Server) handle(ctx context.Context, req *rpcReq) {
	reply := func(result any, e *rpcErr) {
		s.send(rpcResp{JSONRPC: "2.0", ID: req.ID, Result: result, Error: e})
	}
	switch req.Method {
	case "initialize":
		reply(map[string]any{
			"protocolVersion": PROTOCOL_VERSION,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]string{"name": SERVER_NAME, "version": SERVER_VERSION},
		}, nil)
	case "notifications/initialized", "ping":
		if req.ID != nil {
			reply(map[string]any{}, nil)
		}
	case "tools/list":
		reply(map[string]any{"tools": tools}, nil)
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			reply(nil, &rpcErr{-32602, "bad params"})
			return
		}
		out, content, err := s.callTool(p.Name, p.Arguments)
		if err != nil {
			reply(map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": err.Error()}}}, nil)
			return
		}
		_ = out
		reply(map[string]any{"content": []map[string]any{{"type": "text", "text": content}}}, nil)
	default:
		reply(nil, &rpcErr{-32601, "method not found: " + req.Method})
	}
}

// methodFor maps MCP tool names onto RPC verbs.
var methodFor = map[string]string{
	"browser_navigate":     "Page.navigate",
	"browser_snapshot":     "Page.snapshot",
	"browser_click":        "Input.click",
	"browser_fill":         "Input.fill",
	"browser_press":        "Input.press",
	"browser_evaluate":     "Runtime.evaluate",
	"browser_console_log":  "Log.getConsole",
	"browser_network_log":  "Network.getLogs",
	"browser_get_html":     "Page.content",
	"browser_get_text":     "Page.textContent",
	"browser_screenshot":   "Page.screenshot",
	"browser_tab_new":      "Browser.newTab",
	"browser_tab_list":     "Browser.listTabs",
	"browser_tab_close":    "Browser.closeTab",
	"browser_back":         "Page.goBack",
	"browser_forward":      "Page.goForward",
	"browser_route_mock":   "Network.route",
	"browser_har_export":   "Network.har",
	"browser_stats":        "Browser.stats",
}

func (s *Server) callTool(name string, args map[string]any) (json.RawMessage, string, error) {
	m, ok := methodFor[name]
	if !ok {
		return nil, "", fmt.Errorf("unknown tool %q", name)
	}
	raw, _ := json.Marshal(args)
	res, rerr := s.rpcS.Call(m, raw)
	if rerr != nil {
		return nil, "", fmt.Errorf("%s", rerr.Message)
	}
	// pretty-print compactly for LLM consumption
	return res, string(res), nil
}
