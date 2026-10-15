// Package mcp implements a Model Context Protocol server (stdio transport,
// newline-delimited JSON-RPC 2.0) that exposes the Sweef engine as agent tools.
// This is what an AI client (Claude Desktop, Claude Code, Cursor, custom LLM
// loops) talks to: `sweef mcp` runs the engine in-process, so there is no
// browser process and no IPC hop between the protocol and the page.
//
// Tool outputs are shaped for LLM context windows (see format.go): plain text
// instead of JSON where the JSON adds nothing, compact log lines, truncation
// with a continuation offset, and after an action the snapshot lines that
// changed since the agent's last snapshot of the page.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"sync"

	"github.com/sweefbrowser/sweef/internal/engine"
	"github.com/sweefbrowser/sweef/internal/rpc"
)

const (
	PROTOCOL_VERSION = "2025-06-18"
	SERVER_NAME      = "sweef-browser"
	SERVER_VERSION   = "0.2.0"
)

// supportedVersions are the MCP revisions this server speaks; a client asking
// for one of them gets it back, anything else gets PROTOCOL_VERSION.
var supportedVersions = map[string]bool{"2025-06-18": true, "2025-03-26": true, "2024-11-05": true}

const instructions = "Sweef is a headless browser engine. Read a page with browser_snapshot (YAML accessibility tree); " +
	"controls carry [ref=eN] and that ref works as the selector of any action tool. After an action the reply " +
	"lists the snapshot lines that changed since your last snapshot. When you know several steps ahead, send them " +
	"in one browser_batch call."

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

// Server runs the MCP loop.
type Server struct {
	eng  *engine.Engine
	rpcS *rpc.Server
	w    io.Writer
	wmu  sync.Mutex

	// AutoDelta appends, after an action, the snapshot lines that changed since the
	// agent's last snapshot of that tab (SWEEF_MCP_AUTO_DELTA=0 turns it off).
	AutoDelta bool
	snaps     map[string]*snapState // tab id -> what the agent last saw
	toolsJSON []byte                // tools/list result, encoded once
}

func New(e *engine.Engine, w io.Writer) *Server {
	s := &Server{eng: e, rpcS: rpc.NewServer(e), w: w, snaps: map[string]*snapState{},
		AutoDelta: os.Getenv("SWEEF_MCP_AUTO_DELTA") != "0"}
	s.toolsJSON, _ = marshal(map[string]any{"tools": tools})
	return s
}

// marshal is json.Marshal without HTML escaping (<, > and & are everywhere in
// page content, and the escaped form of < costs six bytes).
func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil // ends in '\n', the message delimiter
}

func (s *Server) send(v any) error {
	b, err := marshal(v)
	if err != nil {
		return err
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, err = s.w.Write(b)
	return err
}

// Serve reads newline-delimited JSON-RPC until EOF or ctx ends.
func (s *Server) Serve(ctx context.Context, r io.Reader) error {
	// pages finish what they started (a fetch, a client-side route) while the model thinks
	s.rpcS.StartBackgroundPump(ctx)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		if ctx.Err() != nil {
			return nil
		}
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var req rpcReq
		if err := json.Unmarshal(line, &req); err != nil {
			s.send(rpcResp{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcErr{-32700, "parse error"}})
			continue
		}
		s.handle(&req)
	}
	return sc.Err()
}

func (s *Server) handle(req *rpcReq) {
	isNotification := len(req.ID) == 0 || string(req.ID) == "null"
	reply := func(result any, e *rpcErr) {
		if isNotification {
			return // JSON-RPC: notifications are never answered
		}
		s.send(rpcResp{JSONRPC: "2.0", ID: req.ID, Result: result, Error: e})
	}
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		json.Unmarshal(req.Params, &p)
		v := PROTOCOL_VERSION
		if supportedVersions[p.ProtocolVersion] {
			v = p.ProtocolVersion
		}
		reply(map[string]any{
			"protocolVersion": v,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]string{"name": SERVER_NAME, "version": SERVER_VERSION},
			"instructions":    instructions,
		}, nil)
	case "ping":
		reply(map[string]any{}, nil)
	case "tools/list":
		reply(json.RawMessage(bytes.TrimSpace(s.toolsJSON)), nil)
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil || p.Name == "" {
			reply(nil, &rpcErr{-32602, "bad params: want {name, arguments}"})
			return
		}
		if p.Arguments == nil {
			p.Arguments = map[string]any{}
		}
		reply(s.callTool(p.Name, p.Arguments), nil)
	case "resources/list":
		reply(map[string]any{"resources": []any{}}, nil)
	case "prompts/list":
		reply(map[string]any{"prompts": []any{}}, nil)
	default:
		if len(req.Method) > 14 && req.Method[:14] == "notifications/" {
			return
		}
		reply(nil, &rpcErr{-32601, "method not found: " + req.Method})
	}
}
