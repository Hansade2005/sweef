package app

import (
	"fmt"

	"github.com/sweefbrowser/sweef/internal/app/store"
)

// agentSend starts an agent run from the sidebar.
func (a *App) agentSend(text, mode string) {
	if text == "" {
		return
	}
	if a.agent == nil {
		a.sendChrome(mAgentEvent, AgentEvent{RunID: "none", Kind: "error", Text: "No agent is configured. Add an API key in Settings."})
		return
	}
	if mode == "" {
		mode = "ask"
	}
	id := a.model.Active(a.winID)
	st, ok := a.model.Tab(id)
	if !ok || st.URL == "" || st.URL == newTabURL || len(st.URL) > 6 && st.URL[:6] == "sweef:" {
		a.runAgent(text, mode, PageContext{})
		return
	}
	origin := store.OriginOf(st.URL)
	switch a.prof.Sites.Agent(origin) {
	case store.VisBlocked:
		a.runAgent(text, mode, PageContext{Blocked: true})
	case store.VisAsk:
		a.runSeq++
		run := fmt.Sprintf("ask-%d", a.runSeq)
		a.pendingAsk[origin] = pendingAgent{text: text, mode: mode, tab: id}
		a.sendChrome(mAgentEvent, AgentEvent{RunID: run, Kind: "confirm", Text: "Let the agent read " + origin + "?",
			Data: map[string]any{"origin": origin, "tab": id}})
	default:
		a.runWithPage(id, text, mode)
	}
}

// runWithPage reads the (allowed) page text and starts the run.
func (a *App) runWithPage(tabID, text, mode string) {
	e := a.page(tabID)
	st, _ := a.model.Tab(tabID)
	if e == nil {
		a.runAgent(text, mode, PageContext{})
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
			a.runAgent(text, mode, pc)
		})
	})
}

func (a *App) runAgent(text, mode string, pc PageContext) {
	ch := a.agent.Send(text, mode, pc)
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		for {
			select {
			case ev, ok := <-ch:
				if !ok {
					return
				}
				a.sendChrome(mAgentEvent, ev)
			case <-a.quit:
				return
			}
		}
	}()
}

// setAgentVisibility records the user's answer and resumes a pending run.
func (a *App) setAgentVisibility(origin string, allowed bool) {
	v := store.VisBlocked
	if allowed {
		v = store.VisAllowed
	}
	a.prof.Sites.SetAgent(origin, v)
	if p, ok := a.pendingAsk[origin]; ok {
		delete(a.pendingAsk, origin)
		if allowed {
			a.runWithPage(p.tab, p.text, p.mode)
		} else if a.agent != nil {
			a.runAgent(p.text, p.mode, PageContext{Blocked: true})
		}
	}
	a.markState()
}
