package agentbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/agent"
	"github.com/sweefbrowser/sweef/internal/app"
	"github.com/sweefbrowser/sweef/internal/app/headless"
	"github.com/sweefbrowser/sweef/internal/app/store"
	"github.com/sweefbrowser/sweef/internal/ui"
)

const testKey = "sk-ant-test-0123456789abcdefXYZ"

// pageSite is a local website: pages are served from a map.
type pageSite struct {
	srv  *httptest.Server
	mu   sync.Mutex
	hits []string
}

func newSite(t testing.TB, pages map[string]string) *pageSite {
	s := &pageSite{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.hits = append(s.hits, r.URL.Path)
		s.mu.Unlock()
		body, ok := pages[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("content-type", "text/html; charset=utf-8")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *pageSite) URL() string { return s.srv.URL }
func (s *pageSite) hitCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.hits)
}

type envOpts struct {
	profile *store.Profile
	noKey   bool
	initial []string
	allow   []string // origins/URLs the user allowed
	block   []string
	host    func(*Options)
	incog   bool
	fresh   bool // never share the env
}

// env is a full headless GUI (real chrome UI, real engine) with the agent host
// pointed at a fake Anthropic server.
type env struct {
	t    *testing.T
	host *Host
	app  *app.App
	win  *headless.Window
	fake *fakeAPI
	done chan error
	once sync.Once
	tab0 string
}

func buildEnv(t *testing.T, fake *fakeAPI, o envOpts, persistent bool) *env {
	t.Helper()
	opts := Options{
		NoEnvKey: true,
		Tune: func(c *agent.Config) {
			c.Sleep = func(ctx context.Context, d time.Duration) error { return ctx.Err() }
		},
		Logf: t.Logf,
	}
	if persistent {
		opts.Logf = nil // the first test's t must not outlive its test
	}
	if fake != nil {
		opts.BaseURL = fake.srv.URL
	}
	if fake == nil { // live: the real environment (ANTHROPIC_API_KEY, SWEEF_AGENT_BASE_URL)
		opts.NoEnvKey = false
		opts.Tune = nil
	} else if !o.noKey {
		opts.Env = func(k string) string {
			if k == "ANTHROPIC_API_KEY" {
				return testKey
			}
			return ""
		}
		opts.NoEnvKey = false
	}
	if o.host != nil {
		o.host(&opts)
	}
	host := New(opts)
	win := headless.New(1280, 800, 1)
	prof := o.profile
	if prof == nil {
		prof = store.OpenIncognito()
	}
	for _, u := range o.allow {
		prof.Sites.SetAgent(u, store.VisAllowed)
	}
	for _, u := range o.block {
		prof.Sites.SetAgent(u, store.VisBlocked)
	}
	logf := t.Logf
	dl := t.TempDir()
	if persistent {
		logf = func(string, ...any) {}
		dl = shared.dir
	}
	cfg := app.Config{Shell: win, ChromeFS: ui.FS, Profile: prof, NoRestore: true, Agent: host,
		InitialURLs: o.initial, DownloadsDir: dl, Incognito: o.incog, Logf: logf}
	a, err := app.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	panicT.Store(t)
	app.PanicHook = func(r any) {
		if pt := panicT.Load(); pt != nil {
			pt.Errorf("panic on a tab worker: %v\n%s", r, debug.Stack())
		}
	}
	e := &env{t: t, host: host, app: a, win: win, fake: fake, done: make(chan error, 1)}
	go func() { e.done <- a.Run(context.Background()) }()
	if !persistent {
		t.Cleanup(e.close)
	}
	e.waitFor("chrome ready", func() bool {
		var n float64
		return a.ChromeEval(`(window.SW && SW.state && SW.state.tabs) ? SW.state.tabs.length : -1`, &n) == nil && n >= 1
	})
	if len(o.initial) > 0 {
		e.waitFor("initial page", func() bool { return strings.HasPrefix(e.activeURL(), o.initial[0]) && !e.loading() })
	}
	e.js(`window.__evs = []; SW.on("agent.event", function (e) { window.__evs.push(JSON.parse(JSON.stringify(e))); }); 1`, nil)
	if tabs := a.AgentTabs(); len(tabs) > 0 {
		e.tab0 = tabs[0].ID
	}
	return e
}

var panicT atomic.Pointer[testing.T]

// The headless GUI with the real chrome UI takes seconds to start (much more
// under -race), so plain scenarios share one instance: newEnv recycles it
// (fresh conversation, tabs, site rules and event log) instead of building a
// new one. Scenarios that need their own profile, no key, a private window or
// a custom host get a fresh env.
var shared struct {
	mu  sync.Mutex
	e   *env
	dir string
}

func (o envOpts) plain() bool {
	return o.profile == nil && !o.noKey && !o.incog && o.host == nil && !o.fresh
}

func newEnv(t *testing.T, fake *fakeAPI, o envOpts) *env {
	t.Helper()
	if fake == nil || !o.plain() {
		return buildEnv(t, fake, o, false)
	}
	shared.mu.Lock()
	defer shared.mu.Unlock()
	if shared.e == nil {
		shared.dir, _ = os.MkdirTemp("", "agentbridge-dl")
		shared.e = buildEnv(t, fake, envOpts{initial: o.initial, allow: o.allow, block: o.block}, true)
		shared.e.host.rebase(fake.srv.URL)
	}
	e := shared.e
	e.recycle(t, fake, o)
	return e
}

// recycle returns a shared env to a clean state for the next scenario.
func (e *env) recycle(t *testing.T, fake *fakeAPI, o envOpts) {
	t.Helper()
	e.t, e.fake = t, fake
	panicT.Store(t)
	e.host.Stop()
	e.waitFor("previous run to end", e.runIdle)
	e.host.rebase(fake.srv.URL) // a new agent: empty conversation, the new fake server
	ctx := context.Background()
	for _, tb := range e.app.AgentTabs() {
		if tb.ID != e.tab0 {
			_ = e.app.AgentCloseTab(ctx, tb.ID)
		}
	}
	e.waitFor("tabs closed", func() bool { return len(e.app.AgentTabs()) == 1 })
	sites := e.app.Profile().Sites
	for origin := range sites.All() {
		sites.SetAgent(origin, "")
	}
	for _, u := range o.allow {
		sites.SetAgent(u, store.VisAllowed)
	}
	for _, u := range o.block {
		sites.SetAgent(u, store.VisBlocked)
	}
	_ = e.app.AgentActivateTab(ctx, e.tab0)
	if len(o.initial) > 0 {
		if err := e.app.AgentNavigate(ctx, e.tab0, o.initial[0]); err != nil {
			t.Fatalf("navigate to %s: %v", o.initial[0], err)
		}
	}
	e.waitFor("page loaded", func() bool { return !e.loading() })
	e.openSidebar()
	e.js(`SW.sidekick.newChat(); window.__evs.length = 0; 1`, nil)
	e.waitFor("new chat applied", func() bool { return e.host.chats.title() == "" })
}

func (e *env) close() {
	e.once.Do(func() {
		e.app.Close()
		select {
		case <-e.done:
		case <-time.After(20 * time.Second):
			e.t.Error("app did not stop")
		}
		e.host.Close()
	})
}

func (e *env) waitFor(what string, cond func() bool) {
	e.t.Helper()
	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.t.Fatalf("timed out waiting for %s", what)
}

func (e *env) js(src string, out any) {
	e.t.Helper()
	if err := e.app.ChromeEval(src, out); err != nil {
		e.t.Fatalf("chrome eval: %v\n%s", err, src)
	}
}

func (e *env) tryJS(src string, out any) bool { return e.app.ChromeEval(src, out) == nil }

func (e *env) activeURL() string {
	for _, t := range e.app.AgentTabs() {
		if t.Active {
			return t.URL
		}
	}
	return ""
}

func (e *env) activeTitle() string {
	for _, t := range e.app.AgentTabs() {
		if t.Active {
			return t.Title
		}
	}
	return ""
}

func (e *env) loading() bool {
	for _, t := range e.app.AgentTabs() {
		if t.Loading {
			return true
		}
	}
	return false
}

// openSidebar opens the sidebar and waits until its script is loaded.
func (e *env) openSidebar() {
	e.t.Helper()
	e.js(`sweef.post("sidebar.set", {open: true}); SW.sk.focus(); 1`, nil)
	e.waitFor("sidebar script", func() bool {
		var ok bool
		return e.tryJS(`!!SW.sidekick`, &ok) && ok
	})
}

// send types into the sidebar (SW.sidekick.send is what Enter calls).
func (e *env) send(text, mode string) {
	e.t.Helper()
	e.js(fmt.Sprintf(`SW.sidekick.send(%s, %s); 1`, jstr(text), jstr(mode)), nil)
}

func (e *env) post(typ string, payload any) {
	e.t.Helper()
	b, _ := json.Marshal(payload)
	e.js(fmt.Sprintf(`sweef.post(%s, %s); 1`, jstr(typ), string(b)), nil)
}

// events returns the agent.event messages the chrome page has received.
func (e *env) events() []map[string]any {
	var evs []map[string]any
	e.tryJS(`window.__evs`, &evs)
	return evs
}

func (e *env) kinds() []string {
	var out []string
	for _, ev := range e.events() {
		out = append(out, fmt.Sprint(ev["kind"]))
	}
	return out
}

func (e *env) find(kind string) []map[string]any {
	var out []map[string]any
	for _, ev := range e.events() {
		if ev["kind"] == kind {
			out = append(out, ev)
		}
	}
	return out
}

func (e *env) waitKind(kind string) map[string]any {
	e.t.Helper()
	var got map[string]any
	e.waitFor("agent.event "+kind, func() bool {
		if l := e.find(kind); len(l) > 0 {
			got = l[len(l)-1]
			return true
		}
		return false
	})
	return got
}

func (e *env) waitTerminal() map[string]any {
	e.t.Helper()
	var got map[string]any
	e.waitFor("done or error event", func() bool {
		for _, ev := range e.events() {
			if ev["kind"] == "done" || ev["kind"] == "error" {
				got = ev
				return true
			}
		}
		return false
	})
	return got
}

func (e *env) feed() string {
	var s string
	e.tryJS(`document.getElementById("skfeed") ? document.getElementById("skfeed").textContent : ""`, &s)
	return s
}

func (e *env) pageEval(src string, out any) {
	e.t.Helper()
	if err := e.app.ActivePageEval(src, out); err != nil {
		e.t.Fatalf("page eval: %v\n%s", err, src)
	}
}

func (e *env) runIdle() bool { return e.host.current() == nil }

func (e *env) waitRunEnd() {
	e.t.Helper()
	e.waitFor("run to end", e.runIdle)
}

func (e *env) agentState() string {
	var s string
	e.tryJS(`(SW.state.agent||{}).state||""`, &s)
	return s
}

func (e *env) overlayState() string {
	st, _ := e.app.AgentOverlayState()
	return st
}
