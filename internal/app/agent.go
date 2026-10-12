package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/sweefbrowser/sweef/internal/app/store"
)

// AgentEvent is one streamed item from the agent. Kind is one of
// "text", "thought", "action", "status", "done", "error", "confirm" (and
// "sources"). The app forwards it to the chrome page as `agent.event` with Data
// merged into the top level, which is the shape the sidebar reads:
//
//	text {text, full?}  thought {text, done?}  action {id,label,detail,status,ms}
//	status {state,text}  confirm {id,title,risk,action}  sources {items}
//	done {ttft,tps,ms,sources}  error {code,message,retryAfter}
type AgentEvent struct {
	RunID string         `json:"runId"`
	Kind  string         `json:"kind"`
	Text  string         `json:"text,omitempty"`
	Data  map[string]any `json:"data,omitempty"`
}

// MarshalJSON flattens Data into the event object. Data cannot overwrite
// runId or kind.
func (e AgentEvent) MarshalJSON() ([]byte, error) {
	m := make(map[string]any, len(e.Data)+3)
	for k, v := range e.Data {
		m[k] = v
	}
	m["runId"], m["kind"] = e.RunID, e.Kind
	if e.Text != "" {
		m["text"] = e.Text
	}
	return json.Marshal(m)
}

// AgentInfo is what the settings page and sidebar show about the agent.
type AgentInfo struct {
	HasKey bool        `json:"hasKey"`
	Model  string      `json:"model,omitempty"`
	Chats  []AgentChat `json:"chats,omitempty"`
	Title  string      `json:"title,omitempty"` // current chat
}

// AgentChat is one entry of the chat list.
type AgentChat struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	When  string `json:"when,omitempty"`
}

// Optional capabilities a host may implement on top of AgentHost. The app
// checks for each with a type assertion, so a minimal host keeps working.
type (
	// AgentInfoer reports key/model/chat state for the UI.
	AgentInfoer interface{ Info() AgentInfo }
	// AgentResumer continues after the user handed control back.
	AgentResumer interface{ Resume() }
	// AgentConfirmer answers a `confirm` event.
	AgentConfirmer interface {
		Confirm(runID, id string, approved bool)
	}
	// AgentKeyClearer forgets the stored API key.
	AgentKeyClearer interface{ ClearKey() }
	// AgentChats manages conversations.
	AgentChats interface {
		NewChat()
		// OpenChat returns the chat's title and messages ({role, text}).
		OpenChat(id string) (title string, messages []map[string]any, ok bool)
	}
)

// PageContext is what the agent may know about the page the user is on.
// When Blocked is true every other field is empty: the agent is never handed
// content (or even the URL) of an origin the user blocked.
type PageContext struct {
	TabID   string `json:"tabId,omitempty"`
	URL     string `json:"url,omitempty"`
	Title   string `json:"title,omitempty"`
	Origin  string `json:"origin,omitempty"`
	Text    string `json:"text,omitempty"`
	Blocked bool   `json:"blocked,omitempty"`
}

// AgentHost is what internal/app needs from an agent implementation. The PM
// connects internal/agent to it later; this package never imports it.
type AgentHost interface {
	// Send starts a run. The channel is closed when the run ends.
	Send(text, mode string, page PageContext) <-chan AgentEvent
	Stop()
	TakeControl()
	SetKey(string)
}

// ErrOriginBlocked is returned when the agent asks for a blocked origin.
var ErrOriginBlocked = errors.New("app: the user blocked the agent from this site")

// ErrNeedsApproval is returned for origins whose visibility is "ask".
var ErrNeedsApproval = errors.New("app: this site needs the user's approval before the agent can see it")

// Gate wraps an AgentHost and enforces the per-origin visibility rule at the
// boundary, even if a caller builds a PageContext by hand: content for a
// blocked origin is replaced by the Blocked stub before the inner host sees
// it, and "ask" origins are treated as blocked until approved.
type Gate struct {
	Inner AgentHost
	Vis   func(originOrURL string) store.Visibility
}

func (g *Gate) filter(pc PageContext) PageContext {
	if pc.Blocked {
		return PageContext{Blocked: true}
	}
	if pc.URL == "" && pc.Origin == "" {
		return pc
	}
	o := pc.Origin
	if o == "" {
		o = store.OriginOf(pc.URL)
	}
	if v := g.Vis(o); v != store.VisAllowed {
		return PageContext{Blocked: true}
	}
	return pc
}

func (g *Gate) Send(text, mode string, pc PageContext) <-chan AgentEvent {
	return g.Inner.Send(text, mode, g.filter(pc))
}
func (g *Gate) Stop()           { g.Inner.Stop() }
func (g *Gate) TakeControl()    { g.Inner.TakeControl() }
func (g *Gate) SetKey(k string) { g.Inner.SetKey(k) }

// ---- fake agent --------------------------------------------------------------

// FakeAgent is a scripted AgentHost for tests and UI previews.
type FakeAgent struct {
	mu      sync.Mutex
	Script  []AgentEvent
	Delay   time.Duration
	Calls   []FakeCall
	Stops   int
	Takes   int
	Key     string
	nextRun int
}

// FakeCall records one Send.
type FakeCall struct {
	Text, Mode string
	Page       PageContext
}

// NewFakeAgent returns an agent that streams a short canned answer that
// echoes what it was allowed to see.
func NewFakeAgent() *FakeAgent { return &FakeAgent{Delay: 5 * time.Millisecond} }

func (f *FakeAgent) Send(text, mode string, pc PageContext) <-chan AgentEvent {
	f.mu.Lock()
	f.Calls = append(f.Calls, FakeCall{text, mode, pc})
	f.nextRun++
	run := fmt.Sprintf("run-%d", f.nextRun)
	script := f.Script
	delay := f.Delay
	f.mu.Unlock()
	if script == nil {
		seen := "no page"
		switch {
		case pc.Blocked:
			seen = "a page I am not allowed to read"
		case pc.URL != "":
			seen = pc.Title + " (" + pc.URL + ")"
		}
		script = []AgentEvent{
			{Kind: "status", Text: "thinking"},
			{Kind: "thought", Text: "Looking at " + seen},
			{Kind: "text", Text: "You asked: " + text + ". I can see " + seen + "."},
			{Kind: "done"},
		}
	}
	ch := make(chan AgentEvent, len(script))
	go func() {
		defer close(ch)
		for _, ev := range script {
			ev.RunID = run
			time.Sleep(delay)
			ch <- ev
		}
	}()
	return ch
}

func (f *FakeAgent) Info() AgentInfo { return AgentInfo{HasKey: true, Model: "fake-model"} }

func (f *FakeAgent) Stop()        { f.mu.Lock(); f.Stops++; f.mu.Unlock() }
func (f *FakeAgent) TakeControl() { f.mu.Lock(); f.Takes++; f.mu.Unlock() }
func (f *FakeAgent) SetKey(k string) {
	f.mu.Lock()
	f.Key = k
	f.mu.Unlock()
}

// LastCall returns the most recent Send.
func (f *FakeAgent) LastCall() (FakeCall, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.Calls) == 0 {
		return FakeCall{}, false
	}
	return f.Calls[len(f.Calls)-1], true
}

// ---- Backend adapter skeleton ----------------------------------------------------

// TabInfo is the agent's view of a tab. Blocked tabs expose only their id.
type TabInfo struct {
	ID      string `json:"id"`
	URL     string `json:"url,omitempty"`
	Title   string `json:"title,omitempty"`
	Active  bool   `json:"active"`
	Blocked bool   `json:"blocked,omitempty"`
}

// Backend is the adapter the PM connects to internal/agent's Backend
// interface (method names are intentionally close to the engine's). Every
// method enforces the per-origin visibility rule: a tab on a blocked or
// not-yet-approved origin can be listed (id only) but not read or driven.
type Backend struct{ A *App }

func (b Backend) guard(tabID string) (*etab, error) {
	st, ok := b.A.model.Tab(tabID)
	if !ok {
		return nil, errors.New("no such tab")
	}
	switch b.A.prof.Sites.Agent(st.URL) {
	case store.VisBlocked:
		return nil, ErrOriginBlocked
	case store.VisAsk:
		return nil, ErrNeedsApproval
	}
	b.A.mu.Lock()
	pt := b.A.pages[tabID]
	b.A.mu.Unlock()
	if pt == nil {
		return nil, errors.New("tab not loaded")
	}
	return pt, nil
}

// Tabs lists tabs, hiding everything but the id for non-allowed origins.
func (b Backend) Tabs() []TabInfo {
	act := b.A.model.Active(b.A.winID)
	var out []TabInfo
	for _, id := range b.A.model.Order(b.A.winID) {
		st, _ := b.A.model.Tab(id)
		ti := TabInfo{ID: id, Active: id == act}
		if b.A.prof.Sites.Agent(st.URL) == store.VisAllowed {
			ti.URL, ti.Title = st.URL, st.Title
		} else {
			ti.Blocked = true
		}
		out = append(out, ti)
	}
	return out
}

// ReadPage returns the visible text of an allowed tab.
func (b Backend) ReadPage(tabID string) (string, error) {
	pt, err := b.guard(tabID)
	if err != nil {
		return "", err
	}
	return pt.text()
}

// Navigate loads url in an allowed tab.
func (b Backend) Navigate(tabID, url string) error {
	if _, err := b.guard(tabID); err != nil {
		return err
	}
	b.A.post(func() { b.A.navigateInput(tabID, url) })
	return nil
}

// Click clicks a selector in an allowed tab.
func (b Backend) Click(tabID, selector string) error {
	pt, err := b.guard(tabID)
	if err != nil {
		return err
	}
	return pt.call(func() error { return pt.t.Click(selector) })
}

// Fill sets a form field value in an allowed tab.
func (b Backend) Fill(tabID, selector, value string) error {
	pt, err := b.guard(tabID)
	if err != nil {
		return err
	}
	return pt.call(func() error { return pt.t.Fill(selector, value) })
}

func (f *FakeAgent) callCount() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.Calls) }
func (f *FakeAgent) counters() (stops, takes int, key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Stops, f.Takes, f.Key
}
