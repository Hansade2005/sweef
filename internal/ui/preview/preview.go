// Package preview renders every Sweef UI page through the real Sweef engine
// (the same Tab and paintPage path that `sweef screenshot` uses) with a mocked
// window.sweef and sample state. It is the design feedback loop and the basis
// of the UI regression test.
package preview

import (
	"embed"
	_ "embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sweefbrowser/sweef/internal/engine"
	"github.com/sweefbrowser/sweef/internal/ui"
)

//go:embed mock.js
var mockJS string

// Scenario is one screenshot: a page, a viewport, a state snapshot and a script.
type Scenario struct {
	Name     string // file stem
	Page     string // path in ui.FS, e.g. chrome/index.html
	Query    string // "?k=v" appended to the URL
	W, H     int
	State    any    // JSON-marshalled and delivered as the "state" message
	Setup    string // JS run after the state arrives (open popovers, stream events...)
	Backdrop string // fake web page behind the transparent viewport: article|shop|blank
	NoState  bool
}

// Result of rendering a scenario.
type Result struct {
	PNG    []byte
	Errors []string        // console errors and uncaught exceptions
	Posts  [][]interface{} // messages the page posted to Go
}

//go:embed sheets/*
var sheets embed.FS

// Server serves ui.FS on a loopback port for the engine to fetch from, plus
// the design-system sheets (not part of the shipped UI) under /__sheet/.
func Server() *httptest.Server {
	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(ui.FS)))
	mux.Handle("/__sheet/", http.StripPrefix("/__sheet/", http.FileServer(http.FS(subSheets()))))
	return httptest.NewServer(mux)
}

func subSheets() fs.FS { f, _ := fs.Sub(sheets, "sheets"); return f }

// Engine returns a fresh engine configured for UI rendering.
func Engine() *engine.Engine {
	return engine.New(engine.Options{ViewportWidth: 1280, ViewportHeight: 800, ConsoleRingSize: 400, NetworkRingSize: 200, StateRingSize: 64, CacheDir: "off", SettleMs: 150})
}

// Render loads the scenario and screenshots it at the given scale.
func Render(e *engine.Engine, base string, sc Scenario, scale float64) (*Result, error) {
	t, err := e.NewTab("")
	if err != nil {
		return nil, err
	}
	t.AddInitScript(mockJS)
	t.SetViewport(sc.W, sc.H, scale, false, false)
	if err := t.Navigate(base+"/"+sc.Page+sc.Query, nil); err != nil {
		return nil, err
	}
	res := &Result{}
	run := func(js string) {
		if _, err := t.Evaluate(js); err != nil {
			res.Errors = append(res.Errors, "evaluate: "+err.Error())
		}
	}
	if !sc.NoState && sc.State != nil {
		b, _ := json.Marshal(sc.State)
		run(`__send("state", ` + string(b) + `)`)
	}
	settle := func() {
		for i := 0; i < 4; i++ { // chained rAF callbacks need several pumps
			t.Settle(60 * time.Millisecond)
		}
	}
	settle()
	if sc.Setup != "" {
		run(sc.Setup)
		settle()
	}
	if sc.Backdrop != "" {
		run(`__backdrop(` + jsq(sc.Backdrop) + `)`)
	}
	for _, ev := range t.ConsoleLog(0) {
		m, _ := ev.Data.(map[string]string)
		if m["level"] == "error" || m["level"] == "warning" || m["level"] == "warn" {
			res.Errors = append(res.Errors, m["level"]+": "+m["text"])
		}
	}
	if raw, err := t.Evaluate(`JSON.stringify(window.__errs||[])`); err == nil {
		var errs []string
		var s string
		if json.Unmarshal(raw, &s) == nil {
			_ = json.Unmarshal([]byte(s), &errs)
		}
		res.Errors = append(res.Errors, errs...)
	}
	if raw, err := t.Evaluate(`JSON.stringify(window.__posts||[])`); err == nil {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			_ = json.Unmarshal([]byte(s), &res.Posts)
		}
	}
	res.PNG = t.PNGScreenshot(sc.W, sc.H, scale)
	return res, nil
}

func jsq(s string) string { b, _ := json.Marshal(s); return string(b) }

// Generate renders all scenarios whose name contains filter into outDir at 1x
// (and 2x unless only1x). It returns the files written and any page errors.
func Generate(outDir, filter string, scales []float64, log func(string)) ([]string, error) {
	srv := Server()
	defer srv.Close()
	e := Engine()
	defer e.Close()
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, err
	}
	var files []string
	var bad []string
	for _, sc := range Scenarios() {
		if filter != "" && !strings.Contains(sc.Name, filter) {
			continue
		}
		for _, sf := range scales {
			t0 := time.Now()
			r, err := Render(e, srv.URL, sc, sf)
			if err != nil {
				bad = append(bad, fmt.Sprintf("%s: %v", sc.Name, err))
				continue
			}
			name := sc.Name
			if sf != 1 {
				name += fmt.Sprintf("@%gx", sf)
			}
			p := filepath.Join(outDir, name+".png")
			if err := os.WriteFile(p, r.PNG, 0o644); err != nil {
				return files, err
			}
			files = append(files, p)
			if log != nil {
				log(fmt.Sprintf("%-34s %6d KB  %v", name, len(r.PNG)/1024, time.Since(t0).Round(time.Millisecond)))
			}
			for _, m := range r.Errors {
				bad = append(bad, sc.Name+": "+m)
			}
		}
	}
	if len(bad) > 0 {
		return files, fmt.Errorf("%d page errors:\n  %s", len(bad), strings.Join(bad, "\n  "))
	}
	return files, nil
}
