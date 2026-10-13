package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/app/store"
	"github.com/sweefbrowser/sweef/internal/engine"
)

// ---- a fake engine side with deterministic byte counts -----------------------------------

type fakeData struct {
	mu        sync.Mutex
	avail     bool
	ctxLevel  map[*engine.BrowserContext]string
	ctxSnap   map[*engine.BrowserContext]DataSnap
	tabLevel  map[*engine.Tab]string
	tabSnap   map[*engine.Tab]DataSnap
	bg        map[*engine.Tab]bool
	loaded    []int64
	images    int
	fullCalls int
	full      map[*engine.Tab]bool
	statCalls atomic.Int64
}

func newFakeData() *fakeData {
	return &fakeData{avail: true, ctxLevel: map[*engine.BrowserContext]string{}, ctxSnap: map[*engine.BrowserContext]DataSnap{},
		tabLevel: map[*engine.Tab]string{}, tabSnap: map[*engine.Tab]DataSnap{}, bg: map[*engine.Tab]bool{}, full: map[*engine.Tab]bool{}}
}

func (f *fakeData) Available() bool { return f.avail }
func (f *fakeData) SetContextPolicy(c *engine.BrowserContext, level string) {
	f.mu.Lock()
	f.ctxLevel[c] = level
	f.mu.Unlock()
}
func (f *fakeData) SetTabPolicy(t *engine.Tab, level string) {
	f.mu.Lock()
	f.tabLevel[t] = level
	f.mu.Unlock()
}
func (f *fakeData) SetBackground(t *engine.Tab, bg bool) {
	f.mu.Lock()
	f.bg[t] = bg
	f.mu.Unlock()
}
func (f *fakeData) ContextStats(c *engine.BrowserContext) DataSnap {
	f.statCalls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ctxSnap[c]
}
func (f *fakeData) TabStats(t *engine.Tab) DataSnap {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tabSnap[t]
}
func (f *fakeData) LoadSkipped(t *engine.Tab, id int64) error {
	f.mu.Lock()
	f.loaded = append(f.loaded, id)
	f.mu.Unlock()
	return nil
}
func (f *fakeData) LoadSkippedImages(t *engine.Tab) (int, error) {
	f.mu.Lock()
	f.images++
	f.mu.Unlock()
	return 3, nil
}
func (f *fakeData) LoadFully(t *engine.Tab) error {
	f.mu.Lock()
	f.fullCalls++
	f.full[t] = true
	f.mu.Unlock()
	return t.Reload()
}
func (f *fakeData) FullyLoaded(t *engine.Tab) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.full[t]
}
func (f *fakeData) IsDataEvent(engine.Event) bool { return false }

// add books wire bytes (and origin attribution) on a context, as if the socket counter moved.
func (f *fakeData) add(c *engine.BrowserContext, wire int64, origins map[string]int64) {
	f.mu.Lock()
	s := f.ctxSnap[c]
	s.WireIn += wire
	if s.ByOrigin == nil {
		s.ByOrigin = map[string]int64{}
	}
	for o, n := range origins {
		s.ByOrigin[o] += n
	}
	f.ctxSnap[c] = s
	f.mu.Unlock()
}

func (f *fakeData) setTab(t *engine.Tab, s DataSnap) {
	f.mu.Lock()
	f.tabSnap[t] = s
	f.mu.Unlock()
}

func (f *fakeData) level(c *engine.BrowserContext) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ctxLevel[c]
}
func (f *fakeData) tabLvl(t *engine.Tab) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tabLevel[t]
}

// testClock is a clock tests can move.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *testClock) set(t time.Time) { c.mu.Lock(); c.t = t; c.mu.Unlock() }

type dsRig struct {
	*harness
	f   *fakeData
	clk *testClock
	srv *httptest.Server
}

func newDSRig(t *testing.T, cfg Config) *dsRig {
	t.Helper()
	srv := pageServer(t)
	f := newFakeData()
	clk := &testClock{t: time.Date(2026, 3, 10, 12, 0, 0, 0, time.Local)}
	cfg.DataEngine, cfg.Now = f, clk.now
	if cfg.InitialURLs == nil {
		cfg.InitialURLs = []string{srv.URL + "/"}
	}
	h := newHarness(t, hopts{cfg: cfg})
	h.waitFor("first page", func() bool { return !h.tab().Loading && h.tab().Title != "" && h.tab().Title != "New Tab" })
	h.idle()
	r := &dsRig{harness: h, f: f, clk: clk, srv: srv}
	// record everything the chrome page is sent
	h.chromeEval(`window.__got = []; (function(){ var o = window.sweef.onMessage; window.sweef.onMessage = function(t, p){ if (t !== "state" && t !== "state.patch") window.__got.push([t, p]); if (o) o(t, p); }; })(); 1`, nil)
	return r
}

func (r *dsRig) defCtx() *engine.BrowserContext { return r.app.Engine().DefaultContext() }

// msgs returns the chrome messages of one type received so far.
func (r *dsRig) msgs(typ string) []map[string]any {
	r.t.Helper()
	var got [][]any
	r.chromeEval(`window.__got`, &got)
	var out []map[string]any
	for _, m := range got {
		if m[0] == typ {
			p, _ := m[1].(map[string]any)
			out = append(out, p)
		}
	}
	return out
}

func (r *dsRig) setBudget(m map[string]any) {
	r.t.Helper()
	r.uiPost("data.setBudget", m)
}

// poll books fake counters now and waits for the app to digest them.
func (r *dsRig) poll() {
	r.t.Helper()
	r.loop(func() { r.app.dataPoll() })
	r.idle()
}

func (r *dsRig) used() int64 {
	var n int64
	r.loop(func() { n = r.app.dataStatus().UsedBytes })
	return n
}

func (r *dsRig) dataState() (m map[string]any) {
	r.loop(func() {
		b, _ := json.Marshal(r.app.dataState())
		_ = json.Unmarshal(b, &m)
	})
	return
}

// ---- tests --------------------------------------------------------------------------------

func TestDataPolicyFollowsSettings(t *testing.T) {
	r := newDSRig(t, Config{})
	def := r.defCtx()
	if got := r.f.level(def); got != "off" {
		t.Fatalf("no budget and no choice should leave the saver off, got %q", got)
	}
	r.setBudget(map[string]any{"enabled": true, "limitMB": 100})
	r.idle()
	if got := r.f.level(def); got != "standard" {
		t.Fatalf("setting a budget with data.level auto should give standard, got %q", got)
	}
	r.uiPost("data.setLevel", map[string]any{"level": "strict"})
	if got := r.f.level(def); got != "strict" {
		t.Fatalf("strict: %q", got)
	}
	r.uiPost("data.setLevel", map[string]any{"level": "off"})
	if got := r.f.level(def); got != "off" {
		t.Fatalf("explicit off must win over the budget default: %q", got)
	}
	if got := r.dataState()["level"]; got != "off" {
		t.Fatalf("state.data.level %v", got)
	}
	// private windows get the same policy
	r.uiPost("data.setLevel", map[string]any{"level": "strict"})
	r.uiPost("incognito.new", map[string]any{})
	r.waitFor("incognito tab", func() bool { return r.tab().Incognito })
	r.loop(func() {
		if r.app.incogCtx == nil || r.f.level(r.app.incogCtx) != "strict" {
			t.Errorf("incognito context did not get the policy: %q", r.f.level(r.app.incogCtx))
		}
	})
	r.uiPost("data.setLevel", map[string]any{"level": "standard"})
	r.loop(func() {
		if r.f.level(r.app.incogCtx) != "standard" {
			t.Errorf("level change did not reach the incognito context")
		}
	})
}

func TestDataSiteRulesAndFullOnce(t *testing.T) {
	r := newDSRig(t, Config{})
	origin := store.OriginOf(r.srv.URL)
	// always lite on a site while the saver is off -> at least standard for that tab
	r.uiPost("data.siteRule", map[string]any{"origin": r.srv.URL + "/some/page", "rule": "lite"})
	if got := r.app.prof.Settings.DataSites()[origin]; got != "lite" {
		t.Fatalf("rule not stored by origin: %v", r.app.prof.Settings.DataSites())
	}
	r.uiPost("nav.reload", map[string]any{})
	r.waitFor("reloaded", func() bool { return r.f.tabLvl(r.app.activePage().t) == "standard" })
	if tab := r.dataState()["tab"].(map[string]any); tab["rule"] != "lite" {
		t.Fatalf("state.data.tab.rule = %v", tab["rule"])
	}
	// always full while the saver is strict -> off for that tab
	r.uiPost("data.setLevel", map[string]any{"level": "strict"})
	r.uiPost("data.siteRule", map[string]any{"origin": origin, "rule": "full"})
	r.uiPost("nav.reload", map[string]any{})
	r.waitFor("full rule", func() bool { return r.f.tabLvl(r.app.activePage().t) == "off" })
	// forgetting the rule inherits again
	r.uiPost("data.siteRule", map[string]any{"origin": origin, "rule": ""})
	r.uiPost("nav.reload", map[string]any{})
	r.waitFor("inherit", func() bool { return r.f.tabLvl(r.app.activePage().t) == "" })
	if len(r.app.prof.Settings.DataSites()) != 0 {
		t.Fatalf("rule should be gone: %v", r.app.prof.Settings.DataSites())
	}
	// "Load fully, once" asks the engine to reload without saving and shows in state
	r.uiPost("data.loadFully", map[string]any{})
	r.waitFor("LoadFully", func() bool { r.f.mu.Lock(); defer r.f.mu.Unlock(); return r.f.fullCalls == 1 })
	r.idle()
	if tab := r.dataState()["tab"].(map[string]any); tab["once"] != true {
		t.Fatalf("tab.once = %v", tab["once"])
	}
	// the next navigation is a normal one again
	r.uiPost("nav.reload", map[string]any{})
	r.idle()
	r.f.mu.Lock()
	calls := r.f.fullCalls
	r.f.mu.Unlock()
	if calls != 1 {
		t.Fatalf("full-once leaked into the next load (%d calls)", calls)
	}
}

func TestDataTapToLoadAndLoadImages(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><html><head><title>Lite Page</title></head><body style="margin:0">
<div style="height:60px;background:#eee">header</div>
<img id="a" data-sweef-lite="skipped" alt="one" style="display:block;width:300px;height:100px;background:#c33">
<img id="b" data-sweef-lite="skipped" alt="two" style="display:block;width:300px;height:100px;background:#33c"></body></html>`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	r := newDSRig(t, Config{InitialURLs: []string{srv.URL + "/"}})
	r.waitFor("lite page", func() bool { return r.tab().Title == "Lite Page" })
	r.idle()
	// tap the second placeholder: the app resolves it to the engine node and asks for it
	r.click(40, vpTop+60+100+40)
	r.waitFor("tap", func() bool { r.f.mu.Lock(); defer r.f.mu.Unlock(); return len(r.f.loaded) == 1 })
	e := r.app.activePage()
	var want int64
	if err := e.call(func() error {
		nodes, err := e.t.QuerySelectorAll(`[data-sweef-lite="skipped"]`)
		if err == nil && len(nodes) == 2 {
			want = nodes[1].BackendID
		}
		return err
	}); err != nil || want == 0 {
		t.Fatalf("nodes: %v %d", err, want)
	}
	r.f.mu.Lock()
	got := r.f.loaded[0]
	r.f.mu.Unlock()
	if got != want {
		t.Fatalf("tapped node %d, want the second placeholder %d", got, want)
	}
	// "Load images on this page"
	r.uiPost("data.loadSkipped", map[string]any{})
	r.waitFor("load images", func() bool { r.f.mu.Lock(); defer r.f.mu.Unlock(); return r.f.images == 1 })
	// a single node by id
	r.uiPost("data.loadSkipped", map[string]any{"nodeId": 77})
	r.waitFor("by id", func() bool { r.f.mu.Lock(); defer r.f.mu.Unlock(); return len(r.f.loaded) == 2 && r.f.loaded[1] == 77 })
}

func TestDataBackgroundTabsAreMarked(t *testing.T) {
	r := newDSRig(t, Config{})
	first := r.app.activePage().t
	r.uiPost("tab.new", map[string]any{"url": r.srv.URL + "/two"})
	r.waitFor("second tab", func() bool { return r.tab().Title == "Page Two" })
	r.idle()
	second := r.app.activePage().t
	r.f.mu.Lock()
	bg1, bg2 := r.f.bg[first], r.f.bg[second]
	r.f.mu.Unlock()
	if !bg1 || bg2 {
		t.Fatalf("after opening a second tab: first background=%v second=%v", bg1, bg2)
	}
	r.key("ctrl+1")
	r.waitTitle("Home Page")
	r.f.mu.Lock()
	bg1, bg2 = r.f.bg[first], r.f.bg[second]
	r.f.mu.Unlock()
	if bg1 || !bg2 {
		t.Fatalf("after switching back: first background=%v second=%v", bg1, bg2)
	}
}

func TestDataBudgetThresholdsFireOncePerPeriod(t *testing.T) {
	r := newDSRig(t, Config{})
	def := r.defCtx()
	r.setBudget(map[string]any{"enabled": true, "limitMB": 100, "period": "month", "resetDay": 1})
	r.idle()
	mb := int64(store.MB)
	r.f.add(def, 50*mb, map[string]int64{"https://a.example": 50 * mb})
	r.poll()
	if n := len(r.msgs("toast")); n != 0 {
		t.Fatalf("50%% must stay quiet, got %d toasts", n)
	}
	r.f.add(def, 25*mb, nil) // 75%
	r.poll()
	r.poll() // polling again must not repeat it
	toasts := r.msgs("toast")
	if len(toasts) != 1 || !strings.Contains(toasts[0]["text"].(string), "70%") {
		t.Fatalf("70%% toast: %v", toasts)
	}
	if a, _ := toasts[0]["action"].(map[string]any); a == nil || a["type"] != "data.setLevel" {
		t.Fatalf("the 70%% toast should offer Text first: %v", toasts[0])
	}
	if tx := toasts[0]["text"].(string); strings.Contains(strings.ToLower(tx), "careful") || strings.Contains(tx, "!") {
		t.Fatalf("copy should stay calm: %q", tx)
	}
	r.f.add(def, 20*mb, nil) // 95%
	r.poll()
	r.f.add(def, 10*mb, nil) // 105%
	r.poll()
	r.f.add(def, 10*mb, nil)
	r.poll()
	toasts = r.msgs("toast")
	if len(toasts) != 3 {
		t.Fatalf("want 70, 90 and exhausted once each, got %d: %v", len(toasts), toasts)
	}
	ex := r.msgs("data.exhausted")
	if len(ex) != 1 || ex[0]["limitMB"].(float64) != 100 || ex[0]["resetsInDays"] == nil {
		t.Fatalf("data.exhausted: %v", ex)
	}
	// state.data budget math
	b := r.dataState()["budget"].(map[string]any)
	if b["exhausted"] != true || b["pct"].(float64) < 114.9 || b["resetsInDays"].(float64) != 22 || b["limitMB"].(float64) != 100 {
		t.Fatalf("budget %v", b)
	}
	// the next period starts clean and warns again, once
	r.clk.set(time.Date(2026, 4, 2, 9, 0, 0, 0, time.Local))
	r.poll()
	if got := r.used(); got != 0 {
		t.Fatalf("a new period should start at zero, used %d", got)
	}
	r.f.add(def, 71*mb, nil)
	r.poll()
	r.poll()
	if n := len(r.msgs("toast")); n != 4 {
		t.Fatalf("new period 70%% should toast once more (4 total), got %d", n)
	}
	// moving the clock back into March does not repeat March's thresholds
	r.clk.set(time.Date(2026, 3, 20, 9, 0, 0, 0, time.Local))
	r.poll()
	if n := len(r.msgs("toast")); n != 4 {
		t.Fatalf("clock change repeated thresholds: %d toasts", n)
	}
}

func TestDataSavingABudgetNeverToasts(t *testing.T) {
	r := newDSRig(t, Config{})
	def := r.defCtx()
	mb := int64(store.MB)
	r.f.add(def, 90*mb, nil)
	r.poll()
	r.setBudget(map[string]any{"enabled": true, "limitMB": 100}) // already at 90%: saving it is not news
	r.idle()
	r.poll()
	if n := len(r.msgs("toast")); n != 0 {
		t.Fatalf("saving a budget answered with %d toasts", n)
	}
}

func TestDataHardStopInterstitialAndLoadOnce(t *testing.T) {
	r := newDSRig(t, Config{})
	def := r.defCtx()
	mb := int64(store.MB)
	r.setBudget(map[string]any{"enabled": true, "limitMB": 10, "hardStop": true})
	r.f.add(def, 11*mb, nil)
	r.poll()
	target := r.srv.URL + "/two"
	r.uiPost("tab.new", map[string]any{"url": target})
	r.waitFor("interstitial", func() bool {
		var title string
		if e := r.app.activePage(); e != nil {
			_ = e.call(func() error { title = e.t.Title(); return nil })
		}
		return title == "Data limit reached" || title == "You have used your data for now"
	})
	if got := r.tab().URL; got != target {
		t.Fatalf("the tab keeps the address that was asked for, got %q", got)
	}
	var text string
	r.pageEval(`document.body.innerText`, &text)
	if strings.Contains(text, "Green") || !strings.Contains(text, "used your data") {
		t.Fatalf("expected the interstitial, got %q", text)
	}
	// the agent is told, plainly, why it cannot load
	id := r.app.model.Active(r.app.winID)
	err := r.app.AgentNavigate(t.Context(), id, target)
	if err == nil || !strings.Contains(err.Error(), "data limit") {
		t.Fatalf("agent load during hard stop: %v", err)
	}
	// Load once: the page appears
	r.uiPost("data.loadOnce", map[string]any{"url": target})
	r.waitFor("page after load once", func() bool { return r.tab().Title == "Page Two" })
	// and only once: the next load stops again
	r.uiPost("nav.reload", map[string]any{})
	r.waitFor("stopped again", func() bool { return r.tab().Title != "Page Two" })
	// raising the limit lets it through for good
	r.setBudget(map[string]any{"limitMB": 500})
	r.uiPost("nav.reload", map[string]any{})
	r.waitFor("loads after raising the limit", func() bool { return r.tab().Title == "Page Two" })
	// with the hard stop off nothing stops, even past the limit
	r.setBudget(map[string]any{"limitMB": 10, "hardStop": false})
	r.uiPost("nav.reload", map[string]any{})
	r.idle()
	if r.tab().Title != "Page Two" {
		t.Fatalf("hardStop off must never stop a load, title %q", r.tab().Title)
	}
	// and an internal page is never stopped
	r.setBudget(map[string]any{"hardStop": true})
	r.uiPost("tab.new", map[string]any{})
	r.waitFor("new tab page", func() bool { return r.tab().URL == newTabURL && !r.tab().Loading })
}

func TestDataIncognitoCountsButStoresNoOrigins(t *testing.T) {
	dir := t.TempDir()
	prof, err := store.OpenProfile(dir)
	if err != nil {
		t.Fatal(err)
	}
	r := newDSRig(t, Config{Profile: prof, NoRestore: true})
	mb := int64(store.MB)
	r.setBudget(map[string]any{"enabled": true, "limitMB": 100})
	r.idle()
	r.uiPost("incognito.new", map[string]any{})
	r.waitFor("incognito tab", func() bool { return r.tab().Incognito })
	var ic *engine.BrowserContext
	r.loop(func() { ic = r.app.incogCtx })
	if ic == nil {
		t.Fatal("no incognito context")
	}
	r.f.add(ic, 8*mb, map[string]int64{"https://secret.example": 8 * mb})
	r.f.add(r.defCtx(), 2*mb, map[string]int64{"https://normal.example": 2 * mb})
	r.poll()
	if got := r.used(); got != 10*mb {
		t.Fatalf("private bytes must count toward the budget: used %d want %d", got, 10*mb)
	}
	st := r.dataState()
	if top := st["top"].([]any); len(top) != 0 {
		t.Fatalf("a private tab must not list sites: %v", top)
	}
	if st["incognito"] != true {
		t.Fatal("state.data.incognito")
	}
	// back on a normal tab the normal site is listed, the private one never is
	r.key("ctrl+1")
	r.waitTitle("Home Page")
	top := r.dataState()["top"].([]any)
	if len(top) != 1 || top[0].(map[string]any)["origin"] != "https://normal.example" {
		t.Fatalf("top sites %v", top)
	}
	r.app.prof.Data.Flush()
	raw, err := os.ReadFile(filepath.Join(dir, "datausage.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "secret.example") {
		t.Fatalf("a private origin was written to disk: %s", raw)
	}
	if !strings.Contains(string(raw), `"incog":`) {
		t.Fatalf("private bytes should be stored as a bare total: %s", raw)
	}
}

func TestDataStateShapeSeparatesMeasuredFromEstimated(t *testing.T) {
	r := newDSRig(t, Config{})
	mb := int64(store.MB)
	r.setBudget(map[string]any{"enabled": true, "limitMB": 100})
	e := r.app.activePage()
	r.f.setTab(e.t, DataSnap{ByKind: map[string]int64{"document": 60_000, "image": 200_000, "script": 395_360},
		ByOrigin:      map[string]int64{"https://a.example": 400_000, "https://cdn.example": 255_360},
		CacheHitBytes: 300_000, AbortedBytes: 17_440, Skipped: map[string]int{"image": 18, "font": 5}, SkippedEstimate: 1_992_294})
	r.f.add(r.defCtx(), 39*mb, map[string]int64{"https://a.example": 30 * mb})
	r.poll()
	st := r.dataState()
	for _, k := range []string{"level", "used", "usedToday", "budget", "tab", "top", "kinds"} {
		if _, ok := st[k]; !ok {
			t.Errorf("state.data lacks %q: %v", k, st)
		}
	}
	b := st["budget"].(map[string]any)
	for _, k := range []string{"limitMB", "period", "usedMB", "pct", "resetsInDays", "exhausted", "enabled", "hardStop", "warnAt", "resetDay"} {
		if _, ok := b[k]; !ok {
			t.Errorf("budget lacks %q", k)
		}
	}
	if b["usedMB"].(float64) < 38.9 || b["usedMB"].(float64) > 39.1 || b["pct"].(float64) < 38.9 || b["pct"].(float64) > 39.1 {
		t.Fatalf("budget %v", b)
	}
	tab := st["tab"].(map[string]any)
	sv := tab["saved"].(map[string]any)
	if tab["used"].(float64) != 655_360 || sv["measured"].(float64) != 317_440 || sv["estimated"].(float64) != 1_992_294 {
		t.Fatalf("tab numbers: %v", tab)
	}
	if tab["skipped"].(float64) != 23 || tab["canLoad"].(float64) != 18 || tab["approx"] != true {
		t.Fatalf("tab skipped/canLoad/approx: %v", tab)
	}
	if len(tab["bySrc"].([]any)) != 2 {
		t.Fatalf("bySrc %v", tab["bySrc"])
	}
	// the period totals keep the two kinds apart as well
	if saved := st["saved"].(map[string]any); saved["estimated"] == nil || saved["measured"] == nil {
		t.Fatalf("period saved: %v", saved)
	}
	if kinds := st["kinds"].(map[string]any); kinds["image"].(float64) != 200_000 {
		t.Fatalf("kinds %v", kinds)
	}
	if st["available"] != true {
		t.Fatal("available")
	}
}

func TestDataStateIsThrottled(t *testing.T) {
	r := newDSRig(t, Config{})
	r.idle()
	before := r.f.statCalls.Load()
	for i := 0; i < 200; i++ {
		r.app.dataKick()
	}
	time.Sleep(120 * time.Millisecond)
	if n := r.f.statCalls.Load() - before; n > 4 {
		t.Fatalf("200 data events caused %d counter reads in 120 ms: polling is not throttled", n)
	}
	// and polling stops by itself when nothing moves
	r.waitFor("polling stops", func() bool {
		a := r.f.statCalls.Load()
		time.Sleep(600 * time.Millisecond)
		return r.f.statCalls.Load() == a
	})
}

func TestDataUsagePersistsAcrossRestartsAndNeverRefires(t *testing.T) {
	dir := t.TempDir()
	mb := int64(store.MB)
	run := func(fn func(r *dsRig)) {
		prof, err := store.OpenProfile(dir)
		if err != nil {
			t.Fatal(err)
		}
		r := newDSRig(t, Config{Profile: prof, NoRestore: true})
		fn(r)
		r.close()
	}
	run(func(r *dsRig) {
		r.setBudget(map[string]any{"enabled": true, "limitMB": 100})
		r.f.add(r.defCtx(), 72*mb, map[string]int64{"https://kept.example": 72 * mb})
		r.poll()
		if n := len(r.msgs("toast")); n != 1 {
			t.Fatalf("first run: %d toasts", n)
		}
		r.f.add(r.defCtx(), 1*mb, nil) // counted but not polled yet: closing the browser must still book it
	})
	run(func(r *dsRig) {
		if got := r.used(); got != 73*mb {
			t.Fatalf("usage did not survive the restart (want the last, unpolled megabyte too): %d", got)
		}
		r.f.add(r.defCtx(), 1*mb, nil)
		r.poll()
		if n := len(r.msgs("toast")); n != 0 {
			t.Fatalf("a restart repeated the 70%% toast (%d)", n)
		}
		top := r.dataState()["top"].([]any)
		if len(top) != 1 || top[0].(map[string]any)["origin"] != "https://kept.example" {
			t.Fatalf("top after restart: %v", top)
		}
		r.uiPost("data.reset", map[string]any{})
		if got := r.used(); got != 0 {
			t.Fatalf("reset: %d", got)
		}
	})
}

func TestDataAgentLiteAndResearchLine(t *testing.T) {
	r := newDSRig(t, Config{})
	r.app.AgentDataBegin()
	id := r.app.model.Active(r.app.winID)
	r.idle()
	if err := r.app.AgentNavigate(t.Context(), id, r.srv.URL+"/two"); err != nil {
		t.Fatal(err)
	}
	e := r.app.page(id)
	if got := r.f.tabLvl(e.t); got != "agent" {
		t.Fatalf("agent loads should use the agent mode, got %q", got)
	}
	// the agent tab's own counters feed the line
	r.f.setTab(e.t, DataSnap{ByKind: map[string]int64{"document": 100_000, "script": 321_888}, CacheHitBytes: 1000, Skipped: map[string]int{"image": 9}, SkippedEstimate: 1_000_000})
	res := r.app.AgentDataEnd()
	if !res.Available || res.Bytes != 421_888 || res.SavedEstimated != 1_000_000 || res.SavedMeasured != 1000 || res.Skipped != 9 {
		t.Fatalf("research numbers: %+v", res)
	}
	if res.Text != "Research used 412 KB" {
		t.Fatalf("line %q", res.Text)
	}
	// after the run the tab goes back to the person's own policy
	if got := r.f.tabLvl(e.t); got != "" {
		t.Fatalf("tab policy after the run: %q", got)
	}
	// switched off: the agent loads like the person does
	r.app.prof.Settings.Set("data.agentLite", false)
	r.idle()
	r.app.AgentDataBegin()
	if err := r.app.AgentNavigate(t.Context(), id, r.srv.URL+"/"); err != nil {
		t.Fatal(err)
	}
	if got := r.f.tabLvl(e.t); got != "" {
		t.Fatalf("agentLite off should not change the policy, got %q", got)
	}
	r.app.AgentDataEnd()
	// without counters the line is not claimed
	r.f.avail = false
	if res := r.app.AgentDataEnd(); res.Available {
		t.Fatalf("an engine that does not count must not produce a research line: %+v", res)
	}
}

func TestDataFmtBytesMatchesUI(t *testing.T) {
	for in, want := range map[int64]string{0: "0 B", 900: "900 B", 1536: "1.5 KB", 421_888: "412 KB", 5 << 20: "5.0 MB", 61 << 20: "61 MB", 3 << 30: "3.0 GB"} {
		if got := fmtBytes(in); got != want {
			t.Errorf("fmtBytes(%d) = %q want %q", in, got, want)
		}
	}
}

// ---- against the real engine (no fake) --------------------------------------------------

func realEngineServer(t *testing.T) (*httptest.Server, *atomic.Int64) {
	var imgHits atomic.Int64
	body := strings.Repeat("x", 3000)
	png := make([]byte, 40_000) // content is not a valid image: the request and its size are what we measure
	copy(png, []byte("\x89PNG\r\n\x1a\n"))
	mux := http.NewServeMux()
	mux.HandleFunc("/img.png", func(w http.ResponseWriter, r *http.Request) {
		imgHits.Add(1)
		w.Header().Set("Content-Type", "image/png")
		w.Write(png)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<!doctype html><html><head><title>Real Page</title></head><body style="margin:0"><p>%s</p>
<img id="pic" src="/img.png" width="300" height="100" alt="pic" style="display:block"></body></html>`, body)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &imgHits
}

func TestDataRealEngineCountsAndSkips(t *testing.T) {
	srv, imgHits := realEngineServer(t)
	cfg := Config{InitialURLs: []string{srv.URL + "/"}}
	cfg.Now = (&testClock{t: time.Date(2026, 3, 10, 12, 0, 0, 0, time.Local)}).now
	h := newHarness(t, hopts{cfg: cfg})
	h.waitTitle("Real Page")
	h.idle()
	// counting works with the real engine adapter: the page's bytes are on the meter
	h.loop(func() { h.app.dataPoll() })
	h.idle()
	var used int64
	h.loop(func() { used = h.app.dataStatus().UsedBytes })
	if used < 3000 {
		t.Fatalf("the engine's socket counter should have moved: used %d", used)
	}
	var st map[string]any
	h.loop(func() { b, _ := json.Marshal(h.app.dataState()); _ = json.Unmarshal(b, &st) })
	if st["available"] != true || st["tab"].(map[string]any)["used"].(float64) < 3000 {
		t.Fatalf("state.data %v", st)
	}
	if imgHits.Load() == 0 {
		t.Skip("engine did not fetch the image in this configuration")
	}
	// Text first: a fresh load keeps the picture back behind a tap-to-load placeholder
	before := imgHits.Load()
	h.uiPost("data.setLevel", map[string]any{"level": "strict"})
	h.uiPost("nav.reload", map[string]any{})
	h.idle()
	h.waitFor("skipped image counted", func() bool {
		var n float64
		h.loop(func() {
			b, _ := json.Marshal(h.app.dataState())
			var m map[string]any
			_ = json.Unmarshal(b, &m)
			n, _ = m["tab"].(map[string]any)["skipped"].(float64)
		})
		return n >= 1
	})
	if imgHits.Load() != before {
		t.Fatalf("strict mode should not have downloaded the image (%d -> %d requests)", before, imgHits.Load())
	}
	// "Load images on this page" fetches it
	h.uiPost("data.loadSkipped", map[string]any{})
	h.waitFor("image loaded on request", func() bool { return imgHits.Load() > before })
}

func TestDataRealEngineTapPlaceholder(t *testing.T) {
	srv, imgHits := realEngineServer(t)
	h := newHarness(t, hopts{cfg: Config{InitialURLs: []string{srv.URL + "/"}}})
	h.waitTitle("Real Page")
	h.uiPost("data.setLevel", map[string]any{"level": "strict"})
	h.uiPost("nav.reload", map[string]any{})
	h.idle()
	before := imgHits.Load()
	var rect struct{ Y, H float64 }
	h.waitFor("placeholder in the page", func() bool {
		var n float64
		h.pageEval(`document.querySelectorAll('[data-sweef-lite="skipped"]').length`, &n)
		return n == 1
	})
	h.pageEval(`(function(){var r=document.querySelector('[data-sweef-lite="skipped"]').getBoundingClientRect();return {y:r.top,h:r.height}})()`, &rect)
	h.click(40, vpTop+rect.Y+rect.H/2)
	h.waitFor("tapping a placeholder loads that image", func() bool { return imgHits.Load() > before })
}
