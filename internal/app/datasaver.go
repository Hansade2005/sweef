package app

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/sweefbrowser/sweef/internal/app/store"
	"github.com/sweefbrowser/sweef/internal/engine"
)

// Data Saver, app side (docs/data-saver.md). The engine does the saving and
// the counting; this file turns settings into per-context and per-tab
// policies, keeps the usage ledger and the budget, builds state.data for the
// chrome and decides when a page load stops at the hard-stop interstitial.
//
// Honesty: every number the UI shows as "used" comes from the engine's socket
// counters; "saved" is split into a measured part (cache hits and responses
// aborted by a size cap) and an estimated part (median size per skipped
// resource kind), and the two are never added together.

// Policy levels the app asks the engine for. "agent" is the engine's agent
// loading mode (strict without script limits); "" means "inherit".
const (
	lvlOff      = "off"
	lvlStandard = "standard"
	lvlStrict   = "strict"
	lvlAgent    = "agent"
)

// DataSnap is the engine's DataStats in app terms (kinds are plain strings).
type DataSnap struct {
	WireIn, WireOut int64            // exact socket totals since the context was created
	ByKind          map[string]int64 // transfer bytes by kind (approximate attribution)
	ByOrigin        map[string]int64 // approximate
	CacheHitBytes   int64            // measured
	AbortedBytes    int64            // measured
	SkippedKnown    int64            // measured: size of skipped requests learned from the cache or a Content-Length
	Skipped         map[string]int   // request counts skipped by policy
	SkippedEstimate int64            // ESTIMATED bytes avoided
}

// MeasuredSaved is everything provably not downloaded.
func (s DataSnap) MeasuredSaved() int64 { return s.CacheHitBytes + s.AbortedBytes + s.SkippedKnown }

// Wire is the measured socket total.
func (s DataSnap) Wire() int64 { return s.WireIn + s.WireOut }

// Attributed is the bytes attributed to kinds (what a tab "used").
func (s DataSnap) Attributed() int64 {
	var n int64
	for _, v := range s.ByKind {
		n += v
	}
	return n
}

// SkippedCount is the total number of requests skipped by policy.
func (s DataSnap) SkippedCount() int {
	n := 0
	for _, v := range s.Skipped {
		n += v
	}
	return n
}

// DataEngine is everything the app needs from the engine's Data Saver API
// (docs/data-saver.md, "Engine API"). The real adapter is dataengine_real.go;
// tests use a fake with deterministic byte counts (Config.DataEngine).
type DataEngine interface {
	// Available reports whether the engine counts and saves data. When false
	// the UI says so instead of showing zeros that look like measurements.
	Available() bool
	// SetContextPolicy sets the policy of a whole context (level: off,
	// standard or strict).
	SetContextPolicy(ctx *engine.BrowserContext, level string)
	// SetTabPolicy overrides one tab's policy; level "" inherits the context's.
	SetTabPolicy(t *engine.Tab, level string)
	// SetBackground marks a tab as hidden (no timers or requests when the
	// policy suspends background tabs).
	SetBackground(t *engine.Tab, bg bool)
	// ContextStats and TabStats read the counters (any goroutine).
	ContextStats(ctx *engine.BrowserContext) DataSnap
	TabStats(t *engine.Tab) DataSnap
	// LoadSkipped fetches and swaps one tap-to-load resource (nodeID is the
	// placeholder element's backend node id).
	LoadSkipped(t *engine.Tab, nodeID int64) error
	// LoadSkippedImages loads every withheld image on the page and returns how
	// many it loaded ("Load images on this page").
	LoadSkippedImages(t *engine.Tab) (int, error)
	// LoadFully reloads the current page once without any saving; the engine
	// forgets the exception at the tab's next navigation.
	LoadFully(t *engine.Tab) error
	// FullyLoaded reports whether LoadFully is in effect for the current page.
	FullyLoaded(t *engine.Tab) bool
	// IsDataEvent reports whether a bus event is a throttled "data" event.
	IsDataEvent(ev engine.Event) bool
}

// dataSaver is the app's Data Saver state. UI-loop only unless noted.
type dataSaver struct {
	eng  DataEngine
	now  func() time.Time
	kick atomic.Bool // a poll is already queued (any goroutine)

	marks map[*engine.BrowserContext]*dataMark
	timer *time.Timer
	quiet int // polls in a row that saw no new bytes

	cache struct {
		valid  bool
		day    string // the day and budget period the numbers were computed for
		totals store.PeriodTotals
		top    []store.OriginBytes
		today  store.DayUsage
		start  time.Time
		end    time.Time
	}

	tabAcc map[string]*tabAcc
	agent  agentData
}

// dataMark is what was already booked from a context's counters.
type dataMark struct {
	wire, saved, est int64
	origins          map[string]int64
}

// tabAcc accumulates a tab's attributed bytes across navigations (the
// engine's tab counters restart with each navigation).
type tabAcc struct{ banked, last int64 }

type agentData struct {
	active  bool
	touched map[string]int64 // tab id -> accumulated bytes at first touch
}

func (a *App) dataNow() time.Time {
	if a.dat.now != nil {
		return a.dat.now()
	}
	return time.Now()
}

// initDataSaver runs once from New, before the chrome starts.
func (a *App) initDataSaver() {
	d := &a.dat
	d.eng = a.cfg.DataEngine
	if d.eng == nil {
		d.eng = newDefaultDataEngine(a.eng)
	}
	d.now = a.cfg.Now
	if d.now != nil {
		a.prof.Data.SetClock(d.now)
	}
	d.marks = map[*engine.BrowserContext]*dataMark{}
	d.tabAcc = map[string]*tabAcc{}
	d.agent.touched = map[string]int64{}
	d.eng.SetContextPolicy(a.eng.DefaultContext(), a.dataLevel())
}

// ---- settings -> policy ---------------------------------------------------------------------

func (a *App) dataLevel() string { return a.prof.Settings.DataLevel() }

// applyDataPolicy pushes the current level to every context.
func (a *App) applyDataPolicy() {
	lvl := a.dataLevel()
	a.dat.eng.SetContextPolicy(a.eng.DefaultContext(), lvl)
	a.mu.Lock()
	ic := a.incogCtx
	a.mu.Unlock()
	if ic != nil {
		a.dat.eng.SetContextPolicy(ic, lvl)
	}
}

// siteRule is the person's per-origin rule for a URL: "full", "lite" or "".
func (a *App) siteRule(raw string) string {
	o := store.OriginOf(raw)
	if o == "" {
		return ""
	}
	return a.prof.Settings.DataSites()[o]
}

// tabLevelFor decides the policy override for a load: agent mode for agent
// loads, "off" for always-full sites, at least standard for always-lite sites,
// and "" (inherit the context's level) otherwise. "Load fully, once" is not a
// policy: the engine's LoadFully handles it and forgets it by itself.
func (a *App) tabLevelFor(e *etab, raw string) string {
	if e.agentLoad.Load() && a.prof.Settings.Bool("data.agentLite") {
		return lvlAgent
	}
	switch a.siteRule(raw) {
	case "full":
		return lvlOff
	case "lite":
		if a.dataLevel() == lvlOff {
			return lvlStandard
		}
	}
	return ""
}

// ---- background tabs ------------------------------------------------------------------------

func (a *App) dataMarkBackground(id string, bg bool) {
	if e := a.page(id); e != nil {
		d := a.dat.eng
		t := e.t
		e.w.do(func() { d.SetBackground(t, bg) })
	}
}

// ---- polling and accounting -------------------------------------------------------------------

// dataKick schedules a poll soon. Safe from any goroutine (the engine's
// throttled "data" events arrive on the bus goroutine).
func (a *App) dataKick() {
	if !a.dat.eng.Available() || !a.dat.kick.CompareAndSwap(false, true) {
		return
	}
	a.post(func() {
		a.dat.kick.Store(false)
		a.armDataPoll(0)
	})
}

// armDataPoll runs a poll after d unless one is already scheduled. Polling
// stops by itself once the counters have been quiet for a second, so an idle
// browser wakes nothing.
func (a *App) armDataPoll(d time.Duration) {
	if a.dat.timer != nil || a.closing.Load() {
		return
	}
	if d == 0 {
		d = 250 * time.Millisecond // four state updates per second at most
	}
	a.dat.timer = time.AfterFunc(d, func() {
		a.post(func() {
			a.dat.timer = nil
			a.dataPoll()
		})
	})
}

type ctxRef struct {
	ctx   *engine.BrowserContext
	incog bool
}

func (a *App) dataContexts() []ctxRef {
	out := []ctxRef{{a.eng.DefaultContext(), a.cfg.Incognito}}
	a.mu.Lock()
	if a.incogCtx != nil {
		out = append(out, ctxRef{a.incogCtx, true})
	}
	a.mu.Unlock()
	return out
}

// dataPoll books the bytes counted since the last poll into the ledger, fires
// budget thresholds and asks for a state push when something visible moved.
func (a *App) dataPoll() {
	d := &a.dat
	if a.closing.Load() {
		return
	}
	if a.dataBook() {
		d.quiet = 0
		a.dataCheckThresholds()
		a.markDataState()
	} else {
		d.quiet++
	}
	if d.quiet < 4 || a.anyTabLoading() {
		a.armDataPoll(250 * time.Millisecond)
	}
}

// dataBook moves what the engine counted since the last call into the ledger
// (also used once at shutdown). It reports whether anything changed.
func (a *App) dataBook() bool {
	d := &a.dat
	moved := false
	live := map[*engine.BrowserContext]bool{}
	for _, c := range a.dataContexts() {
		live[c.ctx] = true
		s := d.eng.ContextStats(c.ctx)
		m := d.marks[c.ctx]
		if m == nil {
			m = &dataMark{origins: map[string]int64{}}
			d.marks[c.ctx] = m
		}
		wire := s.Wire()
		dw := wire - m.wire
		if dw < 0 { // the counters restarted
			dw = wire
		}
		saved := s.MeasuredSaved()
		ds := saved - m.saved
		if ds < 0 {
			ds = saved
		}
		de := s.SkippedEstimate - m.est
		if de < 0 {
			de = s.SkippedEstimate
		}
		od := map[string]int64{}
		for o, n := range s.ByOrigin {
			if x := n - m.origins[o]; x > 0 {
				od[o] = x
			} else if x < 0 {
				od[o] = n
			}
		}
		m.wire, m.saved, m.est = wire, saved, s.SkippedEstimate
		m.origins = cloneInt64(s.ByOrigin)
		if dw == 0 && ds == 0 && de == 0 {
			continue
		}
		moved = true
		if c.incog {
			a.prof.Data.AddIncognito(dw) // counts toward the budget; no origin is kept
			// private-window savings are not recorded: they would say where the person went
		} else {
			a.prof.Data.Add(dw, od, ds, de)
		}
	}
	for c := range d.marks {
		if !live[c] {
			delete(d.marks, c)
		}
	}
	if a.dataAccumulateTabs() {
		moved = true
	}
	if moved {
		d.cache.valid = false
	}
	return moved
}

func (a *App) anyTabLoading() bool {
	for _, id := range a.model.Order(a.winID) {
		if t, ok := a.model.Tab(id); ok && t.Loading {
			return true
		}
	}
	return false
}

func cloneInt64(m map[string]int64) map[string]int64 {
	out := make(map[string]int64, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// dataAccumulateTabs keeps per-tab running totals for the tabs the agent
// uses and for the active tab; true when something changed.
func (a *App) dataAccumulateTabs() bool {
	d := &a.dat
	changed := false
	ids := map[string]bool{}
	for id := range d.agent.touched {
		ids[id] = true
	}
	if id := a.model.Active(a.winID); id != "" {
		ids[id] = true
	}
	for id := range ids {
		e := a.page(id)
		if e == nil {
			continue
		}
		cur := d.eng.TabStats(e.t).Attributed()
		acc := d.tabAcc[id]
		if acc == nil {
			acc = &tabAcc{}
			d.tabAcc[id] = acc
		}
		if cur < acc.last { // the tab navigated and its counters restarted
			acc.banked += acc.last
		}
		if cur != acc.last {
			changed = true
		}
		acc.last = cur
		if sc := d.eng.TabStats(e.t).Skipped["image"]; int32(sc) != e.liteSkipped.Load() {
			e.liteSkipped.Store(int32(sc))
			changed = true
		}
	}
	return changed
}

// dataBankTab folds a tab's current numbers into its running total right
// before it navigates, so a restart of the engine counters loses nothing.
func (a *App) dataBankTab(id string) {
	d := &a.dat
	acc := d.tabAcc[id]
	e := a.page(id)
	if acc == nil || e == nil {
		return
	}
	acc.banked += d.eng.TabStats(e.t).Attributed()
	acc.last = 0
}

func (a *App) tabTotal(id string) int64 {
	if acc := a.dat.tabAcc[id]; acc != nil {
		return acc.banked + acc.last
	}
	return 0
}

// ---- budget ------------------------------------------------------------------------------------

func (a *App) dataBudget() store.Budget { return a.prof.Settings.Budget() }

func (a *App) dataRefreshCache() {
	d := &a.dat
	now := a.dataNow()
	b := a.dataBudget()
	day := store.DayKey(now) + "|" + b.PeriodKey(now) + fmt.Sprint(b.ResetDay) // a new day or a changed period recomputes
	if d.cache.valid && d.cache.day == day {
		return
	}
	d.cache.start, d.cache.end = b.PeriodStart(now), b.PeriodEnd(now)
	d.cache.totals = a.prof.Data.Totals(d.cache.start, d.cache.end)
	d.cache.top = a.prof.Data.Top(5, d.cache.start, d.cache.end)
	d.cache.today = a.prof.Data.Today()
	d.cache.day, d.cache.valid = day, true
}

// dataStatus is the budget maths right now.
func (a *App) dataStatus() store.BudgetStatus {
	a.dataRefreshCache()
	return a.dataBudget().StatusFor(a.dat.cache.totals.Used, a.dataNow())
}

// dataCheckThresholds announces each warn threshold and the exhaustion once
// per period (the ledger remembers what it already announced, across
// restarts).
func (a *App) dataCheckThresholds() {
	b := a.dataBudget()
	if !b.Enabled {
		return
	}
	st := a.dataStatus()
	for _, ev := range a.prof.Data.Check(b, st) {
		switch ev.Kind {
		case "warn":
			a.dataWarnToast(b, st, ev.Pct)
		case "exhausted":
			a.dataExhausted(b, st)
		}
	}
}

// fmtBytes writes a size exactly the way the UI's SW.fmtBytes does (one
// decimal below 10, none above), so a toast and the meter never disagree.
func fmtBytes(n int64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	v, i := float64(n), 0
	for v >= 1024 && i < 4 {
		v /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d B", int64(math.Round(v)))
	}
	if v < 10 {
		return fmt.Sprintf("%.1f %s", v, units[i])
	}
	return fmt.Sprintf("%.0f %s", v, units[i])
}

func periodWord(p string) string {
	switch p {
	case "day":
		return "today"
	case "week":
		return "this week"
	}
	return "this month"
}

func (a *App) dataWarnToast(b store.Budget, st store.BudgetStatus, pct int) {
	left := fmtBytes(st.LeftBytes)
	var text string
	kind := "info"
	if pct >= 90 {
		kind = "warn"
		text = fmt.Sprintf("About %s left %s. It resets %s.", left, periodWord(b.Period), resetPhrase(st))
	} else {
		text = fmt.Sprintf("You have used %d%% of your %s %s. %s left.", pct, limitText(b.LimitMB), periodWord(b.Period), left)
	}
	m := map[string]any{"text": text, "kind": kind}
	if a.dataLevel() != lvlStrict {
		m["action"] = map[string]any{"label": "Switch to Text first", "type": "data.setLevel", "payload": map[string]any{"level": "strict"}}
	}
	a.sendChrome(mToast, m)
}

func limitText(mb int) string {
	if mb >= 1024 && mb%1024 == 0 {
		return fmt.Sprintf("%d GB", mb/1024)
	}
	return fmt.Sprintf("%d MB", mb)
}

func resetPhrase(st store.BudgetStatus) string {
	if st.ResetsInDays <= 1 {
		return "tomorrow"
	}
	return fmt.Sprintf("in %d days", st.ResetsInDays)
}

// dataExhausted tells the chrome the budget is used up (data.exhausted) and
// shows a calm toast. With the hard stop on, later page loads stop at the
// interstitial.
func (a *App) dataExhausted(b store.Budget, st store.BudgetStatus) {
	a.sendChrome("data.exhausted", map[string]any{
		"limitMB": b.LimitMB, "usedMB": float64(st.UsedBytes) / store.MB, "period": b.Period,
		"resetsInDays": st.ResetsInDays, "hardStop": b.HardStop,
	})
	text := fmt.Sprintf("You have used your %s %s. It resets %s.", limitText(b.LimitMB), periodWord(b.Period), resetPhrase(st))
	if !b.HardStop {
		text += " Pages still load."
	}
	a.sendChrome(mToast, map[string]any{"text": text, "kind": "warn",
		"action": map[string]any{"label": "Raise limit", "type": "tab.new", "payload": map[string]any{"url": "sweef://settings#data"}}})
}

// ---- hard stop ---------------------------------------------------------------------------------

// dataStopFor reports whether loading raw must stop at the interstitial. The
// person's "Load once" for exactly that URL (set by dataLoadOnce) is honoured
// and consumed here. Only web pages stop; sweef:// pages never do.
func (a *App) dataStopFor(e *etab, raw string) bool {
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		return false
	}
	if once, _ := e.loadOnce.Load().(string); once != "" {
		e.loadOnce.Store("")
		if once == raw {
			return false
		}
	}
	b := a.dataBudget()
	if !b.Enabled || !b.HardStop {
		return false
	}
	return a.dataStatus().Exhausted
}

// dataStopURL is the interstitial's address.
func dataStopURL(target string) string {
	return "sweef://datastop?url=" + url.QueryEscape(target)
}

// renderDataStop shows the interstitial in place of target (worker only).
func (a *App) renderDataStop(e *etab, target string) {
	pageURL := dataStopURL(target)
	body, err := a.asset("datastop/index.html")
	htmlSrc := string(body)
	if err != nil {
		htmlSrc = builtinDataStopHTML(target)
	}
	a.prepareInternal(e, pageURL)
	e.t.SetDocument(htmlSrc, pageURL)
}

func builtinDataStopHTML(target string) string {
	return `<!doctype html><html><head><meta charset="utf-8"><title>Data limit reached</title></head><body>
<h1>You have used your data for now</h1><p>` + htmlEscape(target) + `</p></body></html>`
}

func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

// dataStopData is injected into the interstitial before it loads.
func (a *App) dataStopData(u *url.URL) map[string]any {
	b := a.dataBudget()
	st := a.dataStatus()
	target := u.Query().Get("url")
	return map[string]any{
		"url": target, "host": displayTitle(target),
		"limitMB": b.LimitMB, "usedMB": float64(st.UsedBytes) / store.MB, "period": b.Period,
		"resetsInDays": st.ResetsInDays, "resetsOn": st.ResetsAt.Format("2006-01-02"),
		"incognito": a.cfg.Incognito,
	}
}

// ---- messages from the UI --------------------------------------------------------------------

// onDataMessage handles the data.* messages. It returns false when typ is not
// one of them.
func (a *App) onDataMessage(typ string, raw json.RawMessage, tabID string) bool {
	switch typ {
	case "data.setLevel":
		p := decode[struct {
			Level string `json:"level"`
		}](raw)
		a.setDataSetting("data.level", p.Level)
	case "data.setBudget":
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		cur := a.dataBudget().Map()
		for k, v := range m {
			cur[k] = v
		}
		a.setDataSetting("data.budget", cur)
	case "data.loadFully":
		a.dataLoadFully(tabID)
	case "data.loadSkipped":
		p := decode[struct {
			NodeID *int64 `json:"nodeId"`
		}](raw)
		a.dataLoadSkipped(tabID, p.NodeID)
	case "data.siteRule":
		p := decode[struct {
			Origin string `json:"origin"`
			Rule   string `json:"rule"`
		}](raw)
		a.dataSetSiteRule(p.Origin, p.Rule)
	case "data.reset":
		a.prof.Data.Reset()
		a.dat.cache.valid = false
		a.markDataState()
	case "data.loadOnce":
		p := decode[struct {
			URL string `json:"url"`
		}](raw)
		a.dataLoadOnce(tabID, p.URL)
	case "data.wait":
		a.dataWait(tabID)
	default:
		return false
	}
	return true
}

func (a *App) setDataSetting(key string, v any) {
	if err := a.prof.Settings.Set(key, v); err != nil {
		a.sendChrome(mToast, map[string]any{"text": err.Error(), "kind": "error"})
	}
}

func (a *App) dataSetSiteRule(origin, rule string) {
	o := store.OriginOf(origin)
	if o == "" {
		return
	}
	sites := a.prof.Settings.DataSites()
	if rule == "full" || rule == "lite" {
		sites[o] = rule
	} else {
		delete(sites, o) // anything else forgets the rule
	}
	m := make(map[string]any, len(sites))
	for k, v := range sites {
		m[k] = v
	}
	a.setDataSetting("data.sites", m)
}

// dataLoadFully reloads the tab's page once without any saving. The override
// lasts until the tab's next navigation.
func (a *App) dataLoadFully(tabID string) {
	st, ok := a.model.Tab(tabID)
	if !ok || !strings.HasPrefix(st.URL, "http") {
		return
	}
	if e := a.page(tabID); e != nil {
		e.fullOnce.Store(true)
		a.loadURL(tabID, st.URL, modeReplace, false)
	}
}

// dataLoadOnce lets the one load of url through even though the budget is
// used up (the bytes still count).
func (a *App) dataLoadOnce(tabID, raw string) {
	e := a.page(tabID)
	st, ok := a.model.Tab(tabID)
	if e == nil || !ok {
		return
	}
	if raw == "" {
		raw = st.URL
	}
	if !strings.HasPrefix(raw, "http") {
		return
	}
	e.loadOnce.Store(raw)
	a.loadURL(tabID, raw, modeReplace, false)
}

// dataWait leaves the interstitial: back to where the person came from, or the
// new tab page.
func (a *App) dataWait(tabID string) {
	st, ok := a.model.Tab(tabID)
	if !ok {
		return
	}
	if st.CanBack {
		a.traverse(tabID, -1)
		return
	}
	a.loadURL(tabID, newTabURL, modeReplace, false)
}

// dataLoadSkipped fetches tap-to-load resources on a tab. With nodeID nil it
// loads every withheld image on the page ("Load images on this page").
func (a *App) dataLoadSkipped(tabID string, nodeID *int64) {
	e := a.page(tabID)
	if e == nil {
		return
	}
	d := a.dat.eng
	e.w.do(func() {
		if nodeID != nil {
			_ = d.LoadSkipped(e.t, *nodeID)
		} else {
			_, _ = d.LoadSkippedImages(e.t)
		}
		e.drainBindings()
		e.paint(true)
		a.post(a.dataKick)
	})
}

// dataTapAt resolves a click on a placeholder (its index among the page's
// skipped elements, in document order) to the engine node and loads it.
// Worker only.
func (a *App) dataTapAt(e *etab, idx int) {
	nodes, err := e.t.QuerySelectorAll(`[data-sweef-lite="skipped"]`)
	if err != nil || idx < 0 || idx >= len(nodes) {
		return
	}
	_ = a.dat.eng.LoadSkipped(e.t, nodes[idx].BackendID)
	e.drainBindings()
	e.paint(true)
	a.post(a.dataKick)
}

// ---- reacting to settings ---------------------------------------------------------------------

func (a *App) onDataSetting(key string) {
	switch key {
	case "data.level", "data.sites", "data.agentLite":
		a.applyDataPolicy()
	case "data.budget":
		a.applyDataPolicy() // "auto" follows the budget
		a.dat.cache.valid = false
		a.dataRefreshCache()
		if b := a.dataBudget(); b.Enabled {
			a.prof.Data.Rebaseline(b, a.dataStatus()) // saving a budget never answers with a toast
		}
	}
	a.markDataState()
}

func (a *App) markDataState() { a.markState() }

// ---- state.data ---------------------------------------------------------------------------------

// dataState builds state.data for the chrome (docs/data-saver.md and
// docs/gui-contract.md "Data Saver"). Numbers are bytes unless the name says MB.
func (a *App) dataState() map[string]any {
	a.dataRefreshCache()
	d := &a.dat
	b := a.dataBudget()
	st := a.dataStatus()
	tot := d.cache.totals
	budget := b.Map()
	budget["usedMB"] = float64(st.UsedBytes) / store.MB
	budget["leftMB"] = float64(st.LeftBytes) / store.MB
	budget["pct"] = st.Pct
	budget["resetsInDays"] = st.ResetsInDays
	budget["resetsOn"] = st.ResetsAt.Format("2006-01-02")
	budget["exhausted"] = st.Exhausted
	budget["usedBytes"] = st.UsedBytes
	budget["limitBytes"] = st.LimitBytes

	// a private window never shows the sites seen in normal ones
	priv := a.cfg.Incognito
	if at, ok := a.activeTab(); ok && at.Incognito {
		priv = true
	}
	top := make([]map[string]any, 0, 5)
	for _, o := range d.cache.top {
		if priv {
			break
		}
		top = append(top, map[string]any{"origin": o.Origin, "bytes": o.Bytes})
	}

	out := map[string]any{
		"level":        a.dataLevel(),
		"levelSetting": a.prof.Settings.Str("data.level"),
		"available":    d.eng.Available(),
		"used":         tot.Used,
		"usedToday":    d.cache.today.All(),
		"budget":       budget,
		"top":          top,
		"saved":        map[string]any{"measured": tot.SavedM, "estimated": tot.SavedE},
		"agentLite":    a.prof.Settings.Bool("data.agentLite"),
		"showMeter":    a.prof.Settings.Bool("data.showMeter"),
		"sites":        siteRulesJSON(a.prof.Settings.DataSites()),
		"incognito":    priv,
	}
	tab, kinds := a.dataTabState()
	out["tab"] = tab
	out["kinds"] = kinds
	return out
}

func siteRulesJSON(m map[string]string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// dataTabState is the active tab's part of state.data: the measured/attributed
// bytes this page used, what was skipped, and the saved numbers with the
// measured and estimated parts kept apart.
func (a *App) dataTabState() (map[string]any, map[string]any) {
	tab := map[string]any{"used": int64(0), "bySrc": []any{}, "skipped": 0, "skippedBy": map[string]any{},
		"saved": map[string]any{"measured": int64(0), "estimated": int64(0)}, "approx": true,
		"rule": "", "once": false, "canLoad": 0}
	kinds := map[string]any{}
	id := a.model.Active(a.winID)
	st, ok := a.model.Tab(id)
	e := a.page(id)
	if !ok || e == nil {
		return tab, kinds
	}
	tab["origin"] = store.OriginOf(st.URL)
	tab["rule"] = a.siteRule(st.URL)
	tab["once"] = e.fullActive.Load() || a.dat.eng.FullyLoaded(e.t)
	s := a.dat.eng.TabStats(e.t)
	tab["used"] = s.Attributed()
	if tab["used"].(int64) == 0 {
		tab["used"] = s.Wire()
	}
	tab["skipped"] = s.SkippedCount()
	sb := map[string]any{}
	for k, v := range s.Skipped {
		sb[k] = v
	}
	tab["skippedBy"] = sb
	tab["canLoad"] = int(e.liteSkipped.Load())
	tab["saved"] = map[string]any{"measured": s.MeasuredSaved(), "estimated": s.SkippedEstimate}
	src := make([]store.OriginBytes, 0, len(s.ByOrigin))
	for o, n := range s.ByOrigin {
		src = append(src, store.OriginBytes{Origin: o, Bytes: n})
	}
	sort.Slice(src, func(i, j int) bool {
		if src[i].Bytes != src[j].Bytes {
			return src[i].Bytes > src[j].Bytes
		}
		return src[i].Origin < src[j].Origin
	})
	rows := make([]any, 0, 5)
	for i, o := range src {
		if i == 5 {
			break
		}
		rows = append(rows, map[string]any{"origin": o.Origin, "bytes": o.Bytes})
	}
	tab["bySrc"] = rows
	for k, v := range s.ByKind {
		kinds[k] = v
	}
	return tab, kinds
}

// ---- agent -----------------------------------------------------------------------------------------

// AgentData is what a research run cost, from the agent tabs' own counters.
type AgentData struct {
	Bytes          int64  // attributed transfer bytes of the tabs the agent used
	SavedMeasured  int64  // cache hits and aborted responses (0 when unknown)
	SavedEstimated int64  // ESTIMATE; always shown as one
	Skipped        int    // requests skipped by policy
	Lite           bool   // the run loaded pages in agent mode
	Available      bool   // false when the engine does not count (Bytes is then meaningless)
	Text           string // "Research used 412 KB"
}

// AgentDataBegin marks the start of a run. Tabs the agent touches from now on
// are measured from their numbers at that moment.
//
// It queues the work and returns at once, so the host can call it from the UI
// loop (Send runs there); the agent's first navigation is queued after it.
func (a *App) AgentDataBegin() {
	a.post(func() {
		a.dat.agent.active = true
		a.dat.agent.touched = map[string]int64{}
	})
}

// agentTouch records that the agent uses tab id (UI loop).
func (a *App) agentTouch(id string) {
	d := &a.dat
	if !d.agent.active {
		return
	}
	if _, ok := d.agent.touched[id]; !ok {
		d.agent.touched[id] = a.tabTotal(id)
		a.dataMarkBackground(id, false)
	}
}

// AgentDataEnd closes the run's measurement and returns it. The tabs go back
// to the person's own policy for whatever they load next.
func (a *App) AgentDataEnd() AgentData {
	var res AgentData
	_ = a.onLoop(context.Background(), func() {
		d := &a.dat
		a.dataAccumulateTabs()
		res.Available = d.eng.Available()
		res.Lite = a.prof.Settings.Bool("data.agentLite")
		for id, base := range d.agent.touched {
			res.Bytes += max(a.tabTotal(id)-base, 0)
			if e := a.page(id); e != nil {
				s := d.eng.TabStats(e.t)
				res.SavedMeasured += s.MeasuredSaved()
				res.SavedEstimated += s.SkippedEstimate
				res.Skipped += s.SkippedCount()
				e.agentLoad.Store(false)
				if st, ok := a.model.Tab(id); ok {
					d.eng.SetTabPolicy(e.t, a.tabLevelFor(e, st.URL))
				}
			}
		}
		d.agent.active = false
		d.agent.touched = map[string]int64{}
		res.Text = "Research used " + fmtBytes(res.Bytes)
		a.dataKick()
	})
	return res
}
