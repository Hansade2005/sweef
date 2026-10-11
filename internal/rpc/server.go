// Package rpc exposes the engine over JSON-RPC. It powers:
//
//   - `sweef serve`  — TCP/Unix socket server for SDKs (Node & Python)
//   - stdio loop       — used by the MCP bridge and `sweef call <method>`
//
// Method names mirror CDP namespaces (Page.*, DOM.*, Network.*, Runtime.*)
// so the shim in internal/cdp can translate real DevTools traffic onto these,
// and so Playwright-style SDKs map 1:1 onto our verbs.
package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/sweefbrowser/sweef/internal/engine"
	"github.com/sweefbrowser/sweef/internal/protocol"
)

type Server struct {
	eng *engine.Engine
}

func NewServer(e *engine.Engine) *Server { return &Server{eng: e} }

// ---- dispatch --------------------------------------------------------------

type Params = map[string]any

func str(p Params, k string) string { v, _ := p[k].(string); return v }
func num(p Params, k string) float64 {
	if v, ok := p[k].(float64); ok {
		return v
	}
	return 0
}
func strOr(p Params, k, def string) string {
	if v, ok := p[k].(string); ok && v != "" {
		return v
	}
	return def
}

func (s *Server) tabID(p Params) (string, error) {
	id := str(p, "tab")
	if id == "" {
		t, err := s.eng.ActiveTab()
		if err != nil {
			return "", err
		}
		return t.ID, nil
	}
	return id, nil
}

func (s *Server) tab(p Params) (*engine.Tab, error) {
	id, err := s.tabID(p)
	if err != nil {
		return nil, err
	}
	return s.eng.Tab(id)
}

// Call executes one method and returns a JSON result or an RpcError.
func (s *Server) Call(method string, raw json.RawMessage) (json.RawMessage, *protocol.RpcError) {
	var p Params
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, badReq("params must be an object")
		}
	} else {
		p = Params{}
	}
	res, err := s.dispatch(method, p)
	if err != nil {
		return nil, toRpc(err)
	}
	b, merr := json.Marshal(res)
	if merr != nil {
		return nil, internal(merr.Error())
	}
	return b, nil
}

func (s *Server) dispatch(method string, p Params) (any, error) {
	switch method {
	// ---- Browser / lifecycle ----
	case "Browser.getVersion":
		return map[string]any{"product": "Sweef/0.1", "userAgent": s.eng.Opts().DefaultUserAgent,
			"jsRuntime": "quickjs-es2020", "memoryBudgetMiB": s.eng.Opts().MaxRSSMiB}, nil
	case "Browser.newTab", "Target.createTarget":
		t, err := s.eng.NewTab(str(p, "url"))
		if err != nil {
			return nil, err
		}
		return map[string]any{"tab": t.ID, "url": t.URL(), "title": t.Title()}, nil
	case "Browser.closeTab", "Target.closeTarget":
		return map[string]bool{"ok": true}, s.eng.CloseTab(str(p, "tab"))
	case "Browser.listTabs":
		type tb struct {
			ID    string `json:"id"`
			URL   string `json:"url"`
			Title string `json:"title"`
		}
		out := []tb{}
		for _, t := range s.eng.ListTabs() {
			out = append(out, tb{t.ID, t.URL(), t.Title()})
		}
		return out, nil
	case "Browser.stats":
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		return map[string]any{
			"rssMiB":     ms.Sys >> 20,
			"heapMiB":    ms.HeapAlloc >> 20,
			"goroutines": runtime.NumGoroutine(),
			"tabs":       len(s.eng.ListTabs()),
		}, nil

	// ---- Navigation ----
	case "Page.navigate", "Page.goto":
		t, err := s.tab(p)
		if err != nil {
			return nil, err
		}
		hdrs := map[string]string{}
		if hm, ok := p["headers"].(map[string]any); ok {
			for k, v := range hm {
				hdrs[k] = fmt.Sprint(v)
			}
		}
		if err := t.Navigate(str(p, "url"), hdrs); err != nil {
			return nil, err
		}
		return map[string]any{"url": t.URL(), "title": t.Title()}, nil
	case "Page.goBack":
		t, err := s.tab(p)
		if err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, t.GoBack()
	case "Page.goForward":
		t, err := s.tab(p)
		if err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, t.GoForward()
	case "Page.reload":
		t, err := s.tab(p)
		if err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, t.Navigate(t.URL(), nil)
	case "Page.title":
		t, err := s.tab(p)
		if err != nil {
			return nil, err
		}
		return map[string]any{"title": t.Title()}, nil
	case "Page.url":
		t, err := s.tab(p)
		if err != nil {
			return nil, err
		}
		return map[string]any{"url": t.URL()}, nil

	// ---- Content extraction ----
	case "Page.content", "Page.getHTML":
		t, err := s.tab(p)
		if err != nil {
			return nil, err
		}
		return map[string]any{"html": t.HTML(true)}, nil
	case "Page.textContent":
		t, err := s.tab(p)
		if err != nil {
			return nil, err
		}
		txt, err := t.Text(strOr(p, "selector", "body"))
		return map[string]any{"text": txt}, err
	case "Page.snapshot": // aria-style tree for LLM consumption
		t, err := s.tab(p)
		if err != nil {
			return nil, err
		}
		return t.Snapshot()
	case "Page.screenshot": // structured text render; PNG when headful build
		t, err := s.tab(p)
		if err != nil {
			return nil, err
		}
		w := int(num(p, "width"))
		return map[string]any{"text": t.Screenshot(w)}, nil

	// ---- DOM queries ----
	case "DOM.querySelector":
		t, err := s.tab(p)
		if err != nil {
			return nil, err
		}
		return t.QuerySelector(str(p, "selector"))
	case "DOM.querySelectorAll":
		t, err := s.tab(p)
		if err != nil {
			return nil, err
		}
		return t.QuerySelectorAll(str(p, "selector"))

	// ---- Interaction (Playwright-equivalent actions) ----
	case "Input.click", "Page.click":
		t, err := s.tab(p)
		if err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, t.Click(str(p, "selector"))
	case "Input.fill", "Page.fill":
		t, err := s.tab(p)
		if err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, t.Fill(str(p, "selector"), str(p, "value"))
	case "Input.press", "Page.press":
		t, err := s.tab(p)
		if err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, t.Press(str(p, "selector"), strOr(p, "key", "Enter"))

	// ---- JS ----
	case "Runtime.evaluate":
		t, err := s.tab(p)
		if err != nil {
			return nil, err
		}
		val, err := t.Evaluate(str(p, "expression"))
		if err != nil {
			return nil, err
		}
		return val, nil

	// ---- Logs (console / network / state) with cursor pagination ----
	case "Log.getConsole":
		t, err := s.tab(p)
		if err != nil {
			return nil, err
		}
		return t.ConsoleLog(int64(num(p, "since"))), nil
	case "Network.getLogs":
		t, err := s.tab(p)
		if err != nil {
			return nil, err
		}
		return t.NetworkLog(int64(num(p, "since"))), nil
	case "Page.getStateLog":
		t, err := s.tab(p)
		if err != nil {
			return nil, err
		}
		return t.StateLog(int64(num(p, "since"))), nil
	case "Network.har":
		t, err := s.tab(p)
		if err != nil {
			return nil, err
		}
		har, err := t.HAR()
		return json.RawMessage(har), err

	// ---- Interception ----
	case "Network.route":
		t, err := s.tab(p)
		if err != nil {
			return nil, err
		}
		status := int(num(p, "status"))
		body := str(p, "body")
		abort := str(p, "action") == "abort"
		t.Route(str(p, "pattern"), func(req *http.Request) (int, string, bool) {
			if abort {
				return http.StatusServiceUnavailable, "", true
			}
			return status, body, true
		})
		return map[string]bool{"ok": true}, nil
	case "Network.unrouteAll":
		t, err := s.tab(p)
		if err != nil {
			return nil, err
		}
		t.UnrouteAll()
		return map[string]bool{"ok": true}, nil

	default:
		return nil, fmt.Errorf("unknown method %q", method)
	}
}

func badReq(msg string) *protocol.RpcError {
	return &protocol.RpcError{Code: -32600, Message: msg}
}
func internal(msg string) *protocol.RpcError {
	return &protocol.RpcError{Code: -32603, Message: msg}
}
func toRpc(err error) *protocol.RpcError {
	switch {
	case errors.Is(err, engine.ErrNoTab):
		return &protocol.RpcError{Code: -32001, Message: err.Error()}
	case errors.Is(err, engine.ErrNotFound):
		return &protocol.RpcError{Code: -32004, Message: err.Error()}
	default:
		msg := err.Error()
		code := -32000
		if strings.Contains(msg, "no node matches") || strings.Contains(msg, "vanished") {
			code = -32004
		}
		return &protocol.RpcError{Code: code, Message: msg}
	}
}

// ---- serving ----------------------------------------------------------------

// ServePipe speaks framed protocol over an arbitrary ReadWriteCloser.
// Used for stdio (`sweef mcp`, `sweef call`) and sockets alike.
func (s *Server) ServePipe(ctx context.Context, rw io.ReadWriteCloser, busEvents bool) error {
	conn := protocol.NewConn(rw)
	defer conn.Close()

	var wg sync.WaitGroup
	defer wg.Wait()

	if busEvents {
		id, ch := s.eng.Bus().Subscribe(128)
		defer s.eng.Bus().Unsubscribe(id)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case <-conn.Done():
					return
				case ev, ok := <-ch:
					if !ok {
						return
					}
					data, _ := json.Marshal(ev)
					conn.Write(&protocol.Message{JSONRPC: "2.0", Method: "event." + string(ev.Kind), Result: data})
				}
			}
		}()
	}

	buf := make([]byte, 4096)
	for {
		msg, nb, err := conn.Read(buf)
		buf = nb
		if err != nil {
			if errors.Is(err, io.EOF) || ctx.Err() != nil {
				return nil
			}
			return err
		}
		if msg.Method == "" {
			continue // clients don't send responses on this channel
		}
		if msg.Method == "conn.close" {
			return nil
		}
		res, rerr := s.Call(msg.Method, msg.Params)
		reply := &protocol.Message{JSONRPC: "2.0", ID: msg.ID}
		if rerr != nil {
			reply.Error = rerr
		} else {
			reply.Result = res
		}
		if err := conn.Write(reply); err != nil {
			return err
		}
	}
}

// ServeTCP listens for SDK connections. One goroutine per client; each is a
// few KB of stack until it starts working — fine even at hundreds of agents.
func (s *Server) ServeTCP(ctx context.Context, addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	defer ln.Close()
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if tc, ok := c.(*net.TCPConn); ok {
			tc.SetDeadline(time.Time{}) // long-lived agent sessions: no timeouts
		}
		go s.ServePipe(ctx, c, true)
	}
}
