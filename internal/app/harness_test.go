package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/app/headless"
	"github.com/sweefbrowser/sweef/internal/app/store"
)

type harness struct {
	t    *testing.T
	app  *App
	win  *headless.Window
	done chan error
	dir  string
}

type hopts struct {
	cfg  Config
	w, h int
}

// newHarness runs a full app on a headless window with the placeholder chrome.
func newHarness(t *testing.T, o hopts) *harness {
	t.Helper()
	if o.w == 0 {
		o.w, o.h = 1000, 700
	}
	win := headless.New(o.w, o.h, 1)
	cfg := o.cfg
	cfg.Shell = win
	if cfg.ChromeFS == nil {
		cfg.ChromeFS = PlaceholderFS()
	}
	if cfg.Profile == nil && cfg.ProfileDir == "" {
		cfg.Profile = store.OpenIncognito()
		cfg.NoRestore = true
	}
	if cfg.DownloadsDir == "" {
		cfg.DownloadsDir = t.TempDir()
	}
	cfg.Logf = t.Logf
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, app: a, win: win, done: make(chan error, 1), dir: t.TempDir()}
	go func() { h.done <- a.Run(context.Background()) }()
	t.Cleanup(h.close)
	return h
}

func (h *harness) close() {
	h.app.Close()
	select {
	case <-h.done:
	case <-time.After(15 * time.Second):
		h.t.Error("app did not stop")
	}
}

func (h *harness) idle() {
	h.t.Helper()
	if !h.app.WaitIdle(120*time.Millisecond, 30*time.Second) {
		h.t.Fatal("app never went idle")
	}
}

func (h *harness) shot(name string) string {
	h.t.Helper()
	h.idle()
	dir := os.Getenv("SWEEF_SHOTS")
	if dir == "" {
		dir = h.dir
	}
	_ = os.MkdirAll(dir, 0o755)
	p := filepath.Join(dir, name)
	if err := h.win.SavePNG(p); err != nil {
		h.t.Fatal(err)
	}
	return p
}

func pageServer(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><html><head><title>Home Page</title></head><body style="margin:0;background:#fff">
<div style="height:120px;background:#d33a3a;color:#fff;font:28px sans-serif">Red banner</div>
<p style="font:16px sans-serif">Hello from the home page.</p><a href="/two" id="l">go to two</a></body></html>`)
	})
	mux.HandleFunc("/two", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><html><head><title>Page Two</title></head><body style="margin:0"><div style="height:200px;background:#2f9e44">Green</div></body></html>`)
	})
	mux.HandleFunc("/tall", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><html><head><title>Tall</title></head><body style="margin:0"><div style="height:1000px;background:#2255cc">top</div><div style="height:1000px;background:#cc8822">bottom</div></body></html>`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func (h *harness) waitFor(what string, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.t.Fatalf("timed out waiting for %s", what)
}
