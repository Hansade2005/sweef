package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/sweefbrowser/sweef/internal/engine"
	"github.com/sweefbrowser/sweef/internal/rpc"
)

// RPCBackend implements Backend over an in-process engine. Browser actions
// are executed by rpc.Server's Batch.run (the same verbs as `sweef batch` and
// the MCP browser_batch tool), snapshots by Page.ariaSnapshot; no engine
// logic is duplicated here.
type RPCBackend struct {
	eng *engine.Engine
	srv *rpc.Server

	// ConfirmFunc answers Confirm. The zero value denies every request.
	ConfirmFunc func(context.Context, ConfirmRequest) (bool, error)

	mu     sync.Mutex
	active string
	base   map[string]string // delta baselines for Snapshot(delta=true)

	stopPump context.CancelFunc
}

// NewRPCBackend wraps e. The engine's existing tabs are visible to the agent.
func NewRPCBackend(e *engine.Engine) *RPCBackend {
	return &RPCBackend{eng: e, srv: rpc.NewServer(e), base: map[string]string{}}
}

// StartBackgroundPump keeps the pages running between calls for a while (rpc.Server.BackgroundPump), as the
// CLI's daemon does: what a page still does after a load or an action (hydrating, rendering a route, answers
// arriving) progresses while the model thinks instead of in the settle of the next action. For a headless
// engine only (sweef agent): a window drives its own tabs. Close stops it.
func (b *RPCBackend) StartBackgroundPump() {
	ctx, cancel := context.WithCancel(context.Background())
	b.stopPump = cancel
	b.srv.BackgroundPump = true
	b.srv.StartBackgroundPump(ctx)
}

// Close stops the background pump (the engine is the caller's to close).
func (b *RPCBackend) Close() {
	if b.stopPump != nil {
		b.stopPump()
	}
}

var _ Backend = (*RPCBackend)(nil)

func (b *RPCBackend) call(method string, params map[string]any, out any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	res, rerr := b.srv.Call(method, raw)
	if rerr != nil { // *RpcError: avoid the typed-nil-in-interface trap
		return errors.New(rerr.Message)
	}
	if out != nil {
		return json.Unmarshal(res, out)
	}
	return nil
}

func (b *RPCBackend) activeID() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.active != "" {
		if _, err := b.eng.Tab(b.active); err == nil {
			return b.active
		}
	}
	if t, err := b.eng.ActiveTab(); err == nil {
		b.active = t.ID
		return t.ID
	}
	return ""
}

func (b *RPCBackend) resolve(id string) string {
	if id != "" {
		return id
	}
	return b.activeID()
}

// Snapshot implements Backend.
func (b *RPCBackend) Snapshot(tabID string, delta bool) (string, error) {
	id := b.resolve(tabID)
	if id == "" {
		return "", errors.New("no tab")
	}
	var out struct {
		YAML string `json:"yaml"`
	}
	if err := b.call("Page.ariaSnapshot", map[string]any{"tab": id}, &out); err != nil {
		return "", err
	}
	b.mu.Lock()
	prev, had := b.base[id]
	b.base[id] = out.YAML
	b.mu.Unlock()
	if delta && had {
		return DeltaText(prev, out.YAML), nil
	}
	return out.YAML, nil
}

type batchStepJSON struct {
	Cmd      string   `json:"cmd"`
	Args     []string `json:"args,omitempty"`
	Optional bool     `json:"optional,omitempty"`
}

func normRef(r string) string {
	r = strings.TrimSpace(r)
	r = strings.TrimPrefix(r, "ref=")
	r = strings.TrimPrefix(r, "@")
	return r
}

// toBatch maps an agent Step to one or two rpc batch steps.
func toBatch(s Step) ([]batchStepJSON, error) {
	opt := s.Optional
	one := func(cmd string, args ...string) ([]batchStepJSON, error) {
		return []batchStepJSON{{Cmd: cmd, Args: args, Optional: opt}}, nil
	}
	switch s.Op {
	case OpNavigate:
		return one("open", s.URL)
	case OpClick:
		return one("click", normRef(s.Ref))
	case OpHover:
		return one("hover", normRef(s.Ref))
	case OpType:
		verb := "fill"
		if s.Append {
			verb = "type"
		}
		steps, _ := one(verb, normRef(s.Ref), s.Text)
		if s.Submit {
			steps = append(steps, batchStepJSON{Cmd: "press", Args: []string{"Enter", normRef(s.Ref)}, Optional: opt})
		}
		return steps, nil
	case OpPress:
		if s.Ref != "" {
			return one("press", s.Key, normRef(s.Ref))
		}
		return one("press", s.Key)
	case OpScroll:
		dir := s.Dir
		if dir == "" {
			dir = "down"
		}
		amt := s.Amount
		if amt <= 0 {
			amt = 600
		}
		return one("scroll", dir, strconv.Itoa(amt))
	case OpWait:
		switch {
		case s.Text != "":
			return one("wait", "text", s.Text)
		case s.URL != "":
			return one("wait", "url", s.URL)
		default:
			ms := s.Ms
			if ms <= 0 {
				ms = 500
			}
			if ms > 10000 {
				ms = 10000
			}
			return one("wait", strconv.Itoa(ms))
		}
	case OpSelect:
		return one("select", normRef(s.Ref), s.Value)
	case OpBack:
		return one("back")
	case OpForward:
		return one("forward")
	case OpExtract:
		sel := "body"
		if s.Ref != "" {
			sel = normRef(s.Ref)
		}
		return one("get", "text", sel)
	case OpExpect:
		switch s.Kind {
		case "text", "url", "title":
			return one("expect", s.Kind, s.Value)
		case "visible", "hidden":
			return one("expect", s.Kind, normRef(s.Ref))
		case "count", "value":
			return one("expect", s.Kind, normRef(s.Ref), s.Value)
		}
		return nil, fmt.Errorf("unknown expect kind %q", s.Kind)
	}
	return nil, fmt.Errorf("unsupported op %q", s.Op)
}

// Act implements Backend by running the steps as one Batch.run.
func (b *RPCBackend) Act(ctx context.Context, steps []Step) (Result, error) {
	if len(steps) == 0 {
		return Result{OK: true}, nil
	}
	tab := b.resolve(steps[0].Tab)
	var batch []batchStepJSON
	var owner []int // batch index -> agent step index
	for i, s := range steps {
		bs, err := toBatch(s)
		if err != nil {
			return Result{}, fmt.Errorf("step %d: %w", i+1, err)
		}
		for range bs {
			owner = append(owner, i)
		}
		batch = append(batch, bs...)
	}
	params := map[string]any{"steps": batch, "stopOnError": true, "observe": "none"}
	if tab != "" {
		params["tab"] = tab
	}
	type out struct {
		raw struct {
			OK      bool   `json:"ok"`
			URL     string `json:"url"`
			Title   string `json:"title"`
			TotalMs int64  `json:"totalMs"`
			Steps   []struct {
				Step   int             `json:"step"`
				OK     bool            `json:"ok"`
				Ms     int64           `json:"ms"`
				Result json.RawMessage `json:"result"`
				Error  string          `json:"error"`
			} `json:"steps"`
		}
		err error
	}
	ch := make(chan out, 1)
	go func() { // Batch.run is not interruptible; do not block the caller on it
		var o out
		o.err = b.call("Batch.run", params, &o.raw)
		ch <- o
	}()
	var o out
	select {
	case o = <-ch:
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
	if o.err != nil {
		return Result{}, o.err
	}
	res := Result{URL: o.raw.URL, Title: o.raw.Title, TotalMs: o.raw.TotalMs}
	res.Steps = make([]StepResult, len(steps))
	for i, s := range steps {
		res.Steps[i] = StepResult{Index: i + 1, Op: s.Op, Error: "not run (an earlier step failed)"}
	}
	okAll := true
	// fold batch results back onto agent steps (a step is ok iff all its parts are)
	seen := make([]int, len(steps))
	for bi, r := range o.raw.Steps {
		if bi >= len(owner) {
			break
		}
		si := owner[bi]
		sr := &res.Steps[si]
		if seen[si] == 0 {
			sr.Error = ""
			sr.OK = true
		}
		seen[si]++
		sr.Ms += r.Ms
		if !r.OK {
			sr.OK = false
			sr.Error = r.Error
		} else if steps[si].Op == OpExtract {
			sr.Output = resultText(r.Result)
		}
	}
	for i := range res.Steps {
		if !res.Steps[i].OK && !steps[i].Optional {
			okAll = false
		}
	}
	res.OK = okAll
	return res, nil
}

// resultText flattens a batch step result (string, {"text":..}, other JSON).
func resultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) == nil {
		for _, k := range []string{"text", "value", "result"} {
			if v, ok := m[k].(string); ok {
				return v
			}
		}
	}
	return string(raw)
}

// Tabs implements Backend.
func (b *RPCBackend) Tabs() []TabInfo {
	act := b.activeID()
	var out []TabInfo
	for _, t := range b.eng.ListTabs() {
		out = append(out, TabInfo{ID: t.ID, URL: t.URL(), Title: t.Title(), Active: t.ID == act})
	}
	return out
}

// OpenTab implements Backend. Like the engine, it returns the usable tab
// together with the navigation error if the load failed.
func (b *RPCBackend) OpenTab(url string) (TabInfo, error) {
	t, err := b.eng.NewTab(url)
	if t == nil {
		return TabInfo{}, err
	}
	b.mu.Lock()
	b.active = t.ID
	b.mu.Unlock()
	return TabInfo{ID: t.ID, URL: t.URL(), Title: t.Title(), Active: true}, err
}

// SwitchTab implements Backend.
func (b *RPCBackend) SwitchTab(id string) error {
	if _, err := b.eng.Tab(id); err != nil {
		return err
	}
	b.mu.Lock()
	b.active = id
	b.mu.Unlock()
	return nil
}

// CloseTab implements Backend.
func (b *RPCBackend) CloseTab(id string) error {
	if err := b.eng.CloseTab(id); err != nil {
		return err
	}
	b.mu.Lock()
	delete(b.base, id)
	if b.active == id {
		b.active = ""
	}
	b.mu.Unlock()
	return nil
}

// URL implements Backend.
func (b *RPCBackend) URL(tabID string) string {
	if t, err := b.eng.Tab(b.resolve(tabID)); err == nil {
		return t.URL()
	}
	return ""
}

// Title implements Backend.
func (b *RPCBackend) Title(tabID string) string {
	if t, err := b.eng.Tab(b.resolve(tabID)); err == nil {
		return t.Title()
	}
	return ""
}

// Screenshot implements Backend (PNG bytes).
func (b *RPCBackend) Screenshot(tabID string) ([]byte, error) {
	var out struct {
		PNG string `json:"pngBase64"`
	}
	if err := b.call("Page.screenshot", map[string]any{"tab": b.resolve(tabID), "type": "png"}, &out); err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(out.PNG)
}

// Confirm implements Backend.
func (b *RPCBackend) Confirm(ctx context.Context, req ConfirmRequest) (bool, error) {
	if b.ConfirmFunc == nil {
		return false, nil
	}
	return b.ConfirmFunc(ctx, req)
}
