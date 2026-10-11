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
	PROTOCOL_VERSION = "2025-06-18"
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
	{"browser_aria_snapshot", "Return the page as a Playwright-style YAML accessibility tree: headings, paragraphs, lists, tables, text and every control with role, accessible name, states (checked/disabled/expanded/selected/required) and /url. Actionable nodes carry [ref=eN]; pass that ref back to any action tool as its selector (e.g. selector=\"e42\"). Options: selector (scope to a CSS selector or ref), interactiveOnly (controls + headings only, much smaller), delta (only lines changed since your previous snapshot of this tab), maxChars (default 60000; output is truncated with a note when hit), maxTextLen (per text node, default 200), maxDepth.",
		objProps(map[string]map[string]string{"tab": {"type": "string"}, "selector": {"type": "string"}, "interactiveOnly": {"type": "boolean"}, "delta": {"type": "boolean"}, "maxChars": {"type": "number"}, "maxTextLen": {"type": "number"}, "maxDepth": {"type": "number"}})},
	{"browser_click", "Click the first element matching a CSS selector (links navigate, submit buttons post forms).",
		objProps(map[string]map[string]string{"selector": {"type": "string"}, "tab": {"type": "string"}}, "selector")},
	{"browser_fill", "Set the value of an input/textarea/select matched by CSS selector.",
		objProps(map[string]map[string]string{"selector": {"type": "string"}, "value": {"type": "string"}, "tab": {"type": "string"}}, "selector", "value")},
	{"browser_press", "Press a key on an element; 'Enter' submits the enclosing form.",
		objProps(map[string]map[string]string{"selector": {"type": "string"}, "key": {"type": "string"}, "tab": {"type": "string"}}, "selector")},
	{"browser_evaluate", "Run JavaScript in the page (QuickJS, ES2023, full DOM: document, fetch, localStorage, timers). Expressions and statements both work; a returned promise is awaited (use await). DOM nodes serialise as {nodeType, tagName, id, ref}; pass the ref to action tools.",
		objProps(map[string]map[string]string{"expression": {"type": "string"}, "tab": {"type": "string"}}, "expression")},
	{"browser_console_log", "Read buffered console messages (log/warn/error) since a cursor.",
		objProps(map[string]map[string]string{"since": {"type": "number", "description": "last seen event sid, 0 for all"}, "tab": {"type": "string"}})},
	{"browser_network_log", "Read buffered network events (requests/responses/failures) since a cursor.",
		objProps(map[string]map[string]string{"since": {"type": "number"}, "tab": {"type": "string"}})},
	{"browser_get_html", "Return the current page HTML.",
		objProps(map[string]map[string]string{"tab": {"type": "string"}})},
	{"browser_get_text", "Return normalised visible text of a selector (default body).",
		objProps(map[string]map[string]string{"selector": {"type": "string"}, "tab": {"type": "string"}})},
	{"browser_screenshot", "Capture the page as a PNG image (returned as an image content part) plus a structured text render. Sweef has no pixel layout: the image is a readable text rendering, not a visual replica. Supports width, height, deviceScaleFactor.",
		objProps(map[string]map[string]string{"width": {"type": "number"}, "height": {"type": "number"}, "deviceScaleFactor": {"type": "number"}, "tab": {"type": "string"}})},
	{"browser_tab_new", "Open a new tab, optionally at a URL.",
		objProps(map[string]map[string]string{"url": {"type": "string"}})},
	{"browser_tab_list", "List open tabs with ids, urls, titles.", objProps(map[string]map[string]string{})},
	{"browser_tab_close", "Close a tab by id.",
		objProps(map[string]map[string]string{"tab": {"type": "string"}}, "tab")},
	{"browser_back", "History back.", objProps(map[string]map[string]string{"tab": {"type": "string"}})},
	{"browser_forward", "History forward.", objProps(map[string]map[string]string{"tab": {"type": "string"}})},
	{"browser_route_mock", "Intercept page requests (navigations, fetch/XHR, form posts, scripts). Match by URL glob (** any, * one segment) or /regex/, optionally by method and bodyContains. action: fulfill (status, body or json, contentType, headers), abort, or continue (rewrite url, newMethod, newBody, headers). times limits how often the rule applies.",
		objProps(map[string]map[string]string{"pattern": {"type": "string"}, "action": {"type": "string", "description": "fulfill|abort|continue"}, "method": {"type": "string"}, "bodyContains": {"type": "string"}, "status": {"type": "number"}, "body": {"type": "string"}, "contentType": {"type": "string"}, "url": {"type": "string"}, "newMethod": {"type": "string"}, "newBody": {"type": "string"}, "times": {"type": "number"}, "tab": {"type": "string"}}, "pattern")},
	{"browser_unroute", "Remove interception rules for a pattern (empty pattern removes all).",
		objProps(map[string]map[string]string{"pattern": {"type": "string"}, "tab": {"type": "string"}})},
	{"browser_pdf", "Export the current page as a PDF (headings, text, lists, tables, clickable links). Returns pdfBase64, or writes to path. Options: format (A4|Letter|Legal|A3|A5), landscape, margin (pt), scale, displayHeaderFooter.",
		objProps(map[string]map[string]string{"path": {"type": "string"}, "format": {"type": "string"}, "landscape": {"type": "boolean"}, "margin": {"type": "number"}, "scale": {"type": "number"}, "displayHeaderFooter": {"type": "boolean"}, "tab": {"type": "string"}})},
	{"browser_downloads", "List files the page downloaded (attachments, <a download>, blob/data URLs) with their saved paths.",
		objProps(map[string]map[string]string{"tab": {"type": "string"}})},
	{"browser_download", "Download a URL (relative to the page) to the downloads directory with the page's cookies.",
		objProps(map[string]map[string]string{"url": {"type": "string"}, "filename": {"type": "string"}, "tab": {"type": "string"}}, "url")},
	{"browser_expose_binding", "Expose a function on window that page code can call; calls are recorded (read them with browser_binding_calls) and resolve to reply (any JSON).",
		objProps(map[string]map[string]string{"name": {"type": "string"}, "reply": {"type": "string", "description": "JSON value returned to the page"}, "tab": {"type": "string"}}, "name")},
	{"browser_binding_calls", "Read calls page code made to exposed bindings since a cursor.",
		objProps(map[string]map[string]string{"since": {"type": "number"}, "tab": {"type": "string"}})},
	{"browser_har_export", "Export captured network traffic as HAR 1.2 JSON.",
		objProps(map[string]map[string]string{"tab": {"type": "string"}})},
	{"browser_stats", "Engine memory/goroutine stats — useful for agents supervising their own footprint.",
		objProps(map[string]map[string]string{})}, {"browser_check", "Check a checkbox or radio matched by selector (Playwright engines supported: css, text=, role=..., label=...).",
		objProps(map[string]map[string]string{"selector": {"type": "string"}, "tab": {"type": "string"}}, "selector")},
	{"browser_uncheck", "Uncheck a checkbox matched by selector.",
		objProps(map[string]map[string]string{"selector": {"type": "string"}, "tab": {"type": "string"}}, "selector")},
	{"browser_select_option", "Select an option in a <select> by value or visible label.",
		objProps(map[string]map[string]string{"selector": {"type": "string"}, "value": {"type": "string"}, "tab": {"type": "string"}}, "selector", "value")},
	{"browser_hover", "Hover an element (dispatches mouseenter/over; menus can react).",
		objProps(map[string]map[string]string{"selector": {"type": "string"}, "tab": {"type": "string"}}, "selector")},
	{"browser_drag", "Drag from one selector to another (HTML5 drag and drop events).",
		objProps(map[string]map[string]string{"source": {"type": "string"}, "target": {"type": "string"}, "tab": {"type": "string"}}, "source", "target")},
	{"browser_mouse", "Raw mouse: action move|down|up|click at x,y with optional button/clickCount.",
		objProps(map[string]map[string]string{"action": {"type": "string"}, "x": {"type": "number"}, "y": {"type": "number"}, "tab": {"type": "string"}}, "action")},
	{"browser_keyboard", "Raw keyboard: type text with delayMs, or press key combos like 'Control+a'.",
		objProps(map[string]map[string]string{"text": {"type": "string"}, "key": {"type": "string"}, "delayMs": {"type": "number"}, "tab": {"type": "string"}})},
	{"browser_wait_for_selector", "Wait until a selector reaches state attached|detached|visible|hidden (default visible). Returns match count.",
		objProps(map[string]map[string]string{"selector": {"type": "string"}, "state": {"type": "string"}, "timeoutMs": {"type": "number"}, "tab": {"type": "string"}}, "selector")},
	{"browser_wait_for_function", "Poll a JS expression until it returns truthy or timeout.",
		objProps(map[string]map[string]string{"expression": {"type": "string"}, "pollMs": {"type": "number"}, "timeoutMs": {"type": "number"}, "tab": {"type": "string"}}, "expression")},
	{"browser_wait_for_load_state", "Wait for load state: load|domcontentloaded|networkidle (no request started for 500 ms while page scripts run).",
		objProps(map[string]map[string]string{"state": {"type": "string"}, "timeoutMs": {"type": "number"}, "tab": {"type": "string"}})},
	{"browser_locator_count", "Count elements matching a selector without acting (locator engine prefixes allowed).",
		objProps(map[string]map[string]string{"selector": {"type": "string"}, "tab": {"type": "string"}}, "selector")},
	{"browser_get_attribute", "Read attributes of the first match.",
		objProps(map[string]map[string]string{"selector": {"type": "string"}, "name": {"type": "string"}, "tab": {"type": "string"}}, "selector")},
	{"browser_dialog_respond", "Arm auto-response for next dialogs: accept/dismiss with optional prompt text.",
		objProps(map[string]map[string]string{"action": {"type": "string", "description": "accept|dismiss|none"}, "promptText": {"type": "string"}, "tab": {"type": "string"}}, "action")},
	{"browser_storage_state", "Get cookies + localStorage as Playwright-compatible storage_state JSON (optionally save to path).",
		objProps(map[string]map[string]string{"path": {"type": "string"}, "context": {"type": "string"}})},
	{"browser_set_storage_state", "Load a storage_state JSON (inline string or file path) into a context.",
		objProps(map[string]map[string]string{"state": {"type": "string"}, "path": {"type": "string"}, "context": {"type": "string"}})},
	{"browser_new_context", "Create isolated context (own cookies/storage): userAgent, viewport, offline, colorScheme, proxy, headers.",
		objProps(map[string]map[string]string{"name": {"type": "string"}, "userAgent": {"type": "string"}, "viewportWidth": {"type": "number"}, "viewportHeight": {"type": "number"}, "offline": {"type": "boolean"}, "colorScheme": {"type": "string"}, "proxy": {"type": "string"}})},
	{"browser_list_contexts", "List browsing contexts.", objProps(map[string]map[string]string{})},
	{"browser_close_context", "Close a browsing context and its tabs.",
		objProps(map[string]map[string]string{"context": {"type": "string"}}, "context")},
	{"browser_emulate", "Emulate on a tab: device metrics, UA, geolocation, permissions, offline, color scheme, extra headers.",
		objProps(map[string]map[string]string{"kind": {"type": "string", "description": "metrics|userAgent|geolocation|permissions|offline|colorScheme|headers"}, "value": {"type": "string"}, "tab": {"type": "string"}}, "kind")},
	{"browser_add_init_script", "Register JS that runs before page scripts on every navigation (polyfills, hooks, stubs).",
		objProps(map[string]map[string]string{"source": {"type": "string"}, "tab": {"type": "string"}}, "source")},
	{"browser_request", "Context-level HTTP (APIRequestContext): method,url,body -> status,headers,body. No tab needed.",
		objProps(map[string]map[string]string{"method": {"type": "string"}, "url": {"type": "string"}, "body": {"type": "string"}, "context": {"type": "string"}}, "url")},
	{"browser_batch", "Run many browser steps in ONE call and get one observation back. Use it whenever you already know the next several actions (fill a form, click through a flow) instead of calling tools one by one. steps is an array; each step is {\"cmd\": verb, \"args\": [..]} with verbs open, click, dblclick, fill, type, press, hover, focus, check, uncheck, select, scroll, get (text|html|value|attr|title|url|count), is (visible|enabled|checked), wait (selector|ms|text t|url glob|fn js|load), eval, back, forward, reload, sleep, snapshot, expect (text t | url sub | title sub | visible sel | hidden sel | count sel n | value sel v). Selectors accept refs like e12. Stops at the first failing step unless stopOnError=false or the step has optional=true. observe (default interactive) returns the final page: none, interactive, full, delta (only lines changed since your last snapshot) or text.",
		map[string]any{"type": "object", "properties": map[string]any{
			"steps": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{
				"cmd": map[string]any{"type": "string"}, "args": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"method": map[string]any{"type": "string"}, "params": map[string]any{"type": "object"}, "optional": map[string]any{"type": "boolean"}}}},
			"stopOnError": map[string]any{"type": "boolean"},
			"observe":     map[string]any{"type": "string", "enum": []string{"none", "interactive", "full", "delta", "text"}},
			"tab":         map[string]any{"type": "string"}}, "required": []string{"steps"}}},
	{"browser_events_poll", "Poll DOM-mutation/console/network/navigation event stream since cursor (incremental, agent-friendly).",
		objProps(map[string]map[string]string{"since": {"type": "number"}, "tab": {"type": "string"}})},
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
		// Screenshots go out as a native MCP image part plus the text render;
		// the base64 is not repeated inside the text part, which would double
		// the tokens the client pays for.
		if p.Name == "browser_screenshot" {
			var r struct {
				PNG  string `json:"pngBase64"`
				Text string `json:"text"`
			}
			if json.Unmarshal(out, &r) == nil && r.PNG != "" {
				reply(map[string]any{"content": []map[string]any{
					{"type": "image", "data": r.PNG, "mimeType": "image/png"},
					{"type": "text", "text": r.Text},
				}}, nil)
				return
			}
		}
		// Snapshots are plain YAML, not YAML wrapped in a JSON string.
		if p.Name == "browser_aria_snapshot" {
			var r struct {
				YAML string `json:"yaml"`
			}
			if json.Unmarshal(out, &r) == nil && r.YAML != "" {
				content = r.YAML
			}
		}
		reply(map[string]any{"content": []map[string]any{{"type": "text", "text": content}}}, nil)
	default:
		reply(nil, &rpcErr{-32601, "method not found: " + req.Method})
	}
}

// methodFor maps MCP tool names onto RPC verbs.
var methodFor = map[string]string{
	"browser_navigate":            "Page.navigate",
	"browser_snapshot":            "Page.snapshot",
	"browser_aria_snapshot":       "Page.ariaSnapshot",
	"browser_click":               "Input.click",
	"browser_fill":                "Input.fill",
	"browser_press":               "Input.press",
	"browser_evaluate":            "Runtime.evaluate",
	"browser_console_log":         "Log.getConsole",
	"browser_network_log":         "Network.getLogs",
	"browser_get_html":            "Page.content",
	"browser_get_text":            "Page.textContent",
	"browser_screenshot":          "Page.screenshot",
	"browser_tab_new":             "Browser.newTab",
	"browser_tab_list":            "Browser.listTabs",
	"browser_tab_close":           "Browser.closeTab",
	"browser_back":                "Page.goBack",
	"browser_forward":             "Page.goForward",
	"browser_route_mock":          "Network.route",
	"browser_har_export":          "Network.har",
	"browser_stats":               "Browser.stats",
	"browser_check":               "Input.check",
	"browser_uncheck":             "Input.uncheck",
	"browser_select_option":       "Input.selectOption",
	"browser_hover":               "Input.hover",
	"browser_drag":                "Input.drag",
	"browser_mouse":               "Input.mouse",
	"browser_keyboard":            "Input.keyboard",
	"browser_wait_for_selector":   "Page.waitForSelector",
	"browser_wait_for_function":   "Page.waitForFunction",
	"browser_wait_for_load_state": "Page.waitForLoadState",
	"browser_locator_count":       "Locator.count",
	"browser_get_attribute":       "DOM.getAttributes",
	"browser_dialog_respond":      "Page.armDialog",
	"browser_storage_state":       "BrowserContext.storageState",
	"browser_set_storage_state":   "BrowserContext.loadStorageState",
	"browser_new_context":         "Browser.newContext",
	"browser_list_contexts":       "Browser.listContexts",
	"browser_close_context":       "Browser.closeContext",
	"browser_emulate":             "Emulation.setDeviceMetrics",
	"browser_add_init_script":     "Page.addInitScript",
	"browser_request":             "Fetch.request",
	"browser_events_poll":         "Events.poll",
	"browser_batch":               "Batch.run",
	"browser_unroute":             "Network.unroute",
	"browser_pdf":                 "Page.pdf",
	"browser_downloads":           "Page.downloads",
	"browser_download":            "Page.download",
	"browser_expose_binding":      "Runtime.addBinding",
	"browser_binding_calls":       "Runtime.bindingCalls",
}

func (s *Server) callTool(name string, args map[string]any) (json.RawMessage, string, error) {
	m, ok := methodFor[name]
	if !ok {
		return nil, "", fmt.Errorf("unknown tool %q", name)
	}
	if name == "browser_batch" {
		if _, set := args["observe"]; !set {
			args["observe"] = "interactive"
		}
	}
	raw, _ := json.Marshal(args)
	res, rerr := s.rpcS.Call(m, raw)
	if rerr != nil {
		return nil, "", fmt.Errorf("%s", rerr.Message)
	}
	// pretty-print compactly for LLM consumption
	return res, string(res), nil
}
