//go:build linux

package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

func cmdRun(args []string) {
	fl := flag.NewFlagSet("run", flag.ExitOnError)
	runs := fl.Int("runs", 7, "iterations per browser (N >= 7 for a run that may print verdicts)")
	idle := fl.Int("idle", 20, "idle CPU window in seconds")
	settle := fl.Int("settle", 4, "settle time before reading memory, seconds")
	label := fl.String("label", "baseline", "label of this run (file name and report title)")
	out := fl.String("out", "bench/vschrome/results", "results directory")
	sweefBin := fl.String("sweef", "", "sweef binary (built by scripts/bench/vschrome.sh)")
	chromiumBin := fl.String("chromium", "", "Chromium binary (default: /opt/pw-browsers/chromium)")
	commit := fl.String("sweef-commit", "unknown", "git commit of the sweef source")
	dirty := fl.Bool("sweef-dirty", false, "the source tree had uncommitted changes")
	srcMode := fl.String("sweef-source", "head", "head = built from a clean `git archive HEAD`, worktree = built from the working tree")
	quick := fl.Bool("quick", false, "smoke run: few samples, no 30 s idle, no real sites (never publishable)")
	sitesFile := fl.String("sites", "bench/datasaver/sites.txt", "candidate real sites (file order)")
	nsites := fl.Int("nsites", 3, "real sites to measure")
	shots := fl.String("shots", "", "debug: directory for screenshots of key moments")
	only := fl.String("only", "", "debug: comma list of phases to run: startup,main,mem10 (default all)")
	skipDisk := fl.Bool("skip-disk", false, "skip the disk footprint step")
	trials := fl.Int("trials", 12, "samples per run for tab switch, new tab and the closed-dropdown first key")
	xz := fl.Bool("xz", false, "also measure xz -6 sizes (adds ~2 min)")
	keepLogs := fl.Bool("keep-logs", false, "keep Xvfb/browser logs under <out>/logs/<label>")
	tabs := fl.Int("tabs", 10, "tabs for the many-tab memory measurement")
	_ = fl.Parse(args)

	if *sweefBin == "" {
		fatalf("-sweef is required (scripts/bench/vschrome.sh builds it)")
	}
	if *chromiumBin == "" {
		*chromiumBin = "/opt/pw-browsers/chromium"
	}
	cfg := config{Runs: *runs, IdleSecs: *idle, SettleSec: *settle, Tabs: *tabs, Quick: *quick, Shots: *shots, KeyTrials: *trials, SwitchN: *trials, NewTabN: *trials}
	if *quick {
		cfg.KeyTrials, cfg.SwitchN, cfg.NewTabN = 4, 4, 4
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fatalf("%v", err)
	}
	work, err := os.MkdirTemp("", "vschrome-")
	if err != nil {
		fatalf("%v", err)
	}
	defer os.RemoveAll(work)
	cfg.OutDir = work
	cfg.LogDir = filepath.Join(work, "logs") // Xvfb and browser stderr; kept only with -keep-logs
	if *keepLogs {
		cfg.LogDir = filepath.Join(*out, "logs", *label)
	}
	os.MkdirAll(cfg.LogDir, 0o755)

	chrBin, _ := filepath.EvalSymlinks(*chromiumBin)
	chrVer := strings.TrimSpace(runOut(chrBin, "--version"))
	swVer := strings.TrimSpace(runOut(*sweefBin, "version"))
	h := &harness{cfg: cfg, specs: map[string]*browserSpec{
		"chromium": {Name: "chromium", Bin: chrBin, Version: chrVer, Lay: layouts["chromium"]},
		"sweef":    {Name: "sweef", Bin: *sweefBin, Version: swVer, Lay: layouts["sweef"]},
	}}
	fix, err := newFixtureServer()
	if err != nil {
		fatalf("%v", err)
	}
	defer fix.close()
	h.fix = fix

	res := &Results{Browsers: map[string]*BResults{"chromium": {}, "sweef": {}}}
	m := &res.Meta
	m.Label, m.Date, m.Commit, m.Dirty, m.SrcMode = *label, time.Now().UTC().Format("2006-01-02 15:04 UTC"), *commit, *dirty, *srcMode
	m.StartedAt = time.Now().UTC().Format(time.RFC3339)
	m.CPU, m.Cores, m.MemMiB, m.Kernel = cpuModel(), runtime.NumCPU(), memMiB(), strings.TrimSpace(runOut("uname", "-r"))
	m.Xvfb = strings.TrimSpace(runOut("dpkg-query", "-W", "-f", "${Version}", "xvfb"))
	m.XvfbArgs = "Xvfb :N -screen 0 1920x1080x24 -ac -nolisten tcp -noreset (no window manager, no compositor; a new server for every measured launch)"
	m.Go = runtime.Version()
	m.Load = loadAvg()
	m.Runs, m.Quick, m.IdleSecs, m.SettleSecs, m.Tabs = *runs, *quick, *idle, *settle, *tabs
	m.Window = fmt.Sprintf("%dx%d", winW, winH)
	m.Browsers = map[string]BInfo{
		"chromium": {Name: "Chromium (Playwright build, headed, full browser UI)", Version: chrVer, Binary: chrBin, Args: chromiumArgs(launchOpts{Profile: "<tmp profile>", Proxy: "<recorder>"}),
			Note: "Playwright's Chromium build, NOT branded Google Chrome: no Google services, no sync, no API keys, no proprietary codecs/DRM. The same Blink/V8/Skia core and the same browser UI code."},
		"sweef": {Name: "Sweef desktop GUI (`sweef gui`, native X11 window)", Version: swVer, Binary: *sweefBin, Args: sweefArgs(launchOpts{Profile: "<tmp profile>"}),
			Note: "built from " + *srcMode + " at " + *commit + ", -trimpath -ldflags='-s -w'; profile pre-seeded with onboarding.done=true"},
	}
	m.Env = map[string]string{"proxy": "browsers reach the network through a recording CONNECT proxy in front of the sandbox egress proxy; loopback is not proxied by either browser"}

	// The machine is shared with other jobs: run at a higher scheduling priority (children inherit it, so both browsers,
	// Xvfb and the observer get the same) so that a busy neighbour disturbs the measurements less. Not isolation.
	if err := syscall.Setpriority(syscall.PRIO_PROCESS, 0, -10); err != nil {
		res.Problems = append(res.Problems, "could not raise priority (nice -10): "+err.Error())
		m.Env["priority"] = "normal (nice 0): " + err.Error()
	} else {
		m.Env["priority"] = "nice -10 for the harness, Xvfb and both browsers"
	}

	// the machine must be quiet; record what it was
	quiet := quietMachine(10 * time.Second)
	m.Load = loadAvg()
	if !quiet {
		res.Problems = append(res.Problems, "machine was not idle (>= 0.2 cores used by other processes) when the run started")
	}
	logf("run %q: N=%d, load %s, quiet=%v", *label, *runs, m.Load, quiet)

	// calibration of the pixel detector (once per run, on a bare Xvfb)
	if xs, err := startXvfb(cfg.LogDir); err == nil {
		if cr, err := calibrate(xs.display, rect{108, 36, 1044, 224}, 200); err == nil {
			m.Calib = &CalibOut{pct(cr.Damage, 50), pct(cr.Damage, 99), pct(cr.Poll, 50), pct(cr.Poll, 99), len(cr.Damage)}
		} else {
			res.Problems = append(res.Problems, "calibration failed: "+err.Error())
		}
		xs.stop()
	}

	// detector cross-check: DAMAGE vs tight GetImage polling, same keys alternating, one session per browser
	if !*quick {
		m.Cross = map[string]*CrossOut{}
		for _, n := range []string{"chromium", "sweef"} {
			if c, err := h.crossCheck(h.specs[n]); err == nil {
				m.Cross[n] = c
			} else {
				res.Problems = append(res.Problems, "detector cross-check "+n+": "+err.Error())
			}
		}
	}

	if !*skipDisk {
		m.Disk = diskFootprint(chrBin, *sweefBin, *quick || !*xz)
	}

	// real sites
	if !*quick && *nsites > 0 {
		chosen, notes := h.pickSites(*sitesFile, *nsites)
		cfg.Sites, m.Sites, m.SitesNote = chosen, chosen, notes
		h.cfg.Sites = chosen
		if len(chosen) < *nsites {
			res.Problems = append(res.Problems, fmt.Sprintf("only %d of %d real sites loaded in both browsers", len(chosen), *nsites))
		}
	}

	phases := map[string]bool{"startup": true, "main": true, "mem10": true}
	if *only != "" {
		phases = map[string]bool{}
		for _, p := range strings.Split(*only, ",") {
			phases[p] = true
		}
	}
	partial := filepath.Join(*out, *label+".partial.json")
	var usedProf = map[string]string{}
	if phases["startup"] {
		for _, n := range []string{"chromium", "sweef"} {
			p, err := h.usedProfile(h.specs[n])
			if err != nil {
				res.Problems = append(res.Problems, "used profile for "+n+": "+err.Error())
			}
			usedProf[n] = p
		}
	}
	for i := 0; i < *runs; i++ {
		order := []string{"chromium", "sweef"}
		if i%2 == 1 {
			order = []string{"sweef", "chromium"}
		}
		for pos, name := range order {
			spec := h.specs[name]
			it := &Iter{Index: i, Position: pos, Load: loadAvg(), Startup: map[string]StartupSample{}, Lat: map[string][]float64{}, LatFail: map[string]int{}, Loads: map[string]LoadSample{}}
			if !quietMachine(10 * time.Second) {
				it.Noisy = true
			}
			logf("iteration %d/%d  %-8s load %s", i+1, *runs, name, loadAvg())
			psi0, wall0 := psiSome(), time.Now()
			if phases["startup"] {
				for _, sc := range []string{"cold_fresh_profile", "warm_fresh_profile", "warm_used_profile"} {
					smp := h.startup(spec, sc, usedProf[name])
					if !smp.OK {
						smp = h.startup(spec, sc, usedProf[name]) // one retry
					}
					it.Startup[sc] = smp
					if !smp.OK {
						it.Errors = append(it.Errors, "startup "+sc+": "+smp.Err)
					}
					logf("   startup %-18s ui %.0f ms  ntp %.0f ms %s", sc, smp.FirstUIMs, smp.NTPMs, smp.Err)
				}
			}
			if phases["main"] {
				err := h.runMain(spec, it, int64(1000+i))
				if err != nil {
					logf("   main session failed: %v (retrying once)", err)
					it.Lat, it.LatFail, it.Loads, it.Mem1, it.Idle = map[string][]float64{}, map[string]int{}, map[string]LoadSample{}, nil, nil
					err = h.runMain(spec, it, int64(5000+i))
				}
				if err != nil {
					it.Errors = append(it.Errors, "main session: "+err.Error())
				}
			}
			if phases["mem10"] {
				err := h.runMem10(spec, it)
				if err != nil {
					err = h.runMem10(spec, it)
				}
				if err != nil {
					it.Errors = append(it.Errors, "10 tabs: "+err.Error())
				}
			}
			if wall := time.Since(wall0); wall > 0 && psi0 >= 0 {
				it.CPUStallPct = float64(psiSome()-psi0) / float64(wall.Microseconds()) * 100
			}
			it.Complete = iterComplete(it, cfg, phases)
			res.Browsers[name].Iters = append(res.Browsers[name].Iters, it)
			logf("   done: complete=%v errors=%v mem1=%v", it.Complete, it.Errors, memStr(it.Mem1))
			writeJSON(partial, res)
		}
	}
	m.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	finalize(res, cfg)
	final := filepath.Join(*out, *label+".json")
	writeJSON(final, res)
	os.Remove(partial)
	logf("wrote %s (complete=%v)", final, res.Complete)
}

func memStr(m *MemSnap) string {
	if m == nil {
		return "-"
	}
	return fmt.Sprintf("%.1f MiB pss, %d procs", float64(m.PSSKiB)/1024, m.Procs)
}

// expected number of samples per latency series, per run
func (c config) expect() map[string]int {
	return map[string]int{"key_dropdown_open": 30, "key_dropdown_closed": c.KeyTrials, "tab_switch": c.SwitchN, "new_tab": c.NewTabN}
}

func iterComplete(it *Iter, c config, phases map[string]bool) bool {
	if len(it.Errors) > 0 {
		return false
	}
	if phases["startup"] {
		for _, sc := range []string{"cold_fresh_profile", "warm_fresh_profile", "warm_used_profile"} {
			if !it.Startup[sc].OK {
				return false
			}
		}
	}
	if phases["main"] {
		if it.Mem1 == nil || (c.IdleSecs > 0 && (it.Idle == nil || !it.Idle.OK)) {
			return false
		}
		for k, n := range c.expect() {
			if len(it.Lat[k]) < n*95/100 { // at most 5% of the samples of a series may be missing
				return false
			}
		}
		want := 3 + len(c.Sites)
		if len(it.Loads) != want {
			return false
		}
		for _, l := range it.Loads {
			if !l.OK {
				return false
			}
		}
	}
	if phases["mem10"] && it.Mem10 == nil {
		return false
	}
	return true
}

func finalize(res *Results, c config) {
	res.Complete = true
	for name, b := range res.Browsers {
		if len(b.Iters) != res.Meta.Runs {
			res.Complete = false
			res.Problems = append(res.Problems, fmt.Sprintf("%s: %d of %d iterations present", name, len(b.Iters), res.Meta.Runs))
		}
		for _, it := range b.Iters {
			if !it.Complete {
				res.Complete = false
				res.Problems = append(res.Problems, fmt.Sprintf("%s iteration %d incomplete: %s", name, it.Index+1, strings.Join(it.Errors, "; ")))
			}
		}
	}
	if res.Meta.Quick {
		res.Complete = false
		res.Problems = append(res.Problems, "quick (smoke) run: not publishable")
	}
	if res.Meta.Runs < 7 {
		res.Complete = false
		res.Problems = append(res.Problems, fmt.Sprintf("N=%d < 7", res.Meta.Runs))
	}
	if res.Meta.Calib == nil {
		res.Complete = false
		res.Problems = append(res.Problems, "no detector calibration")
	}
}

// ---- site selection ----------------------------------------------------------------------------------------------------------

// pickSites probes the candidates in file order (one load per browser, same method as the measurement) and keeps the
// first n that load in BOTH browsers. The rule is fixed and deterministic; nothing is chosen by looking at timings.
func (h *harness) pickSites(file string, n int) (chosen, notes []string) {
	f, err := os.Open(file)
	if err != nil {
		return nil, []string{"cannot read " + file}
	}
	defer f.Close()
	var cands []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		ln := strings.TrimSpace(sc.Text())
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		cands = append(cands, strings.TrimSpace(strings.SplitN(ln, "|", 2)[0]))
	}
	ok := map[string]map[string]string{"chromium": {}, "sweef": {}}
	for _, name := range []string{"chromium", "sweef"} {
		s, closeFn, err := h.probeSession(h.specs[name])
		if err != nil {
			notes = append(notes, name+" probe session failed: "+err.Error())
			return nil, notes
		}
		var pass int
		for _, u := range cands {
			if name == "sweef" {
				// only candidates that loaded in Chromium, and only until n have loaded in both
				if _, tried := ok["chromium"][u]; !tried || ok["chromium"][u] != "" {
					continue
				}
				if pass >= n {
					break
				}
			} else if pass >= n+2 {
				break // a couple of spares in case a site fails in Sweef
			}
			r := s.loadPage(h, "", "", false, u)
			if r.OK && r.WireIn > 3000 {
				ok[name][u] = ""
				pass++
			} else {
				ok[name][u] = fmt.Sprintf("failed: %s (first=%.0f ms, in=%d B)", r.Err, r.FirstMs, r.WireIn)
			}
			logf("probe %-8s %-70s %v", name, u, ok[name][u] == "")
		}
		closeFn()
	}
	for _, u := range cands {
		_, cOK := ok["chromium"][u]
		_, sOK := ok["sweef"][u]
		if !cOK && !sOK {
			continue
		}
		if ok["chromium"][u] == "" && ok["sweef"][u] == "" && cOK && sOK {
			if len(chosen) < n {
				chosen = append(chosen, u)
			}
			continue
		}
		if ok["chromium"][u] != "" || ok["sweef"][u] != "" {
			var why []string
			if v := ok["chromium"][u]; v != "" {
				why = append(why, "chromium "+v)
			}
			if v := ok["sweef"][u]; v != "" {
				why = append(why, "sweef "+v)
			}
			notes = append(notes, "excluded "+u+": "+strings.Join(why, "; "))
		}
	}
	return chosen, notes
}

func (h *harness) probeSession(spec *browserSpec) (*session, func(), error) {
	prof, err := freshProfile(spec, h.cfg.OutDir)
	if err != nil {
		return nil, nil, err
	}
	xs, err := startXvfb(h.cfg.LogDir)
	if err != nil {
		return nil, nil, err
	}
	rec, err := NewRecorder(os.Getenv("HTTPS_PROXY"))
	if err != nil {
		xs.stop()
		return nil, nil, err
	}
	s := &session{xs: xs, spec: spec, lay: layouts[spec.Name], rec: rec, fix: h.fix}
	if err := s.attach(); err != nil {
		xs.stop()
		return nil, nil, err
	}
	l, err := spec.launch(launchOpts{Display: xs.display, Profile: prof, Proxy: rec.Addr(), LogDir: h.cfg.LogDir, Tag: h.tag()})
	if err != nil {
		xs.stop()
		return nil, nil, err
	}
	s.l = l
	if _, err := s.findWindow(30 * time.Second); err != nil {
		l.stop()
		xs.stop()
		return nil, nil, err
	}
	time.Sleep(3 * time.Second)
	return s, func() { l.stop(); s.detach(); rec.Close(); xs.stop(); os.RemoveAll(prof) }, nil
}

// ---- environment ------------------------------------------------------------------------------------------------------------------

func cpuModel() string {
	b, _ := os.ReadFile("/proc/cpuinfo")
	for _, ln := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(ln, "model name") {
			if i := strings.IndexByte(ln, ':'); i > 0 {
				return strings.TrimSpace(ln[i+1:])
			}
		}
	}
	return "unknown"
}

func memMiB() int {
	b, _ := os.ReadFile("/proc/meminfo")
	for _, ln := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(ln, "MemTotal:") {
			var kb int
			fmt.Sscanf(strings.TrimPrefix(ln, "MemTotal:"), "%d", &kb)
			return kb / 1024
		}
	}
	return 0
}

func runOut(name string, args ...string) string {
	out, _ := exec.Command(name, args...).CombinedOutput()
	return string(out)
}

func writeJSON(path string, v any) {
	b, _ := json.MarshalIndent(v, "", " ")
	os.WriteFile(path, b, 0o644)
}

func logf(f string, a ...any) { fmt.Fprintf(os.Stderr, time.Now().Format("15:04:05 ")+f+"\n", a...) }
func fatalf(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "vschrome: "+f+"\n", a...)
	os.Exit(1)
}

// ---- disk -----------------------------------------------------------------------------------------------------------------------------

func diskFootprint(chromiumBin, sweefBin string, quick bool) map[string]Disk {
	out := map[string]Disk{}
	dir := filepath.Dir(chromiumBin)
	out["chromium"] = dirDisk(dir, quick, "the whole Playwright Chromium install directory (chrome, resources, locales, helpers); shared system libraries are not counted for either browser")
	out["sweef"] = fileDisk(sweefBin, quick, "the single sweef binary (fonts, chrome UI, engine are embedded); shared system libraries (libc only) are not counted")
	return out
}

func dirDisk(dir string, quick bool, note string) Disk {
	d := Disk{Path: dir, Note: note}
	exec.Command("sh", "-c", "true").Run()
	filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return nil
		}
		d.Files++
		d.ApparentB += fi.Size()
		if st, ok := fi.Sys().(interface{}); ok {
			_ = st
		}
		return nil
	})
	d.OnDiskB = onDisk(dir)
	d.GzipB = pipeSize(fmt.Sprintf("tar cf - -C %q . | gzip -6 | wc -c", dir))
	if !quick {
		d.XzB = pipeSize(fmt.Sprintf("tar cf - -C %q . | xz -6 -T0 | wc -c", dir))
	}
	return d
}

func fileDisk(path string, quick bool, note string) Disk {
	d := Disk{Path: path, Note: note, Files: 1}
	if fi, err := os.Stat(path); err == nil {
		d.ApparentB = fi.Size()
	}
	d.OnDiskB = onDisk(path)
	d.GzipB = pipeSize(fmt.Sprintf("gzip -6 -c %q | wc -c", path))
	if !quick {
		d.XzB = pipeSize(fmt.Sprintf("xz -6 -T0 -c %q | wc -c", path))
	}
	return d
}

func onDisk(path string) int64 {
	out := runOut("du", "-sB1", "--", path)
	var n int64
	fmt.Sscanf(strings.Fields(out + " ")[0], "%d", &n)
	return n
}

func pipeSize(sh string) int64 {
	out, err := exec.Command("sh", "-c", sh).Output()
	if err != nil {
		return 0
	}
	var n int64
	fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &n)
	return n
}

// psiSome returns the cumulative microseconds during which at least one runnable task was waiting for a CPU
// (/proc/pressure/cpu "some total"): a machine-wide measure of CPU contention. -1 if unavailable.
func psiSome() int64 {
	b, err := os.ReadFile("/proc/pressure/cpu")
	if err != nil {
		return -1
	}
	for _, ln := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(ln, "some ") {
			for _, f := range strings.Fields(ln) {
				if strings.HasPrefix(f, "total=") {
					var v int64
					fmt.Sscanf(strings.TrimPrefix(f, "total="), "%d", &v)
					return v
				}
			}
		}
	}
	return -1
}
