package engine

import (
	"bytes"
	"encoding/json"
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

// NodeInfo is the serialisable handle the outside world uses to address DOM
// nodes. BackendNodeID is stable for the lifetime of the document tree.
type NodeInfo struct {
	NodeID      int64  `json:"nodeId"`
	BackendID   int64  `json:"backendNodeId"`
	Name        string `json:"name"`          // tag name, lowercased
	ID          string `json:"id,omitempty"`  // id attribute
	Type        string `json:"type,omitempty"`
	Role        string `json:"role,omitempty"`
	Text        string `json:"text,omitempty"`       // accessible-ish label
	Href        string `json:"href,omitempty"`
	Attrs       map[string]string `json:"attrs,omitempty"`
	Visible     bool   `json:"visible"`
	Disabled    bool   `json:"disabled"`
	Checked     bool   `json:"checked,omitempty"`
	Box         [4]int `json:"box,omitempty"` // x,y,w,h in layout px (approx)
	Focusable   bool   `json:"focusable"`
}

// Tab is one browsing context. Goroutine-light: no dedicated thread, just
// state + a mutex; JS evaluation happens on demand.
type Tab struct {
	ID     string
	mu     sync.RWMutex
	eng    *Engine
	title  string
	url    *url.URL
	html   string
	dom    *fnet.Doc // parsed tree held by our DOM layer
	scripts []*CompiledScript

	consoleRing *Ring
	netRing     *Ring
	stateRing   *Ring

	jsCtx   any // opaque QuickJS context handle
	history []*url.URL
	hIdx    int

	closed atomic.Bool

	// Interception hooks (route.fulfill / route.abort semantics)
	interceptMu sync.RWMutex
	routes      []*fnet.Route
}

func newTab(e *Engine, id string) *Tab {
	t := &Tab{
		ID:          id,
		eng:         e,
		consoleRing: NewRing(e.opts.ConsoleRingSize),
		netRing:     NewRing(e.opts.NetworkRingSize),
		stateRing:   NewRing(e.opts.StateRingSize),
	}
	if !e.opts.DisableJS {
		t.jsCtx = runtimeCreateContext(id)
	}
	return t
}

func (t *Tab) close() {
	if t.closed.CompareAndSwap(false, true) {
		if t.jsCtx != nil {
			runtimeDestroyContext(t.jsCtx)
			t.jsCtx = nil
		}
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

func (t *Tab) Title() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
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
	res, err := t.fetch(u, headers)
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
	req, err := http.NewRequestWithContext(t.eng.ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", t.eng.opts.DefaultUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml;q=0.9,*/*;q=0.8")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := fnet.RequestRecord{Method: "GET", URL: u.String(), Headers: flattenHeaders(req.Header)}
	t.netRing.Push(Event{Tab: t.ID, Kind: EvRequest, At: time.Now(), Data: rec})
	t.emit(EvRequest, rec)

	start := time.Now()
	res, err := t.eng.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var body []byte
	limit := int64(8 << 20) // 8 MiB per doc — protects 50 MB machines
	b, err := io.ReadAll(io.LimitReader(res.Body, limit))
	if err != nil {
		return nil, err
	}
	body = b
	rec.Status = res.StatusCode
	rec.FinalURL = res.Request.URL.String()
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

// SetDocument parses html, rebuilds the DOM, runs inline scripts, fires load.
func (t *Tab) SetDocument(htmlSrc, finalURL string) {
	doc, err := fnet.ParseDocument(htmlSrc, finalURL)
	if err != nil {
		t.consoleRing.Push(Event{Tab: t.ID, Kind: EvConsole, At: time.Now(),
			Data: map[string]string{"level": "error", "text": "parse failure: " + err.Error()}})
		return
	}
	t.mu.Lock()
	t.html = htmlSrc
	t.dom = doc
	t.title = doc.Title()
	u, _ := url.Parse(finalURL)
	t.url = u
	t.scripts = compileScripts(doc.Scripts())
	t.mu.Unlock()

	if !t.eng.opts.DisableJS && t.jsCtx != nil {
		t.runScripts()
	}
	t.stateRing.Push(Event{Tab: t.ID, Kind: EvLoad, At: time.Now(), Data: map[string]any{"url": finalURL, "title": t.Title()}})
	t.emit(EvLoad, map[string]any{"url": finalURL, "title": t.Title()})
}

func (t *Tab) runScripts() {
	t.mu.RLock()
	scripts := append([]*CompiledScript(nil), t.scripts...)
	t.mu.RUnlock()
	for _, s := range scripts {
		out, errs := runtimeEval(t.jsCtx, s.Source, s.Name)
		for _, e := range errs {
			t.LogConsole("error", e)
		}
		if out != "" {
			// script may have mutated DOM via Sweef DOM API bridge
			t.applyDomMutations(out)
		}
	}
}

// applyDomMutations consumes the JSON mutation journal returned by the JS
// bridge (document.write / innerHTML / createElement paths). Keeping mutations
// as a journal rather than live pointers makes the tree cheap to reparse.
func (t *Tab) applyDomMutations(journal string) {
	type mut struct {
		Op    string `json:"op"`
		Sel   string `json:"sel"`
		Value string `json:"value"`
	}
	var ms []mut
	if err := json.Unmarshal([]byte(journal), &ms); err != nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, m := range ms {
		switch m.Op {
		case "title":
			t.title = m.Value
		case "replace-body":
			if d, err := fnet.ParseDocument("<html><body>"+m.Value+"</body></html>", t.URL()); err == nil {
				t.dom = d
			}
		}
	}
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

func (t *Tab) ConsoleLog(sinceSID int64) []Event { return t.consoleRing.Since(sinceSID) }
func (t *Tab) NetworkLog(sinceSID int64) []Event { return t.netRing.Since(sinceSID) }
func (t *Tab) StateLog(sinceSID int64) []Event   { return t.stateRing.Since(sinceSID) }

func (t *Tab) LogConsole(level, text string) {
	e := t.consoleRing.Push(Event{Tab: t.ID, Kind: EvConsole, At: time.Now(),
		Data: map[string]string{"level": level, "text": text}})
	t.emit(EvConsole, e.Data)
}

// Evaluate runs JS in the tab context and returns a JSON-encoded result.
func (t *Tab) Evaluate(expr string) (json.RawMessage, error) {
	if t.eng.opts.DisableJS || t.jsCtx == nil {
		return nil, fmt.Errorf("js disabled")
	}
	out, errs := runtimeEval(t.jsCtx, expr, "<eval>")
	for _, e := range errs {
		t.LogConsole("error", e)
	}
	if out == "" {
		return json.RawMessage("null"), nil
	}
	return json.RawMessage(out), nil
}

// Click performs a semantic click on selector: focus, dispatch mousedown/up/click,
// follow link href or submit button form (Playwright actionability minus pixels).
func (t *Tab) Click(sel string) error {
	ni, err := t.QuerySelector(sel)
	if err != nil {
		return err
	}
	if ni.Disabled {
		return fmt.Errorf("element %q is disabled", sel)
	}
	t.LogConsole("log", fmt.Sprintf("click %q <%s>", sel, ni.Name))
	if ni.Href != "" {
		base, _ := url.Parse(t.URL())
		abs, _ := url.Parse(ni.Href)
		return t.Navigate(base.ResolveReference(abs).String(), nil)
	}
	// forms: if inside <form> and type=submit -> GET/POST navigation
	if ni.Type == "submit" {
		return t.SubmitFormFor(sel)
	}
	return nil
}

func (t *Tab) Fill(sel, value string) error {
	ni, err := t.QuerySelector(sel)
	if err != nil {
		return err
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	err = t.dom.SetValue(ni.BackendID, value)
	if err == nil {
		t.stateRing.Push(Event{Tab: t.ID, Kind: EvDomMutated, At: time.Now(),
			Data: map[string]string{"sel": sel, "value": value}})
	}
	return err
}

func (t *Tab) Press(sel string, keys string) error {
	t.LogConsole("log", fmt.Sprintf("press %q on %q", keys, sel))
	if keys == "Enter" {
		return t.SubmitFormFor(sel)
	}
	return nil
}

func (t *Tab) SubmitFormFor(sel string) error {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.dom == nil {
		return ErrNotFound
	}
	method, action, values, err := t.dom.FormFor(sel)
	if err != nil {
		return err
	}
	u, _ := url.Parse(t.URL())
	if action != "" {
		au, _ := url.Parse(action)
		u = u.ResolveReference(au)
	}
	if strings.EqualFold(method, "get") {
		q := u.Query()
		for k, v := range values {
			q.Set(k, v)
		}
		u.RawQuery = q.Encode()
		t.mu.RUnlock()
		t.mu.RLock()
		return t.Navigate(u.String(), nil)
	}
	return t.postForm(u.String(), values)
}

func (t *Tab) postForm(rawurl string, values map[string]string) error {
	form := url.Values{}
	for k, v := range values {
		form.Set(k, v)
	}
	req, err := http.NewRequestWithContext(t.eng.ctx, http.MethodPost, rawurl, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", t.eng.opts.DefaultUserAgent)
	res, err := t.eng.client().Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	t.mu.Unlock() // caller holds RLock; we intentionally unlock before SetDocument
	t.SetDocument(string(b), res.Request.URL.String())
	t.mu.Lock()
	return nil
}

func (t *Tab) GoBack() error  { return t.traverse(-1) }
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

func (t *Tab) UnrouteAll() {
	t.interceptMu.Lock()
	t.routes = nil
	t.interceptMu.Unlock()
}

// Screenshot renders the structured snapshot into an ANSI/plain-text "image";
// on machines with a display server the same model feeds the PNG rasteriser.
func (t *Tab) Screenshot(width int) string {
	if width <= 0 || width > 240 {
		width = 80 // tiny-machine default
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.dom == nil {
		return ""
	}
	return t.dom.RenderText(width)
}

// HAR exports the network ring in Chrome HAR 1.2 format.
func (t *Tab) HAR() ([]byte, error) {
	evs := t.netRing.All()
	type log struct {
		Version string              `json:"version"`
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
