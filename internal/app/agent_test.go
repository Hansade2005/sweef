package app

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/app/store"
)

func sideLog(h *harness) string {
	var s string
	h.chromeEval(`document.getElementById('sidelog').textContent`, &s)
	return s
}

func TestAgentVisibilityGate(t *testing.T) {
	srv := pageServer(t)
	fake := NewFakeAgent()
	h := newHarness(t, hopts{cfg: Config{Agent: fake, InitialURLs: []string{srv.URL + "/two"}}})
	h.waitTitle("Page Two")
	origin := store.OriginOf(srv.URL)

	// default is "ask": nothing is sent, the user is asked
	h.uiPost("agent.send", map[string]any{"text": "summarize", "mode": "ask"})
	h.waitFor("confirm event", func() bool { return strings.Contains(sideLog(h), "confirm") })
	if fake.callCount() != 0 {
		t.Fatal("the agent was started before the user approved the site")
	}
	// approve -> the run starts with the page content
	h.uiPost("agent.pageVisibility", map[string]any{"origin": origin, "allowed": true})
	h.waitFor("run", func() bool { _, ok := fake.LastCall(); return ok })
	call, _ := fake.LastCall()
	if call.Text != "summarize" || call.Mode != "ask" || call.Page.Blocked || call.Page.Origin != origin || call.Page.Title != "Page Two" || !strings.Contains(call.Page.Text, "Green") {
		t.Fatalf("approved run got %+v", call)
	}
	// events stream into the chrome page as agent.event
	h.waitFor("streamed text", func() bool { return strings.Contains(sideLog(h), "You asked: summarize") })
	if !strings.Contains(sideLog(h), "done") {
		h.waitFor("done event", func() bool { return strings.Contains(sideLog(h), "done") })
	}
	if h.app.prof.Sites.Agent(origin) != store.VisAllowed {
		t.Fatal("approval not remembered")
	}
	// next time no prompt
	h.uiPost("agent.send", map[string]any{"text": "again", "mode": "act"})
	h.waitFor("second run", func() bool { return fake.callCount() == 2 })

	// block: the host gets no content, no URL, no title
	h.uiPost("agent.pageVisibility", map[string]any{"origin": origin, "allowed": false})
	h.uiPost("agent.send", map[string]any{"text": "what is on this page", "mode": "ask"})
	h.waitFor("blocked run", func() bool { return fake.callCount() == 3 })
	bc, _ := fake.LastCall()
	if !bc.Page.Blocked || bc.Page.Text != "" || bc.Page.URL != "" || bc.Page.Title != "" || bc.Page.Origin != "" || bc.Page.TabID != "" {
		t.Fatalf("blocked origin leaked to the agent: %+v", bc.Page)
	}
	h.waitFor("blocked answer", func() bool { return strings.Contains(sideLog(h), "not allowed to read") })

	// stop / take control / key are forwarded
	h.uiPost("agent.stop", nil)
	h.uiPost("agent.takeControl", nil)
	h.uiPost("agent.setKey", map[string]any{"key": "sk-test"})
	if s, tk, k := fake.counters(); s != 1 || tk != 1 || k != "sk-test" {
		t.Fatalf("stop=%d take=%d key=%q", s, tk, k)
	}

	// a page that is not a web page carries no context at all
	h.key("ctrl+t")
	h.waitFor("newtab", func() bool { return h.tab().URL == newTabURL })
	h.uiPost("agent.send", map[string]any{"text": "hello", "mode": "ask"})
	h.waitFor("newtab run", func() bool { return fake.callCount() == 4 })
	nc, _ := fake.LastCall()
	if nc.Page != (PageContext{}) {
		t.Fatalf("internal page leaked into the context: %+v", nc.Page)
	}
}

func TestAgentOmniboxAskOpensSidebar(t *testing.T) {
	srv := pageServer(t)
	fake := NewFakeAgent()
	h := newHarness(t, hopts{cfg: Config{Agent: fake, InitialURLs: []string{srv.URL + "/two"}}})
	h.waitTitle("Page Two")
	h.app.prof.Sites.SetAgent(srv.URL, store.VisAllowed)
	h.key("ctrl+l")
	h.typeText("? what does this page say")
	h.key("Enter")
	h.waitFor("ask run", func() bool { _, ok := fake.LastCall(); return ok })
	if !h.app.model.Sidebar.Open {
		t.Fatal("asking the agent from the omnibox should open the sidebar")
	}
	if c, _ := fake.LastCall(); c.Text != "what does this page say" {
		t.Fatalf("prompt %q", c.Text)
	}
	if h.tab().URL != srv.URL+"/two" {
		t.Fatal("an ask must not navigate the tab")
	}
}

func TestAgentNotConfigured(t *testing.T) {
	srv := pageServer(t)
	h := newHarness(t, hopts{cfg: Config{InitialURLs: []string{srv.URL + "/two"}}})
	h.waitTitle("Page Two")
	h.uiPost("agent.send", map[string]any{"text": "hi"})
	h.waitFor("error event", func() bool { return strings.Contains(sideLog(h), "No agent is configured") })
}

type spyHost struct{ got []PageContext }

func (s *spyHost) Send(text, mode string, pc PageContext) <-chan AgentEvent {
	s.got = append(s.got, pc)
	ch := make(chan AgentEvent)
	close(ch)
	return ch
}
func (s *spyHost) Stop()         {}
func (s *spyHost) TakeControl()  {}
func (s *spyHost) SetKey(string) {}

// Gate protects even callers that build a PageContext by hand.
func TestGateStripsBlockedContent(t *testing.T) {
	spy := &spyHost{}
	vis := map[string]store.Visibility{"https://allowed.test": store.VisAllowed, "https://blocked.test": store.VisBlocked}
	g := &Gate{Inner: spy, Vis: func(o string) store.Visibility {
		if v, ok := vis[store.OriginOf(o)]; ok {
			return v
		}
		return store.VisAsk
	}}
	g.Send("q", "ask", PageContext{URL: "https://allowed.test/x", Text: "fine", Title: "T"})
	g.Send("q", "ask", PageContext{URL: "https://blocked.test/x", Text: "SECRET", Title: "T"})
	g.Send("q", "ask", PageContext{Origin: "https://blocked.test", Text: "SECRET"})
	g.Send("q", "ask", PageContext{URL: "https://unknown.test/", Text: "SECRET"}) // ask == not approved
	g.Send("q", "ask", PageContext{Blocked: true, Text: "SECRET"})
	g.Send("q", "ask", PageContext{}) // no page: fine
	if spy.got[0].Text != "fine" {
		t.Fatal("allowed content must pass")
	}
	for i := 1; i <= 4; i++ {
		if spy.got[i] != (PageContext{Blocked: true}) {
			t.Fatalf("call %d leaked: %+v", i, spy.got[i])
		}
	}
	if spy.got[5] != (PageContext{}) {
		t.Fatalf("empty context altered: %+v", spy.got[5])
	}
}

func TestBackendRespectsVisibility(t *testing.T) {
	srv := pageServer(t)
	h := newHarness(t, hopts{cfg: Config{InitialURLs: []string{srv.URL + "/", srv.URL + "/two"}}})
	h.waitFor("2 tabs", func() bool { return len(h.tabIDs()) == 2 && !h.tab().Loading })
	h.idle()
	b := Backend{A: h.app}
	ids := h.tabIDs()
	// default (ask): tabs are listed by id only, reading is refused
	for _, ti := range b.Tabs() {
		if ti.URL != "" || ti.Title != "" || !ti.Blocked {
			t.Fatalf("tab info leaked before approval: %+v", ti)
		}
	}
	if _, err := b.ReadPage(ids[0]); !errors.Is(err, ErrNeedsApproval) {
		t.Fatalf("ask: %v", err)
	}
	h.app.prof.Sites.SetAgent(srv.URL, store.VisAllowed)
	if txt, err := b.ReadPage(ids[1]); err != nil || !strings.Contains(txt, "Green") {
		t.Fatalf("allowed read: %q %v", txt, err)
	}
	if infos := b.Tabs(); infos[0].URL == "" || infos[1].Title != "Page Two" {
		t.Fatalf("allowed tabs: %+v", infos)
	}
	if err := b.Click(ids[0], "#l"); err != nil {
		t.Fatal(err)
	}
	h.waitTitle("Page Two")
	h.app.prof.Sites.SetAgent(srv.URL, store.VisBlocked)
	for name, f := range map[string]func() error{
		"read":     func() error { _, err := b.ReadPage(ids[0]); return err },
		"navigate": func() error { return b.Navigate(ids[0], "https://x.test/") },
		"click":    func() error { return b.Click(ids[0], "a") },
		"fill":     func() error { return b.Fill(ids[0], "input", "x") },
	} {
		if err := f(); !errors.Is(err, ErrOriginBlocked) {
			t.Errorf("%s on a blocked origin: %v", name, err)
		}
	}
	if err := b.Click("nope", "a"); err == nil {
		t.Error("unknown tab accepted")
	}
	time.Sleep(10 * time.Millisecond)
}
