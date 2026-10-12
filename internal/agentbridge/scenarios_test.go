package agentbridge

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/app/store"
)

const secretText = "TOPSECRET-ACCOUNT-BALANCE-90210"

func vaultSite(t *testing.T) *pageSite {
	return newSite(t, map[string]string{
		"/":       `<!doctype html><html><head><title>Vault</title></head><body><h1>Vault</h1><p>` + secretText + `</p></body></html>`,
		"/secret": `<!doctype html><html><head><title>Vault secret</title></head><body><h1>Vault</h1><p>` + secretText + `</p></body></html>`,
	})
}

func noSecret(t *testing.T, fake *fakeAPI) {
	t.Helper()
	for i, b := range fake.requests() {
		if strings.Contains(string(b), secretText) {
			t.Fatalf("request %d to the model contains text of a page the user did not allow", i+1)
		}
	}
}

func TestStopCancelsRunAndRestoresIdle(t *testing.T) {
	site := shopSite(t)
	gone := make(chan struct{})
	fake := newFakeAPI(t, scripted(
		toolTurn("", tc{"r1", "read_page", `{}`}),
		func(s *sw, body []byte) {
			s.std()
			s.raw("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
			s.raw("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Working on the answer"}}`)
			<-s.r.Context().Done() // hang until the client goes away
			close(gone)
		},
	))
	e := newEnv(t, fake, envOpts{initial: []string{site.URL() + "/products"}, allow: []string{site.URL()}})
	e.openSidebar()
	e.send("take your time", "agent")
	e.waitFor("streamed text", func() bool { return strings.Contains(joinText(e.find("text")), "Working on the answer") })
	e.waitFor("overlay acting", func() bool { return e.overlayState() == "acting" })
	e.post("agent.stop", nil)
	select {
	case <-gone:
	case <-time.After(5 * time.Second):
		t.Fatal("the HTTP request was not cancelled by Stop")
	}
	done := e.waitTerminal()
	if done["kind"] != "done" || done["stop"] != "stopped" {
		t.Fatalf("terminal event %v", done)
	}
	e.waitRunEnd()
	e.waitFor("idle", func() bool { return e.agentState() == "idle" })
	if st := e.overlayState(); st != "stopped" {
		t.Fatalf("overlay %q after stop", st)
	}
	// a new run works afterwards
	fake.set(scripted(textTurn(0, "back again")))
	n := len(e.find("done"))
	e.send("hello?", "ask")
	e.waitFor("second run", func() bool { return len(e.find("done")) > n })
}

func TestTakeControlPausesAndResumeContinues(t *testing.T) {
	site := shopSite(t)
	release := make(chan struct{})
	fake := newFakeAPI(t, scripted(
		func(s *sw, body []byte) {
			<-release
			toolTurn("", tc{"r1", "read_page", `{}`})(s, body)
		},
		textTurn(0, "continued after resume"),
	))
	e := newEnv(t, fake, envOpts{initial: []string{site.URL() + "/products"}, allow: []string{site.URL()}})
	e.openSidebar()
	e.send("do the thing", "agent")
	e.waitFor("first request", func() bool { return fake.count() == 1 })
	e.post("agent.takeControl", nil)
	e.waitFor("paused", func() bool { return e.agentState() == "paused" && e.overlayState() == "control" })
	close(release)
	// the loop must wait for the user: no second request
	time.Sleep(600 * time.Millisecond)
	if fake.count() != 1 {
		t.Fatalf("the agent sent a request while the user had control (%d)", fake.count())
	}
	e.post("agent.resume", nil)
	done := e.waitTerminal()
	if done["kind"] != "done" {
		t.Fatalf("run: %v", done)
	}
	if fake.count() != 2 {
		t.Fatalf("requests: %d", fake.count())
	}
	if !strings.Contains(string(fake.requests()[1]), "while you were paused") {
		t.Fatal("the model was not told the user had control")
	}
	if !strings.Contains(joinText(e.find("text")), "continued after resume") {
		t.Fatal("answer missing")
	}
	var sawPaused bool
	for _, ev := range e.find("status") {
		if ev["state"] == "paused" {
			sawPaused = true
		}
	}
	if !sawPaused {
		t.Fatalf("no paused status event: %v", e.kinds())
	}
}

func TestBlockedOriginNeverReachesTheModel(t *testing.T) {
	t.Run("navigate to a blocked site", func(t *testing.T) {
		shop, vault := shopSite(t), vaultSite(t)
		fake := newFakeAPI(t, scripted(
			toolTurn("", tc{"r1", "read_page", `{}`}),
			toolTurn("", tc{"a1", "act", actJSON(`[{"op":"navigate","url":"` + vault.URL() + `/secret"},{"op":"extract"}]`)}),
			toolTurn("", tc{"r2", "read_page", `{}`}),
			textTurn(0, "I could not read that site."),
		))
		e := newEnv(t, fake, envOpts{initial: []string{shop.URL() + "/"}, allow: []string{shop.URL()}, block: []string{vault.URL()}})
		e.openSidebar()
		e.send("what is my balance", "agent")
		if d := e.waitTerminal(); d["kind"] != "done" {
			t.Fatalf("run: %v", d)
		}
		noSecret(t, fake)
		if vault.hitCount() != 0 {
			t.Fatalf("the browser fetched the blocked site %d times", vault.hitCount())
		}
		if !strings.Contains(toolResultText(fake.requests()[2]), "hidden by user") {
			t.Fatalf("the model was not told: %s", toolResultText(fake.requests()[2]))
		}
	})
	t.Run("click a link to a blocked site", func(t *testing.T) {
		vault := vaultSite(t)
		home := newSite(t, map[string]string{"/": `<!doctype html><html><head><title>Links</title></head><body><a href="` + vault.URL() + `/secret">My bank</a></body></html>`})
		fake := newFakeAPI(t, scripted(
			toolTurn("", tc{"r1", "read_page", `{}`}),
			dyn(func(b []byte) (string, []tc) {
				return "", []tc{{"a1", "act", actJSON(`[{"op":"click","ref":"` + findRef(b, "link", "My bank") + `"},{"op":"extract"}]`)}}
			}),
			toolTurn("", tc{"r2", "read_page", `{}`}),
			textTurn(0, "Blocked."),
		))
		e := newEnv(t, fake, envOpts{initial: []string{home.URL() + "/"}, allow: []string{home.URL()}, block: []string{vault.URL()}})
		e.openSidebar()
		e.send("open my bank and read it", "agent")
		if d := e.waitTerminal(); d["kind"] != "done" {
			t.Fatalf("run: %v", d)
		}
		noSecret(t, fake)
		if vault.hitCount() != 0 {
			t.Fatalf("the blocked link was followed")
		}
	})
	t.Run("page redirected into a blocked site by script", func(t *testing.T) {
		vault := vaultSite(t)
		home := newSite(t, map[string]string{
			"/": `<!doctype html><html><head><title>Jumper</title></head><body><button id="j">Jump</button><script>document.getElementById('j').onclick=function(){location.href='` + vault.URL() + `/secret'}</script></body></html>`})
		fake := newFakeAPI(t, scripted(
			toolTurn("", tc{"r1", "read_page", `{}`}),
			dyn(func(b []byte) (string, []tc) {
				return "", []tc{{"a1", "act", actJSON(`[{"op":"click","ref":"` + findRef(b, "button", "Jump") + `"},{"op":"extract"}]`)}}
			}),
			toolTurn("", tc{"r2", "read_page", `{}`}),
			textTurn(0, "Done."),
		))
		e := newEnv(t, fake, envOpts{initial: []string{home.URL() + "/"}, allow: []string{home.URL()}, block: []string{vault.URL()}})
		e.openSidebar()
		e.send("click jump and read the page", "agent")
		if d := e.waitTerminal(); d["kind"] != "done" {
			t.Fatalf("run: %v", d)
		}
		noSecret(t, fake)
		if !strings.Contains(toolResultText(fake.requests()[3]), "hidden by user") {
			t.Fatalf("expected a hidden placeholder, got: %s", toolResultText(fake.requests()[3]))
		}
	})
	t.Run("the page the user is on is blocked", func(t *testing.T) {
		vault := vaultSite(t)
		fake := newFakeAPI(t, scripted(
			toolTurn("", tc{"r1", "read_page", `{}`}, tc{"t1", "tabs", `{"action":"list"}`}),
			textTurn(0, "That page is hidden from me."),
		))
		e := newEnv(t, fake, envOpts{initial: []string{vault.URL() + "/"}, block: []string{vault.URL()}})
		e.openSidebar()
		e.send("summarize this page", "ask")
		if d := e.waitTerminal(); d["kind"] != "done" {
			t.Fatalf("run: %v", d)
		}
		noSecret(t, fake)
		if strings.Contains(string(fake.requests()[1]), vault.URL()) {
			t.Fatal("the blocked origin's URL reached the model")
		}
	})
}

func TestUndecidedOriginAsksTheUserMidRun(t *testing.T) {
	for _, allow := range []bool{true, false} {
		t.Run(fmt.Sprint("allow=", allow), func(t *testing.T) {
			shop, vault := shopSite(t), vaultSite(t)
			fake := newFakeAPI(t, scripted(
				toolTurn("", tc{"r1", "read_page", `{}`}),
				toolTurn("", tc{"a1", "act", actJSON(`[{"op":"navigate","url":"` + vault.URL() + `/secret"}]`)}),
				toolTurn("", tc{"r2", "read_page", `{}`}),
				textTurn(0, "finished"),
			))
			e := newEnv(t, fake, envOpts{initial: []string{shop.URL() + "/"}, allow: []string{shop.URL()}})
			e.openSidebar()
			e.send("go look at the vault", "agent")
			cf := e.waitKind("confirm")
			id, _ := cf["id"].(string)
			if !strings.HasPrefix(id, "visibility-") {
				t.Fatalf("expected the site-visibility card, got %v", cf)
			}
			if vault.hitCount() != 0 {
				t.Fatal("navigated before the user answered")
			}
			e.waitFor("waiting", func() bool { return e.agentState() == "waiting" })
			e.post("agent.confirm", map[string]any{"runId": cf["runId"], "id": id, "approved": allow})
			if d := e.waitTerminal(); d["kind"] != "done" {
				t.Fatalf("run: %v", d)
			}
			if allow {
				if vault.hitCount() == 0 {
					t.Fatal("approved but never navigated")
				}
				if e.app.AgentSiteRule(vault.URL()) != store.VisAllowed {
					t.Fatal("approval not remembered")
				}
				if !strings.Contains(string(fake.requests()[3]), secretText) {
					t.Fatal("approved page not readable by the model")
				}
			} else {
				if vault.hitCount() != 0 {
					t.Fatal("denied but navigated")
				}
				noSecret(t, fake)
			}
		})
	}
}

func TestIncognitoTabsAreInvisibleToTheAgent(t *testing.T) {
	shop, vault := shopSite(t), vaultSite(t)
	fake := newFakeAPI(t, nil)
	// the vault is even allowed: being incognito alone must hide it
	e := newEnv(t, fake, envOpts{initial: []string{shop.URL() + "/"}, allow: []string{shop.URL(), vault.URL()}})
	before := map[string]bool{}
	for _, tb := range e.app.AgentTabs() {
		before[tb.ID] = true
	}
	e.post("tab.new", map[string]any{"url": vault.URL() + "/secret", "incognito": true})
	var incog string
	e.waitFor("incognito tab", func() bool {
		for _, tb := range e.app.AgentTabs() {
			if !before[tb.ID] && tb.Private && strings.HasPrefix(tb.URL, vault.URL()) && !tb.Loading {
				incog = tb.ID
				return true
			}
		}
		return false
	})
	fake.set(scripted(
		toolTurn("", tc{"t1", "tabs", `{"action":"list"}`}),
		toolTurn("", tc{"r1", "read_page", `{"tab":"` + incog + `"}`}),
		textTurn(0, "no such tab"),
	))
	// bring the normal tab back so the user is "on" the shop page
	e.post("tab.activate", map[string]any{"id": e.tab0})
	e.waitFor("shop tab active", func() bool { return strings.HasPrefix(e.activeURL(), shop.URL()) })
	e.openSidebar()
	e.send("what tabs are open", "ask")
	if d := e.waitTerminal(); d["kind"] != "done" {
		t.Fatalf("run: %v", d)
	}
	noSecret(t, fake)
	res := toolResultText(fake.requests()[1])
	if strings.Contains(res, incog) || strings.Contains(res, "Vault") {
		t.Fatalf("the incognito tab was listed: %s", res)
	}
	if !strings.Contains(toolResultText(fake.requests()[2]), "no such tab") {
		t.Fatalf("read of the incognito tab was not refused: %s", toolResultText(fake.requests()[2]))
	}
}

func TestAskModeReadsButDoesNotAct(t *testing.T) {
	site := shopSite(t)
	fake := newFakeAPI(t, scripted(
		toolTurn("", tc{"r1", "read_page", `{}`}),
		dyn(func(b []byte) (string, []tc) {
			return "", []tc{{"a1", "act", actJSON(`[{"op":"click","ref":"` + findRef(b, "button", "Place order") + `"}]`)},
				{"a2", "act", actJSON(`[{"op":"click","ref":"` + findRef(b, "button", "Add note") + `"}]`)},
				{"a3", "act", actJSON(`[{"op":"extract"}]`)}}
		}),
		textTurn(0, "read only"),
	))
	e := newEnv(t, fake, envOpts{initial: []string{site.URL() + "/products"}, allow: []string{site.URL()}})
	e.openSidebar()
	ovlBefore := e.overlayState()
	e.send("order the kettle", "ask")
	if d := e.waitTerminal(); d["kind"] != "done" {
		t.Fatalf("run: %v", d)
	}
	var ordered bool
	e.pageEval(`!!window.ordered`, &ordered)
	if ordered {
		t.Fatal("ask mode clicked a button")
	}
	if len(e.find("confirm")) != 0 {
		t.Fatal("ask mode must not show approval cards")
	}
	if !strings.Contains(toolResultText(fake.requests()[2]), "Ask mode") {
		t.Fatalf("model not told about ask mode: %s", toolResultText(fake.requests()[2]))
	}
	if !strings.Contains(toolResultText(fake.requests()[2]), "Blue kettle") {
		t.Fatalf("extract should still work in ask mode")
	}
	if st := e.overlayState(); st != ovlBefore {
		t.Fatalf("ask mode must not touch the agent overlay: %q -> %q", ovlBefore, st)
	}
}

func TestOverlayAndCursorFollowTheAgent(t *testing.T) {
	site := shopSite(t)
	fake := newFakeAPI(t, scripted(
		toolTurn("", tc{"r1", "read_page", `{}`}),
		dyn(func(b []byte) (string, []tc) {
			return "", []tc{{"a1", "act", actJSON(`[{"op":"click","ref":"` + findRef(b, "button", "Add note") + `"},{"op":"wait","ms":1500}]`)}}
		}),
		textTurn(0, "noted"),
	))
	e := newEnv(t, fake, envOpts{initial: []string{site.URL() + "/products"}, allow: []string{site.URL()}})
	e.openSidebar()
	e.send("add a note", "agent")
	e.waitFor("agent cursor over the clicked button", func() bool {
		st, cur := e.app.AgentOverlayState()
		if st != "acting" || cur == nil {
			return false
		}
		x, _ := cur["x"].(float64)
		y, _ := cur["y"].(float64)
		return x > 0 && y > 0
	})
	if d := e.waitTerminal(); d["kind"] != "done" {
		t.Fatalf("run: %v", d)
	}
	e.waitFor("overlay done", func() bool { return e.overlayState() == "done" })
	if _, cur := e.app.AgentOverlayState(); cur != nil {
		t.Fatalf("cursor left behind: %v", cur)
	}
	e.waitFor("title changed by the click", func() bool { return e.activeTitle() == "Noted" })
}

func TestKeyLifecycleThroughTheSidebar(t *testing.T) {
	dir := t.TempDir()
	prof, err := store.OpenProfile(dir)
	if err != nil {
		t.Fatal(err)
	}
	site := shopSite(t)
	fake := newFakeAPI(t, scripted(textTurn(0, "hello")))
	e := newEnv(t, fake, envOpts{profile: prof, noKey: true, initial: []string{site.URL() + "/"}, allow: []string{site.URL()}})
	e.openSidebar()

	// no key: a clear auth error, nothing sent
	e.send("hi", "ask")
	er := e.waitKind("error")
	if er["code"] != "auth" {
		t.Fatalf("error %v", er)
	}
	if fake.count() != 0 {
		t.Fatal("a request was made without a key")
	}
	var hasKey bool
	e.js(`!!(SW.state.agent||{}).hasKey`, &hasKey)
	if hasKey {
		t.Fatal("hasKey before a key was set")
	}
	// junk is refused and never stored
	e.js(`window.__kr = []; SW.on("agent.keyResult", function (p) { window.__kr.push(p.ok); }); 1`, nil)
	e.post("agent.setKey", map[string]any{"key": "short key"})
	e.waitFor("keyResult false", func() bool {
		var kr []bool
		return e.tryJS(`window.__kr`, &kr) && len(kr) == 1 && !kr[0]
	})
	e.waitFor("junk refused", func() bool { return e.host.KeySource() == "" })
	if _, err := os.Stat(filepath.Join(dir, keyFile)); err == nil {
		t.Fatal("a junk key was written to disk")
	}
	// a real-looking key is stored 0600 and reported as hasKey only
	e.post("agent.setKey", map[string]any{"key": testKey})
	e.waitFor("keyResult true", func() bool {
		var kr []bool
		return e.tryJS(`window.__kr`, &kr) && len(kr) == 2 && kr[1]
	})
	e.waitFor("hasKey in state", func() bool {
		var ok bool
		return e.tryJS(`!!(SW.state.agent||{}).hasKey`, &ok) && ok
	})
	fi, err := os.Stat(filepath.Join(dir, keyFile))
	if err != nil {
		t.Fatalf("key file: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode %v", fi.Mode().Perm())
	}
	var state string
	e.js(`JSON.stringify(SW.state)`, &state)
	if strings.Contains(state, testKey) {
		t.Fatal("key in chrome state")
	}
	// prewarm happened (HEAD, no key) and the first message uses the key
	e.waitFor("prewarm", func() bool { return fake.heads.Load() > 0 })
	n := len(e.find("done"))
	e.send("hi again", "ask")
	e.waitFor("answer", func() bool { return len(e.find("done")) > n })
	fake.mu.Lock()
	k := fake.keys[0]
	fake.mu.Unlock()
	if k != testKey {
		t.Fatalf("x-api-key %q", k)
	}
	// clearing forgets it everywhere
	e.post("agent.clearKey", nil)
	e.waitFor("hasKey cleared", func() bool {
		var ok bool
		return e.tryJS(`!!(SW.state.agent||{}).hasKey`, &ok) && !ok
	})
	if _, err := os.Stat(filepath.Join(dir, keyFile)); err == nil {
		t.Fatal("key file survived clearKey")
	}
}

func TestClosingTheWindowCancelsTheRun(t *testing.T) {
	site := shopSite(t)
	gone := make(chan struct{})
	fake := newFakeAPI(t, scripted(func(s *sw, body []byte) {
		s.std()
		s.raw("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
		s.raw("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"thinking about it"}}`)
		<-s.r.Context().Done()
		close(gone)
	}))
	e := newEnv(t, fake, envOpts{initial: []string{site.URL() + "/"}, allow: []string{site.URL()}, fresh: true})
	e.openSidebar()
	e.send("long task", "agent")
	e.waitFor("run started", func() bool { return fake.count() == 1 && e.host.current() != nil })
	e.close() // the window closes
	select {
	case <-gone:
	case <-time.After(5 * time.Second):
		t.Fatal("the API request outlived the window")
	}
	if e.host.current() != nil {
		e.waitFor("run cleared", func() bool { return e.host.current() == nil })
	}
}
