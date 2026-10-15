package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/sweefbrowser/sweef/internal/rpc"
)

const (
	defaultMaxChars = 20000 // text outputs (HTML, text, logs, eval results) per call
	autoDeltaMax    = 2500  // larger post-action deltas are replaced by a one-line note
)

// snapState is the snapshot the agent last saw on a tab: what later deltas
// (explicit delta=true, or the automatic one after an action) are taken against.
type snapState struct {
	opts map[string]any
	yaml string
	url  string
}

type content = map[string]any

func textResult(s string) map[string]any {
	return map[string]any{"content": []content{{"type": "text", "text": s}}}
}

func errResult(msg string) map[string]any {
	msg = strings.TrimPrefix(msg, "engine: ")
	return map[string]any{"isError": true, "content": []content{{"type": "text", "text": msg}}}
}

// callTool runs one tool and returns the MCP tools/call result.
func (s *Server) callTool(name string, args map[string]any) map[string]any {
	td, ok := toolByName[name]
	if !ok {
		return errResult(fmt.Sprintf("unknown tool %q", name))
	}
	switch name {
	case "browser_snapshot", "browser_aria_snapshot":
		return s.snapshot(args)
	case "browser_emulate":
		return s.emulate(args)
	case "browser_call":
		m, _ := args["method"].(string)
		p, _ := args["params"].(map[string]any)
		if p == nil {
			p = map[string]any{}
		}
		raw, err := s.call(m, p)
		if err != nil {
			return errResult(err.Error())
		}
		return textResult(page(string(raw), args))
	}
	method := td.method
	switch name {
	case "browser_get_attribute":
		if n, _ := args["name"].(string); n == "" {
			method = "DOM.getAttributes"
		}
	case "browser_batch":
		if _, set := args["observe"]; !set {
			args["observe"] = "interactive"
		}
	case "browser_screenshot":
		if args["text"] != true {
			args["noText"] = true
		}
	case "browser_pdf":
		if p, _ := args["path"].(string); p == "" {
			args["path"] = filepath.Join(os.TempDir(), fmt.Sprintf("sweef-%d-%d.pdf", os.Getpid(), pdfSeq.Add(1)))
		}
	}
	raw, err := s.call(method, args)
	if err != nil {
		return errResult(err.Error())
	}
	switch name {
	case "browser_screenshot":
		return screenshotResult(raw)
	case "browser_batch":
		return s.batchResult(raw, args)
	}
	out := s.format(name, raw, args)
	if td.action && s.AutoDelta {
		out += s.autoDelta(args)
	}
	return textResult(out)
}

var pdfSeq atomic.Int64

func (s *Server) call(method string, args map[string]any) (json.RawMessage, error) {
	raw, _ := rpc.MarshalCompact(args)
	res, rerr := s.rpcS.Call(method, raw)
	if rerr != nil {
		return nil, fmt.Errorf("%s", rerr.Message)
	}
	return res, nil
}

// format turns an RPC result into the text the model reads.
func (s *Server) format(name string, raw json.RawMessage, args map[string]any) string {
	str := string(raw)
	if str == `{"ok":true}` {
		return "ok"
	}
	var obj map[string]any
	switch name {
	case "browser_navigate", "browser_tab_new":
		if json.Unmarshal(raw, &obj) == nil {
			out := fmt.Sprintf("%v\n%v", obj["title"], obj["url"])
			if id, ok := obj["tab"]; ok {
				out = fmt.Sprintf("tab %v\n%s", id, out)
			}
			return out
		}
	case "browser_get_text", "browser_get_html", "browser_evaluate", "browser_get_attribute":
		if json.Unmarshal(raw, &obj) == nil {
			for _, k := range []string{"text", "html", "value"} {
				if v, ok := obj[k].(string); ok && len(obj) <= 2 {
					return page(v, args)
				}
			}
			if v, ok := obj["result"]; ok && len(obj) == 1 {
				if sv, isStr := v.(string); isStr {
					b, _ := rpc.MarshalCompact(sv)
					return page(string(b), args)
				}
				b, _ := rpc.MarshalCompact(v)
				return page(string(b), args)
			}
		}
	case "browser_console_log":
		return page(consoleLines(raw), args)
	case "browser_network_log":
		if args["verbose"] != true {
			return page(networkLines(raw), args)
		}
	}
	return page(str, args)
}

// page applies the output cap with a continuation offset.
func page(s string, args map[string]any) string {
	max := int(numArg(args, "maxChars"))
	if max <= 0 {
		max = defaultMaxChars
	}
	off := int(numArg(args, "offset"))
	if off < 0 {
		off = 0
	}
	if off == 0 && len(s) <= max {
		return s
	}
	if off >= len(s) {
		return fmt.Sprintf("(offset %d is past the end; output is %d chars)", off, len(s))
	}
	end := off + max
	if end > len(s) {
		end = len(s)
	}
	for end < len(s) && end > off && !utf8Start(s[end]) {
		end--
	}
	out := s[off:end]
	if end < len(s) {
		out += fmt.Sprintf("\n[truncated: chars %d-%d of %d; call again with offset=%d for more]", off, end, len(s), end)
	}
	return out
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

func numArg(args map[string]any, k string) float64 {
	switch v := args[k].(type) {
	case float64:
		return v
	case string:
		f, _ := strconv.ParseFloat(v, 64)
		return f
	}
	return 0
}

type logEvent struct {
	SID  int64          `json:"sid"`
	Kind string         `json:"kind"`
	Data map[string]any `json:"data"`
}

func consoleLines(raw json.RawMessage) string {
	var r struct {
		Messages []logEvent `json:"messages"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return string(raw)
	}
	if len(r.Messages) == 0 {
		return "no console messages"
	}
	var b strings.Builder
	var last int64
	for _, m := range r.Messages {
		fmt.Fprintf(&b, "%v: %v\n", m.Data["level"], m.Data["text"])
		last = m.SID
	}
	fmt.Fprintf(&b, "(next: since=%d)", last)
	return b.String()
}

func networkLines(raw json.RawMessage) string {
	var r struct {
		Entries []logEvent `json:"entries"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return string(raw)
	}
	if len(r.Entries) == 0 {
		return "no network activity"
	}
	done := map[string]bool{}
	for _, e := range r.Entries {
		if e.Kind != "Network.request" {
			done[fmt.Sprint(e.Data["request.method"])+" "+fmt.Sprint(e.Data["request.url"])] = true
			done[fmt.Sprint(e.Data["url"])] = true
		}
	}
	var b strings.Builder
	var last int64
	for _, e := range r.Entries {
		last = e.SID
		d := e.Data
		switch e.Kind {
		case "Network.response":
			fmt.Fprintf(&b, "%v %v %v", d["request.method"], d["response.status"], d["request.url"])
			if h, ok := d["response.headers"].(map[string]any); ok {
				if ct, ok := h["Content-Type"].(string); ok {
					ct, _, _ = strings.Cut(ct, ";")
					fmt.Fprintf(&b, " (%s)", ct)
				}
			}
			b.WriteByte('\n')
		case "Network.failure":
			fmt.Fprintf(&b, "FAILED %v: %v\n", d["url"], d["error"])
		case "Network.request":
			if !done[fmt.Sprint(d["request.method"])+" "+fmt.Sprint(d["request.url"])] && !done[fmt.Sprint(d["request.url"])] {
				fmt.Fprintf(&b, "%v (pending) %v\n", d["request.method"], d["request.url"])
			}
		default:
			fmt.Fprintf(&b, "%s %v\n", strings.TrimPrefix(e.Kind, "Network."), firstNonEmpty(d["url"], d["request.url"]))
		}
	}
	fmt.Fprintf(&b, "(next: since=%d)", last)
	return b.String()
}

func firstNonEmpty(vs ...any) any {
	for _, v := range vs {
		if v != nil && v != "" {
			return v
		}
	}
	return ""
}

func screenshotResult(raw json.RawMessage) map[string]any {
	var r struct {
		PNG  string `json:"pngBase64"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &r) != nil || r.PNG == "" {
		return textResult(string(raw))
	}
	parts := []content{{"type": "image", "data": r.PNG, "mimeType": "image/png"}}
	if r.Text != "" {
		parts = append(parts, content{"type": "text", "text": page(r.Text, nil)})
	}
	return map[string]any{"content": parts}
}

// ---- snapshots and deltas ------------------------------------------------------

var snapOptKeys = []string{"selector", "interactiveOnly", "maxChars", "maxTextLen", "maxDepth"}

func (s *Server) tabKey(args map[string]any) (string, string) {
	if id, _ := args["tab"].(string); id != "" {
		if t, err := s.eng.Tab(id); err == nil {
			return id, t.URL()
		}
		return id, ""
	}
	t, err := s.eng.ActiveTab()
	if err != nil {
		return "", ""
	}
	return t.ID, t.URL()
}

func (s *Server) snapshot(args map[string]any) map[string]any {
	opts := map[string]any{}
	for _, k := range snapOptKeys {
		if v, ok := args[k]; ok {
			opts[k] = v
		}
	}
	p := map[string]any{"tab": args["tab"]}
	for k, v := range opts {
		p[k] = v
	}
	raw, err := s.call("Page.ariaSnapshot", p)
	if err != nil {
		return errResult(err.Error())
	}
	var r struct {
		YAML string `json:"yaml"`
	}
	json.Unmarshal(raw, &r)
	id, u := s.tabKey(args)
	prev := s.snaps[id]
	s.snaps[id] = &snapState{opts: opts, yaml: r.YAML, url: u}
	if args["delta"] == true && prev != nil && reflect.DeepEqual(prev.opts, opts) && prev.url == u {
		if prev.yaml == r.YAML {
			return textResult("(no changes since your last snapshot)")
		}
		return textResult(rpc.LineDiff(prev.yaml, r.YAML))
	}
	return textResult(r.YAML)
}

// autoDelta is appended to an action's reply: what changed on the page, relative
// to the snapshot the agent last saw, so it rarely needs a follow-up snapshot.
// Nothing is added before the agent's first snapshot of the tab.
func (s *Server) autoDelta(args map[string]any) string {
	id, u := s.tabKey(args)
	prev := s.snaps[id]
	if prev == nil {
		return ""
	}
	t, err := s.eng.Tab(id)
	if err != nil {
		return ""
	}
	if u != prev.url {
		return fmt.Sprintf("\n[navigated: %s %s; call browser_snapshot to read it]", t.Title(), u)
	}
	p := map[string]any{"tab": id}
	for k, v := range prev.opts {
		p[k] = v
	}
	raw, err := s.call("Page.ariaSnapshot", p)
	if err != nil {
		return ""
	}
	var r struct {
		YAML string `json:"yaml"`
	}
	json.Unmarshal(raw, &r)
	if r.YAML == prev.yaml {
		return "\n[page unchanged]"
	}
	d := rpc.LineDiff(prev.yaml, r.YAML)
	if len(d) > autoDeltaMax {
		return fmt.Sprintf("\n[page changed (%d-char diff); call browser_snapshot, or with delta=true]", len(d))
	}
	s.snaps[id] = &snapState{opts: prev.opts, yaml: r.YAML, url: u}
	return "\n# changes since your last snapshot:\n" + d
}

// ---- emulation --------------------------------------------------------------------

func (s *Server) emulate(args map[string]any) map[string]any {
	kind, _ := args["kind"].(string)
	val := strings.TrimSpace(fmt.Sprint(args["value"]))
	tab := args["tab"]
	var method string
	p := map[string]any{"tab": tab}
	switch strings.ToLower(kind) {
	case "viewport", "metrics", "devicemetrics":
		method = "Emulation.setDeviceMetrics"
		dims, dsf, _ := strings.Cut(val, "@")
		w, h, ok := strings.Cut(strings.ToLower(dims), "x")
		wi, e1 := strconv.Atoi(strings.TrimSpace(w))
		hi, e2 := strconv.Atoi(strings.TrimSpace(h))
		if !ok || e1 != nil || e2 != nil {
			return errResult("viewport value: WIDTHxHEIGHT, optionally @scale (390x844@3)")
		}
		p["width"], p["height"] = wi, hi
		if f, err := strconv.ParseFloat(dsf, 64); err == nil {
			p["deviceScaleFactor"] = f
		}
	case "useragent", "ua":
		method, p["userAgent"] = "Emulation.setUserAgentOverride", val
	case "geolocation", "geo":
		method = "Emulation.setGeolocation"
		la, lo, _ := strings.Cut(val, ",")
		lat, e1 := strconv.ParseFloat(strings.TrimSpace(la), 64)
		lon, e2 := strconv.ParseFloat(strings.TrimSpace(lo), 64)
		if e1 != nil || e2 != nil {
			return errResult("geolocation value: LAT,LON")
		}
		p["latitude"], p["longitude"] = lat, lon
	case "permissions":
		method = "Emulation.grantPermissions"
		var ps []any
		for _, x := range strings.Split(val, ",") {
			if x = strings.TrimSpace(x); x != "" {
				ps = append(ps, x)
			}
		}
		p["permissions"] = ps
	case "offline":
		method, p["offline"] = "Emulation.setOffline", val == "true" || val == "1"
	case "colorscheme", "media":
		method, p["colorScheme"] = "Emulation.setColorScheme", val
	case "headers":
		method = "Network.setExtraHTTPHeaders"
		var h map[string]any
		if err := json.Unmarshal([]byte(val), &h); err != nil {
			return errResult("headers value: a JSON object")
		}
		p["headers"] = h
	default:
		return errResult("kind: viewport|userAgent|geolocation|permissions|offline|colorScheme|headers")
	}
	raw, err := s.call(method, p)
	if err != nil {
		return errResult(err.Error())
	}
	return textResult(s.format("browser_emulate", raw, nil))
}

// batchResult is the step report; the observation becomes the baseline of later
// deltas, as an explicit snapshot would.
func (s *Server) batchResult(raw json.RawMessage, args map[string]any) map[string]any {
	txt, ok := rpc.FormatBatch(raw, 4000)
	var r struct {
		Obs string `json:"observation"`
	}
	json.Unmarshal(raw, &r)
	if obs, _ := args["observe"].(string); r.Obs != "" && (obs == "interactive" || obs == "full") {
		id, u := s.tabKey(args)
		opts := map[string]any{}
		if obs == "interactive" {
			opts["interactiveOnly"] = true
		}
		s.snaps[id] = &snapState{opts: opts, yaml: r.Obs, url: u}
	}
	res := textResult(page(txt, args))
	if !ok {
		res["isError"] = true
	}
	return res
}
