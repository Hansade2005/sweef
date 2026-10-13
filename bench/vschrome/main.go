//go:build linux

package main

import (
	"encoding/json"
	"fmt"
	goimage "image"
	"image/color"
	"image/png"
	"os"
	"strings"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: vschrome shot <browser> <bin> out.png [url]")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "run":
		cmdRun(os.Args[2:])
	case "memcurve":
		cmdMemCurve(os.Args[2:])
	case "load":
		cmdLoad(os.Args[2:])
	case "fixture":
		cmdFixture(os.Args[2:])
	case "report":
		cmdReport(os.Args[2:])
	case "calib":
		cmdCalib()
	case "shot":
		cmdShot(os.Args[2:])
	}
}

func savePNG(im *pixbuf, path string) error {
	out := goimage.NewRGBA(goimage.Rect(0, 0, im.r.w, im.r.h))
	for i := 0; i < im.r.w*im.r.h; i++ {
		out.Set(i%im.r.w, i/im.r.w, color.RGBA{im.pix[4*i+2], im.pix[4*i+1], im.pix[4*i], 255})
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, out)
}

func cmdShot(args []string) {
	name, bin, out := args[0], args[1], args[2]
	url := ""
	if len(args) > 3 {
		url = args[3]
	}
	os.MkdirAll("/tmp/claude-0/shot", 0o755)
	xs, err := startXvfb("/tmp/claude-0/shot")
	if err != nil {
		panic(err)
	}
	defer xs.stop()
	spec := &browserSpec{Name: name, Bin: bin}
	os.MkdirAll("/tmp/claude-0/shot/prof-"+name, 0o755)
	if name == "sweef" {
		os.WriteFile("/tmp/claude-0/shot/prof-sweef/settings.json", []byte(`{"v":3,"values":{"onboarding.done":true}}`), 0o644)
	}
	l, err := spec.launch(launchOpts{Display: xs.display, Profile: "/tmp/claude-0/shot/prof-" + name, URL: url, LogDir: "/tmp/claude-0/shot", Tag: "shot" + name})
	if err != nil {
		panic(err)
	}
	defer l.stop()
	time.Sleep(6 * time.Second)
	c, err := dialX(xs.display)
	if err != nil {
		panic(err)
	}
	var main xwin
	for _, w := range c.topWindows() {
		fmt.Printf("window %#x %dx%d+%d+%d override=%v\n", w.id, w.w, w.h, w.x, w.y, w.override)
		if w.w >= 1000 {
			main = w
		}
	}
	c.setInputFocus(main.id)
	x, _ := newXtest(c)
	snap := func(suffix string) {
		im, err := c.grab(rect{main.x, main.y, main.w, main.h})
		if err != nil {
			panic(err)
		}
		savePNG(im, out+suffix+".png")
	}
	snap("")
	x.chordTap(keyCtrl, 'l')
	time.Sleep(300 * time.Millisecond)
	x.typeString("hello wor", 50*time.Millisecond)
	time.Sleep(800 * time.Millisecond)
	snap("-typed")
	x.chordTap(keyCtrl, 't')
	time.Sleep(800 * time.Millisecond)
	snap("-newtab")
}

// cmdFixture serves the fixture pages on 127.0.0.1:port until killed (debugging aid: open them in any browser).
func cmdFixture(args []string) {
	fix, err := newFixtureServer()
	if err != nil {
		panic(err)
	}
	fmt.Println("fixture on", fix.addr())
	for {
		time.Sleep(time.Second)
		fix.mu.Lock()
		for _, b := range fix.beacons {
			fmt.Println("beacon", b.Page, b.Event, b.Run)
		}
		fix.beacons = nil
		fix.mu.Unlock()
	}
}

// cmdLoad is a debugging aid: vschrome load <chromium|sweef> <bin> <url> [shots-dir] loads one URL the way the
// benchmark does (typed into the omnibox, from the white /blank page) and prints what was measured.
func cmdLoad(args []string) {
	if len(args) < 3 {
		fmt.Println("usage: vschrome load <chromium|sweef> <bin> <url> [shots-dir]")
		return
	}
	work, _ := os.MkdirTemp("", "vsc-load-")
	defer os.RemoveAll(work)
	h := &harness{cfg: config{OutDir: work, LogDir: work}, specs: map[string]*browserSpec{}}
	h.fix, _ = newFixtureServer()
	spec := &browserSpec{Name: args[0], Bin: args[1], Lay: layouts[args[0]]}
	s, closeFn, err := h.probeSession(spec)
	if err != nil {
		panic(err)
	}
	defer closeFn()
	if len(args) > 3 {
		s.shots = args[3]
	}
	if len(args) > 4 { // time-lapse: a frame every 700 ms while the page loads
		stop := make(chan struct{})
		defer close(stop)
		go func() {
			c, err := dialX(s.xs.display)
			if err != nil {
				return
			}
			defer c.close()
			t0 := time.Now()
			for {
				select {
				case <-stop:
					return
				case <-time.After(400 * time.Millisecond):
				}
				if im, err := c.grab(rect{s.win.x, s.win.y, s.win.w, s.win.h}); err == nil {
					savePNG(im, fmt.Sprintf("%s/lapse-%d.png", args[3], time.Now().UnixMilli()))
					_ = t0
				}
			}
		}()
	}
	r := s.loadPage(h, "", "", false, args[2])
	b, _ := json.MarshalIndent(r, "", " ")
	fmt.Println(string(b))
	tot := s.rec.Snapshot()
	fmt.Printf("recorder: in %d out %d conns %d failed %v plain %v\n", tot.In, tot.Out, tot.Conns, tot.FailedConnects, tot.PlainHosts)
	for _, c := range tot.Records {
		fmt.Printf("  conn %-28s start %7.0f ms dur %7.0f ms in %8d out %6d open=%v\n", c.Host, c.StartMs, c.DurMs, c.In, c.Out, c.Open)
	}
}

// cmdMemCurve is a debugging aid: PSS of the whole tree every 2 s after the article has loaded (to choose the settle
// time and to see whether a browser is still growing or shrinking).
func cmdMemCurve(args []string) {
	work, _ := os.MkdirTemp("", "vsc-mem-")
	defer os.RemoveAll(work)
	h := &harness{cfg: config{OutDir: work, LogDir: work}, specs: map[string]*browserSpec{}}
	h.fix, _ = newFixtureServer()
	spec := &browserSpec{Name: args[0], Bin: args[1], Lay: layouts[args[0]]}
	xs, err := startXvfb(work)
	if err != nil {
		panic(err)
	}
	defer xs.stop()
	prof, _ := freshProfile(spec, work)
	tag := h.tag()
	o := launchOpts{Display: xs.display, Profile: prof, URL: h.pageURL("/article", tag), LogDir: work, Tag: tag}
	if os.Getenv("VSC_PROXY") != "" {
		rec, _ := NewRecorder(os.Getenv("HTTPS_PROXY"))
		defer rec.Close()
		o.Proxy = rec.Addr()
	}
	l, err := spec.launch(o)
	if err != nil {
		panic(err)
	}
	defer l.stop()
	if os.Getenv("VSC_FOCUS") != "" {
		s := &session{xs: xs, spec: spec, lay: layouts[spec.Name]}
		if err := s.attach(); err != nil {
			panic(err)
		}
		defer s.detach()
		if _, err := s.findWindow(30 * time.Second); err != nil {
			panic(err)
		}
	}
	h.fix.waitBeacon(tag, "load", 60*time.Second)
	t0 := time.Now()
	for time.Since(t0) < 40*time.Second {
		m := treeMem(tag)
		fmt.Printf("+%4.0fs  pss %6.1f MiB  rss %6.1f MiB  procs %d\n", time.Since(t0).Seconds(), float64(m.PSSKiB)/1024, float64(m.RSSKiB)/1024, m.Procs)
		time.Sleep(2 * time.Second)
		if time.Since(t0) >= 38*time.Second {
			fmt.Println(m.ByComm)
			for _, pid := range tagged(tag) {
				b, _ := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
				c, _ := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
				if len(c) > 80 {
					c = c[:80]
				}
				fmt.Printf("  pid %d comm=%q kind=%s cmd=%q\n", pid, strings.TrimSpace(string(b)), commOf(pid), c)
			}
		}
	}
}
