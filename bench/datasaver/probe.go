package main

// probe.go: `datasaver probe -url U [-mode off|standard|strict] [-warm]` loads U in a fresh engine and prints one line per
// network event of the visit (method, URL, status, cache decision, validators, cache-related response headers), then the
// same for a second visit in the same context. It is the diagnostic behind the cache and header work: it shows exactly
// what a warm visit refetches and why. Traffic goes through $HTTPS_PROXY like any other engine traffic.

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/sweefbrowser/sweef/internal/engine"
	fnet "github.com/sweefbrowser/sweef/internal/net"
)

func probeMain(args []string) {
	fs := flag.NewFlagSet("probe", flag.ExitOnError)
	rawurl := fs.String("url", "", "page to load")
	mode := fs.String("mode", "off", "off, standard or strict")
	warm := fs.Bool("warm", true, "load a second time in the same context")
	idle := fs.Duration("idle", 5*time.Second, "idle window after each load")
	ua := fs.String("ua", "", "user agent (default: the engine's default)")
	restart := fs.Bool("restart", false, "drop the in-memory cache between the visits (only the disk cache survives)")
	allH := fs.Bool("headers", false, "print all request and response headers")
	fs.Parse(args)
	dir, _ := os.MkdirTemp("", "dsprobe-")
	defer os.RemoveAll(dir)
	o := engine.DefaultOptions()
	o.NetworkRingSize = 16384
	o.CacheDir = dir
	if *ua != "" {
		o.DefaultUserAgent = *ua
	}
	e := engine.New(o)
	defer e.Close()
	ctx := e.DefaultContext() // the context the app browses in: the only one with the on-disk cache
	if err := applyPolicy(ctx, *mode); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	visits := 1
	if *warm {
		visits = 2
	}
	for v := 1; v <= visits; v++ {
		if v == 2 && *restart {
			e.DropMemoryCache()
		}
		before, _ := engineCounters(ctx)
		start := time.Now()
		t, err := e.NewTabInContext(ctx, *rawurl)
		fmt.Printf("=== visit %d (%s): ready in %v err=%v\n", v, *mode, time.Since(start).Round(time.Millisecond), err)
		if t == nil {
			continue
		}
		t.Settle(*idle)
		after, _ := engineCounters(ctx)
		pending := map[string]fnet.RequestRecord{}
		for _, ev := range t.NetworkLog(0) {
			switch ev.Kind {
			case engine.EvRequest:
				if rr, ok := ev.Data.(fnet.RequestRecord); ok {
					pending[rr.URL] = rr
				}
			case engine.EvFailure:
				fmt.Printf("FAIL  %v\n", ev.Data)
			case engine.EvResponse:
				rr, ok := ev.Data.(fnet.RequestRecord)
				if !ok {
					continue
				}
				req := pending[rr.URL]
				delete(pending, rr.URL)
				dec := "network"
				if rr.Headers["x-sweef-cache"] == "hit" {
					dec = "CACHE-HIT"
				} else if rr.Headers["x-sweef-route"] != "" {
					dec = "routed"
				}
				cond := ""
				for k, val := range req.Headers {
					lk := strings.ToLower(k)
					if lk == "if-none-match" || lk == "if-modified-since" {
						cond += " " + lk + "=" + trunc(val, 24)
					}
				}
				if cond != "" {
					dec = "REVALIDATE"
				}
				rh := map[string]string{}
				for k, val := range rr.ResponseHeaders {
					rh[strings.ToLower(k)] = val
				}
				fmt.Printf("%-10s %-4s %3d %s%s  [%s]\n", dec, rr.Method, rr.Status, trunc(rr.URL, 150), cond, rr.Headers["x-sweef-cache"])
				fmt.Printf("           resp: cc=%q exp=%q etag=%q lm=%q vary=%q age=%q ce=%q cl=%q ct=%q\n", rh["cache-control"], rh["expires"], trunc(rh["etag"], 24), rh["last-modified"], rh["vary"], rh["age"], rh["content-encoding"], rh["content-length"], trunc(rh["content-type"], 40))
				if *allH {
					printMap("  req ", req.Headers)
					printMap("  resp", rr.ResponseHeaders)
				}
			}
		}
		fmt.Printf("--- visit %d wire in=%d out=%d cacheHitBytes=%d\n", v, after.WireIn-before.WireIn, after.WireOut-before.WireOut, after.CacheHitBytes-before.CacheHitBytes)
		e.CloseTab(t.ID)
	}
}

func printMap(prefix string, m map[string]string) {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	for _, k := range ks {
		fmt.Printf("%s %s: %s\n", prefix, k, trunc(m[k], 200))
	}
}
