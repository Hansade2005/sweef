package main

// sweef.go: the Sweef side. One child process per (site, mode, run) so that cold means cold: a fresh process has a fresh
// engine, caches, cookie jar (the engine keeps a process-wide jar for its default context) and connection pool.
// The child drives the engine in-process, with a recording proxy of its own between the engine and the sandbox proxy.

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"context"
	"net"

	"github.com/sweefbrowser/sweef/internal/engine"
	fnet "github.com/sweefbrowser/sweef/internal/net"
)

type childOut struct {
	Cold   Visit  `json:"cold"`
	Warm   *Visit `json:"warm,omitempty"`
	Fatal  string `json:"fatal,omitempty"`
	Inject string `json:"proxy_injection,omitempty"`
}

// guiUserAgent is what the desktop app (internal/app/app.go userAgent()) sends on Linux by default. The Data Saver is a
// feature of that app, so it is the user agent the benchmark uses; the engine's own default ("Sweef/0.1 (agent;
// lightweight)") is an agent identifier that many sites block outright. -sweef-ua engine selects the latter.
const guiUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Safari/537.36 Sweef/0.1"

// sweefUA is the user agent for Sweef visits ("" = engine default).
var sweefUA = guiUserAgent

func sweefEngineOptions(cacheDir string) engine.Options {
	o := engine.DefaultOptions() // everything else is the product default, deliberately
	if sweefUA != "" {
		o.DefaultUserAgent = sweefUA
	}
	o.NetworkRingSize = 16384
	o.CacheDir = cacheDir
	return o
}

// sweefOptsForReport lists the engine options in the results.
func sweefOptsForReport() map[string]any {
	o := engine.DefaultOptions()
	ua := o.DefaultUserAgent
	if sweefUA != "" {
		ua = sweefUA
	}
	return map[string]any{
		"UserAgent": ua, "SettleMs": o.SettleMs, "MaxCacheMiB": o.MaxCacheMiB, "ViewportWidth": o.ViewportWidth,
		"ViewportHeight": o.ViewportHeight, "JSTimeoutMs": o.JSTimeoutMs, "DisableJS": o.DisableJS,
	}
}

// routeEngineThroughProxy points the engine's HTTP transport at proxyAddr. The engine builds its http.Transport
// without a Proxy func (internal/engine/client.go, wire.go) and ContextOptions.ProxyURL is not wired to anything, so
// HTTPS_PROXY is ignored. Rather than touch engine code, the harness reaches the context's lazily built client with
// reflect and sets Transport.Proxy. The engine's own counting dialer is kept (its DataStats must keep working) but
// wrapped in a guard that makes any dial that is not to the recorder fail loudly, so a request can never silently
// bypass the counter through this transport. WebSockets use a separate dialer (realtime.go) and are not covered; the
// harness fails a visit in which a page opened one.
func routeEngineThroughProxy(e *engine.Engine, ctx *engine.BrowserContext, proxyAddr string) (*http.Transport, error) {
	// Force the context to build its HTTP client with a request to a throwaway local server (no external traffic).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }))
	defer srv.Close()
	if _, _, _, err := ctx.Fetch("GET", srv.URL, nil, ""); err != nil {
		return nil, fmt.Errorf("priming engine client: %w", err)
	}
	// Newer engines give every non-default context its own client and transport; older ones share one per engine.
	var tr *http.Transport
	for _, root := range []reflect.Value{reflect.ValueOf(ctx), reflect.ValueOf(e)} {
		f := root.Elem().FieldByName("httpClient")
		if !f.IsValid() || f.Kind() != reflect.Ptr || f.IsNil() {
			continue
		}
		cl := (*http.Client)(unsafe.Pointer(f.Pointer()))
		if t := findTransport(cl.Transport, 0); t != nil {
			tr = t
			break
		}
	}
	if tr == nil {
		return nil, errors.New("could not find the engine's *http.Transport (engine refactored? add a proxy option there and drop this hack)")
	}
	pu, _ := url.Parse("http://" + proxyAddr)
	tr.Proxy = http.ProxyURL(pu)
	orig := tr.DialContext // keep the engine's own counting dialer when it has one
	var d net.Dialer
	tr.DialContext = func(c context.Context, network, addr string) (net.Conn, error) {
		if addr != proxyAddr {
			return nil, fmt.Errorf("harness guard: dial to %s would bypass the recording proxy", addr)
		}
		if orig != nil {
			return orig(c, network, addr)
		}
		return d.DialContext(c, network, addr)
	}
	tr.CloseIdleConnections()
	return tr, nil
}

// findTransport digs the *http.Transport out of (possibly wrapped, possibly unexported) RoundTrippers.
func findTransport(rt http.RoundTripper, depth int) *http.Transport {
	if rt == nil || depth > 4 {
		return nil
	}
	if t, ok := rt.(*http.Transport); ok {
		return t
	}
	v := reflect.ValueOf(rt)
	if v.Kind() != reflect.Ptr || v.IsNil() || v.Elem().Kind() != reflect.Struct {
		return nil
	}
	st := v.Elem()
	rtType := reflect.TypeOf((*http.RoundTripper)(nil)).Elem()
	for i := 0; i < st.NumField(); i++ {
		f := st.Field(i)
		if !f.Type().Implements(rtType) {
			continue
		}
		inner := reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem().Interface()
		if irt, ok := inner.(http.RoundTripper); ok {
			if t := findTransport(irt, depth+1); t != nil {
				return t
			}
		}
	}
	return nil
}

func guessType(contentType, rawurl string) string {
	ct := strings.ToLower(contentType)
	switch {
	case strings.Contains(ct, "text/html"):
		return "Document"
	case strings.Contains(ct, "css"):
		return "Stylesheet"
	case strings.Contains(ct, "javascript") || strings.Contains(ct, "ecmascript"):
		return "Script"
	case strings.HasPrefix(ct, "image/"):
		return "Image"
	case strings.Contains(ct, "json") || strings.Contains(ct, "xml"):
		return "XHR"
	case strings.HasPrefix(ct, "font/") || strings.Contains(ct, "woff"):
		return "Font"
	}
	return "Other"
}

// sweefVisit loads rawurl in a fresh tab of ctx and measures one visit.
func sweefVisit(e *engine.Engine, ctx *engine.BrowserContext, rec *Recorder, rawurl string, idle, navTimeout time.Duration, keep bool) Visit {
	v := Visit{BrowserIn: -1, BrowserOut: -1}
	before := rec.Snapshot()
	ecBefore, haveEng := engineCounters(ctx)

	type navRes struct {
		t   *engine.Tab
		err error
	}
	ch := make(chan navRes, 1)
	start := time.Now()
	go func() {
		t, err := e.NewTabInContext(ctx, rawurl)
		ch <- navRes{t, err}
	}()
	var t *engine.Tab
	select {
	case r := <-ch:
		t = r.t
		v.ReadyMs = float64(time.Since(start).Microseconds()) / 1000
		if r.err != nil {
			v.Fail = "navigate: " + r.err.Error()
		}
	case <-time.After(navTimeout):
		v.Fail = fmt.Sprintf("navigation not finished within %s", navTimeout)
		v.ReadyMs = float64(navTimeout.Milliseconds())
	}
	baseline := rawurl == "about:blank"

	// Same fixed idle window as every other browser: pump the page's event loop (timers, polling) for the window.
	readyAt := start.Add(time.Duration(v.ReadyMs * float64(time.Millisecond)))
	if t != nil {
		t.Settle(idle)
	}
	if rest := time.Until(readyAt.Add(idle)); rest > 0 {
		time.Sleep(rest)
	}

	tot := rec.Snapshot().Diff(before)
	v.WireIn, v.WireOut, v.Conns = tot.In, tot.Out, tot.Conns
	v.HandshakeIn, v.HandshakeOut = tot.HSIn, tot.HSOut
	v.Hosts = hostAggs(tot.Records)
	if tot.PlainRefused > 0 {
		v.Notes = append(v.Notes, fmt.Sprintf("%d plain-http request(s) refused by the recorder", tot.PlainRefused))
	}
	if tot.ConnectFailed > 0 {
		v.Notes = append(v.Notes, fmt.Sprintf("%d CONNECT(s) failed upstream", tot.ConnectFailed))
	}

	if t != nil {
		v.ByType = map[string]int64{}
		v.ByTypeCount = map[string]int{}
		first := true
		ws := 0
		for _, ev := range t.NetworkLog(0) {
			switch ev.Kind {
			case engine.EvRequest:
				v.Requests++
			case engine.EvWebSocket:
				ws++
			case engine.EvResponse:
				rr, ok := ev.Data.(fnet.RequestRecord)
				if !ok {
					continue
				}
				cached := rr.Headers["x-sweef-cache"] == "hit"
				if cached {
					v.Requests++ // cache hits push no EvRequest
					v.CacheServed++
				}
				ct := ""
				for k, val := range rr.ResponseHeaders {
					if strings.EqualFold(k, "content-type") {
						ct = val
					}
				}
				typ := guessType(ct, rr.URL)
				if first {
					v.Status = rr.Status // the first response in a fresh tab's log is the main document
					first = false
				}
				v.ByTypeCount[typ]++
				if keep {
					enc := int64(-1)
					for k, val := range rr.ResponseHeaders {
						if strings.EqualFold(k, "content-length") {
							if n, err := strconv.ParseInt(val, 10, 64); err == nil {
								enc = n
							}
						}
					}
					v.Resources = append(v.Resources, Resource{URL: trunc(rr.URL, 140), Type: typ, Status: rr.Status, Encoded: enc, FromCach: cached})
				}
			case engine.EvFailure:
				v.Requests++
			}
		}
		if ws > 0 {
			v.Notes = append(v.Notes, fmt.Sprintf("%d WebSocket event(s): the engine dials WebSockets outside the recording proxy, so those bytes are NOT in wire_in/out", ws))
			v.Fail = "unmeasured traffic: page used WebSocket"
		}
		e.CloseTab(t.ID)
	}
	if haveEng {
		ec, _ := engineCounters(ctx)
		d := EngineCounters{
			WireIn: ec.WireIn - ecBefore.WireIn, WireOut: ec.WireOut - ecBefore.WireOut,
			CacheHitBytes: ec.CacheHitBytes - ecBefore.CacheHitBytes, AbortedBytes: ec.AbortedBytes - ecBefore.AbortedBytes,
			SkippedEstimate: ec.SkippedEstimate - ecBefore.SkippedEstimate, Skipped: map[string]int{},
		}
		for k, n := range ec.Skipped {
			d.Skipped[k] = n - ecBefore.Skipped[k]
		}
		v.Engine = &d
	}
	if v.Fail == "" && !baseline {
		if v.Status == 0 {
			v.Fail = "no main-document response seen"
		} else if v.Status >= 400 {
			v.Fail = fmt.Sprintf("main document HTTP %d", v.Status)
		}
	}
	v.OK = v.Fail == ""
	return v
}

// childMain is `datasaver sweef-child`: prints one JSON object on stdout.
func childMain(args []string) {
	fs := flag.NewFlagSet("sweef-child", flag.ExitOnError)
	rawurl := fs.String("url", "", "")
	mode := fs.String("mode", ModeSweefOff, "")
	upstream := fs.String("upstream", "", "")
	idle := fs.Duration("idle", 5*time.Second, "")
	nav := fs.Duration("nav-timeout", 60*time.Second, "")
	warm := fs.Bool("warm", true, "")
	keep := fs.Bool("keep", false, "")
	ua := fs.String("ua", guiUserAgent, "")
	fs.Parse(args)
	sweefUA = *ua
	out := childOut{}
	emit := func() { json.NewEncoder(os.Stdout).Encode(out) }
	fail := func(msg string) {
		out.Fatal = msg
		out.Cold.Fail = msg
		emit()
		os.Exit(0)
	}

	rec, err := NewRecorder(*upstream)
	if err != nil {
		fail(err.Error())
	}
	defer rec.Close()
	cacheDir, err := os.MkdirTemp("", "dsbench-sweef-")
	if err != nil {
		fail(err.Error())
	}
	defer os.RemoveAll(cacheDir)
	e := engine.New(sweefEngineOptions(cacheDir))
	defer e.Close()
	ctx := e.NewContext("bench-"+strconv.FormatInt(time.Now().UnixNano(), 36), engine.ContextOptions{})
	tr, err := routeEngineThroughProxy(e, ctx, rec.Addr())
	if err != nil {
		fail(err.Error())
	}
	out.Inject = "reflect: Transport.Proxy set on the engine client"
	level := strings.TrimPrefix(*mode, "sweef-")
	if err := applyPolicy(ctx, level); err != nil {
		fail(err.Error())
	}

	out.Cold = sweefVisit(e, ctx, rec, *rawurl, *idle, *nav, *keep)
	if *warm && out.Cold.OK {
		tr.CloseIdleConnections() // second visit must open its own connections, as a fresh browser process would
		w := sweefVisit(e, ctx, rec, *rawurl, *idle, *nav, false)
		out.Warm = &w
	}
	emit()
}

// runSweefCell runs the child for one cell.
func runSweefCell(exe, mode, site, upstream string, idle, nav time.Duration, warm bool) (Cell, string) {
	cell := Cell{Site: site, Mode: mode}
	args := []string{"sweef-child", "-url", site, "-mode", mode, "-upstream", upstream, "-idle", idle.String(),
		"-nav-timeout", nav.String(), "-warm=" + strconv.FormatBool(warm), "-keep", "-ua", sweefUA}
	ctx, cancel := context.WithTimeout(context.Background(), 2*nav+2*idle+60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, args...)
	var stdout strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = nil
	err := cmd.Run()
	var co childOut
	line := stdout.String()
	if i := strings.LastIndex(strings.TrimSpace(line), "\n"); i >= 0 {
		line = line[i+1:]
	}
	if jerr := json.Unmarshal([]byte(strings.TrimSpace(line)), &co); jerr != nil {
		cell.Cold = Visit{Fail: fmt.Sprintf("child failed: %v / %v", err, jerr), BrowserIn: -1, BrowserOut: -1}
		return cell, ""
	}
	cell.Cold = co.Cold
	cell.Warm = co.Warm
	return cell, co.Inject
}
