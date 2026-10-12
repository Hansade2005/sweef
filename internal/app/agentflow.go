package app

import (
	"fmt"
	"strings"

	"github.com/sweefbrowser/sweef/internal/app/state"
	"github.com/sweefbrowser/sweef/internal/app/store"
)

// agentRunState is the app's view of the current run, shown as state.agent.state
// and used to drive the overlay.
type agentRunState struct {
	state string // idle | working | waiting | paused | error
	mode  string
	task  string
	step  string
	run   string
}

// agentRequest is the payload of agent.send (docs/gui-contract.md).
type agentRequest struct {
	Text    string `json:"text"`
	Mode    string `json:"mode"`
	Chat    string `json:"chat"`
	Source  string `json:"source"`
	Sources struct {
		Page *bool `json:"page"`
		Web  *bool `json:"web"`
	} `json:"sources"`
}

// agentState builds state.agent.
func (a *App) agentState(act state.Tab, hasAct bool) map[string]any {
	out := map[string]any{"state": "off", "sites": a.agentSites()}
	if a.agentRaw == nil {
		out["hasKey"] = false
		out["pageVisibility"] = "ask"
		return out
	}
	st := a.agentRun.state
	if st == "" {
		st = "idle"
	}
	out["state"] = st
	out["hasKey"] = true
	if in, ok := a.agentRaw.(AgentInfoer); ok {
		info := in.Info()
		out["hasKey"] = info.HasKey
		out["model"] = info.Model
		chats := make([]map[string]any, 0, len(info.Chats))
		for _, c := range info.Chats {
			chats = append(chats, map[string]any{"id": c.ID, "title": c.Title, "when": c.When})
		}
		out["chats"] = chats
		out["chat"] = map[string]any{"title": info.Title}
	}
	vis := "ask"
	if hasAct && strings.HasPrefix(act.URL, "http") {
		vis = string(a.prof.Sites.Agent(act.URL))
	}
	out["pageVisibility"] = vis
	return out
}

func (a *App) agentSites() []map[string]any {
	out := []map[string]any{}
	for origin, r := range a.prof.Sites.All() {
		switch r.Agent {
		case store.VisAllowed:
			out = append(out, map[string]any{"origin": origin, "allowed": true})
		case store.VisBlocked:
			out = append(out, map[string]any{"origin": origin, "allowed": false})
		}
	}
	return out
}

// agentSend starts an agent run from the sidebar, the omnibox or the new tab.
func (a *App) agentSend(req agentRequest) {
	text := strings.TrimSpace(req.Text)
	if text == "" {
		return
	}
	if a.agent == nil {
		a.broadcast(mAgentEvent, AgentEvent{RunID: "none", Kind: "error", Text: "No agent is configured. Add an API key in Settings.",
			Data: map[string]any{"code": "auth", "message": "No agent is configured. Add an API key in Settings."}})
		return
	}
	mode := req.Mode
	if mode == "" {
		mode = a.prof.Settings.Str("agent.mode")
	}
	if mode == "" {
		mode = "ask"
	}
	if c, ok := a.agentRaw.(AgentChats); ok && req.Chat == "" && a.agentRun.run == "" {
		_ = c // a new conversation is started by agent.chat.new, not implied by a send
	}
	id := a.model.Active(a.winID)
	st, ok := a.model.Tab(id)
	pageOn := req.Sources.Page == nil || *req.Sources.Page
	if !pageOn || !ok || st.URL == "" || !strings.HasPrefix(st.URL, "http") {
		a.runAgent(req, mode, PageContext{})
		return
	}
	origin := store.OriginOf(st.URL)
	switch a.prof.Sites.Agent(origin) {
	case store.VisBlocked:
		a.runAgent(req, mode, PageContext{Blocked: true})
	case store.VisAsk:
		a.runSeq++
		run := fmt.Sprintf("ask-%d", a.runSeq)
		a.pendingAsk[origin] = pendingAgent{req: req, mode: mode, tab: id}
		a.agentRun.state = "waiting"
		a.markState()
		a.broadcast(mAgentEvent, AgentEvent{RunID: run, Kind: "confirm", Text: "Let the agent read " + origin + "?",
			Data: map[string]any{"id": "visibility-" + origin, "title": "Let Sweef read " + origin + "?", "risk": "low",
				"origin": origin, "tab": id, "action": map[string]any{"type": "visibility", "origin": origin}}})
	default:
		a.runWithPage(id, req, mode)
	}
}

// runWithPage reads the (allowed) page text and starts the run.
func (a *App) runWithPage(tabID string, req agentRequest, mode string) {
	e := a.page(tabID)
	st, _ := a.model.Tab(tabID)
	if e == nil {
		a.runAgent(req, mode, PageContext{})
		return
	}
	origin := store.OriginOf(st.URL)
	e.w.do(func() {
		body, _ := e.t.Text("body")
		if len(body) > 200_000 {
			body = body[:200_000]
		}
		pc := PageContext{TabID: tabID, URL: st.URL, Title: st.Title, Origin: origin, Text: body}
		a.post(func() {
			// re-check: the user may have blocked the site while we read it
			if a.prof.Sites.Agent(origin) != store.VisAllowed {
				pc = PageContext{Blocked: true}
			}
			a.runAgent(req, mode, pc)
		})
	})
}

func (a *App) runAgent(req agentRequest, mode string, pc PageContext) {
	a.agentRun = agentRunState{state: "working", mode: mode, task: req.Text}
	a.markState()
	if mode == "agent" {
		a.setOverlay("acting", req.Text, "")
	}
	ch := a.agent.Send(req.Text, mode, pc)
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		for {
			select {
			case ev, ok := <-ch:
				if !ok {
					return
				}
				a.post(func() { a.onAgentEvent(ev) })
			case <-a.quit:
				return
			}
		}
	}()
}

// onAgentEvent forwards an event to the UI and tracks the run state.
func (a *App) onAgentEvent(ev AgentEvent) {
	r := &a.agentRun
	r.run = ev.RunID
	switch ev.Kind {
	case "status":
		if s, _ := ev.Data["state"].(string); s != "" {
			switch s {
			case "idle", "working", "waiting", "paused", "error":
				r.state = s
			}
		}
	case "action":
		if l, _ := ev.Data["label"].(string); l != "" {
			r.step = l
			if r.mode == "agent" && a.ovl.state == "acting" {
				a.setOverlay("acting", r.task, r.step)
			}
		}
		if r.state == "idle" {
			r.state = "working"
		}
	case "confirm":
		r.state = "waiting"
	case "done":
		r.state = "idle"
		if r.mode == "agent" && a.ovl.visible() {
			a.setOverlay("done", r.task, "")
		}
	case "error":
		r.state = "error"
		if a.ovl.visible() {
			a.setOverlay("off", "", "")
		}
	default:
		if r.state == "idle" || r.state == "" {
			r.state = "working"
		}
	}
	a.markState()
	a.broadcast(mAgentEvent, ev)
}

// agentStop, agentTakeControl and agentResume are the sidebar / overlay buttons.
func (a *App) agentStop() {
	if a.agent != nil {
		a.agent.Stop()
	}
	if a.agentRun.mode == "agent" && a.ovl.visible() {
		a.setOverlay("stopped", a.agentRun.task, "")
	}
	a.agentRun.state = "idle"
	a.markState()
}

func (a *App) agentTakeControl() {
	if a.agent == nil {
		return
	}
	a.agent.TakeControl()
	a.agentRun.state = "paused"
	if a.ovl.visible() {
		a.setOverlay("control", a.agentRun.task, "")
	}
	a.markState()
}

func (a *App) agentResume() {
	if r, ok := a.agentRaw.(AgentResumer); ok {
		r.Resume()
	}
	a.agentRun.state = "working"
	if a.agentRun.mode == "agent" {
		a.setOverlay("acting", a.agentRun.task, a.agentRun.step)
	}
	a.markState()
}

// setAgentVisibility records the user's answer (allowed true/false, nil =
// forget the rule) and resumes a pending run.
func (a *App) setAgentVisibility(origin string, allowed *bool) {
	if origin == "" {
		return
	}
	if allowed == nil {
		a.prof.Sites.SetAgent(origin, "")
		a.markState()
		return
	}
	v := store.VisBlocked
	if *allowed {
		v = store.VisAllowed
	}
	a.prof.Sites.SetAgent(origin, v)
	if p, ok := a.pendingAsk[origin]; ok {
		delete(a.pendingAsk, origin)
		if *allowed {
			a.runWithPage(p.tab, p.req, p.mode)
		} else if a.agent != nil {
			a.runAgent(p.req, p.mode, PageContext{Blocked: true})
		}
	}
	a.markState()
}
