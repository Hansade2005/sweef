// Command datasaver measures how many bytes a page costs on the wire in Sweef (off / standard / strict) and in headless
// Chromium, the same way for every browser, and writes dated JSON results plus a markdown report.
//
//	go run ./bench/datasaver -sites bench/datasaver/sites.txt -runs 3
//	go run ./bench/datasaver report -in bench/datasaver/results/<file>.json -md docs/data-saver-bench.md
//
// See docs/data-saver.md (Benchmark) for the contract and docs/data-saver-bench.md for the method.
package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// buildSource is set with -ldflags "-X main.buildSource=<commit> (clean snapshot)" by scripts that build from a snapshot.
var buildSource string

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "sweef-child":
			childMain(os.Args[2:])
			return
		case "report":
			reportMain(os.Args[2:])
			return
		}
	}
	runMain(os.Args[1:])
}

func loadSites(path string) ([]Site, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	var sites []Site
	var body strings.Builder
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		body.WriteString(sc.Text() + "\n")
		u, reason, _ := strings.Cut(line, "|")
		sites = append(sites, Site{URL: strings.TrimSpace(u), Reason: strings.TrimSpace(reason)})
	}
	sum := sha256.Sum256([]byte(body.String()))
	return sites, hex.EncodeToString(sum[:]), sc.Err()
}

func runMain(args []string) {
	fs := flag.NewFlagSet("datasaver", flag.ExitOnError)
	sitesPath := fs.String("sites", "bench/datasaver/sites.txt", "frozen site list")
	runs := fs.Int("runs", 3, "runs per site and mode (each run = cold visit then warm visit)")
	modesFlag := fs.String("modes", strings.Join(defaultModes, ","), "modes to run (chromium-noimg is optional context)")
	idle := fs.Duration("idle", 5*time.Second, "fixed idle window after load, identical for every browser")
	nav := fs.Duration("nav-timeout", 60*time.Second, "navigation timeout")
	outDir := fs.String("out", "bench/datasaver/results", "results directory")
	label := fs.String("label", "run", "label in the result file name")
	upstream := fs.String("upstream", firstEnv("HTTPS_PROXY", "https_proxy"), "upstream (sandbox) proxy, empty = direct")
	chrome := fs.String("chromium", "/opt/pw-browsers/chromium", "Chromium binary")
	uaMode := fs.String("chromium-ua", "chrome", `"chrome" strips "Headless" from the UA string, "headless" keeps the default`)
	only := fs.String("only", "", "substring filter on site URLs (debug; the run is then marked incomplete and no report is written)")
	resume := fs.String("resume", "", "result file to continue (skips finished cells)")
	mdPath := fs.String("md", "docs/data-saver-bench.md", "markdown report to (re)generate after a complete run; empty = skip")
	allowModified := fs.Bool("allow-modified-sites", false, "run even though sites.txt does not match sites.txt.sha256 (marks the run as such)")
	swUA := fs.String("sweef-ua", "gui", `Sweef user agent: "gui" (the desktop app's default), "engine" (the engine's agent UA) or a literal string`)
	noBaseline := fs.Bool("no-baseline", false, "skip the about:blank baselines")
	fs.Parse(args)

	switch *swUA {
	case "gui":
		sweefUA = guiUserAgent
	case "engine":
		sweefUA = ""
	default:
		sweefUA = *swUA
	}
	sites, hash, err := loadSites(*sitesPath)
	if err != nil {
		fatalf("sites: %v", err)
	}
	if want, err := os.ReadFile(*sitesPath + ".sha256"); err == nil {
		if strings.TrimSpace(string(want)) != hash && !*allowModified {
			fatalf("%s no longer matches %s.sha256: the list is frozen. Restore it, or pass -allow-modified-sites (the result is then labelled as modified).", *sitesPath, *sitesPath)
		}
	}
	exe, err := os.Executable()
	if err != nil {
		fatalf("%v", err)
	}
	filtered := false
	if *only != "" {
		var keep []Site
		for _, s := range sites {
			if strings.Contains(s.URL, *only) {
				keep = append(keep, s)
			}
		}
		sites, filtered = keep, true
	}

	wanted := splitCSV(*modesFlag)
	res := &Results{Schema: 1, Label: *label, Started: time.Now().UTC().Format(time.RFC3339), SitesHash: hash, Sites: sites,
		ModesWanted: wanted, Runs: *runs, IdleSec: idle.Seconds(), Skipped: map[string]string{}}
	if *allowModified {
		res.Skipped["sites.txt"] = "modified after freezing (hash mismatch)"
	}
	outPath := filepath.Join(*outDir, time.Now().Format("2006-01-02")+"-"+*label+".json")
	if *resume != "" {
		b, err := os.ReadFile(*resume)
		if err != nil {
			fatalf("resume: %v", err)
		}
		var old Results
		if err := json.Unmarshal(b, &old); err != nil {
			fatalf("resume: %v", err)
		}
		if old.SitesHash != hash {
			fatalf("resume: result file was made with a different site list")
		}
		res = &old
		res.Complete = false
		res.Finished = ""
		outPath = *resume
	}

	co := ChromiumOpts{Binary: *chrome, Upstream: *upstream, Idle: *idle, NavTimeout: *nav, UA: *uaMode}
	// Modes that can run in this build.
	var modes []string
	for _, m := range wanted {
		switch {
		case m == ModeSweefStandard || m == ModeSweefStrict:
			if !policyAPI() {
				res.Skipped[m] = "engine DataPolicy API not present in this build (see scripts/bench/datasaver.sh)"
				fmt.Fprintf(os.Stderr, "skipping %s: %s\n", m, res.Skipped[m])
				continue
			}
		}
		modes = append(modes, m)
	}
	res.Modes = modes
	res.Env = collectEnv(co, *upstream)
	res.Env.PolicyAPI = policyAPI()
	res.Env.ChromiumFlags = chromiumFlags("<recorder 127.0.0.1:PORT>", "<fresh temp dir>")

	save := func() {
		os.MkdirAll(*outDir, 0o755)
		b, _ := json.MarshalIndent(res, "", " ")
		tmp := outPath + ".tmp"
		os.WriteFile(tmp, b, 0o644)
		os.Rename(tmp, outPath)
	}
	have := map[string]bool{}
	for _, c := range res.Cells {
		have[fmt.Sprintf("%s|%s|%d", c.Site, c.Mode, c.Run)] = true
	}

	// Baselines: what a browser sends with no page at all, with the same flags and the same idle window.
	if !*noBaseline && len(res.Baselines) == 0 {
		for _, m := range modes {
			for r := 1; r <= *runs; r++ {
				var v Visit
				if isSweef(m) {
					cell, _ := runSweefCell(exe, m, "about:blank", *upstream, *idle, *nav, false)
					v = cell.Cold
				} else {
					d, _ := os.MkdirTemp("", "dsbench-chrome-")
					v, _, _, _, _ = chromiumLaunch(co, m, "about:blank", d, false)
					os.RemoveAll(d)
				}
				hosts := append([]string(nil), v.InternalHosts...)
				for _, h := range v.Hosts {
					hosts = append(hosts, h.Host)
				}
				res.Baselines = append(res.Baselines, Baseline{Mode: m, Run: r, WireIn: v.WireIn, WireOut: v.WireOut, Conns: v.Conns, Hosts: hosts,
					InternalIn: v.InternalIn, InternalOut: v.InternalOut})
				fmt.Fprintf(os.Stderr, "baseline %-18s run %d: page in=%d out=%d conns=%d; browser-internal in=%d out=%d %v\n", m, r, v.WireIn, v.WireOut, v.Conns, v.InternalIn, v.InternalOut, v.InternalHosts)
			}
		}
		save()
	}

	failedBefore := map[string]string{}
	for _, c := range res.Cells {
		if !c.OK() {
			failedBefore[c.Site+"|"+c.Mode] = firstFail(c)
		}
	}
	total := len(sites) * *runs * len(modes)
	done := 0
	for _, s := range sites {
		for r := 1; r <= *runs; r++ {
			// rotate the mode order per run so no mode is always first or last (cache warmth upstream, time of day)
			for i := range modes {
				m := modes[(i+r-1)%len(modes)]
				done++
				if have[fmt.Sprintf("%s|%s|%d", s.URL, m, r)] {
					continue
				}
				var cell Cell
				attempts := 0
				if prev := failedBefore[s.URL+"|"+m]; prev != "" {
					// the site is already going to be excluded everywhere; do not burn minutes proving it again
					cell = Cell{Cold: Visit{Fail: "not attempted, an earlier run failed: " + prev, BrowserIn: -1, BrowserOut: -1}}
					cell.Site, cell.Mode, cell.Run = s.URL, m, r
					res.Cells = append(res.Cells, cell)
					save()
					fmt.Fprintf(os.Stderr, "[%d/%d] %-18s run %d %-60s skipped (failed earlier)\n", done, total, m, r, trunc(s.URL, 60))
					continue
				}
				for attempts < 2 {
					attempts++
					if isSweef(m) {
						cell, _ = runSweefCell(exe, m, s.URL, *upstream, *idle, *nav, true)
					} else {
						cell, _ = runChromiumCell(co, m, s.URL, true)
					}
					if cell.OK() || !retryable(cell) {
						break
					}
					fmt.Fprintf(os.Stderr, "  retrying %s %s run %d after: %s\n", m, s.URL, r, firstFail(cell))
				}
				cell.Site, cell.Mode, cell.Run, cell.Attempts = s.URL, m, r, attempts
				if !cell.OK() {
					failedBefore[s.URL+"|"+m] = firstFail(cell)
				}
				res.Cells = append(res.Cells, cell)
				save()
				st := "ok"
				if !cell.OK() {
					st = "FAIL " + firstFail(cell)
				}
				w := int64(0)
				if cell.Warm != nil {
					w = cell.Warm.Total()
				}
				fmt.Fprintf(os.Stderr, "[%d/%d] %-18s run %d %-60s cold=%8d warm=%8d req=%3d ready=%5.0fms %s\n", done, total, m, r,
					trunc(s.URL, 60), cell.Cold.Total(), w, cell.Cold.Requests, cell.Cold.ReadyMs, st)
			}
		}
	}
	res.Finished = time.Now().UTC().Format(time.RFC3339)
	res.Complete = !filtered && len(modes) > 0
	save()
	fmt.Fprintf(os.Stderr, "results: %s\n", outPath)
	if filtered {
		fmt.Fprintln(os.Stderr, "filtered run: no report written")
		return
	}
	if *mdPath != "" {
		if err := writeReport(res, *mdPath, outPath); err != nil {
			fatalf("report: %v", err)
		}
		fmt.Fprintf(os.Stderr, "report: %s\n", *mdPath)
	}
	printConsole(res)
}

func retryable(c Cell) bool {
	f := firstFail(c)
	if strings.Contains(f, "main document HTTP") || strings.Contains(f, "unmeasured traffic") {
		return false // a server answer or a measurement-integrity failure is not flakiness
	}
	return true
}

func firstFail(c Cell) string {
	if !c.Cold.OK {
		return c.Cold.Fail
	}
	if c.Warm != nil && !c.Warm.OK {
		return "warm: " + c.Warm.Fail
	}
	return ""
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

func fatalf(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "datasaver: "+f+"\n", a...)
	os.Exit(1)
}

func collectEnv(co ChromiumOpts, upstream string) Env {
	e := Env{Date: time.Now().Format("2006-01-02"), GoVersion: runtime.Version(), Cores: runtime.NumCPU(), Upstream: upstream,
		SweefOpts: sweefOptsForReport()}
	if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(l, "model name") {
				_, v, _ := strings.Cut(l, ":")
				e.CPU = strings.TrimSpace(v)
				break
			}
		}
	}
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(l, "MemTotal:") {
				f := strings.Fields(l)
				if len(f) >= 2 {
					kbv, _ := strconv.Atoi(f[1])
					e.MemMB = kbv / 1024
				}
			}
		}
	}
	if b, err := os.ReadFile("/etc/os-release"); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(l, "PRETTY_NAME=") {
				e.OS = strings.Trim(strings.TrimPrefix(l, "PRETTY_NAME="), `"`)
			}
		}
	}
	e.Kernel = strings.TrimSpace(run("uname", "-r"))
	e.SweefCommit = strings.TrimSpace(run("git", "rev-parse", "--short", "HEAD"))
	e.SweefDirty = strings.TrimSpace(run("git", "status", "--porcelain", "--", "internal/engine", "internal/net")) != ""
	if buildSource != "" { // set with -ldflags when the binary was built from a snapshot instead of the working tree
		e.SweefCommit, e.SweefDirty = buildSource, false
	}
	if ver, ua, _, err := chromiumInfo(co); err == nil {
		e.Chromium, e.ChromiumUA = ver, ua
	} else {
		e.Chromium = "unavailable: " + err.Error()
	}
	e.Network = "Cloud sandbox VM. All egress goes through the agent's HTTP CONNECT proxy; TLS is re-terminated by an egress gateway with its own CA (certificate chains therefore differ from the sites' real ones). Bandwidth and latency are not controlled or recorded."
	return e
}

func run(name string, args ...string) string {
	out, _ := exec.Command(name, args...).Output()
	return string(out)
}
