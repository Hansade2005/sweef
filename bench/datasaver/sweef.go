package main

// sweef.go: the Sweef side. One child process per (site, mode, run) so that cold means cold: a fresh process has a fresh
// engine, caches, cookie jar (the engine keeps a process-wide jar for its default context) and connection pool.
// The child drives the engine in-process, with a recording proxy of its own between the engine and the sandbox proxy.

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"context"

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
			v.Notes = append(v.Notes, fmt.Sprintf("%d WebSocket event(s); the engine dials WebSockets through the same proxy, so their bytes are in wire_in/out", ws))
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
	// Every request, WebSockets included, goes to the recorder through the engine's own proxy option.
	ctx := e.NewContext("bench-"+strconv.FormatInt(time.Now().UnixNano(), 36), engine.ContextOptions{ProxyURL: "http://" + rec.Addr()})
	out.Inject = "ContextOptions.ProxyURL"
	level := strings.TrimPrefix(*mode, "sweef-")
	if err := applyPolicy(ctx, level); err != nil {
		fail(err.Error())
	}

	out.Cold = sweefVisit(e, ctx, rec, *rawurl, *idle, *nav, *keep)
	if *warm && out.Cold.OK {
		closeIdle(ctx) // second visit must open its own connections, as a fresh browser process would
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

// closeIdle drops the context's idle connections so that the next visit opens its own.
func closeIdle(ctx *engine.BrowserContext) {
	if ci, ok := ctx.CountedTransport().(interface{ CloseIdleConnections() }); ok {
		ci.CloseIdleConnections()
	}
}
