package main

// chromium.go: headless Chromium driven over CDP, with every byte also counted by the recording proxy.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/websocket"
)

// chromiumFlags are the flags that keep Chromium from generating traffic that is not the page's. They are written
// into the results and the report. Nothing here changes how the page itself loads.
func chromiumFlags(proxyAddr, profile string) []string {
	return append(chromiumFlagsBase(proxyAddr, profile), strings.Fields(os.Getenv("DSBENCH_EXTRA_FLAGS"))...)
}

func chromiumFlagsBase(proxyAddr, profile string) []string {
	return []string{
		"--headless=new",
		"--no-sandbox",
		"--user-data-dir=" + profile,
		"--remote-debugging-port=0",
		"--remote-allow-origins=*",
		"--proxy-server=http://" + proxyAddr,
		"--window-size=1280,720", // same viewport as the Sweef engine default
		// only page traffic: no background networking of any kind
		"--disable-background-networking",
		"--disable-sync",
		"--disable-component-update",
		"--disable-default-apps",
		"--disable-domain-reliability",
		"--disable-client-side-phishing-detection",
		"--safebrowsing-disable-auto-update",
		"--disable-breakpad",
		"--metrics-recording-only",
		"--no-pings",
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-search-engine-choice-screen",
		"--password-store=basic",
		"--disable-features=OptimizationHints,OptimizationGuideModelDownloading,Translate,MediaRouter,AutofillServerCommunication,CertificateTransparencyComponentUpdater,PrivacySandboxSettings4,InterestFeedContentSuggestions,CalculateNativeWinOcclusion",
	}
}

// ---------------------------------------------------------------------------------------------------------------
// minimal CDP client (flat sessions)

type cdpMsg struct {
	ID     int             `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
}

type cdpConn struct {
	ws      *websocket.Conn
	wmu     sync.Mutex
	nextID  atomic.Int64
	mu      sync.Mutex
	pending map[int]chan cdpMsg
	onEvent func(sid, method string, params json.RawMessage)
	closed  atomic.Bool
}

func dialCDP(wsURL string, onEvent func(sid, method string, params json.RawMessage)) (*cdpConn, error) {
	cfg, err := websocket.NewConfig(wsURL, "http://127.0.0.1")
	if err != nil {
		return nil, err
	}
	ws, err := websocket.DialConfig(cfg)
	if err != nil {
		return nil, err
	}
	ws.MaxPayloadBytes = 256 << 20
	c := &cdpConn{ws: ws, pending: map[int]chan cdpMsg{}, onEvent: onEvent}
	go c.readLoop()
	return c, nil
}

func (c *cdpConn) readLoop() {
	for {
		var raw string
		if err := websocket.Message.Receive(c.ws, &raw); err != nil {
			c.closed.Store(true)
			c.mu.Lock()
			for id, ch := range c.pending {
				ch <- cdpMsg{ID: id, Error: &struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
				}{-1, "connection closed"}}
				delete(c.pending, id)
			}
			c.mu.Unlock()
			return
		}
		var m cdpMsg
		if json.Unmarshal([]byte(raw), &m) != nil {
			continue
		}
		if m.ID != 0 {
			c.mu.Lock()
			ch := c.pending[m.ID]
			delete(c.pending, m.ID)
			c.mu.Unlock()
			if ch != nil {
				ch <- m
			}
			continue
		}
		if m.Method != "" && c.onEvent != nil {
			c.onEvent(m.SessionID, m.Method, m.Params)
		}
	}
}

func (c *cdpConn) call(sid, method string, params any, timeout time.Duration) (json.RawMessage, error) {
	id := int(c.nextID.Add(1))
	ch := make(chan cdpMsg, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()
	msg := map[string]any{"id": id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	if sid != "" {
		msg["sessionId"] = sid
	}
	b, _ := json.Marshal(msg)
	c.wmu.Lock()
	err := websocket.Message.Send(c.ws, string(b))
	c.wmu.Unlock()
	if err != nil {
		return nil, err
	}
	select {
	case m := <-ch:
		if m.Error != nil {
			return nil, fmt.Errorf("%s: %s", method, m.Error.Message)
		}
		return m.Result, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("%s: timeout", method)
	}
}

func (c *cdpConn) close() { c.ws.Close() }

// ---------------------------------------------------------------------------------------------------------------
// per-launch event aggregation

type hop struct {
	url, typ, proto string
	method          string
	status          int
	encoded         int64
	finished        bool
	fromCache       bool
	failed          string
	reqHdrBytes     int64
	postBytes       int64
	saveData        bool
	sawExtra        bool
}

type aggregator struct {
	mu        sync.Mutex
	hops      map[string][]*hop // key sid|requestId
	mainID    string            // loaderId of the top-level navigation
	pageSID   string
	loadAt    time.Time
	loaded    chan struct{}
	loadOnce  sync.Once
	redirEnc  int64 // encodedDataLength of redirect responses
	wsCreated int
	armed     atomic.Bool // set just before Page.navigate: load events before that belong to about:blank
}

func newAggregator() *aggregator {
	return &aggregator{hops: map[string][]*hop{}, loaded: make(chan struct{})}
}

func isNetURL(u string) bool {
	return strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")
}

func hdrBytes(method, rawurl string, h map[string]any) int64 {
	path := rawurl
	if u, err := url.Parse(rawurl); err == nil {
		path = u.RequestURI()
	}
	n := int64(len(method) + 1 + len(path) + len(" HTTP/1.1\r\n"))
	for k, v := range h {
		if strings.HasPrefix(k, ":") {
			continue
		}
		s, _ := v.(string)
		n += int64(len(k) + 2 + len(s) + 2)
	}
	return n + 2
}

func (a *aggregator) event(sid, method string, params json.RawMessage) {
	switch method {
	case "Network.requestWillBeSent":
		var p struct {
			RequestID string `json:"requestId"`
			LoaderID  string `json:"loaderId"`
			Type      string `json:"type"`
			Request   struct {
				URL      string         `json:"url"`
				Method   string         `json:"method"`
				Headers  map[string]any `json:"headers"`
				PostData string         `json:"postData"`
			} `json:"request"`
			RedirectResponse *struct {
				EncodedDataLength float64 `json:"encodedDataLength"`
				Status            int     `json:"status"`
			} `json:"redirectResponse"`
		}
		if json.Unmarshal(params, &p) != nil {
			return
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		key := sid + "|" + p.RequestID
		if p.RedirectResponse != nil {
			if hs := a.hops[key]; len(hs) > 0 {
				last := hs[len(hs)-1]
				last.status = p.RedirectResponse.Status
				last.finished = true
				last.encoded = int64(p.RedirectResponse.EncodedDataLength)
				a.redirEnc += last.encoded
			}
		}
		h := &hop{url: p.Request.URL, typ: p.Type, method: p.Request.Method}
		h.reqHdrBytes = hdrBytes(p.Request.Method, p.Request.URL, p.Request.Headers)
		h.postBytes = int64(len(p.Request.PostData))
		for k, v := range p.Request.Headers {
			if strings.EqualFold(k, "save-data") {
				if s, _ := v.(string); strings.EqualFold(s, "on") {
					h.saveData = true
				}
			}
		}
		a.hops[key] = append(a.hops[key], h)
		if p.Type == "Document" && sid == a.pageSID && p.LoaderID == p.RequestID && a.mainID == "" {
			a.mainID = key
		}
	case "Network.requestWillBeSentExtraInfo":
		var p struct {
			RequestID string         `json:"requestId"`
			Headers   map[string]any `json:"headers"`
		}
		if json.Unmarshal(params, &p) != nil {
			return
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		if hs := a.hops[sid+"|"+p.RequestID]; len(hs) > 0 {
			h := hs[len(hs)-1]
			// the extra-info headers are the ones actually put on the wire (cookies included): prefer them
			h.reqHdrBytes = hdrBytes(h.method, h.url, p.Headers)
			h.sawExtra = true
			for k, v := range p.Headers {
				if strings.EqualFold(k, "save-data") {
					if s, _ := v.(string); strings.EqualFold(s, "on") {
						h.saveData = true
					}
				}
			}
		}
	case "Network.responseReceived":
		var p struct {
			RequestID string `json:"requestId"`
			Type      string `json:"type"`
			Response  struct {
				Status        float64 `json:"status"`
				Protocol      string  `json:"protocol"`
				FromDiskCache bool    `json:"fromDiskCache"`
				FromPrefetch  bool    `json:"fromPrefetchCache"`
				FromSW        bool    `json:"fromServiceWorker"`
			} `json:"response"`
		}
		if json.Unmarshal(params, &p) != nil {
			return
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		if hs := a.hops[sid+"|"+p.RequestID]; len(hs) > 0 {
			h := hs[len(hs)-1]
			h.status = int(p.Response.Status)
			h.proto = p.Response.Protocol
			if p.Type != "" {
				h.typ = p.Type
			}
			if p.Response.FromDiskCache || p.Response.FromPrefetch || p.Response.FromSW {
				h.fromCache = true
			}
		}
	case "Network.requestServedFromCache":
		var p struct {
			RequestID string `json:"requestId"`
		}
		if json.Unmarshal(params, &p) != nil {
			return
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		if hs := a.hops[sid+"|"+p.RequestID]; len(hs) > 0 {
			hs[len(hs)-1].fromCache = true
		}
	case "Network.loadingFinished":
		var p struct {
			RequestID         string  `json:"requestId"`
			EncodedDataLength float64 `json:"encodedDataLength"`
		}
		if json.Unmarshal(params, &p) != nil {
			return
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		if hs := a.hops[sid+"|"+p.RequestID]; len(hs) > 0 {
			h := hs[len(hs)-1]
			h.finished = true
			h.encoded = int64(p.EncodedDataLength)
		}
	case "Network.loadingFailed":
		var p struct {
			RequestID string `json:"requestId"`
			ErrorText string `json:"errorText"`
			Canceled  bool   `json:"canceled"`
		}
		if json.Unmarshal(params, &p) != nil {
			return
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		if hs := a.hops[sid+"|"+p.RequestID]; len(hs) > 0 {
			h := hs[len(hs)-1]
			h.failed = p.ErrorText
			if p.Canceled {
				h.failed = "canceled"
			}
		}
	case "Network.webSocketCreated":
		a.mu.Lock()
		a.wsCreated++
		a.mu.Unlock()
	case "Page.loadEventFired":
		a.mu.Lock()
		isPage := sid == a.pageSID && a.pageSID != ""
		a.mu.Unlock()
		if isPage && a.armed.Load() {
			a.loadOnce.Do(func() {
				a.loadAt = time.Now()
				close(a.loaded)
			})
		}
	}
}

// fill copies the aggregate into v.
func (a *aggregator) fill(v *Visit, keepResources bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	v.ByType = map[string]int64{}
	v.ByTypeCount = map[string]int{}
	v.Protos = map[string]int{}
	var in, out int64
	for key, hs := range a.hops {
		for i, h := range hs {
			if !isNetURL(h.url) {
				continue
			}
			v.Requests++
			if h.fromCache {
				v.CacheServed++
			}
			in += h.encoded
			if u, err := url.Parse(h.url); err == nil {
				if v.CDPHostIn == nil {
					v.CDPHostIn = map[string]int64{}
				}
				v.CDPHostIn[u.Host] += h.encoded
			}
			if !h.fromCache {
				out += h.reqHdrBytes + h.postBytes
			}
			if h.saveData {
				v.SaveDataReq++
			}
			t := h.typ
			if t == "" {
				t = "Other"
			}
			v.ByType[t] += h.encoded
			v.ByTypeCount[t]++
			if h.proto != "" {
				v.Protos[h.proto]++
			}
			if key == a.mainID && i == len(hs)-1 {
				v.Status = h.status
				v.DocBytes = h.encoded
			}
			if keepResources {
				r := Resource{URL: trunc(h.url, 140), Type: t, Status: h.status, Proto: h.proto, Encoded: h.encoded, FromCach: h.fromCache, Failed: h.failed}
				v.Resources = append(v.Resources, r)
			}
		}
	}
	v.BrowserIn = in
	v.BrowserOut = out
	if a.wsCreated > 0 {
		v.Notes = append(v.Notes, fmt.Sprintf("%d WebSocket(s) opened; frame payloads are included only in the proxy counters", a.wsCreated))
	}
}

// chromeInternalSuffixes are hosts Chromium itself talks to on startup (time sync, GAIA account list, GCM, search
// provider checks) even with background networking switched off; the flags we know of do not stop them (see the
// about:blank baseline in the report). A connection counts as browser-internal, and is reported separately instead of
// being added to the page's bytes, only if BOTH hold: the host matches this list AND no request the page made (as seen
// over CDP, every frame, worker and service worker) went to that host.
var chromeInternalSuffixes = []string{
	"accounts.google.com", "android.clients.google.com", "mtalk.google.com", "clients2.google.com", "clients1.google.com", "clients4.google.com",
	"www.google.com", "update.googleapis.com", "optimizationguide-pa.googleapis.com", "safebrowsing.googleapis.com",
	"content-autofill.googleapis.com", "clientservices.googleapis.com", "csp.withgoogle.com", "gvt1.com", "gvt2.com",
	"tools.google.com", "edgedl.me.gvt1.com", "www.gstatic.com",
}

func chromeInternalHost(h string) bool {
	for _, s := range chromeInternalSuffixes {
		if h == s || strings.HasSuffix(h, "."+s) {
			return true
		}
	}
	return false
}

// requestHosts is the set of hostnames the page asked for.
func (a *aggregator) requestHosts() map[string]bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	m := map[string]bool{}
	for _, hs := range a.hops {
		for _, h := range hs {
			if u, err := url.Parse(h.url); err == nil && u.Hostname() != "" {
				m[u.Hostname()] = true
			}
		}
	}
	return m
}

// splitInternal removes browser-internal connections from tot and returns them separately.
func splitInternal(tot Totals, pageHosts map[string]bool) (page Totals, internal Totals, hosts []string) {
	page = Totals{PlainRefused: tot.PlainRefused, PlainHosts: tot.PlainHosts, ConnectFailed: tot.ConnectFailed, FailedConnects: tot.FailedConnects}
	seen := map[string]bool{}
	for _, r := range tot.Records {
		h := r.Host
		if i := strings.LastIndex(h, ":"); i >= 0 {
			h = h[:i]
		}
		if chromeInternalHost(h) && !pageHosts[h] {
			internal.In += r.In
			internal.Out += r.Out
			internal.Conns++
			internal.Records = append(internal.Records, r)
			if !seen[h] {
				seen[h] = true
				hosts = append(hosts, h)
			}
			continue
		}
		page.In += r.In
		page.Out += r.Out
		page.Conns++
		page.Records = append(page.Records, r)
	}
	return
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// ---------------------------------------------------------------------------------------------------------------
// the runner

type ChromiumOpts struct {
	Binary     string
	Upstream   string
	Idle       time.Duration
	NavTimeout time.Duration
	UA         string // "chrome" (strip Headless from the UA) or "headless" (leave the default)
	Keep       bool   // keep resource lists
}

// chromiumLaunch runs one browser process on `profile` and measures one visit (url == "about:blank" gives a baseline).
// It returns the visit and the totals, and the browser version string.
func chromiumLaunch(o ChromiumOpts, mode, rawurl, profile string, keep bool) (Visit, string, string, []string, error) {
	var v Visit
	v.BrowserIn, v.BrowserOut = -1, -1
	rec, err := NewRecorder(o.Upstream)
	if err != nil {
		return v, "", "", nil, err
	}
	defer rec.Close()

	flags := chromiumFlags(rec.Addr(), profile)
	if mode == ModeChromiumNoImg {
		flags = append(flags, "--blink-settings=imagesEnabled=false")
	}
	_ = os.Remove(filepath.Join(profile, "DevToolsActivePort"))
	cmd := exec.Command(o.Binary, append(flags, "about:blank")...)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	procGroup(cmd)
	if err := cmd.Start(); err != nil {
		return v, "", "", flags, err
	}
	exited := make(chan struct{})
	go func() { cmd.Wait(); close(exited) }()
	killAll := func() {
		killGroup(cmd)
		<-exited
	}
	defer func() {
		select {
		case <-exited:
		default:
			killAll()
		}
	}()

	// DevToolsActivePort: line 1 = port, line 2 = browser ws path
	var port, wspath string
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(filepath.Join(profile, "DevToolsActivePort"))
		if err == nil {
			ls := strings.Split(strings.TrimSpace(string(b)), "\n")
			if len(ls) >= 2 {
				port, wspath = strings.TrimSpace(ls[0]), strings.TrimSpace(ls[1])
				break
			}
		}
		select {
		case <-exited:
			return v, "", "", flags, errors.New("chromium exited during startup")
		case <-time.After(50 * time.Millisecond):
		}
	}
	if port == "" {
		return v, "", "", flags, errors.New("chromium did not publish DevToolsActivePort")
	}

	agg := newAggregator()
	var conn *cdpConn
	var setupMu sync.Mutex
	pageReady := make(chan struct{})
	var pageOnce sync.Once
	var ua, version string

	saveData := mode == ModeChromiumSD
	setup := func(sid, typ string) {
		call := func(m string, p any) { conn.call(sid, m, p, 10*time.Second) }
		call("Network.enable", map[string]any{"maxTotalBufferSize": 1 << 20, "maxResourceBufferSize": 1 << 16})
		if saveData {
			call("Network.setExtraHTTPHeaders", map[string]any{"headers": map[string]string{"Save-Data": "on"}})
		}
		if ua != "" {
			call("Network.setUserAgentOverride", map[string]any{"userAgent": ua, "userAgentMetadata": uaMetadata(version)})
		}
		if typ == "page" {
			call("Page.enable", nil)
		}
		call("Target.setAutoAttach", map[string]any{"autoAttach": true, "waitForDebuggerOnStart": true, "flatten": true})
		if typ == "page" {
			setupMu.Lock()
			agg.mu.Lock()
			if agg.pageSID == "" {
				agg.pageSID = sid
			}
			agg.mu.Unlock()
			setupMu.Unlock()
			pageOnce.Do(func() { close(pageReady) })
		}
		call("Runtime.runIfWaitingForDebugger", nil)
	}
	onEvent := func(sid, method string, params json.RawMessage) {
		if method == "Target.attachedToTarget" {
			var p struct {
				SessionID  string `json:"sessionId"`
				TargetInfo struct {
					Type string `json:"type"`
				} `json:"targetInfo"`
			}
			if json.Unmarshal(params, &p) == nil {
				switch p.TargetInfo.Type {
				case "page", "iframe", "worker", "service_worker", "shared_worker":
					go setup(p.SessionID, p.TargetInfo.Type)
				default:
					go conn.call(p.SessionID, "Runtime.runIfWaitingForDebugger", nil, 5*time.Second)
				}
			}
			return
		}
		agg.event(sid, method, params)
	}

	wsURL := "ws://127.0.0.1:" + port + wspath
	conn, err = dialCDP(wsURL, onEvent)
	if err != nil {
		return v, "", "", flags, err
	}
	defer conn.close()

	if res, err := conn.call("", "Browser.getVersion", nil, 10*time.Second); err == nil {
		var gv struct {
			Product   string `json:"product"`
			UserAgent string `json:"userAgent"`
		}
		json.Unmarshal(res, &gv)
		version = gv.Product
		if o.UA != "headless" {
			ua = strings.ReplaceAll(gv.UserAgent, "HeadlessChrome", "Chrome")
		} else {
			ua = gv.UserAgent
		}
	}
	if _, err := conn.call("", "Target.setAutoAttach", map[string]any{"autoAttach": true, "waitForDebuggerOnStart": true, "flatten": true}, 10*time.Second); err != nil {
		return v, version, ua, flags, err
	}
	if _, err := conn.call("", "Target.createTarget", map[string]any{"url": "about:blank"}, 10*time.Second); err != nil {
		return v, version, ua, flags, err
	}
	select {
	case <-pageReady:
	case <-time.After(15 * time.Second):
		return v, version, ua, flags, errors.New("page target never became ready")
	}
	agg.mu.Lock()
	psid := agg.pageSID
	agg.mu.Unlock()
	agg.armed.Store(true)
	t0 := time.Now()
	baseline := rawurl == "about:blank"
	if !baseline {
		res, err := conn.call(psid, "Page.navigate", map[string]any{"url": rawurl}, o.NavTimeout)
		if err != nil {
			v.Fail = "navigate: " + err.Error()
		} else {
			var nr struct {
				ErrorText string `json:"errorText"`
			}
			json.Unmarshal(res, &nr)
			if nr.ErrorText != "" {
				v.Fail = "navigate: " + nr.ErrorText
			}
		}
		if v.Fail == "" {
			select {
			case <-agg.loaded:
				v.ReadyMs = float64(agg.loadAt.Sub(t0).Microseconds()) / 1000
			case <-time.After(o.NavTimeout - time.Since(t0)):
				v.Fail = fmt.Sprintf("load event not fired within %s", o.NavTimeout)
				v.ReadyMs = float64(o.NavTimeout.Milliseconds())
			}
		}
	}
	// fixed idle window, identical for every browser
	time.Sleep(o.Idle)
	tot := rec.Snapshot()
	agg.fill(&v, keep)
	tot, internal, ihosts := splitInternal(tot, agg.requestHosts())
	v.InternalIn, v.InternalOut, v.InternalConns, v.InternalHosts = internal.In, internal.Out, internal.Conns, ihosts

	v.WireIn, v.WireOut, v.Conns = tot.In, tot.Out, tot.Conns
	v.HandshakeIn, v.HandshakeOut = tot.HSIn, tot.HSOut
	v.Hosts = hostAggs(tot.Records)
	var pageHosts []string
	for _, h := range tot.PlainHosts {
		hn := h
		if i := strings.LastIndex(hn, ":"); i >= 0 {
			hn = hn[:i]
		}
		if !chromeInternalHost(hn) {
			pageHosts = append(pageHosts, h)
		}
	}
	if len(pageHosts) > 0 {
		v.Notes = append(v.Notes, fmt.Sprintf("%d plain-http request(s) refused by the recorder (not forwarded, not counted): %s", len(pageHosts), strings.Join(pageHosts, ", ")))
	}
	if tot.ConnectFailed > 0 {
		v.Notes = append(v.Notes, fmt.Sprintf("%d CONNECT(s) failed upstream: %s", tot.ConnectFailed, strings.Join(tot.FailedConnects, "; ")))
	}
	if v.Fail == "" && !baseline {
		if v.Status == 0 {
			v.Fail = "no main-document response seen"
		} else if v.Status >= 400 {
			v.Fail = fmt.Sprintf("main document HTTP %d", v.Status)
		}
	}
	v.OK = v.Fail == ""

	// orderly shutdown so the disk cache is flushed for the warm visit
	conn.call("", "Browser.close", nil, 5*time.Second)
	select {
	case <-exited:
	case <-time.After(10 * time.Second):
		killAll()
	}
	return v, version, ua, flags, nil
}

func uaMetadata(version string) map[string]any {
	major := "141"
	full := "141.0.0.0"
	if i := strings.Index(version, "/"); i >= 0 {
		full = version[i+1:]
		if j := strings.Index(full, "."); j > 0 {
			major = full[:j]
		}
	}
	return map[string]any{
		"brands": []map[string]string{
			{"brand": "Chromium", "version": major},
			{"brand": "Google Chrome", "version": major},
			{"brand": "Not=A?Brand", "version": "24"},
		},
		"fullVersion": full, "platform": "Linux", "platformVersion": "6.0", "architecture": "x86", "model": "", "mobile": false,
	}
}

// runChromiumCell does the cold visit (fresh profile) then the warm visit (new process, same profile).
func runChromiumCell(o ChromiumOpts, mode, site string, warm bool) (Cell, error) {
	cell := Cell{Site: site, Mode: mode}
	dir, err := os.MkdirTemp("", "dsbench-chrome-")
	if err != nil {
		return cell, err
	}
	defer os.RemoveAll(dir)
	v, _, _, _, err := chromiumLaunch(o, mode, site, dir, true)
	if err != nil {
		v.Fail = err.Error()
		cell.Cold = v
		return cell, nil
	}
	cell.Cold = v
	if warm && v.OK {
		w, _, _, _, err := chromiumLaunch(o, mode, site, dir, false)
		if err != nil {
			w.Fail = err.Error()
		}
		cell.Warm = &w
	}
	return cell, nil
}

// chromiumInfo returns the product version and effective UA by launching once on about:blank.
func chromiumInfo(o ChromiumOpts) (string, string, []string, error) {
	dir, err := os.MkdirTemp("", "dsbench-chrome-")
	if err != nil {
		return "", "", nil, err
	}
	defer os.RemoveAll(dir)
	oo := o
	oo.Idle = 200 * time.Millisecond
	_, ver, ua, flags, err := chromiumLaunch(oo, ModeChromium, "about:blank", dir, false)
	return ver, ua, flags, err
}
