package main

import (
	"context"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/sweefbrowser/sweef/internal/app"
	"github.com/sweefbrowser/sweef/internal/app/headless"
	"github.com/sweefbrowser/sweef/internal/shell"
)

// Hooks set by tiny files that import packages owned by other teams, so this
// file (and internal/app) build before those packages exist:
//
//	gui_ui.go     guiAssets = ui.FS
//	gui_shell.go  app.RegisterShell(auto.Open)
//	gui_agent.go  guiAgent = func() app.AgentHost { ... }
var (
	guiAssets fs.FS
	guiAgent  func() app.AgentHost
)

// cmdGUI is `sweef gui`: the browser window, or a headless run that writes
// PNG frames so the whole stack can be checked without a display.
func cmdGUI(args []string) {
	fl := flag.NewFlagSet("gui", flag.ExitOnError)
	headlessF := fl.Bool("headless", false, "run without a display (headless shell)")
	script := fl.String("script", "", "headless: JSON script of input steps and screenshots")
	out := fl.String("out", "", "headless: directory for PNG frames")
	size := fl.String("size", "1280x800", "window size WxH (logical px)")
	scale := fl.Float64("scale", 1, "headless: device pixel ratio")
	profile := fl.String("profile", "", "profile directory (default: per-OS; headless default: in memory)")
	incognito := fl.Bool("incognito", false, "start in incognito (nothing is written to disk)")
	stats := fl.Bool("stats", false, "print frame timing and memory on exit")
	noRestore := fl.Bool("no-restore", false, "do not restore the previous session")
	_ = fl.Parse(args)
	w, h, ok := app.ParseSize(*size)
	if !ok {
		fmt.Fprintln(os.Stderr, "gui: bad --size, want WxH")
		os.Exit(2)
	}
	cfg := app.Config{
		ChromeFS:    guiAssets,
		ProfileDir:  *profile,
		Incognito:   *incognito,
		InitialURLs: fl.Args(),
		NoRestore:   *noRestore,
		Logf:        func(f string, a ...any) { fmt.Fprintf(os.Stderr, "sweef: "+f+"\n", a...) },
	}
	if guiAgent != nil {
		cfg.Agent = guiAgent()
	}
	ctx, cancel := withSignals(context.Background())
	defer cancel()

	if !*headlessF {
		win, err := app.OpenShell(shell.Config{Title: "Sweef", Width: w, Height: h, Incognito: *incognito})
		if err != nil {
			fail(err)
		}
		cfg.Shell = win
		a, err := app.New(cfg)
		if err != nil {
			fail(err)
		}
		runErr := a.Run(ctx)
		if *stats {
			fmt.Print(a.Stats().Format())
		}
		if runErr != nil {
			fail(runErr)
		}
		return
	}

	// headless
	win := headless.New(w, h, *scale)
	cfg.Shell = win
	if *profile == "" {
		cfg.Incognito = true // in-memory: a headless run must not touch the user's profile
	}
	if *out != "" {
		if err := os.MkdirAll(*out, 0o755); err != nil {
			fail(err)
		}
	}
	a, err := app.New(cfg)
	if err != nil {
		fail(err)
	}
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	var scriptErr error
	if *script != "" {
		s, err := app.LoadScript(*script)
		if err != nil {
			a.Close()
			fail(err)
		}
		if s.Size != "" {
			if sw, sh, ok := app.ParseSize(s.Size); ok {
				win.Resize(sw, sh, *scale)
			}
		}
		scriptErr = app.RunScript(a, win, s, *out)
	} else {
		a.WaitIdle(200*time.Millisecond, 60*time.Second)
		if *out != "" {
			scriptErr = win.SavePNG(filepath.Join(*out, "frame.png"))
		}
	}
	if *stats {
		fmt.Print(a.Stats().Format())
	}
	a.Close()
	<-done
	if scriptErr != nil {
		fail(scriptErr)
	}
}
