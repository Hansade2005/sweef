//go:build linux

package main

import (
	"encoding/json"
	"fmt"
	goimage "image"
	"image/color"
	"image/png"
	"os"
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
// benchmark does (typed into the omnibox, from about:blank) and prints what was measured.
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
	r := s.loadPage(h, "", "", false, args[2])
	b, _ := json.MarshalIndent(r, "", " ")
	fmt.Println(string(b))
	tot := s.rec.Snapshot()
	fmt.Printf("recorder: in %d out %d conns %d failed %v plain %v\n", tot.In, tot.Out, tot.Conns, tot.FailedConnects, tot.PlainHosts)
}
