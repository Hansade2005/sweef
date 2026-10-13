package app

import (
	"fmt"
	"html"
	"net/url"
	"strings"
	"time"

	"github.com/sweefbrowser/sweef/internal/app/omnibox"
	"github.com/sweefbrowser/sweef/internal/app/state"
	"github.com/sweefbrowser/sweef/internal/engine"
)

// load modes
const (
	modeNone int32 = iota
	modePush
	modeReplace
	modeTraverse
)

// navigateInput handles text typed into the omnibox.
func (a *App) navigateInput(tabID, input string) {
	res := omnibox.Classify(input, a.engineSearch())
	switch res.Kind {
	case omnibox.KindEmpty:
		return
	case omnibox.KindAsk:
		a.setSidebar(true)
		a.agentSend(agentRequest{Text: res.Query, Mode: "ask", Source: "omnibox"})
		return
	}
	if tabID == "" {
		a.newTab(res.URL, false, true)
		return
	}
	a.setFocus("page")
	a.loadURL(tabID, res.URL, modePush, res.Kind == omnibox.KindURL)
}

// externalScheme reports schemes that belong to the OS, not the browser.
func externalScheme(raw string) bool {
	i := strings.Index(raw, ":")
	if i <= 0 {
		return false
	}
	switch strings.ToLower(raw[:i]) {
	case "http", "https", "file", "about", "sweef", "data", "blob", "view-source", "ftp", "javascript":
		return false
	}
	return true
}

// loadURL starts navigating a tab. mode says how history is affected.
func (a *App) loadURL(tabID, target string, mode int32, typed bool) {
	e := a.page(tabID)
	if e == nil {
		return
	}
	if externalScheme(target) {
		if err := a.win.OpenURL(target); err != nil {
			a.sendChrome(mToast, map[string]any{"text": "Could not open " + target, "kind": "error"})
		}
		return
	}
	if strings.HasPrefix(target, "view-source:") {
		a.viewSource(e, strings.TrimPrefix(target, "view-source:"))
		return
	}
	if strings.HasPrefix(strings.ToLower(target), "javascript:") {
		return
	}
	// Data Saver: a used-up budget with the hard stop on shows the interstitial
	// instead of the page; "Load fully, once" and the agent mode set this
	// load's policy (cleared again by the next load).
	stop := a.dataStopFor(e, target)
	fullOnce := e.fullOnce.Swap(false)
	e.fullActive.Store(fullOnce) // shown in state.data.tab.once until the next navigation
	level := a.tabLevelFor(e, target)
	if a.dat.agent.active {
		a.agentTouch(tabID)
	}
	a.dataBankTab(tabID)
	orig := target
	upgraded := false
	if site := a.prof.Sites.Rule(target); !site.NoUpgrade {
		if up, ok := a.priv.Upgrade(target); ok {
			target, upgraded = up, true
		}
	}
	seq := e.navSeq.Add(1)
	e.navTarget.Store(target)
	e.topURL.Store(target)
	e.reqN.Store(0)
	e.respN.Store(0)
	e.blocked.Store(0)
	e.loadMode.Store(mode)
	e.scrollCSS.Store(0)
	a.model.Update(tabID, func(t *state.Tab) {
		t.Loading, t.Progress, t.URL, t.Blocked = true, 0.05, target, 0
		t.Security = securityFor(target, false)
		t.Favicon = ""
	})
	if a.model.Active(a.winID) == tabID {
		a.resetScroll()
	}
	a.markState()
	a.stats.navStart(tabID, seq)
	// The window shows the page while it loads (first frame once the HTML and its stylesheets are in, the rest
	// in slices between paints and input). The agent drives its tabs headless-style: it waits for a settled
	// page, so those loads, internal pages and the Data Saver's "load fully" run to the end before the first
	// paint, as before.
	progressive := !e.agentLoad.Load() && !fullOnce && (strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://"))
	e.t.SetAsyncImages(!e.agentLoad.Load())
	e.t.SetProgressiveNav(progressive)
	e.w.do(func() {
		if stop {
			a.dataStopLoad(e, seq, target, mode)
			return
		}
		a.dat.eng.SetTabPolicy(e.t, level)
		a.prepareInternal(e, target)
		var err error
		switch {
		case fullOnce:
			err = a.dat.eng.LoadFully(e.t) // reloads this page with everything on, once
		case progressive:
			err = e.t.NavigateProgressive(target, nil)
		default:
			err = e.t.Navigate(target, nil)
		}
		fellBack := false
		if err != nil && upgraded && e.navSeq.Load() == seq {
			// the site does not speak HTTPS: fall back to the URL the user asked for
			e.navTarget.Store(orig)
			e.topURL.Store(orig)
			var err2 error
			if progressive {
				err2 = e.t.NavigateProgressive(orig, nil)
			} else {
				err2 = e.t.Navigate(orig, nil)
			}
			if err2 == nil {
				err, fellBack = nil, true
			}
		}
		if e.navSeq.Load() != seq {
			return // superseded or stopped: drop the result
		}
		shown := target
		if fellBack {
			shown = orig
		}
		isErr := false
		e.certError.Store(false)
		if err != nil {
			a.renderErrorPage(e, shown, err)
			isErr = true
			e.loadErr.Store(err.Error())
		}
		e.drainBindings()
		meta := a.collectMeta(e, isErr)
		if fellBack {
			a.post(func() {
				a.sendChrome(mToast, map[string]any{"text": "This site does not support HTTPS; loaded over HTTP", "kind": "warn"})
			})
		}
		e.paint(true)
		if !isErr && e.t.LoadPending() {
			// first frame is out; the title and address are committed now, the load finishes in the background
			a.post(func() { a.onCommitted(e, seq, shown, mode, meta) })
			e.driveLoad(seq, func() {
				m := a.collectMeta(e, false)
				a.post(func() { a.onLoaded(e, seq, shown, mode, typed, m) })
			})
			return
		}
		a.post(func() { a.onLoaded(e, seq, shown, mode, typed, meta) })
		e.armPump()
	})
}

// Progressive loading: the worker steps the tab's pending load in short slices, each a worker op of its own so
// input, scroll and paint requests queue in between, and repaints the document as it changes (images, scripts).
const (
	loadSlice    = 25 * time.Millisecond  // engine work per op
	loadPaintGap = 150 * time.Millisecond // minimum time between repaints of a loading page
)

// requeue queues f behind what is waiting. Called from the worker itself it must not block on a full queue.
func (e *etab) requeue(f func()) {
	if len(e.w.ops) < cap(e.w.ops)/2 {
		e.w.do(f)
		return
	}
	go e.w.do(f)
}

// driveLoad steps e's pending progressive load until it is done. onLoad runs once, when the page's load
// event has fired (or the load ended): the spinner stops and the visit is recorded then, not at the end of
// the settle budget. Worker only; seq is the navigation this belongs to. One navigation drives at a time: a
// newer one takes over (the older chain notices at its next step and ends without touching the tab).
func (e *etab) driveLoad(seq int64, onLoad func()) {
	if e.driveSeq.Load() == seq {
		return // already driven
	}
	e.driveSeq.Store(seq)
	loaded := false
	last := time.Now()
	gen := e.t.Generation()
	var step func()
	finish := func() {
		if !loaded && onLoad != nil {
			onLoad()
		}
		if e.navSeq.Load() == seq {
			e.paint(false)
		}
		e.driveSeq.CompareAndSwap(seq, 0)
		e.armPump()
	}
	step = func() {
		if e.driveSeq.Load() != seq || e.navSeq.Load() != seq {
			e.driveSeq.CompareAndSwap(seq, 0) // superseded (a newer load drives, or the stop button was pressed)
			return
		}
		if e.closed.Load() || e.a.closing.Load() {
			e.driveSeq.CompareAndSwap(seq, 0)
			return
		}
		more, wait := e.t.LoadStep(loadSlice)
		e.drainBindings()
		if !loaded && (!more || e.t.LoadLoaded()) {
			loaded = true
			if onLoad != nil {
				onLoad()
			}
		}
		if g := e.t.Generation(); g != gen && time.Since(last) >= loadPaintGap {
			e.paint(false)
			gen, last = g, time.Now()
		}
		if !more {
			finish()
			return
		}
		if wait <= 0 {
			e.requeue(step)
			return
		}
		e.a.pending.Add(1) // a load waiting for a timer is not idle
		time.AfterFunc(wait, func() {
			e.w.do(step)
			e.a.pending.Add(-1)
		})
	}
	e.w.do(step)
}

// dataStopLoad shows the hard-stop interstitial for target (worker only).
func (a *App) dataStopLoad(e *etab, seq int64, target string, mode int32) {
	if e.navSeq.Load() != seq {
		return
	}
	a.renderDataStop(e, target)
	e.loadErr.Store("data limit reached: the budget is used up and the hard stop is on")
	e.certError.Store(false)
	e.drainBindings()
	meta := a.collectMeta(e, true)
	e.paint(true)
	a.post(func() { a.onLoaded(e, seq, target, mode, false, meta) })
	e.armPump()
}

// pageMeta is what the worker reads after a load.
type pageMeta struct {
	URL, Title, Icon string
	DocH             int
	Audio            bool
	Err              bool
	PageType         string
	ConsoleErrors    int
}

func (a *App) collectMeta(e *etab, isErr bool) pageMeta {
	e.sweepThirdPartyCookies()
	m := pageMeta{URL: e.t.URL(), Title: e.t.Title(), Err: isErr}
	var meta struct {
		Icon  string `json:"icon"`
		Title string `json:"title"`
	}
	if e.eval(`__sweefMeta()`, &meta) == nil {
		m.Icon = meta.Icon
		if m.Title == "" {
			m.Title = meta.Title
		}
	}
	var loc string
	if e.eval(`location.href`, &loc) == nil && loc != "" && !isErr {
		m.URL = loc // history.replaceState/pushState during load changes location, not the engine's URL
	}
	_ = e.eval(`__sweefDocH()`, &m.DocH)
	_ = e.eval(`__sweefMedia()`, &m.Audio)
	if !isErr {
		_ = e.eval(`__sweefPageType()`, &m.PageType)
		for _, ev := range e.t.ConsoleLog(0) {
			if d, ok := ev.Data.(map[string]string); ok && d["level"] == "error" {
				m.ConsoleErrors++
			}
		}
	}
	return m
}

func securityFor(raw string, errPage bool) state.Security {
	if errPage {
		return state.Security{Level: "none"}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return state.Security{Level: "none"}
	}
	switch u.Scheme {
	case "https":
		// Go's TLS stack verified the chain against the system roots or the
		// request would have failed; the engine does not expose the issuer.
		return state.Security{Level: "secure", HTTPS: true, CertValid: true}
	case "http":
		return state.Security{Level: "insecure"}
	case "sweef", "about":
		return state.Security{Level: "internal"}
	}
	return state.Security{Level: "none"}
}

// onCommitted is the first half of onLoaded for a page that is shown while it loads: the address, history
// entry, title and security state are those of the document that painted its first frame.
func (a *App) onCommitted(e *etab, seq int64, shown string, mode int32, m pageMeta) {
	if e.navSeq.Load() != seq || e.closed.Load() {
		return
	}
	final := m.URL
	if final == "" || final == "about:blank" {
		final = shown
	}
	a.model.Navigated(e.id, final, mode == modeTraverse || mode == modeReplace)
	e.commitSeq, e.commitURL = seq, final
	e.topURL.Store(final)
	title := m.Title
	if title == "" {
		title = displayTitle(final)
	}
	a.model.Update(e.id, func(t *state.Tab) {
		t.Title = title
		t.Security = securityFor(final, false)
		if t.Progress < 0.7 {
			t.Progress = 0.7
		}
	})
	a.markState()
}

func (a *App) onLoaded(e *etab, seq int64, shown string, mode int32, typed bool, m pageMeta) {
	if e.navSeq.Load() != seq || e.closed.Load() {
		return
	}
	final := m.URL
	if final == "" || final == "about:blank" {
		final = shown
	}
	if m.Err {
		final = shown
	}
	switch {
	case e.commitSeq != seq:
		a.model.Navigated(e.id, final, mode == modeTraverse || mode == modeReplace)
	case e.commitURL != final:
		a.model.Navigated(e.id, final, true) // the page moved itself (replaceState, a redirect in script) while loading
	}
	e.topURL.Store(final)
	title := m.Title
	if title == "" {
		title = displayTitle(final)
	}
	a.model.Update(e.id, func(t *state.Tab) {
		t.Loading, t.Progress, t.Title, t.Audio = false, 1, title, m.Audio
		t.Security = securityFor(final, m.Err)
		if m.Err && e.certError.Load() {
			t.Security = state.Security{Level: "warning"}
		}
		t.Blocked = int(e.blocked.Load())
	})
	if !strings.HasPrefix(final, "sweef://") {
		delete(a.uiPages, e) // no longer an internal page: stop sending it UI state
	}
	a.pageTypes.set(e.id, m.PageType)
	a.compatErrs[e.id] = m.ConsoleErrors
	e.loadMode.Store(modeNone)
	if m.Err {
		a.markState()
		return
	}
	if p := a.profileFor(e.incog); p != nil && !strings.HasPrefix(final, "sweef:") && !strings.HasPrefix(final, "about:") && !strings.HasPrefix(final, "view-source:") && !strings.HasPrefix(final, "data:") {
		if mode != modeTraverse {
			p.History.Visit(final, title, typed)
		} else {
			p.History.Visit(final, title, false)
		}
		p.Stats.AddPage()
	}
	if a.model.Active(a.winID) == e.id {
		a.onPageMetrics(e.id, m.DocH)
		a.applyRestoreScroll(e)
	}
	if m.Icon != "" || strings.HasPrefix(final, "http") {
		icon := m.Icon
		a.fetchFavicon(e, final, icon)
	}
	a.markState()
}

func displayTitle(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	return strings.TrimPrefix(u.Host, "www.")
}

// onEngineNavigated is called for navigations the engine reports. Ones we
// started ourselves are finished in loadURL; this catches page-initiated
// ones (link clicks, location.href, form posts).
func (a *App) onEngineNavigated(e *etab, raw string) {
	if e.closed.Load() || e.loadMode.Load() != modeNone {
		return
	}
	st, ok := a.model.Tab(e.id)
	if !ok || st.URL == raw {
		return
	}
	seq := e.navSeq.Add(1)
	e.topURL.Store(raw)
	e.navTarget.Store(raw)
	a.model.Update(e.id, func(t *state.Tab) { t.Loading, t.Progress, t.URL = true, 0.3, raw })
	e.loadMode.Store(modePush)
	e.w.do(func() {
		if e.navSeq.Load() != seq {
			return
		}
		a.prepareInternal(e, raw)
		meta := a.collectMeta(e, false)
		e.paint(true)
		if e.t.LoadPending() {
			a.post(func() { a.onCommitted(e, seq, raw, modePush, meta) })
			e.driveLoad(seq, func() {
				m := a.collectMeta(e, false)
				a.post(func() { a.onLoaded(e, seq, raw, modePush, false, m) })
			})
			return
		}
		a.post(func() { a.onLoaded(e, seq, raw, modePush, false, meta) })
		e.armPump()
	})
	a.markState()
}

func (a *App) bumpProgress(e *etab) {
	st, ok := a.model.Tab(e.id)
	if !ok || !st.Loading {
		return
	}
	req, resp := float64(e.reqN.Load()), float64(e.respN.Load())
	p := 0.1
	if req > 0 {
		p = 0.1 + 0.8*resp/req
	}
	if p > st.Progress {
		a.model.Update(e.id, func(t *state.Tab) { t.Progress = p })
		a.markState()
	}
}

func (a *App) noteBlocked(e *etab, host string) {
	if a.closing.Load() {
		return
	}
	a.post(func() {
		a.model.Update(e.id, func(t *state.Tab) { t.Blocked = int(e.blocked.Load()) })
		a.markState()
	})
}

// traverse goes back or forward using the app's own history list (the
// engine's traversal rewrites its list on every step).
func (a *App) traverse(tabID string, delta int) {
	if tabID == "" {
		return
	}
	u, ok := a.model.Traverse(tabID, delta)
	if !ok {
		return
	}
	a.loadURL(tabID, u, modeTraverse, false)
}

func (a *App) reload(tabID string, hard bool) {
	st, ok := a.model.Tab(tabID)
	if !ok || st.URL == "" {
		return
	}
	a.loadURL(tabID, st.URL, modeReplace, false)
}

func (a *App) stopLoading(tabID string) {
	e := a.page(tabID)
	if e == nil {
		return
	}
	// The engine cannot cancel an in-flight fetch; invalidate its result so
	// the UI stops and a late response is dropped.
	e.navSeq.Add(1)
	e.loadMode.Store(modeNone)
	e.w.do(e.t.StopLoading) // what has run stays; the scripts that have not run yet never do
	a.model.Update(tabID, func(t *state.Tab) { t.Loading, t.Progress = false, 0 })
	a.markState()
}

// ---- error pages and view-source ------------------------------------------------------------

func (a *App) renderErrorPage(e *etab, raw string, err error) {
	code, kind := classifyError(err)
	q := url.Values{"kind": {kind}, "url": {raw}, "code": {code}, "detail": {err.Error()}}
	pageURL := "sweef://error?" + q.Encode()
	body, aerr := a.asset("error/index.html")
	htmlSrc := string(body)
	if aerr != nil {
		htmlSrc = builtinErrorHTML(code, kind, raw, err.Error())
	}
	a.prepareInternal(e, pageURL)
	e.t.SetDocument(htmlSrc, pageURL)
	if kind == "cert" {
		e.certError.Store(true)
	}
}

// classifyError maps a load failure to the error page's code and kind
// (offline | dns | cert | timeout).
func classifyError(err error) (code, kind string) {
	s := strings.ToLower(err.Error())
	switch {
	case strings.Contains(s, "no such host"):
		return "ERR_NAME_NOT_RESOLVED", "dns"
	case strings.Contains(s, "x509") || strings.Contains(s, "certificate"):
		return "ERR_CERT_INVALID", "cert"
	case strings.Contains(s, "timeout") || strings.Contains(s, "deadline"):
		return "ERR_TIMED_OUT", "timeout"
	case strings.Contains(s, "connection refused"):
		return "ERR_CONNECTION_REFUSED", "offline"
	case strings.Contains(s, "offline"):
		return "ERR_INTERNET_DISCONNECTED", "offline"
	case strings.Contains(s, "aborted by route"):
		return "ERR_BLOCKED", "offline"
	}
	return "ERR_FAILED", "offline"
}

func builtinErrorHTML(code, kind, raw, msg string) string {
	title := "This page can't be loaded"
	switch kind {
	case "dns":
		title = "This site can't be reached"
	case "cert":
		title = "Your connection is not private"
	case "timeout":
		title = "The page took too long to respond"
	}
	return fmt.Sprintf(`<!doctype html><html><head><meta charset="utf-8"><title>%s</title>
<style>body{font:16px system-ui,sans-serif;margin:0;padding:12vh 8vw;color:#222;background:#fafafa}
h1{font-size:28px;margin:0 0 12px}code{background:#eee;padding:2px 6px;border-radius:4px}.c{color:#777;font-size:13px}</style></head>
<body><h1>%s</h1><p>%s</p><p class="c">%s</p></body></html>`,
		html.EscapeString(title), html.EscapeString(title), html.EscapeString(raw), html.EscapeString(code+": "+msg))
}

func (a *App) viewSource(e *etab, raw string) {
	seq := e.navSeq.Add(1)
	display := "view-source:" + raw
	a.model.Update(e.id, func(t *state.Tab) { t.Loading, t.URL, t.Progress = true, display, 0.2 })
	a.markState()
	e.loadMode.Store(modePush)
	e.w.do(func() {
		status, _, body, err := e.t.Context().Fetch("GET", raw, nil, "")
		if err != nil || status >= 400 {
			if err == nil {
				err = fmt.Errorf("http %d", status)
			}
			a.renderErrorPage(e, display, err)
		} else {
			e.t.SetDocument(`<!doctype html><html><head><meta charset="utf-8"><title>`+html.EscapeString(display)+
				`</title><style>body{margin:0}pre{font:13px/1.45 monospace;margin:0;padding:12px;white-space:pre-wrap;word-break:break-all}</style></head><body><pre>`+
				html.EscapeString(body)+`</pre></body></html>`, display)
		}
		meta := a.collectMeta(e, false)
		meta.URL = display
		e.paint(true)
		a.post(func() { a.onLoaded(e, seq, display, modePush, false, meta) })
	})
}

// ---- popups, downloads -----------------------------------------------------------------------------

func (a *App) onPopup(e *etab, raw string) {
	now := time.Now().UnixNano()
	if now-e.lastInput.Load() > int64(2*time.Second) || now-e.popupAt.Load() < int64(500*time.Millisecond) {
		a.sendChrome(mToast, map[string]any{"text": "A pop-up was blocked", "kind": "info"})
		return
	}
	e.popupAt.Store(now)
	if st, ok := a.model.Tab(e.id); ok {
		if base, err := url.Parse(st.URL); err == nil {
			if ref, err := url.Parse(raw); err == nil {
				raw = base.ResolveReference(ref).String()
			}
		}
	}
	if raw == "" || raw == "about:blank" {
		return
	}
	a.openTab(openOpts{URL: raw, Incognito: e.incog, Activate: true, After: e.id})
}

func (a *App) onEngineDownload(e *etab, d engine.Download) {
	if d.Error != "" {
		a.sendChrome(mToast, map[string]any{"text": "Download failed: " + d.Error, "kind": "error"})
		return
	}
	it := a.profileFor(e.incog).Downloads.Record(d.URL, d.SuggestedFilename, d.Path, d.Size, d.MimeType)
	a.sendChrome(mToast, map[string]any{"text": "Downloaded " + it.Name, "kind": "info"})
}

// startDownload saves a URL ("save link as") with progress/pause/cancel,
// sending the requesting tab's cookies and user agent.
func (a *App) startDownload(raw, name string) {
	act := a.activePage()
	cookie, ua := "", userAgent(a.cfg.UserAgent)
	incog := false
	if act != nil {
		cookie = act.t.Context().JarRef().ForURL(raw)
		ua = act.t.UserAgent()
		incog = act.incog
	}
	if _, err := a.profileFor(incog).Downloads.Start(raw, name, a.downloadsDir(), cookie, ua); err != nil {
		a.sendChrome(mToast, map[string]any{"text": "Could not download: " + err.Error(), "kind": "error"})
	}
}

// onTitle applies a title the page changed after load.
func (a *App) onTitle(id, title string) {
	st, ok := a.model.Tab(id)
	if !ok || st.Loading || st.Title == title {
		return
	}
	a.model.Update(id, func(t *state.Tab) { t.Title = title })
	if p := a.profileFor(st.Incognito); p != nil && strings.HasPrefix(st.URL, "http") {
		p.History.SetTitle(st.URL, title)
	}
	a.markState()
}

// onInPageURL records a same-document URL change (history.pushState, hash
// navigation): a new history entry, no reload.
func (a *App) onInPageURL(e *etab, raw string) {
	st, ok := a.model.Tab(e.id)
	if !ok || st.Loading || st.URL == raw || e.closed.Load() {
		return
	}
	e.topURL.Store(raw)
	a.model.Navigated(e.id, raw, false)
	if p := a.profileFor(st.Incognito); p != nil && strings.HasPrefix(raw, "http") {
		p.History.Visit(raw, st.Title, false) // pushState and hash changes are history entries, as in other browsers
	}
	a.markState()
}
