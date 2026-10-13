//go:build linux

package main

import (
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

type config struct {
	Runs      int
	IdleSecs  int
	SettleSec int
	Tabs      int
	Quick     bool
	Shots     string
	OutDir    string
	LogDir    string
	Sites     []string // chosen real sites
	KeyTrials int      // closed-dropdown trials per run
	SwitchN   int
	NewTabN   int
}

type harness struct {
	cfg   config
	fix   *fixtureServer
	specs map[string]*browserSpec
	n     atomic.Int64
}

// layouts: window-relative geometry (the window sits wherever the browser put it; its origin is read from the X
// server). Measured from the screenshots described in docs/vs-chrome.md.
//
//	KeyROI  : omnibox plus the area its suggestion dropdown opens into. Chromium's dropdown is a separate popup X window,
//	          so every pixel read goes through the ROOT window, which includes popups.
//	Chrome  : the browser UI above the page.
//	Content : the page area.
var layouts = map[string]layout{
	"chromium": {KeyROI: rect{108, 36, 1044, 224}, TextStrip: rect{110, 44, 1030, 40}, Chrome: rect{0, 0, 1280, 88}, Content: rect{0, 90, 1280, 710}},
	"sweef":    {KeyROI: rect{246, 36, 790, 224}, TextStrip: rect{250, 44, 780, 40}, Chrome: rect{0, 0, 1280, 88}, Content: rect{0, 90, 1280, 710}},
}

func (h *harness) tag() string { return fmt.Sprintf("vsc%d-%d", os.Getpid(), h.n.Add(1)) }

// ---- profiles --------------------------------------------------------------------------------------------------------------

func freshProfile(spec *browserSpec, dir string) (string, error) {
	p, err := os.MkdirTemp(dir, spec.Name+"-prof-")
	if err != nil {
		return "", err
	}
	if spec.Name == "chromium" {
		// Pre-seeded profile preferences: no network search suggestions. Chromium otherwise fetches them for every key
		// and repaints the dropdown when the answer arrives, at a time unrelated to the key that caused it; Sweef's
		// omnibox makes no such request. Nothing else is changed.
		if err = os.MkdirAll(filepath.Join(p, "Default"), 0o755); err != nil {
			return p, err
		}
		err = os.WriteFile(filepath.Join(p, "Default", "Preferences"), []byte(`{"search":{"suggest_enabled":false},"alternate_error_pages":{"enabled":false}}`), 0o644)
	}
	if spec.Name == "sweef" {
		// What a user has after the first-run onboarding: otherwise the first tab would be sweef://onboarding.
		err = os.WriteFile(filepath.Join(p, "settings.json"), []byte(`{"v":3,"values":{"onboarding.done":true}}`), 0o644)
	}
	return p, err
}

func copyDir(src, dst string) error {
	return exec.Command("cp", "-a", src+"/.", dst).Run()
}

// terminate asks the browser to exit (SIGTERM, which both handle as a normal quit) and falls back to SIGKILL.
func (l *launched) terminate() {
	if l.cmd.Process != nil {
		syscall.Kill(l.cmd.Process.Pid, syscall.SIGTERM)
	}
	for i := 0; i < 100; i++ {
		if len(tagged(l.tag)) == 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	l.stop()
}

// usedProfile launches the browser once on a profile, lets it write its state, and quits it normally. Each "warm
// profile" launch then starts from a copy of the result.
func (h *harness) usedProfile(spec *browserSpec) (string, error) {
	p, err := freshProfile(spec, h.cfg.OutDir)
	if err != nil {
		return "", err
	}
	xs, err := startXvfb(h.cfg.LogDir)
	if err != nil {
		return "", err
	}
	defer xs.stop()
	l, err := spec.launch(launchOpts{Display: xs.display, Profile: p, LogDir: h.cfg.LogDir, Tag: h.tag()})
	if err != nil {
		return "", err
	}
	time.Sleep(8 * time.Second)
	l.terminate()
	return p, nil
}

// ---- machine noise ------------------------------------------------------------------------------------------------------------

func cpuBusyJiffies() int64 {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0
	}
	f := strings.Fields(strings.SplitN(string(b), "\n", 2)[0])
	var busy, idle int64
	for i, s := range f[1:] {
		v, _ := strconv.ParseInt(s, 10, 64)
		switch i {
		case 3, 4: // idle, iowait
			idle += v
		case 8, 9: // guest, guest_nice are already inside user/nice
		default:
			busy += v
		}
	}
	_ = idle
	return busy
}

func selfJiffies() int64 { j, _ := cpuJiffies(os.Getpid()); return j }

// quietMachine waits (up to max) until the machine outside this benchmark uses < 0.2 cores for 3 s. Returns whether
// it got there.
func quietMachine(max time.Duration) bool {
	end := time.Now().Add(max)
	for {
		b0, s0 := cpuBusyJiffies(), selfJiffies()
		time.Sleep(3 * time.Second)
		b1, s1 := cpuBusyJiffies(), selfJiffies()
		cores := float64((b1-b0)-(s1-s0)) / 100 / 3
		if cores < 0.2 {
			return true
		}
		if time.Now().After(end) {
			return false
		}
	}
}

// ---- startup ------------------------------------------------------------------------------------------------------------------

func dropCaches() error {
	syscall.Sync()
	return os.WriteFile("/proc/sys/vm/drop_caches", []byte("3"), 0o200)
}

// legibleUI: the browser's own UI has been painted: in the top band most pixels are light and there are dark text/icon
// pixels. A black unpainted window, a white blank window and a half-painted background all fail this.
func legibleUI(p *pixbuf) bool {
	light, dark := 0, 0
	n := len(p.pix) / 4
	for i := 0; i < n; i++ {
		lum := (int(p.pix[4*i])*114 + int(p.pix[4*i+1])*587 + int(p.pix[4*i+2])*299) / 1000
		if lum > 200 {
			light++
		} else if lum < 110 {
			dark++
		}
	}
	return light*100 >= n*60 && dark >= 150
}

func (h *harness) startup(spec *browserSpec, scenario, profileTemplate string) StartupSample {
	smp := StartupSample{}
	prof, err := freshProfile(spec, h.cfg.OutDir)
	if err != nil {
		smp.Err = err.Error()
		return smp
	}
	defer os.RemoveAll(prof)
	if scenario == "warm_used_profile" {
		if err := copyDir(profileTemplate, prof); err != nil {
			smp.Err = err.Error()
			return smp
		}
	}
	xs, err := startXvfb(h.cfg.LogDir)
	if err != nil {
		smp.Err = err.Error()
		return smp
	}
	defer xs.stop()
	s := &session{xs: xs, spec: spec, lay: layouts[spec.Name]}
	if err := s.attach(); err != nil {
		smp.Err = err.Error()
		return smp
	}
	defer s.detach()
	s.obs.drain()
	if scenario == "cold_fresh_profile" {
		if err := dropCaches(); err != nil {
			smp.Err = "drop_caches: " + err.Error()
			return smp
		}
		time.Sleep(300 * time.Millisecond)
	} else {
		time.Sleep(1500 * time.Millisecond) // let the machine settle after the previous launch's teardown
	}
	l, err := spec.launch(launchOpts{Display: xs.display, Profile: prof, LogDir: h.cfg.LogDir, Tag: h.tag()})
	if err != nil {
		smp.Err = err.Error()
		return smp
	}
	defer l.stop()
	t0 := l.started
	deadline := t0.Add(45 * time.Second)
	var haveWin bool
	var tUI, tLast time.Time
	var prevC *pixbuf
	for {
		var dl time.Time
		switch {
		case tUI.IsZero():
			dl = deadline
		default:
			dl = tLast.Add(700 * time.Millisecond)
			if dl.After(deadline) {
				dl = deadline
			}
		}
		ev, ok, err := s.obs.nextEvent(dl)
		if err != nil {
			smp.Err = err.Error()
			return smp
		}
		if !ok { // timeout: either the overall deadline or 700 ms of quiet
			if !tUI.IsZero() && time.Now().Before(deadline) && prevC != nil && prevC.nonModal() >= contentNonBlank(prevC) {
				smp.NTPMs = ms(tLast.Sub(t0))
				smp.OK = true
				return smp
			}
			if time.Now().After(deadline) {
				smp.Err = "timeout waiting for the UI / new-tab page"
				return smp
			}
			tLast = time.Now() // quiet but content still blank: keep waiting
			continue
		}
		r, isD := s.dm.notify(ev.data)
		if !isD {
			continue
		}
		if !haveWin {
			for _, w := range s.obs.topWindows() {
				if w.w >= winW-40 && w.h >= winH-40 && !w.override {
					s.win, haveWin = w, true
					smp.WindowMs = ms(ev.at.Sub(t0))
					break
				}
			}
			if !haveWin {
				continue
			}
		}
		if tUI.IsZero() {
			if !r.intersects(s.abs(s.lay.Chrome)) {
				continue
			}
			band, err := s.obs.grab(s.abs(s.lay.Chrome))
			if err != nil {
				smp.Err = err.Error()
				return smp
			}
			if legibleUI(band) {
				tUI, tLast = ev.at, ev.at
				smp.FirstUIMs = ms(ev.at.Sub(t0))
				prevC, _ = s.obs.grab(s.abs(s.lay.Content))
			}
			continue
		}
		if !r.intersects(s.abs(s.lay.Content)) {
			continue
		}
		cur, err := s.obs.grab(s.abs(s.lay.Content))
		if err != nil {
			smp.Err = err.Error()
			return smp
		}
		if prevC == nil || !cur.equal(prevC) {
			tLast = ev.at
		}
		prevC = cur
	}
}

// contentNonBlank is the number of non-background pixels that a rendered new-tab page must have (0.15% of the area).
func contentNonBlank(p *pixbuf) int { return len(p.pix) / 4 * 15 / 10000 }

func ms(d time.Duration) float64 { return float64(d.Nanoseconds()) / 1e6 }

// ---- the main session -------------------------------------------------------------------------------------------------------------

func (h *harness) pageURL(path, run string) string {
	return fmt.Sprintf("http://%s%s?r=%s", h.fix.addr(), path, run)
}

func (h *harness) runMain(spec *browserSpec, it *Iter, seed int64) (err error) {
	prof, err := freshProfile(spec, h.cfg.OutDir)
	if err != nil {
		return err
	}
	defer os.RemoveAll(prof)
	xs, err := startXvfb(h.cfg.LogDir)
	if err != nil {
		return err
	}
	defer xs.stop()
	rec, err := NewRecorder(os.Getenv("HTTPS_PROXY"))
	if err != nil {
		return err
	}
	defer rec.Close()
	s := &session{xs: xs, spec: spec, lay: layouts[spec.Name], rec: rec, fix: h.fix, shots: h.cfg.Shots}
	if s.shots != "" {
		s.shots = filepath.Join(s.shots, spec.Name)
	}
	if err := s.attach(); err != nil {
		return err
	}
	defer s.detach()
	tag := h.tag()
	run := fmt.Sprintf("%s-%d", tag, seed)
	first := h.pageURL("/article", run+"-a")
	l, err := spec.launch(launchOpts{Display: xs.display, Profile: prof, Proxy: rec.Addr(), URL: first, LogDir: h.cfg.LogDir, Tag: tag})
	if err != nil {
		return err
	}
	s.l = l
	defer l.stop()
	if _, err := s.findWindow(30 * time.Second); err != nil {
		return err
	}
	if _, ok := h.fix.waitBeacon(run+"-a", "load", 60*time.Second); !ok {
		return fmt.Errorf("article did not fire its load event within 60 s")
	}
	time.Sleep(time.Duration(h.cfg.SettleSec) * time.Second)
	s.shot("01-article")

	// (2) memory with 1 tab
	m := treeMem(tag)
	it.Mem1 = &MemSnap{m, 1}

	// (6) idle CPU, 30 s on the static page
	if h.cfg.IdleSecs > 0 {
		it.Idle = h.idleCPU(l, xs, time.Duration(h.cfg.IdleSecs)*time.Second)
	}

	// second tab (heavy page) so that there is something to switch to
	if err := s.openTab(h.pageURL("/heavy", run+"-b")); err != nil {
		return err
	}
	if _, ok := h.fix.waitBeacon(run+"-b", "load", 60*time.Second); !ok {
		return fmt.Errorf("heavy page did not fire its load event within 60 s")
	}
	time.Sleep(2 * time.Second)
	s.shot("02-two-tabs")

	rng := rand.New(rand.NewSource(seed))
	whole := s.abs(rect{0, 0, winW, winH})
	content := s.abs(s.lay.Content)
	keyROI := s.abs(s.lay.KeyROI)

	// (4) tab switch (ctrl+Tab between the two loaded tabs)
	for i := 0; i < 3+h.cfg.SwitchN; i++ {
		r, err := s.tabSwitchLatency(whole, &content)
		if err != nil {
			return err
		}
		s.waitQuiet(whole, 300*time.Millisecond, 3*time.Second)
		if i < 3 {
			continue
		}
		it.addLat("tab_switch", r.Any)
		it.addLat("tab_switch_content", r.Content)
		time.Sleep(spacing(rng))
	}
	s.shot("03-after-switch")

	// (4) new tab (ctrl+T, then ctrl+W to get back; the close is not measured)
	for i := 0; i < 2+h.cfg.NewTabN; i++ {
		r, err := s.chordLatency('t', whole, &content)
		if err != nil {
			return err
		}
		s.waitQuiet(whole, 300*time.Millisecond, 3*time.Second)
		if i == 0 {
			s.shot("04-newtab")
		}
		if i >= 2 {
			it.addLat("new_tab", r.Any)
			it.addLat("new_tab_content", r.Content)
		}
		if err := s.xt.chordTap(keyCtrl, 'w'); err != nil {
			return err
		}
		s.waitQuiet(whole, 300*time.Millisecond, 3*time.Second)
		time.Sleep(spacing(rng))
	}

	// (4) typing in the omnibox, dropdown open: 30 characters after one unmeasured character that opens it
	if err := s.focusOmnibox(); err != nil {
		return err
	}
	s.waitQuiet(keyROI, 300*time.Millisecond, 2*time.Second)
	if _, err := s.keyLatency('a', keyROI); err != nil {
		return err
	}
	for i, ch := range keyText {
		s.waitQuiet(keyROI, 60*time.Millisecond, time.Second)
		v, err := s.keyLatency(ch, keyROI)
		if err != nil {
			return err
		}
		it.addLat("key_dropdown_open", v)
		if i == 14 {
			s.shot("05-typing")
		}
		time.Sleep(spacing(rng))
	}
	s.xt.tap(keyEsc)
	time.Sleep(150 * time.Millisecond)
	s.xt.tap(keyEsc)
	s.waitQuiet(whole, 300*time.Millisecond, 2*time.Second)

	// (4) first keystroke with the dropdown closed (it opens on that key)
	for i := 0; i < 2+h.cfg.KeyTrials; i++ {
		if err := s.focusOmnibox(); err != nil {
			return err
		}
		s.waitQuiet(keyROI, 300*time.Millisecond, 1500*time.Millisecond)
		s.alignCaret(keyROI, 650*time.Millisecond)
		v, err := s.keyLatency('x', keyROI)
		if err != nil {
			return err
		}
		if i == 0 {
			time.Sleep(200 * time.Millisecond)
			s.shot("06-dropdown-open-first-key")
		}
		if i >= 2 {
			it.addLat("key_dropdown_closed", v)
		}
		time.Sleep(120 * time.Millisecond)
		s.xt.tap(keyEsc)
		time.Sleep(120 * time.Millisecond)
		s.xt.tap(keyEsc)
		s.waitQuiet(keyROI, 200*time.Millisecond, time.Second)
		if i == 0 {
			s.shot("07-after-escape")
		}
		time.Sleep(spacing(rng))
	}

	// (5) page loads: local fixtures, then the real sites
	it.Loads = map[string]LoadSample{}
	for _, p := range []string{"article", "heavy", "spa"} {
		it.Loads[p] = s.loadPage(h, "/"+p, run+"-L"+p, true, "")
	}
	for i, site := range h.cfg.Sites {
		it.Loads[fmt.Sprintf("real%d", i+1)] = s.loadPage(h, "", "", false, site)
	}
	return nil
}

func (it *Iter) addLat(name string, v float64) {
	if it.Lat == nil {
		it.Lat = map[string][]float64{}
		it.LatFail = map[string]int{}
	}
	if v < 0 {
		it.LatFail[name]++
		return
	}
	it.Lat[name] = append(it.Lat[name], v)
}

func (s *session) openTab(url string) error {
	if err := s.xt.chordTap(keyCtrl, 't'); err != nil {
		return err
	}
	time.Sleep(500 * time.Millisecond)
	if err := s.typeURL(url); err != nil {
		return err
	}
	return s.xt.tap('\n')
}

// idleCPU measures user+sys CPU of the browser tree (and of Xvfb, and of everything else on the machine) over d.
func (h *harness) idleCPU(l *launched, xs *xserver, d time.Duration) *IdleCPU {
	out := &IdleCPU{Seconds: d.Seconds()}
	b0, s0 := cpuBusyJiffies(), selfJiffies()
	xp := xs.cmd.Process.Pid
	x0, _ := cpuJiffies(xp)
	tree, maxp := treeCPU(l.tag, d)
	b1, s1 := cpuBusyJiffies(), selfJiffies()
	x1, _ := cpuJiffies(xp)
	out.TreeCPUs = float64(tree) / 100
	out.XvfbCPUs = float64(x1-x0) / 100
	out.MaxProcs = maxp
	other := float64((b1-b0)-(s1-s0)-tree-(x1-x0)) / 100
	if other < 0 {
		other = 0
	}
	out.OtherCPUs = other
	out.OK = true
	return out
}

// loadPage navigates the current tab from the white /blank page to the URL typed into the omnibox and measures, from the
// Enter key-down: the first frame with non-blank page content, the page's own load event (local pages: beacon seen by
// the server), and the time of the last pixel change in the top 400 px of the page area (visually settled, with a
// 1 s quiet criterion). Bytes on the wire: local pages by the server-side socket counter, real pages by the recording
// proxy.
func (s *session) loadPage(h *harness, path, run string, local bool, site string) (res LoadSample) {
	res = LoadSample{FirstMs: -1, LoadMs: -1, SettleMs: -1}
	url := site
	if local {
		url = h.pageURL(path, run)
	}
	fail := func(f string, a ...any) LoadSample { res.Err = fmt.Sprintf(f, a...); return res }
	// Below the area the omnibox dropdown can cover (it closes on Enter and would otherwise look like page content).
	region := s.abs(rect{0, 340, winW, 460})
	// 1) blank baseline in the same tab (a white page from the fixture server)
	if err := s.typeURL(h.pageURL("/blank", "b")); err != nil {
		return fail("%v", err)
	}
	if err := s.xt.tap('\n'); err != nil {
		return fail("%v", err)
	}
	s.waitQuiet(region, 500*time.Millisecond, 6*time.Second)
	// 2) type the URL, wait for the dropdown to settle
	if err := s.typeURL(url); err != nil {
		return fail("%v", err)
	}
	s.waitQuiet(s.abs(s.lay.KeyROI), 200*time.Millisecond, 2*time.Second)
	s.shot("load-before-" + strconv.Itoa(len(url)%97))
	var fSnap wireSnap
	var hSnap map[string]int
	var rSnap Totals
	if local {
		fSnap = h.fix.snap()
		hSnap = h.fix.hitsSnap()
	} else {
		rSnap = s.rec.Snapshot()
	}
	s.obs.drain()
	prev, err := s.obs.grab(region)
	if err != nil {
		return fail("%v", err)
	}
	t0, rel, err := s.enter()
	if err != nil {
		return fail("%v", err)
	}
	time.AfterFunc(keyHold, rel)
	if s.shots != "" {
		logf("loadPage: Enter at unix ms %d", t0.UnixMilli())
	}
	if os.Getenv("VSC_POLLDEBUG") != "" {
		go func() {
			c, err := dialX(s.xs.display)
			if err != nil {
				return
			}
			defer c.close()
			var last *pixbuf
			for time.Since(t0) < 12*time.Second {
				cur, err := c.grab(region)
				if err != nil {
					return
				}
				if last != nil && !cur.equal(last) {
					logf("  POLL saw change at +%.0f ms (nonModal %d)", ms(time.Since(t0)), cur.nonModal())
				}
				last = cur
				time.Sleep(20 * time.Millisecond)
			}
		}()
	}
	hardEnd := t0.Add(40 * time.Second)
	netTotal := func() int64 {
		if local {
			return h.fix.in.Load() + h.fix.out.Load()
		}
		t := s.rec.Snapshot()
		return t.In + t.Out
	}
	var firstAt, lastChange time.Time
	lastNet := t0
	prevBytes, lastPoll := netTotal(), time.Now()
	for {
		now := time.Now()
		if !firstAt.IsZero() {
			// done when neither the pixels nor the network have changed for 1 s
			q := lastChange
			if lastNet.After(q) {
				q = lastNet
			}
			if now.Sub(q) >= time.Second {
				break
			}
		}
		if !now.Before(hardEnd) {
			if firstAt.IsZero() {
				return fail("no page content within 40 s")
			}
			res.SettledEarly = true
			break
		}
		ev, ok, err := s.obs.nextEvent(now.Add(50 * time.Millisecond))
		if err != nil {
			return fail("%v", err)
		}
		if now.Sub(lastPoll) >= 100*time.Millisecond {
			lastPoll = now
			if b := netTotal(); b != prevBytes {
				prevBytes, lastNet = b, time.Now()
			}
		}
		if !ok {
			continue
		}
		r, isD := s.dm.notify(ev.data)
		if s.shots != "" {
			logf("  ev type %d +%.0f ms rect %v isDamage=%v", ev.data[0], ms(ev.at.Sub(t0)), r, isD)
		}
		if !isD || !r.intersects(region) {
			continue
		}
		cur, err := s.obs.grab(region)
		if err != nil {
			return fail("%v", err)
		}
		if s.shots != "" {
			logf("  damage +%.0f ms %v changed=%v nonModal=%d", ms(ev.at.Sub(t0)), r, !cur.equal(prev), cur.nonModal())
		}
		if !cur.equal(prev) {
			if firstAt.IsZero() {
				if cur.nonModal() >= 600 {
					firstAt, lastChange = ev.at, ev.at
				}
			} else {
				lastChange = ev.at
			}
		}
		prev = cur
	}
	res.LastByteMs = ms(lastNet.Sub(t0))
	res.FirstMs = ms(firstAt.Sub(t0))
	res.SettleMs = ms(lastChange.Sub(t0))
	time.Sleep(500 * time.Millisecond) // trailing requests
	s.shot("load-after-" + strconv.Itoa(len(url)%97))
	if local {
		d := h.fix.snap().sub(fSnap)
		res.WireIn, res.WireOut, res.Conns, res.Reqs = d.out, d.in, d.conns, d.reqs // "in" = bytes the browser received
		res.ReqPaths = diffHits(h.fix.hitsSnap(), hSnap)
		if at, ok := h.fix.waitBeacon(run, "load", 10*time.Second); ok {
			res.LoadMs = ms(at.Sub(t0))
		} else {
			return fail("load event beacon never arrived")
		}
		if path == "/spa" {
			if at, ok := h.fix.waitBeacon(run, "rendered", 5*time.Second); ok {
				res.RenderedMs = ms(at.Sub(t0))
			}
		}
	} else {
		d := s.rec.Snapshot().Diff(rSnap)
		res.WireIn, res.WireOut, res.Conns = d.In, d.Out, int64(d.Conns)
		if d.ConnectFailed > 0 {
			return fail("%d CONNECT(s) failed", d.ConnectFailed)
		}
	}
	res.OK = true
	return res
}

// ---- 10-tab memory ----------------------------------------------------------------------------------------------------------------

func (h *harness) runMem10(spec *browserSpec, it *Iter) error {
	prof, err := freshProfile(spec, h.cfg.OutDir)
	if err != nil {
		return err
	}
	defer os.RemoveAll(prof)
	xs, err := startXvfb(h.cfg.LogDir)
	if err != nil {
		return err
	}
	defer xs.stop()
	rec, err := NewRecorder(os.Getenv("HTTPS_PROXY"))
	if err != nil {
		return err
	}
	defer rec.Close()
	tag := h.tag()
	run := tag
	paths := []string{"/article", "/heavy", "/spa"}
	var urls []string
	for i := 0; i < h.cfg.Tabs; i++ {
		urls = append(urls, h.pageURL(paths[i%3], fmt.Sprintf("%s-%d", run, i)))
	}
	// the URLs are given on the command line (both browsers open them as tabs of the first window)
	o := launchOpts{Display: xs.display, Profile: prof, Proxy: rec.Addr(), URL: urls[0], LogDir: h.cfg.LogDir, Tag: tag}
	o.MoreURLs = urls[1:]
	l, err := spec.launch(o)
	if err != nil {
		return err
	}
	defer l.stop()
	// Same state as the 1-tab measurement: the window is found and given the keyboard focus (there is no window
	// manager to do it) and an observer is connected. A Chromium window that was never focused allocates less.
	s := &session{xs: xs, spec: spec, lay: layouts[spec.Name]}
	if err := s.attach(); err != nil {
		return err
	}
	defer s.detach()
	if _, err := s.findWindow(30 * time.Second); err != nil {
		return err
	}
	for i := range urls {
		if _, ok := h.fix.waitBeacon(fmt.Sprintf("%s-%d", run, i), "load", 90*time.Second); !ok {
			return fmt.Errorf("tab %d did not fire its load event", i)
		}
	}
	time.Sleep(time.Duration(h.cfg.SettleSec) * time.Second)
	it.Mem10 = &MemSnap{treeMem(tag), h.cfg.Tabs}
	return nil
}
