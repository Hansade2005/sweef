package engine

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	fnet "github.com/sweefbrowser/sweef/internal/net"
)

// Ring is a fixed-capacity circular buffer of events. Bounded memory is the
// whole point: an agent that never reads logs still cannot OOM the browser.
type Ring struct {
	mu    sync.Mutex
	items []Event
	size  int
	head  int
	n     int
	seq   int64
}

func NewRing(size int) *Ring {
	if size <= 0 {
		size = 128
	}
	return &Ring{items: make([]Event, size), size: size}
}

func (r *Ring) Push(e Event) Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	e.SID = r.seq
	i := (r.head + r.n) % r.size
	r.items[i] = e
	if r.n == r.size {
		r.head = (r.head + 1) % r.size // overwrite oldest
	} else {
		r.n++
	}
	return e
}

// Since returns all events with SID > sinceSID, oldest first.
func (r *Ring) Since(sinceSID int64) []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Event, 0, r.n)
	for i := 0; i < r.n; i++ {
		e := r.items[(r.head+i)%r.size]
		if e.SID > sinceSID {
			out = append(out, e)
		}
	}
	return out
}

func (r *Ring) All() []Event { return r.Since(0) }

// Trim halves the live window — called by the memory governor under pressure.
func (r *Ring) Trim() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.n > 1 {
		drop := r.n / 2
		r.head = (r.head + drop) % r.size
		r.n -= drop
	}
}

// NodeInfo is the serialisable handle the outside world uses to address DOM
// nodes. BackendNodeID is stable for the lifetime of the document tree.
type NodeInfo struct {
	NodeID    int64             `json:"nodeId"`
	BackendID int64             `json:"backendNodeId"`
	Name      string            `json:"name"`         // tag name, lowercased
	ID        string            `json:"id,omitempty"` // id attribute
	Type      string            `json:"type,omitempty"`
	Role      string            `json:"role,omitempty"`
	Text      string            `json:"text,omitempty"` // accessible-ish label
	Href      string            `json:"href,omitempty"`
	Attrs     map[string]string `json:"attrs,omitempty"`
	Visible   bool              `json:"visible"`
	Disabled  bool              `json:"disabled"`
	Checked   bool              `json:"checked,omitempty"`
	Box       [4]int            `json:"box,omitempty"` // x,y,w,h in layout px (approx)
	Focusable bool              `json:"focusable"`
}

// Tab is one browsing context. Goroutine-light: no dedicated thread, just
// state + a mutex; JS evaluation happens on demand.
type Tab struct {
	ID      string
	mu      sync.RWMutex
	eng     *Engine
	ctx     *BrowserContext // isolation context (nil = engine default)
	title   string
	url     *url.URL
	html    string
	dom     *fnet.Doc // parsed tree held by our DOM layer
	scripts []*CompiledScript

	consoleRing *Ring
	netRing     *Ring
	stateRing   *Ring
	dialogRing  *Ring

	js      *jsVM // per-document JavaScript VM (nil when JS is disabled)
	history []*url.URL
	hIdx    int

	closed atomic.Bool

	// Emulation state (Playwright page.* setters)
	vpW, vpH    int
	dsf         float64
	mobile      bool
	landscape   bool
	userAgent   string
	geo         *[2]float64
	permissions map[string]bool
	offline     bool
	colorScheme string
	extraHdrs   map[string]string

	initScripts []*CompiledScript
	bindings    []string

	// Dialog response armed by the agent before the dialog fires
	dialogAccept bool
	dialogText   string

	// Interception hooks (route.fulfill / route.abort semantics)
	interceptMu sync.RWMutex
	routes      []*fnet.Route
	routes2     routeTable // RouteRule interception (fulfill/abort/continue)

	lastNet        atomic.Int64 // unix nanos of the last request start (networkidle)
	bindingReplies map[string]string
	bindingRing    *Ring
	downloads      []Download
}

func newTab(e *Engine, id string) *Tab {
	t := &Tab{
		ID:          id,
		eng:         e,
		consoleRing: NewRing(e.opts.ConsoleRingSize),
		netRing:     NewRing(e.opts.NetworkRingSize),
		stateRing:   NewRing(e.opts.StateRingSize),
		dialogRing:  NewRing(64),
		bindingRing: NewRing(256),
		vpW:         e.opts.ViewportWidth,
		vpH:         e.opts.ViewportHeight,
		dsf:         1,
	}
	if t.vpW == 0 {
		t.vpW = 1280
	}
	if t.vpH == 0 {
		t.vpH = 720
	}
	return t
}

// Context returns the tab's browser context (creating the default lazily).
func (t *Tab) Context() *BrowserContext {
	if t.ctx != nil {
		return t.ctx
	}
	return t.eng.DefaultContext()
}

func (t *Tab) close() {
	if t.closed.CompareAndSwap(false, true) {
		dropBitmapSources(t.ID)
		t.mu.Lock()
		j := t.js
		t.js = nil
		t.mu.Unlock()
		j.close()
	}
}

func (t *Tab) URL() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.url == nil {
		return "about:blank"
	}
	return t.url.String()
}

// Title reads the live <title> so changes made by page scripts are visible.
func (t *Tab) Title() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.dom != nil {
		if s := t.dom.Title(); s != "" || t.title == "" {
			return s
		}
	}
	return t.title
}

func (t *Tab) emit(kind EventKind, data any) {
	t.eng.bus.Publish(Event{Tab: t.ID, Kind: kind, At: time.Now(), Data: data})
}

// Navigate fetches url and installs the resulting document. This is the
// equivalent of Playwright's page.goto with waitUntil baked in.
func (t *Tab) Navigate(rawurl string, headers map[string]string) error {
	u, err := url.Parse(rawurl)
	if err != nil {
		return fmt.Errorf("bad url %q: %w", rawurl, err)
	}
	if u.Scheme == "" {
		u.Scheme = "https"
	}
	if t.IsOfflineMode() {
		err := fmt.Errorf("offline mode: navigation to %s blocked", rawurl)
		t.netRing.Push(Event{Tab: t.ID, Kind: EvFailure, At: time.Now(), Data: map[string]string{"url": rawurl, "error": err.Error()}})
		return err
	}
	res, err := t.fetch(u, headers)
	if errors.Is(err, errDownloaded) {
		return nil // the response was saved as a download; the page stays
	}
	if err != nil {
		t.netRing.Push(Event{Tab: t.ID, Kind: EvFailure, At: time.Now(), Data: map[string]string{"url": rawurl, "error": err.Error()}})
		t.emit(EvFailure, map[string]any{"url": rawurl, "error": err.Error()})
		return err
	}
	t.SetDocument(res.body, res.finalURL)
	t.mu.Lock()
	// history bookkeeping (goBack/goForward)
	if t.hIdx+1 < len(t.history) {
		t.history = t.history[:t.hIdx+1]
	}
	t.history = append(t.history, u)
	t.hIdx = len(t.history) - 1
	t.mu.Unlock()
	t.emit(EvNavigated, map[string]any{"url": res.finalURL, "status": res.status})
	return nil
}

type fetchResult struct {
	body     string
	finalURL string
	status   int
	har      fnet.RequestRecord
}

func (t *Tab) fetch(u *url.URL, headers map[string]string) (*fetchResult, error) {
	if u.Scheme == "data" {
		mt, data, err := decodeDataURL(u.String())
		if err != nil {
			return nil, err
		}
		h := http.Header{}
		h.Set("Content-Type", mt)
		if isDownloadResponse(h) {
			t.SaveDownloadBytes("download", mt, data, u.String())
			return nil, errDownloaded
		}
		return &fetchResult{body: string(data), finalURL: u.String(), status: 200}, nil
	}
	synth, method, target, hdrs, _, err := t.routedRequest("GET", u.String(), headers, "")
	if err != nil {
		t.netRing.Push(Event{Tab: t.ID, Kind: EvFailure, At: time.Now(), Data: map[string]string{"url": u.String(), "error": err.Error()}})
		return nil, err
	}
	rec := fnet.RequestRecord{Method: method, URL: target}
	start := time.Now()
	var res *http.Response
	if synth != nil {
		res = synth
		rec.Headers = map[string]string{"x-sweef-route": "fulfill"}
	} else {
		req, err := http.NewRequestWithContext(t.eng.ctx, method, target, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", t.UserAgent())
		req.Header.Set("Accept", "text/html,application/xhtml+xml;q=0.9,*/*;q=0.8")
		for k, v := range t.ExtraHeaders() {
			req.Header.Set(k, v)
		}
		for k, v := range hdrs {
			req.Header.Set(k, v)
		}
		// context cookie jar takes precedence over the shared transport jar
		if ck := t.Context().JarRef().ForURL(target); ck != "" {
			req.Header.Set("Cookie", ck)
		}
		rec.Headers = flattenHeaders(req.Header)
		t.netRing.Push(Event{Tab: t.ID, Kind: EvRequest, At: time.Now(), Data: rec})
		t.emit(EvRequest, rec)
		t.touchNet()
		if res, err = t.eng.clientFor(t.Context()).Do(req); err != nil {
			return nil, err
		}
	}
	if isDownloadResponse(res.Header) && res.StatusCode < 400 {
		defer res.Body.Close()
		t.saveDownload(u.String(), suggestedName(res.Header, u.String(), ""), res.Header.Get("Content-Type"), res.Body)
		return nil, errDownloaded
	}
	defer res.Body.Close()
	var body []byte
	limit := int64(8 << 20) // 8 MiB per doc — protects 50 MB machines
	b, err := io.ReadAll(io.LimitReader(res.Body, limit))
	if err != nil {
		return nil, err
	}
	body = b
	// persist Set-Cookie into the tab's context jar
	for _, sc := range res.Header.Values("Set-Cookie") {
		if c := parseCookie(sc, t.eng.now()); c != nil {
			if c.Domain == "" {
				c.Domain = hostOf(target)
			}
			t.Context().JarRef().Set(c)
		}
	}
	rec.Status = res.StatusCode
	rec.FinalURL = target
	if res.Request != nil && res.Request.URL != nil {
		rec.FinalURL = res.Request.URL.String()
	}
	rec.DurationMs = time.Since(start).Milliseconds()
	rec.ResponseHeaders = flattenHeaders(res.Header)
	t.netRing.Push(Event{Tab: t.ID, Kind: EvResponse, At: time.Now(), Data: rec})
	t.emit(EvResponse, rec)
	return &fetchResult{body: string(body), finalURL: rec.FinalURL, status: res.StatusCode, har: rec}, nil
}

func flattenHeaders(h map[string][]string) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		out[k] = strings.Join(v, ", ")
	}
	return out
}

// SetDocument parses html, rebuilds the DOM, runs init scripts + page scripts,
// fires load.
func (t *Tab) SetDocument(htmlSrc, finalURL string) {
	doc, err := fnet.ParseDocument(htmlSrc, finalURL)
	if err != nil {
		t.consoleRing.Push(Event{Tab: t.ID, Kind: EvConsole, At: time.Now(),
			Data: map[string]string{"level": "error", "text": "parse failure: " + err.Error()}})
		return
	}
	// forward DOM mutations from the doc layer onto the bus (EvDomMutated)
	doc.SetMutationListener(func(m fnet.Mutation) {
		t.stateRing.Push(Event{Tab: t.ID, Kind: EvDomMutated, At: time.Now(), Data: m})
		t.emit(EvDomMutated, m)
	})
	if w, _, _, _, _ := t.Viewport(); w > 0 {
		doc.SetLayoutWidth(w)
	}
	t.installRender(doc)
	t.applyStyles(doc)
	t.mu.Lock()
	t.html = htmlSrc
	t.dom = doc
	t.title = doc.Title()
	u, _ := url.Parse(finalURL)
	t.url = u
	t.scripts = compileScripts(doc.Scripts())
	initScripts := append([]*CompiledScript(nil), t.initScripts...)
	old := t.js
	t.js = nil
	t.mu.Unlock()
	// Freeing every object the previous page allocated takes milliseconds;
	// nothing can reach the old VM any more, so do it off the critical path.
	if old != nil {
		go old.close()
	}

	// Pages with no scripts, no inline handlers and no init scripts get no
	// VM at all: there is nothing to run, and a navigation stays at the cost
	// of a fetch and a parse. A VM is created on demand by Evaluate.
	if !t.eng.opts.DisableJS && (len(initScripts) > 0 || doc.NeedsScript()) {
		j := newJSVM(t, doc, finalURL)
		t.mu.Lock()
		t.js = j
		t.mu.Unlock()
		if j != nil {
			// addInitScript sources run BEFORE any page script, on every
			// navigation — the guarantee Playwright gives for hooking timers/fetch.
			for _, s := range initScripts {
				_, _ = j.run(s.Source, s.Name)
			}
			t.runPageScripts(j, doc)
			if t.closed.Load() {
				return
			}
			t.mu.RLock()
			navigated := t.js != j
			t.mu.RUnlock()
			if navigated {
				return // page code navigated; the new document already fired load
			}
		}
	}
	t.stateRing.Push(Event{Tab: t.ID, Kind: EvLoad, At: time.Now(), Data: map[string]any{"url": finalURL, "title": t.Title()}})
	t.emit(EvLoad, map[string]any{"url": finalURL, "title": t.Title()})
}

// ---- public surface used by RPC/MCP handlers -----------------------------

func (t *Tab) HTML(pretty bool) string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.dom == nil {
		return t.html
	}
	if pretty {
		var buf bytes.Buffer
		t.dom.Serialize(&buf)
		return buf.String()
	}
	return t.html
}

func (t *Tab) QuerySelector(sel string) (*NodeInfo, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.dom == nil {
		return nil, ErrNotFound
	}
	info, err := t.dom.QuerySelector(sel)
	if err != nil {
		return nil, err
	}
	return infoToNode(info), nil
}

func (t *Tab) QuerySelectorAll(sel string) ([]*NodeInfo, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.dom == nil {
		return nil, ErrNotFound
	}
	infos, err := t.dom.QuerySelectorAll(sel)
	if err != nil {
		return nil, err
	}
	out := make([]*NodeInfo, 0, len(infos))
	for _, i := range infos {
		out = append(out, infoToNode(i))
	}
	return out, nil
}

// Text returns innerText-like extraction for agents (structured, whitespace-normalised).
func (t *Tab) Text(sel string) (string, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.dom == nil {
		return "", ErrNotFound
	}
	return t.dom.Text(sel)
}

// Snapshot returns an accessibility-style tree (roles/names/states) which is
// what AI agents should read instead of raw HTML — far fewer tokens.
func (t *Tab) Snapshot() ([]*NodeInfo, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.dom == nil {
		return nil, ErrNotFound
	}
	infos, err := t.dom.AriaSnapshot()
	if err != nil {
		return nil, err
	}
	out := make([]*NodeInfo, 0, len(infos))
	for _, i := range infos {
		out = append(out, infoToNode(i))
	}
	return out, nil
}

// SnapshotOptions mirrors net.SnapshotOptions for callers outside the engine.
type SnapshotOptions = fnet.SnapshotOptions

// AriaSnapshotV2 renders the Playwright-format accessibility tree with options.
func (t *Tab) AriaSnapshotV2(o SnapshotOptions) (string, error) {
	t.settle(0) // deliver queued socket/worker events and due timers first
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.dom == nil {
		return "", ErrNotFound
	}
	return t.dom.AriaSnapshotV2(o)
}

func (t *Tab) ConsoleLog(sinceSID int64) []Event { return t.consoleRing.Since(sinceSID) }
func (t *Tab) NetworkLog(sinceSID int64) []Event { return t.netRing.Since(sinceSID) }
func (t *Tab) StateLog(sinceSID int64) []Event   { return t.stateRing.Since(sinceSID) }

func (t *Tab) LogConsole(level, text string) {
	e := t.consoleRing.Push(Event{Tab: t.ID, Kind: EvConsole, At: time.Now(),
		Data: map[string]string{"level": level, "text": text}})
	t.emit(EvConsole, e.Data)
}

// Evaluate runs JS in the tab's VM, awaits a returned promise, and returns
// the JSON-encoded completion value. DOM nodes serialise as {nodeType,
// tagName, id, ref}.
func (t *Tab) Evaluate(expr string) (json.RawMessage, error) {
	j, err := t.ensureJS()
	if err != nil {
		return nil, err
	}
	return j.evaluate(expr)
}

// ensureJS returns the tab's VM, binding one to the current document when the
// page loaded without scripts (lazy VM).
func (t *Tab) ensureJS() (*jsVM, error) {
	t.mu.RLock()
	j, d, u := t.js, t.dom, t.url
	t.mu.RUnlock()
	if j != nil {
		return j, nil
	}
	if t.eng.opts.DisableJS {
		return nil, fmt.Errorf("javascript is disabled (--no-js)")
	}
	if d == nil {
		return nil, fmt.Errorf("no document loaded")
	}
	docURL := "about:blank"
	if u != nil {
		docURL = u.String()
	}
	j = newJSVM(t, d, docURL)
	if j == nil {
		return nil, fmt.Errorf("javascript engine unavailable")
	}
	_, _ = j.call("__swSetDocument", docURL, "complete")
	t.mu.Lock()
	if t.js == nil && t.dom == d {
		t.js = j
	} else {
		other := t.js
		t.mu.Unlock()
		j.close()
		if other == nil {
			return nil, fmt.Errorf("document changed")
		}
		return other, nil
	}
	t.mu.Unlock()
	return j, nil
}

// Click performs a semantic click on selector: focus, dispatch mousedown/up/click,
// follow link href or submit button form (Playwright actionability minus pixels).

// Fill sets a control's value with auto-wait and dispatches input/change
// into page JavaScript (Playwright fill semantics).
func (t *Tab) Fill(sel, value string) error { return t.Fill2(sel, value) }

// SubmitFormFor submits the form enclosing sel (GET navigates with the query,
// POST sends application/x-www-form-urlencoded) and installs the response.
func (t *Tab) SubmitFormFor(sel string) error {
	t.mu.RLock()
	d := t.dom
	t.mu.RUnlock()
	if d == nil {
		return ErrNotFound
	}
	method, action, values, err := d.FormFor(sel)
	if err != nil {
		return err
	}
	return t.submitForm(method, action, values)
}

// SubmitFormBackend submits the form with (or enclosing) the given node id.
func (t *Tab) SubmitFormBackend(id int64) error {
	t.mu.RLock()
	d := t.dom
	t.mu.RUnlock()
	if d == nil {
		return ErrNotFound
	}
	method, action, values, err := d.FormForBackend(id)
	if err != nil {
		return err
	}
	return t.submitForm(method, action, values)
}

func (t *Tab) submitForm(method, action string, values map[string]string) error {
	u, _ := url.Parse(t.URL())
	if action != "" {
		au, err := url.Parse(action)
		if err != nil {
			return err
		}
		u = u.ResolveReference(au)
	}
	if !strings.EqualFold(method, "post") {
		q := u.Query()
		for k, v := range values {
			q.Set(k, v)
		}
		u.RawQuery = q.Encode()
		return t.Navigate(u.String(), nil)
	}
	return t.postForm(u.String(), values)
}

func (t *Tab) postForm(rawurl string, values map[string]string) error {
	form := url.Values{}
	for k, v := range values {
		form.Set(k, v)
	}
	encoded := form.Encode()
	synth, method, target, hdrs, newBody, err := t.routedRequest("POST", rawurl, nil, encoded)
	if err != nil {
		return err
	}
	if synth != nil {
		b, _ := io.ReadAll(synth.Body)
		t.SetDocument(string(b), rawurl)
		return nil
	}
	rawurl = target
	req, err := http.NewRequestWithContext(t.eng.ctx, method, rawurl, strings.NewReader(newBody))
	if err != nil {
		return err
	}
	for k, v := range hdrs {
		req.Header.Set(k, v)
	}
	t.touchNet()
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", t.UserAgent())
	req.Header.Set("Accept", "text/html,application/xhtml+xml;q=0.9,*/*;q=0.8")
	for k, v := range t.ExtraHeaders() {
		req.Header.Set(k, v)
	}
	if ck := t.Context().JarRef().ForURL(rawurl); ck != "" {
		req.Header.Set("Cookie", ck)
	}
	rec := fnet.RequestRecord{Method: "POST", URL: rawurl, Headers: flattenHeaders(req.Header)}
	t.netRing.Push(Event{Tab: t.ID, Kind: EvRequest, At: time.Now(), Data: rec})
	start := time.Now()
	res, err := t.eng.clientFor(t.Context()).Do(req)
	if err != nil {
		t.netRing.Push(Event{Tab: t.ID, Kind: EvFailure, At: time.Now(), Data: map[string]string{"url": rawurl, "error": err.Error()}})
		return err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	for _, sc := range res.Header.Values("Set-Cookie") {
		if c := parseCookie(sc, t.eng.now()); c != nil {
			if c.Domain == "" {
				c.Domain = res.Request.URL.Hostname()
			}
			t.Context().JarRef().Set(c)
		}
	}
	rec.Status = res.StatusCode
	rec.FinalURL = res.Request.URL.String()
	rec.DurationMs = time.Since(start).Milliseconds()
	rec.ResponseHeaders = flattenHeaders(res.Header)
	t.netRing.Push(Event{Tab: t.ID, Kind: EvResponse, At: time.Now(), Data: rec})
	final := res.Request.URL.String()
	t.SetDocument(string(b), final)
	t.mu.Lock()
	if fu, err := url.Parse(final); err == nil {
		if t.hIdx+1 < len(t.history) {
			t.history = t.history[:t.hIdx+1]
		}
		t.history = append(t.history, fu)
		t.hIdx = len(t.history) - 1
	}
	t.mu.Unlock()
	t.emit(EvNavigated, map[string]any{"url": final, "status": res.StatusCode})
	return nil
}

// Reload re-navigates to the current URL, bypassing cached state — like
// Playwright's page.reload().
func (t *Tab) Reload() error {
	t.mu.RLock()
	u := t.url
	t.mu.RUnlock()
	return t.Navigate(u.String(), map[string]string{"Cache-Control": "no-cache"})
}

func (t *Tab) GoBack() error    { return t.traverse(-1) }
func (t *Tab) GoForward() error { return t.traverse(+1) }

func (t *Tab) traverse(delta int) error {
	t.mu.RLock()
	next := t.hIdx + delta
	if next < 0 || next >= len(t.history) {
		t.mu.RUnlock()
		return nil // no-op like real browsers
	}
	u := t.history[next].String()
	t.hIdx = next
	t.mu.RUnlock()
	return t.Navigate(u, nil)
}

// Route installs an interception rule (glob on URL). handler may return a mock
// response body/status or abort the request.
func (t *Tab) Route(glob string, handler func(*http.Request) (int, string, bool)) {
	t.interceptMu.Lock()
	t.routes = append(t.routes, fnet.NewRoute(glob, handler))
	t.interceptMu.Unlock()
}

// Unroute removes one previously installed rule by its glob pattern.
func (t *Tab) Unroute(glob string) {
	t.interceptMu.Lock()
	var keep []*fnet.Route
	for _, r := range t.routes {
		if r.Pattern != glob {
			keep = append(keep, r)
		}
	}
	t.routes = keep
	t.interceptMu.Unlock()
}

func (t *Tab) UnrouteAll() {
	t.interceptMu.Lock()
	t.routes = nil
	t.interceptMu.Unlock()
}

// Screenshot renders the current document as structured text — the agent-first
// equivalent of a screenshot (token-cheap, deterministic). width<=0 uses the
// context viewport width.
func (t *Tab) Screenshot(width int) string {
	t.mu.RLock()
	dom := t.dom
	if width <= 0 {
		width = t.vpW
	}
	t.mu.RUnlock()
	if width <= 0 {
		width = 80
	}
	if dom == nil {
		return ""
	}
	return dom.RenderText(width)
}

// HAR exports the network ring in Chrome HAR 1.2 format.
func (t *Tab) HAR() ([]byte, error) {
	evs := t.netRing.All()
	type log struct {
		Version string               `json:"version"`
		Entries []fnet.RequestRecord `json:"entries"`
	}
	wrapper := struct {
		Log log `json:"log"`
	}{Log: log{Version: "1.2"}}
	for _, e := range evs {
		if e.Kind == EvResponse {
			if rec, ok := e.Data.(fnet.RequestRecord); ok {
				wrapper.Log.Entries = append(wrapper.Log.Entries, rec)
			}
		}
	}
	return json.Marshal(wrapper)
}

// infoToNode converts a DOM-layer Info into the engine's wire NodeInfo.
func infoToNode(i *fnet.Info) *NodeInfo {
	return &NodeInfo{
		NodeID:    i.NodeID,
		BackendID: i.BackendID,
		Name:      i.Name,
		ID:        i.ID,
		Type:      i.Type,
		Role:      i.Role,
		Text:      i.Text,
		Href:      i.Href,
		Visible:   i.Visible,
		Disabled:  i.Disabled,
		Checked:   i.Checked,
		Focusable: i.Focusable,
	}
}

// CompiledScript is a script ready for the JS runtime.
type CompiledScript struct {
	Source string
	Name   string
}

func compileScripts(srcs []fnet.Script) []*CompiledScript {
	out := make([]*CompiledScript, 0, len(srcs))
	for _, s := range srcs {
		out = append(out, &CompiledScript{Source: s.Source, Name: s.Name})
	}
	return out
}

// interceptGet evaluates installed route handlers for a GET; returns
// (statusCode, body, matched). code==0 with matched==true means abort.
func (t *Tab) interceptGet(rawurl string) (int, string, bool) {
	t.interceptMu.RLock()
	routes := append([]*fnet.Route(nil), t.routes...)
	t.interceptMu.RUnlock()
	if len(routes) == 0 {
		return 0, "", false
	}
	req, _ := http.NewRequest(http.MethodGet, rawurl, nil)
	for _, r := range routes {
		if code, body, ok := fnet.MatchRoute(r, req); ok {
			return code, body, true
		}
	}
	return 0, "", false
}

// Dialogs records a JS dialog and applies the armed response like Playwright's
// page.on("dialog"). Returns whether it was accepted.
func (t *Tab) HandleDialog(d Dialog) (bool, string) {
	t.mu.Lock()
	acc, txt := t.dialogAccept, t.dialogText
	t.dialogAccept, t.dialogText = true, ""
	t.mu.Unlock()
	if d.Kind == "alert" || d.Kind == "beforeunload" {
		acc = true
	}
	t.dialogRing.Push(Event{Tab: t.ID, Kind: EvDialog, At: time.Now(),
		Data: map[string]any{"kind": d.Kind, "message": d.Message, "accepted": acc, "reply": txt}})
	t.emit(EvDialog, map[string]any{"dialog": d, "accepted": acc, "text": txt})
	return acc, txt
}

func (t *Tab) ArmDialogResponse(accept bool, promptText string) {
	t.mu.Lock()
	t.dialogAccept = accept
	t.dialogText = promptText
	t.mu.Unlock()
}

func (t *Tab) DialogLog(sinceSID int64) []Event { return t.dialogRing.Since(sinceSID) }

// Emulation setters/getters (Playwright parity).
func (t *Tab) SetViewport(w, h int, dsf float64, mobile, landscape bool) {
	t.mu.Lock()
	if w > 0 {
		t.vpW = w
	}
	if h > 0 {
		t.vpH = h
	}
	if dsf > 0 {
		t.dsf = dsf
	}
	t.mobile, t.landscape = mobile, landscape
	if t.dom != nil {
		t.dom.SetLayoutWidth(t.vpW)
	}
	t.mu.Unlock()
	t.stateRing.Push(Event{Tab: t.ID, Kind: EvDomMutated, At: time.Now(),
		Data: map[string]any{"action": "setViewport", "width": t.vpW, "height": t.vpH, "mobile": mobile}})
}

func (t *Tab) Viewport() (w, h int, dsf float64, mobile, landscape bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.vpW, t.vpH, t.dsf, t.mobile, t.landscape
}

func (t *Tab) SetUserAgent(ua string) { t.mu.Lock(); t.userAgent = ua; t.mu.Unlock() }

func (t *Tab) UserAgent() string {
	t.mu.RLock()
	ua := t.userAgent
	t.mu.RUnlock()
	if ua != "" {
		return ua
	}
	return t.eng.opts.DefaultUserAgent
}

func (t *Tab) SetGeolocation(lat, lng float64) {
	t.mu.Lock()
	t.geo = &[2]float64{lat, lng}
	t.mu.Unlock()
}

func (t *Tab) Geolocation() [2]float64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.geo == nil {
		return [2]float64{}
	}
	return *t.geo
}

func (t *Tab) GrantPermissions(perms []string) {
	t.mu.Lock()
	if t.permissions == nil {
		t.permissions = map[string]bool{}
	}
	for _, p := range perms {
		t.permissions[p] = true
	}
	t.mu.Unlock()
}

func (t *Tab) PermissionsList() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	var out []string
	for p, ok := range t.permissions {
		if ok {
			out = append(out, p)
		}
	}
	return out
}

func (t *Tab) SetOffline(on bool) {
	t.mu.Lock()
	t.offline = on
	t.mu.Unlock()
	t.stateRing.Push(Event{Tab: t.ID, Kind: EvDomMutated, At: time.Now(),
		Data: map[string]any{"action": "setOffline", "offline": on}})
}

func (t *Tab) IsOfflineMode() bool { t.mu.RLock(); defer t.mu.RUnlock(); return t.offline }

func (t *Tab) SetColorScheme(s string) { t.mu.Lock(); t.colorScheme = s; t.mu.Unlock() }

func (t *Tab) ColorSchemeName() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.colorScheme == "" {
		return "light"
	}
	return t.colorScheme
}

func (t *Tab) SetExtraHeaders(h map[string]string) { t.mu.Lock(); t.extraHdrs = h; t.mu.Unlock() }

func (t *Tab) ExtraHeaders() map[string]string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := map[string]string{}
	for k, v := range t.extraHdrs {
		out[k] = v
	}
	return out
}

func (t *Tab) AddInitScript(src string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	// Init scripts apply from the next navigation on, as in Playwright.
	id := fmt.Sprintf("init-%d", len(t.initScripts)+1)
	t.initScripts = append(t.initScripts, &CompiledScript{Source: src, Name: id})
	return id
}

func (t *Tab) RemoveInitScript(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	var keep []*CompiledScript
	for _, s := range t.initScripts {
		if s.Name != id {
			keep = append(keep, s)
		}
	}
	t.initScripts = keep
}

func (t *Tab) ExposeBinding(name string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, b := range t.bindings {
		if b == name {
			return nil
		}
	}
	t.bindings = append(t.bindings, name)
	if j := t.js; j != nil {
		go j.installBindings() // define it in the live page too (outside t.mu)
	}
	return nil
}

func (t *Tab) Bindings() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return append([]string(nil), t.bindings...)
}
