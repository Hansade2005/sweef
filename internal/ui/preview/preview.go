// Package preview renders every Sweef UI page through the real Sweef engine
// (the same Tab and paintPage path that `sweef screenshot` uses) with a mocked
// window.sweef and sample state. It is the design feedback loop and the basis
// of the UI regression test.
package preview

import (
	"embed"
	_ "embed"
	"encoding/base64"
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

// devtoolsMockJS answers the Developer Tools panel's devtools.req calls from fixed fixtures (devtools_mock.js).
//
//go:embed devtools_mock.js
var devtoolsMockJS string

// Scenario is one screenshot: a page, a viewport, a state snapshot and a script.
type Scenario struct {
	Name     string // file stem
	Page     string // path in ui.FS, e.g. chrome/index.html
	Query    string // "?k=v" appended to the URL
	W, H     int
	State    any    // JSON-marshalled and delivered as the "state" message
	Setup    string // JS run after the state arrives (open popovers, stream events...)
	Then     string // JS run after Setup has settled (for steps that need lazily loaded code)
	Backdrop string // fake web page behind the transparent viewport: article|shop|blank
	// BackdropFrom names another scenario whose screenshot is placed in the viewport hole (the engine does not paint iframes)
	BackdropFrom string
	Skip         bool // used only as a backdrop source: not written or tested on its own
	NoState      bool
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

// Session is one loaded page that tests and previews can drive.
type Session struct {
	Tab  *engine.Tab
	sc   Scenario
	errs []string
}

// Open loads the scenario (init mock, navigate, deliver state) without running its Setup.
func Open(e *engine.Engine, base string, sc Scenario, scale float64) (*Session, error) {
	t, err := e.NewTab("")
	if err != nil {
		return nil, err
	}
	t.AddInitScript(mockJS + "\n" + devtoolsMockJS)
	t.SetViewport(sc.W, sc.H, scale, false, false)
	if err := t.Navigate(base+"/"+sc.Page+sc.Query, nil); err != nil {
		return nil, err
	}
	s := &Session{Tab: t, sc: sc}
	if !sc.NoState && sc.State != nil {
		b, _ := json.Marshal(sc.State)
		s.Run(`__send("state", ` + string(b) + `)`)
	}
	s.Settle()
	return s, nil
}

// Run evaluates JS, recording an error if it throws.
func (s *Session) Run(js string) {
	if _, err := s.Tab.Evaluate(js); err != nil {
		s.errs = append(s.errs, "evaluate: "+err.Error())
	}
}

// Eval evaluates an expression and returns its JSON-decoded value.
func (s *Session) Eval(js string) (any, error) {
	raw, err := s.Tab.Evaluate(js)
	if err != nil {
		return nil, err
	}
	var v any
	_ = json.Unmarshal(raw, &v)
	return v, nil
}

// Settle pumps the page's event loop long enough for chained rAF callbacks.
func (s *Session) Settle() {
	for i := 0; i < 8; i++ {
		s.Tab.Settle(60 * time.Millisecond)
	}
}

// Posts returns every message the page has posted to Go so far.
func (s *Session) Posts() [][]interface{} {
	var out [][]interface{}
	if raw, err := s.Tab.Evaluate(`JSON.stringify(window.__posts||[])`); err == nil {
		var str string
		if json.Unmarshal(raw, &str) == nil {
			_ = json.Unmarshal([]byte(str), &out)
		}
	}
	return out
}

// ClearPosts forgets recorded posts.
func (s *Session) ClearPosts() { s.Run(`window.__posts.length = 0`) }

// Errors returns console errors, warnings and uncaught exceptions seen so far.
func (s *Session) Errors() []string {
	out := append([]string(nil), s.errs...)
	for _, ev := range s.Tab.ConsoleLog(0) {
		m, _ := ev.Data.(map[string]string)
		if m["level"] == "error" || m["level"] == "warning" || m["level"] == "warn" {
			out = append(out, m["level"]+": "+m["text"])
		}
	}
	if raw, err := s.Tab.Evaluate(`JSON.stringify(window.__errs||[])`); err == nil {
		var str string
		var errs []string
		if json.Unmarshal(raw, &str) == nil && json.Unmarshal([]byte(str), &errs) == nil {
			out = append(out, errs...)
		}
	}
	return out
}

// PNG screenshots the page at its configured size.
func (s *Session) PNG(scale float64) []byte { return s.Tab.PNGScreenshot(s.sc.W, s.sc.H, scale) }

// Render loads the scenario, runs its Setup and screenshots it at the given scale.
func Render(e *engine.Engine, base string, sc Scenario, scale float64) (*Result, error) {
	s, err := Open(e, base, sc, scale)
	if err != nil {
		return nil, err
	}
	// the result is the screenshot, the errors and the posts: the tab (and its script VM, of which the QuickJS
	// function table has a fixed number of slots) is not needed after it
	defer func() { _ = e.CloseTab(s.Tab.ID) }()
	if sc.Setup != "" {
		s.Run(sc.Setup)
		s.Settle()
	}
	if sc.Then != "" {
		s.Run(sc.Then)
		s.Settle()
	}
	if sc.BackdropFrom != "" {
		for _, o := range Scenarios() {
			if o.Name == sc.BackdropFrom {
				if r, err := Render(e, base, o, scale); err == nil {
					s.Run(`__backdropImg("data:image/png;base64,` + base64.StdEncoding.EncodeToString(r.PNG) + `")`)
				}
			}
		}
	}
	if sc.Backdrop != "" {
		s.Run(`__backdrop(` + jsq(sc.Backdrop) + `)`)
	}
	return &Result{PNG: s.PNG(scale), Errors: s.Errors(), Posts: s.Posts()}, nil
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
		if sc.Skip || (filter != "" && !strings.Contains(sc.Name, filter)) {
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
