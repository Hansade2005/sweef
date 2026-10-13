//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	winW = 1280
	winH = 800
)

// browserSpec describes how to start one browser. Both are started the same way: same Xvfb, same window size and
// position (0,0, no window manager), a fresh profile directory, the recording proxy in front of the network.
type browserSpec struct {
	Name    string
	Bin     string
	Version string
	// layout: where things are in the 1280x800 window, measured once from screenshots (see docs/vs-chrome.md)
	Lay layout
}

// layout holds the browser-specific geometry that the "outside" measurements need. Everything is in root-window
// coordinates (the window sits at 0,0).
type layout struct {
	KeyROI    rect // the omnibox plus the area its dropdown opens into
	TextStrip rect // just the omnibox text row (used by the detector cross-check)
	Content   rect // the page area (below all browser UI)
	Chrome    rect // all browser UI above the page, for the "first frame with UI" detector
}

// launched is one running browser.
type launched struct {
	spec    *browserSpec
	cmd     *exec.Cmd
	tag     string
	started time.Time // taken immediately before fork/exec
	log     *os.File
	profile string
}

type launchOpts struct {
	Display  string
	Profile  string
	Proxy    string   // recorder address host:port ("" = none)
	URL      string   // initial URL ("" = the browser's own new-tab page)
	MoreURLs []string // further URLs, opened as further tabs
	LogDir   string
	Tag      string
}

// chromiumArgs: everything beyond the defaults is listed in docs/vs-chrome.md.
func chromiumArgs(o launchOpts) []string {
	a := []string{
		"--no-sandbox", // running as root in a container; the sandbox cannot be set up here
		"--test-type",  // only hides the "unsupported command-line flag: --no-sandbox" infobar
		"--user-data-dir=" + o.Profile,
		fmt.Sprintf("--window-size=%d,%d", winW, winH),
		"--window-position=0,0",
		"--no-first-run",
		"--no-default-browser-check",
		"--password-store=basic",
		"--disable-search-engine-choice-screen",
		// Playwright's build has no Google services, but it would still try the network in the background. Turning
		// these off only REMOVES Chromium work, which favours Chromium on the idle-CPU and startup numbers.
		"--disable-background-networking",
		"--disable-sync",
		"--disable-component-update",
		"--disable-default-apps",
		"--disable-breakpad",
		"--metrics-recording-only",
		"--no-pings",
	}
	if o.Proxy != "" {
		a = append(a, "--proxy-server=http://"+o.Proxy)
	}
	if extra := strings.Fields(os.Getenv("VSCHROME_CHROMIUM_FLAGS")); len(extra) > 0 {
		a = append(a, extra...)
	}
	if o.URL != "" {
		a = append(a, o.URL)
	}
	a = append(a, o.MoreURLs...)
	return a
}

func sweefArgs(o launchOpts) []string {
	a := []string{"gui", "--profile", o.Profile, "--no-restore", "--size", fmt.Sprintf("%dx%d", winW, winH)}
	if o.URL != "" {
		a = append(a, o.URL)
	}
	a = append(a, o.MoreURLs...)
	return a
}

func (b *browserSpec) launch(o launchOpts) (*launched, error) {
	var args []string
	if b.Name == "chromium" {
		args = chromiumArgs(o)
	} else {
		args = sweefArgs(o)
	}
	cmd := exec.Command(b.Bin, args...)
	env := []string{}
	for _, kv := range os.Environ() {
		k := kv[:strings.IndexByte(kv+"=", '=')]
		switch strings.ToUpper(k) {
		case "DISPLAY", "HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "ALL_PROXY", "BENCH_TAG", "XAUTHORITY", "WAYLAND_DISPLAY":
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "GOOGLE_API_KEY=no", "GOOGLE_DEFAULT_CLIENT_ID=no", "GOOGLE_DEFAULT_CLIENT_SECRET=no") // hides the "API keys are missing" infobar
	env = append(env, "DISPLAY="+o.Display, "BENCH_TAG="+o.Tag, "NO_PROXY=localhost,127.0.0.1,::1")
	if o.Proxy != "" {
		env = append(env, "HTTPS_PROXY=http://"+o.Proxy, "HTTP_PROXY=http://"+o.Proxy)
	}
	cmd.Env = env
	lf, _ := os.Create(filepath.Join(o.LogDir, fmt.Sprintf("%s-%s.log", b.Name, o.Tag)))
	cmd.Stdout, cmd.Stderr = lf, lf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	l := &launched{spec: b, cmd: cmd, tag: o.Tag, log: lf, profile: o.Profile}
	l.started = time.Now()
	if err := cmd.Start(); err != nil {
		lf.Close()
		return nil, err
	}
	setTagPGID(o.Tag, cmd.Process.Pid)
	return l, nil
}

// stop kills every process of the tree (also ones re-parented away from the main process) and reaps the child.
func (l *launched) stop() {
	killTagged(l.tag)
	if l.cmd.Process != nil {
		syscall.Kill(-l.cmd.Process.Pid, syscall.SIGKILL)
		l.cmd.Wait()
	}
	killTagged(l.tag)
	if l.log != nil {
		l.log.Close()
	}
}

func (l *launched) alive() bool {
	return l.cmd.ProcessState == nil && len(tagged(l.tag)) > 0
}
