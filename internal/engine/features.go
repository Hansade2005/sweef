package engine

// features.go — routes with request matching and continue, network idle,
// exposed bindings, and downloads.

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	fnet "github.com/sweefbrowser/sweef/internal/net"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ---- routes ------------------------------------------------------------------

// RouteRule is one interception rule (Playwright page.route semantics):
// match by URL glob (or /regex/), optional method and request-body
// substring, then fulfill with a canned response, abort, or continue with
// modifications. Times > 0 removes the rule after that many hits.
type RouteRule struct {
	Pattern      string            `json:"pattern"`
	Method       string            `json:"method,omitempty"`
	BodyContains string            `json:"bodyContains,omitempty"`
	Action       string            `json:"action"` // fulfill | abort | continue
	Status       int               `json:"status,omitempty"`
	Body         string            `json:"body,omitempty"`
	ContentType  string            `json:"contentType,omitempty"`
	Headers      map[string]string `json:"headers,omitempty"` // fulfill: response headers; continue: request headers to set
	URL          string            `json:"url,omitempty"`     // continue: rewrite URL
	NewMethod    string            `json:"newMethod,omitempty"`
	NewBody      *string           `json:"newBody,omitempty"`
	Times        int               `json:"times,omitempty"`
	Hits         int               `json:"hits"`
	re           *regexp.Regexp
}

type routeTable struct {
	mu    sync.Mutex
	rules []*RouteRule
}

// AddRoute installs a rule; later rules take precedence, as in Playwright.
func (t *Tab) AddRoute(r *RouteRule) error {
	if r.Action == "" {
		r.Action = "fulfill"
	}
	switch r.Action {
	case "fulfill", "abort", "continue":
	default:
		return fmt.Errorf("route action must be fulfill, abort or continue")
	}
	if len(r.Pattern) > 2 && strings.HasPrefix(r.Pattern, "/") && strings.HasSuffix(r.Pattern, "/") {
		re, err := regexp.Compile(r.Pattern[1 : len(r.Pattern)-1])
		if err != nil {
			return fmt.Errorf("route pattern: %w", err)
		}
		r.re = re
	}
	if r.Status == 0 {
		r.Status = 200
	}
	t.routes2.mu.Lock()
	t.routes2.rules = append(t.routes2.rules, r)
	t.routes2.mu.Unlock()
	return nil
}

// RemoveRoutes drops rules with the given pattern ("" drops all).
func (t *Tab) RemoveRoutes(pattern string) {
	t.routes2.mu.Lock()
	defer t.routes2.mu.Unlock()
	if pattern == "" {
		t.routes2.rules = nil
		return
	}
	keep := t.routes2.rules[:0]
	for _, r := range t.routes2.rules {
		if r.Pattern != pattern {
			keep = append(keep, r)
		}
	}
	t.routes2.rules = keep
}

// Routes lists the active rules.
func (t *Tab) Routes() []RouteRule {
	t.routes2.mu.Lock()
	defer t.routes2.mu.Unlock()
	out := make([]RouteRule, 0, len(t.routes2.rules))
	for _, r := range t.routes2.rules {
		out = append(out, *r)
	}
	return out
}

// globMatch implements Playwright URL globs: ** any chars, * any chars but
// '/', ? one char; a pattern without a scheme also matches against the path.
func globMatch(pattern, raw string) bool {
	if pattern == "" || pattern == "**" || pattern == "*" {
		return true
	}
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		switch {
		case c == '*' && i+1 < len(pattern) && pattern[i+1] == '*':
			b.WriteString(".*")
			i++
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return false
	}
	if re.MatchString(raw) {
		return true
	}
	if u, err := url.Parse(raw); err == nil {
		if re.MatchString(u.Path) || re.MatchString(u.Host+u.Path) {
			return true
		}
	}
	// legacy behaviour: substring match on the pattern stripped of stars
	core := strings.Trim(pattern, "*")
	return core != "" && !strings.ContainsAny(core, "*?") && strings.Contains(raw, core)
}

// matchRoute returns the rule that handles a request, or nil.
func (t *Tab) matchRoute(method, rawurl, body string) *RouteRule {
	t.routes2.mu.Lock()
	defer t.routes2.mu.Unlock()
	for i := len(t.routes2.rules) - 1; i >= 0; i-- {
		r := t.routes2.rules[i]
		if r.Method != "" && !strings.EqualFold(r.Method, method) {
			continue
		}
		if r.BodyContains != "" && !strings.Contains(body, r.BodyContains) {
			continue
		}
		ok := false
		if r.re != nil {
			ok = r.re.MatchString(rawurl)
		} else {
			ok = globMatch(r.Pattern, rawurl)
		}
		if !ok {
			continue
		}
		r.Hits++
		if r.Times > 0 && r.Hits >= r.Times {
			t.routes2.rules = append(t.routes2.rules[:i], t.routes2.rules[i+1:]...)
		}
		cp := *r
		return &cp
	}
	return nil
}

var errRouteAbort = errors.New("net::ERR_FAILED (aborted by route)")

// routedRequest applies route rules to an outgoing request. It returns a
// synthetic response for fulfill, errRouteAbort for abort, or the (possibly
// rewritten) request parameters for continue / no match.
func (t *Tab) routedRequest(method, rawurl string, headers map[string]string, body string) (resp *http.Response, outMethod, outURL string, outHeaders map[string]string, outBody string, err error) {
	outMethod, outURL, outHeaders, outBody = method, rawurl, headers, body
	t.recordRequestBody(method, rawurl, body)
	rt := t // a frame's requests are routed by the page it is in
	if t.isFrame() && t.bc.top != nil {
		rt = t.bc.top
	}
	r := rt.matchRoute(method, rawurl, body)
	if r == nil {
		// legacy GET-only rules installed through Tab.Route
		if method == "GET" {
			if code, rbody, matched := rt.interceptGet(rawurl); matched {
				if code == 0 {
					return nil, "", "", nil, "", errRouteAbort
				}
				return synthResponse(rawurl, code, rbody, "", nil), method, rawurl, headers, body, nil
			}
		}
		return nil, outMethod, outURL, outHeaders, outBody, nil
	}
	switch r.Action {
	case "abort":
		return nil, "", "", nil, "", errRouteAbort
	case "fulfill":
		return synthResponse(rawurl, r.Status, r.Body, r.ContentType, r.Headers), method, rawurl, headers, body, nil
	}
	// continue with modifications
	if r.URL != "" {
		if abs, e := resolveURL(rawurl, r.URL); e == nil {
			outURL = abs
		}
	}
	if r.NewMethod != "" {
		outMethod = strings.ToUpper(r.NewMethod)
	}
	if r.NewBody != nil {
		outBody = *r.NewBody
	}
	if len(r.Headers) > 0 {
		h := map[string]string{}
		for k, v := range headers {
			h[k] = v
		}
		for k, v := range r.Headers {
			h[k] = v
		}
		outHeaders = h
	}
	return nil, outMethod, outURL, outHeaders, outBody, nil
}

func synthResponse(rawurl string, status int, body, contentType string, headers map[string]string) *http.Response {
	h := http.Header{}
	if contentType != "" {
		h.Set("Content-Type", contentType)
	}
	for k, v := range headers {
		h.Set(k, v)
	}
	u, _ := url.Parse(rawurl)
	return &http.Response{
		StatusCode: status, Status: fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header: h, Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body)),
		Request: &http.Request{Method: "GET", URL: u},
	}
}

// recordRequestBody keeps request bodies for non-GET requests visible in the
// network log (HAR postData equivalent).
func (t *Tab) recordRequestBody(method, rawurl, body string) {
	if body == "" || method == "GET" {
		return
	}
	if len(body) > 64<<10 {
		body = body[:64<<10] + "…"
	}
	t.netRing.Push(Event{Tab: t.ID, Kind: EvRequest, At: time.Now(), Data: map[string]string{"request.method": method, "request.url": rawurl, "request.postData": body}})
}

// ---- network idle ------------------------------------------------------------

func (t *Tab) touchNet() { t.lastNet.Store(time.Now().UnixNano()) }

// waitNetworkIdle pumps the page until no request has started for 500 ms
// (Playwright's networkidle), or the timeout passes.
func (t *Tab) waitNetworkIdle(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		t.settle(100 * time.Millisecond)
		last := time.Unix(0, t.lastNet.Load())
		if time.Since(last) >= 500*time.Millisecond {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%w: network never went idle", ErrTimeout)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// ---- exposed bindings ----------------------------------------------------------

// BindingCall is one invocation of an exposed binding by page code.
type BindingCall struct {
	SID  int64           `json:"sid"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
	URL  string          `json:"url"`
	At   time.Time       `json:"at"`
}

// SetBindingReply sets the JSON value page code receives from name(...).
func (t *Tab) SetBindingReply(name, replyJSON string) {
	t.mu.Lock()
	if t.bindingReplies == nil {
		t.bindingReplies = map[string]string{}
	}
	t.bindingReplies[name] = replyJSON
	t.mu.Unlock()
}

func (t *Tab) bindingReply(name string) (string, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	v, ok := t.bindingReplies[name]
	return v, ok
}

// BindingCalls returns calls recorded after sinceSID.
func (t *Tab) BindingCalls(sinceSID int64) []BindingCall {
	var out []BindingCall
	for _, e := range t.bindingRing.Since(sinceSID) {
		if c, ok := e.Data.(BindingCall); ok {
			c.SID = e.SID
			out = append(out, c)
		}
	}
	return out
}

const bindingInstaller = `(function(names){names.forEach(function(n){
  if (typeof globalThis[n] === 'function' && globalThis[n].__swBinding) return;
  var f = function(){ var args = Array.prototype.slice.call(arguments);
    var r = __sw.binding(n, JSON.stringify(args, function(k, v){ return (v && v.nodeType) ? {nodeType: v.nodeType, tagName: v.tagName} : v; }));
    return Promise.resolve(r === null || r === undefined ? undefined : JSON.parse(r)); };
  Object.defineProperty(f, '__swBinding', {value: true});
  Object.defineProperty(globalThis, n, {value: f, writable: true, configurable: true});
});})`

// installBindings defines every exposed binding in the VM.
func (j *jsVM) installBindings() {
	names := j.tab.Bindings()
	if len(names) == 0 {
		return
	}
	arr := make([]any, len(names))
	for i, n := range names {
		arr[i] = n
	}
	b, _ := json.Marshal(names)
	if _, err := j.run(bindingInstaller+"("+string(b)+")", "bindings"); err != nil {
		j.tab.LogConsole("error", "bindings: "+err.Error())
	}
}

// ---- downloads ------------------------------------------------------------------

// Download describes one saved file.
type Download struct {
	URL               string    `json:"url"`
	SuggestedFilename string    `json:"suggestedFilename"`
	Path              string    `json:"path"`
	Size              int64     `json:"size"`
	MimeType          string    `json:"mimeType,omitempty"`
	Error             string    `json:"error,omitempty"`
	At                time.Time `json:"at"`
}

var errDownloaded = errors.New("navigation became a download")

func (t *Tab) downloadsDir() string {
	d := t.eng.opts.DownloadsDir
	if d == "" {
		d = filepath.Join(os.TempDir(), "sweef-downloads")
	}
	_ = os.MkdirAll(d, 0o755)
	return d
}

// isDownloadResponse decides whether a navigation response is a file to save
// rather than a document to render.
func isDownloadResponse(h http.Header) bool {
	if cd := strings.ToLower(h.Get("Content-Disposition")); strings.HasPrefix(cd, "attachment") {
		return true
	}
	ct := strings.ToLower(h.Get("Content-Type"))
	if ct == "" {
		return false
	}
	mt, _, _ := mime.ParseMediaType(ct)
	switch {
	case mt == "text/html", mt == "application/xhtml+xml", strings.HasPrefix(mt, "text/"),
		mt == "application/xml", mt == "application/json", strings.HasSuffix(mt, "+xml"), strings.HasSuffix(mt, "+json"):
		return false
	}
	return true
}

func suggestedName(h http.Header, rawurl, fallback string) string {
	if fallback != "" {
		return filepath.Base(fallback)
	}
	if _, params, err := mime.ParseMediaType(h.Get("Content-Disposition")); err == nil {
		if fn := params["filename"]; fn != "" {
			return filepath.Base(fn)
		}
	}
	if u, err := url.Parse(rawurl); err == nil {
		if b := path.Base(u.Path); b != "" && b != "/" && b != "." {
			return b
		}
	}
	return "download"
}

// saveDownload streams body to the downloads directory and records it.
func (t *Tab) saveDownload(rawurl, name, mimeType string, body io.Reader) Download {
	dir := t.downloadsDir()
	dst := filepath.Join(dir, name)
	for i := 1; ; i++ {
		if _, err := os.Stat(dst); os.IsNotExist(err) {
			break
		}
		ext := filepath.Ext(name)
		dst = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(name, ext), i, ext))
	}
	d := Download{URL: rawurl, SuggestedFilename: name, Path: dst, MimeType: mimeType, At: time.Now()}
	f, err := os.Create(dst)
	if err != nil {
		d.Error = err.Error()
	} else {
		n, err := io.Copy(f, io.LimitReader(body, 512<<20))
		f.Close()
		d.Size = n
		if err != nil {
			d.Error = err.Error()
		}
	}
	t.mu.Lock()
	t.downloads = append(t.downloads, d)
	t.mu.Unlock()
	t.stateRing.Push(Event{Tab: t.ID, Kind: EvDownload, At: time.Now(), Data: d})
	t.emit(EvDownload, d)
	return d
}

// DownloadURL fetches a URL straight to the downloads directory
// (<a download>, or an agent asking for a file).
func (t *Tab) DownloadURL(rawurl, name string) (Download, error) {
	abs, err := resolveURL(t.URL(), rawurl)
	if err != nil {
		return Download{}, err
	}
	resp, _, outURL, hdrs, _, err := t.routedRequest("GET", abs, nil, "")
	if err != nil {
		return Download{}, err
	}
	if resp == nil {
		req, err := http.NewRequestWithContext(t.eng.ctx, http.MethodGet, outURL, nil)
		if err != nil {
			return Download{}, err
		}
		req.Header.Set("User-Agent", t.UserAgent())
		for k, v := range t.ExtraHeaders() {
			req.Header.Set(k, v)
		}
		for k, v := range hdrs {
			req.Header.Set(k, v)
		}
		t.touchNet()
		req, _ = withMeter(req, t.Context(), t, KindOther, t.policy())
		if resp, err = t.eng.clientFor(t.Context()).Do(req); err != nil {
			return Download{}, err
		}
	}
	defer resp.Body.Close()
	d := t.saveDownload(abs, suggestedName(resp.Header, abs, name), resp.Header.Get("Content-Type"), resp.Body)
	if d.Error != "" {
		return d, errors.New(d.Error)
	}
	return d, nil
}

// SaveDownloadBytes stores bytes produced by page code (blob: and data: URLs).
func (t *Tab) SaveDownloadBytes(name, mimeType string, data []byte, srcURL string) Download {
	if name == "" {
		name = "download"
	}
	return t.saveDownload(srcURL, filepath.Base(name), mimeType, strings.NewReader(string(data)))
}

// Downloads lists files saved by this tab.
func (t *Tab) Downloads() []Download {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return append([]Download(nil), t.downloads...)
}

func decodeDataURL(u string) (mimeType string, data []byte, err error) {
	rest := strings.TrimPrefix(u, "data:")
	comma := strings.IndexByte(rest, ',')
	if comma < 0 {
		return "", nil, fmt.Errorf("bad data URL")
	}
	meta, payload := rest[:comma], rest[comma+1:]
	mimeType = strings.TrimSuffix(meta, ";base64")
	if strings.HasSuffix(meta, ";base64") {
		data, err = base64.StdEncoding.DecodeString(payload)
		return
	}
	s, err := url.PathUnescape(payload)
	return mimeType, []byte(s), err
}

// ElementBoxes returns every element's layout box in document order (all
// coordinates in CSS px, document space). Used by the fidelity harness and
// diagnostics.
func (t *Tab) ElementBoxes() []fnetBoxRow {
	t.mu.RLock()
	d := t.dom
	w := t.vpW
	t.mu.RUnlock()
	if d == nil {
		return nil
	}
	return d.ElementBoxes(w)
}

type fnetBoxRow = fnet.BoxRow
