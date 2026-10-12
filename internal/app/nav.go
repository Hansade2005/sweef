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
		a.agentSend(res.Query, "ask")
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
	e.w.do(func() {
		a.prepareInternal(e, target)
		err := e.t.Navigate(target, nil)
		fellBack := false
		if err != nil && upgraded && e.navSeq.Load() == seq {
			// the site does not speak HTTPS: fall back to the URL the user asked for
			e.navTarget.Store(orig)
			e.topURL.Store(orig)
			if err2 := e.t.Navigate(orig, nil); err2 == nil {
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
		if err != nil {
			a.renderErrorPage(e, shown, err)
			isErr = true
		}
		e.drainBindings()
		meta := a.collectMeta(e, isErr)
		if fellBack {
			a.post(func() {
				a.sendChrome(mToast, map[string]any{"text": "This site does not support HTTPS; loaded over HTTP", "kind": "warn"})
			})
		}
		e.paint(true)
		a.post(func() { a.onLoaded(e, seq, shown, mode, typed, meta) })
		e.armPump()
	})
}

// pageMeta is what the worker reads after a load.
type pageMeta struct {
	URL, Title, Icon string
	DocH             int
	Audio            bool
	Err              bool
}

func (a *App) collectMeta(e *etab, isErr bool) pageMeta {
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
	_ = e.eval(`__sweefDocH()`, &m.DocH)
	_ = e.eval(`__sweefMedia()`, &m.Audio)
	return m
}

func securityFor(raw string, errPage bool) state.Security {
	if errPage {
		return state.Security{Level: "error"}
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
	if mode == modeTraverse || mode == modeReplace {
		a.model.Navigated(e.id, final, true)
	} else {
		a.model.Navigated(e.id, final, false)
	}
	e.topURL.Store(final)
	title := m.Title
	if title == "" {
		title = displayTitle(final)
	}
	a.model.Update(e.id, func(t *state.Tab) {
		t.Loading, t.Progress, t.Title, t.Audio = false, 1, title, m.Audio
		t.Security = securityFor(final, m.Err)
		t.Blocked = int(e.blocked.Load())
	})
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
	a.model.Update(tabID, func(t *state.Tab) { t.Loading, t.Progress = false, 0 })
	a.markState()
}

// ---- error pages and view-source ------------------------------------------------------------

func (a *App) renderErrorPage(e *etab, raw string, err error) {
	code, title := classifyError(err)
	q := url.Values{"code": {code}, "url": {raw}, "msg": {err.Error()}, "title": {title}}
	pageURL := "sweef://error?" + q.Encode()
	body, aerr := a.asset("error/index.html")
	htmlSrc := string(body)
	if aerr != nil {
		htmlSrc = builtinErrorHTML(code, title, raw, err.Error())
	}
	a.prepareInternal(e, pageURL)
	e.t.SetDocument(htmlSrc, pageURL)
}

func classifyError(err error) (code, title string) {
	s := strings.ToLower(err.Error())
	switch {
	case strings.Contains(s, "no such host"):
		return "ERR_NAME_NOT_RESOLVED", "This site can't be reached"
	case strings.Contains(s, "connection refused"):
		return "ERR_CONNECTION_REFUSED", "Connection refused"
	case strings.Contains(s, "x509") || strings.Contains(s, "certificate"):
		return "ERR_CERT_INVALID", "Your connection is not private"
	case strings.Contains(s, "timeout") || strings.Contains(s, "deadline"):
		return "ERR_TIMED_OUT", "The page took too long to respond"
	case strings.Contains(s, "offline"):
		return "ERR_INTERNET_DISCONNECTED", "You are offline"
	case strings.Contains(s, "aborted by route"):
		return "ERR_BLOCKED", "Blocked"
	}
	return "ERR_FAILED", "This page can't be loaded"
}

func builtinErrorHTML(code, title, raw, msg string) string {
	return fmt.Sprintf(`<!doctype html><html><head><meta charset="utf-8"><title>%s</title>
<style>body{font:16px system-ui,sans-serif;margin:0;padding:12vh 8vw;color:#222;background:#fafafa}
h1{font-size:28px;margin:0 0 12px}code{background:#eee;padding:2px 6px;border-radius:4px}.c{color:#777;font-size:13px}
button{margin-top:18px;font:inherit;padding:8px 16px}</style></head>
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
